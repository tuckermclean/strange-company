package verify

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// CheckIngestion on adversarial diffs built as real Git history (plan 22 sections 14.6 and 14.11; 22-AC-8,
// 22-AC-31). Every test pairs its violating cases with passing twins in the same repository, so a library that
// always (or never) flags cannot pass.

// protectedProbe lists paths of the platform protected set (policy/verification.yaml spec.protectedPaths) and
// look-alikes that are not protected, with the unusual spellings of section 14.11 (unicode, leading dash, newline).
var (
	protectedProbe = []string{
		".woodpecker/ci.yaml", ".woodpecker/deep/x/y.yml", "AGENTS.md", "pkg/sub/AGENTS.md", "-x/AGENTS.md", "CLAUDE.md",
		"docs/é/CLAUDE.md", "a\nb/AGENTS.md", ".claude/x/y.md", "x/.claude/settings.json", ".codex/c.toml", ".opencode/o.json",
		"policy/verification.yaml", "contracts/a.yaml", "CODEOWNERS", "docs/CODEOWNERS", ".forgejo/w.yaml", ".gitea/w.yaml",
		".github/workflows/x.yml", ".gitmodules", ".gitattributes", ".sops.yaml", "secrets/a.sops.yaml", "forge/x.yaml",
		"gitops/a/generated/b.yaml", "gitops/platform/admission/p.yaml", ".strange-company/policy.yaml",
	}
	notProtectedProbe = []string{
		".woodpeckerx/ci.yaml", "x/.woodpecker/ci.yaml", "agents.md", "AGENTS.md.bak", "docs/AGENTS_MD", "claude.md", ".claudex/y",
		".claude.json", "policy.yaml", "docs/policy/x.md", "contracts.md", "src/forge/x.go", "x.sops.yaml.bak", "gitops/a/generatedx/b.yaml",
		"docs/CODEOWNERS.md", ".github.md", ".gitmodules.bak", "sp ace/é.txt", "-dash/y.txt", "a\nb/c.txt",
	}
)

// 22-AC-31 (protected paths from the Runner's own derivation): adding, modifying and deleting a protected path is
// flagged on that path, byte for byte, and a look-alike is not.
func TestCheckIngestion_ProtectedPathsAddModifyDelete_22AC31(t *testing.T) {
	all := append(append([]string(nil), protectedProbe...), notProtectedProbe...)
	for _, mode := range []string{"added", "modified", "deleted"} {
		t.Run(mode, func(t *testing.T) {
			s := newScratch(t)
			s.write("pkg/core_test.go", s3CoreTest)
			if mode != "added" {
				for _, p := range all {
					s.write(p, "1\n")
				}
			}
			red := s.commit("red")
			for _, p := range all {
				switch mode {
				case "deleted":
					s.remove(p)
				default:
					s.write(p, "2\n")
				}
			}
			head := s.commit("head")
			got, err := CheckIngestion(s.ingestionInput(t, "implementer", red, head, s.s3Manifest(red, "pkg/core_test.go")))
			if err != nil {
				t.Fatal(err)
			}
			var want []string
			for _, p := range protectedProbe {
				want = append(want, protectedKey(p))
			}
			if g := violationKeys(got); !equalStrings(g, sortedCopy(want)) {
				t.Fatalf("got %q\nwant %q", g, sortedCopy(want))
			}
		})
	}
}

