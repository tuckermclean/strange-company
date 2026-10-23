// Package scrub removes secrets from text (plan 27 section 13.4): an exact-match set loaded at start plus a
// pattern set, each match replaced by [REDACTED:<kind>] and counted.
package scrub

import (
	"bytes"
	"encoding/json"
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
	{"anthropic-key", regexp.MustCompile(`(^|[^A-Za-z0-9]|\\[A-Za-z]|\\u[0-9A-Fa-f]{4})(sk-ant-[A-Za-z0-9_-]{20,})`)},
	{"openai-key", regexp.MustCompile(`(^|[^A-Za-z0-9]|\\[A-Za-z]|\\u[0-9A-Fa-f]{4})(sk-(?:proj-)?[A-Za-z0-9_-]{20,})`)},
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
// key) the whole block, its trimmed form and every base64 body line of at least 20 characters are added.
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
				// Base64 body lines only: PEM armor ("-----BEGIN ...") is not secret and would make a
				// different key's armor match before the PEM pattern.
				if l := strings.TrimSpace(line); len(l) >= 20 && !strings.HasPrefix(l, "-----") {
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

// String returns v with every secret replaced and counts the replacements. PEM private keys are matched first,
// so a key other than the loaded one is redacted whole; then the exact set; then the remaining patterns.
func (s *Scrubber) String(v string) string {
	if s == nil || v == "" {
		return v
	}
	v = s.applyPatterns(v, patterns[:pemPatterns])
	for _, e := range s.exact {
		if n := strings.Count(v, e); n > 0 {
			v = strings.ReplaceAll(v, e, "[REDACTED:secret]")
			s.count.Add(int64(n))
		}
	}
	return s.applyPatterns(v, patterns[pemPatterns:])
}

const pemPatterns = 2

func (s *Scrubber) applyPatterns(v string, ps []pattern) string {
	for _, p := range ps {
		p := p
		v = p.re.ReplaceAllStringFunc(v, func(m string) string {
			s.count.Add(1)
			if p.re.NumSubexp() == 2 { // the first group is the left boundary and is kept
				sub := p.re.FindStringSubmatch(m)
				return sub[1] + "[REDACTED:" + p.kind + "]"
			}
			return "[REDACTED:" + p.kind + "]"
		})
	}
	return v
}

// Value scrubs every string inside a value and returns the result. Strings, []any and map[string]any are
// scrubbed in place; any other type (typed slices and maps, structs, pointers) is scrubbed through a JSON
// round-trip, so its result is the generic JSON form. Map keys are left alone.
func (s *Scrubber) Value(v any) any {
	switch t := v.(type) {
	case nil:
		return nil
	case string:
		return s.String(t)
	case bool, float64, int, int64, json.Number:
		return v
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
	b, err := json.Marshal(v)
	if err != nil {
		return "[REDACTED:unserializable]"
	}
	var g any
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if err := dec.Decode(&g); err != nil {
		return "[REDACTED:unserializable]"
	}
	return s.Value(g)
}

// Redactions is the number of replacements made so far.
func (s *Scrubber) Redactions() int {
	if s == nil {
		return 0
	}
	return int(s.count.Load())
}
