package policy

import (
	"sort"
	"strings"
)

// Exact glob algebra for the plan 22 section 9.5 path invariants (CD-68).
//
// A glob has the semantics of globRegexp in src/internal/lint/tdd/gitutil.go (anyStar = false), except that a
// newline is an ordinary character everywhere (CD-68):
//
//	**/   zero or more directories (any characters ending in '/', or nothing)
//	**    any string, newlines included
//	*     any string without '/' (newlines included)
//	?     one character that is not '/' (a newline is allowed)
//	else  the character itself (there are no classes or alternations)
//
// Each glob is a small NFA whose state i means "tokens 0..i-1 are consumed"; state len(tokens) accepts. The
// questions the loader asks are decided by a breadth-first search over the subset construction of the product
// of the globs involved, over a finite alphabet: the literal characters appearing in the globs, '/' and one
// "other" character (all characters absent from every glob behave the same). Because the subset states of the
// negated globs are determinised on the fly, a complement (not matched by any of D) is exact.

type tokKind uint8

const (
	tokLit    tokKind = iota // one literal rune
	tokQuest                 // ?  : one rune except '/'
	tokStar                  // *  : any run of runes except '/'
	tokAny                   // ** : any run of runes
	tokDirs                  // **/: zero or more directories
	tokDirsIn                // inside **/: directory characters before the closing '/'
)

type globTok struct {
	kind tokKind
	r    rune
}

func parseGlob(pat string) []globTok {
	var toks []globTok
	rs := []rune(pat)
	for i := 0; i < len(rs); {
		switch {
		case rs[i] == '*' && i+2 < len(rs) && rs[i+1] == '*' && rs[i+2] == '/':
			toks = append(toks, globTok{kind: tokDirs}, globTok{kind: tokDirsIn})
			i += 3
		case rs[i] == '*' && i+1 < len(rs) && rs[i+1] == '*':
			toks = append(toks, globTok{kind: tokAny})
			i += 2
		case rs[i] == '*':
			toks = append(toks, globTok{kind: tokStar})
			i++
		case rs[i] == '?':
			toks = append(toks, globTok{kind: tokQuest})
			i++
		default:
			toks = append(toks, globTok{kind: tokLit, r: rs[i]})
			i++
		}
	}
	return toks
}

// maxGlobStates bounds the product search. It is far above what the shipped policy needs (a few hundred); a
// search that exceeds it reports ok == false and the caller rejects the policy instead of guessing.
const maxGlobStates = 200000

// globSet is a set of globs whose NFA states are laid out in one bit vector.
type globSet struct {
	toks   [][]globTok
	off    []int // offset of glob g's state 0
	total  int
	words  int
	alpha  []rune
	finals []int // bit index of each glob's accepting state
}

func newGlobSet(globs []string) *globSet {
	g := &globSet{}
	lits := map[rune]bool{'/': true}
	for _, p := range globs {
		toks := parseGlob(p)
		g.toks = append(g.toks, toks)
		g.off = append(g.off, g.total)
		g.finals = append(g.finals, g.total+len(toks))
		g.total += len(toks) + 1
		for _, t := range toks {
			if t.kind == tokLit {
				lits[t.r] = true
			}
		}
	}
	g.words = (g.total + 63) / 64
	for r := range lits {
		g.alpha = append(g.alpha, r)
	}
	sort.Slice(g.alpha, func(i, j int) bool { return g.alpha[i] < g.alpha[j] })
	// One representative of every character that no glob mentions: not '/' and not a literal.
	other := rune(0x1)
	for lits[other] {
		other++
	}
	g.alpha = append(g.alpha, other)
	return g
}

type bitset []uint64

func (b bitset) set(i int)      { b[i>>6] |= 1 << (uint(i) & 63) }
func (b bitset) has(i int) bool { return b[i>>6]&(1<<(uint(i)&63)) != 0 }
func (b bitset) key() string {
	buf := make([]byte, 0, len(b)*8)
	for _, w := range b {
		buf = append(buf, byte(w), byte(w>>8), byte(w>>16), byte(w>>24), byte(w>>32), byte(w>>40), byte(w>>48), byte(w>>56))
	}
	return string(buf)
}

