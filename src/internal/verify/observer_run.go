//go:build linux

package verify

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"

	"strange.company/sc/internal/version"
)

// The observer of plan 22 sections 8 and 14.1: `sc-verify run` executes the suites next to the code under test, so
// everything it keeps is held in memory until the last descendant of the last suite is dead, and only then written
// to the record directory (sealed copies read-only, the observation last).

const (
	obsMaxTests      = 2000
	obsMaxDocBytes   = 262144
	obsMaxIDSetData  = 120000
	obsMaxProtected  = 64 << 10
	obsMaxReportSize = 128 << 20
	obsTailBytes     = 4096
)

var (
	obsTaskRE     = regexp.MustCompile(`^sc-[0-9]+$`)
	obsRepoNmRE   = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	obsTestIDRE   = regexp.MustCompile(`^[a-z][a-z0-9-]{0,40}::[^\s].{0,498}$`)
	obsFilePathRE = regexp.MustCompile(`^[A-Za-z0-9._@+/-]{1,400}$`)
	obsGoTestRE   = regexp.MustCompile(`^(.*?)\.((?:Test|Benchmark|Example|Fuzz)[^/]*)(?:/.*)?$`)
)

type protectedTest struct{ Kind, ID string }

// obsIdentity is the verification intent as read from the SC_* variables (plan 22 section 5).
type obsIdentity struct {
	Phase, Subject, TaskID, Expected, Base, Red, Baseline, Repository string
	Protected                                                         []protectedTest
}

// recFile is one file of the record directory, kept in memory until the record is written.
type recFile struct {
	Name string
	Data []byte
}

type observer struct {
	d         Deps
	clock     Clock
	cwd       string
	git       GitRepo
	id        obsIdentity
	cfg       obsConfig
	adapters  map[string]Adapter
	childPath string
	work      string
	valBlob   string
	sealed    []recFile
	logs      []recFile
	seq       int
	baseSnap  *snapshot // the base and red trees, read before any code under test ran (D28 B1)
	redSnap   *snapshot
	treeDirs  []string
}

type suiteOutcome struct {
	Summary RunSummary
	Report  Report
	Found   bool
}

func sha256Hex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func obsIdentityFrom(env map[string]string) (obsIdentity, error) {
	var id obsIdentity
	id.Phase = env["SC_PHASE"]
	if id.Phase != "red" && id.Phase != "green" {
		return id, fmt.Errorf("SC_PHASE must be red or green")
	}
	id.Subject = env["SC_SUBJECT"]
	if id.Subject == "" {
		id.Subject = "tests"
	}
	if id.Subject == "judge" {
		return id, fmt.Errorf("SC_SUBJECT=judge is not supported by this verifier")
	}
	if id.Subject != "tests" {
		return id, fmt.Errorf("SC_SUBJECT must be tests or judge")
	}
	id.TaskID = env["SC_TASK_ID"]
	if !obsTaskRE.MatchString(id.TaskID) {
		return id, fmt.Errorf("SC_TASK_ID is missing or malformed")
	}
	for _, v := range []struct {
		name string
		dst  *string
		need bool
	}{{"SC_EXPECTED_SHA", &id.Expected, true}, {"SC_BASE_SHA", &id.Base, true}, {"SC_RED_SHA", &id.Red, id.Phase == "green"}} {
		*v.dst = env[v.name]
		if (v.need || *v.dst != "") && (!objectIDRE.MatchString(*v.dst) || *v.dst == zeroObject) {
			return id, fmt.Errorf("%s is missing or not a 40 hex object id", v.name)
		}
	}
	id.Baseline = env["SC_BASELINE"]
	switch id.Baseline {
	case "":
		id.Baseline = "skip"
	case "run", "skip":
	default:
		return id, fmt.Errorf("SC_BASELINE must be run or skip")
	}
	id.Repository = env["CI_REPO"]
	if !obsRepoNmRE.MatchString(id.Repository) {
		return id, fmt.Errorf("CI_REPO is missing or malformed")
	}
	if raw := env["SC_PROTECTED_TESTS"]; raw != "" {
		if id.Phase != "green" {
			return id, fmt.Errorf("SC_PROTECTED_TESTS is only valid at green")
		}
		if len(raw) > obsMaxProtected {
			return id, fmt.Errorf("SC_PROTECTED_TESTS is larger than 64 KiB")
		}
		seen := map[string]bool{}
		for _, line := range strings.Split(strings.TrimSuffix(raw, "\n"), "\n") {
			kind, tid, ok := strings.Cut(line, "\t")
			if !ok || (kind != "change" && kind != "preservation" && kind != "other") || !obsTestIDRE.MatchString(tid) {
				return id, fmt.Errorf("SC_PROTECTED_TESTS has a malformed line")
			}
			if !seen[kind+"\t"+tid] {
				seen[kind+"\t"+tid] = true
				id.Protected = append(id.Protected, protectedTest{Kind: kind, ID: tid})
			}
		}
	}
	return id, nil
}

