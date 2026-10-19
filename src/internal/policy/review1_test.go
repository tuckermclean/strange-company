package policy

import (
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// 26-AC-1 (D5): schema-error pointers and issue order are deterministic across repeated runs, for every
// schema-* fixture, and no issue message is the bare "validation failed".
func TestCheck_SchemaIssuesDeterministic_26AC1(t *testing.T) {
	dirs, _ := filepath.Glob(td("invalid", "schema-*[0-9]"))
	if len(dirs) < 41 {
		t.Fatalf("found %d schema fixtures, want at least 41", len(dirs))
	}
	for _, dir := range dirs {
		t.Run(filepath.Base(dir), func(t *testing.T) {
			_, first := Check(dir, opts(t, false))
			sorted := sort.SliceIsSorted(first, func(a, b int) bool { return issueLess(first[a], first[b]) })
			if !sorted {
				t.Errorf("issues not sorted by (file, pointer, code, message): %v", first)
			}
			for _, i := range first {
				if strings.TrimSpace(i.Message) == "validation failed" {
					t.Errorf("uninformative message in %v", i)
				}
			}
			// Check recompiles the schemas on every call (about a second each), so only the fixtures known to
			// vary get the full 20 repeats; the rest get 2.
			reps := 2
			if unstable[filepath.Base(dir)] {
				reps = 20
			}
			for n := 0; n < reps; n++ {
				_, again := Check(dir, opts(t, false))
				if !reflect.DeepEqual(first, again) {
					t.Fatalf("run %d differs:\n first: %v\n again: %v", n, first, again)
				}
			}
		})
	}
}

var unstable = map[string]bool{"schema-budget-policy-2": true, "schema-budget-policy-3": true, "schema-ci-policy-4": true,
	"schema-harness-0": true, "schema-harness-2": true, "schema-model-routing-1": true, "schema-role-2": true}

func issueLess(a, b Issue) bool {
	if a.File != b.File {
		return a.File < b.File
	}
	if a.Pointer != b.Pointer {
		return a.Pointer < b.Pointer
	}
	if a.Code != b.Code {
		return a.Code < b.Code
	}
	return a.Message < b.Message
}

// 26-AC-1 (D5): a bad harness key is reported at the offending instance.
func TestCheck_PropertyNamesPointer_26AC1(t *testing.T) {
	for n := 0; n < 5; n++ {
		_, issues := Check(td("invalid", "schema-harness-2"), opts(t, false))
		found := false
		for _, i := range issues {
			if i.Code == "POLICY_SCHEMA_INVALID" && i.Pointer == "/spec/harnesses/hermes" {
				found = true
			}
		}
		if !found {
			t.Fatalf("run %d: want POLICY_SCHEMA_INVALID at /spec/harnesses/hermes; got %v", n, issues)
		}
	}
}

// Plan 26 section 9.2 step 1: a real file name containing "__" is rejected; two files mapping to one path are
// both rejected.
func TestCheck_ReservedFilenameCases_26AC1(t *testing.T) {
	_, issues := Check(td("invalid", "reserved-collision"), opts(t, false))
	n := 0
	for _, i := range issues {
		if i.Code == "POLICY_RESERVED_FILENAME" {
			n++
		}
	}
	if n < 2 {
		t.Errorf("collision: want POLICY_RESERVED_FILENAME for both files, got %d: %v", n, issues)
	}
	_, issues = Check(td("invalid", "reserved-toplevel-nested"), opts(t, false))
	if !hasCode(issues, "POLICY_RESERVED_FILENAME") {
		t.Errorf("top-level __ file in a nested tree must be rejected: %v", issues)
	}
	if is := NewLoader(opts(t, false)).Load(td("invalid", "reserved-collision")); len(is) == 0 {
		t.Errorf("Load must fail on colliding paths")
	}
}

// 26-AC-1 / plan 01 section 8: every regular file is a policy document regardless of extension.
func TestCheck_EveryFileIsADocument_26AC1(t *testing.T) {
	for name, code := range map[string]string{
		"notutf8-readme":    "POLICY_NOT_UTF8",
		"unknown-kind-json": "POLICY_UNKNOWN_KIND",
		"unknown-kind-txt":  "POLICY_UNKNOWN_KIND",
	} {
		_, issues := Check(td("invalid", name), opts(t, false))
		if !hasCode(issues, code) {
			t.Errorf("%s: want %s; got %v", name, code, issues)
		}
	}
}