// 22-AC-31: a rename out of .woodpecker/ (both the old and the new path are checked), a chmod +x of a protected
// script, a file to symlink type change of a protected file, and a symlink into a protected directory.
func TestCheckIngestion_ProtectedRenameModeAndTypeChanges_22AC31(t *testing.T) {
	type tc struct {
		name  string
		base  func(s *scratch)
		head  func(s *scratch)
		want  []string
		extra bool
	}
	body := "line one\nline two\nline three\nline four\n"
	cases := []tc{
		{"rename_out_of_woodpecker",
			func(s *scratch) { s.write(".woodpecker/ci.yaml", body) },
			func(s *scratch) { s.rename(".woodpecker/ci.yaml", "ci.yaml") },
			[]string{protectedKey(".woodpecker/ci.yaml")}, false},
		{"rename_into_woodpecker",
			func(s *scratch) { s.write("prod/ci.yaml", body) },
			func(s *scratch) { s.rename("prod/ci.yaml", ".woodpecker/ci.yaml") },
			[]string{protectedKey(".woodpecker/ci.yaml")}, false},
		{"rename_within_woodpecker",
			func(s *scratch) { s.write(".woodpecker/a.yaml", body) },
			func(s *scratch) { s.rename(".woodpecker/a.yaml", ".woodpecker/b.yaml") },
			[]string{protectedKey(".woodpecker/a.yaml"), protectedKey(".woodpecker/b.yaml")}, false},
		{"chmod_plus_x_protected_script",
			func(s *scratch) { s.write(".woodpecker/verify.sh", "#!/bin/sh\necho ok\n") },
			func(s *scratch) { s.chmod(".woodpecker/verify.sh", 0o755) },
			[]string{protectedKey(".woodpecker/verify.sh")}, false},
		{"chmod_minus_x_protected_script",
			func(s *scratch) {
				s.write(".woodpecker/verify.sh", "#!/bin/sh\n")
				s.chmod(".woodpecker/verify.sh", 0o755)
			},
			func(s *scratch) { s.chmod(".woodpecker/verify.sh", 0o644) },
			[]string{protectedKey(".woodpecker/verify.sh")}, false},
		{"file_to_symlink_protected",
			func(s *scratch) { s.write(".woodpecker/ci.yaml", body) },
			func(s *scratch) { s.symlink(".woodpecker/ci.yaml", "../elsewhere.yaml") },
			[]string{protectedKey(".woodpecker/ci.yaml")}, false},
		{"symlink_to_file_protected",
			func(s *scratch) { s.symlink("policy/p.yaml", "../elsewhere.yaml") },
			func(s *scratch) { s.write("policy/p.yaml", body) },
			[]string{protectedKey("policy/p.yaml")}, false},
		{"new_symlink_in_protected_dir",
			func(s *scratch) { s.write("keep.txt", "1\n") },
			func(s *scratch) { s.symlink(".github/workflows", "../pkg") },
			[]string{protectedKey(".github/workflows")}, false},
		// Passing twins: the same operations outside the protected set are not violations.
		{"twin_rename_production",
			func(s *scratch) { s.write("docs/a.md", body) },
			func(s *scratch) { s.rename("docs/a.md", "docs/b.md") }, nil, false},
		{"twin_chmod_production_script",
			func(s *scratch) { s.write("scripts/run.sh", "#!/bin/sh\n") },
			func(s *scratch) { s.chmod("scripts/run.sh", 0o755) }, nil, false},
		{"twin_rename_look_alike_dir",
			func(s *scratch) { s.write(".woodpeckerx/ci.yaml", body) },
			func(s *scratch) { s.rename(".woodpeckerx/ci.yaml", "ci.yaml") }, nil, false},
		{"twin_new_symlink_in_production_dir",
			func(s *scratch) { s.write("keep.txt", "1\n") },
			func(s *scratch) { s.symlink("docs/link", "../README") }, nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newScratch(t)
			s.write("pkg/core_test.go", s3CoreTest)
			c.base(s)
			red := s.commit("red")
			c.head(s)
			head := s.commit("head")
			got, err := CheckIngestion(s.ingestionInput(t, "implementer", red, head, s.s3Manifest(red, "pkg/core_test.go")))
			if err != nil {
				t.Fatal(err)
			}
			if g := violationKeys(got); !equalStrings(g, sortedCopy(c.want)) {
				t.Fatalf("got %q\nwant %q", g, sortedCopy(c.want))
			}
		})
	}
}

