package verify

import (
	"strings"
	"testing"
)

// S1 review round 3 (decision D11): for the line languages (Python, TypeScript/JavaScript, Rust) the stub check
// is an allowlist of canonical layouts. Anything else in an added hunk of a stub-class file is a malformed stub
// fact; a file with a line terminator other than \n or \r\n is an AnalyzeStubs error. Go stays AST based with the
// comment, `_`/`.` import and `import "C"` fixes. Fixtures are inline; every case has its own list.

const d11Marker = " SC-STUB(sc-9 AC-1)\n"

var d11Comment = map[string]string{"go": "//", "python": "#", "typescript": "//", "rust": "//"}
var d11Path = map[string]string{"go": "p/a.go", "python": "p/a.py", "typescript": "src/a.ts", "rust": "src/a.rs"}

// d11M is the marker line of the language.
func d11M(lang string) string { return d11Comment[lang] + d11Marker }

type d11Bad struct {
	name, lang string
	base, head string
	wantErr    bool // line-terminator cases (D11(a)): AnalyzeStubs must fail closed
}

func d11BadCases() []d11Bad {
	ts, rs, py, gom := "typescript", "rust", "python", d11M("go")
	tsm, rsm, pym := d11M(ts), d11M(rs), d11M(py)
	tsStub := tsm + "export function f(): number {\n  return 0;\n}\n"
	rsStub := rsm + "pub fn f() -> u32 {\n    0\n}\n"
	pyStub := pym + "def f():\n    return None\n"
	goStub := gom + "func F() int {\n\treturn 0\n}\n"
	return []d11Bad{
		// code on the stub's own line / after the closing brace / braces in params
		{"ts_code_after_close", ts, "", tsm + "export function f(): number { return 0 } globalThis.fetch = () => 42;\n", false},
		{"ts_param_braces_oneline", ts, "", tsm + "export function f(o: {}) { return real(o); }\n", false},
		{"ts_default_param_call", ts, "", tsm + "export function f(a = evil()) {\n  return 0;\n}\n", false},
		{"ts_default_param_literal", ts, "", tsm + "export function f(a = 0): number {\n  return 0;\n}\n", false},
		{"ts_arrow_in_params", ts, "", tsm + "export function f(cb: () => void): number {\n  return 0;\n}\n", false},
		{"rust_code_after_close", rs, "", rsm + "pub fn f() -> u32 { 0 } pub fn helper() -> u32 { 42 }\n", false},
		{"rust_param_braces_oneline", rs, "", rsm + "pub fn f(o: [u8; 1]) -> u32 { real(o) }\n", false},
		// python one-line bodies and signature defaults / calls
		{"py_oneline_def_call", py, "", pym + "def f(): return real()\n", false},
		{"py_oneline_def_semi", py, "", pym + "def f(): pass; evil()\n", false},
		{"py_default_arg_call", py, "", pym + "def f(a=evil()):\n    return None\n", false},
		{"py_default_arg_literal", py, "", pym + "def f(a=1):\n    return None\n", false},
		{"py_annotation_call", py, "", pym + "def f(a: evil()) -> None:\n    return None\n", false},
		{"py_decorator_above_marker", py, "", "@evil\n" + pyStub, false},
		{"py_decorator_below_marker", py, "", pym + "@evil\ndef f():\n    return None\n", false},
		{"py_nested_def", py, "", pym + "def f():\n    def g():\n        evil()\n    return None\n", false},
		{"py_docstring_body", py, "", pym + "def f():\n    \"\"\"doc\"\"\"\n    return None\n", false},
		{"py_code_after_dedent_tabs", py, "", pym + "def f():\n\treturn None\nevil()\n", false},
		// block comments wrapping code, benign-looking first characters
		{"ts_blockcomment_wrap", ts, "", "/* a */ globalThis.fetch = () => 42; /* b */\n" + tsStub, false},
		{"rust_blockcomment_wrap", rs, "", "/* a */ pub fn helper() -> u32 { 42 } /* b */\n" + rsStub, false},
		{"ts_interface_then_code", ts, "", "interface I {} function helper() { return 42 }\n" + tsStub, false},
		{"rust_struct_then_impl", rs, "", "pub struct S {} impl S { pub fn real() -> u32 { 42 } }\n" + rsStub, false},
		{"rust_attr_then_fn", rs, "", "#[inline] pub fn helper() -> u32 { 42 }\n" + rsStub, false},
		{"rust_attr_then_mod", rs, "", "#[cfg(test)] mod m { pub fn helper() -> u32 { 42 } }\n" + rsStub, false},
		{"rust_attr_between_marker_and_fn", rs, "", rsm + "#[inline]\npub fn f() -> u32 {\n    0\n}\n", false},
		{"rust_macro_rules", rs, "", "macro_rules! m { () => { 42 } }\n" + rsStub, false},
		{"ts_overload", ts, "", tsm + "export function f(a: string): string;\nexport function f(a: any): any {\n  return null;\n}\n", false},
		{"ts_body_calls", ts, "", tsm + "export function f(): number {\n  return g();\n}\n", false},
		{"ts_unicode_code", ts, "", "const ä = 1;\nglobalThis.ö = () => 42;\n" + tsStub, false},
		{"ts_crlf_code", ts, "", "globalThis.x = 1;\r\n// SC-STUB(sc-9 AC-1)\r\nexport function f(): number {\r\n  return 0;\r\n}\r\n", false},
		// line terminators other than \n and \r\n (D11(a))
		{"py_bare_cr_comment", py, "", "# note\revil()\n" + pyStub, true},
		{"py_bare_cr_import", py, "", "import os\revil()\n" + pyStub, true},
		{"ts_ls_comment", ts, "", "// note globalThis.fetch = () => 42;\n" + tsStub, true},
		{"ts_cr_comment", ts, "", "// note\rglobalThis.fetch = () => 42;\n" + tsStub, true},
		{"rust_ls_comment", rs, "", "// note pub fn helper() -> u32 { 42 }\n" + rsStub, true},
		// additions inside a declaration that already exists at base (D11(d))
		{"py_existing_decl_extended", py, "X = make(\n    1,\n)\n", "X = make(\n    1,\n    evil(),\n)\n\n" + pyStub, false},
		{"ts_existing_decl_extended", ts, "export const X = make({\n  a: 1,\n});\n", "export const X = make({\n  a: 1,\n  b: evil(),\n});\n\n" + tsStub, false},
		{"rust_existing_static_extended", rs, "pub static X: [u32; 2] = [\n    1,\n    2];\n", "pub static X: [u32; 2] = [\n    1,\n    42];\n\n" + rsStub, false},
		{"py_existing_func_body_extended", py, "def g():\n    return 1\n", "def g():\n    evil()\n    return 1\n\n" + pyStub, false},
		// Go: comment-position pass, `_`/`.` imports hidden behind a comment, cgo preamble
		{"go_blockcomment_blank_import", "go", "", "package p\n\n/* x */ import _ \"net/http/pprof\"\n\n" + goStub, false},
		{"go_blockcomment_blank_import_group", "go", "", "package p\n\nimport (\n\t/* x */ _ \"net/http/pprof\"\n)\n\n" + goStub, false},
		{"go_cgo_preamble", "go", "", "package p\n\n/*\nstatic void __attribute__((constructor)) evil(void) { }\n*/\nimport \"C\"\n\n" + goStub, false},
		{"go_init_dup", "go", "package p\n\nfunc init() {}\n", "package p\n\nfunc init() {}\n\nfunc init() { evil() }\n\n" + goStub, false},
	}
}

