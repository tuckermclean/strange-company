package lint_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// F09 S9b (plan 05 section 14.6 and 22, plan 52 sections 8.1, 8.4 and 8.5, plan 02 sections 7.1, 14 and 22): the wiring
// that makes CI run the lab smoke test (05-AC-6) on a KVM-capable hosted runner and log `sc-lab: kvm ok` (05-AC-8).
// The evidence itself is tier E (the CI run); these tests pin the workflow job, the make target, the harness and the
// ci image packages. The job `e2e-lab` the tests expect, in .github/workflows/e2e.yaml:
//   needs: changes; if: needs.changes.outputs.lab == 'true'; runs-on: ubuntu-24.04; permissions {contents: read};
//   timeout-minutes; checkout with persist-credentials: false; a step writing the udev rule
//   KERNEL=="kvm", GROUP="kvm", MODE="0666" and running `udevadm control --reload-rules` and `udevadm trigger`; a step
//   running ./bootstrap exec --image ci --kvm --network host -- make e2e-lab LAB_SUBSTRATE=lab-small LAB_OS=debian-13
//   LAB_STOP_AFTER=provision; an `if: always()` actions/upload-artifact step for out/e2e-lab (provider-output.json and
//   the console logs). e2e-gate lists it in needs and checks needs.e2e-lab.result.

const labCommand = "./bootstrap exec --image ci --kvm --network host -- make e2e-lab LAB_SUBSTRATE=lab-small LAB_OS=debian-13 LAB_STOP_AFTER=provision"

func labJob(t *testing.T) string {
	t.Helper()
	lines, ok := jobsOf(mustWorkflow(t, "e2e.yaml"))["e2e-lab"]
	if !ok {
		t.Fatalf("e2e.yaml lacks job %q (05-AC-6)", "e2e-lab")
	}
	return strings.Join(lines, "\n")
}

// 05-AC-6, 05-AC-8: the job runs on the hosted ubuntu-24.04 runner, enables KVM and runs the literal command.
func TestWorkflow_E2ELabJob_RunsLiteralCommandWithKVM_05AC6_05AC8(t *testing.T) {
	body := labJob(t)
	for name, re := range map[string]*regexp.Regexp{
		"runs-on ubuntu-24.04":       regexp.MustCompile(`runs-on:\s*ubuntu-24\.04\b`),
		"needs: changes":             regexp.MustCompile(`needs:\s*(\[\s*)?changes\b`),
		"selected by outputs.lab":    regexp.MustCompile(`if:.*needs\.changes\.outputs\.lab\s*==\s*'true'`),
		"timeout-minutes":            regexp.MustCompile(`timeout-minutes:\s*\d+`),
		"contents: read":             regexp.MustCompile(`contents:\s*read`),
		"persist-credentials: false": regexp.MustCompile(`persist-credentials:\s*false`),
		"the kvm udev rule":          regexp.MustCompile(`KERNEL=="kvm",\s*GROUP="kvm",\s*MODE="0666"`),
		"udev reload":                regexp.MustCompile(`udevadm control --reload-rules`),
		"udev trigger":               regexp.MustCompile(`udevadm trigger`),
	} {
		if !re.MatchString(body) {
			t.Errorf("job e2e-lab lacks %s", name)
		}
	}
	if !strings.Contains(body, labCommand) {
		t.Errorf("job e2e-lab has no step running exactly:\n  %s", labCommand)
	}
	// The udev rule must be installed before the lab runs.
	if r, c := strings.Index(body, "udevadm trigger"), strings.Index(body, labCommand); r >= 0 && c >= 0 && r > c {
		t.Errorf("the udev rule step must come before the lab step")
	}
}

