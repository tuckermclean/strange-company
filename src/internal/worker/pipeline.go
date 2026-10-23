package worker

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"strange.company/sc/internal/worker/adapters"
)

// The Git pipeline of sc-worker after the credential check: clone, checkout, context, harness, claim, pre-checks,
// commit, push and the result (plan 27 sections 14.3 to 14.13).

type stageFail struct {
	reason, detail string
	partial        bool // post-harness failure: the partial diff is written to the log
}

type contextSection struct {
	ID            string `json:"id"`
	Kind          string `json:"kind"`
	Authority     string `json:"authority"`
	MediaType     string `json:"media_type"`
	MaterializeAs string `json:"materialize_as"`
	Content       string `json:"content"`
	Truncated     bool   `json:"truncated"`
}

type fullPolicy struct {
	ProtectedPaths     []string `json:"protected_paths"`
	DiffMode           string   `json:"diff_mode"`
	DiffAllow          []string `json:"diff_allow"`
	DiffDeny           []string `json:"diff_deny"`
	MaxFilesChanged    int      `json:"max_files_changed"`
	MaxDiffBytes       int      `json:"max_diff_bytes"`
	AllowDeletions     bool     `json:"allow_deletions"`
	ValidationCommands []struct {
		Name           string   `json:"name"`
		Argv           []string `json:"argv"`
		TimeoutSeconds int      `json:"timeout_seconds"`
	} `json:"validation_commands"`
}

// fullPacket is the part of worker-input.schema.yaml that the pipeline reads.
type fullPacket struct {
	TaskRaw   json.RawMessage         `json:"task"`
	Task      struct{ TaskID string } `json:"-"`
	Execution struct {
		ExecutionID string `json:"execution_id"`
		Attempt     int    `json:"attempt"`
		Role        string `json:"role"`
	} `json:"execution"`
	Git struct {
		CloneURL    string `json:"clone_url"`
		FetchRef    string `json:"fetch_ref"`
		BaseCommit  string `json:"base_commit"`
		StartCommit string `json:"start_commit"`
		Branch      string `json:"branch"`
		BranchMode  string `json:"branch_mode"`
		PushAllowed bool   `json:"push_allowed"`
		Commit      struct {
			SubjectPrefix string            `json:"subject_prefix"`
			AuthorName    string            `json:"author_name"`
			AuthorEmail   string            `json:"author_email"`
			Trailers      map[string]string `json:"trailers"`
		} `json:"commit"`
	} `json:"git"`
	Policy fullPolicy `json:"policy"`
	Limits struct {
		HarnessTimeoutSeconds int `json:"harness_timeout_seconds"`
		TermGraceSeconds      int `json:"term_grace_seconds"`
		MaxHandoffBytes       int `json:"max_handoff_bytes"`
		MaxLogBytes           int `json:"max_log_bytes"`
	} `json:"limits"`
	Context struct {
		Sections json.RawMessage `json:"sections"`
		Digest   string          `json:"digest"`
	} `json:"context"`
	Harness struct {
		Adapter              string `json:"adapter"`
		CLIVersion           string `json:"cli_version"`
		DisableWorkerTimeout bool   `json:"disable_worker_timeout"`
	} `json:"harness"`
	ScopedACs []string `json:"-"`
}

type pipeline struct {
	w       *worker
	p       fullPacket
	repo    string
	keyPath string
	lease   string
	priv    bool // the private GIT_DIR is set up

	restored, doneWritten, harnessRan bool
	reqN                              int
	contextNames                      []string
	env                               map[string]string
	hs                                harnessState
	obuf                              []byte // harness lines not yet written to stdout (see flushOut)
	hout                              shimOutcome
	verdict                           string
	claim                             *claimDoc
	validation                        []any
	skipPrechecks                     bool
	refused                           map[string]bool // paths already reported as symlink_refused

	cloneEnd, harnessStart, harnessEnd time.Time
}

