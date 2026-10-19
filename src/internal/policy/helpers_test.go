package policy

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// repoRoot is the repository root (this package lives in src/internal/policy).
func repoRoot(t testing.TB) string {
	t.Helper()
	r, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func opts(t testing.TB, runner bool) Options {
	return Options{ContractsDir: filepath.Join(repoRoot(t), "contracts"), Runner: runner}
}

// refDigest is an independent reference of plan 26 section 9.2 step 4: sha256 over (path, NUL, bytes, NUL)
// sorted by path. It does not use the code under test.
func refDigest(t testing.TB, dir string) string {
	t.Helper()
	type ent struct {
		p string
		b []byte
	}
	var es []ent
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		es = append(es, ent{filepath.ToSlash(rel), b})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Slice(es, func(i, j int) bool { return es[i].p < es[j].p })
	h := sha256.New()
	for _, e := range es {
		h.Write([]byte(e.p))
		h.Write([]byte{0})
		h.Write(e.b)
		h.Write([]byte{0})
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

func td(parts ...string) string {
	return filepath.Join(append([]string{"testdata"}, parts...)...)
}

func recordedDigest(t testing.TB) string {
	t.Helper()
	b, err := os.ReadFile(td("valid.digest"))
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(b))
}

// copyTree copies src into a new temp dir. When flat is set, nested paths are flattened to ConfigMap keys
// (`roles/tester.yaml` becomes `roles__tester.yaml`).
func copyTree(t testing.TB, src string, flat bool) string {
	t.Helper()
	dst := t.TempDir()
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if flat {
			rel = strings.ReplaceAll(filepath.ToSlash(rel), "/", "__")
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		out := filepath.Join(dst, rel)
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return err
		}
		return os.WriteFile(out, b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	return dst
}

func hasCode(is []Issue, code string) bool {
	for _, i := range is {
		if i.Code == code {
			return true
		}
	}
	return false
}

// expectation reads `<case>.expect`: one `CODE` or `CODE:/pointer` line.
func expectation(t testing.TB, path string) (code, pointer string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	line := strings.TrimSpace(string(bytes.SplitN(b, []byte("\n"), 2)[0]))
	code, pointer, _ = strings.Cut(line, ":")
	return code, pointer
}
