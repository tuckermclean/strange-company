package statemachine

import (
	"path/filepath"
	"strings"
	"testing"
)

// D8 (S1 review 3 follow-up, folded into S2): a phase event's from must be a subset of {its phase state}
// (red_classified -> RED_VERIFYING, green_classified -> GREEN_VERIFYING, judge_classified -> JUDGE_VALIDATING,
// judging_classified -> JUDGING) and execution_failed's from a subset of the activeExecution required states.
// Each fixture gives the event full outcome (or failure class) coverage from a second, wrong state, so no
// other rule trips first; Load accepts every one of them before the D8 rule exists.
var d8Fixtures = map[string]string{
	"phase_event_wrong_state_red":              "SM_OUTCOME_GUARD_INVALID",
	"phase_event_wrong_state_green":            "SM_OUTCOME_GUARD_INVALID",
	"phase_event_wrong_state_judge":            "SM_OUTCOME_GUARD_INVALID",
	"phase_event_wrong_state_judging":          "SM_OUTCOME_GUARD_INVALID",
	"execution_failed_from_optional_state":     "SM_OUTCOME_GUARD_INVALID",
	"execution_failed_from_verification_state": "SM_OUTCOME_GUARD_INVALID",
}

// 29-AC-2: the D8 fixtures exist with their codes (TestInvalidFixtures_29AC2 proves Load emits each one).
func TestInvalidFixtures_D8Coverage_29AC2(t *testing.T) {
	byName := map[string]string{}
	for _, p := range fixtureFiles(t) {
		byName[strings.TrimSuffix(filepath.Base(p), ".yaml")] = readFixture(t, p).Code
	}
	for name, code := range d8Fixtures {
		if got, ok := byName[name]; !ok || got != code {
			t.Errorf("fixture %s: code %q (present=%v), want %s", name, got, ok, code)
		}
	}
}

// 29-AC-2 (D8): the real contract satisfies the rule and each D8 fixture makes Load fail with the code. This
// repeats TestInvalidFixtures_29AC2 for the D8 names so a D8 regression is reported under its own test name.
func TestLoad_RejectsMisplacedPhaseEvents_D8(t *testing.T) {
	if _, err := loadBase(t); err != nil {
		t.Fatalf("the contract itself must load: %v", err)
	}
	base := baseBytes(t)
	g, e := registries(t, parseMap(t, base))
	for name, code := range d8Fixtures {
		m, err := Load(mutate(t, base, readFixture(t, filepath.Join("testdata", "invalid", name+".yaml"))), g, e, opts(t))
		if err == nil {
			t.Errorf("%s: Load accepted the table (machine %v), want %s", name, m != nil, code)
			continue
		}
		if le := codeOf(t, err); le.Code != code {
			t.Errorf("%s: code %s (%s), want %s", name, le.Code, le.Message, code)
		}
	}
}
