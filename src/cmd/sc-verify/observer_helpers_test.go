package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"strange.company/sc/internal/contracts"
)

// Test infrastructure of the observer slice (F06 S7, plan 22 sections 7.1, 8 and 14.1). The tests build the real
// sc-verify binary, create scratch Git repositories from the fixture modules under testdata/observer (every file
// carries the suffix .fx so that no fixture is a package or a module of this repository) and run `sc-verify run`
// and `sc-verify emit` as separate processes with real `go test` suites.
//
// Interface choices the plan leaves open (reported in the F06 S7 RED handoff):
//   - run takes --validation <file> --record <dir> --policy <platform verification.yaml> --child-path <PATH> and
//     works in the current directory (the workspace); the record directory holds out/<suite>-<tree>.log, sealed/
//     and observed.json (plan 22 section 8);
//   - emit takes --record <dir> --emit log (plan 20 section 8.4) and prints the framed block (plan 20 section 8.5);
//   - the repository comes from CI_REPO (Woodpecker's owner/name), the child PATH from --child-path.

const (
	fxPhaseRed   = "red"
	fxPhaseGreen = "green"
	fxTask       = "sc-184"
	fxRepoName   = "projects/fx"
)

var (
	fxBuildOnce sync.Once
	fxBinPath   string
	fxBuildDir  string
	fxBuildErr  error
)

func TestMain(m *testing.M) {
	code := m.Run()
	if fxBuildDir != "" {
		_ = os.RemoveAll(fxBuildDir)
	}
	os.Exit(code)
}

// fxBinary builds cmd/sc-verify once per test binary and returns the path of the executable.
func fxBinary(t testing.TB) string {
	t.Helper()
	fxBuildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "sc-verify-bin-*")
		if err != nil {
			fxBuildErr = err
			return
		}
		fxBuildDir = dir
		out := filepath.Join(dir, "sc-verify")
		cmd := exec.Command("go", "build", "-o", out, ".")
		if b, err := cmd.CombinedOutput(); err != nil {
			fxBuildErr = fmt.Errorf("go build: %v\n%s", err, b)
			return
		}
		fxBinPath = out
	})
	if fxBuildErr != nil {
		t.Fatalf("cannot build sc-verify: %v", fxBuildErr)
	}
	return fxBinPath
}

func fxRoot(t testing.TB) string {
	t.Helper()
	r, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func fxRandHex(t testing.TB, n int) string {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}

// ---------------------------------------------------------------------------------------------------------------
// Fixture repositories

type fxSuite struct {
	Name       string
	Adapter    string
	Language   string
	Argv       []string
	ReportPath string // "" means "-" (the suite's stdout)
	Env        map[string]string
	SelectArgv []string
}

type fxCheck struct {
	Name        string
	Argv        []string
	ToolingFrom string // "" omits the key (the schema default applies)
}

type fxConfig struct {
	Fixture string
	Shared  []string
	Suites  []fxSuite
	Checks  []fxCheck
}

// fxRepo is a scratch workspace holding base, optionally red, and head commits of a fixture module.
type fxRepo struct {
	t         testing.TB
	cfg       fxConfig
	ws        string // the workspace (a Git work tree at head)
	out       string // $FX_OUT of the suites: a directory the code under test writes its observations to
	record    string // the record directory handed to run and emit
	home, tmp string // the HOME and TMPDIR of the sc-verify process itself
	goDir     string
	childPath string
	goCache   string
	goenvFile string
	leak      string // value of LEAK_SENTINEL in the launching environment
	nonce     string
	intent    string
	base      string
	red       string
	head      string
	blob      string // blob id of .strange-company/validation.yaml
}

func fxGitEnv(home string) []string {
	return []string{
		"PATH=/usr/bin:/bin", "HOME=" + home, "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=fx", "GIT_AUTHOR_EMAIL=fx@example.org", "GIT_COMMITTER_NAME=fx", "GIT_COMMITTER_EMAIL=fx@example.org",
		"GIT_AUTHOR_DATE=2026-01-01T00:00:00Z", "GIT_COMMITTER_DATE=2026-01-01T00:00:00Z",
	}
}

func fxGit(t testing.TB, home, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = fxGitEnv(home)
	b, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, b)
	}
	return strings.TrimSpace(string(b))
}

