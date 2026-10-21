package verify

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// Git diff derivation (plan 22 section 14.11, 22-AC-31): the Runner's own `git diff --raw -z --no-renames`
// derivation, run on real scratch repositories. Each scenario has its passing twin (a no-op or ordinary change that
// must produce exactly the expected entries and nothing else).

type diffWant struct {
	status, path, oldMode, newMode string
}

type diffScenario struct {
	name string
	base func(s *scratch)
	head func(s *scratch)
	want []diffWant
}

var s3UnusualPaths = []string{
	"sp ace.txt", "uni/é☃.txt", "-dash.txt", "nl\nname.txt", "tab\tname.txt", "q\"uote.txt", `back\slash.txt`,
	"dots/..hidden", "a..b/c.txt", ":(exclude)magic.txt", "star*.txt", "-rf/x.txt", "pct%20x.txt",
}

func s3DiffScenarios() []diffScenario {
	return []diffScenario{
		{name: "add_modify_delete",
			base: func(s *scratch) { s.write("m.txt", "1\n"); s.write("d.txt", "1\n"); s.write("keep.txt", "1\n") },
			head: func(s *scratch) { s.write("m.txt", "2\n"); s.remove("d.txt"); s.write("a.txt", "1\n") },
			want: []diffWant{{"A", "a.txt", "000000", "100644"}, {"D", "d.txt", "100644", "000000"}, {"M", "m.txt", "100644", "100644"}}},
		{name: "no_change_twin",
			base: func(s *scratch) { s.write("a.txt", "1\n") },
			head: func(s *scratch) {},
			want: nil},
		{name: "rename_identical_content_is_delete_plus_add",
			base: func(s *scratch) { s.write("old/name.txt", "same content\nline two\nline three\n") },
			head: func(s *scratch) { s.rename("old/name.txt", "new/name.txt") },
			want: []diffWant{{"D", "old/name.txt", "100644", "000000"}, {"A", "new/name.txt", "000000", "100644"}}},
		{name: "rename_with_edit",
			base: func(s *scratch) { s.write("old.txt", "one\ntwo\nthree\nfour\nfive\nsix\n") },
			head: func(s *scratch) {
				s.rename("old.txt", "new.txt")
				s.write("new.txt", "one\ntwo\nthree\nfour\nfive\nSIX\n")
			},
			want: []diffWant{{"A", "new.txt", "000000", "100644"}, {"D", "old.txt", "100644", "000000"}}},
		{name: "copy_is_only_an_add",
			base: func(s *scratch) { s.write("orig.txt", "same content\nline two\nline three\n") },
			head: func(s *scratch) { s.copyFile("orig.txt", "copy.txt") },
			want: []diffWant{{"A", "copy.txt", "000000", "100644"}}},
		{name: "chmod_plus_x",
			base: func(s *scratch) { s.write("run.sh", "#!/bin/sh\n") },
			head: func(s *scratch) { s.chmod("run.sh", 0o755) },
			want: []diffWant{{"M", "run.sh", "100644", "100755"}}},
		{name: "chmod_minus_x",
			base: func(s *scratch) { s.write("run.sh", "#!/bin/sh\n"); s.chmod("run.sh", 0o755) },
			head: func(s *scratch) { s.chmod("run.sh", 0o644) },
			want: []diffWant{{"M", "run.sh", "100755", "100644"}}},
		{name: "file_to_symlink_is_type_change",
			base: func(s *scratch) { s.write("f.txt", "content\n") },
			head: func(s *scratch) { s.symlink("f.txt", "elsewhere") },
			want: []diffWant{{"T", "f.txt", "100644", "120000"}}},
		{name: "symlink_to_file_is_type_change",
			base: func(s *scratch) { s.symlink("f.txt", "elsewhere") },
			head: func(s *scratch) { s.write("f.txt", "content\n") },
			want: []diffWant{{"T", "f.txt", "120000", "100644"}}},
		{name: "symlink_retarget",
			base: func(s *scratch) { s.symlink("l", "one") },
			head: func(s *scratch) { s.symlink("l", "two") },
			want: []diffWant{{"M", "l", "120000", "120000"}}},
		{name: "gitlink_added",
			base: func(s *scratch) { s.write("a.txt", "1\n") },
			head: func(s *scratch) { s.setGitlink("vendor/sub", s3Gitlink) },
			want: []diffWant{{"A", "vendor/sub", "000000", "160000"}}},
		{name: "gitlink_bumped",
			base: func(s *scratch) { s.setGitlink("sub", s3Gitlink) },
			head: func(s *scratch) { s.setGitlink("sub", "89abcdef0123456789abcdef0123456789abcdef") },
			want: []diffWant{{"M", "sub", "160000", "160000"}}},
		{name: "gitlink_removed",
			base: func(s *scratch) { s.setGitlink("sub", s3Gitlink) },
			head: func(s *scratch) { s.dropGitlink("sub") },
			want: []diffWant{{"D", "sub", "160000", "000000"}}},
		{name: "file_to_gitlink_is_type_change",
			base: func(s *scratch) { s.write("sub", "was a file\n") },
			head: func(s *scratch) { s.setGitlink("sub", s3Gitlink) },
			want: []diffWant{{"T", "sub", "100644", "160000"}}},
		{name: "empty_file_added_and_filled",
			base: func(s *scratch) { s.write("fill.txt", "") },
			head: func(s *scratch) { s.write("empty.txt", ""); s.write("fill.txt", "x\n") },
			want: []diffWant{{"A", "empty.txt", "000000", "100644"}, {"M", "fill.txt", "100644", "100644"}}},
		{name: "binary_modified_and_added",
			base: func(s *scratch) { s.writeBytes("b.bin", []byte{0, 1, 2, 3, 0, 255}) },
			head: func(s *scratch) {
				s.writeBytes("b.bin", []byte{0, 1, 2, 4, 0, 255})
				s.writeBytes("c.bin", []byte{0, 0, 0})
			},
			want: []diffWant{{"M", "b.bin", "100644", "100644"}, {"A", "c.bin", "000000", "100644"}}},
		{name: "case_only_rename",
			base: func(s *scratch) { s.write("pkg/Old_test.go", "package pkg\n") },
			head: func(s *scratch) { s.rename("pkg/Old_test.go", "pkg/old_test.go") },
			want: []diffWant{{"D", "pkg/Old_test.go", "100644", "000000"}, {"A", "pkg/old_test.go", "000000", "100644"}}},
		{name: "line_ending_only_change",
			base: func(s *scratch) { s.write("e.txt", "a\nb\n") },
			head: func(s *scratch) { s.write("e.txt", "a\r\nb\r\n") },
			want: []diffWant{{"M", "e.txt", "100644", "100644"}}},
		{name: "unusual_paths_added",
			base: func(s *scratch) { s.write("keep.txt", "1\n") },
			head: func(s *scratch) {
				for _, p := range s3UnusualPaths {
					s.write(p, "1\n")
				}
			},
			want: wantAll("A", s3UnusualPaths, "000000", "100644")},
		{name: "unusual_paths_modified",
			base: func(s *scratch) {
				for _, p := range s3UnusualPaths {
					s.write(p, "1\n")
				}
			},
			head: func(s *scratch) {
				for _, p := range s3UnusualPaths {
					s.write(p, "2\n")
				}
			},
			want: wantAll("M", s3UnusualPaths, "100644", "100644")},
		{name: "unusual_paths_deleted",
			base: func(s *scratch) {
				for _, p := range s3UnusualPaths {
					s.write(p, "1\n")
				}
			},
			head: func(s *scratch) {
				for _, p := range s3UnusualPaths {
					s.remove(p)
				}
			},
			want: wantAll("D", s3UnusualPaths, "100644", "000000")},
	}
}

