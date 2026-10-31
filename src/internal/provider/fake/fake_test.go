package fake_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"strange.company/sc/internal/provider/fake"
)

// Plan 03 section 22: the fake provider is refused outside SC_TEST_MODE=1 (proven through plan 50, CD-70(c); this
// is its package-level leg).
func TestNew_RefusedOutsideTestMode_50AC10(t *testing.T) {
	for _, env := range []map[string]string{nil, {}, {"SC_TEST_MODE": ""}, {"SC_TEST_MODE": "0"}, {"SC_TEST_MODE": "true"}} {
		p, err := fake.New(env, t.TempDir())
		if err == nil || p != nil {
			t.Errorf("New(%v) = %v, %v; want a refusal", env, p, err)
			continue
		}
		if !strings.Contains(err.Error(), "TEST_MODE_REQUIRED") {
			t.Errorf("New(%v) error %q does not name TEST_MODE_REQUIRED", env, err)
		}
	}
}

// 50-AC-10 and 50-AC-11 read the create counter: a fresh fake provider has created nothing, and the counter file
// is `creates` in the state directory.
func TestNew_FreshProviderHasNoCreates_50AC10(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "fake-provider")
	p, err := fake.New(map[string]string{"SC_TEST_MODE": "1"}, dir)
	if err != nil || p == nil {
		t.Fatalf("New in test mode = %v, %v", p, err)
	}
	n, err := fake.CreateCount(dir)
	if err != nil || n != 0 {
		t.Errorf("CreateCount of a fresh provider = %d, %v, want 0", n, err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "creates"), []byte("3\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if n, err := fake.CreateCount(dir); err != nil || n != 3 {
		t.Errorf("CreateCount with the file creates = 3 reads %d, %v, want 3", n, err)
	}
}
