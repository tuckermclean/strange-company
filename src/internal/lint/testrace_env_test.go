package lint_test

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// CD-77: interim CI switches the Go race detector off on PR runs through the ENVIRONMENT
// (GO_TEST_RACE set to the empty string), never through the make command line.

func raceEnv(set bool, drop ...string) []string {
	drops := append([]string{"GO_TEST_RACE", "MAKEFLAGS", "MAKELEVEL", "MAKEOVERRIDES"}, drop...)
	var env []string
	for _, e := range os.Environ() {
		skip := false
		for _, d := range drops {
			if strings.HasPrefix(e, d+"=") {
				skip = true
			}
		}
		if !skip {
			env = append(env, e)
		}
	}
	if set {
		env = append(env, "GO_TEST_RACE=")
	}
	return env
}

func goArch(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("go", "env", "GOARCH").Output()
	if err != nil {
		t.Fatalf("go env GOARCH: %v", err)
	}
	return strings.TrimSpace(string(out))
}

func TestMakeTestRace_EnvEmptyDisablesRace_CD77(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	out, err := runCmd(t, root, 2*time.Minute, append(raceEnv(true, "SC_IN_CONTAINER"), "SC_IN_CONTAINER=1"), "make", "-n", "test")
	if err != nil {
		t.Fatalf("make -n test: %v\n%s", err, out)
	}
	if !strings.Contains(out, "go test") {
		t.Fatalf("no go test command printed:\n%s", out)
	}
	if strings.Contains(out, "-race") {
		t.Fatalf("GO_TEST_RACE= in the environment must remove -race:\n%s", out)
	}
}

func TestMakeTestRace_UnsetKeepsRaceOnAmd64_CD77(t *testing.T) {
	t.Parallel()
	if goArch(t) != "amd64" {
		t.Skip("race detector default is amd64 only")
	}
	root := repoRoot(t)
	out, err := runCmd(t, root, 2*time.Minute, append(raceEnv(false, "SC_IN_CONTAINER"), "SC_IN_CONTAINER=1"), "make", "-n", "test")
	if err != nil {
		t.Fatalf("make -n test: %v\n%s", err, out)
	}
	if !strings.Contains(out, "go test") || !strings.Contains(out, "-race") {
		t.Fatalf("expected -race by default on amd64:\n%s", out)
	}
}

func TestMakeVerifyReexec_EnvEmptyNotForwarded_CD77(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	out, err := runCmd(t, root, 2*time.Minute, raceEnv(true, "SC_IN_CONTAINER"), "make", "-n", "verify")
	if err != nil {
		t.Fatalf("make -n verify: %v\n%s", err, out)
	}
	if got, want := strings.TrimSpace(out), "./bootstrap exec --image ci -- make verify"; got != want {
		t.Fatalf("re-exec line = %q, want %q", got, want)
	}
}
