package main

import (
	"strings"
	"testing"
)

// S6a: `status`, `plan` and `run` are built; the binary no longer answers COMMAND_NOT_IMPLEMENTED for them
// (the tree still parses everything else that is not built, see TestBinary_NotImplementedVersusUnknown_CD70e).
func TestBinary_InstallCommandsAreWired_50AC11(t *testing.T) {
	bin := buildBin(t)
	for _, args := range [][]string{{"status"}, {"status", "--json"}, {"plan"}, {"run"}} {
		_, out := runBin(t, bin, []string{"SC_REPO_ROOT=" + t.TempDir()}, args...)
		if strings.Contains(out, "COMMAND_NOT_IMPLEMENTED") {
			t.Errorf("sc-bootstrap %s: still COMMAND_NOT_IMPLEMENTED:\n%s", strings.Join(args, " "), out)
		}
	}
}
