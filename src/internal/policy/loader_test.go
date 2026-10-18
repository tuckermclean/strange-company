package policy

import (
	"os"
	"path/filepath"
	"testing"
)

// 26-AC-3: an invalid revision after a valid one keeps the valid snapshot, disables dispatch and records an
// error; a valid revision afterwards re-enables dispatch.
func TestLoader_InvalidRevisionKeepsLastKnownGood_26AC3(t *testing.T) {
	valid := recordedDigest(t)
	l := NewLoader(opts(t, true))

	if l.DispatchEnabled() || l.Snapshot() != nil {
		t.Fatalf("a fresh loader must have no snapshot and dispatch disabled")
	}
	if is := l.Load(td("valid")); len(is) != 0 {
		t.Fatalf("valid tree rejected: %v", is)
	}
	if s := l.Snapshot(); s == nil || s.Revision != valid {
		t.Fatalf("Snapshot after valid load = %+v, want revision %s", s, valid)
	}
	if !l.DispatchEnabled() || l.LastError() != "" {
		t.Fatalf("after valid load: DispatchEnabled=%v LastError=%q", l.DispatchEnabled(), l.LastError())
	}

	bad := copyTree(t, td("valid"), false)
	if err := os.Remove(filepath.Join(bad, "roles", "tester.yaml")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bad, "ci.yaml"), []byte("kind: Mystery\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if is := l.Load(bad); len(is) == 0 {
		t.Fatal("invalid tree accepted")
	}
	if s := l.Snapshot(); s == nil || s.Revision != valid {
		t.Fatalf("Snapshot after invalid load = %+v, want last-known-good %s", s, valid)
	}
	if l.DispatchEnabled() {
		t.Error("DispatchEnabled() must be false after an invalid revision")
	}
	if l.LastError() == "" {
		t.Error("LastError() must be non-empty after an invalid revision")
	}

	// a different valid revision re-enables dispatch and becomes the snapshot
	next := copyTree(t, td("valid"), false)
	p := filepath.Join(next, "test-quarantine.yaml")
	b, _ := os.ReadFile(p)
	if err := os.WriteFile(p, append([]byte("# one more comment\n"), b...), 0o644); err != nil {
		t.Fatal(err)
	}
	if is := l.Load(next); len(is) != 0 {
		t.Fatalf("second valid tree rejected: %v", is)
	}
	s := l.Snapshot()
	if s == nil || s.Revision == valid || s.Revision != refDigest(t, next) {
		t.Fatalf("Snapshot after recovery = %+v, want revision %s", s, refDigest(t, next))
	}
	if !l.DispatchEnabled() || l.LastError() != "" {
		t.Errorf("after recovery: DispatchEnabled=%v LastError=%q", l.DispatchEnabled(), l.LastError())
	}
}

// 26-AC-3: a loader whose first revision is invalid has no snapshot and dispatch stays disabled.
func TestLoader_FirstRevisionInvalid_26AC3(t *testing.T) {
	l := NewLoader(opts(t, true))
	if is := l.Load(td("invalid", "unknown-kind")); len(is) == 0 {
		t.Fatal("invalid tree accepted")
	}
	if l.Snapshot() != nil || l.DispatchEnabled() || l.LastError() == "" {
		t.Errorf("Snapshot=%v DispatchEnabled=%v LastError=%q", l.Snapshot(), l.DispatchEnabled(), l.LastError())
	}
}
