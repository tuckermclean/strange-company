package verify

import (
	"testing"
)

// Decision D19 (S3 review 2): every semantic-key and discovery recogniser (go.mod, TOML, INI, JSON, the Makefile
// walk) rejects as an error, counted as changed as in D18, a file containing a line terminator other than "\n" or
// "\r\n" (a lone CR, VT, FF, U+001C..U+001E, U+0085, U+2028, U+2029) or a NUL; an agent-added gitlink at a
// semantic-key path is GITLINK_DENIED and never an ingestion error; and the canonical subset is tightened (TOML
// comments with control characters, a TOML table defined twice, go.mod backquoted strings and /* */).
//
// Every input uses Go escapes, so this source file holds no raw control character. Several inputs are outside the
// grammar of their format on purpose; nothing asserts they are valid.
//
// Real-tool evidence (recorded when this file was written, iniconfig 2.3.1, Python 3.13.5 tomllib and json, go
// 1.27.2, GNU Make 4.4.1):
//   - iniconfig splits lines like str.splitlines, so with every separator below (lone CR, VT, FF, FS, GS, RS,
//     U+0085, U+2028, U+2029) and with a NUL inside a value it reads the hidden [tool:pytest] section;
//   - tomllib does NOT honour any of these as a newline (it rejects "Expected newline or end of document") and
//     rejects a control character in a comment and a table declared twice ("Cannot declare ('a',) twice"), but
//     accepts [a.b] followed by [a];
//   - `go mod edit -json` does not honour a lone CR as a newline either (it is whitespace, so the following
//     directive lands on the same line and the file is a usage error); it rejects a backquoted module path
//     ("unquoted string cannot contain quote") and /* */ ("modfiles must use // comments");
//   - GNU Make reads a line that starts with CR before `-include x.mk` as an include (CR is leading whitespace),
//     and does not treat a CR inside a line as a terminator;
//   - Python json and Go encoding/json reject U+2028 or NUL between tokens, so a JSON evasion needs no terminator,
//     but D19 says error regardless.
// Where the tool does not honour a separator, the case stays: D19 says error for the file either way.

// d19Seps are the line terminators D19 forbids (everything but "\n" and "\r\n"), by subtest name.
var d19Seps = []struct{ name, sep string }{
	{"lone_CR", "\r"},
	{"VT", "\v"},
	{"FF", "\f"},
	{"U001C", "\x1c"},
	{"U001D", "\x1d"},
	{"U001E", "\x1e"},
	{"U0085", "\u0085"},
	{"U2028", " "},
	{"U2029", " "},
}

// 22-AC-8 (D19 a): INI. The hidden [tool:pytest] section behind each separator is read by pytest (iniconfig), so
// the file is an error and counts as changed. Twins: CRLF throughout with equal semantic values stays clean.
func TestCheckIngestion_INISeparators_D19_22AC8(t *testing.T) {
	disc := tamper("discovery_config_changed", "setup.cfg")
	setup := func(s string) map[string]string { return map[string]string{"setup.cfg": s} }
	const base = "[metadata]\nname = x\n"
	var cases []d18Case
	for _, s := range d19Seps {
		cases = append(cases, d18Case{name: "hidden_pytest_section_" + s.name, base: setup(base),
			head: setup("[metadata]\nname = x" + s.sep + "[tool:pytest]" + s.sep + "addopts = -p evil\n"), want: []string{disc}})
	}
	cases = append(cases,
		d18Case{name: "hidden_pytest_section_NUL", base: setup(base),
			head: setup("[metadata]\nname = x\x00\n[tool:pytest]\naddopts = -p evil\n"), want: []string{disc}},
		d18Case{name: "NUL_in_otherwise_equal_file", base: setup(base),
			head: setup("[metadata]\nname = x\x00\n"), want: []string{disc}},
		d18Case{name: "separator_in_otherwise_equal_file", base: setup(base),
			head: setup("[metadata]\r" + "name = x\n"), want: []string{disc}},
		d18Case{name: "twin_crlf_throughout_equal_values", base: setup(base),
			head: setup("[metadata]\r\nname = x\r\n")},
		d18Case{name: "twin_crlf_throughout_other_section_added", base: setup(base),
			head: setup("[metadata]\r\nname = x\r\n\r\n[flake8]\r\nmax-line-length = 100\r\n")},
		d18Case{name: "crlf_with_pytest_section_is_changed", base: setup(base),
			head: setup("[metadata]\r\nname = x\r\n[tool:pytest]\r\naddopts = -p evil\r\n"), want: []string{disc}},
	)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { c.run(t, false) })
	}
}