// The repository policy's own protected paths (effective /spec/protectedPaths) are enforced next to the platform
// ones; a look-alike prefix is not protected.
func TestCheckIngestion_RepositoryProtectedPaths_22AC31(t *testing.T) {
	s := newScratch(t)
	s.write("pkg/core_test.go", s3CoreTest)
	s.write("private/a.txt", "1\n")
	s.write("privateer/a.txt", "1\n")
	red := s.commit("red")
	s.write("private/a.txt", "2\n")
	s.write("privateer/a.txt", "2\n")
	s.write("private/new.txt", "2\n")
	head := s.commit("head")
	got, err := CheckIngestion(s.ingestionInput(t, "implementer", red, head, s.s3Manifest(red, "pkg/core_test.go"), "private/**"))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{protectedKey("private/a.txt"), protectedKey("private/new.txt")}
	if g := violationKeys(got); !equalStrings(g, want) {
		t.Fatalf("got %q, want %q", g, want)
	}
}

// 22-AC-31 (gitlinks): mode 160000 is denied in every autonomous diff (the repository policy schema has no
// allowGitlinks, so there is no way to allow it): a new gitlink, a bumped one, a removed one, file to gitlink and
// gitlink to file. A gitlink that is unchanged between start and head is not a violation.
func TestCheckIngestion_Gitlinks_22AC31(t *testing.T) {
	s := newScratch(t)
	s.write("pkg/core_test.go", s3CoreTest)
	s.write("src/x", "was a file\n")
	s.write("docs/readme.md", "1\n")
	s.setGitlink("libs/bumped", s3Gitlink)
	s.setGitlink("libs/removed", s3Gitlink)
	s.setGitlink("libs/untouched", s3Gitlink)
	s.setGitlink("libs/becomes_file", s3Gitlink)
	red := s.commit("red")
	s.setGitlink("libs/new", s3Gitlink)
	s.setGitlink("libs/bumped", "89abcdef0123456789abcdef0123456789abcdef")
	s.dropGitlink("libs/removed")
	s.setGitlink("src/x", s3Gitlink)
	s.dropGitlink("libs/becomes_file")
	s.write("libs/becomes_file", "now a file\n")
	s.write("docs/readme.md", "2\n")
	head := s.commit("head")
	got, err := CheckIngestion(s.ingestionInput(t, "implementer", red, head, s.s3Manifest(red, "pkg/core_test.go")))
	if err != nil {
		t.Fatal(err)
	}
	var want []string
	for _, p := range []string{"libs/new", "libs/bumped", "libs/removed", "src/x", "libs/becomes_file"} {
		want = append(want, "GITLINK_DENIED "+p)
	}
	if g := violationKeys(got); !equalStrings(g, sortedCopy(want)) {
		t.Fatalf("got %q\nwant %q", g, sortedCopy(want))
	}
	// Twin: no gitlink change in the range, only an ordinary edit.
	next := s.commit("noop")
	s.write("docs/readme.md", "3\n")
	last := s.commit("edit")
	got, err = CheckIngestion(s.ingestionInput(t, "implementer", next, last, s.s3Manifest(red, "pkg/core_test.go")))
	if err != nil || len(got) != 0 {
		t.Fatalf("range without gitlink change: %v %v", got, err)
	}
}

