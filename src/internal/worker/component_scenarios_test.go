package worker_test

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Slice S6a of F07: 28-AC-4 and 28-AC-7. Every scenario of plan 28 section 14.4 marked coop or wedged, plus
// malformed-result, malformed-handoff and push-no-result, runs through the real sc-worker pipeline, the shim and the
// real fake harness against the stub Runner and a stub Gateway. The expectations are what sc-worker posts (plan 27
// sections 14.9 to 14.15); the Runner-side columns of the plan 28 table (execution state, class, code, task effect)
// belong to C02 and are not asserted.
//
// Judgement calls where the plan text is not explicit (reported in the slice handback):
//   - permission-denial: sc-worker posts state completed and carries the denial in harness_report.permission_denials
//     (the Runner turns it into the PERMISSION_DENIAL violation);
//   - over-budget / spend-loop: the gateway code the harness prints (SC_BUDGET_EXHAUSTED) makes the verdict
//     budget_denied (plan 27 section 14.7 step 6), so the result is failed/budget_denied;
//   - malformed-stream: failed/harness_stream_incomplete (plan 27 section 14.9, detail prefix "stream:");
//   - claim-missing: failed/malformed_harness_output with detail prefix "claim:";
//   - the scripted fixture commits of the G5 scenarios are the fake's own business; only the posted result is asserted.

// requiredScenarios are the scenarios of plan 28 section 14.4 marked coop or wedged, plus the three knob scenarios of
// 28-AC-4. deadline-exceeded (class: disableWorkerTimeout) is neither and is not a row.
var requiredScenarios = []string{
	"success", "failure", "timeout", "blocked", "push", "push-no-result", "malformed-result", "malformed-handoff",
	"malformed-stream", "claim-missing", "red-valid", "red-broken-test", "already-satisfied", "test-spec-conflict",
	"green-after-retry", "forge-then-push", "phase-conditional-test", "permission-denial", "over-budget", "spend-loop",
	"model-switch", "ignore-cancel", "green", "red-compile-error", "red-with-stub", "red-valid-v2", "review-approve",
	"review-request-changes", "review-reject", "adjudicate-test-valid", "adjudicate-test-invalid", "judge-docs",
	"produce-docs",
}

// rowCtx is what a row's extra check sees.
type rowCtx struct {
	fx  *fixture
	gr  *gitRun
	res map[string]any
	red string // the red commit the branch started from (implementer and continue rows), else ""
}

type scenarioRow struct {
	scenario string
	label    string // sub-test name (default: scenario)
	role     string // default tester
	purpose  string // reviewer purpose
	attempt  int    // default 1
	cont     bool   // tester in continue mode on top of a red commit
	policy   map[string]any
	object   string // task.objective override
	evidence bool   // add a prior_attempt_evidence context section

	gateway      func(n int, model string) (int, string)
	resultStatus int // stub Runner answer to the result POST (default 202)

	state, reason, detail string // reason "" means failure is null; detail is a prefix
	exit                  int
	noResult              bool
	pushed                bool
	anyPush               bool // malformed rows: the pushed/commit fields are not specified
	wantGateway           int  // exact number of Gateway requests (0: at least one)
	check                 func(t *testing.T, c *rowCtx)
}

var denylistPolicy = map[string]any{
	"diff_mode": "denylist", "diff_allow": []any{}, "diff_deny": []any{".strange-company/**", ".woodpecker/**", "AGENTS.md"},
}

func pathsOf(t *testing.T, fx *fixture, commit string) []string {
	t.Helper()
	out := gitIn(t, fx.bare, "diff", "--name-only", commit+"^", commit)
	if out == "" {
		return nil
	}
	return strings.Split(out, "\n")
}

func roleOutput(res map[string]any) map[string]any {
	m, _ := res["role_output"].(map[string]any)
	return m
}

