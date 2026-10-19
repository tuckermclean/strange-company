package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 26-AC-1, 26-AC-5 and 22-AC-1: `sc-policy check --dir <fixture>` exits non-zero for every invariant fixture and
// prints the fixture's primary code (and pointer) as `<CODE>[:<json-pointer>] <file>: <message>`. The sidecar's
// first line is `CODE[:pointer]`; later lines (`also`, `msg`) are read by the library tests.
func TestCheckPrintsInvariantCodes_26AC1_26AC5_22AC1(t *testing.T) {
	n := 0
	for _, group := range []string{"invariants", "verification"} {
		expects, _ := filepath.Glob(filepath.Join(pol, "invalid", group, "*.expect"))
		for _, e := range expects {
			n++
			name := strings.TrimSuffix(filepath.Base(e), ".expect")
			t.Run(group+"/"+name, func(t *testing.T) {
				b, err := os.ReadFile(e)
				if err != nil {
					t.Fatal(err)
				}
				first, _, _ := strings.Cut(string(b), "\n")
				wantCode, wantPtr, _ := strings.Cut(strings.TrimSpace(first), ":")
				code, out, errs := runCmd("check", "--dir", filepath.Join(pol, "invalid", group, name))
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
	if n < 18+15 {
		t.Fatalf("found %d invariant fixtures, want at least 33", n)
	}
}

// 22-AC-1: the repository-side documents under <dir>/.strange-company/ are checked by --dir.
func TestCheckAcceptsRepositorySideFixture_22AC1(t *testing.T) {
	code, out, errs := runCmd("check", "--dir", filepath.Join(pol, "accepted", "repo-side"))
	if code != 0 {
		t.Fatalf("exit %d; stdout %q stderr %q", code, out, errs)
	}
}
