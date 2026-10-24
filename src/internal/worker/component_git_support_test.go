package worker_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"strange.company/sc/internal/clock"
	"strange.company/sc/internal/fakeharness"
	"strange.company/sc/internal/worker"
)

// Layer-1 component harness for the Git pipeline of sc-worker (plan 27 sections 14.4 to 14.12). It runs the real
// worker.Run as a separate process (helper-process re-exec) against
//   - a recording stub Runner (stubRunner of component_exit_test.go),
//   - a fake ssh program that asserts the plan 27 section 10 options and serves a local bare repository built from
//     test/e2e/fake-target with the real /usr/bin/git,
//   - a test-owned shim that speaks the plan 27 section 7.4 file protocol (the production shim is slice S4),
//   - the cooperative fake harness (fakeharness.Run) or a data-driven test double for behaviours that are not rows
//     of the plan 28 scenario table. The double is chosen by the shim, never by packet content.

const (
	gitHelperEnv  = "SC_TEST_WORKER_GIT_CONFIG" // test-only; never read by production code
	helperKindEnv = "SC_TEST_HELPER"
	forgejoHost   = "forgejo-ssh.forgejo.svc.cluster.local"
	deployKeyBody = "-----BEGIN OPENSSH PRIVATE KEY-----\n" +
		"b3BlbnNzaC1rZXktdjEAAAAABG5vbmUAAAAEbm9uZQAAAAAAAAABAAAAMwAAAAtzc2gtZW\n" +
		"QyNTUxOQAAACBGQUtFS0VZRk9SVEVTVFNPTkxZMDEyMzQ1Njc4OWFiY2RlZgAAAJhGQUtF\n" +
		"-----END OPENSSH PRIVATE KEY-----\n"
	agentsMDText = "# fake-target\n\nBase instructions for the fixture repository.\n"
	canaryName   = "pwned"
)

// ---------------------------------------------------------------------------------------------------------------
// git helpers

func gitEnvFor(extra ...string) []string {
	env := []string{
		"PATH=" + os.Getenv("PATH"), "HOME=/nonexistent-home", "LC_ALL=C",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid",
		"GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid",
		"GIT_AUTHOR_DATE=2026-10-13T12:00:00Z", "GIT_COMMITTER_DATE=2026-10-13T12:00:00Z",
	}
	return append(env, extra...)
}

func gitIn(t testing.TB, dir string, args ...string) string {
	t.Helper()
	out, err := gitTry(dir, nil, args...)
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return out
}

func gitTry(dir string, stdin []byte, args ...string) (string, error) {
	cmd := exec.Command("/usr/bin/git", args...)
	cmd.Dir = dir
	cmd.Env = gitEnvFor()
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%w: %s", err, strings.TrimSpace(errb.String()))
	}
	return strings.TrimRight(out.String(), "\n"), nil
}