// fxOverlay copies the fixture directory src into dst, dropping the .fx suffix of every file.
func fxOverlay(t testing.TB, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if !strings.HasSuffix(rel, ".fx") {
			return fmt.Errorf("fixture file %s lacks the .fx suffix", p)
		}
		return os.WriteFile(filepath.Join(dst, strings.TrimSuffix(rel, ".fx")), b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func fxExists(p string) bool { _, err := os.Stat(p); return err == nil }

func fxGoDir(t testing.TB) string {
	t.Helper()
	p, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("go is not on PATH: %v", err)
	}
	return filepath.Dir(p)
}

func fxGoCache(t testing.TB) string {
	t.Helper()
	b, err := exec.Command("go", "env", "GOCACHE").Output()
	if err != nil {
		t.Fatalf("go env GOCACHE: %v", err)
	}
	return strings.TrimSpace(string(b))
}

// fxNewRepo materialises the fixture and commits base (with .strange-company/validation.yaml), red when the
// fixture has a red directory, and head (an empty commit when the fixture has no head directory).
func fxNewRepo(t testing.TB, cfg fxConfig) *fxRepo {
	t.Helper()
	tmp := t.TempDir()
	r := &fxRepo{t: t, cfg: cfg}
	r.ws = filepath.Join(tmp, "ws")
	r.out = filepath.Join(tmp, "out")
	r.record = filepath.Join(tmp, "record")
	r.home = filepath.Join(tmp, "runhome")
	r.tmp = filepath.Join(tmp, "runtmp")
	for _, d := range []string{r.ws, r.out, r.record, r.home, r.tmp} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	r.goDir = fxGoDir(t)
	r.childPath = r.goDir + ":/usr/bin:/bin"
	r.goCache = fxGoCache(t)
	r.leak = "LEAK-" + fxRandHex(t, 8)
	r.nonce = fxRandHex(t, 16)
	r.intent = "vi-" + fxRandHex(t, 6)
	r.goenvFile = filepath.Join(tmp, "goenv")
	if err := os.WriteFile(r.goenvFile, []byte("GOFLAGS=-tags=fxleak2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fixture := filepath.Join("testdata", "observer", cfg.Fixture)
	fxGit(t, r.home, r.ws, "-c", "init.defaultBranch=main", "init", "-q")

	shared := append([]string{"_common"}, cfg.Shared...)
	for _, s := range shared {
		fxOverlay(t, filepath.Join("testdata", "observer", s), r.ws)
	}
	fxOverlay(t, filepath.Join(fixture, "base"), r.ws)
	if err := os.MkdirAll(filepath.Join(r.ws, ".strange-company"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.ws, ".strange-company", "validation.yaml"), r.validationDoc(), 0o644); err != nil {
		t.Fatal(err)
	}
	fxGit(t, r.home, r.ws, "add", "-A")
	fxGit(t, r.home, r.ws, "commit", "-q", "-m", "chore: base")
	r.base = fxGit(t, r.home, r.ws, "rev-parse", "HEAD")
	r.blob = fxGit(t, r.home, r.ws, "rev-parse", "HEAD:.strange-company/validation.yaml")
	if fxExists(filepath.Join(fixture, "red")) {
		fxOverlay(t, filepath.Join(fixture, "red"), r.ws)
		fxGit(t, r.home, r.ws, "add", "-A")
		fxGit(t, r.home, r.ws, "commit", "-q", "-m", "test(sc-184): red tests")
		r.red = fxGit(t, r.home, r.ws, "rev-parse", "HEAD")
	}
	if fxExists(filepath.Join(fixture, "head")) {
		fxOverlay(t, filepath.Join(fixture, "head"), r.ws)
		fxGit(t, r.home, r.ws, "add", "-A")
		fxGit(t, r.home, r.ws, "commit", "-q", "-m", "feat(sc-184): implementation")
	} else {
		fxGit(t, r.home, r.ws, "commit", "-q", "--allow-empty", "-m", "feat(sc-184): implementation")
	}
	r.head = fxGit(t, r.home, r.ws, "rev-parse", "HEAD")
	return r
}

func (r *fxRepo) suiteEnv(extra map[string]string) map[string]string {
	env := map[string]string{"FX_OUT": r.out, "FX_STATE": r.record, "GOCACHE": r.goCache}
	for k, v := range extra {
		env[k] = v
	}
	return env
}

// validationDoc is the RepositoryValidation document (JSON is YAML) committed at base.
func (r *fxRepo) validationDoc() []byte {
	var suites, checks []any
	for _, s := range r.cfg.Suites {
		report := map[string]any{"path": "-"}
		if s.ReportPath != "" {
			report["path"] = s.ReportPath
		}
		m := map[string]any{
			"name": s.Name, "adapter": s.Adapter, "language": s.Language, "argv": s.Argv, "report": report,
			"timeoutSeconds": 120, "phases": []string{"red", "green"}, "env": r.suiteEnv(s.Env),
		}
		if len(s.SelectArgv) > 0 {
			m["selectArgv"] = s.SelectArgv
		}
		suites = append(suites, m)
	}
	for _, c := range r.cfg.Checks {
		m := map[string]any{"name": c.Name, "argv": c.Argv, "timeoutSeconds": 120, "env": r.suiteEnv(nil)}
		if c.ToolingFrom != "" {
			m["toolingFrom"] = c.ToolingFrom
		}
		checks = append(checks, m)
	}
	spec := map[string]any{"image": "registry.example.org/ci@sha256:" + strings.Repeat("a", 64), "suites": suites}
	if len(checks) > 0 {
		spec["checks"] = checks
	}
	doc := map[string]any{
		"apiVersion": "strange.company/v1alpha1", "kind": "RepositoryValidation",
		"metadata": map[string]any{"name": "fx"}, "spec": map[string]any{"verification": spec},
	}
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		panic(err)
	}
	return append(b, '\n')
}

func fxGoSuite(env map[string]string) fxSuite {
	return fxSuite{
		Name: "unit", Adapter: "go-test-json", Language: "go",
		Argv: []string{"go", "test", "-count=1", "-json", "./..."}, Env: env,
		SelectArgv: []string{"-run", "{test_ids}"},
	}
}

func fxJUnitSuite(env map[string]string) fxSuite {
	return fxSuite{
		Name: "unit", Adapter: "junit-xml", Language: "go",
		Argv: []string{"go", "test", "-count=1", "./...", "-args", "-report={report}"}, ReportPath: "out/junit.xml", Env: env,
	}
}

// ---------------------------------------------------------------------------------------------------------------
// Running sc-verify

type fxRunOpts struct {
	Phase     string
	Baseline  string // "run" or "skip" (default skip)
	Protected string
	Expected  string // overrides SC_EXPECTED_SHA
	Hostile   bool   // the launching environment carries the evidence nonce and the intent id
	Extra     []string
	Unset     []string // names removed from the launching environment
}

type fxResult struct {
	Exit     int
	TimedOut bool
	Stdout   string
	Stderr   string
}

func fxExec(t testing.TB, bin, dir string, env, args []string) fxResult {
	t.Helper()
	tmp := t.TempDir()
	so, err := os.Create(filepath.Join(tmp, "stdout"))
	if err != nil {
		t.Fatal(err)
	}
	defer so.Close()
	se, err := os.Create(filepath.Join(tmp, "stderr"))
	if err != nil {
		t.Fatal(err)
	}
	defer se.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = dir, env, so, se
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = 10 * time.Second
	err = cmd.Run()
	res := fxResult{TimedOut: ctx.Err() != nil}
	var ee *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &ee):
		res.Exit = ee.ExitCode()
	default:
		t.Fatalf("cannot execute %s: %v", bin, err)
	}
	b1, _ := os.ReadFile(filepath.Join(tmp, "stdout"))
	b2, _ := os.ReadFile(filepath.Join(tmp, "stderr"))
	res.Stdout, res.Stderr = string(b1), string(b2)
	return res
}

