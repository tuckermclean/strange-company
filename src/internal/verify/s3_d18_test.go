package verify

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Decision D18 (S3 review 1): the semantic-key and discovery parsers are fail-closed recognisers of a canonical
// subset, not best-effort readers. Every input below is a plausible file of its kind; where an input is
// intentionally outside its format's grammar (a go.mod with an unknown directive or a stray ")", a Makefile with
// `load`) the case is the point of the test, and nothing asserts that the input is valid.
//
// Real-tool evidence (recorded when this file was written, GNU Make 4.4.1, go 1.27.2, Python 3.13.5 tomllib,
// iniconfig 2.3.1):
//   - `go mod edit -json` reads `replace(` (no space) as a replace block, accepts a quoted module path, and
//     rejects `bogus x y`, a stray `)` and a block entry that is not `old => new` ("unknown directive" / "usage");
//   - tomllib decodes ["\u0074ool".pytest.ini_options] to tool.pytest.ini_options and ["dev\u002Ddependencies"] to
//     dev-dependencies, parses ["tool".pytest...] exactly like [tool.pytest...], and reads a multi-line string;
//   - iniconfig keeps the section name raw ("tool:pytest " is not "tool:pytest", so pytest does not read it), keeps
//     the interior whitespace of a value (`-k "a  b"` is not `-k "a b"`), strips only the ends of a value and skips
//     comment lines;
//   - make reads `\t-include local.cfg` inside an ifeq as an include (it is only a recipe after a rule), reads
//     `$(eval include x.mk)`, runs `load foo.so`, reads MAKEFILES from the environment, honours `-sC dir`,
//     `--dir=sub`, and the abbreviations `--fil=x.mk`, `--fil x.mk` and `--make=x.mk` of --file/--makefile.

