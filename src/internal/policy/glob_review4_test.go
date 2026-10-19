package policy

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
)

// refGlobRegexp is a copy of globRegexp in src/internal/lint/tdd/gitutil.go (anyStar=false) with dotall
// ("(?s)") so `**` matches "\n" like `*` and `?` (CD-68 newline clarification): the reference semantics of path globs.
func refGlobRegexp(pat string) *regexp.Regexp {
	var b strings.Builder
	b.WriteString("(?s)^")
	for i := 0; i < len(pat); {
		switch {
		case strings.HasPrefix(pat[i:], "**/"):
			b.WriteString("(?:.*/)?")
			i += 3
		case strings.HasPrefix(pat[i:], "**"):
			b.WriteString(".*")
			i += 2
		case pat[i] == '*':
			b.WriteString("[^/]*")
			i++
		case pat[i] == '?':
			b.WriteString("[^/]")
			i++
		default:
			b.WriteString(regexp.QuoteMeta(pat[i : i+1]))
			i++
		}
	}
	b.WriteString("$")
	return regexp.MustCompile(b.String())
}

// 22-AC-1 (CD-68, review 1 findings 1 and 2): `**/` is zero or more whole directories and never `**`; a newline
// is an ordinary character for `*`, `?` and `**`.
func TestExactGlobOverlap_RecursivePrefixIsNotAnyString_22AC1(t *testing.T) {
	cases := []struct {
		name                         string
		autoAllowed, autoDenied, hum []string
		overlap                      bool
	}{
		{"sql-migrations-is-not-a-migrations-dir", []string{"**/*.md"}, []string{"**/migrations/**"}, []string{"sql-migrations/**"}, true},
		{"star-star-slash-needs-a-slash", []string{"**/x.md"}, nil, []string{"ax.md"}, false},
		{"inner-star-star-slash-needs-a-slash", []string{"a/**/b"}, nil, []string{"a/xb"}, false},
		{"inner-star-star-slash-with-directory", []string{"a/**/b"}, nil, []string{"a/x/b"}, true},
		{"leading-star-star-slash-zero-dirs", []string{"**/x.md"}, nil, []string{"x.md"}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := mutatedValid(t, twoClasses(t, c.autoAllowed, c.autoDenied, c.hum))
			_, issues := Check(dir, opts(t, false))
			noStructuralIssues(t, issues)
			got := issuesWith(issues, codeOverlap)
			if c.overlap && len(got) == 0 {
				t.Errorf("auto %v minus %v vs human %v: want %s; issues:\n%s", c.autoAllowed, c.autoDenied, c.hum, codeOverlap, joinIssues(issues))
			}
			if !c.overlap && len(got) != 0 {
				t.Errorf("auto %v minus %v vs human %v: want no %s; issues:\n%s", c.autoAllowed, c.autoDenied, c.hum, codeOverlap, joinIssues(got))
			}
		})
	}
}

// 22-AC-1 (CD-68, review 1 finding 1): a class glob that is not entirely inside the protected set is not dead,
// and not a loosening, only because the protected glob starts with `**/`.
func TestExactGlobDead_ProtectedStarStarSlashIsNotAnyString_22AC1(t *testing.T) {
	dir := mutatedValid(t, twoClasses(t, []string{"docs/*CODEOWNERS"}, nil, []string{"gitops/**"}, "**/CODEOWNERS"))
	_, issues := Check(dir, opts(t, false))
	for _, i := range issues {
		if i.Code == codeDead || i.Code == "POLICY_LOOSENING" {
			t.Errorf("docs/*CODEOWNERS is not inside **/CODEOWNERS (docs/xCODEOWNERS is outside): %s", i)
		}
	}
}

// 22-AC-1 (CD-68, review 1 finding 2): `?`, `*` and `**` all match '\n' (an ordinary non-'/' character), so `?` is
// inside `**` and docs/* has no path outside docs/**.
func TestExactGlobNewlineIsInTheAlphabet_22AC1(t *testing.T) {
	if in, ok := globInsideUnion("?", []string{"**"}); !ok || !in {
		t.Errorf("globInsideUnion(?, [**]) = (%v, %v), want (true, true): \"\\n\" matches both ? and **", in, ok)
	}
	if f, ok := globsIntersect("docs/*", "docs/*", []string{"docs/**"}); !ok || f {
		t.Errorf("globsIntersect(docs/*, docs/*, [docs/**]) = (%v, %v), want (false, true): docs/\\n is matched by ** too", f, ok)
	}
}

// enumGlobs returns every glob of at most maxTokens tokens, in a fixed order, without duplicates.
func enumGlobs(tokens []string, maxTokens int) []string {
	seen := map[string]bool{"": true}
	out := []string{""}
	level := []string{""}
	for n := 0; n < maxTokens; n++ {
		var next []string
		for _, g := range level {
			for _, tk := range tokens {
				s := g + tk
				if !seen[s] {
					seen[s] = true
					out = append(out, s)
				}
				next = append(next, s)
			}
		}
		level = next
	}
	return out
}

// enumPaths returns every string of at most maxLen runes over alphabet.
func enumPaths(alphabet []string, maxLen int) []string {
	out := []string{""}
	level := []string{""}
	for n := 0; n < maxLen; n++ {
		var next []string
		for _, p := range level {
			for _, c := range alphabet {
				next = append(next, p+c)
			}
		}
		out = append(out, next...)
		level = next
	}
	return out
}