func d11File(c d11Bad) StubFile {
	f := StubFile{Path: d11Path[c.lang], Language: c.lang, Head: []byte(c.head)}
	if c.base != "" {
		f.Base = []byte(c.base)
	}
	return f
}

func d11Analyze(t testing.TB, f StubFile) ([]StubFact, int, error) {
	t.Helper()
	sp, err := LoadStubPolicy(platformPolicy(t).Verification)
	if err != nil {
		t.Fatalf("LoadStubPolicy: %v", err)
	}
	facts, err := AnalyzeStubs(sp, f)
	n := 0
	for _, x := range facts {
		if !x.WellFormed {
			n++
		}
	}
	return facts, n, err
}

// d11RedInput builds a full RED input around analysed facts; the stub-class file is the only non-test change.
func d11RedInput(t testing.TB, f StubFile, facts []StubFact) RedInput {
	t.Helper()
	in := redInputOf(t, d9BaseFixture(t))
	in.Scope.Stubs = facts
	in.Scope.ChangedFiles = []ChangedFile{
		{Path: d9TestFile, Change: "added", Class: "test"},
		{Path: f.Path, Change: "modified", Class: "stub"},
	}
	return in
}

// 22-AC-4 (D11): every non-canonical layout in an added hunk is a malformed fact (or an error); line terminators
// other than \n / \r\n are an error.
func TestAnalyzeStubs_NonCanonicalLayouts_D11_22AC4(t *testing.T) {
	for _, c := range d11BadCases() {
		t.Run(c.name, func(t *testing.T) {
			facts, malformed, err := d11Analyze(t, d11File(c))
			switch {
			case c.wantErr:
				if err == nil {
					t.Errorf("a file with a foreign line terminator was analysed: facts = %s, want an error", jsonOf(facts))
				}
			case err != nil:
				// failing closed with an error is also acceptable for a non-canonical layout
			case malformed < 1:
				t.Errorf("non-canonical layout accepted: facts = %s, want >= 1 malformed fact", jsonOf(facts))
			}
		})
	}
}