func wantAll(status string, paths []string, oldMode, newMode string) []diffWant {
	var out []diffWant
	for _, p := range paths {
		out = append(out, diffWant{status, p, oldMode, newMode})
	}
	return out
}

// checkEntries compares the library's entries with the wanted shape and with Git's own blob ids and modes
// (`git rev-parse` / `git ls-tree` in the same repository).
func checkEntries(t *testing.T, s *scratch, a, b string, got []DiffEntry, want []diffWant) {
	t.Helper()
	w := append([]diffWant(nil), want...)
	sort.Slice(w, func(i, j int) bool { return w[i].path < w[j].path })
	if len(got) != len(w) {
		t.Fatalf("got %d entries, want %d\ngot:  %+v\nwant: %+v", len(got), len(w), got, w)
	}
	for i := range w {
		g := got[i]
		if g.Path != w[i].path || g.Status != w[i].status || g.OldMode != w[i].oldMode || g.NewMode != w[i].newMode {
			t.Errorf("entry %d: got %+v, want %+v", i, g, w[i])
		}
		if wb := s.blobAt(a, g.Path); g.OldBlob != wb {
			t.Errorf("%q: old blob %s, git says %s", g.Path, g.OldBlob, wb)
		}
		if wb := s.blobAt(b, g.Path); g.NewBlob != wb {
			t.Errorf("%q: new blob %s, git says %s", g.Path, g.NewBlob, wb)
		}
	}
}

