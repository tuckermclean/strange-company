package verify

import (
	"encoding/json"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"strange.company/sc/internal/contracts"
)

// Discovery protection (plan 22 section 14.5, blind TDD-B5) and its enforcement at ingestion (22-AC-32, discovery
// part): every file make reads for a check (the Makefile, transitively through include, -include, -f and literal
// $(wildcard ...), and every script a recipe or argv names by repository path) and every file in a check's
// configPaths is a discovery-protected whole file; an implementer edit of any of them is discovery_config_changed.
// The criteria also name `mk/lint.mk` (an included makefile) and `.golangci.yml` (a configPaths entry).

const s3ValidationYAML = `apiVersion: strange.company/v1alpha1
kind: RepositoryValidation
metadata: {name: demo}
spec:
  verification:
    image: "docker.io/library/golang@sha256:2d658507a011f1eadc0114d7a421b312b2e77d9042a24036a26e267cc9231677"
    suites:
      - name: unit
        adapter: junit-xml
        language: go
        argv: [make, test, "JUNIT={report}"]
        report: {path: out/unit.xml}
        timeoutSeconds: 1500
        phases: [red, green]
    checks:
      - {name: lint, argv: [make, lint], timeoutSeconds: 900, toolingFrom: base, configPaths: [.golangci.yml, .eslintrc.json, ruff.toml]}
      - {name: script, argv: ["./scripts/check.sh"], timeoutSeconds: 100}
`

const s3ValidationFileYAML = `apiVersion: strange.company/v1alpha1
kind: RepositoryValidation
metadata: {name: demo}
spec:
  verification:
    image: "docker.io/library/golang@sha256:2d658507a011f1eadc0114d7a421b312b2e77d9042a24036a26e267cc9231677"
    suites:
      - name: unit
        adapter: junit-xml
        language: go
        argv: [make, -f, build/ci.mk, test]
        report: {path: out/unit.xml}
        timeoutSeconds: 1500
        phases: [red, green]
`

