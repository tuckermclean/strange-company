package cli_test

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// D10 tightened (plan 50 section 14.2 "any fail exits 3" read with the failure-mode row "Invalid or missing config |
// PF-02 | exit 2"). An INVALID configuration (unknown key, unparseable file, schema-invalid value, test mode with a
// paid provider) is a usage error: PF-02 exits 2 even when PF-01 also fails. A MISSING configuration (unreadable
// file, required variables absent) exits 2 alone and 3 when another check fails (pinned in review4_test.go).

func staleToolbox(i *inst) {
	write(i.t, filepath.Join(i.repo, "toolbox", "tools.lock.yaml"), toolLock+"# changed after the image was built\n", 0o644)
}

// 50-AC-2, 02-AC-10: UNKNOWN_KEY exits 2 although PF-01 TOOLBOX_STALE fails; both rows are printed.
func TestPreflight_InvalidConfigExits2EvenWhenPf01Fails_UnknownKey_50AC2(t *testing.T) {
	i := newInst(t)
	staleToolbox(i)
	fixture := filepath.Join(repoRoot(t), "test", "fixtures", "bootstrap", "invalid-unknown-key.env")
	code, out := i.exec(nil, "--env-file", fixture, "preflight")
	if code != 2 {
		t.Errorf("exit %d, want 2 (invalid configuration):\n%s", code, out)
	}
	contains(t, out, "PF-01", "TOOLBOX_STALE", "PF-02", "UNKNOWN_KEY", "MAX_MONTLY_COST")
	p1, p2 := lineIndex(out, `(?m)^PF-01 TOOLBOX_STALE\b`), lineIndex(out, `(?m)^PF-02 `)
	if p1 < 0 || p2 < 0 || p1 > p2 {
		t.Errorf("want the PF-01 TOOLBOX_STALE line before the PF-02 line:\n%s", out)
	}
}

// 50-AC-7, 02-AC-10: TEST_MODE_REFUSED_PAID exits 2 although PF-01 fails; both rows are printed.
func TestPreflight_InvalidConfigExits2EvenWhenPf01Fails_TestModeRefusedPaid_50AC7(t *testing.T) {
	i := newInst(t)
	staleToolbox(i)
	ovh := filepath.Join(repoRoot(t), "test", "fixtures", "bootstrap", "valid", "ovh-tunnel.env")
	code, out := i.exec(map[string]string{"SC_TEST_MODE": "1", "SC_TEST_CRASH_AT": "plan.storage-apply:after"}, "--env-file", ovh, "--non-interactive", "preflight")
	if code != 2 {
		t.Errorf("exit %d, want 2 (invalid configuration):\n%s", code, out)
	}
	contains(t, out, "PF-01", "TOOLBOX_STALE", "PF-02", "TEST_MODE_REFUSED_PAID")
}

// 50-AC-2: an unparseable env file (a line longer than the scanner limit) exits 2 although PF-01 fails.
func TestPreflight_InvalidConfigExits2EvenWhenPf01Fails_Unparseable_50AC2(t *testing.T) {
	i := newInst(t)
	staleToolbox(i)
	env := write(t, filepath.Join(i.dir, "long.env"), "CLUSTER_NAME="+strings.Repeat("a", 200000)+"\n", 0o600)
	code, out := i.exec(nil, "--env-file", env, "preflight")
	if code != 2 {
		t.Errorf("exit %d, want 2 (unparseable configuration):\n%s", code, out)
	}
	if !regexp.MustCompile(`(?m)^PF-01 TOOLBOX_STALE\b`).MatchString(out) || !regexp.MustCompile(`(?m)^PF-02 `).MatchString(out) {
		t.Errorf("want both the PF-01 and the PF-02 rows:\n%s", out)
	}
}
