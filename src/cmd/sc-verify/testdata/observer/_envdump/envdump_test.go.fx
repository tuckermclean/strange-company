package fx

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// gitAbove reports whether cwd or any ancestor directory holds a .git entry.
func gitAbove() bool {
	wd, err := os.Getwd()
	if err != nil {
		return true
	}
	for d := wd; ; {
		if _, err := os.Lstat(d + "/.git"); err == nil {
			return true
		}
		if d == "/" || d == "." {
			return false
		}
		i := strings.LastIndex(d, "/")
		if i <= 0 {
			d = "/"
		} else {
			d = d[:i]
		}
	}
}

// TestEnvDump records what the code under test can see: its environment (twice, once through /proc) and its
// process ancestry. The verifying test reads $FX_OUT/env.txt.
func TestEnvDump(t *testing.T) { dumpEnv(t) }

func dumpEnv(t testing.TB) {
	var b strings.Builder
	wd, _ := os.Getwd()
	fmt.Fprintf(&b, "BEGIN\ncwd=%s\n", wd)
	for _, e := range os.Environ() {
		fmt.Fprintf(&b, "ENV %s\n", e)
	}
	raw, _ := os.ReadFile("/proc/self/environ")
	for _, e := range strings.Split(string(raw), "\x00") {
		if e != "" {
			fmt.Fprintf(&b, "PROCENV %s\n", e)
		}
	}
	home, tmp := os.Getenv("HOME"), os.Getenv("TMPDIR")
	hs, herr := os.Stat(home)
	ts, terr := os.Stat(tmp)
	fmt.Fprintf(&b, "HOMESTAT dir=%v\n", herr == nil && hs.IsDir())
	fmt.Fprintf(&b, "TMPSTAT dir=%v\n", terr == nil && ts.IsDir())
	fmt.Fprintf(&b, "GITABOVE %v\n", gitAbove())
	for _, p := range ancestors() {
		fmt.Fprintf(&b, "PROC pid=%d ppid=%d pgrp=%d sid=%d comm=%s exe=%s cmd=%s\n", p.pid, p.ppid, p.pgrp, p.sid, p.comm, p.exe, p.cmd)
	}
	b.WriteString("END\n")
	appendOut(t, "env.txt", b.String())
}
