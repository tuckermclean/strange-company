package trajectory_test

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"strange.company/sc/internal/statemachine/trajectory"
)

// 21-AC-20: the trajectory loader behind `make test-tdd-library`. It must reject a missing or empty directory and
// every malformed file with the code the fixture names (a `# expect: CODE` first line). Fixtures are in
// testdata/; the files of test/e2e/tdd are the real trajectories. No clock, no global random source (D7, D8).

func read(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func wantCode(t *testing.T, what string, err error, code string) {
	t.Helper()
	var te *trajectory.Error
	if !errors.As(err, &te) {
		t.Errorf("%s: error = %v, want a *trajectory.Error with code %s", what, err, code)
		return
	}
	if te.Code != code {
		t.Errorf("%s: code %s (%v), want %s", what, te.Code, err, code)
	}
	if !strings.Contains(err.Error(), code) {
		t.Errorf("%s: the message %q must start with or contain the code", what, err.Error())
	}
}

// 21-AC-20: a valid trajectory parses into its fields, including the optional live-only ones.
func TestParse_ValidTrajectory_21AC20(t *testing.T) {
	p := filepath.Join("testdata", "valid", "ok-trajectory.yaml")
	tr, err := trajectory.Parse(p, read(t, p))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if tr.Name != "ok-trajectory" || tr.Mode != "test-first" || len(tr.Events) != 1 || tr.Events[0].Event != "task_created" ||
		tr.Events[0].Actor != "hermes" || tr.Events[0].At != "2026-10-13T12:00:00Z" || tr.Events[0].Facts["G_REPOSITORY_MANAGED"] != true ||
		len(tr.ExpectedStates) != 1 || tr.ExpectedStates[0] != "SPECIFIED" || tr.ExpectedTransitions[0] != "T001" || tr.ExpectedReasons[0] != "" ||
		tr.ExpectedAttempts["autonomous_used"] != 0 || tr.Live == nil || tr.Events[0].Live == nil {
		t.Errorf("Parse = %+v", tr)
	}
}

// 21-AC-20: the mode defaults to test-first.
func TestParse_ModeDefaultsToTestFirst_21AC20(t *testing.T) {
	src := "trajectory: m\nevents:\n  - {event: task_created, actor: hermes, at: \"2026-10-13T12:00:00Z\"}\nexpected_states: [SPECIFIED]\nexpected_transitions: [T001]\nexpected_reasons: [\"\"]\n"
	tr, err := trajectory.Parse("m.yaml", []byte(src))
	if err != nil || tr.Mode != "test-first" {
		t.Errorf("Parse = mode %q, err %v; want test-first", tr.Mode, err)
	}
}

// 21-AC-20: every malformed fixture is rejected with its expected code.
func TestParse_RejectsMalformedFiles_21AC20(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join("testdata", "malformed", "*.yaml"))
	if len(files) < 10 {
		t.Fatalf("%d malformed fixtures, want at least 10", len(files))
	}
	for _, p := range files {
		b := read(t, p)
		m := regexp.MustCompile(`^# expect: (TRAJ_[A-Z_]+)`).FindSubmatch(b)
		if m == nil {
			t.Fatalf("%s: no `# expect: CODE` first line", p)
		}
		tr, err := trajectory.Parse(p, b)
		wantCode(t, filepath.Base(p), err, string(m[1]))
		if err != nil && (tr.Name != "" || len(tr.Events) != 0) {
			t.Errorf("%s: a rejected file must come with the zero Trajectory, got %+v", p, tr)
		}
	}
}

// 21-AC-20: Load fails on a directory without trajectory files and on a directory with a malformed file, and
// reads the valid files in file-name order.
func TestLoad_EmptyAndMalformedDirectories_21AC20(t *testing.T) {
	if all, err := trajectory.Load(filepath.Join("testdata", "empty")); err == nil {
		t.Errorf("Load of a directory without trajectories = %d files, nil; want TRAJ_EMPTY", len(all))
	} else {
		wantCode(t, "empty directory", err, "TRAJ_EMPTY")
	}
	if _, err := trajectory.Load(filepath.Join("testdata", "does-not-exist")); err == nil {
		t.Error("Load of a missing directory succeeded")
	}
	dir := t.TempDir()
	good := read(t, filepath.Join("testdata", "valid", "ok-trajectory.yaml"))
	if err := os.WriteFile(filepath.Join(dir, "ok-trajectory.yaml"), good, 0o644); err != nil {
		t.Fatal(err)
	}
	all, err := trajectory.Load(dir)
	if err != nil || len(all) != 1 || all[0].Name != "ok-trajectory" {
		t.Fatalf("Load of one valid file = %d files, %v", len(all), err)
	}
	bad := read(t, filepath.Join("testdata", "malformed", "length-mismatch.yaml"))
	if err := os.WriteFile(filepath.Join(dir, "length-mismatch.yaml"), bad, 0o644); err != nil {
		t.Fatal(err)
	}
	if all, err := trajectory.Load(dir); err == nil {
		t.Errorf("Load with a malformed file among valid ones = %d files, nil; want an error", len(all))
	} else {
		wantCode(t, "directory with a malformed file", err, "TRAJ_LENGTH_MISMATCH")
	}
}

// 21-AC-20 (D17 N6): Load rejects a *.yml file in the directory (it would be silently skipped, so a renamed
// trajectory would drop out of the suite), also when valid *.yaml files are present.
func TestLoad_RejectsYmlFiles_21AC20(t *testing.T) {
	dir := t.TempDir()
	good := read(t, filepath.Join("testdata", "valid", "ok-trajectory.yaml"))
	if err := os.WriteFile(filepath.Join(dir, "ok-trajectory.yaml"), good, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "other.yml"), good, 0o644); err != nil {
		t.Fatal(err)
	}
	all, err := trajectory.Load(dir)
	if err == nil {
		t.Fatalf("Load of a directory with a .yml file = %d files, nil; want TRAJ_BAD_VALUE", len(all))
	}
	wantCode(t, "directory with a .yml file", err, "TRAJ_BAD_VALUE")
	if !strings.Contains(err.Error(), "other.yml") {
		t.Errorf("the error %q must name the file other.yml", err.Error())
	}
}

// 21-AC-20 (D17 N6): the real trajectory directory holds no *.yml file.
func TestLoad_RealDirectoryHasNoYml_21AC20(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join("..", "..", "..", "..", "test", "e2e", "tdd", "*.yml"))
	if len(files) != 0 {
		t.Errorf("test/e2e/tdd holds %v, rename to .yaml", files)
	}
}

// 21-AC-20 (D17): a multi-document file is TRAJ_YAML and an unknown policy resolutionApproval is TRAJ_BAD_VALUE.
// The fixtures live apart from testdata/malformed so the older table test is unchanged.
func TestParse_RejectsMultiDocumentAndBadPolicy_21AC20_D17(t *testing.T) {
	want := map[string]string{"multi-document.yaml": "TRAJ_YAML", "bad-policy-resolution.yaml": "TRAJ_BAD_VALUE"}
	for name, code := range want {
		p := filepath.Join("testdata", "malformed-d17", name)
		tr, err := trajectory.Parse(p, read(t, p))
		wantCode(t, name, err, code)
		if err != nil && (tr.Name != "" || len(tr.Events) != 0) {
			t.Errorf("%s: a rejected file must come with the zero Trajectory, got %+v", name, tr)
		}
	}
}
