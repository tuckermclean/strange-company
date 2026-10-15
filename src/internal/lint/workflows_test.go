package lint_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Real-tree checks of the interim CI workflows (plan 52 sections 4 and 8.4). The module has no YAML
// dependency, so these read the files line by line.

func workflowFiles(t *testing.T) map[string]string {
	t.Helper()
	root := repoRoot(t)
	files, _ := filepath.Glob(filepath.Join(root, ".github", "workflows", "*.yaml"))
	out := map[string]string{}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		out[filepath.Base(f)] = string(b)
	}
	return out
}

func mustWorkflow(t *testing.T, name string) string {
	t.Helper()
	w, ok := workflowFiles(t)[name]
	if !ok {
		t.Fatalf(".github/workflows/%s does not exist", name)
	}
	return w
}

func indentOf(l string) int { return len(l) - len(strings.TrimLeft(l, " ")) }

// topBlock returns the lines below a top-level `key:` line (indent 0) up to the next top-level key.
func topBlock(body, key string) []string {
	lines := strings.Split(body, "\n")
	for i, l := range lines {
		if strings.HasPrefix(l, key+":") {
			var out []string
			for _, n := range lines[i+1:] {
				if strings.TrimSpace(n) != "" && indentOf(n) == 0 && !strings.HasPrefix(n, "#") {
					break
				}
				out = append(out, n)
			}
			return out
		}
	}
	return nil
}

// jobsOf splits the `jobs:` block into job ID -> its lines.
func jobsOf(body string) map[string][]string {
	jobs := map[string][]string{}
	var cur string
	for _, l := range topBlock(body, "jobs") {
		if strings.TrimSpace(l) == "" || strings.HasPrefix(strings.TrimSpace(l), "#") {
			if cur != "" {
				jobs[cur] = append(jobs[cur], l)
			}
			continue
		}
		if indentOf(l) == 2 && strings.HasSuffix(strings.TrimSpace(l), ":") {
			cur = strings.TrimSuffix(strings.TrimSpace(l), ":")
			jobs[cur] = nil
			continue
		}
		if cur != "" {
			jobs[cur] = append(jobs[cur], l)
		}
	}
	return jobs
}

// permsIn finds a `permissions:` mapping among lines whose indent is exactly ind, as flow or block style.
func permsIn(lines []string, ind int) (map[string]string, bool) {
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if indentOf(l) != ind || !strings.HasPrefix(t, "permissions:") {
			continue
		}
		rest := strings.TrimSpace(strings.TrimPrefix(t, "permissions:"))
		m := map[string]string{}
		if strings.HasPrefix(rest, "{") {
			for _, kv := range strings.Split(strings.Trim(rest, "{} "), ",") {
				if k, v, ok := strings.Cut(kv, ":"); ok {
					m[strings.TrimSpace(k)] = strings.TrimSpace(v)
				}
			}
			return m, true
		}
		for _, n := range lines[i+1:] {
			if strings.TrimSpace(n) == "" {
				continue
			}
			if indentOf(n) <= ind {
				break
			}
			if k, v, ok := strings.Cut(strings.TrimSpace(n), ":"); ok {
				m[strings.TrimSpace(k)] = strings.TrimSpace(v)
			}
		}
		return m, true
	}
	return nil, false
}

// 52-AC-1: verify.yaml exists with exactly the jobs mode, verify, tdd-discipline.
func TestWorkflow_VerifyJobs_52AC1(t *testing.T) {
	jobs := jobsOf(mustWorkflow(t, "verify.yaml"))
	for _, j := range []string{"mode", "verify", "tdd-discipline"} {
		if _, ok := jobs[j]; !ok {
			t.Errorf("verify.yaml lacks job %q", j)
		}
	}
}