// 22-AC-8 (D19 a): pyproject.toml and package.json. A lone CR hides [tool.pytest.ini_options] from a reader that
// splits on it; a NUL or U+2028 outside a JSON string is an error. Twins: CRLF throughout stays clean.
func TestCheckIngestion_TOMLAndJSONSeparators_D19_22AC8(t *testing.T) {
	pyDisc := tamper("discovery_config_changed", "pyproject.toml")
	pyDep := tamper("test_dependency_changed", "pyproject.toml")
	pkgDisc := tamper("discovery_config_changed", "package.json")
	pkgDep := tamper("test_dependency_changed", "package.json")
	py := func(s string) map[string]string { return map[string]string{"pyproject.toml": s} }
	pkg := func(s string) map[string]string { return map[string]string{"package.json": s} }
	const pyBase = "[project]\nname = \"x\"\n"
	const pkgBase = "{\n  \"name\": \"demo\",\n  \"scripts\": {\"test\": \"go test ./...\"}\n}\n"
	cases := []d18Case{
		{name: "toml_lone_CR_hides_pytest_table", base: py(pyBase),
			head: py("[project]\nname = \"x\"\r[tool.pytest.ini_options]\raddopts = \"-p evil\"\n"), want: []string{pyDisc}, allow: []string{pyDep}},
		{name: "toml_U2028_hides_pytest_table", base: py(pyBase),
			head: py("[project]\nname = \"x\" [tool.pytest.ini_options] addopts = \"-p evil\"\n"), want: []string{pyDisc}, allow: []string{pyDep}},
		{name: "toml_NUL_in_otherwise_equal_file", base: py(pyBase),
			head: py("[project]\nname = \"x\"\x00\n"), want: []string{pyDisc}, allow: []string{pyDep}},
		{name: "toml_separator_in_otherwise_equal_file", base: py(pyBase),
			head: py("[project]\r" + "name = \"x\"\n"), want: []string{pyDisc}, allow: []string{pyDep}},
		{name: "twin_toml_crlf_throughout_equal_values", base: py(pyBase),
			head: py("[project]\r\nname = \"x\"\r\n")},
		{name: "json_U2028_outside_a_string", base: pkg(pkgBase),
			head: pkg("{\n  \"name\": \"demo\", \"scripts\": {\"test\": \"echo\"}\n}\n"), want: []string{pkgDisc}, allow: []string{pkgDep}},
		{name: "json_NUL_outside_a_string", base: pkg(pkgBase),
			head: pkg("{\x00\n  \"name\": \"demo\",\n  \"scripts\": {\"test\": \"go test ./...\"}\n}\n"), want: []string{pkgDisc}, allow: []string{pkgDep}},
		{name: "json_lone_CR_between_tokens", base: pkg(pkgBase),
			head: pkg("{\r  \"name\": \"demo\",\r  \"scripts\": {\"test\": \"go test ./...\"}\r}\n"), want: []string{pkgDisc}, allow: []string{pkgDep}},
		// Stricter reading of "any file containing": U+2028 is legal inside a JSON string, but the file is rejected.
		{name: "json_U2028_inside_a_string_is_still_rejected", base: pkg(pkgBase),
			head: pkg("{\n  \"name\": \"de mo\",\n  \"scripts\": {\"test\": \"go test ./...\"}\n}\n"), want: []string{pkgDisc}, allow: []string{pkgDep}},
		{name: "twin_json_crlf_throughout_equal_values", base: pkg(pkgBase),
			head: pkg("{\r\n  \"name\": \"demo\",\r\n  \"scripts\": {\"test\": \"go test ./...\"}\r\n}\r\n")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { c.run(t, false) })
	}
}

// 22-AC-22 (D19 a): go.mod. A lone CR hides a replace from a reader that splits on it (the real go command reads
// it as whitespace and fails), and a NUL or any other terminator is an error that counts as changed at the head.
// Twin: CRLF throughout with equal content stays clean.
func TestCheckIngestion_GoModSeparators_D19_22AC22(t *testing.T) {
	testDep := tamper("test_dependency_changed", "go.mod")
	disc := tamper("discovery_config_changed", "go.mod")
	gomod := func(s string) map[string]string { return map[string]string{"go.mod": s} }
	var cases []d18Case
	for _, s := range d19Seps {
		cases = append(cases, d18Case{name: "hidden_replace_" + s.name, base: gomod(d18GoModBase),
			head: gomod("module example.com/m\n\ngo 1.22" + s.sep + "replace github.com/stretchr/testify => ./x\n"),
			want: []string{testDep}, allow: []string{disc}})
	}
	cases = append(cases,
		d18Case{name: "NUL_in_otherwise_equal_file", base: gomod(d18GoModBase),
			head: gomod("module example.com/m\x00\n\ngo 1.22\n"), want: []string{testDep}, allow: []string{disc}},
		d18Case{name: "twin_crlf_throughout_equal_content", base: gomod(d18GoModBase),
			head: gomod("module example.com/m\r\n\r\ngo 1.22\r\n")},
	)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { c.run(t, false) })
	}
}