func (w *worker) runPipeline() int {
	pl := &pipeline{w: w, repo: filepath.Join(w.cfg.WorkspaceDir, "repo"), validation: []any{}}
	if err := json.Unmarshal(w.pktRaw, &pl.p); err != nil {
		return w.report(w.failedResult("internal", "packet decode: "+oneLine(err.Error())), modeNormal)
	}
	var task struct {
		TaskID string   `json:"task_id"`
		Scoped []string `json:"scoped_acceptance_criteria"`
	}
	_ = json.Unmarshal(pl.p.TaskRaw, &task)
	pl.p.Task.TaskID, pl.p.ScopedACs = task.TaskID, task.Scoped
	if k, err := adapters.DeriveFakeKnobs(w.pktRaw); err == nil {
		pl.skipPrechecks = k.SkipPrechecks
	}
	defer func() {
		if pl.keyPath != "" {
			_ = os.Remove(pl.keyPath)
		}
		pl.shimDone()
	}()
	return pl.run()
}

func (pl *pipeline) run() int {
	w := pl.w
	g := pl.p.Git
	w.setPhase("cloning")
	if f := pl.setupCredentials(); f != nil {
		return pl.finishFail(f)
	}
	if f := pl.clone(); f != nil {
		return pl.finishFail(f)
	}
	pl.cloneEnd = w.clk.Now()
	if w.sig.Err() != nil {
		return w.terminate()
	}
	if f := pl.checkout(); f != nil {
		return pl.finishFail(f)
	}
	w.setPhase("materializing")
	if f := pl.materialize(); f != nil {
		return pl.finishFail(f)
	}
	if w.sig.Err() != nil {
		return w.terminate()
	}

	w.setPhase("harness")
	verdict, f := pl.runHarness()
	if f != nil {
		return pl.finishFail(f)
	}
	pl.verdict = verdict
	if w.sig.Err() != nil {
		return w.terminate()
	}
	pl.scanNotes()
	w.setPhase("validating")
	if verdict != "ok" {
		pl.shimDone()
		detail := fmt.Sprintf("exit_code=%d", pl.hout.res.ExitCode)
		if verdict == "harness_stream_incomplete" {
			detail = "stream: no terminal event"
		}
		return pl.finishFail(&stageFail{reason: verdict, detail: detail, partial: true})
	}
	claim, why := pl.readClaim()
	if claim == nil {
		pl.shimDone()
		return pl.finishFail(&stageFail{reason: "malformed_harness_output", detail: "claim: " + why, partial: true})
	}
	pl.claim = claim
	commitState := claim.State == "completed" || claim.State == "already_satisfied"
	if g.PushAllowed && commitState && len(pl.p.Policy.ValidationCommands) > 0 {
		pl.validation = pl.validate()
	}
	pl.shimDone()
	if w.sig.Err() != nil {
		return w.terminate()
	}
	if claim.State == "failed" {
		return pl.finishFail(&stageFail{reason: "harness_error", detail: "claim_failed: the harness reported state failed", partial: true})
	}
	if !commitState {
		if g.PushAllowed {
			pl.partialDiff()
		}
		return pl.finishClaim("", false)
	}
	if !g.PushAllowed {
		return pl.finishClaim("", false)
	}
	commit, f := pl.commitAndPush()
	if f != nil {
		return pl.finishFail(f)
	}
	return pl.finishClaim(commit, true)
}

