package lint_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Issue #149 (plan 52 section 8.4 `mode` job): candidate code must not decide whether the trusted verifier that
// evaluates it runs. On pull_request the primary CI product is derived from the PR base revision, never from the
// checked-out candidate tree. These tests run the real `mode` step script of verify.yaml in scratch repositories.

// modeStep returns the env mapping and the run script of the step with `id: mode` in the mode job.
func modeStep(t *testing.T) (map[string]string, string) {
	t.Helper()
	lines := jobsOf(mustWorkflow(t, "verify.yaml"))["mode"]
	var step []string
	stepInd := -1
	for _, l := range lines {
		tr := strings.TrimSpace(l)
		if strings.HasPrefix(tr, "- ") {
			if stepInd >= 0 && indentOf(l) == stepInd {
				if containsLine(step, "id: mode") {
					break
				}
				step = nil
			}
			if stepInd < 0 || indentOf(l) == stepInd {
				stepInd = indentOf(l)
				step = []string{strings.Repeat(" ", stepInd+2) + strings.TrimPrefix(tr, "- ")}
				continue
			}
		}
		if stepInd >= 0 {
			step = append(step, l)
		}
	}
	if !containsLine(step, "id: mode") {
		t.Fatal("verify.yaml mode job has no step with `id: mode`")
	}
	env := map[string]string{}
	var run []string
	for i := 0; i < len(step); i++ {
		l := step[i]
		tr := strings.TrimSpace(l)
		ind := indentOf(l)
		switch {
		case tr == "env:":
			for i+1 < len(step) && (strings.TrimSpace(step[i+1]) == "" || indentOf(step[i+1]) > ind) {
				i++
				if k, v, ok := strings.Cut(strings.TrimSpace(step[i]), ":"); ok {
					env[strings.TrimSpace(k)] = strings.TrimSpace(v)
				}
			}
		case tr == "run: |":
			for i+1 < len(step) && (strings.TrimSpace(step[i+1]) == "" || indentOf(step[i+1]) > ind) {
				i++
				run = append(run, step[i])
			}
		}
	}
	if len(run) == 0 {
		t.Fatal("the mode step has no `run: |` block")
	}
	cut := -1
	for _, l := range run {
		if strings.TrimSpace(l) != "" && (cut < 0 || indentOf(l) < cut) {
			cut = indentOf(l)
		}
	}
	for i, l := range run {
		if len(l) >= cut {
			run[i] = l[cut:]
		}
	}
	return env, strings.Join(run, "\n") + "\n"
}

func containsLine(lines []string, want string) bool {
	for _, l := range lines {
		if strings.TrimSpace(l) == want {
			return true
		}
	}
	return false
}

var ghExpr = regexp.MustCompile(`^\$\{\{\s*([A-Za-z0-9_.\-]+)\s*\}\}$`)

type modeCase struct {
	event   string            // github.event_name
	base    map[string]string // files of the trusted base commit (path -> content)
	head    map[string]string // files of the checked-out candidate (path -> content); nil = same as base
	noBase  bool              // pull_request without a base SHA in the event payload
	badBase bool              // pull_request whose base SHA names no object in the clone
	ref     string            // github.ref for push and workflow_dispatch; default refs/heads/main
	baseRef string            // github.event.pull_request.base.ref; default main
	wantErr bool
}

const forgeInit = "apiVersion: strange.company/v1alpha1\nkind: ForgeInit\n"