// 52-AC-1 / plan 52 8.4: the mode job outputs `primary`; verify and tdd-discipline depend on it and run only on github.
func TestWorkflow_ModeGating_52AC1(t *testing.T) {
	jobs := jobsOf(mustWorkflow(t, "verify.yaml"))
	if !strings.Contains(strings.Join(jobs["mode"], "\n"), "primary") {
		t.Error("mode job must output `primary` (forgejo or github)")
	}
	for _, j := range []string{"verify", "tdd-discipline"} {
		body := strings.Join(jobs[j], "\n")
		if !regexp.MustCompile(`needs:.*\bmode\b|needs:\s*\n(\s*-\s*\S+\s*\n)*\s*-\s*mode\b`).MatchString(body) {
			t.Errorf("job %s must declare needs: mode", j)
		}
		if !regexp.MustCompile(`if:.*needs\.mode\.outputs\.primary\s*==\s*'github'`).MatchString(body) {
			t.Errorf("job %s must run only when needs.mode.outputs.primary == 'github'", j)
		}
	}
}

// 52-AC-1 / plan 52 8.4: runner label, checkout depth, cache of .cache/ and of the ci image, concurrency, make verify.
func TestWorkflow_VerifyShape_52AC1(t *testing.T) {
	w := mustWorkflow(t, "verify.yaml")
	for _, j := range []string{"mode", "verify", "tdd-discipline"} {
		if !strings.Contains(strings.Join(jobsOf(w)[j], "\n"), "runs-on: ubuntu-24.04") {
			t.Errorf("job %s must run on ubuntu-24.04", j)
		}
	}
	for _, want := range []string{"fetch-depth: 0", "actions/cache@", "src/go.sum", "versions/platform.lock.yaml", ".cache/ci-image", "make verify", "tdd-discipline-check", "concurrency:"} {
		if !strings.Contains(w, want) {
			t.Errorf("verify.yaml lacks %q", want)
		}
	}
}

// Slice obligation from the S1 review (02-AC-14..16 need Docker rootful results): GitHub Actions runs the
// real-engine checks and the pin checks with Docker.
func TestWorkflow_RunsRealEngineChecksOnDocker_52AC1(t *testing.T) {
	all := ""
	for _, name := range []string{"verify.yaml", "e2e.yaml"} {
		all += workflowFiles(t)[name] + "\n"
	}
	if !regexp.MustCompile(`SC_CONTAINER_ENGINE[=:]\s*"?docker"?`).MatchString(all) {
		t.Error("no step sets SC_CONTAINER_ENGINE=docker")
	}
	for _, s := range []string{"sh test/e2e/wrapper/real-engine.sh", "sh test/e2e/wrapper/check-pins.sh"} {
		if !strings.Contains(all, s) {
			t.Errorf("verify.yaml or e2e.yaml lacks a step running `%s`", s)
		}
	}
}

// 52-AC-1 / plan 52 8.4: e2e.yaml exists, has the e2e-gate job that always runs and waits for the other jobs.
func TestWorkflow_E2EGate_52AC1(t *testing.T) {
	w := mustWorkflow(t, "e2e.yaml")
	jobs := jobsOf(w)
	gate, ok := jobs["e2e-gate"]
	if !ok {
		t.Fatal("e2e.yaml lacks job e2e-gate")
	}
	body := strings.Join(gate, "\n")
	if !strings.Contains(body, "always()") {
		t.Error("e2e-gate must always run (if: always())")
	}
	if !strings.Contains(body, "needs:") {
		t.Error("e2e-gate must declare needs: over the selected tier-E jobs")
	}
	if !strings.Contains(w, "pull_request") {
		t.Error("e2e.yaml must run on pull requests")
	}
}

