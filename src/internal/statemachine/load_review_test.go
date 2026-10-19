package statemachine

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Round 2 (review of 01fbb50, decision D6): fail-closed loader rules the first fixtures did not pin down.
// The 29-AC-2 fixtures themselves are run by TestInvalidFixtures_29AC2; this file pins the names and codes of
// the D6 fixtures so none can be dropped or recoded, and the Check output format (D6h).

// d6Fixtures maps each D6 fixture name to the code it must expect (29-AC-2).
var d6Fixtures = map[string]string{
	"outcome_no_transitions_red":             "SM_OUTCOME_INCOMPLETE",
	"outcome_no_transitions_green":           "SM_OUTCOME_INCOMPLETE",
	"outcome_no_transitions_judge":           "SM_OUTCOME_INCOMPLETE",
	"outcome_no_transitions_judging":         "SM_OUTCOME_INCOMPLETE",
	"outcome_actor_class_incomplete_red":     "SM_OUTCOME_INCOMPLETE",
	"outcome_actor_class_incomplete_judging": "SM_OUTCOME_INCOMPLETE",
	"failure_class_actor_class_incomplete":   "SM_FAILURE_CLASS_INCOMPLETE",
	"outcome_guard_duplicate":                "SM_OUTCOME_GUARD_INVALID",
	"outcome_guard_empty_item":               "SM_OUTCOME_GUARD_INVALID",
	"outcome_guard_unknown_outcome":          "SM_OUTCOME_GUARD_INVALID",
	"outcome_guard_wrong_phase":              "SM_OUTCOME_GUARD_INVALID",
	"failure_class_guard_duplicate":          "SM_OUTCOME_GUARD_INVALID",
	"failure_class_guard_unknown_class":      "SM_OUTCOME_GUARD_INVALID",
	"failure_class_guard_empty_item":         "SM_OUTCOME_GUARD_INVALID",
	"stall_not_in_every_mode":                "SM_STALL_NO_TRANSITION",
	"stall_transition_not_runner":            "SM_STALL_NO_TRANSITION",
	"creation_to_return":                     "SM_CREATION_INVALID",
	"creation_to_same":                       "SM_CREATION_INVALID",
	"creation_to_resume":                     "SM_CREATION_INVALID",
	"creation_to_other_state":                "SM_CREATION_INVALID",
	"creation_from_mixed":                    "SM_CREATION_INVALID",
	"resume_from_non_held_hermes":            "SM_RESUME_FROM_NON_HELD",
	"resume_from_non_held_mixed":             "SM_RESUME_FROM_NON_HELD",
	"mode_leak_target_missing_in_mode":       "SM_MODE_MISMATCH",
	"mode_leak_from_in_no_mode":              "SM_MODE_MISMATCH",
	"return_from_non_execution_only":         "SM_RETURN_FROM_NON_EXECUTION",
	"terminal_list_undeclared":               "SM_STATE_UNDECLARED",
	"implicit_effect_undeclared":             "SM_EFFECT_UNDECLARED",
	"initial_state_terminal":                 "SM_INITIAL_STATE_INVALID",
	"state_name_duplicate":                   "SM_SCHEMA_INVALID",
}

// 29-AC-2: the D6 fixtures exist with their codes (the generic fixture test then proves Load emits them).
func TestInvalidFixtures_D6Coverage_29AC2(t *testing.T) {
	byName := map[string]string{}
	for _, p := range fixtureFiles(t) {
		byName[strings.TrimSuffix(filepath.Base(p), ".yaml")] = readFixture(t, p).Code
	}
	for name, code := range d6Fixtures {
		if got, ok := byName[name]; !ok || got != code {
			t.Errorf("fixture %s: code %q (present=%v), want %s", name, got, ok, code)
		}
	}
}

var checkLine = regexp.MustCompile(`^([A-Z][A-Z0-9_]+)(?::(/\S*))? (\S+): (mode (\S+): )?(.+)$`)

// 29-AC-1 (D6h): a failing Check renders `CODE[:ptr] <file>: [mode m: ]msg`, the file before the mode.
func TestCheck_FailureFormatFileBeforeMode_29AC1(t *testing.T) {
	base := baseBytes(t)
	for _, name := range []string{"non_terminating_in_one_mode", "unreachable_in_one_mode"} {
		fx := readFixture(t, filepath.Join("testdata", "invalid", name+".yaml"))
		p := filepath.Join(t.TempDir(), "state-machine.yaml")
		if err := os.WriteFile(p, mutate(t, base, fx), 0o644); err != nil {
			t.Fatal(err)
		}
		err := Check(&bytes.Buffer{}, p, opts(t))
		if err == nil {
			t.Fatalf("%s: Check accepted the table", name)
		}
		m := checkLine.FindStringSubmatch(err.Error())
		if m == nil || m[1] != fx.Code || m[3] != p || m[5] != fx.Mode {
			t.Errorf("%s: %q does not match `%s[:ptr] %s: mode %s: msg`", name, err.Error(), fx.Code, p, fx.Mode)
		}
	}
	p := filepath.Join(t.TempDir(), "state-machine.yaml")
	bad := bytes.Replace(base, []byte("initialState: SPECIFIED"), []byte("initialState: GHOST"), 1)
	if err := os.WriteFile(p, bad, 0o644); err != nil {
		t.Fatal(err)
	}
	err := Check(&bytes.Buffer{}, p, opts(t))
	if err == nil {
		t.Fatal("Check accepted an undeclared initialState")
	}
	if m := checkLine.FindStringSubmatch(err.Error()); m == nil || m[1] != CodeInitialStateInvalid || m[3] != p || m[5] != "" {
		t.Errorf("%q does not match `%s:ptr %s: msg`", err.Error(), CodeInitialStateInvalid, p)
	}
}