func writeTestFile(t testing.TB, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func copyTree(t testing.TB, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

// ---------------------------------------------------------------------------------------------------------------
// fixture: a bare repository sc-e2e/fake-target built from test/e2e/fake-target

type fixture struct {
	t    testing.TB
	root string
	work string
	bare string // <root>/git/sc-e2e/fake-target.git
	base string // head of main
}

// buildFixture builds the bare fixture (plan 28 section 14.5). The files that match protected globs of this
// repository (AGENTS.md, and for the hostile variant .gitattributes) are written here, not committed (D7).
func buildFixture(t testing.TB, hostile bool) *fixture {
	t.Helper()
	root := t.TempDir()
	fx := &fixture{t: t, root: root, work: filepath.Join(root, "work"), bare: filepath.Join(root, "git", "sc-e2e", "fake-target.git")}
	copyTree(t, filepath.Join(repoRoot(t), "test", "e2e", "fake-target"), fx.work)
	writeTestFile(t, filepath.Join(fx.work, "AGENTS.md"), agentsMDText, 0o644)
	if hostile {
		writeTestFile(t, filepath.Join(fx.work, ".gitattributes"), "* filter=pwn\n", 0o644)
	}
	gitIn(t, fx.work, "init", "-q", "-b", "main")
	gitIn(t, fx.work, "add", "-A")
	gitIn(t, fx.work, "commit", "-q", "-m", "Base fixture commit")
	fx.base = gitIn(t, fx.work, "rev-parse", "HEAD")
	if err := os.MkdirAll(filepath.Dir(fx.bare), 0o755); err != nil {
		t.Fatal(err)
	}
	gitIn(t, root, "init", "-q", "--bare", "-b", "main", fx.bare)
	gitIn(t, fx.work, "push", "-q", fx.bare, "main:refs/heads/main")
	return fx
}

// commitOn creates a commit with the given files on top of parent in the scratch repository and returns its id.
func (fx *fixture) commitOn(parent string, files map[string]string, msg string) string {
	fx.t.Helper()
	gitIn(fx.t, fx.work, "checkout", "-q", "--detach", parent)
	for p, c := range files {
		writeTestFile(fx.t, filepath.Join(fx.work, p), c, 0o644)
	}
	gitIn(fx.t, fx.work, "add", "-A")
	gitIn(fx.t, fx.work, "commit", "-q", "-m", msg)
	return gitIn(fx.t, fx.work, "rev-parse", "HEAD")
}

// setBranch publishes sha as refs/heads/<name> in the bare repository.
func (fx *fixture) setBranch(name, sha string) {
	fx.t.Helper()
	gitIn(fx.t, fx.work, "push", "-q", "--force", fx.bare, sha+":refs/heads/"+name)
}

func (fx *fixture) refList() string {
	fx.t.Helper()
	return gitIn(fx.t, fx.bare, "for-each-ref", "--format=%(refname) %(objectname)")
}

func (fx *fixture) refsDigest() string {
	sum := sha256.Sum256([]byte(fx.refList()))
	return hex.EncodeToString(sum[:])
}

func (fx *fixture) head(branch string) string {
	out, err := gitTry(fx.bare, nil, "rev-parse", "--verify", "-q", "refs/heads/"+branch)
	if err != nil {
		return ""
	}
	return out
}

// redCommit puts a tester-style red commit on top of base (the calc/add_ac_test.go of plan 28 section 14.4).
func (fx *fixture) redCommit() string {
	return fx.commitOn(fx.base, map[string]string{"calc/add_ac_test.go": addACTestSrc}, "Red commit")
}

const addACTestSrc = "package calc\n\nimport \"testing\"\n\nfunc TestAddAC1(t *testing.T) {\n\tif Add(2, 3) != 5 {\n\t\tt.Fatal(\"Add(2, 3) != 5\")\n\t}\n}\n\nfunc TestAddAC2(t *testing.T) {\n\tif Add(-1, 1) != 0 {\n\t\tt.Fatal(\"Add(-1, 1) != 0\")\n\t}\n}\n"

// ---------------------------------------------------------------------------------------------------------------
// packet

type pktOpts struct {
	role           string // default tester
	mode           string // create (default), continue, readonly
	start          string // default fx.base
	fetchRef       string // default refs/heads/main
	policy         map[string]any
	maxResultBytes int
}

func rolePrefix(role string) string {
	switch role {
	case "implementer":
		return "feat"
	case "researcher", "planner":
		return "chore"
	}
	return "test"
}

func defaultPolicy(role string) map[string]any {
	protected := []any{".strange-company/**", ".woodpecker/**", "AGENTS.md"}
	p := map[string]any{
		"protected_paths": protected, "diff_mode": "allowlist", "diff_allow": []any{"**/*_test.go", "testdata/**"},
		"diff_deny": protected, "max_files_changed": 40, "max_diff_bytes": 262144, "allow_deletions": false,
		"validation_commands": []any{}, "forbidden_actions": []any{},
	}
	if role == "implementer" {
		p["diff_mode"] = "denylist"
		p["diff_allow"] = []any{}
		p["diff_deny"] = []any{".strange-company/**", ".woodpecker/**", "AGENTS.md", "**/*_test.go"}
	}
	return p
}

// jcsBytes is the RFC 8785 canonical JSON of v for the ASCII-only documents of these tests.
func jcsBytes(t testing.TB, v any) []byte {
	t.Helper()
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		t.Fatal(err)
	}
	return bytes.TrimRight(buf.Bytes(), "\n")
}

var contextSections = []any{
	map[string]any{
		"id": "task-spec", "kind": "task_spec", "layer": "task", "authority": "authoritative", "title": "Task",
		"media_type": "text/markdown", "materialize_as": "10-task-spec.md", "truncated": false, "original_bytes": 41,
		"content": "Make Add return the sum of its arguments.",
		"sources": []any{map[string]any{"kind": "task_record", "ref": "sc-1", "digest": "sha256:" + strings.Repeat("c", 64)}},
	},
	map[string]any{
		"id": "acceptance-criteria", "kind": "acceptance_criteria", "layer": "task", "authority": "authoritative",
		"title": "Acceptance criteria", "media_type": "application/json", "materialize_as": "20-acceptance-criteria.json",
		"truncated": false, "original_bytes": 59,
		"content": `[{"id":"AC-1","text":"Add returns the sum of two numbers."}]`,
		"sources": []any{map[string]any{"kind": "task_record", "ref": "sc-1", "digest": "sha256:" + strings.Repeat("c", 64)}},
	},
}

// gitPacket builds a schema-valid packet for task sc-1 / execution sc-1-x1 against the fixture.
func gitPacket(t testing.TB, fx *fixture, o pktOpts) map[string]any {
	t.Helper()
	if o.role == "" {
		o.role = "tester"
	}
	if o.mode == "" {
		o.mode = "create"
	}
	if o.start == "" {
		o.start = fx.base
	}
	if o.fetchRef == "" {
		o.fetchRef = "refs/heads/main"
	}
	p := examplePacket(t)
	set := func(path string, kv map[string]any) {
		m := p
		for _, k := range strings.Split(path, ".") {
			m = m[k].(map[string]any)
		}
		for k, v := range kv {
			m[k] = v
		}
	}
	set("task", map[string]any{"task_id": "sc-1", "repository": "sc-e2e/fake-target", "scoped_acceptance_criteria": []any{"AC-1"}})
	p["task"].(map[string]any)["acceptance_criteria"] = []any{map[string]any{"id": "AC-1", "text": "Add returns the sum of two numbers."}}
	set("execution", map[string]any{
		"execution_id": "sc-1-x1", "role": o.role, "deadline_at": "2026-10-13T20:00:00Z",
	})
	prefix := rolePrefix(o.role)
	set("git", map[string]any{
		"clone_url": "ssh://git@" + forgejoHost + ":22/sc-e2e/fake-target.git", "repository": "sc-e2e/fake-target",
		"fetch_ref": o.fetchRef, "base_commit": fx.base, "start_commit": o.start, "branch": "agent/sc-1",
		"branch_mode": o.mode, "push_allowed": o.mode != "readonly",
	})
	set("git.commit", map[string]any{
		"subject_prefix": prefix + "(sc-1): ", "author_name": "Strange Company " + o.role + " (sc-1-x1)",
		"author_email": "sc-1-x1@agents.strange-company.invalid",
		"trailers":     map[string]any{"SC-Task": "sc-1", "SC-Execution": "sc-1-x1", "SC-Role": o.role, "SC-Base": fx.base},
	})
	set("gateway", map[string]any{
		"base_urls": map[string]any{"fake": "http://model-gateway.strange-company.svc.cluster.local:8080/p/fake"},
		"provider":  "fake", "model": "fake-model", "allowed_models": []any{"fake-model"}, "api_family": "fake",
	})
	delete(p["gateway"].(map[string]any), "max_output_tokens")
	p["harness"] = map[string]any{"adapter": "fake", "cli_version": "0.1.0", "fake_scenario": "success"}
	limits := map[string]any{"harness_timeout_seconds": 3600, "heartbeat_interval_seconds": 10}
	if o.maxResultBytes > 0 {
		limits["max_result_bytes"] = o.maxResultBytes
	}
	set("limits", limits)
	pol := defaultPolicy(o.role)
	for k, v := range o.policy {
		pol[k] = v
	}
	p["policy"] = pol
	sections := jsonRound(t, contextSections).([]any)
	set("context", map[string]any{
		"task_id": "sc-1", "execution_id": "sc-1-x1", "role": o.role, "base_commit": fx.base, "sections": sections,
		"packet_id": "ctx-sc-1-x1",
	})
	sum := sha256.Sum256(jcsBytes(t, sections))
	p["context"].(map[string]any)["digest"] = "sha256:" + hex.EncodeToString(sum[:])
	validate(t, "worker-input", p)
	return p
}

// ---------------------------------------------------------------------------------------------------------------
// helper-process entries

func helperArgs() []string {
	for i, a := range os.Args {
		if a == "--" {
			return os.Args[i+1:]
		}
	}
	return nil
}

// stillClock is the worker clock of the Git component tests: Now never moves, and Sleep yields at most 20 ms of
// real time, so no outcome depends on how long git or the harness takes.
type stillClock struct{ *clock.Fake }

func (c stillClock) Sleep(ctx context.Context, d time.Duration) error {
	if d > 20*time.Millisecond {
		d = 20 * time.Millisecond
	}
	return clock.Real{}.Sleep(ctx, d)
}

type gitHelperConfig struct {
	PrivateDir, HarnessSecretsDir, HomeDir, TmpDir, SSHProgram, ClockStart string
	RealClock                                                              bool   // S4: real elapsed time is the subject
	SleepLog                                                               string // S6a: file that records every Sleep duration
}

// TestHelperProcess_ScWorkerGit is the child side of the Git component harness; it does nothing in a normal run.
func TestHelperProcess_ScWorkerGit(t *testing.T) {
	raw := os.Getenv(gitHelperEnv)
	if raw == "" {
		t.Skip("helper process only")
	}
	var hc gitHelperConfig
	if err := json.Unmarshal([]byte(raw), &hc); err != nil {
		t.Fatal(err)
	}
	start, err := time.Parse(time.RFC3339, hc.ClockStart)
	if err != nil {
		t.Fatal(err)
	}
	cfg := worker.DefaultConfig()
	cfg.PrivateDir, cfg.HarnessSecretsDir, cfg.HomeDir, cfg.TmpDir = hc.PrivateDir, hc.HarnessSecretsDir, hc.HomeDir, hc.TmpDir
	cfg.SSHProgram = hc.SSHProgram
	if !hc.RealClock {
		cfg.Clock = stillClock{clock.NewFake(start)}
	}
	if hc.SleepLog != "" {
		cfg.Clock = sleepRecorder{Clock: cfg.Clock, path: hc.SleepLog}
	}
	cfg.Stdout, cfg.Stderr = os.Stdout, os.Stderr
	os.Exit(worker.Run(cfg))
}

type sshConfig struct {
	SecretsDir, PrivateDir, FixtureRoot, KeyContent, ViolationsFile string
}

var sshCmdRe = regexp.MustCompile(`^(?:git-|git )(upload-pack|receive-pack) '(/[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+\.git)'$`)

// TestHelperProcess_FakeSSH is the fake ssh program: it asserts the plan 27 section 10 options, fails closed on
// any deviation and otherwise serves the bare fixture with git upload-pack / receive-pack.
func TestHelperProcess_FakeSSH(t *testing.T) {
	if os.Getenv(helperKindEnv) != "fake-ssh" {
		t.Skip("helper process only")
	}
	os.Exit(fakeSSHMain(helperArgs()))
}

func fakeSSHMain(args []string) int {
	if len(args) < 1 {
		return 255
	}
	var cfg sshConfig
	b, err := os.ReadFile(args[0])
	if err != nil || json.Unmarshal(b, &cfg) != nil {
		fmt.Fprintln(os.Stderr, "fake-ssh: no config")
		return 255
	}
	rest := args[1:]
	var key, port, login string
	opts := map[string]string{}
	var pos []string
	for i := 0; i < len(rest); i++ {
		a := rest[i]
		if len(pos) > 0 {
			pos = append(pos, a)
			continue
		}
		next := func() string {
			if i+1 < len(rest) {
				i++
				return rest[i]
			}
			return ""
		}
		switch {
		case a == "-i":
			key = next()
		case a == "-p":
			port = next()
		case a == "-l":
			login = next()
		case a == "-o":
			k, v, _ := strings.Cut(next(), "=")
			opts[strings.ToLower(k)] = v
		case strings.HasPrefix(a, "-o") && len(a) > 2:
			k, v, _ := strings.Cut(a[2:], "=")
			opts[strings.ToLower(k)] = v
		case strings.HasPrefix(a, "-"):
		default:
			pos = append(pos, a)
		}
	}
	var bad []string
	if len(pos) < 2 {
		bad = append(bad, "missing host or command")
		pos = append(pos, "", "")
	}
	host := pos[0]
	if u, h, ok := strings.Cut(host, "@"); ok {
		host, login = h, u
	}
	command := strings.Join(pos[1:], " ")
	if host != forgejoHost {
		bad = append(bad, "host is "+host)
	}
	if login != "git" {
		bad = append(bad, "user is "+login)
	}
	if port != "" && port != "22" {
		bad = append(bad, "port is "+port)
	}
	wantKey := filepath.Join(cfg.PrivateDir, "home", ".ssh", "id_ed25519")
	if key != wantKey {
		bad = append(bad, "-i is "+key+", want "+wantKey)
	} else if st, err := os.Stat(key); err != nil {
		bad = append(bad, "key file: "+err.Error())
	} else {
		if st.Mode().Perm() != 0o600 {
			bad = append(bad, fmt.Sprintf("key file mode %o, want 600", st.Mode().Perm()))
		}
		if kb, _ := os.ReadFile(key); string(kb) != cfg.KeyContent {
			bad = append(bad, "key file content differs from the deploy key")
		}
	}
	for k, want := range map[string]string{
		"identitiesonly": "yes", "stricthostkeychecking": "yes", "batchmode": "yes",
		"userknownhostsfile": filepath.Join(cfg.SecretsDir, "known_hosts"),
	} {
		if opts[k] != want {
			bad = append(bad, fmt.Sprintf("-o %s=%q, want %q", k, opts[k], want))
		}
	}
	if kh, err := os.ReadFile(filepath.Join(cfg.SecretsDir, "known_hosts")); err != nil {
		bad = append(bad, "known_hosts: "+err.Error())
	} else {
		found := false
		for _, l := range strings.Split(string(kh), "\n") {
			if f := strings.Fields(l); len(f) > 0 && f[0] == forgejoHost {
				found = true
			}
		}
		if !found {
			bad = append(bad, "host not in known_hosts")
		}
	}
	m := sshCmdRe.FindStringSubmatch(command)
	if m == nil {
		bad = append(bad, "unexpected remote command "+command)
	}
	if len(bad) > 0 {
		msg := "fake-ssh: refused: " + strings.Join(bad, "; ")
		fmt.Fprintln(os.Stderr, msg)
		if f, err := os.OpenFile(cfg.ViolationsFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
			fmt.Fprintln(f, msg)
			_ = f.Close()
		}
		return 255
	}
	dir := filepath.Join(cfg.FixtureRoot, filepath.FromSlash(strings.TrimPrefix(m[2], "/")))
	cmd := exec.Command("/usr/bin/git", m[1], dir)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return ee.ExitCode()
		}
		return 255
	}
	return 0
}

