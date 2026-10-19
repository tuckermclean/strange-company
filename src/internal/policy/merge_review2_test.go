package policy

import (
	"math"
	"testing"
)

func mustPlatformInvalid(t *testing.T, eff Effective, is []Issue) {
	t.Helper()
	if len(is) == 0 {
		t.Fatalf("merge accepted the input, want PLATFORM_POLICY_INVALID; Values %v", eff.Values)
	}
	found := false
	for _, i := range is {
		if i.Code == "PLATFORM_POLICY_INVALID" && i.Pointer == "" {
			found = true
		}
	}
	if !found {
		t.Errorf("issues %v, want PLATFORM_POLICY_INVALID with pointer \"\"", issueKeys(is))
	}
	if eff.Values != nil {
		t.Errorf("rejected merge produced Values: %v", eff.Values)
	}
}

// 22-AC-2 / 26-AC-4 (review-2 N1'): an empty platform fails closed instead of bounding nothing.
func TestMergePolicy_EmptyPlatformFailsClosed_22AC2_26AC4(t *testing.T) {
	cases := map[string]map[string]any{
		"testPaths":      {"testPaths": []any{".github/**"}},
		"watchdogAlerts": {"testPaths": []any{"tests/**"}, "watchdog": map[string]any{"groups": map[string]any{"approval": map[string]any{"alertAfter": "9999h"}}}},
	}
	for name, spec := range cases {
		t.Run(name, func(t *testing.T) {
			eff, is := MergePolicy(opts(t, false), Platform{}, minimalRepo(spec))
			mustPlatformInvalid(t, eff, is)
		})
	}
}

// 22-AC-2 / 26-AC-4 (review-2 N1'): a platform document that does not survive the JSON round trip fails closed.
func TestMergePolicy_UnmarshalablePlatformFailsClosed_22AC2_26AC4(t *testing.T) {
	spec := func() map[string]any { return map[string]any{"testPaths": []any{".github/**"}} }
	t.Run("verification", func(t *testing.T) {
		p := clonePlatform(t, basePlatform(t))
		p.Verification = nativize(p.Verification).(map[string]any)
		p.Verification["spec"].(map[string]any)["nan"] = math.NaN()
		eff, is := MergePolicy(opts(t, false), p, minimalRepo(spec()))
		mustPlatformInvalid(t, eff, is)
	})
	t.Run("modelRouting", func(t *testing.T) {
		p := clonePlatform(t, basePlatform(t))
		p.ModelRouting = map[string]any{"spec": map[string]any{"nan": math.NaN()}}
		eff, is := MergePolicy(opts(t, false), p, minimalRepo(spec()))
		mustPlatformInvalid(t, eff, is)
	})
	t.Run("role", func(t *testing.T) {
		p := clonePlatform(t, basePlatform(t))
		p.Roles["tester"] = map[string]any{"spec": map[string]any{"nan": math.NaN()}}
		eff, is := MergePolicy(opts(t, false), p, minimalRepo(spec()))
		mustPlatformInvalid(t, eff, is)
	})
}

// Guard: a valid Go-native platform still judges .github/** testPaths as loosening.
func TestMergePolicy_GoNativePlatformStillJudges_22AC2_26AC4(t *testing.T) {
	p := clonePlatform(t, basePlatform(t))
	p.Verification = nativize(p.Verification).(map[string]any)
	eff, is := MergePolicy(opts(t, false), p, minimalRepo(map[string]any{"testPaths": []any{".github/**"}}))
	mustReject(t, eff, is, "POLICY_LOOSENING:/spec/testPaths/0")
}