func (r *fxRepo) runEnv(o fxRunOpts) []string {
	phase := o.Phase
	if phase == "" {
		phase = fxPhaseRed
	}
	baseline := o.Baseline
	if baseline == "" {
		baseline = "skip"
	}
	expected := o.Expected
	if expected == "" {
		expected = r.head
	}
	env := []string{
		"PATH=/usr/bin:/bin:/opt/fx-leak-path-" + r.leak, "HOME=" + r.home, "TMPDIR=" + r.tmp,
		"GOFLAGS=-tags=fxleak", "GOENV=" + r.goenvFile, "LEAK_SENTINEL=" + r.leak, "CI_REPO=" + fxRepoName,
		"SC_PHASE=" + phase, "SC_SUBJECT=tests", "SC_TASK_ID=" + fxTask, "SC_EXPECTED_SHA=" + expected,
		"SC_BASE_SHA=" + r.base, "SC_BASELINE=" + baseline,
	}
	if phase == fxPhaseGreen {
		env = append(env, "SC_RED_SHA="+r.red, "SC_PROTECTED_TESTS="+o.Protected)
	}
	if o.Hostile {
		env = append(env, "SC_EVIDENCE_NONCE="+r.nonce, "SC_INTENT_ID="+r.intent)
	}
	var kept []string
	for _, e := range env {
		drop := false
		for _, u := range o.Unset {
			drop = drop || strings.HasPrefix(e, u+"=")
		}
		if !drop {
			kept = append(kept, e)
		}
	}
	return append(kept, o.Extra...)
}