// 22-AC-31: the raw derivation reports renames as delete plus add, mode and type changes and gitlinks as entries,
// binary files by blob id, and unusual paths byte for byte.
func TestDeriveDiff_RawEntries_22AC31(t *testing.T) {
	for _, sc := range s3DiffScenarios() {
		t.Run(sc.name, func(t *testing.T) {
			s := newScratch(t)
			sc.base(s)
			a := s.commit("base")
			sc.head(s)
			b := s.commit("head")
			got, err := DeriveDiff(s.repo(), a, b, 2000)
			if err != nil {
				t.Fatalf("DeriveDiff: %v", err)
			}
			checkEntries(t, s, a, b, got, sc.want)
		})
	}
}

// 22-AC-31: the Runner reads a bare mirror; the entries of a mirror equal those of the worktree repository.
func TestDeriveDiff_BareMirrorSameEntries_22AC31(t *testing.T) {
	for _, sc := range s3DiffScenarios() {
		t.Run(sc.name, func(t *testing.T) {
			s := newScratch(t)
			sc.base(s)
			a := s.commit("base")
			sc.head(s)
			b := s.commit("head")
			bare := filepath.Join(t.TempDir(), "mirror.git")
			s.git("clone", "-q", "--bare", s.dir, bare)
			got, err := DeriveDiff(GitRepo{Dir: bare}, a, b, 2000)
			if err != nil {
				t.Fatalf("DeriveDiff on the bare mirror: %v", err)
			}
			checkEntries(t, s, a, b, got, sc.want)
		})
	}
}

