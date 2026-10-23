package verify

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"strange.company/sc/internal/contracts"
)

// Slice S6, decision D23 (review 1 of the S6 GREEN), 22-AC-3. The cases below specify what the GREEN of S6 got
// wrong: the JudgeArtifact check disagrees with the real $defs/JudgeArtifact schema (B1), the research-report
// heading recogniser is not a fail-closed canonical subset (B2), ClassifyJudging ignores the G2 integrity facts
// (N1), the characterization base observation is not bound to the judge observation (N4), and unusable manifests
// and criteria are classified instead of refused (N5). The 100x determinism loop of the Go-built homes is added at
// the end. Every failing case has a passing twin.
//
// N6 (run exit code against the judge checks) is deliberately NOT specified here: twelve existing judge fixtures
// carry a head run whose exit code contradicts their judge checks (see the handoff of this RED), and existing
// fixtures cannot be changed. The orchestrator has to decide the rule first.

// ---------------------------------------------------------------------------------------------------------------
// B1: the JudgeArtifact check agrees with the real schema.

// d23Schema compiles the real contracts/ directory plus one probe document that is nothing but a $ref to
// $defs/JudgeArtifact (the schema file itself is only read, in a copy).
func d23Schema(t *testing.T) interface{ Validate(any) error } {
	t.Helper()
	src := filepath.Join(repoRoot(t), "contracts")
	dir := filepath.Join(t.TempDir(), "contracts")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob(filepath.Join(src, "*.schema.yaml"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no contracts under %s: %v", src, err)
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, filepath.Base(f)), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	probe := "$schema: https://json-schema.org/draft/2020-12/schema\n" +
		"$id: https://strange.company/contracts/judgeprobe/v1alpha1\n" +
		"$ref: \"https://strange.company/contracts/verification/v1alpha1#/$defs/JudgeArtifact\"\n"
	if err := os.WriteFile(filepath.Join(dir, "judgeprobe.schema.yaml"), []byte(probe), 0o644); err != nil {
		t.Fatal(err)
	}
	all, err := contracts.CompileSchemas(dir)
	if err != nil {
		t.Fatal(err)
	}
	s := all["judgeprobe"]
	if s == nil {
		t.Fatal("no judgeprobe schema")
	}
	return s
}

// d23Str is a YAML double-quoted scalar (JSON strings are valid YAML double-quoted scalars).
func d23Str(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func d23StrList(items ...string) string {
	q := make([]string, len(items))
	for i, s := range items {
		q[i] = d23Str(s)
	}
	return "[" + strings.Join(q, ", ") + "]"
}

func d23Repeat(s string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = s
	}
	return out
}

// d23J2 replaces the body (requiredFacts) of check J-2 of the baseline judge by the given check lines (each already
// indented by six spaces, ending in a newline).
func d23J2(body string) string {
	rf := "      requiredFacts:\n        paths: [\"docs/guide.md\"]\n        mustMatch: [\"30 days\"]\n"
	return strings.Replace(s6DocJudgeYAML, rf, body, 1)
}

func d23Facts(paths, mustMatch string) string {
	return "      requiredFacts:\n        paths: " + paths + "\n        mustMatch: " + mustMatch + "\n"
}

func d23Cmd(v string) string   { return "      command: " + v + "\n" }
func d23Thr(v string) string   { return "      threshold: " + v + "\n" }
func d23Rub(v string) string   { return "      rubric: " + v + "\n" }
func d23Glob(g string) string  { return d23J2(d23Facts(d23StrList(g), `["x"]`)) }
func d23Match(m string) string { return d23J2(d23Facts(`["a"]`, d23StrList(m))) }
func d23Argv(items string, timeout string) string {
	return d23J2(d23Cmd("{argv: " + items + ", timeoutSeconds: " + timeout + "}"))
}

// d23Checks builds a judge with n checks J-1..J-n, each covering both frozen criteria.
func d23Checks(n int) (string, []string) {
	var b strings.Builder
	b.WriteString("apiVersion: strange.company/v1alpha1\nkind: Judge\nmetadata:\n  task: sc-9\n  exemptionClass: documentation-only\nspec:\n  kind: required-facts\n  checks:\n")
	ids := make([]string, n)
	for i := 1; i <= n; i++ {
		ids[i-1] = "J-" + itoa(i)
		b.WriteString("    - id: J-" + itoa(i) + "\n      acIds: [AC-1, AC-2]\n      requiredFacts:\n        paths: [\"docs/guide.md\"]\n        mustMatch: [\"x\"]\n")
	}
	return b.String(), ids
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

type d23Case struct {
	name string
	yaml string
	ids  []string // observed check ids (default J-1, J-2)
	// stricter: the schema accepts the document and the library may still reject it, for the stated reason.
	stricter string
}

func d23Cases() []d23Case {
	var cs []d23Case
	add := func(name, y string) { cs = append(cs, d23Case{name: name, yaml: y}) }
	strict := func(name, y, why string) { cs = append(cs, d23Case{name: name, yaml: y, stricter: why}) }
	rep := strings.Replace

	add("control", s6DocJudgeYAML)
	add("control_crlf", strings.ReplaceAll(s6DocJudgeYAML, "\n", "\r\n"))
	add("control_comments", "# a judge\n"+rep(s6DocJudgeYAML, "  checks:\n", "  checks: # all of them\n", 1))
	cs = append(cs, d23Case{ids: []string{"J-1"}, name: "control_flow_style", yaml: "{apiVersion: strange.company/v1alpha1, kind: Judge, metadata: {task: sc-9, exemptionClass: documentation-only}, spec: {kind: required-facts, checks: [{id: J-1, acIds: [AC-1, AC-2], requiredFacts: {paths: [\"docs/guide.md\"], mustMatch: [\"x\"]}}]}}\n"})

	// Review 1's seven cases.
	add("rv1_merge_key", rep(s6DocJudgeYAML, "    - id: J-2\n      acIds: [AC-2]\n", "    - <<: {id: J-2}\n      acIds: [AC-2]\n", 1))
	add("rv1_abs_glob", d23Glob("/etc/passwd"))
	add("rv1_glob_with_space", d23Glob("a b"))
	add("rv1_empty_glob", d23Glob(""))
	add("rv1_201_paths", d23J2(d23Facts(d23StrList(d23Repeat("a", 201)...), `["x"]`)))
	add("rv1_nan_value", d23J2(d23Thr(`{metric: m, op: "<", value: .nan}`)))
	add("rv1_inf_value", d23J2(d23Thr(`{metric: m, op: "<", value: .inf}`)))

	// Glob pattern boundaries ^(?!/)[A-Za-z0-9._@+*?{},/\[\]-]{1,200}$.
	for _, g := range []string{"docs/guide.md", "**", "a/**/b.go", "*.{go,md}", "[a-z]*", "a?b", "a@b+c", "a,b", "a-b", "-", "../../x", "./a", ".hidden", "a//b", "A_Z.09"} {
		add("glob_ok_"+g, d23Glob(g))
	}
	add("glob_200_chars", d23Glob(strings.Repeat("a", 200)))
	for name, g := range map[string]string{
		"201_chars": strings.Repeat("a", 201), "backslash": "a\\b", "colon": "a:b", "latin1": "é", "tab": "a\tb", "trailing_newline": "a\n",
		"leading_newline": "\na", "line_separator": "a\u2028b", "tilde": "~x", "pipe": "a|b", "quote": "a\"b", "semicolon": "a;b",
		"parens": "a(b)", "bang": "!x", "hash": "#x", "dollar": "$x", "percent": "%x", "equals": "a=b", "nul": "a\x00b", "astral": "a😀",
		"only_slash": "/", "lone_backslash": "\\", "fullwidth": "ａ",
	} {
		add("glob_bad_"+name, d23Glob(g))
	}
	add("paths_empty_list", d23J2(d23Facts(`[]`, `["x"]`)))
	add("paths_200", d23J2(d23Facts(d23StrList(d23Repeat("a", 200)...), `["x"]`)))
	add("paths_not_a_list", d23J2(d23Facts(`"docs/a.md"`, `["x"]`)))
	add("paths_null", d23J2(d23Facts(`null`, `["x"]`)))
	add("paths_int_item", d23J2(d23Facts(`[1]`, `["x"]`)))
	add("paths_missing", d23J2("      requiredFacts:\n        mustMatch: [\"x\"]\n"))
	add("mustmatch_missing", d23J2("      requiredFacts:\n        paths: [\"a\"]\n"))
	add("requiredfacts_unknown_key", d23J2(d23Facts(`["a"]`, `["x"]`)+"        extra: 1\n"))

	// mustMatch length is counted in code points.
	add("mustmatch_300", d23Match(strings.Repeat("a", 300)))
	add("mustmatch_301", d23Match(strings.Repeat("a", 301)))
	add("mustmatch_300_latin1", d23Match(strings.Repeat("é", 300)))
	add("mustmatch_301_latin1", d23Match(strings.Repeat("é", 301)))
	add("mustmatch_300_astral", d23Match(strings.Repeat("😀", 300)))
	add("mustmatch_301_astral", d23Match(strings.Repeat("😀", 301)))
	add("mustmatch_empty_list", d23J2(d23Facts(`["a"]`, `[]`)))
	add("mustmatch_empty_string", d23Match(""))
	add("mustmatch_int_item", d23J2(d23Facts(`["a"]`, `[1]`)))
	add("mustmatch_null_item", d23J2(d23Facts(`["a"]`, `[null]`)))
	add("mustmatch_not_a_list", d23J2(d23Facts(`["a"]`, `"x"`)))

	// command: integers versus floats, bounds, very large numbers.
	for _, ts := range []string{"1", "10", "3600", "10.0", "1e1", "0x10", "0o17", "+5", "3600.0", "1_0"} {
		add("timeout_ok_"+ts, d23Argv(`["true"]`, ts))
	}
	for _, ts := range []string{"0", "3601", "-1", "1.5", "0.5", "3600.5", "99999999999999999999", "9223372036854775808", "1e308", "1e400", ".inf", ".nan", `"10"`, "true", "null", "~", "-0.0"} {
		add("timeout_bad_"+ts, d23Argv(`["true"]`, ts))
	}
	add("timeout_missing", d23J2(d23Cmd(`{argv: ["true"]}`)))
	add("argv_40", d23Argv(d23StrList(d23Repeat("a", 40)...), "10"))
	add("argv_41", d23Argv(d23StrList(d23Repeat("a", 41)...), "10"))
	add("argv_empty", d23Argv(`[]`, "10"))
	add("argv_item_500", d23Argv(d23StrList(strings.Repeat("a", 500)), "10"))
	add("argv_item_501", d23Argv(d23StrList(strings.Repeat("a", 501)), "10"))
	add("argv_item_500_latin1", d23Argv(d23StrList(strings.Repeat("é", 500)), "10"))
	add("argv_item_501_latin1", d23Argv(d23StrList(strings.Repeat("é", 501)), "10"))
	add("argv_int_item", d23Argv(`[1]`, "10"))
	add("argv_null_item", d23Argv(`[null]`, "10"))
	add("argv_not_a_list", d23Argv(`"true"`, "10"))
	add("command_unknown_key", d23J2(d23Cmd(`{argv: ["true"], timeoutSeconds: 10, shell: true}`)))
	add("argv_missing", d23J2(d23Cmd(`{timeoutSeconds: 10}`)))

	// threshold: integer and float values, non-finite and very large numbers.
	thr := func(v string) string { return d23Thr(`{metric: m, op: "<", value: ` + v + `}`) }
	for _, v := range []string{"3", "3.5", "-1", "0", "1e10", "1e308", "-1e308", "0x10", "1.5e-300", "99999999999999999999", "-99999999999999999999", "123456789012345678901234567890.5"} {
		add("value_ok_"+v, d23J2(thr(v)))
	}
	for _, v := range []string{".nan", ".NaN", ".inf", "-.inf", "+.inf", ".Inf", "1e400", "-1e400", `"3"`, "true", "null", "[]", "{}"} {
		add("value_bad_"+v, d23J2(thr(v)))
	}
	add("value_missing", d23J2(d23Thr(`{metric: m, op: "<"}`)))
	for _, op := range []string{"<", "<=", ">", ">="} {
		add("op_ok_"+op, d23J2(d23Thr(`{metric: m, op: "`+op+`", value: 1}`)))
	}
	for _, op := range []string{"==", "=", "!=", "lt", "", "<<", "< "} {
		add("op_bad_"+op, d23J2(d23Thr(`{metric: m, op: `+d23Str(op)+`, value: 1}`)))
	}
	add("metric_100", d23J2(d23Thr(`{metric: `+d23Str(strings.Repeat("m", 100))+`, op: "<", value: 1}`)))
	add("metric_101", d23J2(d23Thr(`{metric: `+d23Str(strings.Repeat("m", 101))+`, op: "<", value: 1}`)))
	add("metric_101_latin1", d23J2(d23Thr(`{metric: `+d23Str(strings.Repeat("é", 101))+`, op: "<", value: 1}`)))
	add("metric_int", d23J2(d23Thr(`{metric: 5, op: "<", value: 3}`)))
	add("threshold_unknown_key", d23J2(d23Thr(`{metric: m, op: "<", value: 1, unit: s}`)))

	// rubric.
	add("rubric_ok", d23J2(d23Rub(`"every claim is sourced"`)))
	add("rubric_2000", d23J2(d23Rub(d23Str(strings.Repeat("r", 2000)))))
	add("rubric_2001", d23J2(d23Rub(d23Str(strings.Repeat("r", 2001)))))
	add("rubric_2000_astral", d23J2(d23Rub(d23Str(strings.Repeat("😀", 2000)))))
	add("rubric_2001_astral", d23J2(d23Rub(d23Str(strings.Repeat("😀", 2001)))))
	add("rubric_null", d23J2(d23Rub("null")))
	add("rubric_int", d23J2(d23Rub("5")))
	add("rubric_timestamp", d23J2(d23Rub("2001-12-14")))
	add("rubric_bool", d23J2(d23Rub("true")))

	// checks: counts, ids, acIds.
	for _, n := range []int{1, 200, 201} {
		y, ids := d23Checks(n)
		cs = append(cs, d23Case{name: "checks_" + itoa(n), yaml: y, ids: ids})
	}
	add("checks_empty", rep(s6DocJudgeYAML, "  checks:\n", "  checks: []\n  extra:\n", 1))
	add("checks_null", "apiVersion: strange.company/v1alpha1\nkind: Judge\nmetadata: {task: sc-9, exemptionClass: documentation-only}\nspec: {kind: required-facts, checks: null}\n")
	add("checks_mapping", "apiVersion: strange.company/v1alpha1\nkind: Judge\nmetadata: {task: sc-9, exemptionClass: documentation-only}\nspec: {kind: required-facts, checks: {id: J-1}}\n")
	for _, id := range []string{"J-0", "J-01", "J-1000", "J-", "j-1", "J-1\n", "J-1 ", "1", "J-١"} {
		add("id_bad_"+id, rep(s6DocJudgeYAML, "id: J-2", "id: "+d23Str(id), 1))
	}
	cs = append(cs, d23Case{name: "id_999", yaml: rep(s6DocJudgeYAML, "id: J-2", "id: J-999", 1), ids: []string{"J-1", "J-999"}})
	add("id_int", rep(s6DocJudgeYAML, "id: J-2", "id: 2", 1))
	add("id_tagged_str", rep(s6DocJudgeYAML, "id: J-2", "id: !!str J-2", 1))
	add("id_missing", rep(s6DocJudgeYAML, "    - id: J-2\n", "    - \n", 1))
	for _, ac := range []string{"[]", "[AC-0]", "[AC-01]", "[AC-1000]", "AC-2", "[ac-2]", `["AC-2\n"]`, "[2]", "null", "[null]"} {
		add("acids_bad_"+ac, rep(s6DocJudgeYAML, "acIds: [AC-2]", "acIds: "+ac, 1))
	}
	strict("acids_duplicate_within_check", rep(s6DocJudgeYAML, "acIds: [AC-2]", "acIds: [AC-2, AC-2]", 1), "duplicate acIds in one check; the schema has no uniqueItems, the library may reject the duplicate")
	strict("acids_anchor_alias", rep(s6DocJudgeYAML, "acIds: [AC-1]", "acIds: &a [AC-1]", 1), "an anchor: accepted by the schema loader, the library may refuse it")
	strict("ids_duplicate", rep(s6DocJudgeYAML, "id: J-2", "id: J-1", 1), "duplicate check ids; the schema has no uniqueItems, the library rejects the duplicate (plan 22 14.8)")
	strict("acid_not_frozen", rep(s6DocJudgeYAML, "acIds: [AC-2]", "acIds: [AC-7]", 1), "a check for an AC that is not frozen is judge_invalid (ac_not_covered)")
	add("check_unknown_key", rep(s6DocJudgeYAML, "    - id: J-2\n", "    - id: J-2\n      on: 1\n", 1))
	add("check_missing_acids", rep(s6DocJudgeYAML, "      acIds: [AC-2]\n", "", 1))

	// metadata and top level.
	strict("task_other", rep(s6DocJudgeYAML, "task: sc-9", "task: sc-10", 1), "another task's judge is judge_invalid")
	strict("task_huge_number", rep(s6DocJudgeYAML, "task: sc-9", "task: sc-99999999999999999999", 1), "another task's judge is judge_invalid")
	for _, task := range []string{"sc-", "SC-9", "sc-9\n", "sc-9 ", "9", "sc_9", "sc-٩"} {
		add("task_bad_"+task, rep(s6DocJudgeYAML, "task: sc-9", "task: "+d23Str(task), 1))
	}
	add("task_int", rep(s6DocJudgeYAML, "task: sc-9", "task: 9", 1))
	strict("class_41_chars", rep(s6DocJudgeYAML, "exemptionClass: documentation-only", "exemptionClass: a"+strings.Repeat("b", 40), 1), "another class is judge_invalid")
	strict("class_2_chars", rep(s6DocJudgeYAML, "exemptionClass: documentation-only", "exemptionClass: ab", 1), "another class is judge_invalid")
	for _, cl := range []string{"a", "a" + strings.Repeat("b", 41), "Documentation-only", "-ab", "1ab", "documentation_only", "documentation-only\n"} {
		add("class_bad_"+cl, rep(s6DocJudgeYAML, "exemptionClass: documentation-only", "exemptionClass: "+d23Str(cl), 1))
	}
	add("kind_unknown", rep(s6DocJudgeYAML, "kind: required-facts", "kind: bogus", 1))
	add("kind_case", rep(s6DocJudgeYAML, "kind: required-facts", "kind: Required-Facts", 1))
	strict("kind_other_valid_kind", rep(s6DocJudgeYAML, "kind: required-facts", "kind: invariants", 1), "a kind other than the class's kind is judge_invalid")
	add("apiversion_wrong", rep(s6DocJudgeYAML, "v1alpha1", "v1", 1))
	add("apiversion_newline", rep(s6DocJudgeYAML, "apiVersion: strange.company/v1alpha1", `apiVersion: "strange.company/v1alpha1\n"`, 1))
	add("kind_wrong", rep(s6DocJudgeYAML, "kind: Judge", "kind: judge", 1))
	add("unknown_top_key", s6DocJudgeYAML+"extra: 1\n")
	add("unknown_spec_key", rep(s6DocJudgeYAML, "spec:\n", "spec:\n  extra: 1\n", 1))
	add("unknown_metadata_key", rep(s6DocJudgeYAML, "metadata:\n", "metadata:\n  extra: 1\n", 1))
	add("missing_metadata", rep(s6DocJudgeYAML, "metadata:\n  task: sc-9\n  exemptionClass: documentation-only\n", "", 1))
	add("empty_document", "")
	add("null_document", "null\n")
	add("list_document", "- a\n")
	add("scalar_document", "judge\n")
	add("two_documents", s6DocJudgeYAML+"---\n"+s6DocJudgeYAML)
	add("duplicate_key", rep(s6DocJudgeYAML, "    - id: J-2\n", "    - id: J-2\n      id: J-3\n", 1))
	add("bom", "\xef\xbb\xbf"+s6DocJudgeYAML)
	add("tab_indent", rep(s6DocJudgeYAML, "  checks:\n    - id: J-1", "  checks:\n\t- id: J-1", 1))
	add("not_yaml", "{{{{")
	add("nul_byte", s6DocJudgeYAML+"\x00")
	add("yaml_binary_tag", rep(s6DocJudgeYAML, "task: sc-9", "task: !!binary c2MtOQ==", 1))
	add("merge_key_top_level", "<<: {apiVersion: strange.company/v1alpha1}\n"+rep(s6DocJudgeYAML, "apiVersion: strange.company/v1alpha1\n", "", 1))
	add("merge_key_in_metadata", rep(s6DocJudgeYAML, "  task: sc-9\n", "  <<: {task: sc-9}\n", 1))
	add("non_string_key", rep(s6DocJudgeYAML, "metadata:\n", "1: x\nmetadata:\n", 1))
	// The library counts or types differently from the schema validator and refuses these documents although the
	// schema accepts them. Allowed (D23 B1: native stricter-than-schema cases may stay stricter); listed so that
	// any other disagreement is a failure.
	stricter := map[string]string{
		"mustmatch_300_latin1": "mustMatch length counted in bytes, the validator counts code points",
		"mustmatch_300_astral": "mustMatch length counted in bytes, the validator counts code points",
		"argv_item_500_latin1": "argv item length counted in bytes, the validator counts code points",
		"rubric_2000_astral":   "rubric length counted in bytes, the validator counts code points",
		"timeout_ok_10.0":      "a float-typed whole number for an integer field",
		"timeout_ok_1e1":       "a float-typed whole number for an integer field",
		"timeout_ok_3600.0":    "a float-typed whole number for an integer field",
		"rubric_timestamp":     "a YAML timestamp scalar is not a string for the native decoder",
	}
	for i := range cs {
		if why, ok := stricter[cs[i].name]; ok {
			cs[i].stricter = why
			delete(stricter, cs[i].name)
		}
	}
	if len(stricter) != 0 {
		panic("stricter list names a case that does not exist")
	}
	return cs
}

// 22-AC-3 / D23 (B1): wherever the real $defs/JudgeArtifact (loaded the way the platform loads it,
// contracts.ParseYAML, which refuses duplicate keys and YAML merge keys) rejects a document, ClassifyJudge answers
// judge_invalid; where the schema accepts it, ClassifyJudge answers judge_valid unless the case is a documented
// stricter-than-schema one. Same rule as D16 N2 (differential against the real validator).
func TestClassifyJudge_AgreesWithJudgeArtifactSchema_D23_22AC3(t *testing.T) {
	sch := d23Schema(t)
	rejected, accepted := 0, 0
	names := map[string]bool{}
	for _, c := range d23Cases() {
		if names[c.name] {
			t.Fatalf("duplicate case name %q", c.name)
		}
		names[c.name] = true
		t.Run(c.name, func(t *testing.T) {
			var serr error
			if v, perr := contracts.ParseYAML([]byte(c.yaml)); perr != nil {
				serr = perr
			} else {
				serr = sch.Validate(v)
			}
			fx := s6BaseFixture(t)
			fx.JudgeYAML = c.yaml
			if c.ids != nil {
				fx.Observed.JudgeChecks = nil
				for _, id := range c.ids {
					fx.Observed.JudgeChecks = append(fx.Observed.JudgeChecks, JudgeCheck{ID: id, Status: "fail"})
				}
			}
			in, _ := s6ValidateInput(t, fx)
			got, err := ClassifyJudge(in)
			if err != nil {
				t.Fatalf("ClassifyJudge returned an error (%v), want a classification", err)
			}
			if serr != nil {
				rejected++
				if got.Outcome != "judge_invalid" {
					t.Errorf("the schema rejects the document (%v) but ClassifyJudge = %s %v, want judge_invalid", d23First(serr.Error()), got.Outcome, got.Reasons)
				}
				if got.Outcome == "judge_invalid" && len(got.Reasons) == 0 {
					t.Errorf("judge_invalid without a reason")
				}
				return
			}
			accepted++
			if got.Outcome != "judge_valid" && c.stricter == "" {
				t.Errorf("the schema accepts the document and no stricter reading is documented, but ClassifyJudge = %s %v, want judge_valid", got.Outcome, got.Reasons)
			}
			if c.stricter != "" && got.Outcome != "judge_valid" && got.Outcome != "judge_invalid" {
				t.Errorf("stricter case (%s): outcome %s is neither judge_valid nor judge_invalid", c.stricter, got.Outcome)
			}
		})
	}
	// Not vacuous: the set exercises both directions.
	t.Logf("%d documents rejected by the schema, %d accepted", rejected, accepted)
	if rejected < 100 || accepted < 40 {
		t.Errorf("only %d rejected / %d accepted documents in the boundary set", rejected, accepted)
	}
}

func d23First(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// 22-AC-3 / D23 (B1): review 1's seven documents are judge_invalid (explicit, independent of the differential); the
// control and the nearest valid neighbours are judge_valid.
func TestClassifyJudge_ReviewOneSevenInvalid_D23_22AC3(t *testing.T) {
	rep := strings.Replace
	bad := map[string]string{
		"absolute_glob":  d23Glob("/etc/passwd"),
		"glob_has_space": d23Glob("a b"),
		"empty_glob":     d23Glob(""),
		"201_paths":      d23J2(d23Facts(d23StrList(d23Repeat("a", 201)...), `["x"]`)),
		"nan_value":      d23J2(d23Thr(`{metric: m, op: "<", value: .nan}`)),
		"inf_value":      d23J2(d23Thr(`{metric: m, op: "<", value: .inf}`)),
		"yaml_merge_key": rep(s6DocJudgeYAML, "    - id: J-2\n      acIds: [AC-2]\n", "    - <<: {id: J-2}\n      acIds: [AC-2]\n", 1),
	}
	good := map[string]string{
		"control":          s6DocJudgeYAML,
		"relative_glob":    d23Glob("docs/a.md"),
		"200_paths":        d23J2(d23Facts(d23StrList(d23Repeat("a", 200)...), `["x"]`)),
		"finite_value":     d23J2(d23Thr(`{metric: m, op: "<", value: 3.5}`)),
		"integer_value":    d23J2(d23Thr(`{metric: m, op: "<", value: 3}`)),
		"explicit_id_keys": rep(s6DocJudgeYAML, "    - id: J-2\n      acIds: [AC-2]\n", "    - id: J-2\n      acIds: [AC-2]\n", 1),
	}
	for name, y := range bad {
		t.Run("invalid/"+name, func(t *testing.T) {
			fx := s6BaseFixture(t)
			fx.JudgeYAML = y
			in, sc := s6ValidateInput(t, fx)
			got, err := ClassifyJudge(in)
			s6CheckValidate(t, in, sc, got, err, s6Expect{Function: "validate", Outcome: "judge_invalid", NonEmpty: true})
		})
	}
	for name, y := range good {
		t.Run("valid/"+name, func(t *testing.T) {
			fx := s6BaseFixture(t)
			fx.JudgeYAML = y
			in, sc := s6ValidateInput(t, fx)
			got, err := ClassifyJudge(in)
			s6CheckValidate(t, in, sc, got, err, s6Expect{Function: "validate", Outcome: "judge_valid", Reasons: &[]string{}})
		})
	}
}

// ---------------------------------------------------------------------------------------------------------------
// B2: the research-report heading recogniser.

// d23Research is the judging input of the research class (judged on its report) with the given report.
func d23Research(t *testing.T, report string, criteria []Criterion) JudgingInput {
	t.Helper()
	fx := s6BaseFixture(t)
	fx.TaskClass, fx.Approval = "research", "auto"
	fx.Green.Checks, fx.Green.JudgeChecks = []CheckRun{}, []JudgeCheck{}
	fx.Manifest, fx.Integrity = nil, Integrity{}
	fx.ProducerPaths, fx.Required = nil, []string{}
	fx.Report = &report
	if criteria != nil {
		fx.Criteria = criteria
	}
	return s6JudgingInput(t, fx)
}

var d23TwoACs = []Criterion{{ID: "AC-1", Kind: "change"}, {ID: "AC-2", Kind: "change"}}

// 22-AC-3 / D23 (B2): the recogniser is a fail-closed canonical subset. These reports are judging_fail with a
// reason. The "visible" variants address every AC by a plain heading outside any construct, so that only the
// fail-closed rule (not a hidden heading) can make them fail.
func TestClassifyJudging_ReportRecogniser_FailsClosed_D23_22AC3(t *testing.T) {
	cases := map[string]string{
		// Review 1's repros: the heading hides in a construct the recogniser does not understand.
		"html_comment_hides_heading":                    "# R\n\n<!--\n## AC-1\n-->\n\n## AC-2\ny\n",
		"script_hides_heading":                          "# R\n\n<script>\n## AC-1\n</script>\n\n## AC-2\ny\n",
		"pre_hides_heading":                             "# R\n\n<pre>\n## AC-1\n</pre>\n\n## AC-2\ny\n",
		"3_backticks_inside_4_backtick_fence":           "# R\n\n````\n```\n## AC-1\n````\n\n## AC-2\ny\n",
		"closer_with_info_string":                       "# R\n\n```\n```go\n## AC-1\n```\n\n## AC-2\ny\n",
		"3_backticks_inside_4_backtick_fence_ac2_first": "## AC-2\n\n````\n```\n## AC-1\n````\n",
		"closer_with_info_string_ac2_first":             "## AC-2\n\n```\n```go\n## AC-1\n```\n",
		"tilde_closer_with_info_string":                 "## AC-2\n\n~~~\n~~~ x\n## AC-1\n~~~\n",
		"shorter_closer_run":                            "## AC-2\n\n~~~~\n~~~\n## AC-1\n~~~~\n",
		"other_char_does_not_close":                     "## AC-2\n\n```\n~~~\n## AC-1\n```\n",
		"bare_cr_hides_fence":                           "# R\r```\r## AC-1\r```\n\n## AC-2\ny\n",
		// Fail closed: every AC is addressed outside, the construct is only present.
		"visible_html_comment":      "## AC-1\n\n<!-- note -->\n\n## AC-2\n",
		"visible_script":            "## AC-1\n\n<script>x</script>\n\n## AC-2\n",
		"visible_pre":               "## AC-1\n\n<pre>\nx\n</pre>\n\n## AC-2\n",
		"visible_html_one_space":    "## AC-1\n\n <div>\n\n## AC-2\n",
		"visible_html_three_spaces": "## AC-1\n\n   <div>\n\n## AC-2\n",
		"visible_bare_cr":           "## AC-1\r\n## AC-2\rtext\n",
		"visible_trailing_bare_cr":  "## AC-1\n## AC-2\n\r",
		"visible_nul":               "## AC-1\n## AC-2\n\x00",
		"visible_nul_in_text":       "## AC-1\nab\x00cd\n## AC-2\n",
		"visible_line_separator":    "## AC-1\n## AC-2\n\u2028\n",
		"visible_line_sep_in_text":  "## AC-1\nab\u2028cd\n## AC-2\n",
		"visible_form_feed":         "## AC-1\n## AC-2\n\f\n",
		"visible_form_feed_in_text": "## AC-1\nab\fcd\n## AC-2\n",
		// Not AC headings.
		"setext_heading_is_not_atx":      "# R\n\nAC-1\n====\n\n## AC-2\ny\n",
		"setext_dash_heading_is_not_atx": "# R\n\nAC-1\n----\n\n## AC-2\ny\n",
		"indented_code_is_not_a_heading": "# R\n\n    ## AC-1\n\n## AC-2\ny\n",
	}
	for name, report := range cases {
		t.Run(name, func(t *testing.T) {
			in := d23Research(t, report, nil)
			got, err := ClassifyJudging(in)
			s6CheckJudging(t, in, got, err, s6Expect{Function: "judging", Outcome: "judging_fail", NonEmpty: true})
		})
	}
}

// 22-AC-3 / D23 (B2) twins: ATX headings indented 0 to 3 spaces outside fences, and correctly closed fences (same
// character, run length at least the opener's, nothing but whitespace after it) with the headings after them,
// are judging_pass.
func TestClassifyJudging_ReportRecogniser_Accepts_D23_22AC3(t *testing.T) {
	four := []Criterion{{ID: "AC-1", Kind: "change"}, {ID: "AC-2", Kind: "change"}, {ID: "AC-3", Kind: "change"}, {ID: "AC-4", Kind: "change"}}
	cases := map[string]struct {
		report   string
		criteria []Criterion
	}{
		"atx_at_columns_0_to_3":         {"# R\n\n## AC-1\n\n ## AC-2\n\n  ## AC-3\n\n   ## AC-4\n", four},
		"atx_any_level_in_column_3":     {"   ###### AC-1\n   # AC-2\n", nil},
		"fence_closed_then_headings":    {"# R\n\n```\ncode\n```\n\n## AC-1\n\n## AC-2\n", nil},
		"fence_with_heading_inside":     {"```\n## AC-1\n```\n\n## AC-1 again\n## AC-2\n", nil},
		"fence_opener_with_info_string": {"```go\nx := 1\n```\n## AC-1\n## AC-2\n", nil},
		"4_backtick_closed_by_5":        {"````\n```\nx\n`````\n## AC-1\n## AC-2\n", nil},
		"4_backtick_closed_by_4":        {"````\n```\n## AC-1\n````\n## AC-1\n## AC-2\n", nil},
		"tilde_closed_by_longer":        {"~~~\nx\n~~~~~\n## AC-1\n## AC-2\n", nil},
		"closer_with_trailing_spaces":   {"```\nx\n```   \n## AC-1\n## AC-2\n", nil},
		"fence_closer_indented_3":       {"```\nx\n   ```\n## AC-1\n## AC-2\n", nil},
		"crlf_line_endings":             {"# R\r\n\r\n## AC-1\r\nx\r\n\r\n## AC-2\r\ny\r\n", nil},
		"angle_bracket_inside_heading":  {"## AC-1 <b>x</b>\n## AC-2 a < b\n", nil},
		"angle_bracket_mid_line":        {"## AC-1\nx <y>\n## AC-2\n", nil},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			in := d23Research(t, c.report, c.criteria)
			got, err := ClassifyJudging(in)
			s6CheckJudging(t, in, got, err, s6Expect{Function: "judging", Outcome: "judging_pass", Reasons: &[]string{}})
		})
	}
}

// ---------------------------------------------------------------------------------------------------------------
// N1: ClassifyJudging applies the G2 integrity facts.

func d23Judging(t *testing.T, mutate func(fx *s6Fixture)) JudgingInput {
	t.Helper()
	fx := s6BaseFixture(t)
	mutate(&fx)
	return s6JudgingInput(t, fx)
}

// 22-AC-3 / D23 (N1): an unaccepted foreign commit, a violation-severity static finding and a discovery change are
// tamper_detected with the reason ClassifyGreen's G2 gives; a warning-only non-14.6 finding stays a warning.
func TestClassifyJudging_IntegrityFacts_D23_22AC3(t *testing.T) {
	foreign := "1111111111111111111111111111111111111111"
	type tc struct {
		name   string
		mutate func(fx *s6Fixture)
		want   []string
	}
	cases := []tc{
		{"foreign_commit", func(fx *s6Fixture) { fx.Integrity.ForeignCommits = []string{foreign} }, []string{"foreign_commit"}},
		{"whole_file_discovery_change", func(fx *s6Fixture) {
			fx.Integrity.DiscoveryChanges = []DiscoveryChange{{Path: "docs/pytest.ini", Kind: "whole_file_changed"}}
		}, []string{"discovery_config_changed"}},
		{"semantic_key_discovery_change", func(fx *s6Fixture) {
			fx.Integrity.DiscoveryChanges = []DiscoveryChange{{Path: "package.json", Key: "scripts.test", Kind: "semantic_key_changed"}}
		}, []string{"discovery_config_changed"}},
	}
	for code, reason := range map[string]string{
		"skip_marker_added": "skip_marker_added", "test_env_detection": "test_env_detection", "test_file_deleted": "test_file_deleted",
		"test_file_renamed": "test_file_deleted", "existing_test_modified": "existing_test_modified",
		"test_dependency_changed": "test_dependency_changed", "new_discovery_file": "discovery_config_changed",
		"protected_path_changed": "discovery_config_changed",
	} {
		cases = append(cases, tc{"violation_" + code, func(fx *s6Fixture) {
			fx.Integrity.StaticFindings = []IntegrityFinding{{Code: code, Path: "docs/guide.md", Severity: "violation"}}
		}, []string{reason}})
	}
	cases = append(cases, tc{"foreign_commit_and_discovery_change", func(fx *s6Fixture) {
		fx.Integrity.ForeignCommits = []string{foreign}
		fx.Integrity.DiscoveryChanges = []DiscoveryChange{{Path: "docs/pytest.ini", Kind: "whole_file_changed"}}
	}, []string{"discovery_config_changed", "foreign_commit"}})
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := d23Judging(t, c.mutate)
			got, err := ClassifyJudging(in)
			s6CheckJudging(t, in, got, err, s6Expect{Function: "judging", Outcome: "tamper_detected", Reasons: &c.want})
		})
	}

	// G2 refuses what it cannot judge: a foreign commit that is no commit id, a warning with a section 14.6 code.
	for name, mutate := range map[string]func(fx *s6Fixture){
		"foreign_commit_is_not_a_commit_id": func(fx *s6Fixture) { fx.Integrity.ForeignCommits = []string{"HEAD"} },
		"warning_with_a_14_6_code": func(fx *s6Fixture) {
			fx.Integrity.StaticFindings = []IntegrityFinding{{Code: "skip_marker_added", Path: "docs/guide.md", Severity: "warning"}}
		},
		"unknown_discovery_kind": func(fx *s6Fixture) {
			fx.Integrity.DiscoveryChanges = []DiscoveryChange{{Path: "docs/pytest.ini", Kind: "renamed"}}
		},
	} {
		t.Run("infra/"+name, func(t *testing.T) {
			in := d23Judging(t, mutate)
			got, err := ClassifyJudging(in)
			s6CheckJudging(t, in, got, err, s6Expect{Function: "judging", Error: "verification_infra_failed"})
		})
	}

	// Twins: the baseline, a warning-only non-14.6 finding (a warning of the classification, G2's treatment), and
	// facts that are present but empty.
	t.Run("twin_clean", func(t *testing.T) {
		in := d23Judging(t, func(fx *s6Fixture) {})
		got, err := ClassifyJudging(in)
		s6CheckJudging(t, in, got, err, s6Expect{Function: "judging", Outcome: "judging_pass", Reasons: &[]string{}})
	})
	t.Run("twin_warning_only_finding_stays_a_warning", func(t *testing.T) {
		in := d23Judging(t, func(fx *s6Fixture) {
			fx.Integrity.StaticFindings = []IntegrityFinding{{Code: "test_literal_leakage", Path: "docs/guide.md", Severity: "warning"}}
		})
		got, err := ClassifyJudging(in)
		s6CheckJudging(t, in, got, err, s6Expect{Function: "judging", Outcome: "judging_pass", Reasons: &[]string{}})
		if want := []string{"test_literal_leakage: docs/guide.md"}; jsonOf(got.Warnings) != jsonOf(want) {
			t.Errorf("warnings = %s, want %s (as ClassifyGreen G2 records them)", jsonOf(got.Warnings), jsonOf(want))
		}
	})
}

