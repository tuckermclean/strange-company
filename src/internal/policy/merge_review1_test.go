package policy

import (
	"strings"
	"testing"
)

// nativize converts every list of strings in v to []string, the way a Go caller builds a document.
func nativize(v any) any {
	switch x := v.(type) {
	case map[string]any:
		o := map[string]any{}
		for k, e := range x {
			o[k] = nativize(e)
		}
		return o
	case []any:
		strs := make([]string, 0, len(x))
		for _, e := range x {
			s, ok := e.(string)
			if !ok {
				l := make([]any, len(x))
				for i, e := range x {
					l[i] = nativize(e)
				}
				return l
			}
			strs = append(strs, s)
		}
		return strs
	}
	return v
}

func minimalRepo(spec map[string]any) map[string]any {
	return map[string]any{
		"apiVersion": "strange.company/v1alpha1",
		"kind":       "RepositoryPolicy",
		"metadata":   map[string]any{"name": "fixture"},
		"spec":       spec,
	}
}

func mustReject(t *testing.T, eff Effective, is []Issue, want string) {
	t.Helper()
	got := issueKeys(is)
	found := false
	for _, k := range got {
		if k == want {
			found = true
		}
	}
	if !found {
		t.Errorf("issues %v, want %s", got, want)
	}
	if eff.Values != nil {
		t.Errorf("rejected repository policy produced Values: %v", eff.Values)
	}
}

// 22-AC-2 / 26-AC-4 (review B1): Go-native typed input is judged exactly like the YAML-parsed equivalent.
func TestMergePolicy_GoNativeLoosening_22AC2_26AC4(t *testing.T) {
	cases := []struct {
		name string
		spec map[string]any
		want string
	}{
		{"testPaths", map[string]any{"testPaths": []string{".github/**"}}, "POLICY_LOOSENING:/spec/testPaths/0"},
		{"exemption", map[string]any{
			"testPaths":    []string{"tests/**"},
			"verification": map[string]any{"allowedExemptionClasses": []string{"chaos"}},
		}, "POLICY_LOOSENING:/spec/verification/allowedExemptionClasses/0"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			eff, is := MergePolicy(opts(t, false), basePlatform(t), minimalRepo(c.spec))
			mustReject(t, eff, is, c.want)
		})
	}
}

// 22-AC-2 (review B1): tightening fixtures converted to Go-native lists give the same Effective values.
func TestMergePolicy_GoNativeTighteningSameEffective_22AC2(t *testing.T) {
	for _, row := range []string{"union", "allow-sets", "maxima"} {
		t.Run(row, func(t *testing.T) {
			yamlRepo := mergeDoc(t, td("merge", row, "tighten.yaml"))
			want, is := MergePolicy(opts(t, false), basePlatform(t), yamlRepo)
			if len(is) != 0 {
				t.Fatalf("yaml rejected: %v", is)
			}
			native := nativize(yamlRepo).(map[string]any)
			got, is := MergePolicy(opts(t, false), basePlatform(t), native)
			if len(is) != 0 {
				t.Fatalf("go-native rejected: %v", is)
			}
			if len(want.Values) == 0 || canon(got.Values) != canon(want.Values) {
				t.Errorf("go-native Effective\n  got  %s\n  want %s", canon(got.Values), canon(want.Values))
			}
		})
	}
}

// 22-AC-2 (review B1): a nil or non-map repository policy is REPO_POLICY_INVALID.
func TestMergePolicy_NilOrNonMapRepo_22AC2(t *testing.T) {
	for name, repo := range map[string]map[string]any{
		"nil":          nil,
		"spec-nonmap":  {"apiVersion": "strange.company/v1alpha1", "kind": "RepositoryPolicy", "metadata": map[string]any{"name": "x"}, "spec": "oops"},
		"spec-list":    {"apiVersion": "strange.company/v1alpha1", "kind": "RepositoryPolicy", "metadata": map[string]any{"name": "x"}, "spec": []string{"a"}},
		"empty-object": {},
	} {
		t.Run(name, func(t *testing.T) {
			eff, is := MergePolicy(opts(t, false), basePlatform(t), repo)
			if !hasCode(is, CodeRepoPolicyInvalid) {
				t.Errorf("want REPO_POLICY_INVALID, got %v", issueKeys(is))
			}
			if eff.Values != nil {
				t.Errorf("Values = %v", eff.Values)
			}
		})
	}
}

