package policy

import (
	"fmt"
	"strings"
	"testing"
)

// 26-AC-1 diagnostics (review 4 item 3): above maxExperimentCandidates the candidates of a propertyNames
// violation are not tried one by one. The 200-sibling model-routing document yields exactly one issue, at the
// deepest common prefix of the candidates (`/spec/providers`), whose message names the candidates; the output
// is identical on every run. This pins the bound by shape, not by wall clock (D8).
func TestCheck_ManyPropertyNamesViolationsFallbackShape_26AC1(t *testing.T) {
	doc := readYAML(t, td("valid", "model-routing.yaml")).(map[string]any)
	providers := doc["spec"].(map[string]any)["providers"].(map[string]any)
	proto := providers["anthropic"]
	for i := 0; i < 200; i++ {
		p := deepCopy(t, proto).(map[string]any)
		rp := p["apiFamilies"].(map[string]any)["anthropic-messages"].(map[string]any)["requestPolicy"].(map[string]any)
		rp["pinnedParameters"] = map[string]any{"BAD": "x"}
		providers[fmt.Sprintf("anthropic-%03d", i)] = p
	}
	dir := t.TempDir()
	writeYAML(t, dir, "model-routing.yaml", doc)

	render := func() (string, []Issue) {
		_, issues := Check(dir, opts(t, false))
		var b strings.Builder
		for _, i := range issues {
			fmt.Fprintf(&b, "%s|%s|%s|%s\n", i.Code, i.Pointer, i.File, i.Message)
		}
		return b.String(), issues
	}
	first, issues := render()
	var bad []Issue
	for _, i := range issues {
		if i.Code == "POLICY_SCHEMA_INVALID" && strings.Contains(i.Message, "BAD") {
			bad = append(bad, i)
		}
	}
	if len(bad) != 1 {
		t.Fatalf("issues naming BAD = %d, want exactly 1:\n%s", len(bad), first)
	}
	if bad[0].Pointer != "/spec/providers" {
		t.Errorf("pointer = %q, want /spec/providers", bad[0].Pointer)
	}
	for _, want := range []string{"candidates:", "/spec/providers/anthropic-000/", "/spec/providers/anthropic-199/"} {
		if !strings.Contains(bad[0].Message, want) {
			t.Errorf("message lacks %q: %.300s", want, bad[0].Message)
		}
	}
	if second, _ := render(); second != first {
		t.Errorf("output differs between two runs:\n%s\n---\n%s", first, second)
	}
}