// harnessOp is one step of the data-driven harness double.
type harnessOp struct {
	Op      string   `json:"op"` // write, delete, commit, hostile, record-head, commondir, parent-repo, replace-head
	Path    string   `json:"path,omitempty"`
	Content string   `json:"content,omitempty"`
	Msg     string   `json:"msg,omitempty"`
	N       int      `json:"n,omitempty"`
	Args    []string `json:"args,omitempty"` // git: arguments after the fixed -C/-c prefix; write-hex: Content is hex
}

// TestHelperProcess_FakeHarness is the harness child started by the test shim: the cooperative fake harness
// (mode real) or the data-driven double (mode ops).
func TestHelperProcess_FakeHarness(t *testing.T) {
	if os.Getenv(helperKindEnv) != "fake-harness" {
		t.Skip("helper process only")
	}
	args := helperArgs()
	if os.Getenv("SC_TEST_HARNESS_MODE") == "real" {
		os.Exit(fakeharness.Run(args, os.Getenv, os.Stdout, os.Stderr))
	}
	os.Exit(harnessDouble(args))
}

func harnessDouble(args []string) int {
	flags := map[string]string{}
	for i := 0; i+1 < len(args); i += 2 {
		flags[strings.TrimPrefix(args[i], "--")] = args[i+1]
	}
	repo, out, role := flags["repo"], flags["out"], flags["role"]
	var ops []harnessOp
	if raw := os.Getenv("SC_TEST_HARNESS_OPS"); raw != "" {
		if err := json.Unmarshal([]byte(raw), &ops); err != nil {
			fmt.Fprintln(os.Stderr, "double: ops:", err)
			return 2
		}
	}
	gitH := func(args ...string) (string, error) {
		cmd := exec.Command("/usr/bin/git", append([]string{"-C", repo, "-c", "user.name=Harness", "-c", "user.email=harness@example.invalid", "-c", "commit.gpgsign=false"}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
		b, err := cmd.CombinedOutput()
		return strings.TrimSpace(string(b)), err
	}
	fmt.Println(`{"type":"sc_fake_step","step":"double_start"}`)
	lingerStop, lingerSwap := "", ""
	// S5c: @repo@, @out@, @ws@, @shim@, @private@ and @secrets@ stand for the directories of the run; a relative path
	// is relative to the repository.
	ws := filepath.Dir(out)
	expand := func(s string) string {
		s = strings.NewReplacer("@repo@", repo, "@out@", out, "@ws@", ws, "@shim@", filepath.Join(ws, ".sc-shim"),
			"@private@", os.Getenv("SC_TEST_PRIVATE_DIR"), "@secrets@", os.Getenv("SC_TEST_SECRETS_DIR")).Replace(s)
		if !filepath.IsAbs(s) {
			s = filepath.Join(repo, s)
		}
		return s
	}
	claimDone := false
	var finishClaim func() int
	pre, post := ops, []harnessOp(nil)
	for i, op := range ops {
		if op.Op == "write-claim" {
			pre, post = ops[:i], ops[i+1:]
			break
		}
	}
	runOps := func(list []harnessOp) int {
		for _, op := range list {
			switch op.Op {
			case "wait-stream":
				// S5c review 2 (B1): wait (bounded) until the shim's stream file holds op.Content.
				sp := filepath.Join(ws, ".sc-shim", "stream-1.log")
				wctx, wcancel := context.WithTimeout(context.Background(), 20*time.Second)
				found := false
				for !found {
					b, _ := os.ReadFile(sp)
					if found = strings.Contains(string(b), op.Content); found {
						break
					}
					if (clock.Real{}).Sleep(wctx, 5*time.Millisecond) != nil {
						break
					}
				}
				wcancel()
				if !found {
					fmt.Fprintln(os.Stderr, "double: wait-stream timed out:", op.Content)
					return 2
				}
			case "write-claim":
				if c := finishClaim(); c != 0 {
					return c
				}
				claimDone = true
			case "symlink":
				// S5c: replace op.Path by a symbolic link to op.Content.
				p := expand(op.Path)
				_ = os.RemoveAll(p)
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil || os.Symlink(expand(op.Content), p) != nil {
					fmt.Fprintln(os.Stderr, "double: symlink", op.Path)
					return 2
				}
			case "mkfifo":
				p := expand(op.Path)
				_ = os.RemoveAll(p)
				if err := syscall.Mkfifo(p, 0o644); err != nil {
					fmt.Fprintln(os.Stderr, "double: mkfifo:", err)
					return 2
				}
			case "pause":
				_ = clock.Real{}.Sleep(context.Background(), time.Duration(op.N)*time.Millisecond)
			case "snapshot-private":
				b, _ := json.Marshal(hashPrivate(os.Getenv("SC_TEST_PRIVATE_DIR")))
				if err := os.WriteFile(filepath.Join(out, "priv-before.json"), b, 0o644); err != nil {
					fmt.Fprintln(os.Stderr, "double: snapshot:", err)
					return 2
				}
			case "stream-raw", "stream-fill":
				// S5c: append op.Content (stream-fill: op.N copies of it, in one write) to the shim stream file itself.
				f, err := os.OpenFile(filepath.Join(ws, ".sc-shim", "stream-1.log"), os.O_WRONLY|os.O_APPEND, 0)
				if err != nil {
					fmt.Fprintln(os.Stderr, "double: stream:", err)
					return 2
				}
				data := op.Content
				if op.Op == "stream-fill" {
					data = strings.Repeat(op.Content, op.N)
				}
				_, err = f.WriteString(data)
				_ = f.Close()
				if err != nil {
					fmt.Fprintln(os.Stderr, "double: stream:", err)
					return 2
				}
			case "forge-start-tree", "forge-start-commit":
				// S5a-N1: overwrite the loose object of start_commit's root tree (or of start_commit) in the shared object
				// store with the object of the harness's staged work, so that a diff against start_commit that reads the
				// shared store sees the staged state (or fails on the hash mismatch).
				if o, err := gitH("add", "-A"); err != nil {
					fmt.Fprintln(os.Stderr, "double: add:", o)
					return 2
				}
				newTree, err := gitH("write-tree")
				if err != nil {
					fmt.Fprintln(os.Stderr, "double: write-tree:", newTree)
					return 2
				}
				victim, _ := gitH("rev-parse", "HEAD^{tree}")
				forged := newTree
				if op.Op == "forge-start-commit" {
					victim, _ = gitH("rev-parse", "HEAD")
					if forged, err = gitH("commit-tree", newTree, "-m", "forged start"); err != nil {
						fmt.Fprintln(os.Stderr, "double: commit-tree:", forged)
						return 2
					}
				}
				loose := func(sha string) string { return filepath.Join(repo, ".git", "objects", sha[:2], sha[2:]) }
				raw, err := os.ReadFile(loose(forged))
				if err != nil {
					fmt.Fprintln(os.Stderr, "double: forged object is not loose:", err)
					return 2
				}
				if _, err := os.Lstat(loose(victim)); err != nil {
					fmt.Fprintln(os.Stderr, "double: the start object is not loose:", err)
					return 2
				}
				_ = os.Remove(loose(victim))
				if err := os.WriteFile(loose(victim), raw, 0o444); err != nil {
					fmt.Fprintln(os.Stderr, "double: forge:", err)
					return 2
				}
			case "write":
				p := expand(op.Path)
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil || os.WriteFile(p, []byte(op.Content), 0o644) != nil {
					fmt.Fprintln(os.Stderr, "double: write", op.Path)
					return 2
				}
			case "write-hex":
				// S5a review: write raw bytes (hex in Content), for binary and non-UTF-8 files.
				raw, err := hex.DecodeString(op.Content)
				p := filepath.Join(repo, op.Path)
				if err != nil || os.MkdirAll(filepath.Dir(p), 0o755) != nil || os.WriteFile(p, raw, 0o644) != nil {
					fmt.Fprintln(os.Stderr, "double: write-hex", op.Path)
					return 2
				}
			case "git":
				// S5a review: run git with op.Args in the workspace as the harness (pack-refs, checkout --orphan, ...).
				if o, err := gitH(op.Args...); err != nil {
					fmt.Fprintln(os.Stderr, "double: git:", o)
					return 2
				}
			case "delete":
				if err := os.Remove(filepath.Join(repo, op.Path)); err != nil {
					fmt.Fprintln(os.Stderr, "double: delete:", err)
					return 2
				}
			case "commit":
				if o, err := gitH("add", "-A"); err != nil {
					fmt.Fprintln(os.Stderr, "double: add:", o)
					return 2
				}
				if o, err := gitH("commit", "-q", "--no-verify", "-m", op.Msg); err != nil {
					fmt.Fprintln(os.Stderr, "double: commit:", o)
					return 2
				}
			case "record-head":
				sha, _ := gitH("rev-parse", "HEAD")
				branch, err := gitH("symbolic-ref", "-q", "--short", "HEAD")
				if err != nil {
					branch = "DETACHED"
				}
				_ = os.WriteFile(filepath.Join(out, "head.txt"), []byte(sha+"\n"+branch+"\n"), 0o644)
			case "commondir":
				// F1: .git/commondir names a directory the harness controls; its config carries the hostile code.
				canary := os.Getenv("SC_TEST_CANARY")
				evil := filepath.Join(filepath.Dir(filepath.Dir(repo)), "common-evil")
				if o, err := exec.Command("cp", "-a", filepath.Join(repo, ".git"), evil).CombinedOutput(); err != nil {
					fmt.Fprintln(os.Stderr, "double: cp:", string(o))
					return 2
				}
				script := "#!/bin/sh\n: > '" + canary + "'\n"
				_ = os.WriteFile(filepath.Join(evil, "fsmon.sh"), []byte(script), 0o755)
				_ = os.MkdirAll(filepath.Join(evil, "hooks"), 0o755)
				for _, h := range []string{"pre-commit", "post-commit"} {
					_ = os.WriteFile(filepath.Join(evil, "hooks", h), []byte(script), 0o755)
				}
				f, err := os.OpenFile(filepath.Join(evil, "config"), os.O_APPEND|os.O_WRONLY, 0o644)
				if err != nil {
					fmt.Fprintln(os.Stderr, "double: config:", err)
					return 2
				}
				fmt.Fprintf(f, "[core]\n\tfsmonitor = %s\n\thooksPath = %s\n[filter \"pwn\"]\n\tclean = \"sh -c ': > %s; cat'\"\n\tsmudge = cat\n\trequired = true\n",
					filepath.Join(evil, "fsmon.sh"), filepath.Join(evil, "hooks"), canary)
				_ = f.Close()
				if err := os.WriteFile(filepath.Join(repo, ".git", "commondir"), []byte(evil+"\n"), 0o644); err != nil {
					fmt.Fprintln(os.Stderr, "double: commondir:", err)
					return 2
				}
			case "parent-repo":
				// F2: the workspace's parent directory is a repository whose config and attributes are hostile.
				canary := os.Getenv("SC_TEST_CANARY")
				parent := filepath.Dir(filepath.Dir(repo))
				if o, err := exec.Command("/usr/bin/git", "init", "-q", parent).CombinedOutput(); err != nil {
					fmt.Fprintln(os.Stderr, "double: init:", string(o))
					return 2
				}
				script := "#!/bin/sh\n: > '" + canary + "'\n"
				_ = os.WriteFile(filepath.Join(parent, "fsmon.sh"), []byte(script), 0o755)
				_ = os.WriteFile(filepath.Join(parent, ".gitattributes"), []byte("* filter=pwn\n"), 0o644)
				f, err := os.OpenFile(filepath.Join(parent, ".git", "config"), os.O_APPEND|os.O_WRONLY, 0o644)
				if err != nil {
					fmt.Fprintln(os.Stderr, "double: config:", err)
					return 2
				}
				fmt.Fprintf(f, "[core]\n\tfsmonitor = %s\n[filter \"pwn\"]\n\tclean = \"sh -c ': > %s; cat'\"\n\tsmudge = cat\n\trequired = true\n",
					filepath.Join(parent, "fsmon.sh"), canary)
				_ = f.Close()
			case "replace-head":
				// F6: stage the working tree, then replace HEAD (start_commit) by a commit whose tree already contains the
				// staged change, so that a diff against start_commit that honours refs/replace/* shows nothing.
				if o, err := gitH("add", "-A"); err != nil {
					fmt.Fprintln(os.Stderr, "double: add:", o)
					return 2
				}
				tree, err := gitH("write-tree")
				if err != nil {
					fmt.Fprintln(os.Stderr, "double: write-tree:", tree)
					return 2
				}
				head, _ := gitH("rev-parse", "HEAD")
				fake, err := gitH("commit-tree", tree, "-m", "fake")
				if err != nil {
					fmt.Fprintln(os.Stderr, "double: commit-tree:", fake)
					return 2
				}
				if o, err := gitH("replace", "-f", head, fake); err != nil {
					fmt.Fprintln(os.Stderr, "double: replace:", o)
					return 2
				}
			case "double-fork":
				// S6a (S4-N4): a double-forked, setsid'd background sleeper that outlives the harness; out/linger.pid carries
				// its pid.
				c := exec.Command("/bin/sh", "-c", `sleep 1000 >/dev/null 2>&1 & echo $! > "$1"`, "sh", filepath.Join(out, "linger.pid"))
				c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
				if err := c.Run(); err != nil {
					fmt.Fprintln(os.Stderr, "double: double-fork:", err)
					return 2
				}
			case "record-proc":
				// S4: out/proc.txt carries "<pid> <pgid> <ppid>" of the harness process.
				line := fmt.Sprintf("%d %d %d\n", os.Getpid(), syscall.Getpgrp(), os.Getppid())
				_ = os.WriteFile(filepath.Join(out, "proc.txt"), []byte(line), 0o644)
			case "spawn-child":
				// S4: a child in the harness's process group that ignores SIGTERM; out/child.txt carries its pid.
				c := exec.Command("/bin/sh", "-c", "trap '' TERM; exec sleep 1000")
				if err := c.Start(); err != nil {
					fmt.Fprintln(os.Stderr, "double: spawn:", err)
					return 2
				}
				_ = os.WriteFile(filepath.Join(out, "child.txt"), []byte(fmt.Sprintf("%d\n", c.Process.Pid)), 0o644)
			case "ignore-sigterm":
				// S4: out/term.txt carries the clock.Real unix nanoseconds at which SIGTERM arrived; the process lives on.
				ch := make(chan os.Signal, 8)
				signal.Notify(ch, syscall.SIGTERM)
				go func() {
					first := true
					for range ch {
						if first {
							first = false
							_ = os.WriteFile(filepath.Join(out, "term.txt"), []byte(fmt.Sprintf("%d\n", clock.Real{}.Now().UnixNano())), 0o644)
						}
					}
				}()
			case "hang":
				// S4: never returns on its own.
				_ = os.WriteFile(filepath.Join(out, "ready.txt"), []byte("ready\n"), 0o644)
				_ = clock.Real{}.Sleep(context.Background(), 24*time.Hour)
			case "read-stdin":
				// S4 (27-AC-15): block on stdin; out/stdin.txt carries "<elapsed ms> <what fd 0 points to>".
				t0 := clock.Real{}.Now()
				_, _ = io.Copy(io.Discard, os.Stdin)
				link, _ := os.Readlink("/proc/self/fd/0")
				_ = os.WriteFile(filepath.Join(out, "stdin.txt"), []byte(fmt.Sprintf("%d %s\n", clock.Real{}.Now().Sub(t0).Milliseconds(), link)), 0o644)
			case "print":
				// S4: op.N copies (default 1) of op.Content on stdout and, when op.Msg is set, of op.Msg on stderr.
				for i := 0; i < max(op.N, 1); i++ {
					fmt.Println(op.Content)
					if op.Msg != "" {
						fmt.Fprintln(os.Stderr, op.Msg)
					}
				}
			case "bigline":
				// S4: one stdout line of op.N bytes.
				fmt.Println(strings.Repeat("A", op.N))
			case "straddle":
				// S5b (S4-N5a): one stdout line of op.N 'A' bytes, op.Content, and Args[0] (default 100) 'B' bytes.
				trail := 100
				if len(op.Args) > 0 {
					fmt.Sscanf(op.Args[0], "%d", &trail)
				}
				fmt.Println(strings.Repeat("A", op.N) + op.Content + strings.Repeat("B", trail))
			case "stream-direct":
				// S5b (S4-N5b): the harness appends op.N MiB of 'x' without a newline to the shim stream file itself.
				f, err := os.OpenFile(filepath.Join(filepath.Dir(out), ".sc-shim", "stream-1.log"), os.O_WRONLY|os.O_APPEND, 0)
				if err != nil {
					fmt.Fprintln(os.Stderr, "double: stream-direct:", err)
					return 2
				}
				chunk := bytes.Repeat([]byte("x"), 1<<20)
				for i := 0; i < op.N; i++ {
					if _, err := f.Write(chunk); err != nil {
						fmt.Fprintln(os.Stderr, "double: stream-direct:", err)
						return 2
					}
				}
				_ = f.Close()
			case "print-hex":
				// S5b review: one stdout line of the raw bytes in hex Content (invalid UTF-8, lone C1 bytes).
				raw, err := hex.DecodeString(op.Content)
				if err != nil {
					return 2
				}
				os.Stdout.Write(append(raw, '\n'))
			case "stream-sparse":
				// S5b review B1: the harness sparsely extends the shim stream file to 1 TiB.
				if err := os.Truncate(filepath.Join(filepath.Dir(out), ".sc-shim", "stream-1.log"), 1<<40); err != nil {
					fmt.Fprintln(os.Stderr, "double: stream-sparse:", err)
					return 2
				}
			case "bg-writer":
				// S5b review B1: a setsid'd background process appends 256 MiB of empty lines to the stream file as fast as it
				// can; out/bg.txt carries its pid (= its process group) so that the test can stop it.
				c := exec.Command("/bin/sh", "-c", `head -c 268435456 /dev/zero | tr '\0' '\n' >> "$1"`, "sh",
					filepath.Join(filepath.Dir(out), ".sc-shim", "stream-1.log"))
				c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
				if err := c.Start(); err != nil {
					fmt.Fprintln(os.Stderr, "double: bg-writer:", err)
					return 2
				}
				_ = os.WriteFile(filepath.Join(out, "bg.txt"), []byte(fmt.Sprintf("%d\n", c.Process.Pid)), 0o644)
			case "write-gen":
				// S5b: file op.Path = op.N 'a' bytes, op.Content, then Args[1] repeated Args[0] times, then a newline.
				var k int
				fmt.Sscanf(op.Args[0], "%d", &k)
				body := strings.Repeat("a", op.N) + op.Content + strings.Repeat(op.Args[1], k) + "\n"
				p := filepath.Join(repo, op.Path)
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil || os.WriteFile(p, []byte(body), 0o644) != nil {
					fmt.Fprintln(os.Stderr, "double: write-gen", op.Path)
					return 2
				}
			case "lie-commit-graph":
				// S5a (S3-NB3): write a commit-graph in which start_commit's tree is the tree of the staged working tree,
				// so that a diff against start_commit that trusts the graph shows nothing.
				if o, err := gitH("commit-graph", "write", "--reachable"); err != nil {
					fmt.Fprintln(os.Stderr, "double: commit-graph:", o)
					return 2
				}
				if o, err := gitH("add", "-A"); err != nil {
					fmt.Fprintln(os.Stderr, "double: add:", o)
					return 2
				}
				oldTree, _ := gitH("rev-parse", "HEAD^{tree}")
				newTree, err := gitH("write-tree")
				if err != nil {
					fmt.Fprintln(os.Stderr, "double: write-tree:", newTree)
					return 2
				}
				ob, _ := hex.DecodeString(oldTree)
				nb, _ := hex.DecodeString(newTree)
				graph := filepath.Join(repo, ".git", "objects", "info", "commit-graph")
				raw, err := os.ReadFile(graph)
				if err != nil || len(ob) == 0 || !bytes.Contains(raw, ob) {
					fmt.Fprintln(os.Stderr, "double: commit-graph does not hold the start tree:", err)
					return 2
				}
				_ = os.Remove(graph)
				if err := os.WriteFile(graph, bytes.ReplaceAll(raw, ob, nb), 0o644); err != nil {
					fmt.Fprintln(os.Stderr, "double: commit-graph write:", err)
					return 2
				}
			case "forge-job":
				// S6a review B1 (S4-N4): the harness plants a request file of its own (req-99.json) whose command is a
				// lingering writer; out/forged.pid carries its pid and out/forged.log grows while it lives. The op returns
				// once the pid is recorded (bounded wait).
				shimDir := filepath.Join(filepath.Dir(out), ".sc-shim")
				body, _ := json.Marshal(map[string]any{"id": "forged-99", "kind": "validation", "cwd": out, "timeout_seconds": 600, "term_grace_seconds": 5,
					"argv": []string{"/bin/sh", "-c", `echo $$ > "$1/forged.pid"; while :; do echo x >> "$1/forged.log"; done`, "sh", out}})
				tmp := filepath.Join(shimDir, "forged-99.tmp")
				if err := os.WriteFile(tmp, body, 0o644); err != nil {
					fmt.Fprintln(os.Stderr, "double: forge-job:", err)
					return 2
				}
				if err := os.Rename(tmp, filepath.Join(shimDir, "req-99.json")); err != nil {
					fmt.Fprintln(os.Stderr, "double: forge-job:", err)
					return 2
				}
				fctx, fcancel := context.WithTimeout(context.Background(), 20*time.Second)
				for {
					if b, err := os.ReadFile(filepath.Join(out, "forged.pid")); err == nil && strings.Contains(string(b), "\n") {
						break
					}
					if (clock.Real{}).Sleep(fctx, 5*time.Millisecond) != nil {
						fmt.Fprintln(os.Stderr, "double: forge-job: the forged job did not start")
						fcancel()
						return 2
					}
				}
				fcancel()
			case "plant-req":
				// S6a review 2 (B1'): plant req-99.json (a lingering writer that records its pid in out/forged.pid) and
				// return at once, without waiting for it to start.
				shimDir := filepath.Join(filepath.Dir(out), ".sc-shim")
				body, _ := json.Marshal(map[string]any{"id": "forged-99", "kind": "validation", "cwd": out, "timeout_seconds": 600, "term_grace_seconds": 5,
					"argv": []string{"/bin/sh", "-c", `echo $$ > "$1/forged.pid"; while :; do echo x >> "$1/forged.log"; done`, "sh", out}})
				tmp := filepath.Join(shimDir, "forged-99.tmp")
				if err := os.WriteFile(tmp, body, 0o644); err != nil {
					fmt.Fprintln(os.Stderr, "double: plant-req:", err)
					return 2
				}
				if err := os.Rename(tmp, filepath.Join(shimDir, "req-99.json")); err != nil {
					fmt.Fprintln(os.Stderr, "double: plant-req:", err)
					return 2
				}
			case "linger":
				// S5a: after the claim, forge the shim result and keep rewriting the workspace .git until op.Path exists.
				lingerStop, lingerSwap = op.Path, op.Content
			default:
				fmt.Fprintln(os.Stderr, "double: unknown op:", op.Op)
				return 2
			case "hostile":
				canary := os.Getenv("SC_TEST_CANARY")
				script := "#!/bin/sh\n: > '" + canary + "'\n"
				for _, h := range []string{"pre-commit", "post-commit"} {
					p := filepath.Join(repo, ".git", "hooks", h)
					if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
						fmt.Fprintln(os.Stderr, "double: hook:", err)
						return 2
					}
				}
				fsmon := filepath.Join(repo, ".git", "fsmon.sh")
				_ = os.WriteFile(fsmon, []byte(script), 0o755)
				f, err := os.OpenFile(filepath.Join(repo, ".git", "config"), os.O_APPEND|os.O_WRONLY, 0o644)
				if err != nil {
					fmt.Fprintln(os.Stderr, "double: config:", err)
					return 2
				}
				fmt.Fprintf(f, "[core]\n\tfsmonitor = %s\n[filter \"pwn\"]\n\tclean = \"sh -c ': > %s; cat'\"\n\tsmudge = cat\n\trequired = true\n", fsmon, canary)
				_ = f.Close()
			}
		}
		return 0
	}
	finishClaim = func() int {
		summary := os.Getenv("SC_TEST_HARNESS_SUMMARY")
		if summary == "" {
			summary = "Add tests for Add covering AC-1 and AC-2"
		}
		handoff := map[string]any{
			"what_i_did": "Test double wrote the claim.", "what_remains": "Nothing.",
			"recommended_next_role": nil, "questions": []any{}, "known_risks": []any{},
		}
		if raw := os.Getenv("SC_TEST_HARNESS_HANDOFF"); raw != "" { // S5b: JSON object merged over the default handoff
			var over map[string]any
			if err := json.Unmarshal([]byte(raw), &over); err != nil {
				fmt.Fprintln(os.Stderr, "double: handoff:", err)
				return 2
			}
			for k, v := range over {
				handoff[k] = v
			}
		}
		state := os.Getenv("SC_TEST_HARNESS_STATE")
		if state == "" {
			state = "completed"
		}
		claim := map[string]any{
			"state": state, "summary": summary,
			"handoff": handoff,
		}
		switch role {
		case "tester":
			claim["role_output"] = map[string]any{"kind": "tester", "test_files": []any{"calc/add_ac_test.go"},
				"ac_mapping": []any{map[string]any{"ac_id": "AC-1", "tests": []any{"TestAddAC1"}}}}
		case "implementer":
			claim["role_output"] = map[string]any{"kind": "implementer", "changed_files": []any{"calc/calc.go"}, "ac_addressed": []any{"AC-1"}}
		case "researcher":
			n := 200
			fmt.Sscanf(os.Getenv("SC_TEST_HARNESS_REPORT_BYTES"), "%d", &n)
			line := "Finding: the redirect handler copies the request but drops GetBody on 307 (see client.go).\n"
			claim["role_output"] = map[string]any{"kind": "research_report", "report_markdown": strings.Repeat(line, n/len(line)+1)[:n]}
		}
		b, _ := json.Marshal(claim)
		if err := os.WriteFile(filepath.Join(out, "claim.json"), b, 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "double: claim:", err)
			return 2
		}
		fmt.Println(`{"type":"sc_fake_result","is_error":false,"num_turns":1}`)
		return 0
	}
	if c := runOps(pre); c != 0 {
		return c
	}
	if !claimDone {
		if c := finishClaim(); c != 0 {
			return c
		}
	}
	if c := runOps(post); c != 0 {
		return c
	}
	if lingerStop != "" {
		return lingerForged(repo, out, lingerStop, lingerSwap)
	}
	return 0
}