// 52-AC-1 / plan 52 8.4: every job of every workflow declares permissions {contents: read}, either at the
// workflow level or on the job, except publish.yaml (conditional: it is not part of F01; plan 23 owns it).
func TestWorkflow_Permissions_52AC1(t *testing.T) {
	ws := workflowFiles(t)
	if len(ws) < 3 {
		t.Fatalf("expected verify.yaml, e2e.yaml and stage0-validate.yaml, found %d files", len(ws))
	}
	for name, w := range ws {
		jobs := jobsOf(w)
		top, hasTop := permsIn(strings.Split(w, "\n"), 0)
		for j, lines := range jobs {
			p, ok := permsIn(lines, 4)
			if !ok {
				p, ok = top, hasTop
			}
			if name == "publish.yaml" {
				if len(p) != 3 || p["contents"] != "read" || p["id-token"] != "write" || p["packages"] != "write" {
					t.Errorf("publish.yaml job %s: permissions must be exactly {contents: read, id-token: write, packages: write}, got %v", j, p)
				}
				if !regexp.MustCompile(`if:.*github\.repository\s*==`).MatchString(strings.Join(lines, "\n")) {
					t.Errorf("publish.yaml job %s lacks the upstream-repository `if:` guard", j)
				}
				continue
			}
			if !ok || len(p) != 1 || p["contents"] != "read" {
				t.Errorf("%s job %s: permissions must be exactly {contents: read}, got %v (declared=%v)", name, j, p, ok)
			}
		}
	}
}

// 52-AC-1: no `secrets.` reference except secrets.GITHUB_TOKEN; every non-local `uses:` is a 40-hex SHA.
func TestWorkflow_NoSecretsAndPinnedUses_52AC1(t *testing.T) {
	secretRef := regexp.MustCompile(`secrets\.([A-Za-z0-9_]+)`)
	uses := regexp.MustCompile(`(?m)^\s*-?\s*uses:\s*(\S+)`)
	pinned := regexp.MustCompile(`^[A-Za-z0-9._-]+/[A-Za-z0-9._/-]+@[0-9a-f]{40}$`)
	ws := workflowFiles(t)
	if _, ok := ws["verify.yaml"]; !ok {
		t.Fatal("verify.yaml does not exist")
	}
	for name, w := range ws {
		for _, m := range secretRef.FindAllStringSubmatch(w, -1) {
			if m[1] != "GITHUB_TOKEN" {
				t.Errorf("%s references secrets.%s", name, m[1])
			}
		}
		for _, m := range uses.FindAllStringSubmatch(w, -1) {
			if strings.HasPrefix(m[1], "./") {
				continue
			}
			if !pinned.MatchString(m[1]) {
				t.Errorf("%s: uses %s is not pinned to a 40-hex commit SHA", name, m[1])
			}
		}
	}
}

// 52-AC-1: `make lint` runs actionlint, and the pinned tool is in the ci toolset.
func TestLint_RunsActionlint_52AC1(t *testing.T) {
	root := repoRoot(t)
	ents, _ := filepath.Glob(filepath.Join(root, "mk", "*.mk"))
	recipe := ""
	for _, e := range ents {
		b, _ := os.ReadFile(e)
		if i := strings.Index(string(b), "\nlint:"); i >= 0 {
			recipe += string(b)[i:]
		}
	}
	if !strings.Contains(recipe, "actionlint") {
		t.Error("the `lint` target does not run actionlint")
	}
	tools := readFile(t, root, "toolbox/tools.lock.yaml")
	if !regexp.MustCompile(`(?m)^\s*- name: actionlint\s*$`).MatchString(tools) {
		t.Error("toolbox/tools.lock.yaml has no actionlint tool entry (set: ci)")
	}
	runMake(t, "lint")
}

// plan 01 7.1: secrets-scan and quarantine-check are tier V, so `make verify` runs them.
func TestVerifyRunsSecretsScanAndQuarantine_01AC8_52AC7(t *testing.T) {
	root := repoRoot(t)
	ents, _ := filepath.Glob(filepath.Join(root, "mk", "*.mk"))
	var targets string
	for _, e := range ents {
		b, _ := os.ReadFile(e)
		if m := regexp.MustCompile(`(?m)^VERIFY_TARGETS\s*:?=(.*)$`).FindStringSubmatch(string(b)); m != nil {
			targets = m[1]
		}
	}
	for _, want := range []string{"locks-check", "secrets-scan", "quarantine-check"} {
		if !regexp.MustCompile(`(^|\s)` + want + `(\s|$)`).MatchString(targets) {
			t.Errorf("VERIFY_TARGETS lacks %s: %q", want, targets)
		}
	}
}
