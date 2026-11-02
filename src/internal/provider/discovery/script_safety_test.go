package discovery_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"strange.company/sc/internal/provider/discovery"
)

// 05-AC-3 and 05-R-1 (plan 05 section 22, script safety): the discovery script runs under strace and changes nothing.
// A container with a read-only root filesystem cannot be nested inside the ci container, so the test traces every
// syscall that could modify the system instead: it fails on a write-mode open (other than /dev/null), on any
// create/unlink/rename/mkdir/truncate/symlink/link, and on a write to a descriptor that is neither 1, 2 nor opened on
// /dev/null. strace is a ci image tool; its absence is a failure, never a skip.

const straceSyscalls = "openat,open,creat,unlink,unlinkat,rename,renameat,renameat2,mkdir,mkdirat,write,truncate,symlink,link"

var (
	reOpenAt  = regexp.MustCompile(`^(?:\d+\s+)?openat\([^,]+,\s*"((?:[^"\\]|\\.)*)"[^,]*,\s*([A-Z_|0-9]+)`)
	reOpen    = regexp.MustCompile(`^(?:\d+\s+)?open\("((?:[^"\\]|\\.)*)"[^,]*,\s*([A-Z_|0-9]+)`)
	reWrite   = regexp.MustCompile(`^(?:\d+\s+)?write\((\d+),`)
	reMutate  = regexp.MustCompile(`^(?:\d+\s+)?(creat|unlink|unlinkat|rename|renameat|renameat2|mkdir|mkdirat|truncate|symlink|link)\(`)
	reRet     = regexp.MustCompile(`\)\s+=\s+(-?\d+)`)
	writeFlag = regexp.MustCompile(`O_WRONLY|O_RDWR|O_CREAT|O_TRUNC|O_APPEND|O_TMPFILE`)
)

func requireScript(t *testing.T) []byte {
	t.Helper()
	s := discovery.Script()
	if len(bytes.TrimSpace(s)) == 0 {
		t.Fatalf("discovery.Script() is empty: the embedded discovery script does not exist yet")
	}
	return s
}

// 05-AC-3: the script is syntactically valid POSIX sh.
func TestScript_ParsesAsPosixSh_05AC3(t *testing.T) {
	script := requireScript(t)
	if !bytes.HasPrefix(script, []byte("#!/bin/sh")) {
		t.Errorf("the script does not start with #!/bin/sh (POSIX sh, no bashisms)")
	}
	cmd := exec.Command("sh", "-n")
	cmd.Stdin = bytes.NewReader(script)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("sh -n rejects the script: %v\n%s", err, out)
	}
}

// 05-AC-3: strace reports zero writes outside stdout, stderr and /dev/null, and the script prints its JSON document.
func TestScript_ReadOnlyUnderStrace_05AC3(t *testing.T) {
	script := requireScript(t)
	straceBin, err := exec.LookPath("strace")
	if err != nil {
		t.Fatalf("strace is not installed: the script-safety test needs it in the ci image (toolbox/tools.lock.yaml): %v", err)
	}
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	work := filepath.Join(dir, "work")
	for _, d := range []string{home, work} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	trace := filepath.Join(dir, "trace.txt")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, straceBin, "-f", "-qq", "-s", "4096", "-o", trace, "-e", "trace="+straceSyscalls, "sh", "-s")
	cmd.Dir = work
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "LC_ALL=C", "TMPDIR=" + filepath.Join(dir, "no-such-tmp")}
	cmd.Stdin = bytes.NewReader(script)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("strace sh -s failed: %v\nstderr:\n%s", err, stderr.String())
	}

	var doc map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &doc); err != nil {
		t.Fatalf("the script's stdout is not one JSON object: %v\nstdout:\n%.2000s\nstderr:\n%.1000s", err, stdout.String(), stderr.String())
	}
	if _, ok := doc["missing_tools"].([]any); !ok {
		t.Errorf("the JSON document has no missing_tools list (fixtures README): %v", doc)
	}

	raw, err := os.ReadFile(trace)
	if err != nil {
		t.Fatalf("strace wrote no trace file: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) < 5 {
		t.Fatalf("the trace has %d lines: strace did not follow the script (stderr: %s)", len(lines), stderr.String())
	}
	nullFDs := map[string]bool{"1": true, "2": true}
	var violations []string
	pendingNull := map[string]bool{}
	for _, l := range lines {
		pid := ""
		if f := strings.Fields(l); len(f) > 0 {
			pid = f[0]
		}
		if strings.Contains(l, "resumed>") {
			if pendingNull[pid] {
				if m := reRet.FindStringSubmatch(l); m != nil {
					if n, _ := strconv.Atoi(m[1]); n >= 0 {
						nullFDs[m[1]] = true
					}
				}
				delete(pendingNull, pid)
			}
			continue
		}
		var path, flags string
		if m := reOpenAt.FindStringSubmatch(l); m != nil {
			path, flags = m[1], m[2]
		} else if m := reOpen.FindStringSubmatch(l); m != nil {
			path, flags = m[1], m[2]
		}
		switch {
		case flags != "":
			if path == "/dev/null" {
				if strings.Contains(l, "<unfinished") {
					pendingNull[pid] = true
				} else if m := reRet.FindStringSubmatch(l); m != nil {
					if n, _ := strconv.Atoi(m[1]); n >= 0 {
						nullFDs[m[1]] = true
					}
				}
				continue
			}
			if writeFlag.MatchString(flags) {
				violations = append(violations, l)
			}
		case reMutate.MatchString(l):
			violations = append(violations, l)
		default:
			if m := reWrite.FindStringSubmatch(l); m != nil && !nullFDs[m[1]] {
				violations = append(violations, l)
			}
		}
	}
	if len(violations) > 0 {
		show := violations
		if len(show) > 10 {
			show = show[:10]
		}
		t.Errorf("the discovery script is not read-only (05-R-1): %d traced writes, first:\n%s", len(violations), strings.Join(show, "\n"))
	}
}