func buildRowPacket(t *testing.T, fx *fixture, r scenarioRow) (pkt map[string]any, red string) {
	t.Helper()
	o := pktOpts{role: r.role, policy: r.policy}
	switch {
	case r.role == "implementer" || r.cont:
		red = fx.redCommit()
		fx.setBranch("agent/sc-1", red)
		o.mode, o.start, o.fetchRef = "continue", red, "refs/heads/agent/sc-1"
	case r.role == "reviewer" || r.role == "researcher" || r.role == "planner":
		o.mode = "readonly"
	}
	pkt = gitPacket(t, fx, o)
	pkt["harness"].(map[string]any)["fake_scenario"] = r.scenario
	exec := pkt["execution"].(map[string]any)
	if r.attempt > 0 {
		exec["attempt"] = r.attempt
	}
	if r.purpose != "" {
		exec["purpose"] = r.purpose
	}
	if r.object != "" {
		pkt["task"].(map[string]any)["objective"] = r.object
	}
	if r.evidence {
		ctx := pkt["context"].(map[string]any)
		sections := ctx["sections"].([]any)
		sections = append(sections, jsonRound(t, map[string]any{
			"id": "prior-attempt-evidence", "kind": "prior_attempt_evidence", "layer": "task", "authority": "evidence",
			"title": "Prior attempts", "media_type": "text/markdown", "materialize_as": "30-prior-attempt-evidence.md",
			"truncated": false, "original_bytes": 36, "content": "Attempt 1: Add returned a*b (GREEN failed).",
			"sources": []any{map[string]any{"kind": "task_record", "ref": "sc-1", "digest": "sha256:" + strings.Repeat("d", 64)}},
		}))
		ctx["sections"] = sections
		sum := sha256.Sum256(jcsBytes(t, sections))
		ctx["digest"] = "sha256:" + hex.EncodeToString(sum[:])
	}
	validate(t, "worker-input", pkt)
	return pkt, red
}

// rowSem bounds the number of rows that run at once (each is an sc-worker process, a shim and several git commands).
var rowSem = make(chan struct{}, 6)

func runRow(t *testing.T, r scenarioRow) {
	t.Helper()
	rowSem <- struct{}{}
	defer func() { <-rowSem }()
	if r.role == "" {
		r.role = "tester"
	}
	fx := buildFixture(t, false)
	pkt, red := buildRowPacket(t, fx, r)
	sp := gitSpec{fx: fx, pkt: pkt, gateway: r.gateway}
	if r.resultStatus != 0 {
		sp.stub = func(s *stubRunner) {
			s.mu.Lock()
			defer s.mu.Unlock()
			s.resultStatus = r.resultStatus
		}
	}
	gr := runGit(t, sp)
	code := waitDone(t, gr.proc, "start")
	gr.cancelShim()
	<-gr.shimDone

	if code != r.exit {
		t.Errorf("exit code = %d, want %d; stderr:\n%s", code, r.exit, gr.proc.stderr.String())
	}
	if hits := int(gr.gwHits.Load()); hits < 1 {
		t.Errorf("the scenario sent %d requests to SC_FAKE_GATEWAY_URL, want at least 1 (28-AC-7)", hits)
	} else if r.wantGateway > 0 && hits != r.wantGateway {
		t.Errorf("the scenario sent %d requests to SC_FAKE_GATEWAY_URL, want exactly %d (28-AC-7)", hits, r.wantGateway)
	}
	posts := gr.stub.calls("POST", "/v1/worker/result")
	if r.noResult {
		if len(posts) != 0 {
			t.Errorf("%d result POSTs, want none (documented absence of a result)", len(posts))
		}
		if n := countLines(gr.proc.stdout.String(), "::SC-RESULT-BEGIN::"); n != 0 {
			t.Errorf("stdout carries %d framed result blocks, want none", n)
		}
		if r.pushed && fx.head("agent/sc-1") == "" {
			t.Errorf("the branch agent/sc-1 was not pushed")
		}
		return
	}
	if len(posts) != 1 {
		t.Fatalf("%d result POSTs, want 1; stderr:\n%s", len(posts), gr.proc.stderr.String())
	}
	checkFramed(t, gr.proc.stdout.String(), posts[0].Body)
	res := decodeResult(t, posts[0].Body)
	if res["state"] != r.state {
		t.Errorf("state = %v, want %s (failure %v)", res["state"], r.state, res["failure"])
	}
	reason, detail := failureReason(res)
	if reason != r.reason || !strings.HasPrefix(detail, r.detail) {
		t.Errorf("failure.reason = %q (detail %q), want %q (detail prefix %q)", reason, detail, r.reason, r.detail)
	}
	if !r.anyPush { // the malformed-* rows only specify the invalid field
		if res["pushed"] != r.pushed {
			t.Errorf("pushed = %v, want %v", res["pushed"], r.pushed)
		}
		if head := fx.head("agent/sc-1"); r.pushed {
			if c, _ := res["commit"].(string); c == "" || c != head {
				t.Errorf("commit = %v, want the remote branch head %q", res["commit"], head)
			}
		} else if res["commit"] != nil {
			t.Errorf("commit = %v, want null", res["commit"])
		}
	}
	if r.check != nil {
		r.check(t, &rowCtx{fx: fx, gr: gr, res: res, red: red})
	}
}