// 22-AC-4 (D11): ClassifyRed never returns red_valid for them; where analysis succeeded the outcome is
// red_invalid with stub_malformed.
func TestClassifyRed_NonCanonicalLayouts_D11_22AC4(t *testing.T) {
	for _, c := range d11BadCases() {
		if c.wantErr {
			continue
		}
		t.Run(c.name, func(t *testing.T) {
			f := d11File(c)
			facts, _, err := d11Analyze(t, f)
			if err != nil {
				return
			}
			got, err := ClassifyRed(d11RedInput(t, f, facts))
			if err != nil {
				t.Fatalf("ClassifyRed: %v", err)
			}
			if got.Outcome != "red_invalid" || !d9HasReason(got, "stub_malformed") {
				t.Errorf("classification = %s %v, want red_invalid with stub_malformed", got.Outcome, got.Reasons)
			}
		})
	}
}

// 22-AC-4 (D11(a)): each foreign terminator (bare CR, U+2028, U+2029, form feed, vertical tab, NEL) in a comment
// of an otherwise canonical file is an AnalyzeStubs error for every line language; the twin without it is fine.
func TestAnalyzeStubs_ForeignLineTerminators_D11a_22AC4(t *testing.T) {
	terms := map[string]string{"cr": "\r", "ls": " ", "ps": " ", "ff": "\f", "vt": "\v", "nel": "\u0085"}
	stubs := map[string]string{
		"python":     "def f():\n    return None\n",
		"typescript": "export function f(): number {\n  return 0;\n}\n",
		"rust":       "pub fn f() -> u32 {\n    0\n}\n",
	}
	for lang, stub := range stubs {
		for tn, term := range terms {
			t.Run(lang+"_"+tn, func(t *testing.T) {
				head := d11Comment[lang] + " note" + term + "more\n" + d11M(lang) + stub
				_, _, err := d11Analyze(t, StubFile{Path: d11Path[lang], Language: lang, Head: []byte(head)})
				if err == nil {
					t.Errorf("%s terminator was accepted, want an AnalyzeStubs error", tn)
				}
			})
		}
		t.Run(lang+"_twin_lf_and_crlf", func(t *testing.T) {
			head := d11Comment[lang] + " note\n" + d11M(lang) + stub
			for _, h := range []string{head, strings.ReplaceAll(head, "\n", "\r\n")} {
				facts, malformed, err := d11Analyze(t, StubFile{Path: d11Path[lang], Language: lang, Head: []byte(h)})
				if err != nil || malformed != 0 || len(facts) != 1 {
					t.Errorf("canonical file: facts = %s err = %v, want one well-formed fact", jsonOf(facts), err)
				}
			}
		})
	}
}

