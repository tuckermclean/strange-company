package policy

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// reversedFS lists every directory in reverse name order.
type reversedFS struct{ fs.FS }

func (r reversedFS) ReadDir(name string) ([]fs.DirEntry, error) {
	es, err := fs.ReadDir(r.FS, name)
	for i, j := 0, len(es)-1; i < j; i, j = i+1, j-1 {
		es[i], es[j] = es[j], es[i]
	}
	return es, err
}

// 26-AC-2: the digest of testdata/valid equals the recorded value.
func TestDigest_ValidTreeEqualsRecorded_26AC2(t *testing.T) {
	rec := recordedDigest(t)
	if !regexp.MustCompile(`^sha256:[0-9a-f]{64}$`).MatchString(rec) {
		t.Fatalf("testdata/valid.digest = %q, want sha256:<64 hex>", rec)
	}
	if ref := refDigest(t, td("valid")); ref != rec {
		t.Fatalf("reference digest %s != recorded %s (fixture out of date)", ref, rec)
	}
	got, err := Digest(td("valid"))
	if err != nil {
		t.Fatal(err)
	}
	if got != rec {
		t.Fatalf("Digest(valid) = %q, want %q", got, rec)
	}
}

// 26-AC-2: renaming a file or changing one byte changes the digest.
func TestDigest_RenameOrByteChangeChangesIt_26AC2(t *testing.T) {
	rec := recordedDigest(t)
	t.Run("rename", func(t *testing.T) {
		d := copyTree(t, td("valid"), false)
		if err := os.Rename(filepath.Join(d, "roles", "tester.yaml"), filepath.Join(d, "roles", "tester2.yaml")); err != nil {
			t.Fatal(err)
		}
		got, err := Digest(d)
		if err != nil || got == "" || got == rec {
			t.Fatalf("Digest after rename = %q, %v; want a digest different from %q", got, err, rec)
		}
		if want := refDigest(t, d); got != want {
			t.Fatalf("Digest after rename = %q, want %q", got, want)
		}
	})
	t.Run("one-byte", func(t *testing.T) {
		d := copyTree(t, td("valid"), false)
		p := filepath.Join(d, "budgets.yaml")
		b, _ := os.ReadFile(p)
		b[len(b)-2] ^= 0x01
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
		got, err := Digest(d)
		if err != nil || got == "" || got == rec {
			t.Fatalf("Digest after byte change = %q, %v; want a digest different from %q", got, err, rec)
		}
		if want := refDigest(t, d); got != want {
			t.Fatalf("Digest after byte change = %q, want %q", got, want)
		}
	})
}

// 26-AC-2: reordering directory entries does not change the digest.
func TestDigest_DirectoryOrderDoesNotMatter_26AC2(t *testing.T) {
	rec := recordedDigest(t)
	got, err := DigestFS(reversedFS{os.DirFS(td("valid"))})
	if err != nil {
		t.Fatal(err)
	}
	if got != rec {
		t.Fatalf("DigestFS(reversed order) = %q, want %q", got, rec)
	}
}

// 26-AC-2 and plan 26 section 9.2 step 1: a ConfigMap key `roles__tester.yaml` is read as `roles/tester.yaml`,
// so the flattened mount has the same revision as the nested tree.
func TestDigest_ConfigMapKeysMapToPaths_26AC2(t *testing.T) {
	flat := copyTree(t, td("valid"), true)
	got, err := Digest(flat)
	if err != nil {
		t.Fatal(err)
	}
	if got != recordedDigest(t) {
		t.Fatalf("Digest(flattened mount) = %q, want %q", got, recordedDigest(t))
	}
	if is := loadIssues(t, flat, true); len(is) != 0 {
		t.Fatalf("flattened valid tree rejected: %v", is)
	}
}

// loadIssues loads dir in the given mode with a fresh Loader and returns the issues.
func loadIssues(t testing.TB, dir string, runner bool) []Issue {
	t.Helper()
	return NewLoader(opts(t, runner)).Load(dir)
}