// 05-AC-6: the lab run artifacts (provider-output.json, console logs) are uploaded even when the run fails.
func TestWorkflow_E2ELabJob_UploadsLabArtifacts_05AC6(t *testing.T) {
	body := labJob(t)
	i := strings.Index(body, "actions/upload-artifact@")
	if i < 0 {
		t.Fatalf("job e2e-lab has no actions/upload-artifact step")
	}
	step := body[i:]
	if j := strings.Index(step[1:], "\n      - name:"); j >= 0 {
		step = step[:j+1]
	}
	pre := body[:i]
	if k := strings.LastIndex(pre, "\n      - "); k >= 0 {
		pre = pre[k:]
	}
	stepAll := pre + step
	if !regexp.MustCompile(`actions/upload-artifact@[0-9a-f]{40}`).MatchString(step) {
		t.Errorf("upload-artifact must be pinned by commit SHA")
	}
	if !regexp.MustCompile(`if:\s*always\(\)`).MatchString(stepAll) {
		t.Errorf("the artifact step must run with `if: always()`")
	}
	if !strings.Contains(stepAll, "out/e2e-lab") {
		t.Errorf("the artifact step must upload out/e2e-lab (provider-output.json, *.console, up.log, bootstrap.log)")
	}
}

// 05-AC-6: e2e-gate waits for e2e-lab and fails when it failed or was cancelled.
func TestWorkflow_E2EGateAwaitsLab_52AC1_05AC6(t *testing.T) {
	gate := strings.Join(jobsOf(mustWorkflow(t, "e2e.yaml"))["e2e-gate"], "\n")
	if !regexp.MustCompile(`needs:[^\n]*\be2e-lab\b|(?m)^\s*-\s*e2e-lab\s*$`).MatchString(gate) {
		t.Errorf("e2e-gate must list e2e-lab in needs")
	}
	if !strings.Contains(gate, "needs.e2e-lab.result") {
		t.Errorf("e2e-gate must check needs.e2e-lab.result")
	}
}

// 05-AC-6 (plan 52 section 8.1): the path filter selects e2e-lab for infra/ansible/, infra/lab/ and
// src/internal/bootstrap/ (plus the lab's own inputs, so this change runs it), the schedule selects it on Sundays only,
// and tags select it.
func TestWorkflow_E2ELabSelection_Paths_Weekly_Tags_05AC6(t *testing.T) {
	changes := strings.Join(jobsOf(mustWorkflow(t, "e2e.yaml"))["changes"], "\n")
	if !regexp.MustCompile(`(?m)^\s*lab:\s*\$\{\{\s*steps\.select\.outputs\.lab\s*\}\}`).MatchString(changes) {
		t.Errorf("the changes job must expose the output `lab: ${{ steps.select.outputs.lab }}`")
	}
	if !regexp.MustCompile(`echo "lab=\$lab"`).MatchString(changes) {
		t.Errorf(`the select step must write "lab=$lab" to $GITHUB_OUTPUT`)
	}
	for _, p := range []string{`^infra/ansible/`, `^infra/lab/`, `^src/internal/bootstrap/`, `^test/e2e/lab/`, `^src/internal/lab/`, `^src/cmd/sc-lab/`,
		`^versions/lab-images\.lock\.yaml$`, `^\.github/workflows/e2e\.yaml$`} {
		if !strings.Contains(changes, p) {
			t.Errorf("the lab path filter lacks %s", p)
		}
	}
	if !regexp.MustCompile(`date\s+(-u\s+)?\+%u`).MatchString(changes) {
		t.Errorf("the schedule leg must select the lab only on Sundays (date -u +%%u)")
	}
	if !regexp.MustCompile(`refs/tags/|GITHUB_REF_TYPE|ref_type`).MatchString(changes) {
		t.Errorf("the select step must select the lab for tags")
	}
}

func mkSources(t *testing.T) string {
	t.Helper()
	files, _ := filepath.Glob(filepath.Join(repoRoot(t), "mk", "*.mk"))
	var b strings.Builder
	for _, f := range files {
		c, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		b.Write(c)
		b.WriteByte('\n')
	}
	return b.String()
}

