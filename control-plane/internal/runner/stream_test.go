package runner

import (
	"bytes"
	"errors"
	"testing"
)

func TestExtractStream_ExtractsFramedRegionDiscardingSurroundingNoise(t *testing.T) {
	podLog := []byte(
		"Cloning into 'workspace'...\n" +
			"remote: Enumerating objects: 42, done.\n" +
			StreamBegin + "\n" +
			`{"type":"system","subtype":"init"}` + "\n" +
			`{"type":"result","is_error":false}` + "\n" +
			StreamEnd + "\n" +
			"On branch agent/card-123-widget\n" +
			"nothing to commit, working tree clean\n",
	)

	got, err := ExtractStream(podLog)
	if err != nil {
		t.Fatalf("ExtractStream() error = %v, want nil: content before/after markers must not prevent extraction", err)
	}

	want := `{"type":"system","subtype":"init"}` + "\n" + `{"type":"result","is_error":false}`
	if string(got) != want {
		t.Fatalf("ExtractStream() = %q, want %q: only bytes between the markers (with marker lines removed) should be returned, and clone/commit noise outside them must be discarded", got, want)
	}
}

func TestExtractStream_TrimsLeadingAndTrailingBlankLinesInsideMarkers(t *testing.T) {
	podLog := []byte(
		StreamBegin + "\n" +
			"\n" +
			"   \n" +
			`{"type":"result","is_error":false}` + "\n" +
			"\n" +
			StreamEnd + "\n",
	)

	got, err := ExtractStream(podLog)
	if err != nil {
		t.Fatalf("ExtractStream() error = %v, want nil", err)
	}

	want := `{"type":"result","is_error":false}`
	if string(got) != want {
		t.Fatalf("ExtractStream() = %q, want %q: leading/trailing blank lines between the markers must be trimmed", got, want)
	}
}

func TestExtractStream_BeginWithoutEndIsTruncatedNotPartial(t *testing.T) {
	podLog := []byte(
		StreamBegin + "\n" +
			`{"type":"system","subtype":"init"}` + "\n" +
			`{"type":"assistant","message":"still working when the Job was killed"}` + "\n",
	)

	got, err := ExtractStream(podLog)
	if !errors.Is(err, ErrStreamTruncated) {
		t.Fatalf("ExtractStream() error = %v, want ErrStreamTruncated: spec 12.1 says a killed Job must not be scored as a failed implementation attempt, so a missing end marker must never yield a partial stream", err)
	}
	if got != nil {
		t.Fatalf("ExtractStream() returned %d bytes alongside ErrStreamTruncated, want nil: spec 12.1 says a killed Job must not be scored as a failed implementation attempt, and returning partial bytes here would let a truncated stream be parsed as if it were complete", len(got))
	}
}

func TestExtractStream_EndBeforeBeginWithNoLaterEndIsTruncated(t *testing.T) {
	podLog := []byte(
		StreamEnd + "\n" +
			"some stray output that happens to contain an end marker above\n" +
			StreamBegin + "\n" +
			`{"type":"result","is_error":false}` + "\n",
	)

	got, err := ExtractStream(podLog)
	if !errors.Is(err, ErrStreamTruncated) {
		t.Fatalf("ExtractStream() error = %v, want ErrStreamTruncated: an end marker that only appears before the first begin does not close it, so the region the begin opened was never closed", err)
	}
	if got != nil {
		t.Fatalf("ExtractStream() returned non-nil bytes with ErrStreamTruncated, want nil")
	}
}

func TestExtractStream_NeitherMarkerPresentIsMissing(t *testing.T) {
	podLog := []byte(
		"Cloning into 'workspace'...\n" +
			"remote: Enumerating objects: 42, done.\n" +
			"On branch agent/card-123-widget\n",
	)

	got, err := ExtractStream(podLog)
	if !errors.Is(err, ErrStreamMissing) {
		t.Fatalf("ExtractStream() error = %v, want ErrStreamMissing: a log with no markers at all was never framed as a harness run", err)
	}
	if got != nil {
		t.Fatalf("ExtractStream() returned non-nil bytes with ErrStreamMissing, want nil")
	}
}