type d11Twin struct {
	name, lang string
	pre, stub  string // pre: benign lines before the marker; stub: marker line + declaration + body (LF)
	post       string
	crlf       bool
	shape      string // expected shape of the one fact
}

func d11Twins() []d11Twin {
	py, ts, rs := "python", "typescript", "rust"
	pym, tsm, rsm := d11M(py), d11M(ts), d11M(rs)
	return []d11Twin{
		// Python
		{"py_three_line", py, "", pym + "def replay():\n    return None\n", "", false, "zero_value"},
		{"py_pass_body", py, "", pym + "def replay():\n    pass\n", "", false, "zero_value"},
		{"py_param_plain", py, "", pym + "def replay(a):\n    return None\n", "", false, "zero_value"},
		{"py_params_typed", py, "", pym + "def replay(a: int, b: str) -> int:\n    return 0\n", "", false, "zero_value"},
		{"py_panic", py, "", pym + "def replay():\n    raise NotImplementedError(\"SC-STUB-NOT-IMPLEMENTED\")\n", "", false, "panic"},
		{"py_imports_comments", py, "import os\nimport a.b\nfrom a.b import c, d\n\n# a note\n", pym + "def replay():\n    return None\n", "\n# trailing\n", false, "zero_value"},
		{"py_crlf", py, "import os\n# a note\n", pym + "def replay():\n    return None\n", "", true, "zero_value"},
		// TypeScript
		{"ts_three_line", ts, "", tsm + "export function replay(): number {\n  return 0;\n}\n", "", false, "zero_value"},
		{"ts_one_line", ts, "", tsm + "export function replay(): number { return 0; }\n", "", false, "zero_value"},
		{"ts_param_plain", ts, "", tsm + "export function replay(a): number {\n  return 0;\n}\n", "", false, "zero_value"},
		{"ts_param_typed", ts, "", tsm + "export function replay(a: number): number {\n  return 0;\n}\n", "", false, "zero_value"},
		{"ts_panic", ts, "", tsm + "export function replay(): number {\n  throw new Error(\"SC-STUB-NOT-IMPLEMENTED\");\n}\n", "", false, "panic"},
		{"ts_panic_one_line", ts, "", tsm + "export function replay(): number { throw new Error(\"SC-STUB-NOT-IMPLEMENTED\"); }\n", "", false, "panic"},
		{"ts_imports_comments", ts, "import { a, b } from \"x\";\nimport x from \"x\";\n\n// a note\n", tsm + "export function replay(): number {\n  return 0;\n}\n", "\n// trailing\n", false, "zero_value"},
		{"ts_pure_types", ts, "type X = number;\ninterface I { a: number; b: string; }\n", tsm + "export function replay(): number {\n  return 0;\n}\n", "interface J {\n  a: number;\n  b: string;\n}\n", false, "zero_value"},
		{"ts_crlf", ts, "// x\n", tsm + "export function replay(): number {\n  return 0;\n}\n", "", true, "zero_value"},
		// Rust
		{"rust_three_line", rs, "", rsm + "pub fn replay() -> u32 {\n    0\n}\n", "", false, "zero_value"},
		{"rust_one_line", rs, "", rsm + "pub fn replay() -> u32 { 0 }\n", "", false, "zero_value"},
		{"rust_param_typed", rs, "", rsm + "pub fn replay(a: &str) -> u32 {\n    0\n}\n", "", false, "zero_value"},
		{"rust_panic", rs, "", rsm + "pub fn replay() -> u32 {\n    todo!(\"SC-STUB-NOT-IMPLEMENTED\")\n}\n", "", false, "panic"},
		{"rust_panic_one_line", rs, "", rsm + "pub fn replay() -> u32 { todo!(\"SC-STUB-NOT-IMPLEMENTED\") }\n", "", false, "panic"},
		{"rust_uses_comments", rs, "use a::b;\nuse a::{b, c};\n\n// a note\n", rsm + "pub fn replay() -> u32 {\n    0\n}\n", "\n// trailing\n", false, "zero_value"},
		{"rust_pure_types", rs, "pub struct S { a: u32, b: String, }\npub struct U;\npub struct T(u32);\npub enum E { A, B }\n", rsm + "pub fn replay() -> u32 {\n    0\n}\n", "pub struct B {\n    a: u32,\n    b: String,\n}\n", false, "zero_value"},
		{"rust_crlf", rs, "// x\n", rsm + "pub fn replay() -> u32 {\n    0\n}\n", "", true, "zero_value"},
	}
}

