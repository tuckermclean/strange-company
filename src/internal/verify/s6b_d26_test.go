package verify

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// 22-AC-3 / D26 (a): when no fence is open, a fence opener (``` or ~~~) with any leading space or tab fails the
// report closed. A column-0 opener is top-level and its closer may be indented up to 3 spaces. Each report is
// "# R", a plain "## AC-2" heading, then the case: AC-1 is "addressed" only by a heading CommonMark renders as
// code inside a list item.
func TestClassifyJudging_IndentedFenceOpenerFailsClosed_D26_22AC3(t *testing.T) {
	for name, x := range map[string]string{
		"list_fence_outdented_closer": "- x\n\n  ```\n  y\n```\n## AC-1\n```\n",
		"empty_item_fence":            "-\n  ```\n  y\n```\n## AC-1\n```\n",
		"ordered_3sp_fence":           "1. x\n\n   ```\n   y\n ```\n## AC-1\n```\n",
		"list_tilde_fence":            "* x\n  ~~~\n~~~\n## AC-1\n~~~\n",
		"four_space_opener_in_item":   "- x\n    ```\n  ## AC-1\n",
		"tab_opener_in_item":          "- x\n\t```\n  ## AC-1\n",
	} {
		t.Run(name, func(t *testing.T) {
			in := d23Research(t, "# R\n\n## AC-2\n\n"+x, d23TwoACs)
			got, err := ClassifyJudging(in)
			if err != nil || got.Outcome != "judging_fail" {
				t.Fatalf("got %s %v, %v, want judging_fail (AC-1 hidden in a list-item fence)", got.Outcome, got.Reasons, err)
			}
			if len(got.Reasons) == 0 {
				t.Fatal("no reason")
			}
		})
	}
}

// 22-AC-3 / D26 twins: a column-0 fence with a closer indented up to 3 spaces and headings after it (as D23
// fence_closer_indented_3), and a list item holding only text beside proper headings, stay judging_pass.
func TestClassifyJudging_IndentedFenceOpener_Twins_D26_22AC3(t *testing.T) {
	for name, x := range map[string]string{
		"column0_fence_closer_indented_3": "```\nx\n   ```\n## AC-1\n",
		"column0_tilde_closer_indented_2": "~~~\nx\n  ~~~\n## AC-1\n",
		"list_item_text_only":             "- x\n\n## AC-1\n",
		"list_items_beside_headings":      "- one\n- two\n\n## AC-1\n\n1. first\n",
	} {
		t.Run("twin/"+name, func(t *testing.T) {
			in := d23Research(t, "# R\n\n## AC-2\n\n"+x, d23TwoACs)
			got, err := ClassifyJudging(in)
			s6CheckJudging(t, in, got, err, s6Expect{Function: "judging", Outcome: "judging_pass", Reasons: &[]string{}})
		})
	}
}

type reportOracleEntry struct {
	Report  string          `json:"report"`
	ACs     []string        `json:"acs"`
	Visible map[string]bool `json:"visible"`
}

// 22-AC-3 / D26 (b): structural guard. testdata/report_oracle/corpus.json records, for generated reports, a
// CommonMark oracle's verdict (markdown-it-py 3.0.0) on whether each AC id is visible text of a rendered
// heading. When the oracle says any AC is not visible the recogniser must not call the report addressed
// (judging_fail); when every AC is visible, either outcome is allowed (fail-closed is permitted).
func TestReportRecogniser_NeverAddressesWhatCommonMarkHides_D26_22AC3(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("testdata", "report_oracle", "corpus.json"))
	if err != nil {
		t.Fatal(err)
	}
	var corpus []reportOracleEntry
	if err := json.Unmarshal(b, &corpus); err != nil {
		t.Fatal(err)
	}
	if len(corpus) < 2000 {
		t.Fatalf("corpus has %d entries, want at least 2000", len(corpus))
	}
	var hidden, failOpen, visiblePass, visibleFail int
	for i, e := range corpus {
		crit := make([]Criterion, 0, len(e.ACs))
		allVisible := true
		for _, id := range e.ACs {
			crit = append(crit, Criterion{ID: id, Kind: "change"})
			if !e.Visible[id] {
				allVisible = false
			}
		}
		in := d23Research(t, e.Report, crit)
		got, err := ClassifyJudging(in)
		if err != nil {
			t.Fatalf("entry %d %q: %v", i, e.Report, err)
		}
		if got.Outcome != "judging_pass" && got.Outcome != "judging_fail" {
			t.Fatalf("entry %d %q: outcome %s", i, e.Report, got.Outcome)
		}
		if allVisible {
			if got.Outcome == "judging_pass" {
				visiblePass++
			} else {
				visibleFail++
			}
			continue
		}
		hidden++
		if got.Outcome != "judging_fail" {
			failOpen++
			t.Errorf("entry %d: oracle says %v visible but the recogniser passed the report: %q", i, e.Visible, e.Report)
		}
	}
	t.Logf("corpus %d: oracle-hidden %d (fail-open %d); all-visible %d (pass %d, fail-closed %d, rate %.3f)",
		len(corpus), hidden, failOpen, len(corpus)-hidden, visiblePass, visibleFail,
		float64(visibleFail)/float64(max(1, len(corpus)-hidden)))
}