func names(res map[string]any, key string) []string {
	var out []string
	if l, ok := res[key].([]any); ok {
		for _, v := range l {
			if s, ok := v.(string); ok {
				out = append(out, s)
			}
		}
	}
	return out
}

// commitOf is the posted commit; a result without one fails the row instead of panicking the whole test binary.
func commitOf(t *testing.T, c *rowCtx) string {
	t.Helper()
	s, _ := c.res["commit"].(string)
	if s == "" {
		t.Fatalf("the result has no commit (state %v, pushed %v)", c.res["state"], c.res["pushed"])
	}
	return s
}

func tooling(t *testing.T, c *rowCtx) []string {
	t.Helper()
	return pathsOf(t, c.fx, commitOf(t, c))
}

func allTests(files []string) bool {
	for _, f := range files {
		if !strings.HasSuffix(f, "_test.go") {
			return false
		}
	}
	return len(files) > 0
}

func commitHas(t *testing.T, c *rowCtx, needle string, testOnly bool) bool {
	t.Helper()
	commit := commitOf(t, c)
	for _, f := range pathsOf(t, c.fx, commit) {
		if testOnly != strings.HasSuffix(f, "_test.go") {
			continue
		}
		if strings.Contains(gitIn(t, c.fx.bare, "show", commit+":"+f), needle) {
			return true
		}
	}
	return false
}

func handoffOf(res map[string]any) map[string]any {
	h, _ := res["handoff"].(map[string]any)
	return h
}

func reviewerRow(scenario, label, purpose string, check func(t *testing.T, ro map[string]any)) scenarioRow {
	return scenarioRow{scenario: scenario, label: label, role: "reviewer", purpose: purpose, state: "completed",
		check: func(t *testing.T, c *rowCtx) {
			ro := roleOutput(c.res)
			if ro["kind"] != "reviewer" {
				t.Errorf("role_output.kind = %v, want reviewer", ro["kind"])
			}
			check(t, ro)
		}}
}