// 22-AC-31: hostile repository configuration and environment (renames, external diff, submodule ignoring, quoting,
// colour) must not change or hide any entry. The preconditions run plain git to show each setting would.
func TestDeriveDiff_HostileConfigAndEnv_22AC31(t *testing.T) {
	s := newScratch(t)
	marker := filepath.Join(t.TempDir(), "external-diff-ran")
	script := filepath.Join(t.TempDir(), "ext.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho hit >> "+marker+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	s.write("old.txt", "one\ntwo\nthree\nfour\nfive\nsix\nseven\n")
	s.write("r.sh", "#!/bin/sh\n")
	s.write("tracked/é.txt", "1\n")
	s.setGitlink("sub", s3Gitlink)
	s.write(".gitmodules", "[submodule \"sub\"]\n\tpath = sub\n\turl = ../sub\n\tignore = all\n")
	a := s.commit("base")
	s.rename("old.txt", "new.txt")
	s.chmod("r.sh", 0o755)
	s.write("tracked/é.txt", "2\n")
	s.setGitlink("sub", "89abcdef0123456789abcdef0123456789abcdef")
	b := s.commit("head")
	want := []diffWant{{"A", "new.txt", "000000", "100644"}, {"D", "old.txt", "100644", "000000"},
		{"M", "r.sh", "100644", "100755"}, {"M", "sub", "160000", "160000"}, {"M", "tracked/é.txt", "100644", "100644"}}

	for k, v := range map[string]string{"diff.renames": "copies", "diff.external": script, "diff.ignoreSubmodules": "all",
		"core.quotePath": "true", "color.ui": "always", "diff.noprefix": "true", "diff.algorithm": "histogram", "status.submoduleSummary": "true"} {
		s.git("config", k, v)
	}
	t.Setenv("GIT_EXTERNAL_DIFF", script)
	// Precondition: plain git with these settings hides the gitlink change.
	plain := s.git("diff", "--raw", "-z", a, b)
	if strings.Contains(plain, "sub\x00") || strings.Contains(plain, "\tsub") {
		t.Fatalf("precondition: diff.ignoreSubmodules=all should hide the gitlink in plain git, got %q", plain)
	}

	got, err := DeriveDiff(s.repo(), a, b, 2000)
	if err != nil {
		t.Fatalf("DeriveDiff: %v", err)
	}
	checkEntries(t, s, a, b, got, want)
	if _, err := os.Stat(marker); err == nil {
		t.Errorf("the external diff program ran")
	}
}

// 22-AC-31 (DIFF_TOO_LARGE): the whole list is read, never truncated. A range over the role's maxFilesChanged is
// rejected before any entry is used; at the limit it is accepted (passing twin). A rename counts as two entries.
func TestDeriveDiff_TooLarge_22AC31(t *testing.T) {
	const n = 61 // more than one Forgejo API page (50)
	s := newScratch(t)
	s.write("keep.txt", "1\n")
	for i := 0; i < n; i++ {
		s.write(fmt.Sprintf("f/m%03d.txt", i), "1\n")
	}
	a := s.commit("base")
	for i := 0; i < n; i++ {
		s.write(fmt.Sprintf("f/m%03d.txt", i), "2\n")
	}
	b := s.commit("head")
	for _, tc := range []struct {
		max      int
		tooLarge bool
	}{{n, false}, {n + 1, false}, {2000, false}, {n - 1, true}, {50, true}, {1, true}, {0, true}} {
		t.Run(fmt.Sprintf("max_%d", tc.max), func(t *testing.T) {
			got, err := DeriveDiff(s.repo(), a, b, tc.max)
			var de *DiffError
			if tc.tooLarge {
				if !errors.As(err, &de) || de.Code != "DIFF_TOO_LARGE" {
					t.Fatalf("max %d with %d entries: want DIFF_TOO_LARGE, got %v", tc.max, n, err)
				}
				if len(got) != 0 {
					t.Errorf("DIFF_TOO_LARGE returned %d entries; no check may see a partial list", len(got))
				}
				return
			}
			if err != nil {
				t.Fatalf("max %d: %v", tc.max, err)
			}
			if len(got) != n {
				t.Fatalf("max %d: %d entries, want all %d", tc.max, len(got), n)
			}
		})
	}

	t.Run("empty_diff_with_max_zero", func(t *testing.T) {
		c := s.commit("empty")
		got, err := DeriveDiff(s.repo(), b, c, 0)
		if err != nil || len(got) != 0 {
			t.Fatalf("empty range at max 0: %v %v", got, err)
		}
	})
	t.Run("negative_max_is_an_error", func(t *testing.T) {
		if _, err := DeriveDiff(s.repo(), a, b, -1); err == nil {
			t.Fatal("negative maxFiles accepted")
		}
	})
	t.Run("rename_counts_twice", func(t *testing.T) {
		s := newScratch(t)
		for i := 0; i < 30; i++ {
			s.write(fmt.Sprintf("o/f%02d.txt", i), fmt.Sprintf("content %d\nsecond line\nthird line\n", i))
		}
		a := s.commit("base")
		for i := 0; i < 30; i++ {
			s.rename(fmt.Sprintf("o/f%02d.txt", i), fmt.Sprintf("n/f%02d.txt", i))
		}
		b := s.commit("head")
		var de *DiffError
		if _, err := DeriveDiff(s.repo(), a, b, 59); !errors.As(err, &de) || de.Code != "DIFF_TOO_LARGE" {
			t.Fatalf("30 renames are 60 entries, max 59: want DIFF_TOO_LARGE, got %v", err)
		}
		got, err := DeriveDiff(s.repo(), a, b, 60)
		if err != nil || len(got) != 60 {
			t.Fatalf("max 60: %d entries, %v", len(got), err)
		}
	})
}

// 22-AC-31: a commit changing more files than one Forgejo API page yields every entry, in path order.
func TestDeriveDiff_EveryEntrySeenBeyondOnePage_22AC31(t *testing.T) {
	const n = 130
	s := newScratch(t)
	s.write("keep.txt", "1\n")
	a := s.commit("base")
	var want []string
	for i := 0; i < n; i++ {
		p := fmt.Sprintf("d%d/f%03d.txt", i%7, i)
		s.write(p, "x\n")
		want = append(want, p)
	}
	b := s.commit("head")
	got, err := DeriveDiff(s.repo(), a, b, 2000)
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, e := range got {
		paths = append(paths, e.Path)
		if e.Status != "A" {
			t.Errorf("%s: status %s", e.Path, e.Status)
		}
	}
	if !equalStrings(paths, sortedCopy(want)) {
		t.Fatalf("entries %d, want %d; missing or extra entries", len(paths), n)
	}
}

// 22-AC-31: only 40-hex object ids are accepted as revisions; options, ranges, refs and abbreviations are errors,
// and no option is ever passed through to git (the output file must not appear).
func TestDeriveDiff_OnlyObjectIDs_22AC31(t *testing.T) {
	s := newScratch(t)
	s.write("a.txt", "1\n")
	a := s.commit("base")
	s.write("a.txt", "2\n")
	b := s.commit("head")
	pwn := filepath.Join(t.TempDir(), "pwn")
	for _, bad := range []string{"--output=" + pwn, "-h", "--no-index", a + " --", "", "main", "HEAD", "HEAD~1", a[:12], a + ".." + b,
		strings.ToUpper(a), "refs/heads/main", b + "^{tree}", "0000000000000000000000000000000000000000", strings.Repeat("e", 40)} {
		t.Run("head_"+strings.NewReplacer("/", "_", " ", "_", "-", "d", "=", "_").Replace(strings.ReplaceAll(bad, pwn, "PWN")), func(t *testing.T) {
			got, err := DeriveDiff(s.repo(), a, bad, 2000)
			if err == nil {
				t.Fatalf("revision %q accepted, entries %+v", bad, got)
			}
			var de *DiffError
			if errors.As(err, &de) && de.Code == "DIFF_TOO_LARGE" {
				t.Fatalf("revision %q: wrong error class %v", bad, err)
			}
		})
		t.Run("base_"+strings.NewReplacer("/", "_", " ", "_", "-", "d", "=", "_").Replace(strings.ReplaceAll(bad, pwn, "PWN")), func(t *testing.T) {
			if _, err := DeriveDiff(s.repo(), bad, b, 2000); err == nil {
				t.Fatalf("base revision %q accepted", bad)
			}
		})
	}
	if _, err := os.Stat(pwn); err == nil {
		t.Fatal("an option reached git: the --output file exists")
	}
	got, err := DeriveDiff(s.repo(), a, b, 2000)
	if err != nil || len(got) != 1 || got[0].Path != "a.txt" {
		t.Fatalf("twin with full object ids: %+v, %v", got, err)
	}
}

// 22-AC-31 (DIFF_UNVERIFIABLE): a compare response that disagrees with the Runner's own list, in either direction,
// is an infrastructure failure; an agreeing one (any order, renames listed with their previous name) is nil.
func TestCrossCheckCompare_22AC31(t *testing.T) {
	s := newScratch(t)
	for i := 0; i < 60; i++ {
		s.write(fmt.Sprintf("f/m%03d.txt", i), "1\n")
	}
	s.write("old/name.txt", "same content\nline two\nline three\n")
	s.write("run.sh", "#!/bin/sh\n")
	a := s.commit("base")
	for i := 0; i < 60; i++ {
		s.write(fmt.Sprintf("f/m%03d.txt", i), "2\n")
	}
	s.rename("old/name.txt", "new/name.txt")
	s.chmod("run.sh", 0o755)
	b := s.commit("head")
	entries, err := DeriveDiff(s.repo(), a, b, 2000)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 63 { // 60 modified + rename (2) + chmod
		t.Fatalf("DeriveDiff returned %d entries, want 63", len(entries))
	}
	// What a faithful Forgejo compare lists: every changed file once; a rename under its new name with the
	// previous name.
	var full []CompareFile
	for _, e := range entries {
		switch e.Path {
		case "old/name.txt":
			continue
		case "new/name.txt":
			full = append(full, CompareFile{Filename: "new/name.txt", PreviousFilename: "old/name.txt"})
		default:
			full = append(full, CompareFile{Filename: e.Path})
		}
	}
	mustOK := func(name string, c []CompareFile) {
		t.Run(name, func(t *testing.T) {
			if err := CrossCheckCompare(entries, c); err != nil {
				t.Fatalf("agreeing compare rejected: %v", err)
			}
		})
	}
	mustUnverifiable := func(name string, c []CompareFile) {
		t.Run(name, func(t *testing.T) {
			var de *DiffError
			if err := CrossCheckCompare(entries, c); !errors.As(err, &de) || de.Code != "DIFF_UNVERIFIABLE" {
				t.Fatalf("want DIFF_UNVERIFIABLE, got %v", err)
			}
		})
	}
	mustOK("faithful", full)
	rev := append([]CompareFile(nil), full...)
	for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
		rev[i], rev[j] = rev[j], rev[i]
	}
	mustOK("reordered", rev)
	mustUnverifiable("one_page_of_fifty", full[:50])
	mustUnverifiable("last_entry_missing", full[:len(full)-1])
	mustUnverifiable("first_entry_missing", full[1:])
	mustUnverifiable("empty_response", nil)
	mustUnverifiable("extra_file", append(append([]CompareFile(nil), full...), CompareFile{Filename: "not/in/diff.txt"}))
	var noPrev []CompareFile
	for _, c := range full {
		c.PreviousFilename = ""
		noPrev = append(noPrev, c)
	}
	mustUnverifiable("rename_without_previous_name", noPrev)
	var swapped []CompareFile
	for _, c := range full {
		if c.Filename == "run.sh" {
			c.Filename = "run.SH"
		}
		swapped = append(swapped, c)
	}
	mustUnverifiable("case_differs", swapped)
	t.Run("both_empty", func(t *testing.T) {
		if err := CrossCheckCompare(nil, nil); err != nil {
			t.Fatalf("empty and empty: %v", err)
		}
	})
	t.Run("empty_runner_list_nonempty_compare", func(t *testing.T) {
		var de *DiffError
		if err := CrossCheckCompare(nil, full[:1]); !errors.As(err, &de) || de.Code != "DIFF_UNVERIFIABLE" {
			t.Fatalf("want DIFF_UNVERIFIABLE, got %v", err)
		}
	})
}

