package verify

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// Slice S6, decision D24 (review 2 of the S6 GREEN), 22-AC-3. X1: a YAML alias bomb hangs ClassifyJudge, so the judge
// artifact parser refuses any anchor or alias. X2: the report recogniser looks through container markers (blockquote,
// list items) for fences and HTML. Inline-hidden ids in a heading do not count. The characterization base
// observation's BaseSha must equal Observed's. Every failing case has a passing twin.

// d24Bomb is review 2's alias bomb: l0 is a ten-item list, every next level is ten aliases of the previous one.
func d24Bomb(levels int) string {
	var b strings.Builder
	b.WriteString("x:\n  l0: &l0 [\"x\",\"x\",\"x\",\"x\",\"x\",\"x\",\"x\",\"x\",\"x\",\"x\"]\n")
	for i := 1; i <= levels; i++ {
		refs := make([]string, 10)
		for j := range refs {
			refs[j] = fmt.Sprintf("*l%d", i-1)
		}
		fmt.Fprintf(&b, "  l%d: &l%d [%s]\n", i, i, strings.Join(refs, ","))
	}
	return b.String() + s6DocJudgeYAML
}

// d24Classify runs ClassifyJudge in a goroutine and fails (an assertion, not a panic) when it has not returned in
// 2 seconds. A timed-out goroutine leaks until it finishes; the levels used are low enough (5 and 6 levels are 10^5
// and 10^6 expanded items, only expanded by an unbounded parser) for it to finish soon and free its memory, so the
// test binary is never killed by memory pressure at RED.
func d24Classify(t *testing.T, in JudgeInput) (RedClassification, bool, error) {
	t.Helper()
	type res struct {
		got RedClassification
		err error
	}
	ch := make(chan res, 1)
	go func() {
		g, e := ClassifyJudge(in)
		ch <- res{g, e}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	select {
	case r := <-ch:
		return r.got, true, r.err
	case <-ctx.Done():
		return RedClassification{}, false, nil
	}
}

// 22-AC-3 / D24 (X1): alias bombs, an anchor and an alias are judge_invalid, quickly; the same document without
// anchors or aliases is judge_valid.
func TestClassifyJudge_AliasBombAndAnchors_D24_22AC3(t *testing.T) {
	rep := strings.Replace
	bad := map[string]string{
		"bomb_5_levels":   d24Bomb(5),
		"bomb_6_levels":   d24Bomb(6),
		"single_anchor":   rep(s6DocJudgeYAML, "acIds: [AC-1]", "acIds: &a [AC-1]", 1),
		"single_alias":    rep(rep(s6DocJudgeYAML, "acIds: [AC-1]", "acIds: &a [AC-1]", 1), "acIds: [AC-2]", "acIds: *a", 1),
		"alias_no_anchor": rep(s6DocJudgeYAML, "acIds: [AC-2]", "acIds: *a", 1),
	}
	for name, y := range bad {
		t.Run("invalid/"+name, func(t *testing.T) {
			fx := s6BaseFixture(t)
			fx.JudgeYAML = y
			in, _ := s6ValidateInput(t, fx)
			got, done, err := d24Classify(t, in)
			if !done {
				t.Fatalf("ClassifyJudge did not return within 5s (unbounded alias expansion)")
			}
			if err != nil || got.Outcome != "judge_invalid" || len(got.Reasons) == 0 {
				t.Fatalf("got %s %v, %v, want judge_invalid with a reason", got.Outcome, got.Reasons, err)
			}
		})
	}
	good := map[string]string{
		"control": s6DocJudgeYAML,
	}
	for name, y := range good {
		t.Run("valid/"+name, func(t *testing.T) {
			fx := s6BaseFixture(t)
			fx.JudgeYAML = y
			in, _ := s6ValidateInput(t, fx)
			got, done, err := d24Classify(t, in)
			if !done {
				t.Fatalf("ClassifyJudge did not return within 5s")
			}
			if err != nil || got.Outcome != "judge_valid" {
				t.Fatalf("got %s %v, %v, want judge_valid", got.Outcome, got.Reasons, err)
			}
		})
	}
}

// 22-AC-3 / D24 (X2): a fence or HTML block opening behind container markers hides the heading from a renderer;
// the recogniser fails closed. AC-2 is a plain top-level heading in each, so only the container rule can fail it.
func TestClassifyJudging_ReportContainers_FailsClosed_D24_22AC3(t *testing.T) {
	cases := map[string]string{
		"list_fence_dash":                 "# R\n\n## AC-2\n\n- ```\n  ## AC-1\n  ```\n",
		"list_fence_ordered_3sp":          "# R\n\n## AC-2\n\n1. ```\n   ## AC-1\n   ```\n",
		"list_fence_tilde":                "# R\n\n## AC-2\n\n* ~~~\n  ## AC-1\n  ~~~\n",
		"list_fence_unclosed":             "# R\n\n## AC-2\n\n- ```\n  ## AC-1\n",
		"list_html_comment":               "# R\n\n## AC-2\n\n- <!--\n  ## AC-1\n  -->\n",
		"list_html_div":                   "# R\n\n## AC-2\n\n+ <div>\n  ## AC-1\n",
		"blockquote_fence":                "# R\n\n## AC-2\n\n> ```\n> ## AC-1\n> ```\n",
		"nested_list_html":                "# R\n\n## AC-2\n\n- - <div>\n    ## AC-1\n",
		"nested_list_fence":               "# R\n\n## AC-2\n\n- a\n  - ```\n    ## AC-1\n    ```\n",
		"blockquote_then_list_fence":      "# R\n\n## AC-2\n\n> - ```\n>   ## AC-1\n",
		"list_paren_ordered_fence":        "# R\n\n## AC-2\n\n1) ```\n   ## AC-1\n",
		"list_item_tab_after_marker_html": "# R\n\n## AC-2\n\n-\t<pre>\n  ## AC-1\n",
		"visible_blockquote_fence":        "## AC-1\n\n## AC-2\n\n> ```\n> x\n",
		"visible_nested_list_html":        "## AC-1\n\n## AC-2\n\n- - <div>\n",
		"visible_nested_list_fence":       "## AC-1\n\n## AC-2\n\n- a\n  - ```\n    x\n",
		"visible_blockquote_list_fence":   "## AC-1\n\n## AC-2\n\n> - ~~~\n",
		"visible_ids_with_container_html": "## AC-1\n\n## AC-2\n\n- <div>\n",
	}
	for name, report := range cases {
		t.Run(name, func(t *testing.T) {
			in := d23Research(t, report, d23TwoACs)
			got, err := ClassifyJudging(in)
			s6CheckJudging(t, in, got, err, s6Expect{Function: "judging", Outcome: "judging_fail", NonEmpty: true})
		})
	}
}

// 22-AC-3 / D24 (X2) twins: top-level headings and list items that only hold text are judging_pass.
func TestClassifyJudging_ReportContainers_Accepts_D24_22AC3(t *testing.T) {
	cases := map[string]string{
		"top_level_atx_headings":        "# R\n\n## AC-1\nx\n\n## AC-2\ny\n",
		"text_list_beside_headings":     "# R\n\n## AC-1\n\n- one\n- two\n\n## AC-2\n\n1. first\n2. second\n",
		"text_blockquote_beside":        "## AC-1\n\n> quoted text\n\n## AC-2\n",
		"nested_text_list":              "## AC-1\n\n- a\n  - b\n\n## AC-2\n",
		"list_item_with_inline_tag":     "## AC-1\n\n- see <b>this</b> now\n\n## AC-2\n",
		"list_item_with_inline_code":    "## AC-1\n\n- use `go test`\n\n## AC-2\n",
		"list_item_starting_with_digit": "## AC-1\n\n- 3 items < 5\n\n## AC-2\n",
	}
	for name, report := range cases {
		t.Run(name, func(t *testing.T) {
			in := d23Research(t, report, d23TwoACs)
			got, err := ClassifyJudging(in)
			s6CheckJudging(t, in, got, err, s6Expect{Function: "judging", Outcome: "judging_pass", Reasons: &[]string{}})
		})
	}
}

// 22-AC-3 / D24 (inline hidden): an AC id that CommonMark does not render as heading text (inside an HTML comment, a
// tag, a link destination or an image) leaves the AC unaddressed: judging_fail. AC-2 is a plain heading.
func TestClassifyJudging_InlineHiddenId_D24_22AC3(t *testing.T) {
	for name, report := range map[string]string{
		"html_comment":   "# R\n\n## AC-2\n\n## <!-- AC-1 -->\n",
		"tag_name":       "# R\n\n## AC-2\n\n## <AC-1>\n",
		"link_dest":      "# R\n\n## AC-2\n\n## [](AC-1)\n",
		"image_dest":     "# R\n\n## AC-2\n\n## ![](AC-1)\n",
		"link_dest_text": "# R\n\n## AC-2\n\n## [x](AC-1)\n",
	} {
		t.Run(name, func(t *testing.T) {
			in := d23Research(t, report, d23TwoACs)
			got, err := ClassifyJudging(in)
			if err != nil || got.Outcome != "judging_fail" {
				t.Fatalf("got %s %v, %v, want judging_fail (AC-1 unaddressed)", got.Outcome, got.Reasons, err)
			}
			if len(got.Reasons) == 0 {
				t.Fatal("no reason")
			}
		})
	}
	// Twins: the id stays visible next to inline markup.
	for name, report := range map[string]string{
		"bold_after_id":  "# R\n\n## AC-1 <b>x</b>\n\n## AC-2\n",
		"link_after_id":  "# R\n\n## AC-1 [link](x)\n\n## AC-2\n",
		"plain_headings": "# R\n\n## AC-1\n\n## AC-2\n",
	} {
		t.Run("twin/"+name, func(t *testing.T) {
			in := d23Research(t, report, d23TwoACs)
			got, err := ClassifyJudging(in)
			s6CheckJudging(t, in, got, err, s6Expect{Function: "judging", Outcome: "judging_pass", Reasons: &[]string{}})
		})
	}
}

// 22-AC-3 / D24 (base sha): the characterization base observation must be for the same base revision as the judge
// observation, else an infra error; equal values are judge_valid.
func TestClassifyJudge_CharacterizationBaseShaBound_D24_22AC3(t *testing.T) {
	load := func(t *testing.T) JudgeInput {
		fx, _ := s6Load(t, "characterization", "valid_passes_at_judge_commit_and_base")
		in, _ := s6ValidateInput(t, fx)
		if in.BaseObserved == nil {
			t.Fatal("the characterization fixture has no base observation")
		}
		return in
	}
	t.Run("twin_equal", func(t *testing.T) {
		in := load(t)
		if in.BaseObserved.BaseSha != in.Observed.BaseSha {
			t.Fatalf("fixture premise: base shas differ")
		}
		got, err := ClassifyJudge(in)
		if err != nil || got.Outcome != "judge_valid" {
			t.Fatalf("got %s %v, %v, want judge_valid", got.Outcome, got.Reasons, err)
		}
	})
	for name, sha := range map[string]string{
		"differs_hex": "0000000000000000000000000000000000000001",
		"empty":       "",
	} {
		t.Run(name, func(t *testing.T) {
			in := load(t)
			in.BaseObserved.BaseSha = sha
			got, err := ClassifyJudge(in)
			s6WantErr(t, err, got, s6Expect{Error: "verification_infra_failed"})
		})
	}
}