// 22-AC-31 (DIFF_TOO_LARGE): the range is rejected before any other rule runs; at the limit the same range is
// evaluated and flags everything.
func TestCheckIngestion_DiffTooLargeBeforeAnyRule_22AC31(t *testing.T) {
	const n = 61
	s := newScratch(t)
	s.write("pkg/core_test.go", s3CoreTest)
	s.write("policy/x.yaml", "1\n")
	for i := 0; i < n-2; i++ {
		s.write(fmt.Sprintf("src/f%03d.go", i), "package src\n")
	}
	red := s.commit("red")
	s.write("policy/x.yaml", "2\n")
	s.write("pkg/core_test.go", s3CoreTest+"\n")
	for i := 0; i < n-2; i++ {
		s.write(fmt.Sprintf("src/f%03d.go", i), "package src\n\nvar X = 1\n")
	}
	head := s.commit("head")
	in := s.ingestionInput(t, "implementer", red, head, s.s3Manifest(red, "pkg/core_test.go"))
	for _, max := range []int{n - 1, 50, 0} {
		in.MaxFilesChanged = max
		got, err := CheckIngestion(in)
		var de *DiffError
		if !errors.As(err, &de) || de.Code != "DIFF_TOO_LARGE" {
			t.Fatalf("max %d: want DIFF_TOO_LARGE, got %v", max, err)
		}
		if len(got) != 0 {
			t.Errorf("max %d: %d violations returned next to DIFF_TOO_LARGE", max, len(got))
		}
	}
	for _, max := range []int{n, n + 1, 2000} {
		in.MaxFilesChanged = max
		got, err := CheckIngestion(in)
		if err != nil {
			t.Fatalf("max %d: %v", max, err)
		}
		want := []string{protectedKey("policy/x.yaml"), tamper("manifest_file_changed", "pkg/core_test.go")}
		if g := violationKeys(got); !equalStrings(g, sortedCopy(want)) {
			t.Fatalf("max %d: got %q, want %q", max, g, want)
		}
	}
}

// 22-AC-31: a commit changing far more files than one Forgejo API page is judged on every file: violations that
// sort last (a protected path, a manifest file, a skip marker in a new test, a gitlink) are all found.
func TestCheckIngestionManyFiles_EveryViolationSeen_22AC31(t *testing.T) {
	const n = 130
	s := newScratch(t)
	s.write("pkg/zz_test.go", s3CoreTest)
	s.write("zz/policy.txt", "1\n")
	s.write("policy/zz.yaml", "1\n")
	for i := 0; i < n; i++ {
		s.write(fmt.Sprintf("src/a%03d.go", i), "package src\n")
	}
	red := s.commit("red")
	for i := 0; i < n; i++ {
		s.write(fmt.Sprintf("src/a%03d.go", i), "package src\n\nvar X = 1\n")
	}
	s.write("policy/zz.yaml", "2\n")
	s.write("pkg/zz_test.go", s3CoreTest+"\n")
	s.write("zzz/new_test.go", "package zzz\n\nimport \"testing\"\n\nfunc TestZ(t *testing.T) {\n\tt.Skip(\"later\")\n}\n")
	s.setGitlink("zzzz/sub", s3Gitlink)
	head := s.commit("head")
	got, err := CheckIngestion(s.ingestionInput(t, "implementer", red, head, s.s3Manifest(red, "pkg/zz_test.go")))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{protectedKey("policy/zz.yaml"), tamper("manifest_file_changed", "pkg/zz_test.go"), tamper("skip_marker_added", "zzz/new_test.go"), "GITLINK_DENIED zzzz/sub"}
	if g := violationKeys(got); !equalStrings(g, sortedCopy(want)) {
		t.Fatalf("got %q, want %q", g, sortedCopy(want))
	}
}

// 22-AC-31: the diff is the tree difference Start..Head, so a change made only in the resolution of a merge commit
// is found; the same merge without it is clean.
func TestCheckIngestion_MergeCommitResolution_22AC31(t *testing.T) {
	for _, evil := range []bool{true, false} {
		t.Run(fmt.Sprintf("evil_%v", evil), func(t *testing.T) {
			s := newScratch(t)
			s.write("pkg/core_test.go", s3CoreTest)
			s.write(".woodpecker/ci.yaml", "steps: []\n")
			s.write("src/a.go", "package src\n")
			s.write("src/b.go", "package src\n")
			red := s.commit("red")
			s.git("checkout", "-q", "-b", "side")
			s.write("src/a.go", "package src\n\nvar A = 1\n")
			s.commit("side")
			s.git("checkout", "-q", "main")
			s.write("src/b.go", "package src\n\nvar B = 1\n")
			s.commit("main")
			s.git("merge", "-q", "--no-commit", "--no-ff", "side")
			if evil {
				s.write(".woodpecker/ci.yaml", "steps: [skip]\n")
			}
			head := s.commit("merge")
			if len(s.git("rev-list", "--merges", red+".."+head)) == 0 {
				t.Fatal("precondition: head is not a merge commit")
			}
			got, err := CheckIngestion(s.ingestionInput(t, "implementer", red, head, s.s3Manifest(red, "pkg/core_test.go")))
			if err != nil {
				t.Fatal(err)
			}
			var want []string
			if evil {
				want = []string{protectedKey(".woodpecker/ci.yaml")}
			}
			if g := violationKeys(got); !equalStrings(g, want) {
				t.Fatalf("got %q, want %q", g, want)
			}
		})
	}
}