// 05-AC-6: `make e2e-lab` exists and runs test/e2e/lab/run with LAB_SUBSTRATE, LAB_OS and LAB_STOP_AFTER.
func TestMake_E2ELabTargetRunsHarness_05AC6(t *testing.T) {
	src := mkSources(t)
	m := regexp.MustCompile(`(?m)^e2e-lab:[^\n]*\n((?:\t[^\n]*\n|[ \t]*\n)*)`).FindStringSubmatch(src)
	if m == nil {
		t.Fatalf("no `e2e-lab:` target in mk/*.mk")
	}
	if !regexp.MustCompile(`(?m)^\.PHONY:.*\be2e-lab\b`).MatchString(src) {
		t.Errorf("e2e-lab is not declared .PHONY")
	}
	recipe := m[1]
	if !strings.Contains(recipe, "test/e2e/lab/run") {
		t.Errorf("the e2e-lab recipe does not run test/e2e/lab/run:\n%s", recipe)
	}
	for _, v := range []string{"LAB_SUBSTRATE", "LAB_OS", "LAB_STOP_AFTER"} {
		if !strings.Contains(recipe, "$("+v+")") {
			t.Errorf("the e2e-lab recipe does not pass $(%s) to the harness", v)
		}
	}
	for _, v := range []string{"LAB_SUBSTRATE", "LAB_OS", "LAB_STOP_AFTER"} {
		if !regexp.MustCompile(`(?m)^` + v + `\s*\??=`).MatchString(src) {
			t.Errorf("mk/*.mk declares no default for %s", v)
		}
	}
}

// 05-AC-6: the harness exists, is executable and parses as POSIX sh; it calls the pieces in the plan 05 order.
func TestLabHarness_ExistsExecutableAndWired_05AC6_05AC8(t *testing.T) {
	p := filepath.Join(repoRoot(t), "test", "e2e", "lab", "run")
	st, err := os.Stat(p)
	if err != nil {
		t.Fatalf("test/e2e/lab/run: %v", err)
	}
	if st.Mode().Perm()&0o111 == 0 {
		t.Errorf("test/e2e/lab/run is not executable")
	}
	if out, err := exec.Command("sh", "-n", p).CombinedOutput(); err != nil {
		t.Errorf("sh -n: %v\n%s", err, out)
	}
	b, _ := os.ReadFile(p)
	s := string(b)
	for _, w := range []string{"sc-lab up", "sc-lab env", "sc-bootstrap", "SC_TEST_STOP_AFTER", "trap", "sc-lab down", "pgrep -c qemu-system",
		"sc-lab: kvm ok", "host_key_source", "10.77.0.11", "2201", "cloud-init"} {
		if !strings.Contains(s, w) {
			t.Errorf("the harness never mentions %q", w)
		}
	}
}

// 05-AC-6 (plan 02 lines 140 and 209): the ci image carries what sc-lab runs: QEMU, qemu-img, cloud-localds and the ISO tool.
func TestToolsLock_CIImageHasQemuAndCloudInitTools_05AC6(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(repoRoot(t), "toolbox", "tools.lock.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^\s+ci:\s*\[([^\]]*)\]`).FindStringSubmatch(string(b))
	if m == nil {
		t.Fatalf("tools.lock.yaml has no aptPackages ci list")
	}
	have := map[string]bool{}
	for _, p := range strings.Split(m[1], ",") {
		have[strings.TrimSpace(p)] = true
	}
	for _, want := range []string{"qemu-system-x86", "qemu-utils", "cloud-image-utils", "genisoimage"} {
		if !have[want] {
			t.Errorf("aptPackages.ci lacks %s (has %v)", want, have)
		}
	}
	for _, keep := range []string{"make", "strace"} {
		if !have[keep] {
			t.Errorf("aptPackages.ci lost %s", keep)
		}
	}
}