func mgit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid", "GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func commitTree(t *testing.T, dir string, files map[string]string) string {
	t.Helper()
	mgit(t, dir, "rm", "-rq", "--ignore-unmatch", ".")
	for p, c := range files {
		f := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mgit(t, dir, "add", "-A")
	mgit(t, dir, "commit", "-q", "--allow-empty", "-m", "c")
	return mgit(t, dir, "rev-parse", "HEAD")
}

// runMode builds base and candidate commits, checks out the candidate, runs the mode step and returns
// `primary` from $GITHUB_OUTPUT.
func runMode(t *testing.T, c modeCase) (string, error) {
	t.Helper()
	envMap, script := modeStep(t)
	if strings.Contains(script, "${{") {
		t.Fatal("the mode step interpolates ${{ }} into its script; pass event data through env")
	}
	dir := t.TempDir()
	mgit(t, dir, "init", "-q", "-b", "main")
	base := map[string]string{"README.md": "x\n"}
	for k, v := range c.base {
		base[k] = v
	}
	baseSHA := commitTree(t, dir, base)
	headSHA := baseSHA
	if c.head != nil {
		head := map[string]string{"README.md": "x\n"}
		for k, v := range c.head {
			head[k] = v
		}
		headSHA = commitTree(t, dir, head)
	}
	if c.noBase {
		baseSHA = ""
	}
	ctx := map[string]string{
		"github.event_name":                      c.event,
		"github.sha":                             headSHA,
		"github.event.pull_request.base.sha":     "",
		"github.event.pull_request.head.sha":     "",
		"github.event.before":                    "",
		"github.ref":                             "refs/heads/main",
		"github.event.pull_request.base.ref":     "",
		"github.event.repository.default_branch": "main",
	}
	if c.ref != "" {
		ctx["github.ref"] = c.ref
	}
	if c.event == "pull_request" {
		if c.badBase {
			baseSHA = strings.Repeat("ab", 20)
		}
		ctx["github.event.pull_request.base.sha"] = baseSHA
		ctx["github.event.pull_request.head.sha"] = headSHA
		ctx["github.event.pull_request.base.ref"] = "main"
		if c.baseRef != "" {
			ctx["github.event.pull_request.base.ref"] = c.baseRef
		}
		ctx["github.ref"] = "refs/pull/1/merge"
	}
	tmp := t.TempDir()
	out := filepath.Join(tmp, "out")
	sum := filepath.Join(tmp, "summary")
	scriptFile := filepath.Join(tmp, "step.sh")
	if err := os.WriteFile(scriptFile, []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}
	env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + tmp, "GITHUB_OUTPUT=" + out, "GITHUB_STEP_SUMMARY=" + sum,
		"GITHUB_EVENT_NAME=" + c.event, "GITHUB_SHA=" + headSHA, "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1"}
	for k, v := range envMap {
		m := ghExpr.FindStringSubmatch(v)
		if m == nil {
			env = append(env, k+"="+strings.Trim(v, `'"`))
			continue
		}
		val, ok := ctx[m[1]]
		if !ok {
			t.Fatalf("mode step env %s uses %s, which this test does not model", k, v)
		}
		env = append(env, k+"="+val)
	}
	// What `shell: bash` runs on Linux runners (TestModeStepDeclaresBash pins the step to it).
	cmd := exec.Command("bash", "--noprofile", "--norc", "-eo", "pipefail", scriptFile)
	cmd.Dir = dir
	cmd.Env = env
	logb, err := cmd.CombinedOutput()
	b, _ := os.ReadFile(out)
	primary := ""
	for _, l := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(l, "primary="); ok {
			primary = v
		}
	}
	if err != nil {
		t.Logf("mode step output:\n%s", logb)
	}
	return primary, err
}

func TestModeStep_PRCandidateAddsForgeInit_StaysGithub_Issue149(t *testing.T) {
	got, err := runMode(t, modeCase{event: "pull_request", head: map[string]string{
		"gitops/clusters/pwn/config/forge-init.yaml": forgeInit,
	}})
	if err != nil || got != "github" {
		t.Errorf("base has no forge-init.yaml; candidate-only file must not switch mode: primary=%q err=%v", got, err)
	}
}

func TestModeStep_PRCandidateMovesCIForgeInit_StaysGithub_Issue149(t *testing.T) {
	got, err := runMode(t, modeCase{event: "pull_request",
		base: map[string]string{"gitops/clusters/ci-lab/config/forge-init.yaml": forgeInit},
		head: map[string]string{"gitops/clusters/prod/config/forge-init.yaml": forgeInit},
	})
	if err != nil || got != "github" {
		t.Errorf("base is github primary; modifying candidate forge-init.yaml must not switch mode: primary=%q err=%v",
			got, err)
	}
}

func TestModeStep_PRTrustedBaseForgejo_StaysForgejo_Issue149(t *testing.T) {
	got, err := runMode(t, modeCase{event: "pull_request",
		base: map[string]string{"gitops/clusters/prod/config/forge-init.yaml": forgeInit},
		head: map[string]string{},
	})
	if err != nil || got != "forgejo" {
		t.Errorf("base declares forgejo primary; the candidate deleting it must not change mode: primary=%q err=%v",
			got, err)
	}
}

func TestModeStep_PRWithoutBaseSHA_FailsClosed_Issue149(t *testing.T) {
	got, err := runMode(t, modeCase{event: "pull_request", noBase: true})
	if err == nil {
		t.Errorf("a pull_request without a base SHA must fail the mode step, got primary=%q", got)
	}
}

func TestModeStep_PRNoForgeInit_Github_Issue149(t *testing.T) {
	got, err := runMode(t, modeCase{event: "pull_request", head: map[string]string{"src/x.go": "package x\n"}})
	if err != nil || got != "github" {
		t.Errorf("primary=%q err=%v, want github", got, err)
	}
}