// ---------------------------------------------------------------------------------------------------------------
// N4: the characterization base observation is bound to the judge observation.

// 22-AC-3 / D23 (N4): BaseObserved whose ValidationBlob, task, repository or IntentID differs from Observed is an
// infra error; the bound base observation is judge_valid.
func TestClassifyJudge_CharacterizationBaseObservedBound_D23_22AC3(t *testing.T) {
	load := func(t *testing.T) JudgeInput {
		fx, _ := s6Load(t, "characterization", "valid_passes_at_judge_commit_and_base")
		in, _ := s6ValidateInput(t, fx)
		if in.BaseObserved == nil {
			t.Fatal("the characterization fixture has no base observation")
		}
		return in
	}
	t.Run("twin_bound", func(t *testing.T) {
		in := load(t)
		got, err := ClassifyJudge(in)
		if err != nil || got.Outcome != "judge_valid" {
			t.Fatalf("bound base observation = %s %v, %v, want judge_valid", got.Outcome, got.Reasons, err)
		}
	})
	for name, mutate := range map[string]func(b *Observation){
		"validation_blob_differs": func(b *Observation) { b.ValidationBlob = "0000000000000000000000000000000000000001" },
		"validation_blob_empty":   func(b *Observation) { b.ValidationBlob = "" },
		"task_differs":            func(b *Observation) { b.TaskID = "sc-10" },
		"repository_differs":      func(b *Observation) { b.Repository = "o/other" },
		"intent_id_differs":       func(b *Observation) { b.IntentID = "vi-0000" },
		"intent_id_empty":         func(b *Observation) { b.IntentID = "" },
	} {
		t.Run(name, func(t *testing.T) {
			in := load(t)
			mutate(in.BaseObserved)
			got, err := ClassifyJudge(in)
			s6WantErr(t, err, got, s6Expect{Error: "verification_infra_failed"})
		})
	}
}

