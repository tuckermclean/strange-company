package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// 29-AC-3 (S2 GREEN): `sc-statemachine check` (make state-machine-check) judges the table against the real
// guard and effect registries: a table declaring a guard the library does not implement exits 1 with
// SM_GUARD_UNKNOWN, one declaring an unimplemented effect with SM_EFFECT_UNKNOWN.
func TestCheck_RealRegistriesRejectUnimplementedIdentifiers_29AC3(t *testing.T) {
	for _, tc := range []struct{ old, repl, code string }{
		{"    G_ACTOR_AUTHORIZED:", "    G_UNREGISTERED_GUARD: Declared in the table but implemented nowhere in the library.\n    G_ACTOR_AUTHORIZED:", "SM_GUARD_UNKNOWN"},
		{"    E_FREEZE_BASE_COMMIT:", "    E_UNREGISTERED_EFFECT: Declared in the table but implemented nowhere in the library.\n    E_FREEZE_BASE_COMMIT:", "SM_EFFECT_UNKNOWN"},
	} {
		p := writeTable(t, tc.old, tc.repl)
		code, out, errs := runCmd("check", "--contracts", filepath.Join(root(t), "contracts"), "--table", p)
		if code != 1 {
			t.Errorf("%s: exit %d, want 1; stdout %q stderr %q", tc.code, code, out, errs)
			continue
		}
		if !strings.HasPrefix(out, tc.code) || !strings.Contains(out, p) {
			t.Errorf("%s: stdout %q does not start with the code and name %s", tc.code, out, p)
		}
	}
}