// 22-AC-31: commit lists and ancestry come from Git (rev-list --reverse, merge-base --is-ancestor); merge commits are
// listed, parents precede children, and an unknown commit is an error, never "not an ancestor".
func TestRevListAndIsAncestor_22AC31(t *testing.T) {
	s := newScratch(t)
	s.write("a.txt", "1\n")
	base := s.commit("base")
	s.git("checkout", "-q", "-b", "side")
	s.write("side.txt", "1\n")
	side1 := s.commit("side1")
	s.write("side.txt", "2\n")
	side2 := s.commit("side2")
	s.git("checkout", "-q", "main")
	s.write("main.txt", "1\n")
	main1 := s.commit("main1")
	s.git("merge", "-q", "--no-ff", "--no-edit", "side")
	merge := s.git("rev-parse", "HEAD")
	s.write("after.txt", "1\n")
	after := s.commit("after")

	got, err := RevList(s.repo(), base, after)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{main1, side1, side2, merge, after}
	if !equalStrings(sortedCopy(got), sortedCopy(want)) {
		t.Fatalf("rev-list %v, want the set %v", got, want)
	}
	pos := map[string]int{}
	for i, c := range got {
		pos[c] = i
	}
	for _, p := range [][2]string{{side1, side2}, {side2, merge}, {main1, merge}, {merge, after}} {
		if pos[p[0]] >= pos[p[1]] {
			t.Errorf("%s must come before %s (oldest first): %v", p[0][:7], p[1][:7], got)
		}
	}
	if got, err := RevList(s.repo(), after, base); err != nil || len(got) != 0 {
		t.Errorf("reversed range: %v %v", got, err)
	}
	if got, err := RevList(s.repo(), after, after); err != nil || len(got) != 0 {
		t.Errorf("empty range: %v %v", got, err)
	}
	if _, err := RevList(s.repo(), base, "--all"); err == nil {
		t.Error("an option as revision was accepted")
	}
	if _, err := RevList(s.repo(), "main", after); err == nil {
		t.Error("a ref name as revision was accepted")
	}

	for _, tc := range []struct {
		a, b string
		want bool
	}{{base, after, true}, {side2, after, true}, {main1, after, true}, {after, base, false}, {side2, main1, false}, {main1, side2, false}, {after, after, true}} {
		ok, err := IsAncestor(s.repo(), tc.a, tc.b)
		if err != nil || ok != tc.want {
			t.Errorf("IsAncestor(%s, %s) = %v, %v; want %v", tc.a[:7], tc.b[:7], ok, err, tc.want)
		}
	}
	if ok, err := IsAncestor(s.repo(), strings.Repeat("e", 40), after); err == nil || ok {
		t.Errorf("unknown commit: ok=%v err=%v; want an error, not false", ok, err)
	}
	if ok, err := IsAncestor(s.repo(), "--help", after); err == nil || ok {
		t.Errorf("option as revision: ok=%v err=%v", ok, err)
	}
}

