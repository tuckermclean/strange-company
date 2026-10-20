package main

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var line = regexp.MustCompile(`^([A-Z][A-Z0-9_]+)(?::(/\S*))? (\S+): (mode (\S+): )?(.+)$`)

func root(t *testing.T) string {
	r, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func runCmd(args ...string) (int, string, string) {
	var o, e bytes.Buffer
	return run(args, &o, &e), o.String(), e.String()
}

// 29-AC-1 (D6i): exit 0 on the committed contract, reporting both modes.
func TestCheck_ContractExitsZero_29AC1(t *testing.T) {
	code, out, errs := runCmd("check", "--contracts", filepath.Join(root(t), "contracts"))
	if code != 0 {
		t.Fatalf("exit %d; stdout %q stderr %q", code, out, errs)
	}
	for _, mode := range []string{"test-first", "verification-first"} {
		if !strings.Contains(out, "mode "+mode+":") || !strings.Contains(out, "unreachable=0 non-terminating=0") {
			t.Errorf("stdout lacks the %s report: %q", mode, out)
		}
	}
}

func writeTable(t *testing.T, old, repl string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root(t), "contracts", "state-machine.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Count(b, []byte(old)) != 1 {
		t.Fatalf("base table must contain %q exactly once", old)
	}
	p := filepath.Join(t.TempDir(), "table.yaml")
	if err := os.WriteFile(p, bytes.Replace(b, []byte(old), []byte(repl), 1), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// 29-AC-2 (D6i): exit 1 on an invalid table, printing `CODE[:ptr] <file>: msg` on stdout.
func TestCheck_InvalidTableExitsOne_29AC2(t *testing.T) {
	p := writeTable(t, "initialState: SPECIFIED", "initialState: GHOST")
	code, out, errs := runCmd("check", "--contracts", filepath.Join(root(t), "contracts"), "--table", p)
	if code != 1 {
		t.Fatalf("exit %d, want 1; stdout %q stderr %q", code, out, errs)
	}
	ok := false
	for _, l := range strings.Split(out, "\n") {
		if m := line.FindStringSubmatch(l); m != nil && m[1] == "SM_INITIAL_STATE_INVALID" && m[3] == p {
			ok = true
		}
	}
	if !ok {
		t.Errorf("no line `SM_INITIAL_STATE_INVALID[:ptr] %s: msg` in stdout %q", p, out)
	}
}

// 29-AC-2 (D6d, D6h, D6i): with a mode-specific failure the file precedes `mode m:` in the printed line.
// RED_VERIFIED is test-first only; restricting T140 to verification-first leaves its stall without a transition.
func TestCheck_ModeFailurePrintsFileBeforeMode_29AC2(t *testing.T) {
	p := writeTable(t, "      actors: [runner]\n      modes: [test-first, verification-first]\n      guards: []\n      effects: [E_CANCEL_ACTIVE_EXECUTION, E_CANCEL_PENDING_VERIFICATION, E_RECORD_RESUME_STATE, \"E_NOTIFY_HUMAN:stalled\"]",
		"      actors: [runner]\n      modes: [verification-first]\n      guards: []\n      effects: [E_CANCEL_ACTIVE_EXECUTION, E_CANCEL_PENDING_VERIFICATION, E_RECORD_RESUME_STATE, \"E_NOTIFY_HUMAN:stalled\"]")
	code, out, errs := runCmd("check", "--contracts", filepath.Join(root(t), "contracts"), "--table", p)
	if code != 1 {
		t.Fatalf("exit %d, want 1; stdout %q stderr %q", code, out, errs)
	}
	ok := false
	for _, l := range strings.Split(out, "\n") {
		if m := line.FindStringSubmatch(l); m != nil && m[1] == "SM_STALL_NO_TRANSITION" && m[3] == p && m[5] == "test-first" {
			ok = true
		}
	}
	if !ok {
		t.Errorf("no line `SM_STALL_NO_TRANSITION[:ptr] %s: mode test-first: msg` in stdout %q", p, out)
	}
}

// 29-AC-1 (D6i): usage errors exit 2.
func TestUsageErrorsExitTwo_29AC1(t *testing.T) {
	for _, args := range [][]string{{}, {"frobnicate"}, {"check", "--bogus"}, {"check", "extra"}} {
		if code, _, _ := runCmd(args...); code != 2 {
			t.Errorf("args %v: exit %d, want 2", args, code)
		}
	}
}
