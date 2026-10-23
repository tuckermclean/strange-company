// Package scrub removes secrets from text (plan 27 section 13.4): an exact-match set loaded at start plus a
// pattern set, each match replaced by [REDACTED:<kind>] and counted.
package scrub

import (
	"regexp"
	"sort"
	"strings"
	"sync/atomic"
)

type pattern struct {
	kind string
	re   *regexp.Regexp
}

// The PEM pattern also matches the JSON-escaped form (literal backslash-n between the lines).
var patterns = []pattern{
	{"pem-private-key", regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----[\s\S]*?-----END [A-Z0-9 ]*PRIVATE KEY-----`)},
	{"pem-private-key", regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----(?:[^"]|\\n)*?-----END [A-Z0-9 ]*PRIVATE KEY-----`)},
	{"anthropic-key", regexp.MustCompile(`sk-ant-[A-Za-z0-9_-]{20,}`)},
	{"openai-key", regexp.MustCompile(`sk-(?:proj-)?[A-Za-z0-9_-]{20,}`)},
	{"jwt", regexp.MustCompile(`eyJ[A-Za-z0-9_-]{8,}\.eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`)},
	{"github-token", regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{36,}`)},
	{"github-pat", regexp.MustCompile(`github_pat_[A-Za-z0-9_]{40,}`)},
	{"aws-access-key", regexp.MustCompile(`AKIA[0-9A-Z]{16}`)},
	{"slack-token", regexp.MustCompile(`xox[abprs]-[A-Za-z0-9-]{10,}`)},
}

const minExact = 8 // shorter exact values are ignored: redacting them would destroy ordinary text

// Scrubber redacts known secret values and secret-shaped strings. It is safe for concurrent use.
type Scrubber struct {
	exact []string
	count atomic.Int64
}

// New returns a Scrubber with the given secret contents as the exact-match set. For a multi-line secret (a PEM
// key) the whole block, its trimmed form and every line of at least 20 characters are added.
func New(secrets ...string) *Scrubber {
	set := map[string]bool{}
	add := func(v string) {
		if len(v) >= minExact {
			set[v] = true
		}
	}
	for _, s := range secrets {
		t := strings.TrimSpace(s)
		add(t)
		if strings.Contains(t, "\n") {
			for _, line := range strings.Split(t, "\n") {
				if l := strings.TrimSpace(line); len(l) >= 20 {
					add(l)
				}
			}
		}
	}
	sc := &Scrubber{}
	for v := range set {
		sc.exact = append(sc.exact, v)
	}
	// Longest first, so a whole block is replaced before its lines.
	sort.Slice(sc.exact, func(i, j int) bool {
		if len(sc.exact[i]) != len(sc.exact[j]) {
			return len(sc.exact[i]) > len(sc.exact[j])
		}
		return sc.exact[i] < sc.exact[j]
	})
	return sc
}

// AddSecret extends the exact-match set (for example with a value placed into the harness environment).
func (s *Scrubber) AddSecret(v string) *Scrubber {
	n := New(append(append([]string(nil), s.exact...), v)...)
	n.count.Store(s.count.Load())
	return n
}

// String returns v with every secret replaced and counts the replacements.
func (s *Scrubber) String(v string) string {
	if s == nil || v == "" {
		return v
	}
	for _, e := range s.exact {
		if n := strings.Count(v, e); n > 0 {
			v = strings.ReplaceAll(v, e, "[REDACTED:secret]")
			s.count.Add(int64(n))
		}
	}
	for _, p := range patterns {
		v = p.re.ReplaceAllStringFunc(v, func(string) string {
			s.count.Add(1)
			return "[REDACTED:" + p.kind + "]"
		})
	}
	return v
}

// Value scrubs every string inside a JSON-compatible value (maps, slices, strings) in place where possible and
// returns the result. Map keys are left alone.
func (s *Scrubber) Value(v any) any {
	switch t := v.(type) {
	case string:
		return s.String(t)
	case []any:
		for i := range t {
			t[i] = s.Value(t[i])
		}
		return t
	case map[string]any:
		for k := range t {
			t[k] = s.Value(t[k])
		}
		return t
	}
	return v
}

// Redactions is the number of replacements made so far.
func (s *Scrubber) Redactions() int {
	if s == nil {
		return 0
	}
	return int(s.count.Load())
}