func TestExtractStream_LoneEndMarkerWithNoBeginIsMissing(t *testing.T) {
	podLog := []byte(
		"some diagnostic output\n" +
			StreamEnd + "\n",
	)

	got, err := ExtractStream(podLog)
	if !errors.Is(err, ErrStreamMissing) {
		t.Fatalf("ExtractStream() error = %v, want ErrStreamMissing: an end marker with no begin marker anywhere in the log closes nothing, so there is no framed region at all", err)
	}
	if got != nil {
		t.Fatalf("ExtractStream() returned non-nil bytes with ErrStreamMissing, want nil")
	}
}

func TestExtractStream_MarkerTextInsideJSONDoesNotTerminateTheStream(t *testing.T) {
	podLog := []byte(
		StreamBegin + "\n" +
			`{"type":"assistant","text":"printing the marker ::STRANGE-COMPANY-STREAM-END:: as an example, not as a real terminator"}` + "\n" +
			`{"type":"result","is_error":false}` + "\n" +
			StreamEnd + "\n",
	)

	got, err := ExtractStream(podLog)
	if err != nil {
		t.Fatalf("ExtractStream() error = %v, want nil: a JSON string field containing the end-marker text must not be mistaken for the real marker", err)
	}

	want := `{"type":"assistant","text":"printing the marker ::STRANGE-COMPANY-STREAM-END:: as an example, not as a real terminator"}` + "\n" +
		`{"type":"result","is_error":false}`
	if string(got) != want {
		t.Fatalf("ExtractStream() = %q, want %q: markers must be matched as whole lines, never as substrings, so the full stream (including the line whose payload merely mentions the end marker) must come back intact", got, want)
	}
}

func TestExtractStream_MarkerSubstringOnAnOrdinaryLineDoesNotCount(t *testing.T) {
	podLog := []byte(
		StreamBegin + "\n" +
			"noise" + StreamEnd + "noise\n" +
			`{"type":"result","is_error":false}` + "\n" +
			StreamEnd + "\n",
	)

	got, err := ExtractStream(podLog)
	if err != nil {
		t.Fatalf("ExtractStream() error = %v, want nil: a line where the marker text is only a substring must not be matched as the marker", err)
	}

	want := "noise" + StreamEnd + "noise\n" + `{"type":"result","is_error":false}`
	if string(got) != want {
		t.Fatalf("ExtractStream() = %q, want %q: markers must be matched as whole trimmed lines, never as substrings of a longer line", got, want)
	}
}

func TestExtractStream_TolerantOfCRLFLineEndings(t *testing.T) {
	podLog := []byte(
		"Cloning...\r\n" +
			StreamBegin + "\r\n" +
			`{"type":"result","is_error":false}` + "\r\n" +
			StreamEnd + "\r\n" +
			"done\r\n",
	)

	got, err := ExtractStream(podLog)
	if err != nil {
		t.Fatalf("ExtractStream() error = %v, want nil: CRLF line endings around the markers must not prevent extraction", err)
	}

	want := `{"type":"result","is_error":false}`
	if string(got) != want {
		t.Fatalf("ExtractStream() = %q, want %q: CRLF line endings must be tolerated both for marker matching and for the extracted content", got, want)
	}
}

func TestExtractStream_EmptyRegionBetweenMarkersIsValid(t *testing.T) {
	podLog := []byte(StreamBegin + "\n" + StreamEnd + "\n")

	got, err := ExtractStream(podLog)
	if err != nil {
		t.Fatalf("ExtractStream() error = %v, want nil: an empty region between the markers is valid input to this function, not an error condition it should raise", err)
	}
	if len(got) != 0 {
		t.Fatalf("ExtractStream() = %q, want empty: an empty stream is the adapter's problem to report, not this function's", got)
	}
}

