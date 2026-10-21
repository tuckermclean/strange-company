package fx

import (
	"flag"
	"os"
	"path/filepath"
	"testing"
)

var reportFlag = flag.String("report", "", "JUnit report path")

const outsideXML = `<?xml version="1.0" encoding="UTF-8"?>
<testsuites><testsuite name="o" tests="1"><testcase classname="fx" name="TestOutside"/></testsuite></testsuites>
`

// TestMain writes the JUnit report after the run, as a test runner would, and exits 1 because the report holds a
// failure. FX_MODE=symlink replaces the report by a symlink to a file outside the workspace; FX_MODE=plant
// replaces the record directory's sealed directory and observed.json by symlinks (the suite can reach them).
func TestMain(m *testing.M) {
	m.Run()
	rp := *reportFlag
	out := os.Getenv("FX_OUT")
	if os.Getenv("FX_MODE") == "plant" {
		st := os.Getenv("FX_STATE")
		_ = os.Rename(filepath.Join(st, "sealed"), filepath.Join(st, "sealed.moved"))
		_ = os.Symlink(filepath.Join(out, "evil-sealed"), filepath.Join(st, "sealed"))
		_ = os.Remove(filepath.Join(st, "observed.json"))
		_ = os.Symlink(filepath.Join(out, "victim.txt"), filepath.Join(st, "observed.json"))
	}
	_ = os.MkdirAll(filepath.Dir(rp), 0o755)
	if os.Getenv("FX_MODE") == "symlink" {
		outside := filepath.Join(out, "outside.xml")
		_ = os.WriteFile(outside, []byte(outsideXML), 0o644)
		_ = os.Remove(rp)
		_ = os.Symlink(outside, rp)
	} else {
		orig, err := os.ReadFile("original.xml")
		if err != nil {
			os.Exit(3)
		}
		if err := os.WriteFile(rp, orig, 0o644); err != nil {
			os.Exit(4)
		}
	}
	os.Exit(1)
}

func TestDummy(t *testing.T) {}