// s3Validation parses validation YAML and checks the document against repo-validation.schema.yaml, so that no
// test runs on an input outside its contract.
func s3Validation(t *testing.T, text string) map[string]any {
	t.Helper()
	v, err := contracts.ParseYAML([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	schemas, err := contracts.CompileSchemas(filepath.Join(repoRoot(t), "contracts"))
	if err != nil {
		t.Fatal(err)
	}
	s := schemas["repo-validation"]
	if s == nil {
		t.Fatal("no repo-validation schema")
	}
	if err := s.Validate(v); err != nil {
		t.Fatalf("the test's validation.yaml violates its schema: %v", err)
	}
	m, ok := v.(map[string]any)
	if !ok {
		t.Fatal("validation.yaml is not a mapping")
	}
	return m
}

const s3Makefile = `# Makefile of the demo repository.
include mk/lint.mk  # lint rules
-include mk/local.mk
include $(wildcard mk/gen/*.mk)
include mk/multi1.mk mk/multi2.mk
# include mk/unused.mk

test:
	go test ./...
`

// s3DiscoveryRepo builds the demo repository and returns it with the red commit.
func s3DiscoveryRepo(t *testing.T) (*scratch, string) {
	s := newScratch(t)
	s.write("Makefile", s3Makefile)
	s.write("mk/lint.mk", "include mk/lint2.mk\n\nlint:\n\t./scripts/lint.sh --fast\n\t@./scripts/quiet.sh\n\tgolangci-lint run\n")
	s.write("mk/lint2.mk", "include mk/lint.mk\nLINTFLAGS := -v\n")
	s.write("mk/gen/a.mk", "A := 1\n")
	s.write("mk/gen/b.mk", "B := 1\n")
	s.write("mk/multi1.mk", "M1 := 1\n")
	s.write("mk/multi2.mk", "M2 := 1\n")
	s.write("mk/unused.mk", "U := 1\n")
	s.write("scripts/lint.sh", "#!/bin/sh\necho lint\n")
	s.write("scripts/quiet.sh", "#!/bin/sh\necho quiet\n")
	s.write("scripts/check.sh", "#!/bin/sh\necho check\n")
	s.write("scripts/other.sh", "#!/bin/sh\necho other\n")
	s.write(".golangci.yml", "run:\n  timeout: 5m\n")
	s.write("jest.config.js", "module.exports = {};\n")
	s.write("package.json", "{\n  \"name\": \"demo\",\n  \"scripts\": {\"test\": \"go test ./...\", \"build\": \"tsc\"}\n}\n")
	s.write("README.md", "demo\n")
	s.write("pkg/core.go", "package pkg\n")
	s.write("pkg/core_test.go", s3CoreTest)
	return s, s.commit("red")
}

func (s *scratch) derive(t *testing.T, rev string, validation string) (DiscoveryProtection, error) {
	t.Helper()
	plat, eff := s3Effective(t)
	return DeriveDiscoveryProtection(DiscoveryInput{Git: s.repo(), Rev: rev, Validation: s3Validation(t, validation), Platform: plat, Effective: eff})
}

// 22-AC-32 (discovery): the snapshot at the red commit lists every file a check reads, with the blob id Git has (nil
// for a file that does not exist but would be read if it appeared), and nothing else.
func TestDeriveDiscoveryProtection_22AC32(t *testing.T) {
	s, red := s3DiscoveryRepo(t)
	dp, err := s.derive(t, red, s3ValidationYAML)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]*string{}
	var paths []string
	for _, w := range dp.WholeFiles {
		if _, dup := got[w.Path]; dup {
			t.Errorf("%s listed twice", w.Path)
		}
		got[w.Path] = w.BlobSha
		paths = append(paths, w.Path)
	}
	if !sort.StringsAreSorted(paths) {
		t.Errorf("whole files are not sorted by path: %q", paths)
	}
	protected := []string{
		".golangci.yml",               // configPaths
		".eslintrc.json", "ruff.toml", // configPaths entries that do not exist yet (must stay absent)
		"Makefile", "GNUmakefile", "makefile", // make reads the first that exists
		"mk/lint.mk",                 // include (with a trailing comment)
		"mk/lint2.mk",                // transitive include; the include cycle terminates
		"mk/local.mk",                // -include of a file that does not exist
		"mk/gen/a.mk", "mk/gen/b.mk", // literal $(wildcard mk/gen/*.mk)
		"mk/multi1.mk", "mk/multi2.mk", // one include line, two files
		"scripts/lint.sh", "scripts/quiet.sh", // recipe scripts (also with a leading @)
		"scripts/check.sh", // argv[0] of a check
		"jest.config.js",   // platform wholeFiles glob
	}
	for _, p := range protected {
		blob, ok := got[p]
		if !ok {
			t.Errorf("%s is not discovery-protected", p)
			continue
		}
		if want := s.blobAt(red, p); want == s3ZeroBlob {
			if blob != nil {
				t.Errorf("%s does not exist, want a nil blob (must stay absent), got %s", p, *blob)
			}
		} else if blob == nil || *blob != want {
			t.Errorf("%s: blob %v, git says %s", p, blob, want)
		}
	}
	for _, p := range []string{"mk/unused.mk", "scripts/other.sh", "README.md", "pkg/core.go", "pkg/core_test.go", "package.json"} {
		if _, ok := got[p]; ok {
			t.Errorf("%s is protected but nothing makes a check read it", p)
		}
	}

	// Semantic keys: value is the sha256 of the canonical JSON of the key's value, nil for an absent key.
	keys := map[string]*string{}
	for _, k := range dp.SemanticKeys {
		keys[k.Path+"#"+k.Key] = k.ValueSha256
	}
	val, _ := json.Marshal("go test ./...")
	if v := keys["package.json#scripts.test"]; v == nil || *v != sha256Of(val) {
		t.Errorf("package.json#scripts.test = %v, want %s", v, sha256Of(val))
	}
	for _, k := range []string{"jest", "vitest", "mocha", "devDependencies"} {
		if v, ok := keys["package.json#"+k]; !ok || v != nil {
			t.Errorf("package.json#%s: present=%v value=%v, want a nil value (the key is absent and must stay absent)", k, ok, v)
		}
	}
	for k := range keys {
		if !strings.HasPrefix(k, "package.json#") {
			t.Errorf("semantic key of a file that does not exist: %s", k)
		}
	}
}

// 22-AC-32: `make -f <file>` protects that file and what it includes; the result does not depend on the order
// of the checks (deterministic bytes).
func TestDeriveDiscoveryProtection_MakeDashF_22AC32(t *testing.T) {
	s := newScratch(t)
	s.write("Makefile", "test:\n\techo\n")
	s.write("build/ci.mk", "include build/inc.mk\ntest:\n\tgo test ./...\n")
	s.write("build/inc.mk", "X := 1\n")
	s.write("build/unrelated.mk", "Y := 1\n")
	red := s.commit("red")
	dp, err := s.derive(t, red, s3ValidationFileYAML)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]*string{}
	for _, w := range dp.WholeFiles {
		got[w.Path] = w.BlobSha
	}
	for _, p := range []string{"build/ci.mk", "build/inc.mk"} {
		if b := got[p]; b == nil || *b != s.blobAt(red, p) {
			t.Errorf("%s: %v", p, b)
		}
	}
	if _, ok := got["build/unrelated.mk"]; ok {
		t.Error("build/unrelated.mk is protected but not read")
	}
	again, err := s.derive(t, red, s3ValidationFileYAML)
	a, _ := json.Marshal(dp)
	b, _ := json.Marshal(again)
	if err != nil || string(a) != string(b) {
		t.Errorf("not deterministic: %s vs %s (%v)", a, b, err)
	}
}