func TestExtractStream_MultipleBeginsUsesFirstBeginAndLastEnd(t *testing.T) {
	podLog := []byte(
		StreamBegin + "\n" +
			`{"type":"result","is_error":false,"attempt":1}` + "\n" +
			StreamEnd + "\n" +
			"retrying the harness after a transient failure\n" +
			StreamBegin + "\n" +
			`{"type":"result","is_error":false,"attempt":2}` + "\n" +
			StreamEnd + "\n",
	)

	got, err := ExtractStream(podLog)
	if err != nil {
		t.Fatalf("ExtractStream() error = %v, want nil", err)
	}

	want := `{"type":"result","is_error":false,"attempt":1}` + "\n" +
		StreamEnd + "\n" +
		"retrying the harness after a transient failure\n" +
		StreamBegin + "\n" +
		`{"type":"result","is_error":false,"attempt":2}`
	if string(got) != want {
		t.Fatalf("ExtractStream() = %q, want %q: with more than one begin marker, extraction must span from the FIRST begin to the LAST end, because taking the widest span is the only choice that cannot silently drop events from a retrying entrypoint", got, want)
	}
}

func TestExtractStream_NeverPanics(t *testing.T) {
	inputs := map[string][]byte{
		"nil input":                   nil,
		"empty input":                 {},
		"marker-only, no content":     []byte(StreamBegin + "\n" + StreamEnd),
		"begin marker only":           []byte(StreamBegin),
		"end marker only":             []byte(StreamEnd),
		"end before begin, no later end": []byte(StreamEnd + "\n" + StreamBegin),
		"just whitespace":             []byte("   \n\t\n"),
		"just newlines":               []byte("\n\n\n"),
	}

	for name, input := range inputs {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("ExtractStream(%q) panicked: %v; it must never panic on any input, including malformed or missing markers", name, r)
				}
			}()
			_, _ = ExtractStream(input)
		})
	}
}

func TestExtractStream_NeverReturnsNilResultWithNilError(t *testing.T) {
	// A degenerate but successful extraction (both markers present,
	// nothing between them) must return a non-nil, zero-length byte slice
	// paired with a nil error, never a nil slice masquerading as "no
	// error, no data" ambiguity with a failed call.
	podLog := []byte(StreamBegin + "\n" + StreamEnd)

	got, err := ExtractStream(podLog)
	if err != nil {
		t.Fatalf("ExtractStream() error = %v, want nil", err)
	}
	if got == nil {
		t.Fatalf("ExtractStream() = nil, want a non-nil (possibly zero-length) byte slice when err is nil")
	}
}

func TestExtractStream_ErrorsAreDistinctSentinels(t *testing.T) {
	if errors.Is(ErrStreamMissing, ErrStreamTruncated) || errors.Is(ErrStreamTruncated, ErrStreamMissing) {
		t.Fatalf("ErrStreamMissing and ErrStreamTruncated must be distinct sentinel errors: infra-error classification (spec 12.1) depends on telling a never-started stream apart from a killed-mid-run one")
	}
}

func TestExtractStream_MarkerConstantsMatchThePlan(t *testing.T) {
	// docs/superpowers/plans/2026-08-26-control-plane-m3.md ("M3c") and
	// the runner entrypoint on the other side of this contract both
	// depend on these exact strings.
	if StreamBegin != "::STRANGE-COMPANY-STREAM-BEGIN::" {
		t.Fatalf("StreamBegin = %q, want %q", StreamBegin, "::STRANGE-COMPANY-STREAM-BEGIN::")
	}
	if StreamEnd != "::STRANGE-COMPANY-STREAM-END::" {
		t.Fatalf("StreamEnd = %q, want %q", StreamEnd, "::STRANGE-COMPANY-STREAM-END::")
	}
}

func TestExtractStream_ContentIsReturnedVerbatimBetweenMarkers(t *testing.T) {
	inner := `{"type":"result","is_error":false,"result":"done","session_id":"abc-123"}`
	podLog := []byte(StreamBegin + "\n" + inner + "\n" + StreamEnd)

	got, err := ExtractStream(podLog)
	if err != nil {
		t.Fatalf("ExtractStream() error = %v, want nil", err)
	}
	if !bytes.Equal(got, []byte(inner)) {
		t.Fatalf("ExtractStream() = %q, want %q: the harness JSONL payload must pass through unmodified", got, inner)
	}
}
