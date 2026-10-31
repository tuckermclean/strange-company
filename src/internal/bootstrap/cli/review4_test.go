package cli_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// S2 regression found by the e2e job (F03 wrapper-real.sh, 02-AC-10 and 02-AC-11). Plan 50 section 14.2: preflight
// prints one row per check ("printed as a table") and "any fail exits 3"; the plan never says it stops at the first
// failing check. PF-01 (toolbox self-check) comes first because every other check runs on that toolbox. A check
// that does not need the config (PF-01, and PF-06 on the parsed variables) is therefore still evaluated and
// printed when PF-02 fails on missing required variables.

func lineIndex(out, re string) int {
	loc := regexp.MustCompile(re).FindStringIndex(out)
	if loc == nil {
		return -1
	}
	return loc[0]
}

// 02-AC-11: an env file holding only SSH_PRIVATE_KEY_PATH=$(touch ...) makes preflight exit 3 with a PF-06 line that
// names the literal path; nothing in it is executed.
func TestPreflight_Pf06RunsWhenConfigIsIncomplete_02AC11(t *testing.T) {
	i := newInst(t)
	marker := filepath.Join(i.dir, "pwned")
	env := write(t, filepath.Join(i.dir, "pwn.env"), "SSH_PRIVATE_KEY_PATH=$(touch "+marker+")\n", 0o600)
	code, out := i.exec(nil, "--env-file", env, "preflight")
	if code != 3 {
		t.Errorf("exit %d, want 3 (PF-06 fails):\n%s", code, out)
	}
	if !regexp.MustCompile(`(?m)^PF-06 .*\$\(touch ` + regexp.QuoteMeta(marker) + `\)`).MatchString(out) {
		t.Errorf("no PF-06 line naming the literal path:\n%s", out)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Errorf("the env-file value was executed: %s exists", marker)
	}
	contains(t, out, "PF-02", "CONFIG_REQUIRED_MISSING")
}

// PF-06 passes and is printed for an env file with only valid key paths, although PF-02 fails on the missing
// required variables (the config failure alone keeps exit 2).
func TestPreflight_Pf06PassesWhenConfigIsIncomplete_02AC11(t *testing.T) {
	i := newInst(t)
	env := write(t, filepath.Join(i.dir, "key.env"), "SSH_PRIVATE_KEY_PATH="+i.key+"\nSSH_PUBLIC_KEY_PATH="+i.key+".pub\n", 0o600)
	code, out := i.exec(nil, "--env-file", env, "preflight")
	if !regexp.MustCompile(`(?m)^PF-06 pass\b`).MatchString(out) {
		t.Errorf("no line starting \"PF-06 pass\":\n%s", out)
	}
	if code != 2 {
		t.Errorf("exit %d, want 2 (only PF-02 fails):\n%s", code, out)
	}
	contains(t, out, "PF-02", "CONFIG_REQUIRED_MISSING")
}

// 02-AC-10: a toolbox built from a different tools.lock.yaml and no bootstrap.env: PF-01 TOOLBOX_STALE is
// reported, exit 3, and before the unreadable-file row.
func TestPreflight_Pf01StaleReportedBeforeEnvFile_02AC10(t *testing.T) {
	i := newInst(t)
	write(t, filepath.Join(i.repo, "toolbox", "tools.lock.yaml"), toolLock+"# changed after the image was built\n", 0o644)
	code, out := i.exec(nil, "--env-file", filepath.Join(i.dir, "absent.env"), "preflight")
	if code != 3 {
		t.Errorf("exit %d, want 3:\n%s", code, out)
	}
	p1 := lineIndex(out, `(?m)^PF-01 TOOLBOX_STALE\b`)
	if p1 < 0 {
		t.Fatalf("no \"PF-01 TOOLBOX_STALE\" line:\n%s", out)
	}
	if p2 := lineIndex(out, `(?m)^PF-02 `); p2 >= 0 && p2 < p1 {
		t.Errorf("PF-02 is printed before PF-01:\n%s", out)
	}
}

// Plan 50 section 14.2 order: PF-01 is printed before PF-02, also when PF-02 fails and the exit stays 2 (50-AC-2).
func TestPreflight_Pf01PrintedBeforeFailingPf02_50AC2(t *testing.T) {
	i := newInst(t)
	fixture := filepath.Join(repoRoot(t), "test", "fixtures", "bootstrap", "invalid-unknown-key.env")
	code, out := i.exec(nil, "--env-file", fixture, "preflight")
	if code != 2 {
		t.Errorf("exit %d, want 2:\n%s", code, out)
	}
	p1 := lineIndex(out, `(?m)^PF-01 pass\b`)
	p2 := lineIndex(out, `(?m)^PF-02 `)
	if p1 < 0 || p2 < 0 || p1 > p2 {
		t.Errorf("want a \"PF-01 pass\" line before the PF-02 line:\n%s", out)
	}
	if strings.Contains(out, "fixture-secret") {
		t.Errorf("a secret value reached the output:\n%s", out)
	}
}
