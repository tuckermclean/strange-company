package guard_test

import (
	"testing"

	"strange.company/sc/internal/provider/guard"
)

// F09 slice S8 attempt 2, 03-AC-12 (plan 03 section 20.1 and section 8): the recover exception of rule 6 covers only
// entries of the old region that stay as they are (no-op or read). A fenced address that the plan creates or updates
// is not excused, and a retired (fenced) address is never created again, even in the configured region: the guard sees
// the fenced addresses, so it refuses that name reuse as GUARD_ACTION_FORBIDDEN (the provider chooses the new names;
// the guard is the backstop).

// 03-AC-12: create or update of a fenced address in an unconfigured region is GUARD_REGION_MISMATCH, not excused.
func TestGuardRecover_FencedCreateOrUpdateInOldRegionIsNotExcused_03AC12(t *testing.T) {
	g := rcLoad(t)
	for _, act := range []string{"create", "update"} {
		plan := rcPlan(rcRes{"node-01", "fake-region-a", []string{act}})
		v := g.EvaluateRecover(plan, rcExpect(1), guard.RecoverMode{Fenced: []string{rcAddr("node-01")}})
		if v.Pass || v.Code != guard.CodeRegionMismatch {
			t.Errorf("03-AC-12: %s of fenced node-01 in an unconfigured region: verdict %+v, want %s", act, v, guard.CodeRegionMismatch)
		}
	}
}

// 03-AC-12: a read entry of a fenced address in the old region is still excused (the exception's legitimate scope).
func TestGuardRecover_FencedReadInOldRegionIsAccepted_03AC12(t *testing.T) {
	g := rcLoad(t)
	for _, act := range []string{"no-op", "read"} {
		plan := rcPlan(rcRes{"node-01", "fake-region-a", []string{act}})
		if v := g.EvaluateRecover(plan, rcExpect(1), guard.RecoverMode{Fenced: []string{rcAddr("node-01")}}); !v.Pass {
			t.Errorf("03-AC-12: %s of fenced node-01 in the old region: verdict %+v, want pass", act, v)
		}
	}
}

// 03-AC-12: creating or updating a fenced (retired) name in the configured region reuses a retired name (section 8).
func TestGuardRecover_FencedNameReuseInConfiguredRegionFails_03AC12(t *testing.T) {
	g := rcLoad(t)
	for _, act := range []string{"create", "update"} {
		plan := rcPlan(rcRes{"node-01", "fake-region-b", []string{act}})
		v := g.EvaluateRecover(plan, rcExpect(1), guard.RecoverMode{Fenced: []string{rcAddr("node-01")}})
		if v.Pass || v.Code != guard.CodeActionForbidden {
			t.Errorf("03-AC-12: %s of fenced name node-01 in the configured region: verdict %+v, want %s", act, v, guard.CodeActionForbidden)
		}
	}
}