// 22-AC-8 (roles): CheckIngestion judges implementer and producer diffs; any other role is an error, never an
// empty (passing) result.
func TestCheckIngestion_Roles_22AC8(t *testing.T) {
	s := newScratch(t)
	s.write("pkg/core_test.go", s3CoreTest)
	red := s.commit("red")
	s.write("src/a.go", "package src\n")
	head := s.commit("head")
	man := s.s3Manifest(red, "pkg/core_test.go")
	for _, role := range []string{"implementer", "producer"} {
		got, err := CheckIngestion(s.ingestionInput(t, role, red, head, man))
		if err != nil || len(got) != 0 {
			t.Errorf("role %s: %v %v", role, got, err)
		}
	}
	for _, role := range []string{"", "tester", "reviewer", "researcher", "IMPLEMENTER", "implementer "} {
		got, err := CheckIngestion(s.ingestionInput(t, role, red, head, man))
		if err == nil {
			t.Errorf("role %q accepted: %v", role, got)
		}
	}
}

// 22-AC-8: the output is deterministic and sorted by (Path, Code, SubCode, Line); two runs are identical.
func TestCheckIngestion_DeterministicAndSorted_22AC8(t *testing.T) {
	s := newScratch(t)
	s.write("pkg/core_test.go", s3CoreTest)
	s.write("pkg/old_test.go", s3CoreTest)
	s.write("testdata/g.golden", "g\n")
	s.write("app/main.py", "x = 1\n")
	red := s.commit("red")
	s.remove("pkg/core_test.go")
	s.write("pkg/old_test.go", s3CoreTest+"\n")
	s.write("testdata/g.golden", "h\n")
	s.write("app/main.py", "import os\nx = os.environ.get('PYTEST_CURRENT_TEST')\n")
	s.write("tests/test_new.py", "import pytest\n\n\n@pytest.mark.skip\ndef test_a():\n    pass\n\n\n@pytest.mark.xfail\ndef test_b():\n    pass\n")
	s.write(".woodpecker/ci.yaml", "x\n")
	head := s.commit("head")
	in := s.ingestionInput(t, "implementer", red, head, s.s3Manifest(red, "pkg/core_test.go"))
	first, err := CheckIngestion(in)
	if err != nil {
		t.Fatal(err)
	}
	second, err := CheckIngestion(in)
	if err != nil || !reflect.DeepEqual(first, second) {
		t.Fatalf("not deterministic:\n%v\n%v\n%v", first, second, err)
	}
	if len(first) < 7 {
		t.Fatalf("expected at least 7 violations, got %v", first)
	}
	if !sort.SliceIsSorted(first, func(i, j int) bool {
		a, b := first[i], first[j]
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		if a.Code != b.Code {
			return a.Code < b.Code
		}
		if a.SubCode != b.SubCode {
			return a.SubCode < b.SubCode
		}
		return a.Line < b.Line
	}) {
		t.Errorf("violations are not sorted by (Path, Code, SubCode, Line): %v", first)
	}
	// Both skip markers of the new file are reported, with their own line numbers.
	var lines []int
	for _, v := range first {
		if v.SubCode == "skip_marker_added" && v.Path == "tests/test_new.py" {
			lines = append(lines, v.Line)
		}
	}
	if !reflect.DeepEqual(lines, []int{4, 9}) {
		t.Errorf("skip marker lines %v, want [4 9]", lines)
	}
}