// forceRemove deletes a path without following symlinks, also when a suite made directories read-only.
func forceRemove(p string) error {
	fi, err := os.Lstat(p)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if fi.IsDir() {
		_ = filepath.WalkDir(p, func(q string, d os.DirEntry, err error) error {
			if err == nil && d.IsDir() {
				_ = os.Chmod(q, 0o700)
			}
			return nil
		})
	}
	return os.RemoveAll(p)
}

// hasGitAbove reports whether dir or an ancestor holds a .git entry.
func hasGitAbove(dir string) bool {
	for d := dir; ; d = filepath.Dir(d) {
		if _, err := os.Lstat(filepath.Join(d, ".git")); err == nil {
			return true
		}
		if d == "/" || d == "." || d == filepath.Dir(d) {
			return false
		}
	}
}

func within(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, "../")
}

// makeWorkRoot creates the private directory of the run (homes, temp dirs, base tree, replay export). It lies
// outside the workspace and has no .git above it.
func (o *observer) makeWorkRoot(clean []string) error {
	dir, err := o.privateDir(clean, "sc-verify-")
	if err != nil {
		return err
	}
	o.work = dir
	return nil
}

// privateDir creates a fresh directory in the first of the candidate parents that lies outside the workspace and
// outside every Git repository.
func (o *observer) privateDir(clean []string, prefix string) (string, error) {
	for _, base := range clean {
		if base == "" || hasGitAbove(base) {
			continue
		}
		if real, err := filepath.EvalSymlinks(base); err == nil && within(o.cwd, real) {
			continue
		}
		dir, err := os.MkdirTemp(base, prefix)
		if err != nil {
			continue
		}
		return dir, nil
	}
	return "", fmt.Errorf("no private directory outside the workspace and outside every Git repository")
}

// extractTree writes a snapshot into a fresh private directory that is not below the run's work directory (whose
// subdirectories hold the HOME and TMPDIR the suites can write), and returns the tree and its cleanup.
func (o *observer) extractTree(snap *snapshot, prefix string) (string, func(), error) {
	parent, err := o.privateDir([]string{os.TempDir(), "/tmp", "/var/tmp"}, prefix)
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = forceRemove(parent) }
	tree := filepath.Join(parent, "tree")
	if err := snap.extract(tree); err != nil {
		cleanup()
		return "", nil, err
	}
	return tree, cleanup, nil
}

func (o *observer) childEnv(home, tmp string, suiteEnv map[string]string, extra ...string) []string {
	env := []string{"PATH=" + o.childPath, "HOME=" + home, "TMPDIR=" + tmp, "CI=true"}
	keys := make([]string, 0, len(suiteEnv))
	for k := range suiteEnv {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		env = append(env, k+"="+suiteEnv[k])
	}
	return append(env, extra...)
}

func argvDigest(argv []string) string {
	return "sha256:" + sha256Hex([]byte(strings.Join(argv, "\x00")))
}

