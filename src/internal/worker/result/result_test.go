package result_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"strange.company/sc/internal/contracts"
	"strange.company/sc/internal/worker"
	"strange.company/sc/internal/worker/result"
)

const truncMarker = "[truncated by sc-worker]"

func jsonRound(t testing.TB, v any) map[string]any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func validateResult(t testing.TB, v any) error {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	set, err := contracts.CompileSchemas(filepath.Join(root, "contracts"))
	if err != nil {
		t.Fatal(err)
	}
	return set["worker-result"].Validate(v)
}

// researcherResult is a schema-valid completed researcher result (worker-result.schema.yaml examples[1] with a
// research_report role output) carrying report as report_markdown.
func researcherResult(t testing.TB, report string) map[string]any {
	t.Helper()
	doc, err := contracts.ParseYAML(worker.EmbeddedSchema("worker-result"))
	if err != nil {
		t.Fatal(err)
	}
	r := jsonRound(t, doc.(map[string]any)["examples"].([]any)[1])
	r["role"] = "researcher"
	r["execution_id"] = "sc-184-x2"
	r["role_output"] = map[string]any{"kind": "research_report", "report_markdown": report}
	return r
}

func bigReport(n int) string {
	line := "Finding: the redirect handler copies the request but drops GetBody on 307 (see client.go).\n"
	return strings.Repeat(line, n/len(line)+1)[:n]
}

// 27-AC-13: a claim with a 400 KiB report_markdown yields a result that fits max_result_bytes, validates against
// worker-result.schema.yaml and ends the text with the truncation marker.
func TestFit_OversizedReportIsTruncated_27AC13(t *testing.T) {
	const report = 400 * 1024
	for _, limit := range []int{262144, 100000, 20000} {
		t.Run(fmt.Sprintf("max_result_bytes_%d", limit), func(t *testing.T) {
			doc := researcherResult(t, bigReport(report))
			if err := validateResult(t, jsonRound(t, researcherResult(t, "ok"))); err != nil {
				t.Fatalf("fixture is not schema-valid: %v", err)
			}
			body, err := result.Fit(doc, limit)
			if err != nil {
				t.Fatalf("Fit: %v", err)
			}
			if len(body) == 0 || len(body) > limit {
				t.Fatalf("Fit returned %d bytes, want 1..%d", len(body), limit)
			}
			if bytes.ContainsRune(body, '\n') {
				t.Errorf("canonical JSON must be one line")
			}
			var got map[string]any
			if err := json.Unmarshal(body, &got); err != nil {
				t.Fatalf("result is not JSON: %v", err)
			}
			if err := validateResult(t, got); err != nil {
				t.Errorf("fitted result is not schema-valid: %v", err)
			}
			ro, _ := got["role_output"].(map[string]any)
			md, _ := ro["report_markdown"].(string)
			if !strings.HasSuffix(md, truncMarker) {
				t.Errorf("report_markdown does not end with %q (len %d)", truncMarker, len(md))
			}
			if !strings.HasPrefix(md, "Finding: the redirect handler") {
				t.Errorf("report_markdown lost its leading text")
			}
			if got["state"] != "completed" || got["execution_id"] != "sc-184-x2" || ro["kind"] != "research_report" {
				t.Errorf("identity or state changed: state %v execution %v kind %v", got["state"], got["execution_id"], ro["kind"])
			}
		})
	}
}

// 27-AC-13: a result that already fits is returned without truncation.
func TestFit_ResultThatFitsIsNotTruncated_27AC13(t *testing.T) {
	doc := researcherResult(t, "A short report.\n")
	body, err := result.Fit(doc, 262144)
	if err != nil {
		t.Fatalf("Fit: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("result is not JSON: %v (%q)", err, body)
	}
	ro, _ := got["role_output"].(map[string]any)
	if md, _ := ro["report_markdown"].(string); md != "A short report.\n" {
		t.Errorf("report_markdown = %q, want it unchanged", md)
	}
}
