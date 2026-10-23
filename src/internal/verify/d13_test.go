package verify

import "testing"

// S1 review round 4 (decision D13): (B1) AnalyzeStubs never panics, (B2) a Go TypeSpec that exists at base gains
// no added line except a marked, untagged, named struct field, (N1) Rust/TS type names must be fresh like stub
// names, (N3) Python annotation simple types allow spaces only around `|` and after `,`. Fixtures are inline.

// d13Run analyses one file; a panic is reported to the caller instead of crashing the test binary.
func d13Run(t testing.TB, c d11Bad) (facts []StubFact, malformed int, err error, panicked any) {
	t.Helper()
	defer func() { panicked = recover() }()
	facts, malformed, err = d11Analyze(t, d11File(c))
	return
}

// d13Bad asserts AnalyzeStubs neither panics nor accepts the case (an error or >= 1 malformed fact).
func d13Bad(t *testing.T, cases []d11Bad) {
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			facts, malformed, err, p := d13Run(t, c)
			switch {
			case p != nil:
				t.Errorf("AnalyzeStubs panicked: %v", p)
			case err != nil:
			case malformed < 1:
				t.Errorf("non-canonical input accepted: facts = %s, want >= 1 malformed fact or an error", jsonOf(facts))
			}
		})
	}
}

// d13BadRed asserts ClassifyRed is never red_valid for them (red_invalid / stub_malformed when analysis succeeds).
func d13BadRed(t *testing.T, cases []d11Bad) {
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			facts, _, err, p := d13Run(t, c)
			if p != nil {
				t.Fatalf("AnalyzeStubs panicked: %v", p)
			}
			if err != nil {
				return
			}
			got, err := ClassifyRed(d11RedInput(t, d11File(c), facts))
			if err != nil {
				t.Fatalf("ClassifyRed: %v", err)
			}
			if got.Outcome != "red_invalid" || !d9HasReason(got, "stub_malformed") {
				t.Errorf("classification = %s %v, want red_invalid with stub_malformed", got.Outcome, got.Reasons)
			}
		})
	}
}

// d13Good asserts a canonical twin: no error, no malformed fact, at least one fact, and red_valid.
func d13Good(t *testing.T, cases []d11Bad) {
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			facts, malformed, err, p := d13Run(t, c)
			if p != nil {
				t.Fatalf("AnalyzeStubs panicked: %v", p)
			}
			if err != nil || malformed != 0 || len(facts) < 1 {
				t.Fatalf("facts = %s err = %v, want >= 1 fact, none malformed", jsonOf(facts), err)
			}
			got, err := ClassifyRed(d11RedInput(t, d11File(c), facts))
			if err != nil {
				t.Fatalf("ClassifyRed: %v", err)
			}
			if got.Outcome != "red_valid" || len(got.Reasons) != 0 {
				t.Errorf("classification = %s %v, want red_valid", got.Outcome, got.Reasons)
			}
		})
	}
}

func d13B1Cases() []d11Bad {
	ts, rs := "typescript", "rust"
	tsm, rsm := d11M(ts), d11M(rs)
	return []d11Bad{
		{"ts_new_file_decl_last_line", ts, "", tsm + "export function f(): number {\n", false},
		{"rust_decl_at_eof", rs, "", rsm + "pub fn f() -> u32 {\n", false},
		{"ts_prepended_before_existing_body", ts, "  return 0;\n}\n", tsm + "export function f(): number {\n  return 0;\n}\n", false},
		{"rust_blank_line_after_brace_at_eof", rs, "", rsm + "pub fn f() -> u32 {\n\n", false},
		{"ts_brace_then_comment_only", ts, "", tsm + "export function f(): number {\n// note\n", false},
	}
}

// 22-AC-4 (D13 B1): a declaration line ending in `{` that closes the added run never crashes the analysis.
func TestAnalyzeStubs_NoPanicOnOpenBrace_D13B1_22AC4(t *testing.T) { d13Bad(t, d13B1Cases()) }

func TestClassifyRed_OpenBraceStubs_D13B1_22AC4(t *testing.T) { d13BadRed(t, d13B1Cases()) }

const (
	d13GoStubFn = "// SC-STUB(sc-9 AC-1)\nfunc F() int {\n\treturn 0\n}\n"
	d13Marker   = "\t// SC-STUB(sc-9 AC-1)\n"
)

