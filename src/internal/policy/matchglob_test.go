package policy

import (
	"math/rand"
	"testing"
)

// 22-AC-1 (D10): MatchGlob agrees with F04's reference semantics (refGlobRegexp, dotall, `**/` is zero or more
// whole directories) over seeded random glob/path pairs, newlines included. May already pass.
func TestMatchGlob_DifferentialAgainstReference_D10_22AC1(t *testing.T) {
	fixed := []struct {
		glob, path string
		want       bool
	}{
		{"**/x.md", "x.md", true},
		{"**/x.md", "ax.md", false},
		{"a/**/b", "a/xb", false},
		{"a/**/b", "a/x/y/b", true},
		{"docs/*", "docs/\n", true},
		{"docs/*", "docs/a/b", false},
		{"?", "\n", true},
		{"**", "a/\n/b", true},
	}
	for _, c := range fixed {
		if got := MatchGlob(c.glob, c.path); got != c.want {
			t.Errorf("MatchGlob(%q, %q) = %v, want %v", c.glob, c.path, got, c.want)
		}
	}
	rng := rand.New(rand.NewSource(20261020))
	globToks := []string{"*", "**", "**/", "?", "/", "a", "b", ".", "\n", "x.md"}
	pathToks := []string{"/", "a", "b", ".", "\n", "x.md", "ab", "/a/"}
	pick := func(toks []string, max int) string {
		s := ""
		for n := rng.Intn(max + 1); n > 0; n-- {
			s += toks[rng.Intn(len(toks))]
		}
		return s
	}
	for i := 0; i < 20000; i++ {
		g, p := pick(globToks, 6), pick(pathToks, 6)
		if want, got := refGlobRegexp(g).MatchString(p), MatchGlob(g, p); got != want {
			t.Fatalf("MatchGlob(%q, %q) = %v, reference says %v", g, p, got, want)
		}
	}
}