// Legitimate push-mode selection (plan 52 section 8.4) is unchanged: the pushed revision is the trusted one.
func TestModeStep_PushModes_Issue149(t *testing.T) {
	for name, c := range map[string]struct {
		files map[string]string
		want  string
	}{
		"none":      {nil, "github"},
		"ci-only":   {map[string]string{"gitops/clusters/ci-lab/config/forge-init.yaml": forgeInit}, "github"},
		"non-ci":    {map[string]string{"gitops/clusters/prod/config/forge-init.yaml": forgeInit}, "forgejo"},
		"mixed":     {map[string]string{"gitops/clusters/ci-lab/config/forge-init.yaml": forgeInit, "gitops/clusters/prod/config/forge-init.yaml": forgeInit}, "forgejo"},
		"elsewhere": {map[string]string{"gitops/clusters/prod/forge-init.yaml": forgeInit}, "github"},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := runMode(t, modeCase{event: "push", base: c.files})
			if err != nil || got != c.want {
				t.Errorf("primary=%q err=%v, want %s", got, err, c.want)
			}
		})
	}
}

// Issue #149: if the mode job fails, verify and tdd-discipline must still run. A job skipped by its `if:` counts as
// a passing required check, so only a successful mode job that chose forgejo may skip them.
func TestVerifyJobsRunWhenModeFails_Issue149(t *testing.T) {
	jobs := jobsOf(mustWorkflow(t, "verify.yaml"))
	for _, j := range []string{"verify", "tdd-discipline"} {
		cond := ""
		for _, l := range jobs[j] {
			if indentOf(l) == 4 && strings.HasPrefix(strings.TrimSpace(l), "if:") {
				cond = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(l), "if:"))
			}
		}
		if !strings.Contains(cond, "!cancelled()") || !regexp.MustCompile(`needs\.mode\.result\s*!=\s*'success'`).MatchString(cond) {
			t.Errorf("job %s: `if: %s` skips the job when the mode job fails; want !cancelled() and "+
				"needs.mode.result != 'success' as a run condition", j, cond)
		}
	}
}

func TestModeStep_PRToNonDefaultBase_StaysGithub_Issue149(t *testing.T) {
	got, err := runMode(t, modeCase{event: "pull_request", baseRef: "feature",
		base: map[string]string{"gitops/clusters/prod/config/forge-init.yaml": forgeInit},
	})
	if err != nil || got != "github" {
		t.Errorf("only a default-branch base may select forgejo; a PR into another branch must run verify: "+
			"primary=%q err=%v", got, err)
	}
}

func TestModeStep_DispatchOnNonDefaultRef_StaysGithub_Issue149(t *testing.T) {
	for _, ev := range []string{"workflow_dispatch", "push"} {
		got, err := runMode(t, modeCase{event: ev, ref: "refs/heads/pr-branch",
			base: map[string]string{"gitops/clusters/prod/config/forge-init.yaml": forgeInit},
		})
		if err != nil || got != "github" {
			t.Errorf("%s on a non-default ref: its tree must not select forgejo: primary=%q err=%v", ev, got, err)
		}
	}
	got, err := runMode(t, modeCase{event: "workflow_dispatch",
		base: map[string]string{"gitops/clusters/prod/config/forge-init.yaml": forgeInit},
	})
	if err != nil || got != "forgejo" {
		t.Errorf("workflow_dispatch on the default branch reads its tree: primary=%q err=%v, want forgejo", got, err)
	}
}

func TestModeStep_PRUnreadableBase_FailsClosed_Issue149(t *testing.T) {
	got, err := runMode(t, modeCase{event: "pull_request", badBase: true})
	if err == nil {
		t.Errorf("a base SHA that cannot be read must fail the mode step, got primary=%q", got)
	}
}

func TestModeStep_NonASCIIClusterName_Issue149(t *testing.T) {
	got, err := runMode(t, modeCase{event: "push",
		base: map[string]string{"gitops/clusters/pr\u00f8d/config/forge-init.yaml": forgeInit},
	})
	if err != nil || got != "forgejo" {
		t.Errorf("a cluster name with non-ASCII bytes must be read like any other: primary=%q err=%v", got, err)
	}
}

func TestModeStepDeclaresBash_Issue149(t *testing.T) {
	lines := jobsOf(mustWorkflow(t, "verify.yaml"))["mode"]
	if !containsLine(lines, "shell: bash") {
		t.Error("the mode step must declare `shell: bash` (bash --noprofile --norc -eo pipefail), which these tests run")
	}
}

// The verify/tdd-discipline condition exactly: they skip only when mode succeeded and chose forgejo.
func TestVerifyJobsIfTruthTable_Issue149(t *testing.T) {
	want := "!cancelled() && (needs.mode.result != 'success' || needs.mode.outputs.primary == 'github')"
	jobs := jobsOf(mustWorkflow(t, "verify.yaml"))
	for _, j := range []string{"verify", "tdd-discipline"} {
		cond := ""
		for _, l := range jobs[j] {
			if indentOf(l) == 4 && strings.HasPrefix(strings.TrimSpace(l), "if:") {
				cond = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(l), "if:"))
			}
		}
		cond = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(cond, "${{"), "}}"))
		cond = strings.TrimSuffix(cond, " && github.event_name == 'pull_request'")
		if cond != want {
			t.Errorf("job %s: if: %q, want %q (runs unless mode succeeded with forgejo)", j, cond, want)
		}
	}
}
