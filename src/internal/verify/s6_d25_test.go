package verify

import "testing"

// 22-AC-3 / D25: in a top-level ATX heading an AC id counts only in the heading text before the first `<`, `![`,
// `](` or `][`. Ids hidden in tag attributes, link destinations or titles, reference labels or image alt text leave
// the AC unaddressed (judging_fail). Each report is "# R", a plain "## AC-2" heading, then the line under test.
func TestClassifyJudging_InlineHiddenId_Attributes_D25_22AC3(t *testing.T) {
	for name, line := range map[string]string{
		"attr_double_quote":    "## <span title=\"a>AC-1\">x</span>\n",
		"attr_single_quote":    "## <span title='a>AC-1'>x</span>\n",
		"dest_angle_paren":     "## [x](<a)AC-1>)\n",
		"dest_escaped_paren":   "## [x](a\\)AC-1)\n",
		"dest_balanced_paren":  "## [x](a(b)AC-1)\n",
		"title_with_paren":     "## [x](u \"a)AC-1\")\n",
		"ref_link_defined":     "## [x][AC-1]\n\n[AC-1]: /u\n",
		"image_alt":            "## ![AC-1](u)\n",
		"html_hidden_attr_tag": "## <span hidden>AC-1</span>\n",
	} {
		t.Run(name, func(t *testing.T) {
			in := d23Research(t, "# R\n\n## AC-2\n\n"+line, d23TwoACs)
			got, err := ClassifyJudging(in)
			if err != nil || got.Outcome != "judging_fail" {
				t.Fatalf("got %s %v, %v, want judging_fail (AC-1 unaddressed)", got.Outcome, got.Reasons, err)
			}
			if len(got.Reasons) == 0 {
				t.Fatal("no reason")
			}
		})
	}
}

// 22-AC-3 / D25 twins: the id is visible text before any markup that could hide it, so it stays addressed.
func TestClassifyJudging_InlineHiddenId_Twins_D25_22AC3(t *testing.T) {
	for name, line := range map[string]string{
		"bold_after_id":    "## AC-1 <b>x</b>\n",
		"link_after_id":    "## AC-1 [link](x)\n",
		"lt_after_id":      "## AC-1 a < b\n",
		"nested_brackets":  "## [[AC-1]](u)\n",
		"plain_heading_ac": "## AC-1\n",
	} {
		t.Run("twin/"+name, func(t *testing.T) {
			in := d23Research(t, "# R\n\n## AC-2\n\n"+line, d23TwoACs)
			got, err := ClassifyJudging(in)
			s6CheckJudging(t, in, got, err, s6Expect{Function: "judging", Outcome: "judging_pass", Reasons: &[]string{}})
		})
	}
}
