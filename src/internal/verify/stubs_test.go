package verify

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// Stub check (plan 22 section 14.2, ADR-0352). The facts of Scope.stubs are derived from real source: Go by the
// AST, other languages by per-language line patterns. Fixtures: testdata/stubs/<case>/{lang,path,base.*.txt?,
// head.*.txt,expect.json}; the sources carry a .txt suffix so that no tool treats them as part of this module.

type stubExpect = []StubFact

func stubCases() []string {
	return []string{
		"go_zero_value", "go_zero_value_method", "go_panic", "go_non_zero_body", "go_real_logic", "go_too_long",
		"go_no_marker", "go_marker_not_adjacent", "go_modifies_existing", "go_new_file_two_stubs", "go_no_stubs_only_existing",
		"rust_panic", "rust_zero_value", "rust_real_logic", "rust_no_marker",
	}
}

// stubFileOf reads a stubs fixture case into the library input.
func stubFileOf(t testing.TB, dir string) StubFile {
	t.Helper()
	lang := string(trimNL(mustRead(t, dir, "lang")))
	ext := map[string]string{"go": "go", "rust": "rs"}[lang]
	f := StubFile{Path: string(trimNL(mustRead(t, dir, "path"))), Language: lang, Head: mustRead(t, dir, "head."+ext+".txt")}
	if b, err := os.ReadFile(filepath.Join(dir, "base."+ext+".txt")); err == nil {
		f.Base = b
	}
	return f
}

func sortedFacts(f []StubFact) []StubFact {
	out := append([]StubFact(nil), f...)
	sort.Slice(out, func(i, j int) bool { return out[i].Symbol < out[j].Symbol })
	return out
}

// 22-AC-4: stub shapes (zero value, panic, unknown), marker adjacency, body length and additions-only are derived
// from the source: Go through go/parser, Rust through line patterns.
func TestAnalyzeStubs_Fixtures_22AC4(t *testing.T) {
	sp, err := LoadStubPolicy(platformPolicy(t).Verification)
	if err != nil {
		t.Fatalf("LoadStubPolicy: %v", err)
	}
	if sp.Marker != "SC-STUB" || sp.PanicMessage != "SC-STUB-NOT-IMPLEMENTED" || sp.MaxAddedLinesPerStub != 12 ||
		sp.MaxStubsPerTask != 20 || len(sp.AllowPanicStubsFor) != 1 || sp.AllowPanicStubsFor[0] != "rust" {
		t.Fatalf("StubPolicy = %+v, want the platform stubPolicy", sp)
	}
	for _, name := range stubCases() {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join("testdata", "stubs", name)
			var want []StubFact
			readJSONFile(t, &want, dir, "expect.json")
			f := stubFileOf(t, dir)
			got, err := AnalyzeStubs(sp, f)
			if err != nil {
				t.Fatalf("AnalyzeStubs: %v", err)
			}
			for i := range want {
				want[i].Path = f.Path
			}
			g, w := sortedFacts(got), sortedFacts(want)
			if jsonOf(g) != jsonOf(w) {
				t.Errorf("stubs = %s\nwant    %s", jsonOf(g), jsonOf(w))
			}
		})
	}
}

// 22-AC-4 / 22-R-3: a file that cannot be analyzed is an error, not an empty (clean) result.
func TestAnalyzeStubs_UnparsableGo_FailsClosed_22AC4(t *testing.T) {
	sp, err := LoadStubPolicy(platformPolicy(t).Verification)
	if err != nil {
		t.Fatalf("LoadStubPolicy: %v", err)
	}
	_, err = AnalyzeStubs(sp, StubFile{Path: "internal/x/x.go", Language: "go", Head: []byte("package x\nfunc (\n")})
	if err == nil {
		t.Error("an unparsable Go file was analyzed without error")
	}
	_, err = AnalyzeStubs(sp, StubFile{Path: "x.cob", Language: "cobol", Head: []byte("DISPLAY 'HI'.\n")})
	if err == nil {
		t.Error("a language without a stub form was analyzed without error")
	}
}
