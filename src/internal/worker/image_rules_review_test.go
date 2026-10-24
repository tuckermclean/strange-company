package worker_test

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
)

// S6d review round 1 (static Layer-1 specs for findings N4, N5, N6). The runtime counterparts are in
// test/e2e/worker/run (image-test); the checks here run without a container engine or network.

const testInputsID = "0123456789abcdef0123456789abcdef01234567"

// envOfStage returns the KEY=VALUE pairs (and the legacy `ENV KEY VALUE` form) of the ENV instructions of a stage.
func envOfStage(s containerStage) map[string]string {
	env := map[string]string{}
	for _, l := range s.lines {
		f := strings.Fields(l)
		if len(f) < 2 || !strings.EqualFold(f[0], "ENV") {
			continue
		}
		if !strings.Contains(f[1], "=") {
			env[f[1]] = strings.Join(f[2:], " ")
			continue
		}
		for _, kv := range f[1:] {
			if k, v, ok := strings.Cut(kv, "="); ok {
				env[k] = strings.Trim(v, `"`)
			}
		}
	}
	return env
}

// checkGoToolchain: plan 28 section 13.1 contents row ("the Go toolchain (section 9.3)") and section 9.3 ("Go toolchain in
// images ... local validation matches trusted CI"): the final stage carries the toolchain of the pinned golang stage at
// /usr/local/go, has it on PATH, and never downloads another one (GOTOOLCHAIN=local), so a fake run against
// test/e2e/fake-target (validation `go test ./...`) works on the read-only root, offline.
func checkGoToolchain(text string) []string {
	var bad []string
	stages := parseContainerfile(text)
	if len(stages) < 2 {
		return []string{"the Containerfile has no stage besides the final one to take the Go toolchain from"}
	}
	final := stages[len(stages)-1]
	golangStages := map[string]bool{}
	for _, s := range stages[:len(stages)-1] {
		if s.alias != "" && regexp.MustCompile(`(^|/)golang[@:]`).MatchString(s.ref) {
			golangStages[s.alias] = true
		}
	}
	copied := false
	for _, l := range final.lines {
		f := strings.Fields(l)
		if len(f) < 4 || !strings.EqualFold(f[0], "COPY") {
			continue
		}
		from := ""
		var args []string
		for _, a := range f[1:] {
			if v, ok := strings.CutPrefix(a, "--from="); ok {
				from = v
			} else if !strings.HasPrefix(a, "--") {
				args = append(args, a)
			}
		}
		if golangStages[from] && len(args) == 2 && strings.TrimSuffix(args[0], "/") == "/usr/local/go" && strings.TrimSuffix(args[1], "/") == "/usr/local/go" {
			copied = true
		}
	}
	if !copied {
		bad = append(bad, "the final stage never runs `COPY --from=<golang stage> /usr/local/go /usr/local/go` (28 section 13.1 contents: the Go toolchain, section 9.3)")
	}
	env := envOfStage(final)
	if env["GOTOOLCHAIN"] != "local" {
		bad = append(bad, fmt.Sprintf("the final stage sets ENV GOTOOLCHAIN=%q, want local (the toolchain is the image's, nothing is downloaded at run time)", env["GOTOOLCHAIN"]))
	}
	if !strings.Contains(env["PATH"], "/usr/local/go/bin") {
		bad = append(bad, fmt.Sprintf("the final stage's ENV PATH %q does not contain /usr/local/go/bin", env["PATH"]))
	}
	return bad
}

