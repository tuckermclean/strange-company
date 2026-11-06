package lab_test

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"strange.company/sc/internal/lab"
)

// F09 05-AC-6 (review of the S3 front wiring): Down trusts a pid file only when the process name fits that file.

func downWithPIDs(t *testing.T, files map[string]int, names map[int]string) (signalled map[int]bool) {
	t.Helper()
	r := newRig(t, debianSHA256(sha256hex(debianBytes)))
	runDir := filepath.Join(r.env.RunBase, "t1")
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for f, pid := range files {
		if err := os.WriteFile(filepath.Join(runDir, f+".pid"), []byte(strconv.Itoa(pid)+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	signalled = map[int]bool{}
	r.env.ProcName = func(pid int) (string, error) { return names[pid], nil }
	r.env.Kill = func(pid int) error { signalled[pid] = true; return nil }
	r.env.KillHard = func(pid int) error { signalled[pid] = true; return nil }
	r.env.Alive = func(pid int) bool { return false }
	if err := lab.Down(r.env, "t1"); err != nil {
		t.Fatal(err)
	}
	return signalled
}

// 05-AC-6
func TestDown_ProcessNameMustMatchItsPIDFile_05AC6(t *testing.T) {
	t.Parallel()
	// matching names are signalled
	got := downWithPIDs(t, map[string]int{"s3-front": 5001, "s3": 5002, "node-01": 5003},
		map[int]string{5001: "sc-lab", 5002: "weed", 5003: "qemu-system-x86"})
	for _, pid := range []int{5001, 5002, 5003} {
		if !got[pid] {
			t.Errorf("pid %d with a matching name was not signalled", pid)
		}
	}
	// every mismatch is left alone
	got = downWithPIDs(t, map[string]int{"s3-front": 5001, "s3": 5002, "node-01": 5003, "node-02": 5004},
		map[int]string{5001: "weed", 5002: "sc-lab", 5003: "weed", 5004: "sc-lab"})
	for _, pid := range []int{5001, 5002, 5003, 5004} {
		if got[pid] {
			t.Errorf("pid %d with a name that does not fit its pid file was signalled", pid)
		}
	}
	got = downWithPIDs(t, map[string]int{"s3": 5002, "node-01": 5003}, map[int]string{5002: "qemu-system-x86", 5003: "weed"})
	if got[5002] || got[5003] {
		t.Errorf("qemu under s3 or weed under a node file was signalled: %v", got)
	}
}

// 05-AC-6
func TestDown_NeverSignalsItself_05AC6(t *testing.T) {
	t.Parallel()
	self := os.Getpid()
	got := downWithPIDs(t, map[string]int{"s3-front": self, "s3": self, "node-01": self},
		map[int]string{self: "sc-lab"})
	if got[self] {
		t.Error("Down signalled the current process")
	}
}

// 05-AC-6 (hardening): the front serves a plaintext-bypass-free lab endpoint on loopback only.
func TestRunS3Front_RefusesNonLoopbackListen_05AC6(t *testing.T) {
	t.Parallel()
	for _, listen := range []string{"0.0.0.0:9000", ":9000", "[::]:9000", "10.0.2.2:9000"} {
		err := lab.RunS3Front(context.Background(), listen, "http://127.0.0.1:1", "/nonexistent/cert.pem", "/nonexistent/key.pem")
		if err == nil || !strings.Contains(err.Error(), "loopback") {
			t.Errorf("listen %q: want a refusal mentioning loopback, got %v", listen, err)
		}
	}
}