// lingerForged is the S5a hostile harness: it posts a forged shim result (exit 0) for its own request, so that
// sc-worker proceeds, and then keeps rewriting the workspace .git (config with core.fsmonitor, core.hooksPath,
// include.path and a filter driver, info/attributes selecting the filter, hooks) until the stop file exists.
func lingerForged(repo, out, stop, swap string) int {
	tight := swap == "tight" // S6a review 2: no pause between rewrites
	if tight {
		swap = ""
	}
	canary := os.Getenv("SC_TEST_CANARY")
	ws := filepath.Dir(out)
	gd := filepath.Join(repo, ".git")
	base, err := os.ReadFile(filepath.Join(gd, "config"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "double: config:", err)
		return 2
	}
	script := "#!/bin/sh\n: > '" + canary + "'\n"
	evilHooks := filepath.Join(gd, "evil-hooks")
	_ = os.MkdirAll(evilHooks, 0o755)
	_ = os.WriteFile(filepath.Join(gd, "fsmon.sh"), []byte(script), 0o755)
	for _, h := range []string{"pre-commit", "post-commit", "pre-push", "reference-transaction"} {
		_ = os.WriteFile(filepath.Join(evilHooks, h), []byte(script), 0o755)
	}
	include := filepath.Join(gd, "evil.inc")
	atomic := func(path, content string, mode os.FileMode) {
		tmp := path + ".tmp"
		if os.WriteFile(tmp, []byte(content), mode) == nil {
			_ = os.Rename(tmp, path)
		}
	}
	filter := "[filter \"pwn\"]\n\tclean = \"sh -c ': > " + canary + "; cat'\"\n\tsmudge = cat\n\trequired = true\n"
	cfg := string(base) + "[core]\n\tfsmonitor = " + filepath.Join(gd, "fsmon.sh") + "\n\thooksPath = " + evilHooks +
		"\n[include]\n\tpath = " + include + "\n" + filter
	// A forged result for the harness request (the one with kind harness).
	ents, _ := os.ReadDir(filepath.Join(ws, ".sc-shim"))
	for _, e := range ents {
		m := shimReqRe.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		raw, _ := os.ReadFile(filepath.Join(ws, ".sc-shim", e.Name()))
		var req shimRequest
		if json.Unmarshal(raw, &req) != nil || req.Kind != "harness" {
			continue
		}
		now := clock.Real{}.Now().UTC().Format(time.RFC3339)
		forged, _ := json.Marshal(map[string]any{"id": req.ID, "exit_code": 0, "signal": nil, "started_at": now, "finished_at": now, "timed_out": false})
		atomic(filepath.Join(ws, ".sc-shim", "res-"+m[1]+".json"), string(forged), 0o644)
	}
	// S5c review 1 (N1): swap=private or decoy: right after the forged result, replace the repository directory by a symlink to
	// the private directory, or to a copy of the repository holding a distinctive extra file, and keep it that way.
	// S5c review 2: "-late" waits until sc-worker has written its commit message (COMMIT_MSG in the private directory,
	// after every pre-commit check) and then marks out/swapped.txt; "toggle-late" renames repo away, links it to the
	// private directory, unlinks it and renames the real directory back, once, and then only waits for stop.
	late := strings.HasSuffix(swap, "-late")
	swap = strings.TrimSuffix(swap, "-late")
	if late {
		msg := filepath.Join(os.Getenv("SC_TEST_PRIVATE_DIR"), "COMMIT_MSG")
		for {
			if _, err := os.Stat(stop); err == nil {
				return 0
			}
			if _, err := os.Stat(msg); err == nil {
				break
			}
			_ = clock.Real{}.Sleep(context.Background(), time.Millisecond)
		}
	}
	if swap == "toggle" {
		_ = os.Rename(repo, repo+".real")
		_ = os.Symlink(os.Getenv("SC_TEST_PRIVATE_DIR"), repo)
		_ = os.Remove(repo)
		_ = os.Rename(repo+".real", repo)
		_ = os.WriteFile(filepath.Join(out, "swapped.txt"), nil, 0o644)
		for {
			if _, err := os.Stat(stop); err == nil {
				return 0
			}
			_ = clock.Real{}.Sleep(context.Background(), 5*time.Millisecond)
		}
	}
	if swap != "" {
		target := os.Getenv("SC_TEST_PRIVATE_DIR")
		if swap == "decoy" {
			target = filepath.Join(filepath.Dir(target), "decoy")
			if o, err := exec.Command("cp", "-a", repo, target).CombinedOutput(); err != nil {
				fmt.Fprintln(os.Stderr, "double: decoy:", string(o))
				return 2
			}
			_ = os.WriteFile(filepath.Join(target, "DECOY-CANARY-0031.txt"), []byte("DECOY-CANARY-0031\n"), 0o644)
		}
		_ = os.Rename(repo, repo+".real")
		_ = os.WriteFile(filepath.Join(out, "swapped.txt"), nil, 0o644)
		for {
			if _, err := os.Stat(stop); err == nil {
				return 0
			}
			if st, err := os.Lstat(repo); err != nil || st.Mode()&os.ModeSymlink == 0 {
				_ = os.RemoveAll(repo)
				_ = os.Symlink(target, repo)
			}
			_ = clock.Real{}.Sleep(context.Background(), time.Millisecond)
		}
	}
	for {
		if _, err := os.Stat(stop); err == nil {
			return 0
		}
		atomic(include, "[core]\n\tfsmonitor = "+filepath.Join(gd, "fsmon.sh")+"\n"+filter, 0o644)
		atomic(filepath.Join(gd, "config"), cfg, 0o644)
		_ = os.MkdirAll(filepath.Join(gd, "info"), 0o755)
		atomic(filepath.Join(gd, "info", "attributes"), "* filter=pwn\n", 0o644)
		for _, h := range []string{"pre-commit", "post-commit", "pre-push"} {
			atomic(filepath.Join(gd, "hooks", h), script, 0o755)
		}
		if !tight {
			_ = clock.Real{}.Sleep(context.Background(), 2*time.Millisecond)
		}
	}
}