// 22-AC-32: fail closed. A make include the derivation cannot resolve statically, an unknown revision and a
// repository without any Makefile (all three names listed as absent, so a later GNUmakefile is caught).
func TestDeriveDiscoveryProtection_FailClosed_22AC32(t *testing.T) {
	t.Run("non_literal_include_is_an_error", func(t *testing.T) {
		s := newScratch(t)
		s.write("Makefile", "MK := mk\ninclude $(MK)/x.mk\n")
		s.write("mk/x.mk", "X := 1\n")
		red := s.commit("red")
		if dp, err := s.derive(t, red, s3ValidationYAML); err == nil {
			t.Fatalf("an include that cannot be resolved was accepted: %+v", dp.WholeFiles)
		}
	})
	t.Run("unknown_revision", func(t *testing.T) {
		s, _ := s3DiscoveryRepo(t)
		if _, err := s.derive(t, strings.Repeat("e", 40), s3ValidationYAML); err == nil {
			t.Fatal("an unknown revision was accepted")
		}
		if _, err := s.derive(t, "--help", s3ValidationYAML); err == nil {
			t.Fatal("an option as revision was accepted")
		}
	})
	t.Run("no_makefile_all_three_names_absent", func(t *testing.T) {
		s := newScratch(t)
		s.write("scripts/check.sh", "#!/bin/sh\n")
		s.write("pkg/core.go", "package pkg\n")
		red := s.commit("red")
		dp, err := s.derive(t, red, s3ValidationYAML)
		if err != nil {
			t.Fatal(err)
		}
		got := map[string]*string{}
		for _, w := range dp.WholeFiles {
			got[w.Path] = w.BlobSha
		}
		for _, p := range []string{"Makefile", "GNUmakefile", "makefile", ".golangci.yml"} {
			if b, ok := got[p]; !ok || b != nil {
				t.Errorf("%s: present=%v blob=%v, want a nil blob", p, ok, b)
			}
		}
	})
}

