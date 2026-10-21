package fx

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// procEntry is one process of the ancestor chain of the test binary.
type procEntry struct {
	pid, ppid, pgrp, sid int
	exe                  string
}

// statFields parses /proc/<pid>/stat: pid (comm) state ppid pgrp session ...
func statFields(pid int) (ppid, pgrp, sid int, ok bool) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, 0, 0, false
	}
	s := string(b)
	i := strings.LastIndex(s, ")")
	if i < 0 {
		return 0, 0, 0, false
	}
	f := strings.Fields(s[i+1:])
	if len(f) < 4 {
		return 0, 0, 0, false
	}
	ppid, _ = strconv.Atoi(f[1])
	pgrp, _ = strconv.Atoi(f[2])
	sid, _ = strconv.Atoi(f[3])
	return ppid, pgrp, sid, true
}

// ancestors lists this process and every ancestor up to (not including) pid 1.
func ancestors() []procEntry {
	var out []procEntry
	for pid := os.Getpid(); pid > 1; {
		ppid, pgrp, sid, ok := statFields(pid)
		if !ok {
			break
		}
		exe, _ := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
		out = append(out, procEntry{pid: pid, ppid: ppid, pgrp: pgrp, sid: sid, exe: exe})
		pid = ppid
	}
	return out
}

// appendOut appends text to $FX_OUT/<name>; FX_OUT is a directory owned by the verifying test.
func appendOut(t testing.TB, name, text string) {
	t.Helper()
	dir := os.Getenv("FX_OUT")
	if dir == "" {
		t.Fatal("FX_OUT is not set")
	}
	f, err := os.OpenFile(filepath.Join(dir, name), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(text); err != nil {
		t.Fatal(err)
	}
}