// ---------------------------------------------------------------------------------------------------------------
// test shim (plan 27 section 7.4)

type shimRequest struct {
	ID               string            `json:"id"`
	Kind             string            `json:"kind"`
	Argv             []string          `json:"argv"`
	Env              map[string]string `json:"env"`
	Cwd              string            `json:"cwd"`
	TimeoutSeconds   int               `json:"timeout_seconds"`
	TermGraceSeconds int               `json:"term_grace_seconds"`
}

// testShim stands in for the harness container: it executes run requests, maps the production mount points to the
// test directories and resolves @file: references from the harness-secrets directory.
type testShim struct {
	ws, hsecrets, home, tmp, gatewayURL string
	mode                                string // real or ops
	ops                                 []harnessOp
	extraEnv                            map[string]string
	resExtra                            map[string]any // S6a review: extra fields of every result file

	// S6a review 2: extra fields of the result of request n only.
	resExtraByN map[string]map[string]any

	mu      sync.Mutex
	handled map[string]bool
	reqs    []shimRequest
	wg      sync.WaitGroup
}

var shimReqRe = regexp.MustCompile(`^req-([0-9]+)\.json$`)
var fileRefRe = regexp.MustCompile(`@file:([a-z][a-z0-9-]*)`)

func (s *testShim) mapPath(v string) string {
	for _, m := range [][2]string{{"/var/run/sc/harness-secrets", s.hsecrets}, {"/workspace", s.ws}} {
		if v == m[0] || strings.HasPrefix(v, m[0]+"/") {
			return m[1] + v[len(m[0]):]
		}
	}
	return v
}