// run executes `sc-verify run` in the workspace.
func (r *fxRepo) run(o fxRunOpts) fxResult {
	r.t.Helper()
	args := []string{
		"run", "--validation", ".strange-company/validation.yaml", "--record", r.record,
		"--policy", filepath.Join(fxRoot(r.t), "policy", "verification.yaml"), "--child-path", r.childPath,
	}
	return fxExec(r.t, fxBinary(r.t), r.ws, r.runEnv(o), args)
}

// emit executes `sc-verify emit` in an empty directory with the nonce and the intent id, as the emit step would.
func (r *fxRepo) emit(nonce, intent string) fxResult {
	r.t.Helper()
	env := []string{"PATH=/usr/bin:/bin", "HOME=" + r.home}
	if nonce != "" {
		env = append(env, "SC_EVIDENCE_NONCE="+nonce)
	}
	if intent != "" {
		env = append(env, "SC_INTENT_ID="+intent)
	}
	return fxExec(r.t, fxBinary(r.t), r.t.TempDir(), env, []string{"emit", "--record", r.record, "--emit", "log"})
}

// ---------------------------------------------------------------------------------------------------------------
// The framed evidence block (plan 20 section 8.5) and the observed document

var (
	fxBeginRe = regexp.MustCompile(`^::sc-evidence-begin v1 intent=(\S+) nonce=([0-9a-f]{32}) phase=(red|green) sha=([0-9a-f]{40}) bytes=([0-9]+) sha256=([0-9a-f]{64})::$`)
	fxEndRe   = regexp.MustCompile(`^::sc-evidence-end v1 intent=(\S+)::$`)
)

type fxBlock struct {
	Intent, Nonce, Phase, Sha string
	Doc                       []byte
}

