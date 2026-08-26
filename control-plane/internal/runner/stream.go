package runner

import (
	"bytes"
	"errors"
)

// StreamBegin and StreamEnd are the marker lines the runner entrypoint
// writes immediately before and after the coding harness's JSONL stream
// (docs/superpowers/plans/2026-08-26-control-plane-m3.md, "M3c"; spec
// §12.1). Kubernetes merges a container's stdout and stderr into a single
// log stream, and the adapters in this package deliberately error on any
// malformed line so a truncated stream is never mistaken for success.
// Without framing, a single line of `git clone` progress output would
// corrupt the run result. ExtractStream recovers the framed region so
// only the harness's own JSONL ever reaches an adapter's Parse, while the
// rest of the pod log — clone, checkout, commit, push, diagnostics — is
// discarded here but stays visible to a human reading `kubectl logs`.
const (
	StreamBegin = "::STRANGE-COMPANY-STREAM-BEGIN::"
	StreamEnd   = "::STRANGE-COMPANY-STREAM-END::"
)

// ErrStreamMissing means the begin marker does not appear anywhere in the
// log. The pod log was never framed as a harness run at all (or the
// entrypoint crashed before it ever started the harness), so there is no
// harness stream to extract. A lone end marker with no begin marker
// anywhere in the log is treated the same way: without a begin there is
// no region to close, framed or otherwise.
var ErrStreamMissing = errors.New("no harness stream markers in log")

// ErrStreamTruncated means a begin marker was found but no end marker
// closes it. Spec §12.1 is explicit that this is the signature of a Job
// killed mid-run (wall-clock timeout, OOM, node eviction, ...) — an
// infrastructure failure, not a code failure. ExtractStream deliberately
// refuses to return the partial bytes it did see: doing so would let a
// killed Job be scored as a failed implementation attempt and silently
// burn a rung of the Haiku -> Sonnet -> Opus escalation ladder (spec
// §12.3) for work the harness never got a chance to finish.
var ErrStreamTruncated = errors.New("harness stream is missing its end marker")

// ExtractStream finds the harness JSONL stream framed by StreamBegin and
// StreamEnd in a raw Kubernetes pod log and returns only the bytes
// between them, with the marker lines themselves removed and any
// leading/trailing blank lines trimmed. Content before the first begin
// marker and after the chosen end marker (clone/checkout/commit/push
// output, shell diagnostics, ...) is discarded.
//
// Markers are matched as whole lines, after trimming surrounding
// whitespace — which also makes matching tolerant of CRLF line endings,
// since the trailing \r trims away along with everything else. A harness
// event whose JSON payload happens to contain the marker text inside a
// string field does not start or end the stream: that text is never alone
// on its own line once the surrounding JSON punctuation is accounted for,
// so it can never equal the marker exactly.
//
// If the begin marker appears more than once, ExtractStream uses the
// FIRST begin and the LAST end. A retrying entrypoint could plausibly
// emit the framing more than once (for example, a supervisor re-running
// the harness after a transient failure); spanning from the first begin
// to the last end is the only choice that can never silently drop events
// belonging to an earlier or later attempt — any narrower choice risks
// throwing away a real portion of the run.
//
// An empty region between the markers is valid: it returns empty bytes
// and a nil error. Whether an empty stream is itself a problem is for the
// adapter parsing it to decide, not this function.
//
// ExtractStream never panics, including on nil or empty input.
func ExtractStream(podLog []byte) ([]byte, error) {
	lines := bytes.Split(podLog, []byte("\n"))

	begin := []byte(StreamBegin)
	end := []byte(StreamEnd)

	firstBegin := -1
	lastEnd := -1
	for i, line := range lines {
		trimmed := bytes.TrimSpace(line)
		switch {
		case bytes.Equal(trimmed, begin):
			if firstBegin == -1 {
				firstBegin = i
			}
		case bytes.Equal(trimmed, end):
			lastEnd = i
		}
	}

	if firstBegin == -1 {
		// No begin marker anywhere. This also covers "neither marker
		// present" (lastEnd is then necessarily -1 too, since a log with
		// no begin was never framed): with no begin there is no framed
		// region to extract, regardless of any end marker's presence.
		return nil, ErrStreamMissing
	}
	if lastEnd == -1 || lastEnd <= firstBegin {
		// Either no end marker exists anywhere in the log
		// (begin-without-end), or every end marker found sits at or
		// before the first begin (end-before-begin with no later end).
		// Both mean the region the first begin opened was never closed.
		return nil, ErrStreamTruncated
	}

	inner := lines[firstBegin+1 : lastEnd]

	lo := 0
	for lo < len(inner) && isBlankLine(inner[lo]) {
		lo++
	}
	hi := len(inner)
	for hi > lo && isBlankLine(inner[hi-1]) {
		hi--
	}
	inner = inner[lo:hi]

	if len(inner) == 0 {
		return []byte{}, nil
	}

	normalized := make([][]byte, len(inner))
	for i, line := range inner {
		// Strip a trailing \r left over from a CRLF-terminated line; the
		// rest of the line is otherwise untouched so the harness's JSON
		// payload passes through verbatim.
		normalized[i] = bytes.TrimRight(line, "\r")
	}

	return bytes.Join(normalized, []byte("\n")), nil
}

// isBlankLine reports whether line is empty or contains only whitespace
// (including a lone CRLF remnant).
func isBlankLine(line []byte) bool {
	return len(bytes.TrimSpace(line)) == 0
}
