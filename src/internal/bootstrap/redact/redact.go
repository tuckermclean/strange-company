// Package redact is the log redactor of the bootstrap engine (plan 50 section 10.2 rule 3, ADR-0101): a registered
// secret value is replaced by the marker «redacted:SC-CLASS» in every byte the engine writes.
package redact

import (
	"bytes"
	"encoding/json"
	"io"
	"sort"
	"strings"
	"sync"
)

type pattern struct {
	text   string
	marker string
}

// Redactor holds the registered secret values. It is safe for concurrent use.
type Redactor struct {
	mu       sync.Mutex
	classes  map[string]string // pattern text -> class
	index    map[byte][]pattern
	maxLen   int
	rebuilt  bool
	registry []string // pattern texts in registration order
}

// New returns an empty Redactor.
func New() *Redactor {
	return &Redactor{classes: map[string]string{}}
}

// Register adds a secret value of the given class (for example SC-OBJECT-STORAGE) before its first use. Besides the
// value itself, its JSON-escaped forms are redacted.
func (r *Redactor) Register(class, value string) {
	if value == "" {
		return
	}
	forms := []string{value}
	if b, err := json.Marshal(value); err == nil {
		forms = append(forms, string(b[1:len(b)-1]))
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(value); err == nil {
		t := strings.TrimSuffix(buf.String(), "\n")
		forms = append(forms, t[1:len(t)-1])
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, f := range forms {
		if f == "" {
			continue
		}
		if _, dup := r.classes[f]; dup {
			continue
		}
		r.classes[f] = class
		r.registry = append(r.registry, f)
		r.rebuilt = false
	}
}

// build prepares the first-byte index, longest pattern first. The caller holds r.mu.
func (r *Redactor) build() {
	if r.rebuilt {
		return
	}
	r.index = map[byte][]pattern{}
	r.maxLen = 0
	for _, t := range r.registry {
		r.index[t[0]] = append(r.index[t[0]], pattern{text: t, marker: "«redacted:" + r.classes[t] + "»"})
		if len(t) > r.maxLen {
			r.maxLen = len(t)
		}
	}
	for _, ps := range r.index {
		sort.SliceStable(ps, func(i, j int) bool { return len(ps[i].text) > len(ps[j].text) })
	}
	r.rebuilt = true
}

// scan replaces the registered values in s. When final is false the tail that could still be the start of a value
// is held back and returned as rest.
func (r *Redactor) scan(s string, final bool) (out, rest string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.build()
	if r.maxLen == 0 {
		return s, ""
	}
	limit := len(s)
	if !final {
		limit = len(s) - (r.maxLen - 1)
		if limit <= 0 {
			return "", s
		}
	}
	var sb strings.Builder
	i := 0
	for i < limit {
		matched := false
		for _, p := range r.index[s[i]] {
			if strings.HasPrefix(s[i:], p.text) {
				sb.WriteString(p.marker)
				i += len(p.text)
				matched = true
				break
			}
		}
		if !matched {
			sb.WriteByte(s[i])
			i++
		}
	}
	if i >= len(s) {
		return sb.String(), ""
	}
	return sb.String(), s[i:]
}

// String returns s with every registered value replaced by «redacted:<class>».
func (r *Redactor) String(s string) string {
	out, _ := r.scan(s, true)
	return out
}

// Bytes is String for byte slices.
func (r *Redactor) Bytes(b []byte) []byte {
	return []byte(r.String(string(b)))
}

type writer struct {
	r    *Redactor
	w    io.Writer
	mu   sync.Mutex
	hold string
}

// Writer returns a writer that redacts what is written through it, also when a secret is split across Write calls.
// Close flushes the held-back bytes and closes w when it is an io.Closer.
func (r *Redactor) Writer(w io.Writer) io.WriteCloser {
	return &writer{r: r, w: w}
}

func (w *writer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	out, rest := w.r.scan(w.hold+string(p), false)
	w.hold = rest
	if out != "" {
		if _, err := io.WriteString(w.w, out); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}

func (w *writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	out, _ := w.r.scan(w.hold, true)
	w.hold = ""
	var err error
	if out != "" {
		_, err = io.WriteString(w.w, out)
	}
	if c, ok := w.w.(io.Closer); ok {
		if cerr := c.Close(); err == nil {
			err = cerr
		}
	}
	return err
}