func tailOf(stdout, stderr []byte) string {
	all := append(append([]byte(nil), stdout...), stderr...)
	if len(all) > obsTailBytes {
		all = all[len(all)-obsTailBytes:]
	}
	return strings.ToValidUTF8(string(all), "?")
}

// readTreeReport reads a report file of a tree without following a symlink out of the tree: the parent directory
// must resolve inside the tree and the file itself must be a regular file.
func readTreeReport(root, rel string) ([]byte, bool) {
	full := filepath.Join(root, rel)
	rootReal, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, false
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(full))
	if err != nil || !within(rootReal, parent) {
		return nil, false
	}
	f, err := os.OpenFile(filepath.Join(parent, filepath.Base(full)), os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > obsMaxReportSize {
		return nil, false
	}
	b, err := io.ReadAll(io.LimitReader(f, obsMaxReportSize+1))
	if err != nil || len(b) > obsMaxReportSize {
		return nil, false
	}
	return b, true
}

func countsOf(tests []TestResult) Counts {
	c := Counts{Total: len(tests)}
	for _, t := range tests {
		switch t.Status {
		case statusPassed:
			c.Passed++
		case statusFailedAssertion:
			c.FailedAssertion++
		case statusFailedError:
			c.FailedError++
		case statusStub:
			c.StubUnimplemented++
		case statusSkipped:
			c.Skipped++
		}
	}
	return c
}

// runSuite executes one suite in the tree rooted at root. The commit is the commit the tree holds.
func (o *observer) runSuite(ctx context.Context, root, treeName, commit string, s obsSuite, argv []string) (suiteOutcome, error) {
	var out suiteOutcome
	reportPath := ""
	if s.ReportPath != "-" {
		reportPath = filepath.Join(root, s.ReportPath)
		if fi, err := os.Lstat(reportPath); err == nil && !fi.IsDir() {
			_ = os.Remove(reportPath)
		}
	}
	full := make([]string, len(argv))
	for i, a := range argv {
		full[i] = strings.ReplaceAll(a, "{report}", reportPath)
	}
	dir := root
	if s.WorkingDir != "" {
		dir = filepath.Join(root, s.WorkingDir)
	}
	o.seq++
	home, err := os.MkdirTemp(o.work, "home-")
	if err != nil {
		return out, err
	}
	tmp, err := os.MkdirTemp(o.work, "tmp-")
	if err != nil {
		return out, err
	}
	defer forceRemove(home)
	defer forceRemove(tmp)
	res, err := execArgv(ctx, o.clock, dir, full, o.childEnv(home, tmp, s.Env), o.childPath, s.TimeoutSeconds)
	if err != nil {
		return out, err
	}
	logName := "out/" + s.Name + "-" + treeName + ".log"
	logData := append(append([]byte(nil), res.Stdout...), res.Stderr...)
	o.logs = append(o.logs, recFile{Name: logName, Data: logData})
	var data []byte
	found := false
	if s.ReportPath == "-" {
		data, found = res.Stdout, !res.Overflow && res.Started
	} else if res.Started {
		data, found = readTreeReport(root, s.ReportPath)
		if found {
			o.sealed = append(o.sealed, recFile{Name: "sealed/" + s.Name + "-" + treeName + ".report", Data: data})
		}
	}
	var rep Report
	if found {
		if r, err := NormalizeReport(o.adapters[s.Adapter], s.Name, data); err == nil {
			rep = r
		} else {
			found = false
		}
	}
	out.Report, out.Found = rep, found
	out.Summary = RunSummary{
		Tree: treeName, Commit: commit, Suite: s.Name, Adapter: s.Adapter, ArgvSha256: argvDigest(full),
		ExitCode: res.Exit, DurationMs: res.DurationMs, TimedOut: res.TimedOut || rep.TimedOut,
		BuildFailed: rep.BuildFailed, ReportFound: found, Counts: countsOf(rep.Tests), OutputTail: tailOf(res.Stdout, res.Stderr),
	}
	return out, nil
}

