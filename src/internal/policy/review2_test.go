package policy

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

func issueStrings(is []Issue) []string {
	out := make([]string, len(is))
	for i, x := range is {
		out[i] = x.Code + "|" + x.Pointer + "|" + x.File + "|" + x.Message
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// 26-AC-1 (D5): a schema-valid file with five unknown root keys given in non-sorted order yields the same
// issue list (codes, pointers, messages) on every run, and the same Loader.LastError() on every Load.
func TestCheck_ManyUnknownKeysDeterministic_26AC1(t *testing.T) {
	dir := td("invalid", "schema-unknown-keys")
	_, first := Check(dir, opts(t, false))
	if !hasCode(first, "POLICY_SCHEMA_INVALID") {
		t.Fatalf("want POLICY_SCHEMA_INVALID; got %v", first)
	}
	want := issueStrings(first)
	for n := 0; n < 20; n++ {
		_, again := Check(dir, opts(t, false))
		if got := issueStrings(again); !equalStrings(want, got) {
			t.Fatalf("Check run %d differs:\n first: %q\n again: %q", n, want, got)
		}
	}
	l := NewLoader(opts(t, false))
	var lastErr string
	for n := 0; n < 20; n++ {
		if is := l.Load(dir); len(is) == 0 {
			t.Fatal("invalid tree accepted")
		}
		if n == 0 {
			lastErr = l.LastError()
			if lastErr == "" {
				t.Fatal("LastError() empty after an invalid load")
			}
		} else if got := l.LastError(); got != lastErr {
			t.Fatalf("Load %d: LastError differs:\n first: %q\n again: %q", n, lastErr, got)
		}
	}
}

// 26-AC-1 diagnostics: the same bad resource name under defaults/requests and defaults/limits is reported
// once at each real location.
func TestCheck_SameKeyUnderSiblingObjects_26AC1(t *testing.T) {
	base := "/spec/lanes/ci/defaults/"
	for n := 0; n < 3; n++ {
		_, issues := Check(td("invalid", "schema-ci-defaults-gpu"), opts(t, false))
		var got []string
		for _, i := range issues {
			if i.Code == "POLICY_SCHEMA_INVALID" {
				got = append(got, i.Pointer)
			}
		}
		sort.Strings(got)
		want := []string{base + "limits/gpu", base + "requests/gpu"}
		if !equalStrings(got, want) {
			t.Fatalf("run %d: POLICY_SCHEMA_INVALID pointers = %q, want %q (issues %v)", n, got, want, issues)
		}
	}
}

// 26-AC-1 diagnostics: an invalid key under spec.models whose name exists validly under spec.providers is
// reported under spec/models, not spec.
func TestCheck_KeyNameExistsElsewhereAtDepth_26AC1(t *testing.T) {
	_, issues := Check(td("invalid", "schema-routing-models-key"), opts(t, false))
	var got []string
	for _, i := range issues {
		if i.Code == "POLICY_SCHEMA_INVALID" {
			got = append(got, i.Pointer)
		}
	}
	if len(got) != 1 || got[0] != "/spec/models/anthropic" {
		t.Fatalf("POLICY_SCHEMA_INVALID pointers = %q, want [/spec/models/anthropic]; issues %v", got, issues)
	}
}

// resolvePointer walks a JSON pointer through a decoded YAML document.
func resolvePointer(doc any, ptr string) bool {
	if ptr == "" {
		return true
	}
	cur := doc
	for _, tok := range strings.Split(ptr[1:], "/") {
		tok = strings.ReplaceAll(strings.ReplaceAll(tok, "~1", "/"), "~0", "~")
		switch v := cur.(type) {
		case map[string]any:
			n, ok := v[tok]
			if !ok {
				return false
			}
			cur = n
		case []any:
			i, err := strconv.Atoi(tok)
			if err != nil || i < 0 || i >= len(v) {
				return false
			}
			cur = v[i]
		default:
			return false
		}
	}
	return true
}

// 26-AC-1 diagnostics: every POLICY_SCHEMA_INVALID pointer of every schema fixture resolves to an existing
// node of the offending document (a missing required property is reported at its existing parent).
func TestCheck_SchemaPointersResolve_26AC1(t *testing.T) {
	dirs, _ := filepath.Glob(td("invalid", "schema-*"))
	n := 0
	for _, dir := range dirs {
		if st, err := os.Stat(dir); err != nil || !st.IsDir() {
			continue
		}
		n++
		t.Run(filepath.Base(dir), func(t *testing.T) {
			docs := map[string]any{}
			_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
				if err == nil && d.Type().IsRegular() {
					rel, _ := filepath.Rel(dir, p)
					docs[filepath.ToSlash(rel)] = readYAML(t, p)
				}
				return nil
			})
			_, issues := Check(dir, opts(t, false))
			for _, i := range issues {
				if i.Code != "POLICY_SCHEMA_INVALID" {
					continue
				}
				doc, ok := docs[filepath.ToSlash(i.File)]
				if !ok {
					for k, d := range docs {
						if strings.HasSuffix(filepath.ToSlash(i.File), k) {
							doc, ok = d, true
						}
					}
				}
				if !ok {
					t.Fatalf("issue file %q is not a fixture file", i.File)
				}
				if !resolvePointer(doc, i.Pointer) {
					t.Errorf("pointer %q does not resolve in %s: %s", i.Pointer, i.File, i.Message)
				}
			}
		})
	}
	if n < 44 {
		t.Fatalf("found %d schema fixtures, want at least 44", n)
	}
}