// 22-AC-32 (discovery): an implementer edit of an included mk/lint.mk or of .golangci.yml (configPaths), and of
// every other file make or a check reads, yields discovery_config_changed; the same commit touching files that
// nothing reads is clean. The protection is the snapshot DeriveDiscoveryProtection took at the red commit, passed
// through the manifest.
func TestCheckIngestion_IncludedMakefileAndConfigPaths_22AC32(t *testing.T) {
	type tc struct {
		name string
		edit func(s *scratch)
		flag []string // paths flagged discovery_config_changed
	}
	cases := []tc{
		{"mk_lint_mk_edited", func(s *scratch) { s.write("mk/lint.mk", "lint:\n\ttrue\n") }, []string{"mk/lint.mk"}},
		{"golangci_yml_edited", func(s *scratch) { s.write(".golangci.yml", "run:\n  timeout: 1s\n") }, []string{".golangci.yml"}},
		{"golangci_yml_deleted", func(s *scratch) { s.remove(".golangci.yml") }, []string{".golangci.yml"}},
		{"transitive_include_edited", func(s *scratch) { s.write("mk/lint2.mk", "LINTFLAGS := -v -n\n") }, []string{"mk/lint2.mk"}},
		{"wildcard_included_edited", func(s *scratch) { s.write("mk/gen/a.mk", "A := 2\n") }, []string{"mk/gen/a.mk"}},
		{"second_file_of_one_include_line", func(s *scratch) { s.write("mk/multi2.mk", "M2 := 2\n") }, []string{"mk/multi2.mk"}},
		{"makefile_edited", func(s *scratch) { s.write("Makefile", s3Makefile+"\nlint:\n\ttrue\n") }, []string{"Makefile"}},
		{"minus_include_appears", func(s *scratch) { s.write("mk/local.mk", "lint:\n\ttrue\n") }, []string{"mk/local.mk"}},
		{"gnumakefile_takes_precedence", func(s *scratch) { s.write("GNUmakefile", "lint:\n\ttrue\n") }, []string{"GNUmakefile"}},
		{"absent_config_path_appears", func(s *scratch) { s.write(".eslintrc.json", "{}\n") }, []string{".eslintrc.json"}},
		{"recipe_script_edited", func(s *scratch) { s.write("scripts/lint.sh", "#!/bin/sh\nexit 0\n") }, []string{"scripts/lint.sh"}},
		{"at_prefixed_recipe_script_edited", func(s *scratch) { s.write("scripts/quiet.sh", "#!/bin/sh\nexit 0\n") }, []string{"scripts/quiet.sh"}},
		{"argv_script_edited", func(s *scratch) { s.write("scripts/check.sh", "#!/bin/sh\nexit 0\n") }, []string{"scripts/check.sh"}},
		{"mode_only_change_of_included_makefile", func(s *scratch) { s.chmod("mk/lint.mk", 0o755) }, []string{"mk/lint.mk"}},
		{"new_file_under_literal_wildcard", func(s *scratch) { s.write("mk/gen/c.mk", "C := 1\n") }, []string{"mk/gen/c.mk"}},
		{"several_at_once", func(s *scratch) {
			s.write("mk/lint.mk", "lint:\n\ttrue\n")
			s.write(".golangci.yml", "run: {}\n")
			s.write("mk/unused.mk", "U := 2\n")
		}, []string{"mk/lint.mk", ".golangci.yml"}},
		// Passing twins: files nothing reads are not protected.
		{"twin_unused_makefile_edited", func(s *scratch) { s.write("mk/unused.mk", "U := 2\n") }, nil},
		{"twin_unreferenced_script_edited", func(s *scratch) { s.write("scripts/other.sh", "#!/bin/sh\nexit 1\n") }, nil},
		{"twin_production_and_docs", func(s *scratch) {
			s.write("pkg/core.go", "package pkg\n\nvar X = 1\n")
			s.write("README.md", "demo 2\n")
		}, nil},
		{"twin_new_mk_outside_the_wildcard", func(s *scratch) { s.write("mk/other/c.mk", "C := 1\n"); s.write("mk/gen/c.txt", "c\n") }, nil},
		{"twin_nothing_changed", func(s *scratch) {}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, red := s3DiscoveryRepo(t)
			dp, err := s.derive(t, red, s3ValidationYAML)
			if err != nil {
				t.Fatal(err)
			}
			c.edit(s)
			head := s.commit("implementation")
			man := s.s3Manifest(red, "pkg/core_test.go")
			man.DiscoveryProtection = dp
			in := s.ingestionInput(t, "implementer", red, head, man)
			in.Validation = s3Validation(t, s3ValidationYAML)
			got, err := CheckIngestion(in)
			if err != nil {
				t.Fatal(err)
			}
			var want []string
			for _, p := range c.flag {
				want = append(want, tamper("discovery_config_changed", p))
			}
			if g := violationKeys(got); !equalStrings(g, sortedCopy(want)) {
				t.Fatalf("got %q, want %q", g, sortedCopy(want))
			}
		})
	}
}