// fxParseBlock parses stdout strictly: exactly one begin and one end marker, 76-column base64, matching length
// and sha256, at most 262144 decoded bytes.
func fxParseBlock(out string) (fxBlock, error) {
	var b fxBlock
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	begin, end := -1, -1
	for i, l := range lines {
		if strings.HasPrefix(l, "::sc-evidence-") {
			switch {
			case fxBeginRe.MatchString(l) && begin < 0:
				begin = i
			case fxEndRe.MatchString(l) && end < 0 && begin >= 0:
				end = i
			default:
				return b, fmt.Errorf("ambiguous evidence marker line %d: %q", i, l)
			}
		}
	}
	if begin < 0 || end < 0 {
		return b, errors.New("no complete framed block")
	}
	m := fxBeginRe.FindStringSubmatch(lines[begin])
	b.Intent, b.Nonce, b.Phase, b.Sha = m[1], m[2], m[3], m[4]
	wantBytes, _ := strconv.Atoi(m[5])
	if e := fxEndRe.FindStringSubmatch(lines[end]); e[1] != b.Intent {
		return b, errors.New("end marker names another intent")
	}
	var enc strings.Builder
	for i, l := range lines[begin+1 : end] {
		if len(l) > 76 || (len(l) < 76 && begin+1+i != end-1) {
			return b, fmt.Errorf("base64 line %d has %d columns", i, len(l))
		}
		enc.WriteString(l)
	}
	doc, err := base64.StdEncoding.DecodeString(enc.String())
	if err != nil {
		return b, fmt.Errorf("base64: %v", err)
	}
	sum := sha256.Sum256(doc)
	if len(doc) != wantBytes || hex.EncodeToString(sum[:]) != m[6] || len(doc) > 262144 {
		return b, fmt.Errorf("length or sha256 mismatch (bytes %d, header %d)", len(doc), wantBytes)
	}
	b.Doc = doc
	return b, nil
}

type fxCounts struct {
	Total             int `json:"total"`
	Passed            int `json:"passed"`
	FailedAssertion   int `json:"failed_assertion"`
	FailedError       int `json:"failed_error"`
	StubUnimplemented int `json:"stub_unimplemented"`
	Skipped           int `json:"skipped"`
}

type fxRun struct {
	Tree        string   `json:"tree"`
	Commit      string   `json:"commit"`
	Suite       string   `json:"suite"`
	Adapter     string   `json:"adapter"`
	ExitCode    int      `json:"exit_code"`
	TimedOut    bool     `json:"timed_out"`
	BuildFailed bool     `json:"build_failed"`
	ReportFound bool     `json:"report_found"`
	Counts      fxCounts `json:"counts"`
}

type fxTest struct {
	ID         string `json:"id"`
	Suite      string `json:"suite"`
	StatusBase string `json:"status_base"`
	StatusHead string `json:"status_head"`
}

type fxIDSet struct {
	Count  int    `json:"count"`
	Sha256 string `json:"sha256"`
	Data   string `json:"data"`
}

type fxCheckRun struct {
	Name     string `json:"name"`
	ExitCode int    `json:"exit_code"`
	TimedOut bool   `json:"timed_out"`
	Required bool   `json:"required"`
}

type fxStatus struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

type fxObserved struct {
	TaskID         string `json:"task_id"`
	Repository     string `json:"repository"`
	Phase          string `json:"phase"`
	Subject        string `json:"subject"`
	IntentID       string `json:"intent_id"`
	BaseSha        string `json:"base_sha"`
	HeadSha        string `json:"head_sha"`
	ValidationBlob string `json:"validation_blob"`
	ToolchainImage string `json:"toolchain_image"`
	Verifier       struct {
		Version      string `json:"version"`
		BinarySha256 string `json:"binary_sha256"`
	} `json:"verifier"`
	Runs       []fxRun             `json:"runs"`
	Tests      []fxTest            `json:"tests"`
	Truncated  bool                `json:"tests_truncated"`
	TestIDSets map[string]*fxIDSet `json:"test_id_sets"`
	Checks     []fxCheckRun        `json:"checks"`
	RedReplay  *struct {
		RedCommit string     `json:"red_commit"`
		Tests     []fxStatus `json:"tests"`
	} `json:"red_replay"`
}