// workerVersionLabel evaluates the sc-worker-version LABEL of the final stage the way a Dockerfile expands it (POSIX
// parameter expansion over the stage's ARG defaults, SC_INPUTS_ID replaced by id) and returns the value.
func workerVersionLabel(text, id string) (string, error) {
	stages := parseContainerfile(text)
	if len(stages) == 0 {
		return "", fmt.Errorf("no FROM")
	}
	final := stages[len(stages)-1]
	m := regexp.MustCompile(`strange\.company/sc-worker-version=("([^"]*)"|[^\s"\\]+)`)
	var raw string
	for _, l := range final.lines {
		f := strings.Fields(l)
		if len(f) > 1 && strings.EqualFold(f[0], "LABEL") {
			if sm := m.FindStringSubmatch(l); sm != nil {
				raw = sm[1]
				if sm[2] != "" {
					raw = sm[2]
				}
			}
		}
	}
	if raw == "" {
		return "", fmt.Errorf("no LABEL strange.company/sc-worker-version in the final stage")
	}
	var script strings.Builder
	script.WriteString("SC_INPUTS_ID=" + id + "\n")
	for _, l := range final.lines {
		f := strings.Fields(l)
		if len(f) < 2 || !strings.EqualFold(f[0], "ARG") || !strings.Contains(f[1], "=") || strings.HasPrefix(f[1], "SC_INPUTS_ID=") {
			continue
		}
		if strings.ContainsAny(l, "`;|&<>") || strings.Contains(l, "$(") {
			return "", fmt.Errorf("ARG line %q is not a plain expansion", l)
		}
		script.WriteString(strings.Join(f[1:], " ") + "\n")
	}
	if strings.ContainsAny(raw, "`;|&<>\"\\") || strings.Contains(raw, "$(") {
		return "", fmt.Errorf("label value %q is not a plain expansion", raw)
	}
	script.WriteString(`printf '%s' "` + raw + `"` + "\n")
	cmd := exec.Command("sh")
	cmd.Stdin = strings.NewReader(script.String())
	cmd.Env = []string{"PATH=" + os.Getenv("PATH")}
	out, err := cmd.Output()
	return string(out), err
}

// checkWorkerVersionLabel: the sc-worker-version label is the version stamped into the binary (plan 01 section 6.2 flags,
// CD-66(4): Version=in-<first 12 characters of the inputs id>), not a literal.
func checkWorkerVersionLabel(text string) []string {
	got, err := workerVersionLabel(text, testInputsID)
	if err != nil {
		return []string{"sc-worker-version label: " + err.Error()}
	}
	if want := "in-" + testInputsID[:12]; got != want {
		return []string{fmt.Sprintf("LABEL strange.company/sc-worker-version evaluates to %q for SC_INPUTS_ID=%s, want %q (the binary's stamped Version, CD-66(4))", got, testInputsID, want)}
	}
	return nil
}

// N4 (28-AC-3, plan 28 section 13.1 contents row and section 9.3): the Go toolchain is in the image.
func TestHarnessFakeImage_HasGoToolchain_28AC3(t *testing.T) {
	text, ok := readRepoFile(t, imageDir+"/Containerfile")
	if !ok {
		return
	}
	for _, msg := range checkGoToolchain(text) {
		t.Error(msg)
	}
}

// N6 (28-AC-3, CD-66(4)): the sc-worker-version label follows the inputs id like the stamped binary version.
func TestHarnessFakeImage_WorkerVersionLabel_28AC3(t *testing.T) {
	text, ok := readRepoFile(t, imageDir+"/Containerfile")
	if !ok {
		return
	}
	for _, msg := range checkWorkerVersionLabel(text) {
		t.Error(msg)
	}
}