// ---------------------------------------------------------------------------------------------------------------
// N5: unusable manifests and criteria are refused, not classified.

// 22-AC-3 / D23 (N5): a judge manifest with no tests, or with no files, is an infra error (it is also invalid under
// test-manifest.schema.yaml, minItems 1, which is why its digest and the integrity facts are recomputed here: the
// facts are consistent, the manifest is unusable). Twin: the baseline manifest is judging_pass.
func TestClassifyJudging_UnusableJudgeManifest_D23_22AC3(t *testing.T) {
	redigest := func(fx *s6Fixture, mutate func(m *Manifest)) {
		m := *fx.Manifest
		mutate(&m)
		d, err := manifestDigest(m)
		if err != nil {
			t.Fatalf("manifestDigest: %v", err)
		}
		m.Digest = d
		fx.Manifest = &m
		fx.Integrity.ManifestDigest = d
	}
	t.Run("twin_baseline", func(t *testing.T) {
		in := d23Judging(t, func(fx *s6Fixture) { redigest(fx, func(m *Manifest) {}) })
		got, err := ClassifyJudging(in)
		s6CheckJudging(t, in, got, err, s6Expect{Function: "judging", Outcome: "judging_pass", Reasons: &[]string{}})
	})
	t.Run("no_tests", func(t *testing.T) {
		in := d23Judging(t, func(fx *s6Fixture) {
			redigest(fx, func(m *Manifest) { m.Tests, m.ACMapping = []ManifestTest{}, []ManifestACMapping{} })
			fx.Green.JudgeChecks = []JudgeCheck{}
		})
		got, err := ClassifyJudging(in)
		s6CheckJudging(t, in, got, err, s6Expect{Function: "judging", Error: "verification_infra_failed"})
	})
	t.Run("no_files", func(t *testing.T) {
		in := d23Judging(t, func(fx *s6Fixture) {
			redigest(fx, func(m *Manifest) { m.Files = []ManifestFile{} })
			fx.Integrity.ManifestFiles = []IntegrityFile{}
		})
		got, err := ClassifyJudging(in)
		s6CheckJudging(t, in, got, err, s6Expect{Function: "judging", Error: "verification_infra_failed"})
	})
}