type fxDoc struct {
	SchemaVersion string     `json:"schema_version"`
	Document      string     `json:"document"`
	Stage         string     `json:"stage"`
	Observed      fxObserved `json:"observed"`
}

// fxEmitDoc runs emit, requires exit 0 and one well-formed block, checks the header against the intent, validates
// the document against its contract and returns it.
func (r *fxRepo) emitDoc(phase string) (fxDoc, fxBlock) {
	r.t.Helper()
	res := r.emit(r.nonce, r.intent)
	if res.Exit != 0 {
		r.t.Fatalf("emit exit %d; stdout %q stderr %q", res.Exit, fxTrunc(res.Stdout), fxTrunc(res.Stderr))
	}
	blk, err := fxParseBlock(res.Stdout)
	if err != nil {
		r.t.Fatalf("emit: %v\nstdout: %s", err, fxTrunc(res.Stdout))
	}
	if blk.Intent != r.intent || blk.Nonce != r.nonce || blk.Phase != phase || blk.Sha != r.head {
		r.t.Errorf("emit header = intent %q nonce %q phase %q sha %q; want %q %q %q %q",
			blk.Intent, blk.Nonce, blk.Phase, blk.Sha, r.intent, r.nonce, phase, r.head)
	}
	if n := strings.Count(res.Stdout, r.nonce); n != 1 {
		r.t.Errorf("the nonce appears %d times in emit's output, want exactly once (the begin marker)", n)
	}
	if strings.Contains(res.Stderr, r.nonce) {
		r.t.Errorf("the nonce appears in emit's stderr")
	}
	if len(res.Stdout) > 1<<20 {
		r.t.Errorf("emit printed %d bytes, want under 1 MiB", len(res.Stdout))
	}
	var d fxDoc
	if err := json.Unmarshal(blk.Doc, &d); err != nil {
		r.t.Fatalf("document is not JSON: %v", err)
	}
	fxValidate(r.t, blk.Doc, phase+"-result")
	if d.Document != phase+"-result" || d.Stage != "observed" || d.Observed.Phase != phase {
		r.t.Errorf("document = %q stage %q phase %q, want %s-result observed %s", d.Document, d.Stage, d.Observed.Phase, phase, phase)
	}
	return d, blk
}

func fxTrunc(s string) string {
	if len(s) > 1500 {
		return s[:1500] + "..."
	}
	return s
}

func fxValidate(t testing.TB, doc []byte, schema string) {
	t.Helper()
	schemas, err := contracts.CompileSchemas(filepath.Join(fxRoot(t), "contracts"))
	if err != nil {
		t.Fatal(err)
	}
	s := schemas[schema]
	if s == nil {
		t.Fatalf("no %s schema", schema)
	}
	var v any
	if err := json.Unmarshal(doc, &v); err != nil {
		t.Fatal(err)
	}
	if err := s.Validate(v); err != nil {
		t.Errorf("the observed document does not validate against %s: %v", schema, err)
	}
}

// fxDecodeIDSet decodes a TestIdSet independently of the library: base64, gzip, newline separated.
func fxDecodeIDSet(t testing.TB, s *fxIDSet) []string {
	t.Helper()
	if s == nil {
		t.Fatal("test id set missing")
	}
	raw, err := base64.StdEncoding.DecodeString(s.Data)
	if err != nil {
		t.Fatalf("test id set: %v", err)
	}
	zr, err := fxGunzip(raw)
	if err != nil {
		t.Fatalf("test id set: %v", err)
	}
	sum := sha256.Sum256(zr)
	if hex.EncodeToString(sum[:]) != s.Sha256 {
		t.Errorf("test id set sha256 does not match its text")
	}
	if len(zr) == 0 {
		return nil
	}
	ids := strings.Split(string(zr), "\n")
	if len(ids) != s.Count {
		t.Errorf("test id set count %d, but %d ids", s.Count, len(ids))
	}
	return ids
}

func (d fxDoc) test(id string) (fxTest, bool) {
	for _, x := range d.Observed.Tests {
		if x.ID == id {
			return x, true
		}
	}
	return fxTest{}, false
}