// commitAndPush is plan 27 sections 14.10 and 14.11.
func (pl *pipeline) commitAndPush() (string, *stageFail) {
	w := pl.w
	g := pl.p.Git
	w.setPhase("committing")
	if f := pl.restoreInternals(); f != nil {
		return "", f
	}
	if head := pl.harnessHead(); head != "" && head != g.StartCommit {
		if r := pl.gitPost(30*time.Second, nil, "merge-base", "--is-ancestor", g.StartCommit, head); r.err != nil {
			return "", &stageFail{reason: "diff_precheck_failed", detail: "head_not_descendant", partial: true}
		}
		count := pl.gitPost(30*time.Second, nil, "rev-list", "--count", g.StartCommit+".."+head).text()
		var n int
		fmt.Sscanf(count, "%d", &n)
		w.log("info", "harness_commits_folded", map[string]any{"count": n})
	}
	if r := pl.gitPost(120*time.Second, nil, "add", "-A", "--", ":/"); r.err != nil {
		return "", &stageFail{reason: "commit_failed", detail: "git add: " + tail(r.stderr, 500)}
	}
	ns := pl.gitPost(60*time.Second, nil, "diff", "--cached", "--name-status", "-M", "-z", g.StartCommit)
	if ns.err != nil {
		return "", &stageFail{reason: "commit_failed", detail: "git diff: " + tail(ns.stderr, 500)}
	}
	changes := parseNameStatus(ns.stdout)
	patch := pl.gitPost(120*time.Second, nil, "diff", "--cached", "--no-ext-diff", "--no-textconv", "--text", "-M", g.StartCommit)
	if patch.err != nil {
		return "", &stageFail{reason: "commit_failed", detail: "git diff: " + tail(patch.stderr, 500)}
	}
	// The context directory is a sibling of the working tree; nothing staged may name it.
	ctxNames := map[string]bool{}
	for _, n := range pl.contextNames {
		ctxNames[n] = true
	}
	for _, c := range changes {
		if strings.HasPrefix(c.path, "../") || ctxNames[c.path] {
			return "", &stageFail{reason: "diff_precheck_failed", detail: "context_leak: " + c.path, partial: true}
		}
	}
	if !pl.skipPrechecks {
		if hits := secretPaths(w.secrets, string(patch.stdout)); len(hits) > 0 {
			return "", &stageFail{reason: "secret_detected", detail: "secret pattern in added lines of: " + strings.Join(hits, ", ")}
		}
		if v := runPrechecks(pl.p.Policy, changes, patch.total, true); len(v) > 0 {
			parts := make([]string, len(v))
			for i, x := range v {
				parts[i] = x.String()
			}
			return "", &stageFail{reason: "diff_precheck_failed", detail: strings.Join(parts, "; "), partial: true}
		}
	}

	// Commit.
	msgPath := w.privPath("COMMIT_MSG")
	if err := os.WriteFile(msgPath, []byte(pl.commitMessage()), 0o600); err != nil {
		return "", &stageFail{reason: "commit_failed", detail: "message file: " + err.Error()}
	}
	when := w.clk.Now().UTC().Format(time.RFC3339)
	c := g.Commit
	ident := []string{
		"GIT_AUTHOR_NAME=" + c.AuthorName, "GIT_AUTHOR_EMAIL=" + c.AuthorEmail, "GIT_AUTHOR_DATE=" + when,
		"GIT_COMMITTER_NAME=" + c.AuthorName, "GIT_COMMITTER_EMAIL=" + c.AuthorEmail, "GIT_COMMITTER_DATE=" + when,
	}
	if r := pl.gitPost(120*time.Second, ident, "commit", "-q", "--no-verify", "--cleanup=verbatim", "-F", msgPath); r.err != nil {
		return "", &stageFail{reason: "commit_failed", detail: "git commit: " + tail(r.stderr, 500)}
	}
	if n := pl.gitPost(30*time.Second, nil, "rev-list", "--count", g.StartCommit+"..HEAD").text(); n != "1" {
		return "", &stageFail{reason: "commit_failed", detail: "expected one commit on top of start_commit, found " + n}
	}
	if p := pl.gitPost(30*time.Second, nil, "rev-parse", "HEAD^").text(); p != g.StartCommit {
		return "", &stageFail{reason: "commit_failed", detail: "the commit's parent is not start_commit"}
	}
	sha := pl.gitPost(30*time.Second, nil, "rev-parse", "HEAD").text()

	// Push.
	w.setPhase("pushing")
	ref := "refs/heads/" + g.Branch
	var last gitOut
	for attempt := 0; ; attempt++ {
		last = pl.gitPost(120*time.Second, nil, "push", "--porcelain", "--no-verify", "origin", pl.lease, "HEAD:"+ref)
		if last.err == nil {
			break
		}
		out := string(last.stdout) + last.stderr
		if strings.Contains(out, "rejected") || strings.Contains(out, "stale info") || strings.Contains(out, "non-fast-forward") {
			return "", &stageFail{reason: "push_rejected", detail: tail(out, 1024)}
		}
		if attempt >= 3 || w.sig.Err() != nil {
			return "", &stageFail{reason: "push_rejected", detail: tail(out, 1024)}
		}
		_ = w.clk.Sleep(w.sig, time.Duration(2<<attempt)*time.Second)
	}
	remote, err := pl.remoteHead(ref)
	if err != nil || remote != sha {
		return "", &stageFail{reason: "push_rejected", detail: "remote_head_mismatch"}
	}
	return sha, nil
}

