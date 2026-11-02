package discovery_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// 05-AC-3 and 05-R-1 as amended by CD-75 (plan 05 section 22): the discovery script runs under strace -f with the complete
// list of mutating syscall classes and none occurs. A second test runs deliberately mutating control commands through
// the same tracer and requires each to be detected, so the tracer and the analysis are proven able to see what the safety
// test says it excludes. Neither test skips: strace is a ci image tool.

// s7Syscalls is the list of plan 05 section 22: openat/open/openat2/creat, write/writev/pwrite64 (and the vectored and
// offset variants), unlink*, rename*, mkdir*, rmdir, symlink*, link*, truncate/ftruncate, chmod/fchmod*, chown*, mknod*,
// utimensat (and its older forms) and the *xattr calls.
const s7Syscalls = "open,openat,openat2,creat,write,writev,pwrite64,pwritev,pwritev2," +
	"unlink,unlinkat,rename,renameat,renameat2,mkdir,mkdirat,rmdir,symlink,symlinkat,link,linkat," +
	"truncate,ftruncate,chmod,fchmod,fchmodat,fchmodat2,chown,fchown,lchown,fchownat,mknod,mknodat," +
	"utimensat,utimes,utime,futimesat,setxattr,lsetxattr,fsetxattr,removexattr,lremovexattr,fremovexattr"

var (
	s7Open    = regexp.MustCompile(`^(?:\d+\s+)?(?:open|openat|openat2)\((?:[^,"]+,\s*)?"((?:[^"\\]|\\.)*)"`)
	s7OpenFl  = regexp.MustCompile(`O_WRONLY|O_RDWR|O_CREAT|O_TRUNC|O_APPEND|O_TMPFILE`)
	s7Write   = regexp.MustCompile(`^(?:\d+\s+)?(?:write|writev|pwrite64|pwritev|pwritev2)\((\d+),`)
	s7Mutate  = regexp.MustCompile(`^(?:\d+\s+)?(creat|unlink|unlinkat|rename|renameat|renameat2|mkdir|mkdirat|rmdir|symlink|symlinkat|link|linkat|truncate|ftruncate|chmod|fchmod|fchmodat|fchmodat2|chown|fchown|lchown|fchownat|mknod|mknodat|utimensat|utimes|utime|futimesat|setxattr|lsetxattr|fsetxattr|removexattr|lremovexattr|fremovexattr)\(`)
	s7Ret     = regexp.MustCompile(`\)\s+=\s+(-?\d+)`)
	s7Pid     = regexp.MustCompile(`^(\d+)\s`)
	s7Unfinis = regexp.MustCompile(`<unfinished \.\.\.>`)
)

// s7Violations returns the lines of an `strace -f` trace that mutate the system: a write-mode open of anything but
// /dev/null, a call of the mutating classes, or a write to a descriptor that is neither 1, 2 nor opened on /dev/null.
func s7Violations(trace string) []string {
	nullFDs := map[string]bool{"1": true, "2": true}
	pendingNull := map[string]bool{}
	var out []string
	for _, l := range strings.Split(strings.TrimSpace(trace), "\n") {
		pid := ""
		if m := s7Pid.FindStringSubmatch(l); m != nil {
			pid = m[1]
		}
		if strings.Contains(l, "resumed>") {
			if pendingNull[pid] {
				if m := s7Ret.FindStringSubmatch(l); m != nil {
					if n, _ := strconv.Atoi(m[1]); n >= 0 {
						nullFDs[m[1]] = true
					}
				}
				delete(pendingNull, pid)
			}
			continue
		}
		if m := s7Open.FindStringSubmatch(l); m != nil {
			if m[1] == "/dev/null" {
				if s7Unfinis.MatchString(l) {
					pendingNull[pid] = true
				} else if r := s7Ret.FindStringSubmatch(l); r != nil {
					if n, _ := strconv.Atoi(r[1]); n >= 0 {
						nullFDs[r[1]] = true
					}
				}
				continue
			}
			if s7OpenFl.MatchString(l) {
				out = append(out, l)
			}
			continue
		}
		if s7Mutate.MatchString(l) {
			out = append(out, l)
			continue
		}
		if m := s7Write.FindStringSubmatch(l); m != nil && !nullFDs[m[1]] {
			out = append(out, l)
		}
	}
	return out
}