// 22-AC-32 (D19 a): the Makefile walk. GNU Make reads a line that starts with CR before `-include x.mk` as an
// include, so a protected Makefile containing a lone CR (or any other forbidden separator, or a NUL) is an error
// in the derivation (D17 5: fail closed). Twins: the same include on its own LF or CRLF line protects x.mk.
func TestDeriveDiscoveryProtection_MakeSeparators_D19_22AC32(t *testing.T) {
	const mk = "test:\n\tgo test ./...\n\nlint:\n\ttrue\n"
	type tc struct {
		name     string
		makefile string
		wantErr  bool
	}
	cases := []tc{
		{name: "lone_CR_before_include_line", makefile: mk + "\r-include x.mk\n", wantErr: true},
		{name: "lone_CR_between_assignment_and_include", makefile: "X = 1\r-include x.mk\n" + mk, wantErr: true},
		{name: "NUL_before_include_line", makefile: mk + "\x00-include x.mk\n", wantErr: true},
		{name: "NUL_in_otherwise_plain_makefile", makefile: mk + "# c\x00\n", wantErr: true},
		{name: "twin_include_on_own_lf_line", makefile: mk + "-include x.mk\n"},
		{name: "twin_include_on_own_crlf_line", makefile: "test:\r\n\tgo test ./...\r\n\r\nlint:\r\n\ttrue\r\n-include x.mk\r\n"},
	}
	for _, s := range d19Seps[1:] {
		cases = append(cases, tc{name: "separator_" + s.name + "_before_include_line", makefile: mk + s.sep + "-include x.mk\n", wantErr: true})
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dp, _, _, err := d18Derive(t, map[string]string{"Makefile": c.makefile}, d18Validation(d18SuiteArgv, d18CheckArgv, ""))
			if c.wantErr {
				if err == nil {
					t.Fatalf("accepted: whole files %+v", dp.WholeFiles)
				}
				return
			}
			if err != nil {
				t.Fatalf("a plain include was rejected: %v", err)
			}
			if present, blob := d18WholeFile(dp, "x.mk"); !present || blob != nil {
				t.Fatalf("x.mk: present=%v blob=%v, want a protected nil blob", present, blob)
			}
		})
	}
}

// d19GitlinkIngest commits a base, snapshots the discovery protection at red, adds a gitlink at path in the head
// commit and runs CheckIngestion.
func d19GitlinkIngest(t *testing.T, path string, withValidation bool) ([]string, error) {
	t.Helper()
	s, red := s3DiscoveryRepo(t)
	dp, err := s.derive(t, red, s3ValidationYAML)
	if err != nil {
		t.Fatalf("the base repository must derive at red: %v", err)
	}
	s.setGitlink(path, s3Gitlink)
	h := s.commit("impl")
	man := s.s3Manifest(red, "pkg/core_test.go")
	man.DiscoveryProtection = dp
	in := s.ingestionInput(t, "implementer", red, h, man)
	if withValidation {
		in.Validation = s3Validation(t, s3ValidationYAML)
	}
	got, err := CheckIngestion(in)
	return violationKeys(got), err
}

// 22-AC-8 (D19 b, N1): a gitlink the agent adds at a semantic-key path is GITLINK_DENIED and nothing else: no
// ingestion error (a gitlink has no blob to parse), with Validation set or nil. Twin: a gitlink at an ordinary path.
func TestCheckIngestion_GitlinkAtSemanticKeyPath_D19_22AC8(t *testing.T) {
	for _, path := range []string{"sub/package.json", "sub/go.mod", "sub/setup.cfg", "sub/pyproject.toml", "sub/other"} {
		for _, withValidation := range []bool{true, false} {
			name := "validation_nil_"
			if withValidation {
				name = "validation_set_"
			}
			t.Run(name+path, func(t *testing.T) {
				got, err := d19GitlinkIngest(t, path, withValidation)
				if err != nil {
					t.Fatalf("CheckIngestion: %v (violations %q)", err, got)
				}
				if want := []string{"GITLINK_DENIED " + path}; !equalStrings(got, want) {
					t.Fatalf("got %q, want %q", got, want)
				}
			})
		}
	}
}