func (o *observer) suitesFor(phase string) []obsSuite {
	var out []obsSuite
	for _, s := range o.cfg.Suites {
		if s.inPhase(phase) {
			out = append(out, s)
		}
	}
	return out
}

type treeRun struct {
	Runs  []RunSummary
	Tests map[string]TestResult
}

// runTree runs every suite of the phase in a tree.
func (o *observer) runTree(ctx context.Context, root, treeName, commit string) (treeRun, error) {
	tr := treeRun{Tests: map[string]TestResult{}}
	for _, s := range o.suitesFor(o.id.Phase) {
		oc, err := o.runSuite(ctx, root, treeName, commit, s, s.Argv)
		if err != nil {
			return tr, err
		}
		tr.Runs = append(tr.Runs, oc.Summary)
		for _, t := range oc.Report.Tests {
			tr.Tests[t.ID] = t
		}
	}
	return tr, nil
}

func obsEncodeIDSet(ids []string) *TestIDSet {
	sort.Strings(ids)
	text := []byte(strings.Join(ids, "\n"))
	var zb bytes.Buffer
	zw := gzip.NewWriter(&zb)
	_, _ = zw.Write(text)
	_ = zw.Close()
	data := base64.StdEncoding.EncodeToString(zb.Bytes())
	if len(data) > obsMaxIDSetData {
		return nil
	}
	return &TestIDSet{Count: len(ids), Sha256: sha256Hex(text), Data: data}
}

func idsWhere(m map[string]TestResult, keep func(TestResult) bool) []string {
	ids := []string{}
	for id, t := range m {
		if keep(t) {
			ids = append(ids, id)
		}
	}
	return ids
}

func testObs(t TestResult) TestObservation {
	to := TestObservation{ID: t.ID, Suite: suiteOf(t.ID), StatusHead: t.Status, FailureType: t.FailureType, Message: t.Message}
	if obsFilePathRE.MatchString(t.File) && !strings.HasPrefix(t.File, "/") && !strings.Contains(t.File, "..") {
		to.File = t.File
	}
	return to
}

// selectTests orders the candidate tests by importance and cuts at the schema bound.
func selectTests(sel []TestObservation, rank func(TestObservation) int) ([]TestObservation, bool) {
	if sel == nil {
		sel = []TestObservation{}
	}
	sort.SliceStable(sel, func(i, j int) bool {
		ri, rj := rank(sel[i]), rank(sel[j])
		if ri != rj {
			return ri < rj
		}
		return sel[i].ID < sel[j].ID
	})
	if len(sel) > obsMaxTests {
		return sel[:obsMaxTests], true
	}
	return sel, false
}

func (o *observer) verifier() (Verifier, error) {
	exe, err := os.Executable()
	if err != nil {
		return Verifier{}, err
	}
	b, err := os.ReadFile(exe)
	if err != nil {
		return Verifier{}, err
	}
	v := version.Version
	if len(v) > 40 {
		v = v[:40]
	}
	return Verifier{Version: v, BinarySha256: "sha256:" + sha256Hex(b)}, nil
}

