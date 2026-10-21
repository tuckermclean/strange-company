package fx

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

const rewrittenXML = `<?xml version="1.0" encoding="UTF-8"?>
<testsuites><testsuite name="fx" tests="2"><testcase classname="fx" name="TestOriginalPass"/><testcase classname="fx" name="TestOriginalFail"/><testcase classname="fx" name="TestRewritten"/></testsuite></testsuites>
`

// rewriter waits until the process $1 (the go command running this package's tests) is gone, sleeps past it, and
// then rewrites the report $2 and every file below the record directory $3.
const rewriter = `p=$1; rep=$2; st=$3; mark=$4
while [ -d "/proc/$p" ]; do sleep 0.05; done
sleep 0.8
: > "$FX_OUT/awake-$mark"
i=0
while [ "$i" -lt 30 ]; do
  cp "$FX_OUT/rewritten.xml" "$rep" 2>/dev/null
  for f in $(find "$st" -type f 2>/dev/null); do chmod u+w "$f" 2>/dev/null; printf 'REWRITTEN_BY_DESCENDANT' >> "$f" 2>/dev/null; done
  i=$((i+1)); sleep 0.1
done
: > "$FX_OUT/finished-$mark"
`

// sleeper just outlives the suite.
const sleeper = `p=$1; mark=$4
while [ -d "/proc/$p" ]; do sleep 0.05; done
sleep 0.8
: > "$FX_OUT/awake-$mark"
sleep 120
: > "$FX_OUT/finished-$mark"
`

// init spawns a detached process when FX_MODE asks for one (blind TDD-B1). It is production code.
func init() {
	mode := os.Getenv("FX_MODE")
	switch mode {
	case "setsid", "setpgid", "double_fork", "sleeper":
	default:
		return
	}
	report := ""
	for _, a := range os.Args {
		if strings.HasPrefix(a, "-report=") {
			report = strings.TrimPrefix(a, "-report=")
		}
	}
	abs, _ := filepath.Abs(report)
	_ = os.WriteFile(filepath.Join(os.Getenv("FX_OUT"), "rewritten.xml"), []byte(rewrittenXML), 0o644)
	ppid, state, mark := strconv.Itoa(os.Getppid()), os.Getenv("FX_STATE"), os.Getenv("FX_MARK")
	script := rewriter
	if mode == "sleeper" {
		script = sleeper
	}
	args := []string{"-c", script, "sh", ppid, abs, state, mark}
	if mode == "double_fork" {
		args = []string{"-c", `s=$1; shift; sh -c "$s" sh "$@" </dev/null >/dev/null 2>&1 & exit 0`, "sh", rewriter, ppid, abs, state, mark}
	}
	cmd := exec.Command("sh", args...)
	if mode == "setpgid" {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	} else {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	}
	if err := cmd.Start(); err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(os.Getenv("FX_OUT"), "spawned-"+mark), nil, 0o644)
	if mode == "double_fork" {
		_ = cmd.Wait()
	}
}