type pathSet []uint64

func (a pathSet) set(i int) { a[i>>6] |= 1 << (uint(i) & 63) }
func (a pathSet) andNot3(b, c pathSet) bool { // a & b & ^c is non-empty
	for i := range a {
		if a[i]&b[i]&^c[i] != 0 {
			return true
		}
	}
	return false
}

// 22-AC-1 (CD-68 "exact", review 1 findings 1 and 2): globsIntersect and globInsideUnion agree with a brute-force
// reference on every glob up to a small length. Reference: each glob is compiled with the checker's semantics and
// matched against every path over {a, b, c, /, "\n"} up to length 5. The enumeration is fixed (no randomness, no
// clock): a fixed subset of the pairs of globs of at most 3 tokens of {a, b, /, *, ?, **/, **}, alone and with
// one deny glob of at most 2 tokens, all pairs of globs of at most 2 tokens with every one-token deny, and
// every 7th glob of at most 4 tokens inside one-token globs, the globs of at most 3 tokens inside the globs of
// at most 2 tokens and inside pairs of them. Paths are limited to length 5, so the globs are kept short enough
// that a witness never needs more.
func TestExactGlobAgreesWithBruteForce_22AC1(t *testing.T) {
	tokens := []string{"a", "b", "/", "*", "?", "**/", "**"}
	paths := enumPaths([]string{"a", "b", "c", "/", "\n"}, 5)
	words := (len(paths) + 63) / 64
	cache := map[string]pathSet{}
	matches := func(g string) pathSet {
		if s, ok := cache[g]; ok {
			return s
		}
		re := refGlobRegexp(g)
		s := make(pathSet, words)
		for i, p := range paths {
			if re.MatchString(p) {
				s.set(i)
			}
		}
		cache[g] = s
		return s
	}
	all3 := enumGlobs(tokens, 3)
	all4 := enumGlobs(tokens, 4)
	all2 := enumGlobs(tokens, 2)
	all1 := enumGlobs(tokens, 1)
	none := make(pathSet, words)

	const maxReports = 25
	bad := 0
	report := func(format string, args ...any) {
		bad++
		if bad <= maxReports {
			t.Errorf(format, args...)
		}
	}
	checks := 0

	intersect := func(a, b string, deny []string) {
		checks++
		d := none
		if len(deny) > 0 {
			d = make(pathSet, words)
			for _, g := range deny {
				for i, w := range matches(g) {
					d[i] |= w
				}
			}
		}
		want := matches(a).andNot3(matches(b), d)
		got, ok := globsIntersect(a, b, deny)
		if !ok {
			report("globsIntersect(%q, %q, %q): search gave up", a, b, deny)
		} else if got != want {
			report("globsIntersect(%q, %q, %q) = %v, brute force says %v", a, b, deny, got, want)
		}
	}
	k := 0
	for i, a := range all3 {
		for _, b := range all3[i:] {
			if k++; k%6 == 0 {
				intersect(a, b, nil)
			}
		}
	}
	for i, a := range all2 {
		for _, b := range all2[i:] {
			for _, d := range all1 {
				intersect(a, b, []string{d})
			}
		}
	}
	for i, a := range all3 {
		for j, b := range all3 {
			if (i+j)%23 == 0 {
				intersect(a, b, []string{all2[(i*5+j*3)%len(all2)]})
			}
		}
	}

	inside := func(g string, set []string) {
		checks++
		u := make(pathSet, words)
		for _, s := range set {
			for i, w := range matches(s) {
				u[i] |= w
			}
		}
		want := !matches(g).andNot3(matches(g), u)
		got, ok := globInsideUnion(g, set)
		if !ok {
			report("globInsideUnion(%q, %q): search gave up", g, set)
		} else if got != want {
			report("globInsideUnion(%q, %q) = %v, brute force says %v", g, set, got, want)
		}
	}
	for i, g := range all4 {
		if i%7 != 0 {
			continue
		}
		for _, s := range all1 {
			inside(g, []string{s})
		}
	}
	for i, g := range all3 {
		for j, s := range all2 {
			inside(g, []string{s})
			if (i+j)%9 == 0 {
				inside(g, []string{s, all2[(i*3+j*5)%len(all2)]})
			}
		}
	}
	if bad > maxReports {
		t.Errorf("%d mismatches in %d checks (first %d shown)", bad, checks, maxReports)
	}
	t.Log(fmt.Sprintf("%d checks over %d paths, %d distinct globs matched", checks, len(paths), len(cache)))
}

// 26-AC-1 (review 1 finding 5): ROLE_MISSING names the file the missing role should have, roles/<role>.yaml, not
// the first role file that happens to exist.
func TestRoleMissingIsFiledAgainstTheMissingRolesFile_26AC1(t *testing.T) {
	for _, runner := range []bool{false, true} {
		_, issues := Check(td("invalid", "invariants", "role-missing"), opts(t, runner))
		got := issuesWith(issues, "ROLE_MISSING")
		if len(got) != 1 {
			t.Fatalf("runner=%v: ROLE_MISSING issues = %d, want 1:\n%s", runner, len(got), joinIssues(issues))
		}
		if got[0].File != "roles/reviewer.yaml" {
			t.Errorf("runner=%v: ROLE_MISSING file = %q, want roles/reviewer.yaml", runner, got[0].File)
		}
	}
}