// redObservation builds the RED observation from the runs of both trees (base nil when the baseline is skipped).
func (o *observer) redObservation(head string, base *treeRun, headRun treeRun, ver Verifier) Observation {
	obs := Observation{
		TaskID: o.id.TaskID, Repository: o.id.Repository, Phase: "red", Subject: "tests", BaseSha: o.id.Base, HeadSha: head,
		Verifier: ver, ValidationBlob: o.valBlob, ToolchainImage: o.cfg.Image, Tests: []TestObservation{}, ObservedAt: o.clock.Now(),
	}
	obs.Runs = []RunSummary{}
	if base != nil {
		obs.Runs = append(obs.Runs, base.Runs...)
	}
	obs.Runs = append(obs.Runs, headRun.Runs...)
	ids := map[string]bool{}
	for id := range headRun.Tests {
		ids[id] = true
	}
	if base != nil {
		for id := range base.Tests {
			ids[id] = true
		}
	}
	var sel []TestObservation
	for id := range ids {
		h, inHead := headRun.Tests[id]
		headStatus := statusAbsent
		if inHead {
			headStatus = h.Status
		}
		to := testObs(h)
		to.ID, to.Suite, to.StatusHead = id, suiteOf(id), headStatus
		if base != nil {
			b, inBase := base.Tests[id]
			baseStatus := statusAbsent
			if inBase {
				baseStatus = b.Status
			}
			to.StatusBase = baseStatus
			if !inHead {
				to = testObs(b)
				to.StatusBase, to.StatusHead = baseStatus, statusAbsent
			}
			if baseStatus == headStatus && headStatus == statusPassed {
				continue
			}
		}
		sel = append(sel, to)
	}
	obs.Tests, obs.TestsTruncated = selectTests(sel, func(t TestObservation) int {
		if t.StatusHead != statusPassed {
			return 0
		}
		return 1
	})
	sets := &TestIDSets{HeadPresent: obsEncodeIDSet(idsWhere(headRun.Tests, func(TestResult) bool { return true }))}
	if base != nil {
		sets.BasePassing = obsEncodeIDSet(idsWhere(base.Tests, func(t TestResult) bool { return t.Status == statusPassed }))
		sets.BaseSkipped = obsEncodeIDSet(idsWhere(base.Tests, func(t TestResult) bool { return t.Status == statusSkipped }))
	}
	obs.TestIDSets = sets
	return obs
}

// greenObservation builds the GREEN observation of the head run.
func (o *observer) greenObservation(head string, headRun treeRun, checks []CheckRun, replay *RedReplay, ver Verifier) GreenObservation {
	obs := GreenObservation{
		TaskID: o.id.TaskID, Repository: o.id.Repository, Phase: "green", Subject: "tests", BaseSha: o.id.Base, HeadSha: head,
		Verifier: ver, ValidationBlob: o.valBlob, ToolchainImage: o.cfg.Image, Checks: checks, Tests: []TestObservation{},
		RedReplay: replay, ObservedAt: o.clock.Now(),
	}
	if obs.Checks == nil {
		obs.Checks = []CheckRun{}
	}
	obs.Runs = headRun.Runs
	if obs.Runs == nil {
		obs.Runs = []RunSummary{}
	}
	protected := map[string]bool{}
	for _, p := range o.id.Protected {
		protected[p.ID] = true
	}
	var sel []TestObservation
	for id, t := range headRun.Tests {
		if t.Status != statusPassed || protected[id] {
			sel = append(sel, testObs(t))
		}
	}
	for id := range protected {
		if _, ok := headRun.Tests[id]; !ok {
			sel = append(sel, TestObservation{ID: id, Suite: suiteOf(id), StatusHead: statusAbsent})
		}
	}
	obs.Tests, obs.TestsTruncated = selectTests(sel, func(t TestObservation) int {
		switch {
		case t.StatusHead != statusPassed && t.StatusHead != statusSkipped:
			return 0
		case protected[t.ID]:
			return 1
		}
		return 2
	})
	obs.TestIDSets = &GreenTestIDSets{
		HeadPresent: obsEncodeIDSet(idsWhere(headRun.Tests, func(TestResult) bool { return true })),
		HeadSkipped: obsEncodeIDSet(idsWhere(headRun.Tests, func(t TestResult) bool { return t.Status == statusSkipped })),
	}
	return obs
}

// goTestNames returns the top-level Go test names of test ids of one suite, or nil when one cannot be derived.
func goTestNames(ids []string) []string {
	seen := map[string]bool{}
	var names []string
	for _, id := range ids {
		_, rest, _ := strings.Cut(id, "::")
		m := obsGoTestRE.FindStringSubmatch(rest)
		if m == nil {
			return nil
		}
		if !seen[m[2]] {
			seen[m[2]] = true
			names = append(names, m[2])
		}
	}
	sort.Strings(names)
	return names
}

