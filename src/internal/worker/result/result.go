// Package result assembles, size-fits and validates the worker result (plan 27 section 14.12).
package result

import (
	"bytes"
	"encoding/json"
	"errors"
	"unicode/utf8"
)

// TruncMarker ends every text field that Fit shortened.
const TruncMarker = "[truncated by sc-worker]"

// ErrTooLarge is returned when no fitting step brings the result under the limit; the caller replaces it by a
// minimal failed/internal result (detail result_too_large).
var ErrTooLarge = errors.New("result_too_large")

// reportMarkdownMax is maxLength of ResearchReport.report_markdown in worker-result.schema.yaml; fitting
// respects the schema limit even when max_result_bytes would allow more.
const reportMarkdownMax = 65536

// Canonical returns the canonical one-line JSON of v (sorted keys, no HTML escaping, no trailing newline).
func Canonical(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// Fit returns the canonical JSON of doc, shortened in the deterministic order of plan 27 section 14.12 step 3 so
// that it is at most maxBytes long: (a) drop validation[*].output_tail from the last command backwards, (b)
// truncate harness_report.permission_denials to 20, (c) truncate long role_output text (report_markdown, then
// finding texts), (d) truncate summary to 1000 characters. doc is not modified.
func Fit(doc map[string]any, maxBytes int) ([]byte, error) {
	d, err := deepCopy(doc)
	if err != nil {
		return nil, err
	}
	if ro, ok := d["role_output"].(map[string]any); ok {
		if md, ok := ro["report_markdown"].(string); ok && utf8.RuneCountInString(md) > reportMarkdownMax {
			ro["report_markdown"] = cutRunes(md, reportMarkdownMax)
		}
	}
	fits := func() ([]byte, bool) {
		b, err := Canonical(d)
		return b, err == nil && len(b) <= maxBytes
	}
	if b, ok := fits(); ok {
		return b, nil
	}
	// (a)
	if vs, ok := d["validation"].([]any); ok {
		for i := len(vs) - 1; i >= 0; i-- {
			if m, ok := vs[i].(map[string]any); ok {
				if _, has := m["output_tail"]; has {
					delete(m, "output_tail")
					if b, ok := fits(); ok {
						return b, nil
					}
				}
			}
		}
	}
	// (b)
	if hr, ok := d["harness_report"].(map[string]any); ok {
		if pd, ok := hr["permission_denials"].([]any); ok && len(pd) > 20 {
			hr["permission_denials"] = pd[:20]
			if b, ok := fits(); ok {
				return b, nil
			}
		}
	}
	// (c)
	if ro, ok := d["role_output"].(map[string]any); ok {
		if md, ok := ro["report_markdown"].(string); ok {
			ro["report_markdown"] = fitText(md, func(s string) bool {
				ro["report_markdown"] = s
				_, ok := fits()
				return ok
			})
			if b, ok := fits(); ok {
				return b, nil
			}
		}
		if fs, ok := ro["findings"].([]any); ok {
			for i := len(fs) - 1; i >= 0; i-- {
				f, ok := fs[i].(map[string]any)
				if !ok {
					continue
				}
				if t, ok := f["text"].(string); ok {
					f["text"] = fitText(t, func(s string) bool {
						f["text"] = s
						_, ok := fits()
						return ok
					})
					if b, ok := fits(); ok {
						return b, nil
					}
				}
			}
		}
	}
	// (d)
	if s, ok := d["summary"].(string); ok && utf8.RuneCountInString(s) > 1000 {
		d["summary"] = cutRunes(s, 1000)
		if b, ok := fits(); ok {
			return b, nil
		}
	}
	return nil, ErrTooLarge
}

// fitText returns the longest prefix of s (in runes) followed by the truncation marker for which ok reports
// that the whole document fits; it returns the marker alone if even that does not help. ok is called with each
// candidate and leaves the last tried value set; the caller re-checks.
func fitText(s string, ok func(string) bool) string {
	runes := []rune(s)
	lo, hi := 0, len(runes) // invariant: prefix of lo runes fits (or lo==0), hi does not
	best := TruncMarker
	cand := func(n int) string {
		if n == 0 {
			return TruncMarker
		}
		return string(runes[:n]) + "\n" + TruncMarker
	}
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if ok(cand(mid)) {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	if ok(cand(lo)) {
		best = cand(lo)
	}
	ok(best)
	return best
}

// cutRunes keeps the first runes of s so that the result, including the marker line, has at most n runes.
func cutRunes(s string, n int) string {
	suffix := "\n" + TruncMarker
	keep := n - utf8.RuneCountInString(suffix)
	r := []rune(s)
	if keep < 0 || keep > len(r) {
		keep = max(0, min(keep, len(r)))
	}
	return string(r[:keep]) + suffix
}

func deepCopy(m map[string]any) (map[string]any, error) {
	b, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out, nil
}
