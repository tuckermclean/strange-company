package cli

import (
	"sort"
	"strings"
	"testing"
)

// 50-AC-13 (S9 review 1 B2): the command tree is strict. A leaf that accepts "any flag" (the "*" catch-all) makes the
// documented-invocation check vacuous for that command, since `--typo` parses. Together with TestCLIIndexCoverage (every
// documented invocation parses) this pins "zero unknown flags": no leaf is lenient, and every documented flag is listed.
func TestTree_NoLeafAcceptsArbitraryFlags_50AC13(t *testing.T) {
	t.Parallel()
	var lenient []string
	var walk func(path string, c *cmd)
	walk = func(path string, c *cmd) {
		if c.lenient {
			lenient = append(lenient, path)
		}
		for name, sub := range c.subs {
			walk(path+" "+name, sub)
		}
	}
	for name, c := range tree {
		walk(name, c)
	}
	sort.Strings(lenient)
	if len(lenient) > 0 {
		t.Errorf("50-AC-13: %d commands accept arbitrary flags (the \"*\" catch-all); list each documented flag instead:\n%s",
			len(lenient), strings.Join(lenient, "\n"))
	}
}