// s7Trace runs argv under strace -f with the s7 syscall list in work (HOME is an empty temporary directory) and
// returns the trace.
func s7Trace(t *testing.T, work string, stdin []byte, argv ...string) string {
	t.Helper()
	straceBin, err := exec.LookPath("strace")
	if err != nil {
		t.Fatalf("strace is not installed: the script-safety test needs it in the ci image (toolbox/tools.lock.yaml): %v", err)
	}
	home := t.TempDir()
	trace := filepath.Join(t.TempDir(), "trace.txt")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	args := append([]string{"-f", "-qq", "-s", "4096", "-o", trace, "-e", "trace=" + s7Syscalls}, argv...)
	cmd := exec.CommandContext(ctx, straceBin, args...)
	cmd.Dir = work
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "LC_ALL=C"}
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &bytes.Buffer{}, &stderr
	if err := cmd.Run(); err != nil {
		t.Logf("strace %v: %v (stderr: %.500s)", argv, err, stderr.String())
	}
	raw, err := os.ReadFile(trace)
	if err != nil {
		t.Fatalf("strace wrote no trace (stderr: %s): %v", stderr.String(), err)
	}
	return string(raw)
}

// 05-AC-3 (CD-75): none of the mutating syscalls of plan 05 section 22 occurs while the script runs.
func TestScript_NoMutatingSyscallOfTheCompleteList_05AC3(t *testing.T) {
	script := requireScript(t)
	work := t.TempDir()
	tr := s7Trace(t, work, script, "sh", "-s")
	if n := len(strings.Split(strings.TrimSpace(tr), "\n")); n < 5 {
		t.Fatalf("the trace has %d lines: strace did not follow the script", n)
	}
	if v := s7Violations(tr); len(v) > 0 {
		if len(v) > 10 {
			v = v[:10]
		}
		t.Errorf("the discovery script performs mutating syscalls (05-R-1), first:\n%s", strings.Join(v, "\n"))
	}
	entries, err := os.ReadDir(work)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("the working directory is no longer empty: %d entries", len(entries))
	}
}

// 05-AC-3 (CD-75): the tracer and the analysis detect each class of the list: every control command mutates something
// and must produce at least one violation.
func TestScript_StraceDetectsEveryMutatingControl_05AC3(t *testing.T) {
	controls := []struct{ name, cmd string }{
		{"symlink", "ln -s a newlink"},
		{"hardlink", "ln a newhard"},
		{"rmdir", "rmdir d"},
		{"chmod", "chmod 600 a"},
		{"touch-existing-utimensat", "touch a"},
		{"touch-new-create", "touch brandnew"},
		{"mkdir", "mkdir newdir"},
		{"unlink", "rm a"},
		{"rename", "mv a moved"},
		{"truncate", "truncate -s 0 a"},
		{"mkfifo-mknod", "mkfifo pipe0"},
		{"chown", "chown \"$(id -u)\" a"},
		{"redirect-write", "echo x > a"},
		{"append", "echo x >> a"},
		{"write-fd", "exec 3<>a; echo x >&3"},
	}
	for _, c := range controls {
		t.Run(c.name, func(t *testing.T) {
			work := t.TempDir()
			if err := os.WriteFile(filepath.Join(work, "a"), []byte("data\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(filepath.Join(work, "d"), 0o755); err != nil {
				t.Fatal(err)
			}
			tr := s7Trace(t, work, nil, "sh", "-c", c.cmd)
			if v := s7Violations(tr); len(v) == 0 {
				t.Errorf("%q was not detected as a mutation; trace:\n%.1500s", c.cmd, tr)
			}
		})
	}
	// and a harmless control is not flagged: reads and a write to stdout only.
	work := t.TempDir()
	tr := s7Trace(t, work, nil, "sh", "-c", "cat /etc/os-release >/dev/null; echo hello; ls / >/dev/null 2>&1")
	if v := s7Violations(tr); len(v) > 0 {
		t.Errorf("a read-only control was flagged:\n%s", strings.Join(v, "\n"))
	}
}
