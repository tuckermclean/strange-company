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
	exe, comm, cmd       string
}

// commOf returns the command name from /proc/<pid>/stat (world readable even when /proc/<pid>/exe is not), with
// spaces replaced so that it is one field.
func commOf(pid int) string {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return "-"
	}
	s := string(b)
	i, j := strings.Index(s, "("), strings.LastIndex(s, ")")
	if i < 0 || j < i {
		return "-"
	}
	return strings.ReplaceAll(s[i+1:j], " ", "_")
}

// cmdOf returns the command line from /proc/<pid>/cmdline (readable whether or not the process is dumpable) as one
// field: arguments joined by commas, spaces replaced.
func cmdOf(pid int) string {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil || len(b) == 0 {
		return "-"
	}
	s := strings.TrimSuffix(string(b), "\x00")
	return strings.ReplaceAll(strings.ReplaceAll(s, "\x00", ","), " ", "_")
}

// exeOf returns the target of /proc/<pid>/exe, or "-" when it cannot be read.
func exeOf(pid int) string {
	exe, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
	if err != nil || exe == "" {
		return "-"
	}
	return exe
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
		out = append(out, procEntry{pid: pid, ppid: ppid, pgrp: pgrp, sid: sid, exe: exeOf(pid), comm: commOf(pid), cmd: cmdOf(pid)})
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