// closure adds the states reachable without consuming input: a state in front of * , ** or **/ may be skipped.
func (g *globSet) closure(b bitset) {
	for gi, toks := range g.toks {
		for i := 0; i < len(toks); i++ {
			if !b.has(g.off[gi] + i) {
				continue
			}
			switch toks[i].kind {
			case tokStar, tokAny:
				b.set(g.off[gi] + i + 1)
			case tokDirs:
				b.set(g.off[gi] + i + 2)
			}
		}
	}
}

func (g *globSet) initial() bitset {
	b := make(bitset, g.words)
	for gi := range g.toks {
		b.set(g.off[gi])
	}
	g.closure(b)
	return b
}

func (g *globSet) step(b bitset, c rune) bitset {
	n := make(bitset, g.words)
	for gi, toks := range g.toks {
		for i := 0; i < len(toks); i++ {
			if !b.has(g.off[gi] + i) {
				continue
			}
			t := toks[i]
			switch t.kind {
			case tokLit:
				if c == t.r {
					n.set(g.off[gi] + i + 1)
				}
			case tokQuest:
				if c != '/' {
					n.set(g.off[gi] + i + 1)
				}
			case tokStar:
				if c != '/' {
					n.set(g.off[gi] + i)
				}
			case tokAny:
				n.set(g.off[gi] + i)
			case tokDirs:
				n.set(g.off[gi] + i + 1)
				if c == '/' {
					n.set(g.off[gi] + i + 2)
				}
			case tokDirsIn:
				n.set(g.off[gi] + i)
				if c == '/' {
					n.set(g.off[gi] + i + 1)
				}
			}
		}
	}
	g.closure(n)
	return n
}

// existsPath decides whether some path is matched by every glob of pos and by no glob of neg. ok is false when the
// search exceeded maxGlobStates.
func existsPath(pos, neg []string) (found, ok bool) {
	if len(neg) > 0 {
		// A negated glob that shares no path with the positive globs cannot remove a solution; dropping it keeps
		// the determinised state space small.
		if found, ok := searchPath(pos, nil); !found || !ok {
			return found, ok
		}
		var kept []string
		seen := map[string]bool{}
		for _, n := range neg {
			if seen[n] {
				continue
			}
			seen[n] = true
			if f, ok := searchPath(append(append([]string{}, pos...), n), nil); f || !ok {
				kept = append(kept, n)
			}
		}
		neg = kept
	}
	return searchPath(pos, neg)
}

func searchPath(pos, neg []string) (found, ok bool) {
	g := newGlobSet(append(append([]string{}, pos...), neg...))
	alive := func(b bitset) bool { // every positive glob still has a live state
		for gi := 0; gi < len(pos); gi++ {
			live := false
			for i := g.off[gi]; i <= g.finals[gi] && !live; i++ {
				live = b.has(i)
			}
			if !live {
				return false
			}
		}
		return true
	}
	accepting := func(b bitset) bool {
		for gi := range g.toks {
			in := b.has(g.finals[gi])
			if gi < len(pos) && !in || gi >= len(pos) && in {
				return false
			}
		}
		return true
	}
	start := g.initial()
	seen := map[string]bool{start.key(): true}
	queue := []bitset{start}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if accepting(cur) {
			return true, true
		}
		for _, c := range g.alpha {
			nx := g.step(cur, c)
			if !alive(nx) {
				continue
			}
			k := nx.key()
			if seen[k] {
				continue
			}
			if len(seen) >= maxGlobStates {
				return false, false
			}
			seen[k] = true
			queue = append(queue, nx)
		}
	}
	return false, true
}

// globsIntersect reports whether some path matches both a and b and none of the denied globs.
func globsIntersect(a, b string, denied []string) (found, ok bool) {
	return existsPath([]string{a, b}, denied)
}

// globInsideUnion reports whether every path matched by g is matched by some glob of set.
func globInsideUnion(g string, set []string) (inside, ok bool) {
	found, ok := existsPath([]string{g}, set)
	return !found, ok
}

// isCatchAll reports whether the glob list holds `**`.
func isCatchAll(globs []string) bool {
	for _, g := range globs {
		if strings.TrimSpace(g) == "**" {
			return true
		}
	}
	return false
}