// 22-AC-31: git ls-tree -r -z: modes, blobs, symlinks, gitlinks and newline paths exactly, sorted by path.
func TestLsTree_22AC31(t *testing.T) {
	s := newScratch(t)
	s.write("a.txt", "1\n")
	s.write("bin/run.sh", "#!/bin/sh\n")
	s.chmod("bin/run.sh", 0o755)
	s.symlink("link", "a.txt")
	s.setGitlink("vendor/sub", s3Gitlink)
	s.write("nl\nname.txt", "x\n")
	s.write("sp ace/é.txt", "y\n")
	s.write("empty", "")
	head := s.commit("head")
	got, err := LsTree(s.repo(), head)
	if err != nil {
		t.Fatal(err)
	}
	wantPaths := []string{"a.txt", "bin/run.sh", "empty", "link", "nl\nname.txt", "sp ace/é.txt", "vendor/sub"}
	var gotPaths []string
	for _, e := range got {
		gotPaths = append(gotPaths, e.Path)
		if e.Blob != s.blobAt(head, e.Path) || e.Mode != s.modeAt(head, e.Path) {
			t.Errorf("%q: %s %s, git says %s %s", e.Path, e.Mode, e.Blob, s.modeAt(head, e.Path), s.blobAt(head, e.Path))
		}
	}
	if !equalStrings(gotPaths, wantPaths) {
		t.Fatalf("paths %q, want %q", gotPaths, wantPaths)
	}
	modes := map[string]string{}
	for _, e := range got {
		modes[e.Path] = e.Mode
	}
	for p, m := range map[string]string{"a.txt": "100644", "bin/run.sh": "100755", "link": "120000", "vendor/sub": "160000"} {
		if modes[p] != m {
			t.Errorf("%s: mode %q, want %q", p, modes[p], m)
		}
	}
	if _, err := LsTree(s.repo(), "--help"); err == nil {
		t.Error("an option as revision was accepted")
	}
	if _, err := LsTree(s.repo(), strings.Repeat("e", 40)); err == nil {
		t.Error("an unknown revision returned no error")
	}
}