func (s *testShim) requests() []shimRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]shimRequest(nil), s.reqs...)
}

func (s *testShim) run(ctx context.Context) {
	dir := filepath.Join(s.ws, ".sc-shim")
	s.handled = map[string]bool{}
	for ctx.Err() == nil {
		if _, err := os.Stat(filepath.Join(dir, "done")); err == nil {
			break
		}
		ents, _ := os.ReadDir(dir)
		for _, e := range ents {
			if m := shimReqRe.FindStringSubmatch(e.Name()); m != nil && !s.handled[m[1]] {
				s.handled[m[1]] = true
				s.wg.Add(1)
				go func(n string) {
					defer s.wg.Done()
					s.exec(ctx, dir, n)
				}(m[1])
			}
		}
		_ = clock.Real{}.Sleep(ctx, 10*time.Millisecond)
	}
	s.wg.Wait()
}

type lineWriter struct {
	mu     *sync.Mutex
	w      io.Writer
	prefix string
	buf    []byte
}

func (l *lineWriter) Write(p []byte) (int, error) {
	l.buf = append(l.buf, p...)
	for {
		i := bytes.IndexByte(l.buf, '\n')
		if i < 0 {
			break
		}
		l.mu.Lock()
		fmt.Fprintf(l.w, "%s%s\n", l.prefix, l.buf[:i])
		l.mu.Unlock()
		l.buf = l.buf[i+1:]
	}
	return len(p), nil
}

