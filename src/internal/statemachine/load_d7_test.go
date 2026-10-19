package statemachine

import (
	"path/filepath"
	"strings"
	"testing"
)

// Round 3 (review of 16a51e8, decision D7): the D7 fixtures must keep existing under these codes so the
// implementation cannot drop or recode them. TestInvalidFixtures_29AC2 proves Load emits each code.
// Note for GREEN: creation_missing_in_mode (D7d) also leaves the verification-first states unreachable, so the
// creation rule (SM_CREATION_INVALID, mode verification-first) must be checked before the graph rules.
var d7Fixtures = map[string]string{
	"outcome_guard_missing_red":                "SM_OUTCOME_GUARD_INVALID",
	"failure_class_guard_missing":              "SM_OUTCOME_GUARD_INVALID",
	"outcome_guard_outside_phase":              "SM_OUTCOME_GUARD_INVALID",
	"failure_class_guard_outside_event":        "SM_OUTCOME_GUARD_INVALID",
	"verification_state_without_phase":         "SM_OUTCOME_INCOMPLETE",
	"creation_missing_in_mode":                 "SM_CREATION_INVALID",
	"execution_state_without_failure_coverage": "SM_FAILURE_CLASS_INCOMPLETE",
}

// 29-AC-2: the D7 fixtures exist with their codes.
func TestInvalidFixtures_D7Coverage_29AC2(t *testing.T) {
	byName := map[string]string{}
	for _, p := range fixtureFiles(t) {
		byName[strings.TrimSuffix(filepath.Base(p), ".yaml")] = readFixture(t, p).Code
	}
	for name, code := range d7Fixtures {
		if got, ok := byName[name]; !ok || got != code {
			t.Errorf("fixture %s: code %q (present=%v), want %s", name, got, ok, code)
		}
	}
	if fx := readFixture(t, filepath.Join("testdata", "invalid", "creation_missing_in_mode.yaml")); fx.Mode != "verification-first" {
		t.Errorf("creation_missing_in_mode: mode %q, want verification-first", fx.Mode)
	}
}
