package main

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const pol = "../../internal/policy/testdata"

var line = regexp.MustCompile(`^([A-Z][A-Z0-9_]+)(?::(/\S*))? (\S.*?): (.+)$`)

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

// 22-AC-1 and 26-AC-1: `check --root <repo>` exits 0 on the committed policy and reports its revision.
func TestCheck_CommittedPolicyExitsZero_26AC1(t *testing.T) {
	code, out, errs := runCmd("check", "--root", root(t))
	if code != 0 {
		t.Fatalf("exit %d; stdout %q stderr %q", code, out, errs)
	}
	if !regexp.MustCompile(`sha256:[0-9a-f]{64}`).MatchString(out) {
		t.Errorf("stdout does not report the policy revision: %q", out)
	}
	if _, err := os.Stat(filepath.Join(root(t), "policy", "verification.yaml")); err != nil {
		t.Errorf("policy/verification.yaml is not committed: %v", err)
	}
}

// 26-AC-1: `check --dir` accepts the valid tree and reports the recorded revision.
func TestCheck_ValidDirExitsZero_26AC1(t *testing.T) {
	code, out, errs := runCmd("check", "--dir", filepath.Join(pol, "valid"))
	rec, _ := os.ReadFile(filepath.Join(pol, "valid.digest"))
	if code != 0 || !strings.Contains(out, strings.TrimSpace(string(rec))) {
		t.Fatalf("exit %d; stdout %q stderr %q; want exit 0 and revision %s", code, out, errs, strings.TrimSpace(string(rec)))
	}
}

// 22-AC-1 and 26-AC-1: every invalid fixture makes the command exit non-zero with the expected code printed
// as `<CODE>[:<json-pointer>] <file>: <message>`.
func TestCheck_InvalidFixturesPrintExpectedCode_26AC1(t *testing.T) {
	expects, _ := filepath.Glob(filepath.Join(pol, "invalid", "*.expect"))
	if len(expects) == 0 {
		t.Fatal("no invalid fixtures")
	}
	for _, e := range expects {
		name := strings.TrimSuffix(filepath.Base(e), ".expect")
		t.Run(name, func(t *testing.T) {
			b, err := os.ReadFile(e)
			if err != nil {
				t.Fatal(err)
			}
			wantCode, wantPtr, _ := strings.Cut(strings.TrimSpace(string(b)), ":")
			code, out, errs := runCmd("check", "--dir", filepath.Join(pol, "invalid", name))
			if code == 0 {
				t.Fatalf("exit 0; stdout %q", out)
			}
			for _, l := range strings.Split(out+errs, "\n") {
				if m := line.FindStringSubmatch(l); m != nil && m[1] == wantCode && (wantPtr == "" || m[2] == wantPtr) {
					return
				}
			}
			t.Errorf("no line %s%s in output:\n%s%s", wantCode, map[bool]string{true: ":" + wantPtr}[wantPtr != ""], out, errs)
		})
	}
}

// 26-AC-1 (D1): CI mode checks only what is present.
func TestCheck_PartialTreeExitsZero_26AC1(t *testing.T) {
	code, out, errs := runCmd("check", "--dir", filepath.Join(pol, "runner-only", "missing-kinds"))
	if code != 0 || !regexp.MustCompile(`sha256:[0-9a-f]{64}`).MatchString(out) {
		t.Fatalf("exit %d; stdout %q stderr %q; want exit 0 and the revision printed", code, out, errs)
	}
}

// Usage errors exit non-zero.
func TestUsageErrorsExitNonZero_26AC1(t *testing.T) {
	for _, args := range [][]string{{}, {"frobnicate"}, {"check", "--dir", filepath.Join(pol, "does-not-exist")}} {
		if code, _, _ := runCmd(args...); code == 0 {
			t.Errorf("args %v: exit 0, want non-zero", args)
		}
	}
}