// d18Ingest builds the standard demo repository, commits base on top of it as the red commit, snapshots the
// discovery protection at red (validation s3ValidationYAML), commits head as the implementation and runs
// CheckIngestion. A head value "<rm>" deletes the file.
func d18Ingest(t *testing.T, base, head map[string]string, withValidation bool) ([]string, error) {
	t.Helper()
	s, red := s3DiscoveryRepo(t)
	if len(base) > 0 {
		for p, c := range base {
			s.write(p, c)
		}
		red = s.commit("red2")
	}
	dp, err := s.derive(t, red, s3ValidationYAML)
	if err != nil {
		t.Fatalf("the base repository must derive at red: %v", err)
	}
	for p, c := range head {
		if c == "<rm>" {
			s.remove(p)
			continue
		}
		s.write(p, c)
	}
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

type d18Case struct {
	name  string
	base  map[string]string
	head  map[string]string
	want  []string // must all be reported
	allow []string // may additionally be reported; anything else is a failure
}

func (c d18Case) run(t *testing.T, withValidation bool) {
	t.Helper()
	got, err := d18Ingest(t, c.base, c.head, withValidation)
	if err != nil {
		t.Fatalf("CheckIngestion: %v", err)
	}
	have := map[string]bool{}
	for _, g := range got {
		have[g] = true
	}
	ok := map[string]bool{}
	for _, w := range c.want {
		ok[w] = true
		if !have[w] {
			t.Errorf("not reported: %q (got %q)", w, got)
		}
	}
	for _, a := range c.allow {
		ok[a] = true
	}
	for _, g := range got {
		if !ok[g] {
			t.Errorf("reported but not expected: %q (got %q)", g, got)
		}
	}
}

const (
	d18GoModBase  = "module example.com/m\n\ngo 1.22\n"
	d18PyBase     = "[tool.black]\nx = 1\n"
	d18PyPytest   = "[tool.black]\nx = 1\n\n[tool.pytest.ini_options]\naddopts = \"-q\"\n"
	d18CargoBase  = "[package]\nname = \"a\"\nversion = \"0.1.0\"\n"
	d18SetupBase  = "[metadata]\nname = demo\n\n[tool:pytest]\naddopts = -W error --strict-markers\n"
	d18DevDepsEvl = "evil = { path = \"./x\" }\n"
)

// 22-AC-22 (D18 a): go.mod is tokenised like modfile: `replace(` and `require(` open blocks, a quoted module path is
// handled, and a line the parser does not recognise is an error that counts as changed at the head. Twins: the
// same change with a space, a clean comment-only change, a `require(` block followed by a quoted replace.
func TestCheckIngestion_GoMod_D18_22AC22(t *testing.T) {
	testDep := tamper("test_dependency_changed", "go.mod")
	disc := tamper("discovery_config_changed", "go.mod")
	cases := []d18Case{
		{name: "replace_paren_without_space", base: map[string]string{"go.mod": d18GoModBase},
			head: map[string]string{"go.mod": d18GoModBase + "\nreplace(\n\tgithub.com/stretchr/testify => ./x\n)\n"},
			want: []string{testDep}, allow: []string{disc}},
		{name: "twin_replace_paren_with_space", base: map[string]string{"go.mod": d18GoModBase},
			head: map[string]string{"go.mod": d18GoModBase + "\nreplace (\n\tgithub.com/stretchr/testify => ./x\n)\n"},
			want: []string{testDep}, allow: []string{disc}},
		{name: "replace_paren_without_space_quoted_paths", base: map[string]string{"go.mod": d18GoModBase},
			head: map[string]string{"go.mod": d18GoModBase + "\nreplace(\n\t\"github.com/stretchr/testify\" => \"./x\"\n)\n"},
			want: []string{testDep}, allow: []string{disc}},
		{name: "require_paren_then_quoted_replace", base: map[string]string{"go.mod": d18GoModBase},
			head: map[string]string{"go.mod": d18GoModBase + "\nrequire(\n\tgolang.org/x/mod v0.1.0\n)\n\nreplace \"github.com/stretchr/testify\" => \"./x\"\n"},
			want: []string{testDep}, allow: []string{disc}},
		{name: "twin_require_paren_only", base: map[string]string{"go.mod": d18GoModBase},
			head: map[string]string{"go.mod": d18GoModBase + "\nrequire(\n\tgolang.org/x/mod v0.1.0\n)\n"}},
		{name: "unknown_directive_is_an_error_at_head", base: map[string]string{"go.mod": d18GoModBase},
			head: map[string]string{"go.mod": d18GoModBase + "\nbogus x y\n"},
			want: []string{testDep, disc}},
		{name: "stray_close_paren_is_an_error_at_head", base: map[string]string{"go.mod": d18GoModBase},
			head: map[string]string{"go.mod": d18GoModBase + "\nrequire (\n\tgolang.org/x/mod v0.1.0\n)\n)\n"},
			want: []string{testDep, disc}},
		{name: "unrecognised_block_entry_is_an_error_at_head", base: map[string]string{"go.mod": d18GoModBase},
			head: map[string]string{"go.mod": d18GoModBase + "\nreplace (\n\tgithub.com/a/b => ./x\n\tfoo bar baz qux\n)\n"},
			want: []string{testDep, disc}},
		{name: "twin_comments_in_odd_places_only_changed",
			base: map[string]string{"go.mod": "module example.com/m\n\ngo 1.22\n\nreplace (\n\tgithub.com/a/b => ./x\n)\n"},
			head: map[string]string{"go.mod": "module example.com/m // c\n\n// c\ngo 1.22 // c\n\nreplace ( // c\n\t// c\n\tgithub.com/a/b => ./x // c\n\n\t// c\n)\n// c\n"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { c.run(t, false) })
	}
}

// 22-AC-8 (D18 b): TOML is a recogniser of bare keys, dotted bare keys and quoted keys without escapes. Headers
// compare on the decoded key path; an escape in a quoted key, a multi-line string and anything else outside the
// subset is an error that counts as changed.
func TestCheckIngestion_TOML_D18_22AC8(t *testing.T) {
	disc := tamper("discovery_config_changed", "pyproject.toml")
	cargoDisc := tamper("discovery_config_changed", "Cargo.toml")
	cargoDep := tamper("test_dependency_changed", "Cargo.toml")
	py := func(s string) map[string]string { return map[string]string{"pyproject.toml": s} }
	cargo := func(s string) map[string]string { return map[string]string{"Cargo.toml": s} }
	cases := []d18Case{
		// Already satisfied at the S3 head (quoted keys without escapes are decoded).
		{name: "quoted_header_addopts_changed", base: py(d18PyPytest),
			head: py("[tool.black]\nx = 1\n\n[\"tool\".pytest.ini_options]\naddopts = \"-k nothing\"\n"), want: []string{disc}},
		{name: "twin_quoted_header_same_values", base: py(d18PyPytest),
			head: py("[tool.black]\nx = 1\n\n[\"tool\".pytest.ini_options]\naddopts = \"-q\"\n")},
		{name: "twin_quoted_dotted_key_same_values", base: py("[tool]\npytest = { ini_options = { addopts = \"-q\" } }\n"),
			head: py("[\"tool\"]\n\"pytest\" = { ini_options = { addopts = \"-q\" } }\n")},
		{name: "cargo_quoted_dev_dependencies_added", base: cargo(d18CargoBase),
			head: cargo(d18CargoBase + "\n[\"dev-dependencies\"]\n" + d18DevDepsEvl), want: []string{cargoDep}, allow: []string{cargoDisc}},
		{name: "twin_cargo_dependencies_added", base: cargo(d18CargoBase),
			head: cargo(d18CargoBase + "\n[dependencies]\nserde = \"1\"\n")},
		{name: "multiline_value_in_protected_table", base: py("[tool.pytest.ini_options]\naddopts = \"-q\\n\"\n"),
			head: py("[tool.pytest.ini_options]\naddopts = \"\"\"\n-q\n\"\"\"\n"), want: []string{disc}},
		{name: "twin_unrelated_table_added", base: py(d18PyPytest),
			head: py(d18PyPytest + "\n[project]\nname = \"x\"\n")},

		// The evasions of review 1 (the base has no pytest table, so a header that is not decoded hides it).
		{name: "escaped_key_hides_pytest_table", base: py(d18PyBase),
			head: py(d18PyBase + "\n[\"\\u0074ool\".pytest.ini_options]\naddopts = \"-k nothing\"\n"), want: []string{disc}},
		{name: "escaped_key_hides_cargo_dev_dependencies", base: cargo(d18CargoBase),
			head: cargo(d18CargoBase + "\n[\"dev\\u002Ddependencies\"]\n" + d18DevDepsEvl), want: []string{cargoDep, cargoDisc}},
		{name: "literal_quoted_key_with_backslash_is_an_error", base: py(d18PyBase),
			head: py(d18PyBase + "\n['tool\\x']\na = 1\n"), want: []string{disc}},
		// Multi-line strings are standard TOML and must be parsed, not rejected: a Python repo with a multi-line
		// [project] description must not be blocked.
		{name: "twin_multiline_string_in_unrelated_table", base: py(d18PyBase),
			head: py(d18PyBase + "\n[project]\ndescription = \"\"\"\nhello\n\"\"\"\n")},
		{name: "multiline_string_hides_protected_change", base: py(d18PyPytest),
			head: py("[tool.black]\nx = 1\n\n[tool.pytest.ini_options]\naddopts = \"\"\"\n-k nothing\n\"\"\"\n"), want: []string{disc}},
		// python3 tomllib: addopts = "-q" and the multi-line literal ''' newline -q ''' (the newline right after
		// the opener is trimmed) decode to the identical value "-q".
		{name: "twin_multiline_string_same_value", base: py(d18PyPytest),
			head: py("[tool.black]\nx = 1\n\n[tool.pytest.ini_options]\naddopts = '''\n-q'''\n")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { c.run(t, false) })
	}
}

// 22-AC-8 (D18 c): the INI section name is the raw text between [ and ] (iniconfig does not trim it), and a value
// keeps its interior whitespace. Twins: a change elsewhere in the file, comment lines and trailing blanks.
func TestCheckIngestion_INI_D18_22AC8(t *testing.T) {
	disc := tamper("discovery_config_changed", "setup.cfg")
	setup := func(s string) map[string]string { return map[string]string{"setup.cfg": s} }
	cases := []d18Case{
		{name: "section_header_gains_a_space", base: setup(d18SetupBase),
			head: setup("[metadata]\nname = demo\n\n[tool:pytest ]\naddopts = -W error --strict-markers\n"), want: []string{disc}},
		{name: "interior_whitespace_collapsed_in_addopts", base: setup("[tool:pytest]\naddopts = -W  error --strict-markers\n"),
			head: setup("[tool:pytest]\naddopts = -W error --strict-markers\n"), want: []string{disc}},
		{name: "interior_whitespace_collapsed_in_quoted_argument", base: setup("[tool:pytest]\naddopts = -k \"a  b\"\n"),
			head: setup("[tool:pytest]\naddopts = -k \"a b\"\n"), want: []string{disc}},
		{name: "addopts_changed", base: setup(d18SetupBase),
			head: setup("[metadata]\nname = demo\n\n[tool:pytest]\naddopts = --strict-markers\n"), want: []string{disc}},
		{name: "twin_other_section_changed", base: setup(d18SetupBase),
			head: setup("[metadata]\nname = demo2\n\n[tool:pytest]\naddopts = -W error --strict-markers\n")},
		{name: "twin_comment_and_trailing_blanks", base: setup(d18SetupBase),
			head: setup("[metadata]\nname = demo\n\n[tool:pytest]\n# note\naddopts = -W error --strict-markers   \n")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { c.run(t, false) })
	}
}

// 22-AC-8 (D18 N3): a JSON configuration with data after the value (an extra "}" or "]") is unparsable, which
// counts as changed; trailing whitespace is valid JSON and is clean.
func TestCheckIngestion_JSONTrailingData_D18_22AC8(t *testing.T) {
	const pkg = "{\n  \"name\": \"demo\",\n  \"scripts\": {\"test\": \"go test ./...\", \"build\": \"tsc\"}\n}\n"
	disc := tamper("discovery_config_changed", "package.json")
	dep := tamper("test_dependency_changed", "package.json")
	one := func(s string) map[string]string { return map[string]string{"package.json": s} }
	cases := []d18Case{
		{name: "extra_closing_brace", head: one(strings.TrimSuffix(pkg, "\n") + "}\n"), want: []string{disc}, allow: []string{dep}},
		{name: "extra_closing_bracket", head: one(strings.TrimSuffix(pkg, "\n") + "]\n"), want: []string{disc}, allow: []string{dep}},
		{name: "trailing_garbage", head: one(pkg + "x\n"), want: []string{disc}, allow: []string{dep}},
		{name: "twin_trailing_whitespace_only", head: one(pkg + "\n\n  \n")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { c.run(t, false) })
	}
}

// d18Validation is a validation.yaml with the standard unit suite and one extra check.
func d18Validation(suiteArgv, checkArgv, checkExtra string) string {
	return `apiVersion: strange.company/v1alpha1
kind: RepositoryValidation
metadata: {name: demo}
spec:
  verification:
    image: "docker.io/library/golang@sha256:2d658507a011f1eadc0114d7a421b312b2e77d9042a24036a26e267cc9231677"
    suites:
      - name: unit
        adapter: junit-xml
        language: go
        argv: ` + suiteArgv + `
        report: {path: out/unit.xml}
        timeoutSeconds: 1500
        phases: [red, green]
    checks:
      - {name: lint, argv: ` + checkArgv + `, timeoutSeconds: 900` + checkExtra + `}
`
}

const (
	d18SuiteArgv = `[make, test, "JUNIT={report}"]`
	d18CheckArgv = `[make, lint]`
)

// d18Derive snapshots the discovery protection of a scratch repository holding files (and a Go test file).
func d18Derive(t *testing.T, files map[string]string, validation string) (DiscoveryProtection, *scratch, string, error) {
	t.Helper()
	s := newScratch(t)
	s.write("pkg/core.go", "package pkg\n\nfunc Core() int { return 0 }\n")
	s.write("pkg/core_test.go", s3CoreTest)
	for p, c := range files {
		s.write(p, c)
	}
	red := s.commit("red")
	dp, err := s.derive(t, red, validation)
	return dp, s, red, err
}

func d18WholeFile(dp DiscoveryProtection, p string) (present bool, blob *string) {
	for _, w := range dp.WholeFiles {
		if w.Path == p {
			return true, w.BlobSha
		}
	}
	return false, nil
}

// 22-AC-32 (D18 d): a tab-led include inside a conditional is an include. The file it names is protected, and the
// implementer adding it is discovery_config_changed. The same include written without the tab is the passing twin.
func TestDiscovery_TabLedIncludeInConditional_D18_22AC32(t *testing.T) {
	for _, tc := range []struct{ name, makefile string }{
		{"tab_minus_include_in_ifeq", "GO ?= go\nifeq ($(CI),)\n\t-include local.cfg\nendif\n\ntest:\n\tgo test ./...\n"},
		{"tab_include_in_ifeq", "GO ?= go\nifeq ($(CI),)\n\tinclude local.cfg\nendif\n\ntest:\n\tgo test ./...\n"},
		{"tab_sinclude_in_ifeq", "GO ?= go\nifeq ($(CI),)\n\tsinclude local.cfg\nendif\n\ntest:\n\tgo test ./...\n"},
		{"twin_no_tab_minus_include_in_ifeq", "GO ?= go\nifeq ($(CI),)\n-include local.cfg\nendif\n\ntest:\n\tgo test ./...\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dp, s, red, err := d18Derive(t, map[string]string{"Makefile": tc.makefile}, s3ValidationYAML)
			if err != nil {
				t.Fatal(err)
			}
			if present, blob := d18WholeFile(dp, "local.cfg"); !present || blob != nil {
				t.Fatalf("local.cfg: present=%v blob=%v, want a nil blob (make reads it when it appears)", present, blob)
			}
			s.write("local.cfg", "test:\n\ttrue\n")
			head := s.commit("impl")
			man := s.s3Manifest(red, "pkg/core_test.go")
			man.DiscoveryProtection = dp
			got, err := CheckIngestion(s.ingestionInput(t, "implementer", red, head, man))
			if err != nil {
				t.Fatal(err)
			}
			if g, want := violationKeys(got), []string{tamper("discovery_config_changed", "local.cfg")}; !equalStrings(g, want) {
				t.Fatalf("got %q, want %q", g, want)
			}
		})
	}
}

// 22-AC-32 (D18 d, D17 5): what the derivation cannot resolve statically is an error (fail closed), never a quiet
// omission. `$(eval ...)` containing include, a `load` directive, MAKEFILES in a check's environment, a directory
// change in any flag cluster or long option, and an abbreviated --file/--makefile. Each has a passing twin.
func TestDeriveDiscoveryProtection_MakeFailClosed_D18_22AC32(t *testing.T) {
	const mk = "test:\n\tgo test ./...\n\nlint:\n\ttrue\n"
	type tc struct {
		name     string
		makefile string
		suite    string
		check    string
		extra    string
		files    map[string]string
		wantErr  bool
		whole    []string // protected when no error
	}
	cases := []tc{
		{name: "eval_include", makefile: mk + "$(eval include x.mk)\n", wantErr: true},
		{name: "eval_minus_include", makefile: mk + "$(eval -include x.mk)\n", wantErr: true},
		{name: "twin_eval_without_include", makefile: mk + "$(eval X := 1)\n"},
		{name: "load_directive", makefile: "load foo.so\n" + mk, wantErr: true},
		{name: "minus_load_directive", makefile: "-load foo.so\n" + mk, wantErr: true},
		{name: "twin_load_variable_and_target", makefile: "load := 1\n" + mk + "\nload:\n\ttrue\n"},
		{name: "check_env_makefiles", makefile: mk, extra: `, env: {MAKEFILES: x.mk}`, wantErr: true},
		{name: "twin_check_env_other_variable", makefile: mk, extra: `, env: {CI: "1"}`},
		{name: "argv_dash_sC", makefile: mk, check: `[make, -sC, sub, lint]`, wantErr: true},
		{name: "argv_dash_Cs_cluster", makefile: mk, check: `[make, -Cs, sub, lint]`, wantErr: true},
		{name: "argv_directory_abbreviation", makefile: mk, check: `[make, --dir=sub, lint]`, wantErr: true},
		{name: "twin_argv_dash_s", makefile: mk, check: `[make, -s, lint]`},
		{name: "argv_file_abbreviation_equals", makefile: mk, check: `[make, --fil=x.mk, lint]`, wantErr: true},
		{name: "argv_file_abbreviation_separate", makefile: mk, check: `[make, --fil, x.mk, lint]`, wantErr: true},
		{name: "argv_makefile_abbreviation", makefile: mk, check: `[make, --make=x.mk, lint]`, wantErr: true},
		{name: "twin_argv_file_long_option", makefile: mk, check: `[make, --file=x.mk, lint]`,
			files: map[string]string{"x.mk": "lint:\n\ttrue\n"}, whole: []string{"x.mk"}},
		{name: "suite_argv_dash_sC", makefile: mk, suite: `[make, -sC, sub, test, "JUNIT={report}"]`, wantErr: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			suite, check := c.suite, c.check
			if suite == "" {
				suite = d18SuiteArgv
			}
			if check == "" {
				check = d18CheckArgv
			}
			files := map[string]string{"Makefile": c.makefile}
			for p, content := range c.files {
				files[p] = content
			}
			dp, _, _, err := d18Derive(t, files, d18Validation(suite, check, c.extra))
			if c.wantErr {
				if err == nil {
					t.Fatalf("accepted: whole files %+v", dp.WholeFiles)
				}
				return
			}
			if err != nil {
				t.Fatalf("a statically resolvable Makefile was rejected: %v", err)
			}
			for _, p := range c.whole {
				if present, blob := d18WholeFile(dp, p); !present || blob == nil {
					t.Errorf("%s is not protected (present=%v)", p, present)
				}
			}
		})
	}
}

