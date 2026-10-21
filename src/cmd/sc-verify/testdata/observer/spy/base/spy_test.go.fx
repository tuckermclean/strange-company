package fx

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
)

// TestSpy reads /proc/<pid>/environ and /proc/<pid>/cmdline of every ancestor and of every process it may read,
// and records them in $FX_OUT/spy.txt (blind TDD-B1: code under test hunting for the evidence nonce).
func TestSpy(t *testing.T) {
	var b strings.Builder
	for _, p := range ancestors() {
		env, eerr := os.ReadFile(fmt.Sprintf("/proc/%d/environ", p.pid))
		cmd, _ := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", p.pid))
		fmt.Fprintf(&b, "ANCESTOR pid=%d ppid=%d exe=%s cmdline=%q environ_err=%v environ=%q\n", p.pid, p.ppid, p.exe, cmd, eerr != nil, env)
	}
	ents, _ := os.ReadDir("/proc")
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		env, eerr := os.ReadFile(fmt.Sprintf("/proc/%d/environ", pid))
		cmd, _ := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
		exe, _ := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
		fmt.Fprintf(&b, "ALL pid=%d exe=%s cmdline=%q environ_err=%v environ=%q\n", pid, exe, cmd, eerr != nil, env)
	}
	if self, err := os.ReadFile("/proc/self/environ"); err == nil {
		fmt.Fprintf(&b, "SELF environ=%q\n", self)
	}
	appendOut(t, "spy.txt", b.String())
}