// 22-AC-3 / D23 (N5): research criteria that are empty, contain a duplicate or have an unknown kind are an infra
// error, however good the report; the valid criteria are judged on the report (twin: judging_pass).
func TestClassifyJudging_ResearchCriteria_D23_22AC3(t *testing.T) {
	report := "# R\n\n## AC-1\nx\n\n## AC-2\ny\n"
	t.Run("twin_valid_criteria", func(t *testing.T) {
		in := d23Research(t, report, d23TwoACs)
		got, err := ClassifyJudging(in)
		s6CheckJudging(t, in, got, err, s6Expect{Function: "judging", Outcome: "judging_pass", Reasons: &[]string{}})
	})
	t.Run("twin_preservation_kind", func(t *testing.T) {
		in := d23Research(t, report, []Criterion{{ID: "AC-1", Kind: "change"}, {ID: "AC-2", Kind: "preservation"}})
		got, err := ClassifyJudging(in)
		s6CheckJudging(t, in, got, err, s6Expect{Function: "judging", Outcome: "judging_pass", Reasons: &[]string{}})
	})
	for name, crit := range map[string][]Criterion{
		"empty":           {},
		"duplicate":       {{ID: "AC-1", Kind: "change"}, {ID: "AC-1", Kind: "change"}},
		"duplicate_kinds": {{ID: "AC-1", Kind: "change"}, {ID: "AC-2", Kind: "change"}, {ID: "AC-1", Kind: "preservation"}},
		"unknown_kind":    {{ID: "AC-1", Kind: "bogus"}, {ID: "AC-2", Kind: "change"}},
		"empty_kind":      {{ID: "AC-1", Kind: ""}, {ID: "AC-2", Kind: "change"}},
	} {
		t.Run(name, func(t *testing.T) {
			in := d23Research(t, report, crit)
			in.Criteria = crit // an empty list must not fall back to the default
			got, err := ClassifyJudging(in)
			s6CheckJudging(t, in, got, err, s6Expect{Function: "judging", Error: "verification_infra_failed"})
		})
	}
}

