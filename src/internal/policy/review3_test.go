package policy

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"
)

func deepCopy(t testing.TB, v any) any {
	t.Helper()
	b, err := yaml.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out any
	if err := yaml.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func writeYAML(t testing.TB, dir, name string, v any) {
	t.Helper()
	b, err := yaml.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// 26-AC-1 diagnostics (review 3 item 1): the cost of locating propertyNames violations is bounded. A
// model-routing document with 200 providers, each with one offending pinnedParameters key, is checked within a
// few seconds (the unbounded experiment took over nine minutes) and is still rejected.
func TestCheck_ManyPropertyNamesViolationsAreBounded_26AC1(t *testing.T) {
	doc := readYAML(t, td("valid", "model-routing.yaml")).(map[string]any)
	spec := doc["spec"].(map[string]any)
	providers := spec["providers"].(map[string]any)
	proto := providers["anthropic"]
	for i := 0; i < 200; i++ {
		p := deepCopy(t, proto).(map[string]any)
		rp := p["apiFamilies"].(map[string]any)["anthropic-messages"].(map[string]any)["requestPolicy"].(map[string]any)
		rp["pinnedParameters"] = map[string]any{"BAD": "x"}
		providers[fmt.Sprintf("anthropic-%03d", i)] = p
	}
	dir := t.TempDir()
	writeYAML(t, dir, "model-routing.yaml", doc)

	type result struct{ issues []Issue }
	done := make(chan result, 1)
	start := time.Now()
	go func() {
		_, is := Check(dir, opts(t, false))
		done <- result{is}
	}()
	select {
	case r := <-done:
		if el := time.Since(start); el > 10*time.Second {
			t.Errorf("Check took %v, want < 10s", el)
		}
		n := 0
		for _, i := range r.issues {
			if i.Code == "POLICY_SCHEMA_INVALID" && strings.Contains(i.Pointer+i.Message, "BAD") {
				n++
			}
		}
		if n == 0 {
			t.Errorf("no POLICY_SCHEMA_INVALID issue about the offending key; %d issues", len(r.issues))
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Check did not finish within 10s on 200 propertyNames violations")
	}
}

// 26-AC-1 diagnostics (review 3 item 2): the same invalid key under spec.providers and under spec.models yields
// exactly two issues, each with the message of its own schema (the provider id pattern, the model id pattern).
func TestCheck_SameBadKeyUnderTwoPropertyNamesSchemas_26AC1(t *testing.T) {
	doc := readYAML(t, td("valid", "model-routing.yaml")).(map[string]any)
	spec := doc["spec"].(map[string]any)
	spec["providers"].(map[string]any)["Bad_Key"] = deepCopy(t, spec["providers"].(map[string]any)["deepseek"])
	spec["models"].(map[string]any)["Bad_Key"] = deepCopy(t, spec["models"].(map[string]any)["deepseek/deepseek-v4-flash"])
	dir := t.TempDir()
	writeYAML(t, dir, "model-routing.yaml", doc)

	_, issues := Check(dir, opts(t, false))
	if len(issues) != 2 {
		t.Fatalf("issues = %d, want exactly 2:\n%s", len(issues), joinIssues(issues))
	}
	const providerPattern, modelPattern = `^[a-z][a-z0-9-]{0,31}$`, `/[A-Za-z0-9._:-]{1,128}$`
	for _, i := range issues {
		if i.Code != "POLICY_SCHEMA_INVALID" {
			t.Errorf("unexpected code in %s", i)
		}
		switch i.Pointer {
		case "/spec/providers/Bad_Key":
			if !strings.Contains(i.Message, providerPattern) || strings.Contains(i.Message, modelPattern) {
				t.Errorf("provider key issue carries the wrong message: %s", i)
			}
		case "/spec/models/Bad_Key":
			if !strings.Contains(i.Message, modelPattern) || strings.Contains(i.Message, providerPattern) {
				t.Errorf("model key issue carries the wrong message: %s", i)
			}
		default:
			t.Errorf("unexpected pointer: %s", i)
		}
	}
}
