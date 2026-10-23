package fx

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

func errName(err error) string {
	switch {
	case err == nil:
		return "OK"
	case errors.Is(err, syscall.EACCES):
		return "EACCES"
	case errors.Is(err, syscall.EPERM):
		return "EPERM"
	case errors.Is(err, syscall.ENOENT):
		return "ENOENT"
	}
	return "ERR:" + err.Error()
}

// firstMapped returns an address of the process that is mapped readable, from /proc/<pid>/maps, or a typical
// text address when maps cannot be read.
func firstMapped(pid int) int64 {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/maps", pid))
	if err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			f := strings.Fields(l)
			if len(f) >= 2 && strings.HasPrefix(f[1], "r") {
				lo, _, _ := strings.Cut(f[0], "-")
				if a, err := strconv.ParseInt(lo, 16, 64); err == nil {
					return a
				}
			}
		}
	}
	return 0x400000
}

// TestProcSpy is code under test hunting for the sc-verify run process: it finds it among its ancestors through
// its command line (never through exe, which may be unreadable) and tries to open its mem and environ files. It
// records, for each, how the open failed ("OK" when it did not) and how many bytes it could read.
func TestProcSpy(t *testing.T) {
	var b strings.Builder
	found := 0
	for _, p := range ancestors() {
		// The run process is told by its command line: sc-verify run --validation ... --record ...
		if !strings.Contains(","+p.cmd+",", ",run,") || !strings.Contains(p.cmd, ",--validation,") || !strings.Contains(p.cmd, ",--record,") {
			continue
		}
		found++
		memErr, memN := "OK", 0
		f, err := os.Open(fmt.Sprintf("/proc/%d/mem", p.pid))
		if err != nil {
			memErr = errName(err)
		} else {
			buf := make([]byte, 64)
			n, rerr := f.ReadAt(buf, firstMapped(p.pid))
			memN = n
			if rerr != nil && n == 0 {
				memErr = "OPEN_OK_READ_" + errName(rerr)
			}
			f.Close()
		}
		envErr, envN := "OK", 0
		g, err := os.Open(fmt.Sprintf("/proc/%d/environ", p.pid))
		if err != nil {
			envErr = errName(err)
		} else {
			buf := make([]byte, 4096)
			n, rerr := g.Read(buf)
			envN = n
			if rerr != nil && n == 0 {
				envErr = "OPEN_OK_READ_" + errName(rerr)
			}
			g.Close()
		}
		fmt.Fprintf(&b, "RUN pid=%d mem=%s memread=%d environ=%s environread=%d\n", p.pid, memErr, memN, envErr, envN)
	}
	fmt.Fprintf(&b, "FOUND %d\n", found)
	appendOut(t, "procspy.txt", b.String())
}