func (l *lineWriter) flush() {
	if len(l.buf) > 0 {
		l.mu.Lock()
		fmt.Fprintf(l.w, "%s%s\n", l.prefix, l.buf)
		l.mu.Unlock()
		l.buf = nil
	}
}

func (s *testShim) exec(ctx context.Context, dir, n string) {
	raw, err := os.ReadFile(filepath.Join(dir, "req-"+n+".json"))
	var req shimRequest
	if err != nil || json.Unmarshal(raw, &req) != nil || len(req.Argv) == 0 {
		return
	}
	s.mu.Lock()
	s.reqs = append(s.reqs, req)
	s.mu.Unlock()

	argv := make([]string, len(req.Argv))
	for i, a := range req.Argv {
		argv[i] = s.mapPath(a)
	}
	env := []string{}
	for k, v := range req.Env {
		v = s.mapPath(v)
		v = fileRefRe.ReplaceAllStringFunc(v, func(ref string) string {
			b, _ := os.ReadFile(filepath.Join(s.hsecrets, strings.TrimPrefix(ref, "@file:")))
			return strings.TrimSpace(string(b))
		})
		switch k {
		case "HOME":
			v = s.home
		case "TMPDIR":
			v = s.tmp
		case "SC_FAKE_GATEWAY_URL":
			v = s.gatewayURL
		}
		env = append(env, k+"="+v)
	}
	var cmd *exec.Cmd
	if req.Kind == "harness" && argv[0] == "sc-fake-harness" {
		exe, _ := os.Executable()
		opsJSON, _ := json.Marshal(s.ops)
		env = append(env, helperKindEnv+"=fake-harness", "SC_TEST_HARNESS_MODE="+s.mode, "SC_TEST_HARNESS_OPS="+string(opsJSON))
		for k, v := range s.extraEnv {
			env = append(env, k+"="+v)
		}
		cmd = exec.CommandContext(ctx, exe, append([]string{"-test.run=^TestHelperProcess_FakeHarness$", "--"}, argv[1:]...)...)
	} else {
		cmd = exec.CommandContext(ctx, argv[0], argv[1:]...)
	}
	cmd.Env = env
	cmd.Dir = s.mapPath(req.Cwd)
	stream, err := os.OpenFile(filepath.Join(dir, "stream-"+n+".log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	var smu sync.Mutex
	so := &lineWriter{mu: &smu, w: stream}
	se := &lineWriter{mu: &smu, w: stream, prefix: "[stderr] "}
	cmd.Stdout, cmd.Stderr = so, se
	started := clock.Real{}.Now().UTC().Format(time.RFC3339)
	runErr := cmd.Run()
	so.flush()
	se.flush()
	_ = stream.Close()
	code := 0
	if runErr != nil {
		var ee *exec.ExitError
		if errors.As(runErr, &ee) {
			code = ee.ExitCode()
		} else {
			code = -1
		}
	}
	resDoc := map[string]any{
		"id": req.ID, "exit_code": code, "signal": nil, "started_at": started,
		"finished_at": clock.Real{}.Now().UTC().Format(time.RFC3339), "timed_out": false,
	}
	for k, v := range s.resExtra {
		resDoc[k] = v
	}
	for k, v := range s.resExtraByN[n] {
		resDoc[k] = v
	}
	res, _ := json.Marshal(resDoc)
	tmp := filepath.Join(dir, "res-"+n+".json.tmp")
	if os.WriteFile(tmp, res, 0o644) == nil {
		_ = os.Rename(tmp, filepath.Join(dir, "res-"+n+".json"))
	}
}

// ---------------------------------------------------------------------------------------------------------------
// one worker run

type gitSpec struct {
	fx      *fixture
	pkt     map[string]any
	mode    string // harness mode: real (default) or ops
	ops     []harnessOp
	shimEnv map[string]string
	// S4: run the production shim (worker.Shim) as a separate process instead of the test-owned goroutine, and/or
	// give sc-worker the real clock (the packet deadline must then be in the real future).
	realShim, realClock bool
	// N3: SC_WORKSPACE as given to sc-worker, when it must differ from the cleaned workspace path.
	scWorkspace func(workspace string) string
	// S6a: the stub Gateway answers request n (1-based) for the given model with a status and an x-sc-error-code
	// (nil: always 200); stub configures the stub Runner before sc-worker starts; sshScript replaces the ssh wrapper
	// script (it receives the run directories and the shell words that run the fake ssh helper); sleepLog records
	// every Sleep duration of sc-worker's clock in gitRun.sleeps.
	gateway   func(n int, model string) (status int, code string)
	stub      func(*stubRunner)
	sshScript func(dirs map[string]string, helper string) string
	sleepLog  bool
	// S6a review: extra fields that the test-owned shim writes into every result file.
	shimResExtra map[string]any
	// S6a review 2: extra fields of the result file of request n (key "2" is the first validation command).
	shimResExtraByN map[string]map[string]any
}

type gitRun struct {
	fx         *fixture
	stub       *stubRunner
	shim       *testShim
	proc       *process
	violations string
	ws         string
	canary     string
	gwHits     atomic.Int64
	cancelShim context.CancelFunc
	shimDone   chan struct{}
	shimProc   *process // set when the production shim runs as a process
	dirs       map[string]string
	gwMu       sync.Mutex
	gwModels   []string // S6a: the model of every request the stub Gateway received, in order
	sleepPath  string
}

// runGit starts the shim and sc-worker for the packet and returns at once; call finish to wait for the result.
func runGit(t *testing.T, sp gitSpec) *gitRun {
	t.Helper()
	root := t.TempDir()
	dirs := map[string]string{}
	for _, d := range []string{"secrets", "workspace", "harness-secrets", "private", "home", "tmp", "bin"} {
		dirs[d] = filepath.Join(root, d)
		if err := os.MkdirAll(dirs[d], 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// /workspace/context is made 0555; the temporary directories must stay removable.
	t.Cleanup(func() {
		_ = filepath.WalkDir(dirs["workspace"], func(p string, d fs.DirEntry, err error) error {
			if err == nil && d.IsDir() {
				_ = os.Chmod(p, 0o755)
			}
			return nil
		})
	})
	gr := &gitRun{fx: sp.fx, ws: dirs["workspace"], canary: filepath.Join(root, canaryName), violations: filepath.Join(root, "ssh-violations.log")}
	for name, content := range map[string]string{
		"task-token": taskToken, "gateway-token": "gw-CANARY-0002-gateway-token-value", "deploy-key": deployKeyBody,
		"known_hosts": forgejoHost + " ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIFAKEHOSTKEYFORTESTSONLY\n",
	} {
		writeTestFile(t, filepath.Join(dirs["secrets"], name), content, 0o444)
	}
	writeTestFile(t, filepath.Join(dirs["harness-secrets"], "gateway-token"), "gw-CANARY-0002-gateway-token-value", 0o444)
	writeTestFile(t, filepath.Join(dirs["harness-secrets"], "egress-token"), "eg-CANARY-0003-egress-token-value", 0o444)

	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(root, "ssh-config.json")
	cb, _ := json.Marshal(sshConfig{
		SecretsDir: dirs["secrets"], PrivateDir: dirs["private"], FixtureRoot: filepath.Join(sp.fx.root, "git"),
		KeyContent: deployKeyBody, ViolationsFile: gr.violations,
	})
	writeTestFile(t, cfgPath, string(cb), 0o644)
	sshProg := filepath.Join(dirs["bin"], "ssh")
	helperWords := "env SC_TEST_HELPER=fake-ssh '" + exe + "' -test.run='^TestHelperProcess_FakeSSH$' -- '" + cfgPath + "'"
	if sp.sshScript != nil {
		writeTestFile(t, sshProg, sp.sshScript(dirs, helperWords), 0o755)
	} else {
		writeTestFile(t, sshProg, "#!/bin/sh\nexec "+helperWords+" \"$@\"\n", 0o755)
	}

	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		n := int(gr.gwHits.Add(1))
		var req struct {
			Model string `json:"model"`
		}
		_ = json.Unmarshal(body, &req)
		gr.gwMu.Lock()
		gr.gwModels = append(gr.gwModels, req.Model)
		gr.gwMu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if sp.gateway != nil {
			if status, code := sp.gateway(n, req.Model); status != http.StatusOK {
				if code != "" {
					w.Header().Set("x-sc-error-code", code)
				}
				w.WriteHeader(status)
				_, _ = io.WriteString(w, `{"error":"denied"}`)
				return
			}
		}
		_, _ = io.WriteString(w, `{"id":"fake","choices":[]}`)
	}))
	t.Cleanup(gw.Close)

	mode := sp.mode
	if mode == "" {
		mode = "real"
	}
	extra := map[string]string{"SC_TEST_CANARY": gr.canary, "SC_TEST_PRIVATE_DIR": dirs["private"], "SC_TEST_SECRETS_DIR": dirs["secrets"]}
	for k, v := range sp.shimEnv {
		extra[k] = v
	}
	gr.dirs = dirs
	if sp.realShim {
		gr.startRealShim(t, exe, dirs, gw.URL, mode, sp.ops, extra)
	} else {
		gr.shim = &testShim{ws: dirs["workspace"], hsecrets: dirs["harness-secrets"], home: dirs["home"], tmp: dirs["tmp"],
			gatewayURL: gw.URL, mode: mode, ops: sp.ops, extraEnv: extra, resExtra: sp.shimResExtra, resExtraByN: sp.shimResExtraByN}
		shimCtx, cancel := context.WithCancel(context.Background())
		gr.cancelShim = cancel
		gr.shimDone = make(chan struct{})
		go func() {
			defer close(gr.shimDone)
			gr.shim.run(shimCtx)
		}()
		t.Cleanup(func() { cancel(); <-gr.shimDone })
	}

	gr.stub = newStubRunner(t, sp.pkt)
	if sp.stub != nil {
		sp.stub(gr.stub)
	}
	if sp.sleepLog {
		gr.sleepPath = filepath.Join(root, "sleeps.log")
	}
	hc, _ := json.Marshal(gitHelperConfig{
		PrivateDir: dirs["private"], HarnessSecretsDir: dirs["harness-secrets"], HomeDir: dirs["home"], TmpDir: dirs["tmp"],
		SSHProgram: sshProg, ClockStart: clockOrigin, RealClock: sp.realClock, SleepLog: gr.sleepPath,
	})
	p := &process{done: make(chan struct{})}
	p.cmd = exec.Command(exe, "-test.run=^TestHelperProcess_ScWorkerGit$")
	wsEnv := dirs["workspace"]
	if sp.scWorkspace != nil {
		wsEnv = sp.scWorkspace(dirs["workspace"])
	}
	p.cmd.Env = []string{
		gitHelperEnv + "=" + string(hc),
		"SC_SECRETS_DIR=" + dirs["secrets"], "SC_WORKSPACE=" + wsEnv,
		"SC_RUNNER_URL=" + gr.stub.srv.URL, "SC_TASK_ID=sc-1", "SC_EXECUTION_ID=sc-1-x1",
		"HOME=" + dirs["home"], "TMPDIR=" + dirs["tmp"], "PATH=" + os.Getenv("PATH"),
	}
	p.cmd.Stdout, p.cmd.Stderr = &p.stdout, &p.stderr
	if err := p.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() {
		err := p.cmd.Wait()
		p.code = 0
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			p.code = ee.ExitCode()
		} else if err != nil {
			p.code = -2
		}
		close(p.done)
	}()
	t.Cleanup(func() { _ = p.cmd.Process.Kill() })
	gr.proc = p
	return gr
}