func d11TwinFile(c d11Twin) StubFile {
	l := d10Langs[c.lang]
	head := l.base + "\n" + c.pre + c.stub + "\n" + c.post
	base := l.base
	if c.crlf {
		head = strings.ReplaceAll(head, "\n", "\r\n")
		base = strings.ReplaceAll(base, "\n", "\r\n")
	}
	return StubFile{Path: l.path, Language: l.lang, Base: []byte(base), Head: []byte(head)}
}

// 22-AC-4 (D11(b)/(c)): the canonical layouts (three-line and one-line stubs, panic form, plain/typed params,
// imports, comments, pure types, CRLF) stay exactly one well-formed fact of the expected shape.
func TestAnalyzeStubs_CanonicalLayouts_D11_22AC4(t *testing.T) {
	for _, c := range d11Twins() {
		t.Run(c.name, func(t *testing.T) {
			facts, malformed, err := d11Analyze(t, d11TwinFile(c))
			if err != nil {
				t.Fatalf("AnalyzeStubs: %v", err)
			}
			if malformed != 0 || len(facts) != 1 || facts[0].Shape != c.shape || !facts[0].WellFormed {
				t.Errorf("facts = %s, want exactly one well-formed %s fact", jsonOf(facts), c.shape)
			}
		})
	}
}

// 22-AC-4 (D11, SPEC 50): the canonical stubs classify red_valid; a Rust panic stub is red_valid, a Python or
// TypeScript panic stub is red_invalid / panic_stub_not_allowed.
func TestClassifyRed_CanonicalLayouts_D11_22AC4(t *testing.T) {
	for _, c := range d11Twins() {
		t.Run(c.name, func(t *testing.T) {
			f := d11TwinFile(c)
			facts, _, err := d11Analyze(t, f)
			if err != nil {
				t.Fatalf("AnalyzeStubs: %v", err)
			}
			got, err := ClassifyRed(d11RedInput(t, f, facts))
			if err != nil {
				t.Fatalf("ClassifyRed: %v", err)
			}
			if c.shape == "panic" && c.lang != "rust" {
				if got.Outcome != "red_invalid" || !d9HasReason(got, "panic_stub_not_allowed") {
					t.Errorf("classification = %s %v, want red_invalid with panic_stub_not_allowed", got.Outcome, got.Reasons)
				}
				return
			}
			if got.Outcome != "red_valid" || len(got.Reasons) != 0 {
				t.Errorf("classification = %s %v, want red_valid", got.Outcome, got.Reasons)
			}
		})
	}
}

// 22-AC-4 (D11(d) twin): an existing multi-line declaration left untouched, with the new stub added after it,
// is one well-formed fact (the widened skip range covers only the unchanged declaration).
func TestAnalyzeStubs_UnchangedExistingDeclarationTwin_D11d_22AC4(t *testing.T) {
	cases := []d11Bad{
		{"py_existing_decl_unchanged", "python", "X = make(\n    1,\n)\n", "X = make(\n    1,\n)\n\n" + d11M("python") + "def f():\n    return None\n", false},
		{"ts_existing_decl_unchanged", "typescript", "export const X = make({\n  a: 1,\n});\n", "export const X = make({\n  a: 1,\n});\n\n" + d11M("typescript") + "export function f(): number {\n  return 0;\n}\n", false},
		{"rust_existing_static_unchanged", "rust", "pub static X: [u32; 2] = [\n    1,\n    2];\n", "pub static X: [u32; 2] = [\n    1,\n    2];\n\n" + d11M("rust") + "pub fn f() -> u32 {\n    0\n}\n", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			facts, malformed, err := d11Analyze(t, d11File(c))
			if err != nil || malformed != 0 || len(facts) != 1 {
				t.Errorf("facts = %s err = %v, want exactly one well-formed fact", jsonOf(facts), err)
			}
		})
	}
}