func (d fxDoc) run(tree, suite string) (fxRun, bool) {
	for _, x := range d.Observed.Runs {
		if x.Tree == tree && x.Suite == suite {
			return x, true
		}
	}
	return fxRun{}, false
}

// ---------------------------------------------------------------------------------------------------------------
// Observations of the code under test ($FX_OUT/env.txt)

type fxProc struct {
	Pid, PPid, Pgrp, Sid int
	Exe                  string
}

type fxEnvBlock struct {
	Cwd      string
	Env      map[string]string
	ProcEnv  map[string]string
	HomeDir  bool
	TmpDir   bool
	GitAbove bool
	Procs    []fxProc
}

func fxReadEnvBlocks(t testing.TB, path string) []fxEnvBlock {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the code under test recorded no environment: %v", err)
	}
	var out []fxEnvBlock
	var cur *fxEnvBlock
	for _, l := range strings.Split(string(b), "\n") {
		switch {
		case l == "BEGIN":
			out = append(out, fxEnvBlock{Env: map[string]string{}, ProcEnv: map[string]string{}})
			cur = &out[len(out)-1]
		case cur == nil:
		case strings.HasPrefix(l, "cwd="):
			cur.Cwd = strings.TrimPrefix(l, "cwd=")
		case strings.HasPrefix(l, "ENV "):
			k, v, _ := strings.Cut(strings.TrimPrefix(l, "ENV "), "=")
			cur.Env[k] = v
		case strings.HasPrefix(l, "PROCENV "):
			k, v, _ := strings.Cut(strings.TrimPrefix(l, "PROCENV "), "=")
			cur.ProcEnv[k] = v
		case strings.HasPrefix(l, "HOMESTAT "):
			cur.HomeDir = strings.HasSuffix(l, "dir=true")
		case strings.HasPrefix(l, "TMPSTAT "):
			cur.TmpDir = strings.HasSuffix(l, "dir=true")
		case strings.HasPrefix(l, "GITABOVE "):
			cur.GitAbove = strings.HasSuffix(l, "true")
		case strings.HasPrefix(l, "PROC "):
			var p fxProc
			if _, err := fmt.Sscanf(strings.TrimPrefix(l, "PROC "), "pid=%d ppid=%d pgrp=%d sid=%d exe=%s", &p.Pid, &p.PPid, &p.Pgrp, &p.Sid, &p.Exe); err == nil {
				cur.Procs = append(cur.Procs, p)
			}
		}
	}
	return out
}

// ---------------------------------------------------------------------------------------------------------------
// Processes and files

// fxProcsWith returns the pids of the processes whose command line contains marker.
func fxProcsWith(marker string) []int {
	var pids []int
	ents, _ := os.ReadDir("/proc")
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid == os.Getpid() {
			continue
		}
		b, err := os.ReadFile("/proc/" + e.Name() + "/cmdline")
		if err == nil && bytes.Contains(b, []byte(marker)) {
			pids = append(pids, pid)
		}
	}
	return pids
}

func fxKillAll(marker string) {
	for _, pid := range fxProcsWith(marker) {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
}

// fxFilesWith lists the files below roots (symlinks not followed) whose content contains needle.
func fxFilesWith(roots []string, needle string) []string {
	var hits []string
	for _, root := range roots {
		_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil || !d.Type().IsRegular() {
				return nil
			}
			if b, err := os.ReadFile(p); err == nil && bytes.Contains(b, []byte(needle)) {
				hits = append(hits, p)
			}
			return nil
		})
	}
	sort.Strings(hits)
	return hits
}

func fxRegularFiles(root string) []string {
	var out []string
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			out = append(out, p)
		}
		return nil
	})
	sort.Strings(out)
	return out
}

func fxSHA256(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func fxSleep(d time.Duration) { time.Sleep(d) }

func fxGunzip(raw []byte) ([]byte, error) {
	zr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	return io.ReadAll(zr)
}