// 22-AC-8 (diff text for the static checks, section 14.11 step 4): added lines with head line numbers, parsed from
// `git diff -U0`.
func TestAddedLines_Content_22AC8(t *testing.T) {
	type al = AddedLine
	cases := []struct {
		name       string
		base, head string
		want       []al
	}{
		{"appended", "a\nb\n", "a\nb\nc\nd\n", []al{{3, "c"}, {4, "d"}}},
		{"two_hunks_and_a_deletion", "l1\nl2\nl3\nl4\nl5\nl6\nl7\nl8\n", "l1\nL2\nl3\nl5\nl6\nl7\nl8\nl9\n", []al{{2, "L2"}, {8, "l9"}}},
		{"deleted_lines_only", "a\nb\nc\n", "a\nc\n", nil},
		{"header_lookalike_content", "x\n", "x\n++ b/zz.go\n-- a/zz.go\n@@ -1 +1 @@\n+++ b/yy.go\n",
			[]al{{2, "++ b/zz.go"}, {3, "-- a/zz.go"}, {4, "@@ -1 +1 @@"}, {5, "+++ b/yy.go"}}},
		{"no_newline_at_end_of_head", "a\nb\n", "a\nb\nlast", []al{{3, "last"}}},
		{"no_newline_at_end_of_base", "a\nb", "a\nb\nc\n", []al{{2, "b"}, {3, "c"}}},
		{"crlf", "a\r\nb\r\n", "a\r\nb\r\nc\r\n", []al{{3, "c"}}},
		{"whole_file_replaced", "one\ntwo\n", "three\nfour\n", []al{{1, "three"}, {2, "four"}}},
		{"unchanged", "a\nb\n", "a\nb\n", nil},
		{"blank_line_added", "a\nb\n", "a\n\nb\n", []al{{2, ""}}},
		{"unicode_and_tabs", "a\n", "a\n\té☃ \t\n", []al{{2, "\té☃ \t"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newScratch(t)
			s.write("p/x_test.go", tc.base)
			a := s.commit("base")
			s.write("p/x_test.go", tc.head)
			b := s.commit("head")
			got, err := AddedLines(s.repo(), a, b, "p/x_test.go")
			if err != nil {
				t.Fatal(err)
			}
			for i := range got {
				got[i].Text = strings.TrimRight(got[i].Text, "\r")
			}
			if fmt.Sprint(got) != fmt.Sprint(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
	t.Run("new_file", func(t *testing.T) {
		s := newScratch(t)
		s.write("keep.txt", "1\n")
		a := s.commit("base")
		s.write("p/new.go", "package p\n\nfunc f() {}\n")
		b := s.commit("head")
		got, err := AddedLines(s.repo(), a, b, "p/new.go")
		if err != nil || fmt.Sprint(got) != fmt.Sprint([]AddedLine{{1, "package p"}, {2, ""}, {3, "func f() {}"}}) {
			t.Fatalf("got %v, %v", got, err)
		}
	})
	t.Run("deleted_empty_binary_and_untouched_paths_have_no_added_lines", func(t *testing.T) {
		s := newScratch(t)
		s.write("del.txt", "1\n")
		s.write("untouched.txt", "1\n")
		s.writeBytes("b.bin", []byte{0, 1, 2})
		a := s.commit("base")
		s.remove("del.txt")
		s.write("empty.txt", "")
		s.writeBytes("b.bin", []byte{0, 1, 3, 0})
		b := s.commit("head")
		for _, p := range []string{"del.txt", "untouched.txt", "empty.txt", "absent.txt"} {
			got, err := AddedLines(s.repo(), a, b, p)
			if err != nil || len(got) != 0 {
				t.Errorf("%s: %v, %v", p, got, err)
			}
		}
		if _, err := AddedLines(s.repo(), a, b, "b.bin"); err != nil {
			t.Errorf("binary file: %v", err)
		}
	})
}

// 22-AC-8: the path is a literal path, never an option, a glob or a pathspec; a neighbour that matches the glob or
// magic form must not leak in (passing twin: the neighbour has its own lines).
func TestAddedLines_PathNames_22AC8(t *testing.T) {
	s := newScratch(t)
	names := []string{"-dash.go", "a*b.go", "axb.go", ":(top)x.go", "x.go", "sp ace.go", "uni/é.go", "nl\nname.go", "-rf/y.go", ":!x.go", "[ab].go", "a.go", "b.go", "?.go"}
	for _, n := range names {
		s.write(n, "base\n")
	}
	a := s.commit("base")
	for _, n := range names {
		s.write(n, "base\nadded in "+strings.NewReplacer("\n", "<nl>").Replace(n)+"\n")
	}
	b := s.commit("head")
	for _, n := range names {
		got, err := AddedLines(s.repo(), a, b, n)
		if err != nil {
			t.Errorf("%q: %v", n, err)
			continue
		}
		want := []AddedLine{{2, "added in " + strings.NewReplacer("\n", "<nl>").Replace(n)}}
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("%q: got %v, want %v", n, got, want)
		}
	}
	if _, err := AddedLines(s.repo(), a, b, "--output=/dev/null"); err == nil {
		// A path that starts with a dash is valid (see -dash.go); an absent such path yields no lines, not an
		// option. Either an error or no lines is acceptable, but git must not have taken it as an option.
		got, _ := AddedLines(s.repo(), a, b, "--output=/dev/null")
		if len(got) != 0 {
			t.Errorf("absent path produced lines: %v", got)
		}
	}
}

// 22-AC-8: attributes and diff drivers committed to the repository cannot hide added lines (`-diff` makes plain git
// print "Binary files differ"; textconv rewrites the text; an external diff program replaces the output).
func TestAddedLines_HostileGitConfigAndAttributes_22AC8(t *testing.T) {
	s := newScratch(t)
	marker := filepath.Join(t.TempDir(), "ran")
	script := filepath.Join(t.TempDir(), "ext.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho hit >> "+marker+"\nhead -n 1 \"$1\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	s.write(".gitattributes", "*_test.go -diff\n*.txt diff=evil\n")
	s.git("config", "diff.evil.textconv", script)
	s.git("config", "color.ui", "always")
	s.git("config", "diff.noprefix", "true")
	s.write("p/x_test.go", "package p\n")
	s.write("p/y.txt", "one\n")
	a := s.commit("base")
	s.write("p/x_test.go", "package p\n\nfunc TestX(t *testing.T) { t.Skip(\"later\") }\n")
	s.write("p/y.txt", "one\ntwo\n")
	b := s.commit("head")
	// Preconditions: plain git is blinded (-diff) or rewritten (textconv) by these settings.
	if out := s.git("diff", "-U0", a, b, "--", "p/x_test.go"); !strings.Contains(out, "Binary files") {
		t.Fatalf("precondition: -diff attribute should blind plain git, got %q", out)
	}
	if out := s.git("diff", "-U0", a, b, "--", "p/y.txt"); strings.Contains(out, "two") {
		t.Fatalf("precondition: textconv (first line only) should hide the added line from plain git, got %q", out)
	}
	_ = os.Remove(marker)
	s.git("config", "diff.external", script)
	_ = os.Remove(marker)
	t.Setenv("GIT_EXTERNAL_DIFF", script)

	got, err := AddedLines(s.repo(), a, b, "p/x_test.go")
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(got) != fmt.Sprint([]AddedLine{{2, ""}, {3, "func TestX(t *testing.T) { t.Skip(\"later\") }"}}) {
		t.Errorf("-diff attribute hid or changed the added lines: %v", got)
	}
	got, err = AddedLines(s.repo(), a, b, "p/y.txt")
	if err != nil || fmt.Sprint(got) != fmt.Sprint([]AddedLine{{2, "two"}}) {
		t.Errorf("textconv or external diff changed the added lines: %v, %v", got, err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Errorf("a configured diff program ran")
	}
}