// 22-AC-8: only lines the range adds are judged, and existence is judged at the red commit. Range red..c1 adds a
// skip marker in the implementer's own new test (flagged); c1..c2 edits another line of it (clean, the marker is
// not an added line), a later c2..c3 adds a second marker (flagged at its line); an edit of a file that existed at
// the red commit is flagged even when the range starts after red.
func TestCheckIngestion_StartAfterRed_22AC8(t *testing.T) {
	s := newScratch(t)
	s.write("pkg/core_test.go", s3CoreTest)
	s.write("pkg/old_test.go", s3CoreTest)
	red := s.commit("red")
	man := s.s3Manifest(red, "pkg/core_test.go")
	mine := "package pkg\n\nimport \"testing\"\n\nfunc TestMine(t *testing.T) {\n\tt.Skip(\"later\")\n\t_ = 1\n}\n"
	s.write("pkg/mine_test.go", mine)
	c1 := s.commit("c1")
	s.write("pkg/mine_test.go", strings.Replace(mine, "_ = 1", "_ = 2", 1))
	c2 := s.commit("c2")
	s.write("pkg/mine_test.go", strings.Replace(mine, "_ = 1", "_ = 2", 1)+"\nfunc TestMore(t *testing.T) {\n\tt.Skipf(\"later\")\n}\n")
	c3 := s.commit("c3")
	s.write("pkg/old_test.go", s3CoreTest+"\n")
	c4 := s.commit("c4")

	check := func(name, from, to string, want []string, line int) {
		t.Run(name, func(t *testing.T) {
			got, err := CheckIngestion(s.ingestionInput(t, "implementer", from, to, man))
			if err != nil {
				t.Fatal(err)
			}
			if g := violationKeys(got); !equalStrings(g, sortedCopy(want)) {
				t.Fatalf("got %q, want %q", g, want)
			}
			for _, v := range got {
				if line > 0 && v.SubCode == "skip_marker_added" && v.Line != line {
					t.Errorf("line %d, want %d", v.Line, line)
				}
			}
		})
	}
	// The manifest's TestCommit is the red commit throughout.
	check("red_to_c1_flags_marker", red, c1, []string{tamper("skip_marker_added", "pkg/mine_test.go")}, 6)
	check("c1_to_c2_marker_not_added", c1, c2, nil, 0)
	check("c2_to_c3_second_marker", c2, c3, []string{tamper("skip_marker_added", "pkg/mine_test.go")}, 11)
	check("c3_to_c4_old_test_existed_at_red", c3, c4, []string{tamper("existing_test_modified", "pkg/old_test.go")}, 0)
}

// 22-AC-8: diff text parsing is by hunk, not by line prefix: an added line whose text starts with `++ ` (so the diff
// shows `+++ `), and a last line without a line feed, still carry their markers.
func TestCheckIngestion_AddedLineParsing_22AC8(t *testing.T) {
	s := newScratch(t)
	s.write("pkg/core_test.go", s3CoreTest)
	red := s.commit("red")
	s.write("pkg/sneaky_test.go", "package pkg\n\n/*\n++ b/zz.go\n++ t.Skip(\"sneaky\")\n*/\n")
	s.write("pkg/nonl_test.go", "package pkg\n\nimport \"testing\"\n\nfunc TestN(t *testing.T) {\n\tt.SkipNow()\n}")
	s.write("pkg/crlf_test.go", "package pkg\r\n\r\nimport \"testing\"\r\n\r\nfunc TestC(t *testing.T) {\r\n\tt.Skip(\"x\")\r\n}\r\n")
	head := s.commit("head")
	got, err := CheckIngestion(s.ingestionInput(t, "implementer", red, head, s.s3Manifest(red, "pkg/core_test.go")))
	if err != nil {
		t.Fatal(err)
	}
	lines := map[string]int{}
	for _, v := range got {
		if v.SubCode == "skip_marker_added" {
			lines[v.Path] = v.Line
		}
	}
	want := map[string]int{"pkg/sneaky_test.go": 5, "pkg/nonl_test.go": 6, "pkg/crlf_test.go": 6}
	if !reflect.DeepEqual(lines, want) {
		t.Fatalf("skip marker lines %v, want %v (all violations: %v)", lines, want, got)
	}
}