// commitMessage builds the message of plan 27 section 14.10 step 5.
func (pl *pipeline) commitMessage() string {
	g := pl.p.Git
	clean := func(s string) string {
		return strings.Map(func(r rune) rune {
			if r == '\n' || r == '\t' {
				return r
			}
			if unicode.IsControl(r) {
				return -1
			}
			return r
		}, pl.w.scrub.String(s))
	}
	summary := clean(pl.claim.Summary)
	first := strings.TrimSpace(strings.SplitN(strings.NewReplacer("\t", " ").Replace(summary), "\n", 2)[0])
	room := 72 - len([]rune(g.Commit.SubjectPrefix))
	if room < 1 {
		room = 1
	}
	if r := []rune(first); len(r) > room {
		first = strings.TrimSpace(string(r[:room]))
	}
	if first == "" {
		first = "update"
	}
	var b strings.Builder
	b.WriteString(g.Commit.SubjectPrefix + first + "\n\n")
	body := clip(strings.TrimSpace(summary), 2000)
	b.WriteString(wrap(body, 72) + "\n\n")
	if len(pl.p.ScopedACs) > 0 {
		b.WriteString("Acceptance criteria: " + strings.Join(pl.p.ScopedACs, ", ") + "\n\n")
	}
	for _, k := range []string{"SC-Task", "SC-Execution", "SC-Role", "SC-Base"} {
		b.WriteString(k + ": " + strings.NewReplacer("\n", " ", "\r", " ").Replace(g.Commit.Trailers[k]) + "\n")
	}
	return b.String()
}

// wrap word-wraps each line of s at width columns.
func wrap(s string, width int) string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		cur := ""
		for _, word := range strings.Fields(line) {
			switch {
			case cur == "":
				cur = word
			case len([]rune(cur))+1+len([]rune(word)) <= width:
				cur += " " + word
			default:
				out = append(out, cur)
				cur = word
			}
		}
		out = append(out, cur)
	}
	return strings.Join(out, "\n")
}

// partialDiff writes the scrubbed patch of the unpushed work to the log (plan 27 section 14.13) and returns its
// diffstat for failure.detail.
func (pl *pipeline) partialDiff() string {
	w := pl.w
	g := pl.p.Git
	if pl.restoreInternals() != nil {
		return ""
	}
	if r := pl.gitPost(120*time.Second, nil, "add", "-A", "--", ":/"); r.err != nil {
		return ""
	}
	if r := pl.gitPost(60*time.Second, nil, "diff", "--cached", "--quiet", g.StartCommit); r.err == nil || r.code != 1 {
		return ""
	}
	d := pl.gitPost(120*time.Second, nil, "diff", "--cached", "--no-ext-diff", "--no-textconv", g.StartCommit)
	if d.err != nil {
		return ""
	}
	raw := d.stdout
	if d.total > len(d.stdout) {
		// The capture was cut: its last line is unterminated and may hold half a secret that no filter could
		// recognise, so it is dropped.
		raw = raw[:bytes.LastIndexByte(raw, '\n')+1]
	}
	text, dropped := textLinesOnly(string(raw))
	// Scrub the whole captured text first and cut afterwards, so that a secret across the cut is redacted; the cut
	// never splits a rune. Control characters are made visible.
	text = escapeControls(w.scrub.String(text))
	const limit = 1 << 20
	truncated := len(text) > limit || d.total > len(d.stdout)
	if len(text) > limit {
		text = text[:utf8Boundary(text, limit)]
	}
	w.logMu.Lock()
	fmt.Fprintf(w.out, "::SC-PARTIAL-DIFF-BEGIN::\n%s", text)
	if !strings.HasSuffix(text, "\n") {
		fmt.Fprintln(w.out)
	}
	if dropped > 0 {
		fmt.Fprintf(w.out, "[%d non-text lines dropped by sc-worker]\n", dropped)
	}
	if truncated {
		fmt.Fprintln(w.out, "[diff truncated by sc-worker]")
	}
	fmt.Fprintln(w.out, "::SC-PARTIAL-DIFF-END::")
	w.logMu.Unlock()
	ns := pl.gitPost(60*time.Second, nil, "diff", "--cached", "--numstat", g.StartCommit)
	var parts []string
	for _, l := range strings.Split(ns.text(), "\n") {
		if f := strings.Split(l, "\t"); len(f) == 3 {
			parts = append(parts, fmt.Sprintf("%s +%s -%s", f[2], f[0], f[1]))
		}
	}
	return w.scrub.String(strings.Join(parts, "; "))
}