// 22-AC-32 (D18 e, N1): with Validation set, a head whose re-derivation fails is not an ingestion error. The
// entry-level violations are reported and the file whose derivation failed is discovery_config_changed. Twins:
// a clean head, and the same heads without Validation (no re-derivation).
func TestCheckIngestion_HeadDerivationFailure_D18_22AC32(t *testing.T) {
	cases := []d18Case{
		{name: "malformed_package_json", head: map[string]string{"package.json": "{ not json"},
			want: []string{tamper("discovery_config_changed", "package.json")}, allow: []string{tamper("test_dependency_changed", "package.json")}},
		{name: "mk_lint_non_literal_include", head: map[string]string{"mk/lint.mk": "include $(shell echo x)\nlint:\n\ttrue\n"},
			want: []string{tamper("discovery_config_changed", "mk/lint.mk")}},
		{name: "tsconfig_with_a_comment_added", head: map[string]string{"tsconfig.json": "{\n // c\n \"compilerOptions\": {}\n}\n"},
			want: []string{tamper("discovery_config_changed", "tsconfig.json")}},
		{name: "twin_readme_only", head: map[string]string{"README.md": "demo 2\n"}},
		{name: "twin_tsconfig_without_protected_keys", head: map[string]string{"tsconfig.json": "{\"compilerOptions\": {\"strict\": true}}\n"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { c.run(t, true) })
	}
	// Without Validation there is no head derivation, so a failing head is judged by the semantic keys alone.
	for _, c := range cases {
		if strings.HasPrefix(c.name, "twin_") {
			continue
		}
		t.Run("no_validation_"+c.name, func(t *testing.T) { c.run(t, false) })
	}
}

// d18Chain builds a linear history a -> b -> c and a side commit x on its own root, and returns the repository
// with the commit ids.
func d18Chain(t *testing.T) (s *scratch, a, b, c, x string) {
	t.Helper()
	s = newScratch(t)
	s.write("f.txt", "1\n")
	a = s.commit("a")
	s.write("f.txt", "2\n")
	b = s.commit("b")
	s.write("f.txt", "3\n")
	c = s.commit("c")
	s.git("checkout", "-q", "--orphan", "other")
	s.git("rm", "-rfq", ".")
	s.write("g.txt", "1\n")
	x = s.commit("x")
	s.git("checkout", "-q", "main")
	return
}

func d18WriteGrafts(t *testing.T, gitDir string, lines ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(gitDir, "info"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "info", "grafts"), []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// 22-AC-31 (D18 f): `.git/info/grafts` rewrites parents for every Git command that reads it. IsAncestor and RevList
// must ignore it, in a worktree and in a bare mirror: a graft that cuts the chain (c becomes a root) must not make
// a a non-ancestor or shrink the range, and a graft that replaces c's parent by an unrelated commit x must not make
// x an ancestor. Each scenario also runs without a graft file (the twin).
func TestRevListAndIsAncestor_IgnoreGrafts_D18_22AC31(t *testing.T) {
	check := func(t *testing.T, g GitRepo, a, b, c, x string) {
		t.Helper()
		if ok, err := IsAncestor(g, a, c); err != nil || !ok {
			t.Errorf("IsAncestor(a, c) = %v, %v; want true (the real history)", ok, err)
		}
		if ok, err := IsAncestor(g, a, b); err != nil || !ok {
			t.Errorf("IsAncestor(a, b) = %v, %v; want true", ok, err)
		}
		if ok, err := IsAncestor(g, x, c); err != nil || ok {
			t.Errorf("IsAncestor(x, c) = %v, %v; want false (x is unrelated)", ok, err)
		}
		got, err := RevList(g, a, c)
		if err != nil {
			t.Fatal(err)
		}
		if want := []string{b, c}; !equalStrings(got, want) {
			t.Errorf("RevList(a, c) = %v, want %v (the real history)", got, want)
		}
	}
	grafts := []struct {
		name string
		line func(c, x string) string
	}{
		{"cut_chain_c_becomes_a_root", func(c, x string) string { return c }},
		{"link_c_to_unrelated_x", func(c, x string) string { return c + " " + x }},
	}
	for _, gr := range grafts {
		t.Run("worktree_"+gr.name, func(t *testing.T) {
			s, a, b, c, x := d18Chain(t)
			check(t, s.repo(), a, b, c, x)
			d18WriteGrafts(t, filepath.Join(s.dir, ".git"), gr.line(c, x))
			check(t, s.repo(), a, b, c, x)
		})
		t.Run("bare_mirror_"+gr.name, func(t *testing.T) {
			s, a, b, c, x := d18Chain(t)
			mirror := filepath.Join(t.TempDir(), "mirror.git")
			s.git("clone", "-q", "--bare", "--no-local", s.dir, mirror)
			g := GitRepo{Dir: mirror}
			check(t, g, a, b, c, x)
			d18WriteGrafts(t, mirror, gr.line(c, x))
			check(t, g, a, b, c, x)
		})
	}
}