// checkFramed asserts 27-AC-17 for execution sc-1-x1: exactly one result block whose sha256 matches the body.
func checkFramed(t *testing.T, stdout string, body []byte) {
	t.Helper()
	if b, e := countLines(stdout, "::SC-RESULT-BEGIN::"), countLines(stdout, "::SC-RESULT-END::"); b != 1 || e != 1 {
		t.Errorf("stdout has %d BEGIN and %d END result markers, want exactly 1 each", b, e)
		return
	}
	lines := strings.Split(stdout, "\n")
	for i, l := range lines {
		if !strings.HasPrefix(l, "::SC-RESULT-BEGIN::") {
			continue
		}
		var hdr blockHeader
		if err := json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(l, "::SC-RESULT-BEGIN::"))), &hdr); err != nil {
			t.Errorf("BEGIN header is not JSON: %v", err)
			return
		}
		sum := sha256.Sum256(body)
		if hdr.SHA256 != hex.EncodeToString(sum[:]) || hdr.Bytes != len(body) || hdr.ExecutionID != "sc-1-x1" {
			t.Errorf("framed block header %+v does not describe the posted body (%d bytes, sha256 %x)", hdr, len(body), sum)
		}
		if i+2 >= len(lines) || lines[i+1] != string(body) || !strings.HasPrefix(lines[i+2], "::SC-RESULT-END::") {
			t.Errorf("framed block is not BEGIN, the posted body, END")
		}
	}
}

// finish waits for sc-worker, asserts the common outcome (exit 0, exactly one framed schema-valid result, no
// refused ssh invocation) and returns the posted result.
func (gr *gitRun) finish(t *testing.T) map[string]any {
	t.Helper()
	code := waitDone(t, gr.proc, "start")
	gr.cancelShim()
	<-gr.shimDone
	if code != 0 {
		t.Errorf("exit code = %d, want 0; stderr:\n%s", code, gr.proc.stderr.String())
	}
	if b, err := os.ReadFile(gr.violations); err == nil {
		t.Errorf("the fake ssh refused an invocation:\n%s", b)
	}
	posts := gr.stub.calls("POST", "/v1/worker/result")
	if len(posts) != 1 {
		t.Fatalf("%d result POSTs, want 1; stderr:\n%s", len(posts), gr.proc.stderr.String())
	}
	checkFramed(t, gr.proc.stdout.String(), posts[0].Body)
	return decodeResult(t, posts[0].Body)
}

// events returns the sc-worker log events (JSON lines with an "event" key) on stdout.
func (gr *gitRun) events() []map[string]any {
	var out []map[string]any
	sc := bufio.NewScanner(strings.NewReader(gr.proc.stdout.String()))
	sc.Buffer(make([]byte, 1<<20), 8<<20)
	for sc.Scan() {
		l := sc.Text()
		if !strings.HasPrefix(l, "{") {
			continue
		}
		var m map[string]any
		if json.Unmarshal([]byte(l), &m) == nil {
			if _, ok := m["event"]; ok {
				out = append(out, m)
			}
		}
	}
	return out
}

func (gr *gitRun) event(name string) map[string]any {
	for _, e := range gr.events() {
		if e["event"] == name {
			return e
		}
	}
	return nil
}
