package policy

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// harnessRegistryCases are the seven fixtures of 28-AC-1. Each is a single-entry registry with exactly one
// fault and must be rejected with exactly its own error code (7 fixtures, 7 distinct codes).
var harnessRegistryCases = []struct{ dir, code string }{
	{"tag-image", "HARNESS_IMAGE_NOT_DIGEST_PINNED"},
	{"reserved-hermes", "HARNESS_NAME_RESERVED"},
	{"credential-field", "HARNESS_PROVIDER_CREDENTIAL_FIELD"},
	{"unknown-scenario", "HARNESS_FAKE_SCENARIO_UNKNOWN"},
	{"repo-config-not-ignored", "HARNESS_REPO_CONFIG_NOT_IGNORED"},
	{"worker-timeout-production", "HARNESS_WORKER_TIMEOUT_NOT_TEST_ONLY"},
	{"telemetry-prompts", "HARNESS_TELEMETRY_CONTENT_CAPTURE"},
}

// 28-AC-1: each of the 7 harness registry fixtures is rejected with its own distinct error code. The schema-level
// fixtures may also carry the generic POLICY_SCHEMA_INVALID (F04 26-AC-1 reports it for the same documents); no
// other code, in particular no other HARNESS_* code, is allowed. The two invariant-level fixtures are
// schema-valid and must yield exactly their own code.
func TestHarnessRegistry_FixturesRejectedWithDistinctCodes_28AC1(t *testing.T) {
	invariantLevel := map[string]bool{"repo-config-not-ignored": true, "worker-timeout-production": true}
	distinct := map[string]bool{}
	for _, c := range harnessRegistryCases {
		distinct[c.code] = true
		t.Run(c.dir, func(t *testing.T) {
			_, issues := Check(td("harness-registry", c.dir), opts(t, false))
			got := map[string]bool{}
			for _, i := range issues {
				got[i.Code] = true
			}
			if !got[c.code] {
				t.Errorf("issue codes = %v, want to contain %s; issues:\n%s", keys(got), c.code, joinIssues(issues))
			}
			for code := range got {
				if code == c.code || (code == "POLICY_SCHEMA_INVALID" && !invariantLevel[c.dir]) {
					continue
				}
				t.Errorf("unexpected code %s beside %s; issues:\n%s", code, c.code, joinIssues(issues))
			}
		})
	}
	if len(harnessRegistryCases) != 7 || len(distinct) != 7 {
		t.Fatalf("want 7 fixtures with 7 distinct codes, have %d fixtures and %d codes", len(harnessRegistryCases), len(distinct))
	}
	dirs, err := filepath.Glob(td("harness-registry", "*"))
	if err != nil || len(dirs) != 7 {
		t.Errorf("testdata/harness-registry has %d fixture directories, want 7 (err %v)", len(dirs), err)
	}
}

// 28-AC-1: the committed policy/harnesses.yaml exists and the committed policy tree has no issues with it.
func TestHarnessRegistry_CommittedRegistryAccepted_28AC1(t *testing.T) {
	dir := filepath.Join(repoRoot(t), "policy")
	if _, err := os.Stat(filepath.Join(dir, "harnesses.yaml")); err != nil {
		t.Fatalf("policy/harnesses.yaml must be committed: %v", err)
	}
	_, issues := Check(dir, opts(t, false))
	if len(issues) != 0 {
		t.Errorf("committed policy has issues:\n%s", joinIssues(issues))
	}
}

// 28-AC-2: the committed registry has one enabled fake-<scenario> entry per value of the fake.scenario enum of
// harness.schema.yaml, all sharing one harness-fake image digest and stage test-only; claude is the disabled
// production claude-code entry (decision D6, plan 28 section 18).
func TestHarnessRegistry_FakeEntriesMatchScenarioEnum_28AC2(t *testing.T) {
	root := repoRoot(t)
	schema := dig(t, readYAML(t, filepath.Join(root, "contracts", "harness.schema.yaml")),
		"$defs", "Harness", "properties", "fake", "properties", "scenario", "enum")
	enum := map[string]bool{}
	for _, v := range schema.([]any) {
		enum[v.(string)] = true
	}
	if len(enum) == 0 {
		t.Fatal("fake.scenario enum is empty")
	}
	reg, ok := dig(t, readYAML(t, filepath.Join(root, "policy", "harnesses.yaml")), "spec", "harnesses").(map[string]any)
	if !ok {
		t.Fatal("policy/harnesses.yaml has no spec.harnesses")
	}
	have := map[string]bool{}
	images := map[string]bool{}
	for name, h := range reg {
		hm := h.(map[string]any)
		if hm["adapter"] != "fake" {
			continue
		}
		if hm["enabled"] != true {
			t.Errorf("%s: fake entry is not enabled", name)
		}
		if hm["stage"] != "test-only" {
			t.Errorf("%s: stage = %v, want test-only", name, hm["stage"])
		}
		img, _ := hm["image"].(string)
		images[img] = true
		sc, _ := dig(t, h, "fake", "scenario").(string)
		if name != "fake-"+sc {
			t.Errorf("entry %q runs scenario %q, want name fake-%s", name, sc, sc)
		}
		have[sc] = true
	}
	var missing, extra []string
	for s := range enum {
		if !have[s] {
			missing = append(missing, s)
		}
	}
	for s := range have {
		if !enum[s] {
			extra = append(extra, s)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	if len(missing) != 0 || len(extra) != 0 {
		t.Errorf("registry fake scenarios differ from the enum: missing %v, extra %v", missing, extra)
	}
	if len(images) != 1 {
		t.Errorf("fake entries use %d distinct images %v, want exactly 1 harness-fake image", len(images), keys(images))
	}
	for img := range images {
		if !strings.Contains(img, "/harness-fake@sha256:") {
			t.Errorf("fake image %q is not a digest-pinned harness-fake reference", img)
		}
	}

	claude, ok := reg["claude"].(map[string]any)
	if !ok {
		t.Fatal("registry has no claude entry")
	}
	if claude["enabled"] != false || claude["stage"] != "production" || claude["adapter"] != "claude-code" {
		t.Errorf("claude = enabled %v stage %v adapter %v, want false/production/claude-code",
			claude["enabled"], claude["stage"], claude["adapter"])
	}
}

func dig(t testing.TB, v any, path ...string) any {
	t.Helper()
	for _, p := range path {
		m, ok := v.(map[string]any)
		if !ok {
			t.Fatalf("no map at %q", p)
		}
		v = m[p]
	}
	return v
}