// redReplay runs the change-criterion manifest tests against an export of the red commit (plan 22 section 14.1
// step 4, G9): no .git above it, no stub marker line, the same from-scratch environment.
func (o *observer) redReplay(ctx context.Context) (*RedReplay, error) {
	var change []string
	for _, p := range o.id.Protected {
		if p.Kind == "change" {
			change = append(change, p.ID)
		}
	}
	rp := &RedReplay{RedCommit: o.id.Red, Tests: []ReplayTest{}}
	if len(change) == 0 {
		return rp, nil
	}
	if o.redSnap == nil {
		return nil, fmt.Errorf("internal error: the red tree was not read before the run")
	}
	root, cleanup, err := o.extractTree(o.redSnap, "sc-verify-replay-")
	if err != nil {
		return nil, err
	}
	defer cleanup()
	if hasGitAbove(root) {
		return nil, fmt.Errorf("the replay export has a .git entry above it")
	}
	if err := stripStubMarkers(root); err != nil {
		return nil, err
	}
	status := map[string]string{}
	for _, s := range o.suitesFor("green") {
		var ids []string
		for _, id := range change {
			if suiteOf(id) == s.Name {
				ids = append(ids, id)
			}
		}
		if len(ids) == 0 {
			continue
		}
		argv := s.Argv
		if names := goTestNames(ids); s.Language == "go" && names != nil && len(s.SelectArgv) > 0 {
			quoted := make([]string, len(names))
			for i, n := range names {
				quoted[i] = regexp.QuoteMeta(n)
			}
			sel := "^(" + strings.Join(quoted, "|") + ")$"
			argv = append([]string(nil), s.Argv...)
			for _, a := range s.SelectArgv {
				argv = append(argv, strings.ReplaceAll(a, "{test_ids}", sel))
			}
		}
		oc, err := o.runSuite(ctx, root, "replay", o.id.Red, s, argv)
		if err != nil {
			return nil, err
		}
		for _, t := range oc.Report.Tests {
			status[t.ID] = t.Status
		}
	}
	for _, id := range change {
		st, ok := status[id]
		if !ok {
			st = statusAbsent
		}
		rp.Tests = append(rp.Tests, ReplayTest{ID: id, Status: st})
	}
	sort.Slice(rp.Tests, func(i, j int) bool { return rp.Tests[i].ID < rp.Tests[j].ID })
	return rp, nil
}

// runChecks executes every check of validation.yaml (GREEN). A base check runs from a base tree with
// SC_TARGET_TREE naming the head workspace; only toolingFrom head runs the head's own checker (TDD-15).
func (o *observer) runChecks(ctx context.Context) ([]CheckRun, error) {
	runs := []CheckRun{}
	for _, c := range o.cfg.Checks {
		root := o.cwd
		if c.ToolingFrom == "base" {
			if o.baseSnap == nil {
				return nil, fmt.Errorf("internal error: the base tree was not read before the run")
			}
			tree, cleanup, err := o.extractTree(o.baseSnap, "sc-verify-base-")
			if err != nil {
				return nil, err
			}
			defer cleanup()
			root = tree
		}
		home, err := os.MkdirTemp(o.work, "home-")
		if err != nil {
			return nil, err
		}
		tmp, err := os.MkdirTemp(o.work, "tmp-")
		if err != nil {
			return nil, err
		}
		res, err := execArgv(ctx, o.clock, root, c.Argv, o.childEnv(home, tmp, c.Env, "SC_TARGET_TREE="+o.cwd), o.childPath, c.TimeoutSeconds)
		_ = forceRemove(home)
		_ = forceRemove(tmp)
		if err != nil {
			return nil, err
		}
		o.logs = append(o.logs, recFile{Name: "out/check-" + c.Name + ".log", Data: append(append([]byte(nil), res.Stdout...), res.Stderr...)})
		runs = append(runs, CheckRun{
			Name: c.Name, ArgvSha256: argvDigest(c.Argv), ExitCode: res.Exit, DurationMs: res.DurationMs, TimedOut: res.TimedOut,
			Required: true, OutputTail: tailOf(res.Stdout, res.Stderr),
		})
	}
	return runs, nil
}