// 22-AC-2 (review N1): a platform lacking a schema-required bounded value fails closed; the pointer is the
// repository field's pointer.
func TestMergePolicy_PlatformMissingRequiredValueFailsClosed_22AC2(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(Platform)
		spec   map[string]any
		want   string
	}{
		{"maxAutonomousAttempts", func(p Platform) {
			delete(p.Verification["spec"].(map[string]any)["retry"].(map[string]any), "maxAutonomousAttempts")
		}, map[string]any{"retry": map[string]any{"maxAutonomousAttempts": 20}}, "PLATFORM_POLICY_INVALID:/spec/retry/maxAutonomousAttempts"},
		{"humanApprovalsRequired", func(p Platform) {
			delete(p.Verification["spec"].(map[string]any)["merge"].(map[string]any), "humanApprovalsRequired")
		}, map[string]any{"merge": map[string]any{"humanApprovalsRequired": 0}}, "PLATFORM_POLICY_INVALID:/spec/merge/humanApprovalsRequired"},
		{"maxBaseLag.commits", func(p Platform) {
			delete(p.Verification["spec"].(map[string]any)["task"].(map[string]any)["maxBaseLag"].(map[string]any), "commits")
		}, map[string]any{"task": map[string]any{"maxBaseLag": map[string]any{"commits": 200}}}, "PLATFORM_POLICY_INVALID:/spec/task/maxBaseLag/commits"},
		{"maxBaseLag.age", func(p Platform) {
			delete(p.Verification["spec"].(map[string]any)["task"].(map[string]any)["maxBaseLag"].(map[string]any), "age")
		}, map[string]any{"task": map[string]any{"maxBaseLag": map[string]any{"age": "9999h"}}}, "PLATFORM_POLICY_INVALID:/spec/task/maxBaseLag/age"},
		{"role-absent", func(p Platform) { delete(p.Roles, "tester") },
			map[string]any{"diffLimits": map[string]any{"tester": map[string]any{"maxFilesChanged": 2000}}},
			"PLATFORM_POLICY_INVALID:/spec/diffLimits/tester"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := clonePlatform(t, basePlatform(t))
			c.mutate(p)
			c.spec["testPaths"] = []any{"tests/**"}
			eff, is := MergePolicy(opts(t, false), p, minimalRepo(c.spec))
			mustReject(t, eff, is, c.want)
			if strings.Contains(strings.Join(issueKeys(is), " "), "POLICY_LOOSENING") {
				t.Errorf("missing platform value reported as loosening: %v", issueKeys(is))
			}
		})
	}
}

// 22-AC-2 (review N4): a repository staticChecks pattern that is not RE2 is POLICY_REGEX_INVALID with its pointer.
func TestMergePolicy_StaticCheckPatternMustCompile_22AC2(t *testing.T) {
	for name, pat := range map[string]string{"unclosed": "(unclosed", "lookahead": "(?=x)"} {
		t.Run(name, func(t *testing.T) {
			repo := minimalRepo(map[string]any{
				"testPaths": []any{"tests/**"},
				"staticChecks": map[string]any{
					"skipMarkers": map[string]any{"go": []any{`\bok\(`, pat}},
				},
			})
			eff, is := MergePolicy(opts(t, false), basePlatform(t), repo)
			mustReject(t, eff, is, "POLICY_REGEX_INVALID:/spec/staticChecks/skipMarkers/go/1")
		})
	}
}