func d13B2Bad() []d11Bad {
	g := func(name, base, head string) d11Bad {
		return d11Bad{name, "go", "package p\n\n" + base, "package p\n\n" + head + "\n" + d13GoStubFn, false}
	}
	iface := "type Fooer interface {\n\tFoo()\n}\n"
	obj := "type Obj struct {\n\tName string\n}\n"
	return []d11Bad{
		g("iface_method_unmarked", iface, "type Fooer interface {\n\tFoo()\n\tBar()\n}\n"),
		g("iface_method_marked", iface, "type Fooer interface {\n\tFoo()\n"+d13Marker+"\tBar()\n}\n"),
		g("iface_oneline_method_added", "type Fooer interface{ Foo() }\n", "type Fooer interface{ Foo(); Bar() }\n"),
		g("struct_embedded_unmarked", obj, "type Obj struct {\n\tName string\n\tstrings.Builder\n}\n"),
		g("struct_embedded_marked", obj, "type Obj struct {\n\tName string\n"+d13Marker+"\tstrings.Builder\n}\n"),
		g("struct_named_tagged_unmarked", obj, "type Obj struct {\n\tName string\n\tB int `json:\"b\"`\n}\n"),
		g("struct_named_tagged_marked", obj, "type Obj struct {\n\tName string\n"+d13Marker+"\tB int `json:\"b\"`\n}\n"),
		g("struct_named_unmarked", obj, "type Obj struct {\n\tName string\n\tB int\n}\n"),
		g("struct_oneline_repro", "type Obj struct{ Name string }\n", "type Obj struct{ Name string; strings.Builder }\n"),
		g("struct_marker_not_last_doc_line", obj, "type Obj struct {\n\tName string\n"+d13Marker+"\t// trailing doc\n\tB int\n}\n"),
		g("struct_field_marker_only_line_not_added", obj, "type Obj struct {\n\tName string\n\tB int // SC-STUB(sc-9 AC-1)\n}\n"),
		g("grouped_existing_spec_gains_method", "type (\n\tFooer interface {\n\t\tFoo()\n\t}\n)\n", "type (\n\tFooer interface {\n\t\tFoo()\n\t\tBar()\n\t}\n)\n"),
	}
}

// 22-AC-4 (D13 B2): added lines inside a Go TypeSpec that exists at base are malformed (each case also carries one
// well-formed marked zero-value func, so only the type edit can fail it).
func TestAnalyzeStubs_ExistingTypeSpecGainsLines_D13B2_22AC4(t *testing.T) { d13Bad(t, d13B2Bad()) }

func TestClassifyRed_ExistingTypeSpecGainsLines_D13B2_22AC4(t *testing.T) { d13BadRed(t, d13B2Bad()) }

// d13B2Twins: the phase-1 checker (lint/tdd fieldStubsOnly) accepts the marked-field twin and a new top-level
// type. The new spec inside an existing group is D13's explicit "stays benign" case (phase-1 rejects a group
// that is only partly added; see the handoff).
func d13B2Twins() []d11Bad {
	g := func(name, base, head string) d11Bad {
		return d11Bad{name, "go", "package p\n\n" + base, "package p\n\n" + head + "\n" + d13GoStubFn, false}
	}
	obj := "type Obj struct {\n\tName string\n}\n"
	return []d11Bad{
		g("twin_marked_untagged_named_field", obj, "type Obj struct {\n\tName string\n"+d13Marker+"\tB int\n}\n"),
		g("twin_marked_field_with_extra_doc_line", obj, "type Obj struct {\n\tName string\n\t// B is new.\n"+d13Marker+"\tB int\n}\n"),
		g("twin_new_spec_in_existing_group", "type (\n\tA struct{ X int }\n)\n", "type (\n\tA struct{ X int }\n\tB struct{ Y int }\n)\n"),
		g("twin_new_top_level_type", obj, obj+"\ntype Other struct {\n\tY int\n}\n"),
		g("twin_new_top_level_interface", obj, obj+"\ntype Doer interface {\n\tDo()\n}\n"),
	}
}

// 22-AC-4 (D13 B2 twins): these stay well-formed and red_valid.
func TestAnalyzeStubs_ExistingTypeSpecTwins_D13B2_22AC4(t *testing.T) { d13Good(t, d13B2Twins()) }