// ---------------------------------------------------------------------------------------------------------------
// 100x determinism of the Go-built homes (R11b, R14, protected_path_changed).

// 22-AC-3 / D23: the cases that have no fixture directory (R11b nondeterministic and marker-conditional, R14 too
// large and its twin, protected_path_changed for the tester and implementer roles) are classified 100 times from
// the same inputs; the output bytes are identical and carry the expected classification.
func TestGoBuiltHomes_100xByteIdentical_D23_22AC3(t *testing.T) {
	gen := func(t *testing.T, name string, in GenerationInput, outcome string, reason string) {
		t.Run(name, func(t *testing.T) {
			out := s6Repeat(t, func() string { got, err := ClassifyRedGen(in); return s6Output(got, err) })
			if !strings.Contains(out, `"outcome":"`+outcome+`"`) || (reason != "" && !strings.Contains(out, `"`+reason+`"`)) {
				t.Errorf("output %s does not carry outcome %s / reason %q", out, outcome, reason)
			}
		})
	}
	var seed uint64
	var runs []string
	for seed = 1; seed < 1000; seed++ {
		if runs = flakySimulation(seed); runs[0] != runs[1] || runs[1] != runs[2] {
			break
		}
	}
	flaky := genTest{"TestFlaky", testFilePath, "absent", runs[0]}
	steady := genTest{"TestFlaky", testFilePath, "absent", "failed_assertion"}
	gen(t, "R11b_nondeterministic", r11bSpec(t, flaky, &Repeats{Runs: 3, Inconsistent: []string{gid("TestFlaky")}}), "inconclusive", "nondeterministic_test")
	gen(t, "R11b_deterministic_twin", r11bSpec(t, steady, &Repeats{Runs: 3}), "red_valid", "")
	mk := func(sensitive []string) GenerationInput {
		fx, _ := loadRedFixture(t, "valid/go_zero_value_stub")
		return GenerationInput{RedInput: redInputOf(t, fx), Repeats: &Repeats{Runs: 3, MarkerSensitive: sensitive}}
	}
	gen(t, "R11b_marker_conditional", mk([]string{"unit::example.com/x/internal/httpx.TestNew"}), "red_invalid", "marker_conditional")
	gen(t, "R11b_marker_twin", mk(nil), "red_valid", "")
	gen(t, "R14_manifest_too_large", sizedInput(t, sizedTests(205, 300)), "red_invalid", "manifest_too_large")
	gen(t, "R14_twin_below_limit", sizedInput(t, sizedTests(198, 300)), "red_valid", "")
	gen(t, "R14_more_than_1000_tests", sizedInput(t, sizedTests(1001, 44)), "red_invalid", "manifest_too_large")

	plat, eff := s4Effective(t, s4PolicySpec{Name: "demo", Allowed: s4AllClasses})
	changes := s4Add(".woodpecker/ci.yaml", "docs/AGENTS.md", "policy/verification.yaml", "contracts/x.schema.yaml", ".claude/settings.json",
		"src/main.go", "src/main_test.go", "docs/a.md", "Makefile")
	for _, role := range []string{"tester", "implementer"} {
		t.Run("protected_path_changed_"+role, func(t *testing.T) {
			in := ScopeInput{Role: role, Entries: s4Diff(t, nil, changes), Platform: plat, Effective: eff}
			out := s6Repeat(t, func() string { got, err := CheckScope(in); return s6Output(got, err) })
			var paths []string
			got, err := CheckScope(in)
			if err != nil {
				t.Fatal(err)
			}
			for _, v := range got {
				paths = append(paths, v.Path)
			}
			sort.Strings(paths)
			want := []string{".claude/settings.json", ".woodpecker/ci.yaml", "Makefile", "contracts/x.schema.yaml", "docs/AGENTS.md", "policy/verification.yaml"}
			if jsonOf(paths) != jsonOf(want) || !strings.Contains(out, s4ProtectedCode) {
				t.Errorf("violations %v (output %s), want %s on %v", paths, out, s4ProtectedCode, want)
			}
		})
	}
}
