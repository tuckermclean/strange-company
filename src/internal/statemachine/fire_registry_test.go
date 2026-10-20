package statemachine

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// 29-AC-3 (plan 29 section 7.1, "Unknown guard/effect identifier -> error"): the real guard and effect
// registries cover exactly the identifiers of the table's catalogues - no guard of the table is unimplemented
// and the library implements no guard the table does not declare.
func TestRegistries_CoverExactlyTheCatalogues_29AC3(t *testing.T) {
	mdl := evContract(t)
	if got := (Guards{}).GuardIDs(); !slices.Equal(got, mdl.guards) {
		t.Errorf("Guards.GuardIDs() = %d ids, want the %d catalogue guards; missing %v, extra %v",
			len(got), len(mdl.guards), evDiff(mdl.guards, got), evDiff(got, mdl.guards))
	}
	if got := (Effects{}).EffectIDs(); !slices.Equal(got, mdl.effects) {
		t.Errorf("Effects.EffectIDs() = %d ids, want the %d catalogue effects; missing %v, extra %v",
			len(got), len(mdl.effects), evDiff(mdl.effects, got), evDiff(got, mdl.effects))
	}
	for _, g := range mdl.guards {
		if !(Guards{}).HasGuard(g) {
			t.Errorf("HasGuard(%s) = false", g)
		}
	}
	for _, e := range mdl.effects {
		if !(Effects{}).HasEffect(e) {
			t.Errorf("HasEffect(%s) = false", e)
		}
	}
	for _, id := range []string{"", "G_UNREGISTERED_GUARD", "g_actor_authorized", "G_ACTOR_AUTHORIZED:x", " G_ACTOR_AUTHORIZED", "E_RESUME"} {
		if (Guards{}).HasGuard(id) {
			t.Errorf("HasGuard(%q) = true", id)
		}
	}
	for _, id := range []string{"", "E_UNREGISTERED_EFFECT", "e_resume", "E_RESUME:x", "G_ACTOR_AUTHORIZED"} {
		if (Effects{}).HasEffect(id) {
			t.Errorf("HasEffect(%q) = true", id)
		}
	}
}

func evDiff(a, b []string) []string {
	var out []string
	for _, x := range a {
		if !slices.Contains(b, x) {
			out = append(out, x)
		}
	}
	return out
}

// 29-AC-3: Load accepts the contract with the real registries, and still rejects an identifier the registries
// do not implement (29-AC-2 fixtures unknown_guard_id and unknown_effect_id).
func TestLoad_WithRealRegistries_29AC3(t *testing.T) {
	if _, err := Load(baseBytes(t), Guards{}, Effects{}, opts(t)); err != nil {
		t.Fatalf("Load(contract, Guards{}, Effects{}): %v", err)
	}
	base := baseBytes(t)
	for name, code := range map[string]string{"unknown_guard_id": CodeGuardUnknown, "unknown_effect_id": CodeEffectUnknown} {
		table := mutate(t, base, readFixture(t, filepath.Join("testdata", "invalid", name+".yaml")))
		_, err := Load(table, Guards{}, Effects{}, opts(t))
		if err == nil || codeOf(t, err).Code != code {
			t.Errorf("%s with the real registries: err = %v, want %s", name, err, code)
		}
	}
}

// 29-AC-3 (S2 GREEN): Check, the entry point of `make state-machine-check`, runs against the real registries,
// not against a registry built from the table's own catalogues: a table that declares a guard or an effect the
// library does not implement fails with SM_GUARD_UNKNOWN or SM_EFFECT_UNKNOWN.
func TestCheck_UsesRealRegistries_29AC3(t *testing.T) {
	base := baseBytes(t)
	for name, code := range map[string]string{"unknown_guard_id": CodeGuardUnknown, "unknown_effect_id": CodeEffectUnknown} {
		p := filepath.Join(t.TempDir(), "state-machine.yaml")
		if err := os.WriteFile(p, mutate(t, base, readFixture(t, filepath.Join("testdata", "invalid", name+".yaml"))), 0o644); err != nil {
			t.Fatal(err)
		}
		err := Check(&bytes.Buffer{}, p, opts(t))
		if err == nil {
			t.Errorf("%s: Check accepted a table declaring an identifier the library does not implement", name)
			continue
		}
		if le := codeOf(t, err); le.Code != code || le.File != p || !strings.HasPrefix(err.Error(), code) {
			t.Errorf("%s: Check error %q, want %s naming %s", name, err.Error(), code, p)
		}
	}
	var out bytes.Buffer
	if err := Check(&out, tablePath(t), opts(t)); err != nil {
		t.Fatalf("Check(contract) with the real registries: %v", err)
	}
}