// decorate adds the harness facts, validation, timings and handoff to a result document.
func (pl *pipeline) decorate(doc map[string]any) {
	w := pl.w
	now := w.clk.Now()
	t := map[string]any{"started_at": w.stamp(w.started), "finished_at": w.stamp(now)}
	if !pl.cloneEnd.IsZero() {
		t["clone_finished_at"] = w.stamp(pl.cloneEnd)
	}
	if pl.harnessRan {
		t["harness_started_at"], t["harness_finished_at"] = w.stamp(pl.harnessStart), w.stamp(pl.harnessEnd)
	}
	doc["timings"] = t
	if !pl.harnessRan {
		return
	}
	hr, _ := doc["harness_report"].(map[string]any)
	if hr == nil {
		hr = map[string]any{}
	}
	code := pl.hout.res.ExitCode
	if !pl.hout.haveRes {
		code = -1
	}
	hr["exit_code"], hr["stream_complete"], hr["terminal_event_seen"] = code, true, pl.hs.terminal
	if s, ok := pl.hout.res.Signal.(string); ok && (s == "SIGTERM" || s == "SIGKILL") {
		hr["signal"] = s
	}
	if pl.hs.terminal {
		hr["is_error"], hr["num_turns"] = pl.hs.isError, pl.hs.numTurns
	}
	doc["harness_report"] = hr
	doc["validation"] = pl.validation
	if pl.claim != nil {
		doc["handoff"] = pl.claim.raw["handoff"]
		return
	}
	verdict := pl.verdict
	if verdict == "" {
		verdict = "no_valid_claim"
	}
	doc["handoff"] = map[string]any{
		"what_i_did":            "sc-worker: harness ended with " + verdict + "; no valid claim",
		"what_remains":          pl.p.Execution.Role + " work for " + pl.p.Task.TaskID + " is incomplete",
		"recommended_next_role": nil,
		"questions":             []any{},
		"known_risks":           []any{verdict},
	}
}

// finishFail reports a failed result for f.
func (pl *pipeline) finishFail(f *stageFail) int {
	w := pl.w
	if w.sig.Err() != nil {
		pl.shimDone()
		return w.terminate()
	}
	detail := f.detail
	if f.partial && pl.harnessRan {
		if st := pl.partialDiff(); st != "" {
			detail = clip(detail, 1200) + " | diffstat: " + st
		}
	}
	w.log("error", "pipeline_failed", map[string]any{"reason": f.reason, "detail": clip(detail, 400)})
	doc := w.failedResult(f.reason, detail)
	pl.decorate(doc)
	return w.report(doc, modeNormal)
}

// finishClaim reports the result for a usable claim.
func (pl *pipeline) finishClaim(commit string, pushed bool) int {
	w := pl.w
	c := pl.claim
	doc := w.failedResult("internal", "")
	delete(doc, "failure")
	doc["state"] = c.State
	doc["summary"] = c.Summary
	if ro, ok := c.raw["role_output"]; ok {
		doc["role_output"] = ro
	}
	if cf, ok := c.raw["conflict"]; ok {
		doc["conflict"] = cf
	}
	if pushed {
		doc["commit"], doc["pushed"] = commit, true
	}
	if !pl.p.Git.PushAllowed {
		doc["branch"] = nil
	} else {
		doc["branch"] = pl.p.Git.Branch
	}
	pl.decorate(doc)
	return w.report(doc, modeNormal)
}

// textLinesOnly drops every line holding a NUL byte or invalid UTF-8, so the logged partial diff stays binary-free,
// and returns the number of lines it dropped.
func textLinesOnly(s string) (string, int) {
	lines := strings.SplitAfter(s, "\n")
	out := lines[:0]
	dropped := 0
	for _, l := range lines {
		if strings.IndexByte(l, 0) >= 0 || !utf8.ValidString(l) {
			dropped++
			continue
		}
		out = append(out, l)
	}
	return strings.Join(out, ""), dropped
}