func d13N1Bad() []d11Bad {
	ts, rs := "typescript", "rust"
	rsStub := d11M(rs) + "pub fn f() -> u32 {\n    0\n}\n"
	tsStub := d11M(ts) + "export function f(): number {\n  return 0;\n}\n"
	return []d11Bad{
		{"rust_glob_import_shadowed_tuple_struct", rs, "use crate::imp::*;\npub fn g() { let _ = helper(5); }\n",
			"use crate::imp::*;\npub fn g() { let _ = helper(5); }\n\npub struct helper(pub u32);\n" + rsStub, false},
		{"rust_unit_struct_name_in_unchanged_code", rs, "pub fn g() { let _ = FLAG; }\n",
			"pub fn g() { let _ = FLAG; }\n\npub struct FLAG;\n" + rsStub, false},
		{"rust_enum_name_in_unchanged_code", rs, "pub fn g() { let _ = Mode::A; }\n",
			"pub fn g() { let _ = Mode::A; }\n\npub enum Mode { A, B }\n" + rsStub, false},
		{"rust_braced_struct_name_in_unchanged_code", rs, "pub fn g(s: Shape) {}\n",
			"pub fn g(s: Shape) {}\n\npub struct Shape { a: u32 }\n" + rsStub, false},
		{"ts_interface_name_in_unchanged_code", ts, "export const v: Shape = make();\n",
			"export const v: Shape = make();\n\ninterface Shape { a: number; }\n" + tsStub, false},
		{"ts_type_alias_name_in_unchanged_code", ts, "export const v: Alias = make();\n",
			"export const v: Alias = make();\n\ntype Alias = number;\n" + tsStub, false},
		{"ts_interface_block_name_in_unchanged_code", ts, "export const v: Shape = make();\n",
			"export const v: Shape = make();\n\ninterface Shape {\n  a: number;\n}\n" + tsStub, false},
	}
}

// 22-AC-4 (D13 N1): a type name present in unchanged code is not fresh, so the declaration is malformed.
func TestAnalyzeStubs_TypeNameNotFresh_D13N1_22AC4(t *testing.T) { d13Bad(t, d13N1Bad()) }

func TestClassifyRed_TypeNameNotFresh_D13N1_22AC4(t *testing.T) { d13BadRed(t, d13N1Bad()) }

func d13N1Twins() []d11Bad {
	ts, rs := "typescript", "rust"
	rsStub := d11M(rs) + "pub fn f() -> u32 {\n    0\n}\n"
	tsStub := d11M(ts) + "export function f(): number {\n  return 0;\n}\n"
	return []d11Bad{
		{"twin_rust_fresh_types", rs, "pub fn g() { let _ = FLAG; }\n",
			"pub fn g() { let _ = FLAG; }\n\npub struct Fresh;\npub struct Pair(pub u32);\npub enum Kind { A, B }\n" + rsStub, false},
		{"twin_ts_fresh_types", ts, "export const v: Shape = make();\n",
			"export const v: Shape = make();\n\ninterface Fresh { a: number; }\ntype FreshAlias = number;\n" + tsStub, false},
	}
}

// 22-AC-4 (D13 N1 twins): fresh type names stay well-formed and red_valid.
func TestAnalyzeStubs_FreshTypeNameTwins_D13N1_22AC4(t *testing.T) { d13Good(t, d13N1Twins()) }

func d13PyStub(sig string) string {
	return d11M("python") + "def f(" + sig + ":\n    return None\n"
}

func d13N3Bad() []d11Bad {
	py := "python"
	return []d11Bad{
		{"py_annotation_ternary", py, "", d13PyStub("a: x if y else z)"), false},
		{"py_annotation_in", py, "", d13PyStub("a: x in y)"), false},
		{"py_annotation_space_before_dot", py, "", d13PyStub("a: a .b)"), false},
		{"py_return_annotation_in", py, "", d11M(py) + "def f() -> x in y:\n    return None\n", false},
		{"py_annotation_space_after_dot", py, "", d13PyStub("a: a. b)"), false},
	}
}

// 22-AC-4 (D13 N3): Python annotations with spaces anywhere but around `|` and after `,` are not simple types.
func TestAnalyzeStubs_PythonAnnotationSpaces_D13N3_22AC4(t *testing.T) { d13Bad(t, d13N3Bad()) }

func TestClassifyRed_PythonAnnotationSpaces_D13N3_22AC4(t *testing.T) { d13BadRed(t, d13N3Bad()) }

func d13N3Twins() []d11Bad {
	py := "python"
	return []d11Bad{
		{"twin_py_path_type", py, "", d13PyStub("a: a.b)"), false},
		{"twin_py_union_none", py, "", d13PyStub("a: int | None)"), false},
		{"twin_py_dict", py, "", d13PyStub("a: dict[str, int])"), false},
		{"twin_py_return_union", py, "", d11M(py) + "def f() -> int | None:\n    return None\n", false},
	}
}

// 22-AC-4 (D13 N3 twins): path, union and generic annotations stay canonical.
func TestAnalyzeStubs_PythonAnnotationTwins_D13N3_22AC4(t *testing.T) { d13Good(t, d13N3Twins()) }
