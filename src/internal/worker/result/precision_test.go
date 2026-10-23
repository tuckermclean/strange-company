package result_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"strange.company/sc/internal/worker/result"
)

// S2-FU1: Fit copies the document without losing integer precision; a posted result must carry
// harness_report.num_turns and every other integer exactly (9007199254740993 is 2^53 + 1).
func TestFit_KeepsLargeIntegersExact_S2FU1(t *testing.T) {
	const big = "9007199254740993"
	var viaDecoder map[string]any
	dec := json.NewDecoder(strings.NewReader(`{"harness_report":{"num_turns":` + big + `},"summary":"s"}`))
	dec.UseNumber()
	if err := dec.Decode(&viaDecoder); err != nil {
		t.Fatal(err)
	}
	docs := map[string]map[string]any{
		"int64":       {"harness_report": map[string]any{"num_turns": int64(9007199254740993)}, "summary": "s"},
		"json.Number": viaDecoder,
	}
	for name, doc := range docs {
		body, err := result.Fit(doc, 4096)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !bytes.Contains(body, []byte(`"num_turns":`+big)) {
			t.Errorf("%s: Fit lost integer precision: %s", name, body)
		}
	}
}
