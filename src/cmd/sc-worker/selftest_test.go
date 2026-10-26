package main

import "testing"

// 28-AC-3 (Layer 1): `sc-worker selftest` is implemented and wired to worker.SelfTest: it ends 0 (ok) or 1 (a check failed,
// for example because the test is not uid 65532), never with the 2 of "not implemented" / usage. Extra arguments stay a
// usage error.
func TestSelftestSubcommand_IsWired_28AC3(t *testing.T) {
	if code := run([]string{"selftest"}); code != 0 && code != 1 {
		t.Errorf("sc-worker selftest exit code = %d, want 0 or 1", code)
	}
	if code := run([]string{"selftest", "extra"}); code != 2 {
		t.Errorf("sc-worker selftest extra: exit code = %d, want 2 (usage)", code)
	}
}
