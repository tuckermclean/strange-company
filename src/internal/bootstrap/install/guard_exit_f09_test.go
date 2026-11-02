package install

import (
	"errors"
	"testing"

	"strange.company/sc/internal/bootstrap/engine"
	"strange.company/sc/internal/provider"
)

// 03-AC-5 (plan 03 section 14.4, plan 50 section 7.2): exit 4 means the plan was judged and refused. An internal
// failure of the guard machinery (the embedded guard does not parse, the digest cannot be computed) judged nothing; it
// is a failed phase (exit 1), not a refusal. The test is in package install because wrapProviderErr is unexported.
func TestWrapProviderErr_InternalGuardErrorsAreNotExit4_03AC5(t *testing.T) {
	for _, code := range []string{"GUARD_INVALID", "GUARD_DIGEST_FAILED"} {
		t.Run(code, func(t *testing.T) {
			err := wrapProviderErr(provider.NewError(code, "internal", false))
			var ee *engine.Error
			if !errors.As(err, &ee) {
				t.Fatalf("wrapProviderErr = %T %v, want *engine.Error", err, err)
			}
			if ee.Code != code {
				t.Errorf("code = %q, want %q kept", ee.Code, code)
			}
			if ee.Exit != 0 && ee.Exit != 1 {
				t.Errorf("exit = %d, want 1 (a failed phase, not a guard refusal)", ee.Exit)
			}
		})
	}
}

// 03-AC-5: the guard's verdict codes stay exit 4.
func TestWrapProviderErr_GuardVerdictsStayExit4_03AC5(t *testing.T) {
	for _, code := range []string{"GUARD_UNEXPECTED_RESOURCE", "GUARD_ACTION_FORBIDDEN", "GUARD_REPLACE_FORBIDDEN", "GUARD_COUNT_EXCEEDED", "GUARD_PRODUCT_MISMATCH", "GUARD_PROVIDER_FORBIDDEN"} {
		var ee *engine.Error
		if !errors.As(wrapProviderErr(provider.NewError(code, "refused", false)), &ee) || ee.Exit != 4 {
			t.Errorf("%s: want *engine.Error with exit 4, got %+v", code, ee)
		}
	}
}
