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
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
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
	cfg.Clock = stillClock{clock.NewFake(start)}
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
	Op      string `json:"op"` // write, delete, commit, hostile, record-head
	Path    string `json:"path,omitempty"`
	Content string `json:"content,omitempty"`
	Msg     string `json:"msg,omitempty"`
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
	for _, op := range ops {
		switch op.Op {
		case "write":
			p := filepath.Join(repo, op.Path)
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil || os.WriteFile(p, []byte(op.Content), 0o644) != nil {
				fmt.Fprintln(os.Stderr, "double: write", op.Path)
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
			fmt.Fprintf(f, "[core]\n\tfsmonitor = %s\n[filter \"pwn\"]\n\tclean = sh -c ': > %s; cat'\n\tsmudge = cat\n\trequired = true\n", fsmon, canary)
			_ = f.Close()
		}
	}
	summary := os.Getenv("SC_TEST_HARNESS_SUMMARY")
	if summary == "" {
		summary = "Add tests for Add covering AC-1 and AC-2"
	}
	state := os.Getenv("SC_TEST_HARNESS_STATE")
	if state == "" {
		state = "completed"
	}
	claim := map[string]any{
		"state": state, "summary": summary,
		"handoff": map[string]any{
			"what_i_did": "Test double wrote the claim.", "what_remains": "Nothing.",
			"recommended_next_role": nil, "questions": []any{}, "known_risks": []any{},
		},
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
	res, _ := json.Marshal(map[string]any{
		"id": req.ID, "exit_code": code, "signal": nil, "started_at": started,
		"finished_at": clock.Real{}.Now().UTC().Format(time.RFC3339), "timed_out": false,
	})
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
	writeTestFile(t, sshProg, "#!/bin/sh\nSC_TEST_HELPER=fake-ssh exec '"+exe+"' -test.run='^TestHelperProcess_FakeSSH$' -- '"+cfgPath+"' \"$@\"\n", 0o755)

	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		gr.gwHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"fake","choices":[]}`)
	}))
	t.Cleanup(gw.Close)

	mode := sp.mode
	if mode == "" {
		mode = "real"
	}
	extra := map[string]string{"SC_TEST_CANARY": gr.canary}
	for k, v := range sp.shimEnv {
		extra[k] = v
	}
	gr.shim = &testShim{ws: dirs["workspace"], hsecrets: dirs["harness-secrets"], home: dirs["home"], tmp: dirs["tmp"],
		gatewayURL: gw.URL, mode: mode, ops: sp.ops, extraEnv: extra}
	shimCtx, cancel := context.WithCancel(context.Background())
	gr.cancelShim = cancel
	gr.shimDone = make(chan struct{})
	go func() {
		defer close(gr.shimDone)
		gr.shim.run(shimCtx)
	}()
	t.Cleanup(func() { cancel(); <-gr.shimDone })

	gr.stub = newStubRunner(t, sp.pkt)
	hc, _ := json.Marshal(gitHelperConfig{
		PrivateDir: dirs["private"], HarnessSecretsDir: dirs["harness-secrets"], HomeDir: dirs["home"], TmpDir: dirs["tmp"],
		SSHProgram: sshProg, ClockStart: clockOrigin,
	})
	p := &process{done: make(chan struct{})}
	p.cmd = exec.Command(exe, "-test.run=^TestHelperProcess_ScWorkerGit$")
	p.cmd.Env = []string{
		gitHelperEnv + "=" + string(hc),
		"SC_SECRETS_DIR=" + dirs["secrets"], "SC_WORKSPACE=" + dirs["workspace"],
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