// 22-AC-8 and 22-AC-31: hostile configuration of the mirror (ignored submodules, an external diff program, a
// committed `-diff` attribute that makes plain git print "Binary files differ") cannot hide a violation or run code.
func TestCheckIngestion_HostileGitConfig_22AC8(t *testing.T) {
	s := newScratch(t)
	marker := filepath.Join(t.TempDir(), "ran")
	script := filepath.Join(t.TempDir(), "ext.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho hit >> "+marker+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	s.write(".gitattributes", "*_test.go -diff\n")
	s.write("pkg/core_test.go", s3CoreTest)
	s.write(".woodpecker/ci.yaml", "steps: []\n")
	s.setGitlink("libs/sub", s3Gitlink)
	red := s.commit("red")
	s.write(".woodpecker/ci.yaml", "steps: [x]\n")
	s.write("pkg/new_test.go", "package pkg\n\nimport \"testing\"\n\nfunc TestNew(t *testing.T) {\n\tt.Skip(\"later\")\n}\n")
	s.setGitlink("libs/sub", "89abcdef0123456789abcdef0123456789abcdef")
	head := s.commit("head")
	if out := s.git("diff", "-U0", red, head, "--", "pkg/new_test.go"); !strings.Contains(out, "Binary files") {
		t.Fatalf("precondition: -diff attribute should blind plain git: %q", out)
	}
	s.git("config", "diff.ignoreSubmodules", "all")
	s.git("config", "diff.external", script)
	s.git("config", "diff.renames", "copies")
	t.Setenv("GIT_EXTERNAL_DIFF", script)
	got, err := CheckIngestion(s.ingestionInput(t, "implementer", red, head, s.s3Manifest(red, "pkg/core_test.go")))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{protectedKey(".woodpecker/ci.yaml"), tamper("skip_marker_added", "pkg/new_test.go"), "GITLINK_DENIED libs/sub"}
	if g := violationKeys(got); !equalStrings(g, sortedCopy(want)) {
		t.Fatalf("got %q, want %q", g, sortedCopy(want))
	}
	if _, err := os.Stat(marker); err == nil {
		t.Errorf("a configured diff program ran")
	}
}

// 22-AC-8: binary files are compared by blob id: a modified binary fixture is existing_test_modified, a modified
// binary manifest file manifest_file_changed; an identical rewrite is clean; an empty new test file is clean.
func TestCheckIngestion_BinaryFiles_22AC8(t *testing.T) {
	s := newScratch(t)
	s.write("pkg/core_test.go", s3CoreTest)
	s.writeBytes("testdata/blob.bin", []byte{0, 1, 2, 3, 0})
	s.writeBytes("testdata/same.bin", []byte{0, 9, 9})
	s.writeBytes("testdata/m.bin", []byte{0, 7, 7, 0})
	red := s.commit("red")
	s.writeBytes("testdata/blob.bin", []byte{0, 1, 2, 4, 0})
	s.writeBytes("testdata/same.bin", []byte{0, 9, 9})
	s.writeBytes("testdata/m.bin", []byte{0, 7, 8, 0})
	s.write("pkg/empty_test.go", "")
	head := s.commit("head")
	man := s.s3Manifest(red, "pkg/core_test.go", "testdata/m.bin")
	got, err := CheckIngestion(s.ingestionInput(t, "implementer", red, head, man))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{tamper("existing_test_modified", "testdata/blob.bin"), tamper("manifest_file_changed", "testdata/m.bin")}
	if g := violationKeys(got); !equalStrings(g, sortedCopy(want)) {
		t.Fatalf("got %q, want %q", g, sortedCopy(want))
	}
}