// The two checkers above catch violations of a model definition (guard: passes now).
func TestHarnessImageRules_ToolchainAndLabelCheckersCatchViolations_28AC3(t *testing.T) {
	golang := lockDigest(t, "golang")
	debian := lockDigest(t, "debian")
	model := `FROM docker.io/library/golang@` + golang + ` AS gobuild
ARG SC_INPUTS_ID=unknown
RUN go build -o /out/ ./cmd/sc-worker

FROM docker.io/library/debian@` + debian + `
ARG SC_INPUTS_ID=unknown
ENV GOTOOLCHAIN=local PATH=/usr/local/go/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
COPY --from=gobuild /usr/local/go /usr/local/go
LABEL strange.company/harness="fake" \
      strange.company/sc-worker-version="in-${SC_INPUTS_ID%${SC_INPUTS_ID#????????????}}"
USER 65532:65532
`
	if bad := append(checkGoToolchain(model), checkWorkerVersionLabel(model)...); len(bad) != 0 {
		t.Fatalf("the model definition is rejected: %v", bad)
	}
	argForm := strings.Replace(model, `LABEL strange.company/harness="fake" \
      strange.company/sc-worker-version="in-${SC_INPUTS_ID%${SC_INPUTS_ID#????????????}}"`,
		"ARG V=in-${SC_INPUTS_ID%${SC_INPUTS_ID#????????????}}\nLABEL strange.company/sc-worker-version=\"${V}\"", 1)
	if bad := append(checkGoToolchain(argForm), checkWorkerVersionLabel(argForm)...); len(bad) != 0 {
		t.Errorf("the ARG form of the label is rejected: %v", bad)
	}
	for name, c := range map[string]struct {
		check  func(string) []string
		mutate func(string) string
	}{
		"toolchain not copied": {checkGoToolchain, func(s string) string {
			return strings.Replace(s, "COPY --from=gobuild /usr/local/go /usr/local/go\n", "", 1)
		}},
		"toolchain wrong dir": {checkGoToolchain, func(s string) string {
			return strings.Replace(s, "/usr/local/go /usr/local/go", "/usr/local/go /opt/go", 1)
		}},
		"toolchain from debian": {checkGoToolchain, func(s string) string { return strings.Replace(s, "--from=gobuild", "--from=0", 1) }},
		"no GOTOOLCHAIN":        {checkGoToolchain, func(s string) string { return strings.Replace(s, "GOTOOLCHAIN=local ", "", 1) }},
		"GOTOOLCHAIN auto":      {checkGoToolchain, func(s string) string { return strings.Replace(s, "GOTOOLCHAIN=local", "GOTOOLCHAIN=auto", 1) }},
		"go not on PATH":        {checkGoToolchain, func(s string) string { return strings.Replace(s, "/usr/local/go/bin:", "", 1) }},
		"literal label": {checkWorkerVersionLabel, func(s string) string {
			return strings.Replace(s, `"in-${SC_INPUTS_ID%${SC_INPUTS_ID#????????????}}"`, `"0.1.0"`, 1)
		}},
		"untruncated label": {checkWorkerVersionLabel, func(s string) string {
			return strings.Replace(s, `"in-${SC_INPUTS_ID%${SC_INPUTS_ID#????????????}}"`, `"in-${SC_INPUTS_ID}"`, 1)
		}},
		"no prefix": {checkWorkerVersionLabel, func(s string) string {
			return strings.Replace(s, `"in-${SC_INPUTS_ID%`, `"${SC_INPUTS_ID%`, 1)
		}},
	} {
		mutated := c.mutate(model)
		if mutated == model {
			t.Errorf("mutation %q did not change the model", name)
			continue
		}
		if len(c.check(mutated)) == 0 {
			t.Errorf("mutation %q is not caught", name)
		}
	}
}

// N5 (plan 01 section 8.1, CD-64 pattern of `make toolbox`): the host-only worker goals must be run alone. Combined with
// another goal the Makefile must say so, not fail with "No rule to make target".
func TestMakeTargets_WorkerLayer2_MustRunAlone_28AC3(t *testing.T) {
	makeBin, err := exec.LookPath("make")
	if err != nil {
		t.Skip("make is not installed")
	}
	root := repoRoot(t)
	for _, args := range [][]string{
		{"lint", "test-worker-component"},
		{"test-worker-component", "lint"},
		{"lint", "harness-image-test", "IMAGE=harness-fake"},
		{"harness-image-test", "lint", "IMAGE=harness-fake"},
	} {
		cmd := exec.Command(makeBin, append([]string{"-n"}, args...)...)
		cmd.Dir = root
		for _, kv := range os.Environ() {
			if !strings.HasPrefix(kv, "SC_IN_CONTAINER=") {
				cmd.Env = append(cmd.Env, kv)
			}
		}
		out, err := cmd.CombinedOutput()
		if err == nil {
			t.Errorf("make -n %s succeeded; it must fail because the worker goals must be run alone:\n%s", strings.Join(args, " "), out)
			continue
		}
		if !regexp.MustCompile(`(?i)must be run alone`).Match(out) {
			t.Errorf("make -n %s fails without a \"must be run alone\" error (like `make toolbox`):\n%s", strings.Join(args, " "), out)
		}
	}
}