func scenarioRows() []scenarioRow {
	exhausted := func(after int) func(int, string) (int, string) {
		return func(n int, _ string) (int, string) {
			if n > after {
				return 403, "SC_BUDGET_EXHAUSTED"
			}
			return 200, ""
		}
	}
	return []scenarioRow{
		// success, one row per role.
		{scenario: "success", label: "success-tester", state: "completed", pushed: true},
		{scenario: "success", label: "success-implementer", role: "implementer", state: "completed", pushed: true,
			check: func(t *testing.T, c *rowCtx) {
				if got := gitIn(t, c.fx.bare, "rev-parse", commitOf(t, c)+"^"); got != c.red {
					t.Errorf("parent of the pushed commit = %s, want the red commit %s", got, c.red)
				}
			}},
		reviewerRow("success", "success-reviewer", "final_review", func(t *testing.T, ro map[string]any) {
			if ro["verdict"] != "approve" {
				t.Errorf("verdict = %v, want approve", ro["verdict"])
			}
			checks, _ := ro["checks"].([]any)
			if len(checks) != 5 {
				t.Errorf("%d checks, want 5", len(checks))
			}
			for _, ck := range checks {
				if m, _ := ck.(map[string]any); m["result"] != "pass" {
					t.Errorf("check %v is not pass", ck)
				}
			}
		}),
		{scenario: "success", label: "success-researcher", role: "researcher", state: "completed",
			check: func(t *testing.T, c *rowCtx) {
				ro := roleOutput(c.res)
				if s, _ := ro["report_markdown"].(string); ro["kind"] != "research_report" || strings.TrimSpace(s) == "" {
					t.Errorf("role_output = %v, want a research_report with a report", ro)
				}
				if _, ok := ro["context_proposals"]; ok {
					t.Errorf("context_proposals present without the [fake:context-proposal] marker")
				}
			}},
		{scenario: "success", label: "success-researcher-context-proposal", role: "researcher", state: "completed",
			object: "Investigate the redirect handler [fake:context-proposal]",
			check: func(t *testing.T, c *rowCtx) {
				props, _ := roleOutput(c.res)["context_proposals"].([]any)
				if len(props) != 1 {
					t.Fatalf("context_proposals = %v, want one entry", props)
				}
				if p, _ := props[0].(map[string]any); p["path"] != "AGENTS.md" {
					t.Errorf("context proposal = %v, want path AGENTS.md", props[0])
				}
			}},
		{scenario: "success", label: "success-planner", role: "planner", state: "completed",
			check: func(t *testing.T, c *rowCtx) {
				ro := roleOutput(c.res)
				acs, _ := ro["proposed_acceptance_criteria"].([]any)
				if ro["kind"] != "plan" || len(acs) != 2 {
					t.Errorf("role_output = %v, want a plan with 2 acceptance criteria", ro)
				}
			}},

		{scenario: "failure", state: "failed", reason: "harness_error"},
		{scenario: "blocked", state: "blocked",
			check: func(t *testing.T, c *rowCtx) {
				q := names(handoffOf(c.res), "questions")
				if len(q) != 1 || q[0] != "Should Add saturate on overflow?" {
					t.Errorf("handoff.questions = %q, want the overflow question", q)
				}
			}},
		{scenario: "push", label: "push-tester", state: "completed", pushed: true,
			check: func(t *testing.T, c *rowCtx) {
				if f := tooling(t, c); len(f) != 3 {
					t.Errorf("the push scenario committed %v, want 3 files", f)
				}
			}},
		{scenario: "push", label: "push-implementer", role: "implementer", state: "completed", pushed: true,
			check: func(t *testing.T, c *rowCtx) {
				if f := tooling(t, c); len(f) != 3 {
					t.Errorf("the push scenario committed %v, want 3 files", f)
				}
			}},
		{scenario: "push-no-result", noResult: true, pushed: true},
		{scenario: "malformed-result", resultStatus: 422, exit: 4, state: "done", anyPush: true},
		{scenario: "malformed-handoff", resultStatus: 422, exit: 4, state: "completed", anyPush: true,
			check: func(t *testing.T, c *rowCtx) {
				if got := handoffOf(c.res)["recommended_next_role"]; got != "deployer" {
					t.Errorf("handoff.recommended_next_role = %v, want deployer (the only invalid field)", got)
				}
			}},
		{scenario: "malformed-stream", state: "failed", reason: "harness_stream_incomplete", detail: "stream:"},
		{scenario: "claim-missing", state: "failed", reason: "malformed_harness_output", detail: "claim:"},
		{scenario: "red-valid", state: "completed", pushed: true},
		{scenario: "red-broken-test", state: "completed", pushed: true,
			check: func(t *testing.T, c *rowCtx) {
				if !commitHas(t, c, "calc.Multiply", true) {
					t.Errorf("no committed test file calls the undefined calc.Multiply")
				}
			}},
		{scenario: "already-satisfied", state: "already_satisfied", pushed: true,
			check: func(t *testing.T, c *rowCtx) {
				if f := tooling(t, c); !allTests(f) {
					t.Errorf("committed %v, want only test files", f)
				}
				if !commitHas(t, c, "Sub", true) {
					t.Errorf("no committed test exercises the existing calc.Sub")
				}
			}},
		{scenario: "test-spec-conflict", role: "implementer", state: "test_spec_conflict",
			check: func(t *testing.T, c *rowCtx) {
				cf, _ := c.res["conflict"].(map[string]any)
				if cf == nil || len(names(cf, "ac_ids")) == 0 || len(names(cf, "tests")) == 0 {
					t.Errorf("conflict = %v, want ac_ids, tests and an explanation", c.res["conflict"])
				}
				if got := c.fx.head("agent/sc-1"); got != c.red {
					t.Errorf("the branch moved to %s, want it to stay at %s", got, c.red)
				}
			}},
		{scenario: "green-after-retry", label: "green-after-retry-attempt1", role: "implementer", attempt: 1, state: "completed", pushed: true,
			check: func(t *testing.T, c *rowCtx) {
				if !commitHas(t, c, "a * b", false) {
					t.Errorf("attempt 1 did not commit the wrong implementation (Add returning a*b)")
				}
			}},
		{scenario: "green-after-retry", label: "green-after-retry-attempt2", role: "implementer", attempt: 2, evidence: true, state: "completed", pushed: true,
			check: func(t *testing.T, c *rowCtx) {
				if !commitHas(t, c, "a + b", false) {
					t.Errorf("attempt 2 did not commit the correct implementation (Add returning a+b)")
				}
			}},
		{scenario: "green-after-retry", label: "green-after-retry-attempt2-no-evidence", role: "implementer", attempt: 2,
			state: "failed", reason: "harness_error"},
		{scenario: "forge-then-push", state: "completed", pushed: true,
			check: func(t *testing.T, c *rowCtx) {
				if tags := gitIn(t, c.fx.bare, "tag", "--list"); tags != "" {
					t.Errorf("the bare repository has tags %q, want none (the harness holds no deploy key)", tags)
				}
				if n := len(c.gr.stub.calls("POST", "/v1/worker/result")); n != 1 {
					t.Errorf("the stub Runner received %d result POSTs, want only sc-worker's own", n)
				}
			}},
		{scenario: "phase-conditional-test", state: "completed", pushed: true,
			check: func(t *testing.T, c *rowCtx) {
				if !commitHas(t, c, "SC-STUB", true) {
					t.Errorf("no committed test depends on the SC-STUB marker")
				}
			}},
		{scenario: "permission-denial", state: "completed", pushed: true,
			check: func(t *testing.T, c *rowCtx) {
				hr, _ := c.res["harness_report"].(map[string]any)
				pd, _ := hr["permission_denials"].([]any)
				if len(pd) != 1 {
					t.Fatalf("harness_report.permission_denials = %v, want one entry", hr["permission_denials"])
				}
				if d, _ := pd[0].(map[string]any); d["tool"] != "Bash(git push *)" {
					t.Errorf("permission denial = %v, want tool Bash(git push *)", pd[0])
				}
			}},
		// 28-AC-7: over-budget issues requests until the stub Gateway returns 403, and stops there.
		{scenario: "over-budget", gateway: exhausted(3), wantGateway: 4, state: "failed", reason: "budget_denied"},
		{scenario: "spend-loop", gateway: exhausted(1), wantGateway: 2, state: "failed", reason: "budget_denied"},
		{scenario: "model-switch", state: "completed", pushed: true, wantGateway: 2,
			gateway: func(n int, model string) (int, string) {
				if model != "fake-model" {
					return 403, "SC_MODEL_NOT_ALLOWED"
				}
				return 200, ""
			},
			check: func(t *testing.T, c *rowCtx) {
				c.gr.gwMu.Lock()
				defer c.gr.gwMu.Unlock()
				if got := strings.Join(c.gr.gwModels, ","); got != "fake-model,fake-model-b" {
					t.Errorf("requested models = %s, want fake-model,fake-model-b", got)
				}
			}},
		{scenario: "green", role: "implementer", state: "completed", pushed: true},
		{scenario: "red-compile-error", state: "completed", pushed: true,
			check: func(t *testing.T, c *rowCtx) {
				if f := tooling(t, c); !allTests(f) {
					t.Errorf("committed %v, want only test files", f)
				}
			}},
		{scenario: "red-with-stub", state: "completed", pushed: true, policy: denylistPolicy,
			check: func(t *testing.T, c *rowCtx) {
				if !commitHas(t, c, "SC-STUB", false) {
					t.Errorf("no committed non-test file carries an SC-STUB marker")
				}
			}},
		{scenario: "red-valid-v2", cont: true, state: "completed", pushed: true,
			check: func(t *testing.T, c *rowCtx) {
				if got := gitIn(t, c.fx.bare, "rev-parse", commitOf(t, c)+"^"); got != c.red {
					t.Errorf("parent of the pushed commit = %s, want red(g1) %s", got, c.red)
				}
			}},
		reviewerRow("review-approve", "", "final_review", func(t *testing.T, ro map[string]any) {
			if ro["verdict"] != "approve" {
				t.Errorf("verdict = %v, want approve", ro["verdict"])
			}
		}),
		reviewerRow("review-request-changes", "", "final_review", func(t *testing.T, ro map[string]any) {
			f, _ := ro["findings"].([]any)
			if ro["verdict"] != "request_changes" || len(f) != 1 {
				t.Errorf("verdict %v with %d findings, want request_changes with one", ro["verdict"], len(f))
			} else if m, _ := f[0].(map[string]any); m["severity"] != "major" {
				t.Errorf("finding = %v, want severity major", f[0])
			}
		}),
		reviewerRow("review-reject", "", "final_review", func(t *testing.T, ro map[string]any) {
			if ro["verdict"] != "reject" {
				t.Errorf("verdict = %v, want reject", ro["verdict"])
			}
			failed := false
			cks, _ := ro["checks"].([]any)
			for _, ck := range cks {
				if m, _ := ck.(map[string]any); m["id"] == "green_not_by_bypass" && m["result"] == "fail" {
					failed = true
				}
			}
			if !failed {
				t.Errorf("checks = %v, want green_not_by_bypass fail", ro["checks"])
			}
		}),
		reviewerRow("adjudicate-test-valid", "", "adjudication", func(t *testing.T, ro map[string]any) {
			cd, _ := ro["conflict_decision"].(map[string]any)
			if ro["purpose"] != "adjudication" || cd["verdict"] != "test_valid" {
				t.Errorf("role_output = %v, want purpose adjudication with conflict_decision.verdict test_valid", ro)
			}
		}),
		reviewerRow("adjudicate-test-invalid", "", "adjudication", func(t *testing.T, ro map[string]any) {
			cd, _ := ro["conflict_decision"].(map[string]any)
			if s, _ := cd["proposed_test_change"].(string); ro["purpose"] != "adjudication" || cd["verdict"] != "test_invalid" || s == "" {
				t.Errorf("role_output = %v, want conflict_decision.verdict test_invalid with proposed_test_change", ro)
			}
		}),
		{scenario: "judge-docs", state: "completed", pushed: true, policy: denylistPolicy},
		{scenario: "produce-docs", role: "implementer", state: "completed", pushed: true, policy: denylistPolicy},
	}
}

