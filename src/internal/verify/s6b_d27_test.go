package verify

import "testing"

// 22-AC-3 / D27 (a): outside an open top-level fence, a line with leading space or tab whose trimmed rest starts
// with a fence opener or `<` fails the report closed (an indented HTML opener on a list-item continuation line
// hides the headings after it). Each report is "# R", a plain "## AC-2" heading, then the case: AC-1 is
// "addressed" only by a heading that CommonMark renders inside an HTML block.
func TestClassifyJudging_IndentedHTMLOpenerFailsClosed_D27_22AC3(t *testing.T) {
	for name, x := range map[string]string{
		"tab_div_after_blank":        "- x\n\n\t<div>\n  ## AC-1\n",
		"four_space_div_after_blank": "- x\n\n    <div>\n  ## AC-1\n",
		"four_space_div_no_blank":    "- x\n    <div>\n  ## AC-1\n",
		"tab_comment_star_item":      "* x\n\n\t<!--\n   ## AC-1\n",
		"tab_space_comment_closed":   "- x\n\t <!--\n  ## AC-1\n-->\n",
		"one_space_div":              "- x\n\n <div>\n  ## AC-1\n",
		"two_space_div":              "- x\n\n  <div>\n  ## AC-1\n",
		"ordered_list_pre":           "1. x\n\n   <pre>\n   ## AC-1\n   </pre>\n",
		"ordered_list_five_space":    "1. x\n\n     <div>\n   ## AC-1\n",
		"blockquote_list_div":        "> - x\n>\n>     <div>\n>   ## AC-1\n",
		"blockquote_list_comment":    "> - x\n>\n>\t<!--\n>   ## AC-1\n",
	} {
		t.Run(name, func(t *testing.T) {
			in := d23Research(t, "# R\n\n## AC-2\n\n"+x, d23TwoACs)
			got, err := ClassifyJudging(in)
			if err != nil || got.Outcome != "judging_fail" {
				t.Fatalf("got %s %v, %v, want judging_fail (AC-1 hidden in an HTML block)", got.Outcome, got.Reasons, err)
			}
			if len(got.Reasons) == 0 {
				t.Fatal("no reason")
			}
		})
	}
}

// 22-AC-3 / D27 (b): the `<` rules do not apply to lines inside an open top-level fence. Fenced XML/HTML and Go
// `<-` lines, in a column-0 fence beside proper headings for every AC, stay judging_pass.
func TestClassifyJudging_FencedAngleLinesAreNotHTML_D27_22AC3(t *testing.T) {
	for name, x := range map[string]string{
		"fenced_xml_settings":      "```xml\n<settings>\n  <cache/>\n```\n",
		"fenced_go_receive":        "```go\n  <-done\n```\n",
		"fenced_html_comment":      "```\n<!--\n```\n",
		"tilde_fenced_html":        "~~~html\n<div>\n<pre>\n~~~\n",
		"fenced_indented_angle":    "```\n\t<div>\n    <b>x</b>\n```\n",
		"fenced_xml_then_headings": "```xml\n<settings>\n</settings>\n```\n\n## AC-1\n",
	} {
		t.Run("twin/"+name, func(t *testing.T) {
			in := d23Research(t, "# R\n\n## AC-2\n\n## AC-1\n\n"+x, d23TwoACs)
			got, err := ClassifyJudging(in)
			s6CheckJudging(t, in, got, err, s6Expect{Function: "judging", Outcome: "judging_pass", Reasons: &[]string{}})
		})
	}
}