// 22-AC-8 (D19 c): the canonical TOML subset. A comment with a control character (TOML allows only tab) and a
// table header defined twice are errors that count as changed. Twins: a tab or non-ASCII text in a comment, a
// distinct new table, and [a.b] followed by [a] (valid TOML).
func TestCheckIngestion_TOMLCanonicalSubset_D19_22AC8(t *testing.T) {
	disc := tamper("discovery_config_changed", "pyproject.toml")
	dep := tamper("test_dependency_changed", "pyproject.toml")
	py := func(s string) map[string]string { return map[string]string{"pyproject.toml": s} }
	cases := []d18Case{
		{name: "comment_with_SOH_in_protected_file", base: py(d18PyPytest),
			head: py("[tool.black]\nx = 1 # note\x01\n\n[tool.pytest.ini_options]\naddopts = \"-q\"\n"), want: []string{disc}, allow: []string{dep}},
		{name: "comment_with_DEL", base: py(d18PyPytest),
			head: py("# note\x7f\n" + d18PyPytest), want: []string{disc}, allow: []string{dep}},
		{name: "comment_with_control_char_on_own_line", base: py(d18PyBase),
			head: py(d18PyBase + "# c\x1b[31m\n"), want: []string{disc}, allow: []string{dep}},
		{name: "twin_comment_with_tab_and_non_ascii", base: py(d18PyPytest),
			head: py("[tool.black]\nx = 1 # note\t\u00e9\n\n[tool.pytest.ini_options]\naddopts = \"-q\"\n")},
		{name: "table_header_defined_twice", base: py(d18PyPytest),
			head: py(d18PyPytest + "\n[tool.black]\ny = 2\n"), want: []string{disc}, allow: []string{dep}},
		{name: "pytest_table_defined_twice_equal_first", base: py(d18PyPytest),
			head: py(d18PyPytest + "\n[tool.pytest.ini_options]\nmarkers = [\"x\"]\n"), want: []string{disc}, allow: []string{dep}},
		{name: "twin_distinct_new_table", base: py(d18PyBase),
			head: py(d18PyBase + "\n[tool.ruff]\nline-length = 100\n")},
		{name: "twin_sub_table_then_super_table", base: py(d18PyBase),
			head: py(d18PyBase + "\n[tool.ruff.lint]\nx = 1\n\n[tool.ruff]\nline-length = 100\n")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { c.run(t, false) })
	}
}

// 22-AC-22 (D19 c): the canonical go.mod subset. A backquoted string and a /* */ comment are errors that count as
// changed at the head (the real go command rejects both). Twins: a quoted path and a // comment.
func TestCheckIngestion_GoModCanonicalSubset_D19_22AC22(t *testing.T) {
	testDep := tamper("test_dependency_changed", "go.mod")
	disc := tamper("discovery_config_changed", "go.mod")
	gomod := func(s string) map[string]string { return map[string]string{"go.mod": s} }
	cases := []d18Case{
		{name: "backquoted_replace_path", base: gomod(d18GoModBase),
			head: gomod(d18GoModBase + "\nreplace `github.com/stretchr/testify` => ./x\n"), want: []string{testDep}, allow: []string{disc}},
		{name: "backquoted_module_path", base: gomod(d18GoModBase),
			head: gomod("module `example.com/m`\n\ngo 1.22\n"), want: []string{testDep}, allow: []string{disc}},
		{name: "backquoted_path_in_block", base: gomod(d18GoModBase),
			head: gomod(d18GoModBase + "\nreplace (\n\t`github.com/stretchr/testify` => `./x`\n)\n"), want: []string{testDep}, allow: []string{disc}},
		{name: "block_comment_on_its_own_line", base: gomod(d18GoModBase),
			head: gomod(d18GoModBase + "\n/* c */\n"), want: []string{testDep}, allow: []string{disc}},
		{name: "block_comment_hides_replace", base: gomod(d18GoModBase),
			head: gomod(d18GoModBase + "\n/* c\nreplace github.com/stretchr/testify => ./x\n*/\n"), want: []string{testDep}, allow: []string{disc}},
		{name: "block_comment_after_directive", base: gomod(d18GoModBase),
			head: gomod("module example.com/m /* c */\n\ngo 1.22\n"), want: []string{testDep}, allow: []string{disc}},
		{name: "twin_quoted_replace_path", base: gomod(d18GoModBase),
			head: gomod(d18GoModBase + "\nreplace \"github.com/stretchr/testify\" => \"./x\"\n"), want: []string{testDep}, allow: []string{disc}},
		{name: "twin_line_comment_only", base: gomod(d18GoModBase),
			head: gomod(d18GoModBase + "\n// c /* not a block */\n")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { c.run(t, false) })
	}
}