// 28-AC-4 and 28-AC-7: one table-driven row per scenario (rows listed in scenarioRows; timeout and ignore-cancel need
// real elapsed time and have their own tests below).
func TestScenarioTable_PostedResults_28AC4(t *testing.T) {
	for _, r := range scenarioRows() {
		name := r.label
		if name == "" {
			name = r.scenario
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			runRow(t, r)
		})
	}
}

// 28-AC-4: the table has a row for every scenario the criterion names ("all rows").
func TestScenarioTable_CoversEverySpecifiedScenario_28AC4(t *testing.T) {
	have := map[string]bool{"timeout": true, "ignore-cancel": true} // rows with their own tests
	for _, r := range scenarioRows() {
		have[r.scenario] = true
	}
	var missing []string
	for _, s := range requiredScenarios {
		if !have[s] {
			missing = append(missing, s)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("no row for scenarios %v", missing)
	}
	// A row that cannot hold is a RED-visible reminder that the run below covers it: the rows must be non-trivial.
	for _, r := range scenarioRows() {
		if r.state == "" && !r.noResult {
			t.Errorf("row %s/%s has no expected state", r.scenario, r.label)
		}
	}
}

// 28-AC-4 and 28-AC-7, scenario timeout: harness_timeout_seconds 60 -> failed/harness_timeout; the harness has called
// the Gateway.
func TestScenarioTable_Timeout_28AC4(t *testing.T) {
	t.Parallel()
	gr := s4Run(t, s4Opts{scenario: "timeout", timeout: 60, grace: 5})
	res := gr.finishWithin(t, 120*time.Second, "the harness timeout")
	wantFailedResult(t, res, "harness_timeout")
	if gr.gwHits.Load() < 1 {
		t.Errorf("the timeout scenario sent no request to SC_FAKE_GATEWAY_URL (28-AC-7)")
	}
}

// 28-AC-4 and 28-AC-7, scenario ignore-cancel (wedged): SIGTERM to sc-worker -> failed/cancelled, exit 143.
func TestScenarioTable_IgnoreCancel_28AC4(t *testing.T) {
	t.Parallel()
	gr := s4Run(t, s4Opts{scenario: "ignore-cancel", grace: 5})
	waitUntil(t, 45*time.Second, "the harness to call the gateway", func() bool { return gr.gwHits.Load() > 0 })
	gr.harnessGroup(t)
	if err := gr.proc.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if code := gr.waitExit(t, 40*time.Second, "SIGTERM"); code != 143 {
		t.Errorf("exit code = %d, want 143", code)
	}
	gr.endShim()
	posts := gr.stub.calls("POST", "/v1/worker/result")
	if len(posts) != 1 {
		t.Fatalf("%d result POSTs, want 1", len(posts))
	}
	wantFailedResult(t, decodeResult(t, posts[0].Body), "cancelled")
}

// 28-AC-4, wedged knob disable_cancel (plan 28 section 8.2 and 14.4): sc-worker ignores cancel_requested in the
// heartbeat response, so the wedged harness keeps running; only the SIGTERM path (plan 27 section 14.14) ends it.
// (Judgement: the table's "END missing" is read as the harness never being cancelled by the heartbeat; the SIGTERM
// path stays as in 27-AC-3.)
func TestScenarioTable_IgnoreCancelKnobIgnoresHeartbeatCancel_28AC4(t *testing.T) {
	t.Parallel()
	gr := s4Run(t, s4Opts{scenario: "ignore-cancel", grace: 5, cancelAfterHB: 1})
	pgrp := gr.harnessGroup(t)
	waitUntil(t, 60*time.Second, "two heartbeats answered with cancel_requested (or the end of the run)", func() bool {
		return len(gr.stub.calls("POST", "/v1/worker/heartbeat")) >= 2 || gr.resultPosted()
	})
	if _, err := os.Stat(filepath.Join(gr.shimDir(), "cancel-1")); err == nil {
		t.Errorf("sc-worker wrote cancel-1 although disable_cancel is set for scenario ignore-cancel")
	}
	if gr.resultPosted() {
		t.Errorf("a result was posted before any SIGTERM: the heartbeat cancel was not ignored")
	}
	if len(survivors(pgrp)) == 0 {
		t.Errorf("the wedged harness is gone after cancel_requested; disable_cancel must keep it running")
	}
	if err := gr.proc.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if code := gr.waitExit(t, 40*time.Second, "SIGTERM"); code != 143 {
		t.Errorf("exit code = %d, want 143", code)
	}
	gr.endShim()
}
