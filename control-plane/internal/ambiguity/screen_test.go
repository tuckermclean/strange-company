package ambiguity

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/tuckermclean/strange-company/control-plane/internal/spec"
)

// fakeCompleter is a test double for Completer. No network, no
// credentials: CI can and must run every test in this file with neither
// (spec §26).
type fakeCompleter struct {
	completion *Completion
	err        error

	called  bool
	lastReq CompleteRequest
}

func (f *fakeCompleter) Complete(ctx context.Context, req CompleteRequest) (*Completion, error) {
	f.called = true
	f.lastReq = req
	if f.err != nil {
		return nil, f.err
	}
	return f.completion, nil
}

// testDocument builds a realistic, non-empty *spec.Document to screen.
func testDocument(t *testing.T) *spec.Document {
	t.Helper()
	raw := []byte(`# Specification: card-1

## Context

Some context explaining why this card exists.

## Task

Do the thing.

## Acceptance criteria

- AC1: the thing happens — verified by: go test ./...
`)
	doc, _ := spec.Parse("card-1", raw)
	return doc
}

func TestScoreZeroThroughThreeAreParsed(t *testing.T) {
	cases := []struct {
		name string
		json string
		want Score
	}{
		{"zero", `{"score":0,"rationale":"mechanical change","findings":[]}`, ScoreMechanical},
		{"one", `{"score":1,"rationale":"minor judgment call","findings":[]}`, ScoreMinorInterpretation},
		{"two", `{"score":2,"rationale":"material ambiguity","findings":[]}`, ScoreMaterialAmbiguity},
		{"three", `{"score":3,"rationale":"fundamental ambiguity","findings":[]}`, ScoreFundamentalAmbiguity},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeCompleter{completion: &Completion{Text: tc.json, Model: "test-model"}}
			s := NewScreener(fake, "test-model")

			report, err := s.Screen(context.Background(), testDocument(t))
			if err != nil {
				t.Fatalf("Screen returned error %v for a well-formed score %d; a well-formed screen must succeed", err, tc.want)
			}
			if report.Score != tc.want {
				t.Fatalf("Score = %d, want %d", report.Score, tc.want)
			}
		})
	}
}

func TestScoreAsNumericStringIsAccepted(t *testing.T) {
	fake := &fakeCompleter{completion: &Completion{Text: `{"score":"2","rationale":"models sometimes stringify","findings":[]}`}}
	s := NewScreener(fake, "test-model")

	report, err := s.Screen(context.Background(), testDocument(t))
	if err != nil {
		t.Fatalf("Screen returned error %v for a numeric-string score; models return scores both ways and both must be tolerated", err)
	}
	if report.Score != ScoreMaterialAmbiguity {
		t.Fatalf("Score = %d, want %d", report.Score, ScoreMaterialAmbiguity)
	}
}

func TestJSONWrappedInCodeFencesIsExtracted(t *testing.T) {
	text := "```json\n{\"score\":1,\"rationale\":\"wrapped in a fence\",\"findings\":[]}\n```"
	fake := &fakeCompleter{completion: &Completion{Text: text}}
	s := NewScreener(fake, "test-model")

	report, err := s.Screen(context.Background(), testDocument(t))
	if err != nil {
		t.Fatalf("Screen returned error %v for JSON wrapped in code fences; that is a common, tolerable model habit", err)
	}
	if report.Score != ScoreMinorInterpretation {
		t.Fatalf("Score = %d, want %d", report.Score, ScoreMinorInterpretation)
	}
}

func TestJSONSurroundedByProseIsExtracted(t *testing.T) {
	text := "Sure! Here is my assessment:\n" +
		`{"score":3,"rationale":"the goal itself is unclear","findings":[{"section":"Task","concern":"no stated outcome"}]}` +
		"\nLet me know if you'd like more detail."
	fake := &fakeCompleter{completion: &Completion{Text: text}}
	s := NewScreener(fake, "test-model")

	report, err := s.Screen(context.Background(), testDocument(t))
	if err != nil {
		t.Fatalf("Screen returned error %v for JSON surrounded by prose; that is a common, tolerable model habit", err)
	}
	if report.Score != ScoreFundamentalAmbiguity {
		t.Fatalf("Score = %d, want %d", report.Score, ScoreFundamentalAmbiguity)
	}
	if len(report.Findings) != 1 || report.Findings[0].Section != "Task" {
		t.Fatalf("Findings = %+v, want one finding for section Task", report.Findings)
	}
}

func TestModelErrorFailsClosedRatherThanScoringZero(t *testing.T) {
	fake := &fakeCompleter{err: fmt.Errorf("connection reset")}
	s := NewScreener(fake, "test-model")

	report, err := s.Screen(context.Background(), testDocument(t))
	if err == nil {
		t.Fatal("Screen returned no error for a failed model call; a failed screen must not read as 'nothing ambiguous' - that promotes exactly the cards that most need a human")
	}
	if !errors.Is(err, ErrScreenFailed) {
		t.Fatalf("error = %v, want it to wrap ErrScreenFailed", err)
	}
	if report != nil {
		t.Fatalf("report = %+v, want nil report on a failed model call - a failed screen must not read as 'nothing ambiguous'", report)
	}
}

func TestUnparseableOutputFailsClosed(t *testing.T) {
	fake := &fakeCompleter{completion: &Completion{Text: "I refuse to answer in JSON today."}}
	s := NewScreener(fake, "test-model")

	report, err := s.Screen(context.Background(), testDocument(t))
	if err == nil {
		t.Fatal("Screen returned no error for unparseable output; a failed screen must not read as 'nothing ambiguous' - that promotes exactly the cards that most need a human")
	}
	if !errors.Is(err, ErrScreenFailed) {
		t.Fatalf("error = %v, want it to wrap ErrScreenFailed", err)
	}
	if report != nil {
		t.Fatalf("report = %+v, want nil report on unparseable output", report)
	}
}

func TestMissingScoreFailsClosed(t *testing.T) {
	fake := &fakeCompleter{completion: &Completion{Text: `{"rationale":"no score at all","findings":[]}`}}
	s := NewScreener(fake, "test-model")

	report, err := s.Screen(context.Background(), testDocument(t))
	if err == nil {
		t.Fatal("Screen returned no error for JSON with no score field; a missing score must fail closed, not default to 0 - that promotes exactly the cards that most need a human")
	}
	if !errors.Is(err, ErrInvalidScore) {
		t.Fatalf("error = %v, want it to wrap ErrInvalidScore", err)
	}
	if report != nil {
		t.Fatalf("report = %+v, want nil report on a missing score", report)
	}
}

func TestOutOfRangeScoreIsRejected(t *testing.T) {
	for _, n := range []int{-1, 4, 99} {
		t.Run(fmt.Sprintf("score_%d", n), func(t *testing.T) {
			text := fmt.Sprintf(`{"score":%d,"rationale":"out of range","findings":[]}`, n)
			fake := &fakeCompleter{completion: &Completion{Text: text}}
			s := NewScreener(fake, "test-model")

			report, err := s.Screen(context.Background(), testDocument(t))
			if err == nil {
				t.Fatalf("Screen returned no error for out-of-range score %d; a failed screen must not read as 'nothing ambiguous' - that promotes exactly the cards that most need a human", n)
			}
			if !errors.Is(err, ErrInvalidScore) {
				t.Fatalf("error = %v, want it to wrap ErrInvalidScore", err)
			}
			if report != nil {
				t.Fatalf("report = %+v, want nil report for out-of-range score %d", report, n)
			}
		})
	}
}

func TestScreenNeverMutatesTheDocument(t *testing.T) {
	doc := testDocument(t)

	// Snapshot everything before Screen runs.
	wantRaw := append([]byte(nil), doc.Raw...)
	wantSections := make(map[string]string, len(doc.Sections))
	for k, v := range doc.Sections {
		wantSections[k] = v
	}
	wantCriteria := append([]spec.Criterion(nil), doc.Criteria...)
	wantCardID := doc.CardID

	fake := &fakeCompleter{completion: &Completion{Text: `{"score":2,"rationale":"fine","findings":[]}`}}
	s := NewScreener(fake, "test-model")

	if _, err := s.Screen(context.Background(), doc); err != nil {
		t.Fatalf("Screen returned unexpected error: %v", err)
	}

	if !bytes.Equal(doc.Raw, wantRaw) {
		t.Fatalf("doc.Raw changed after Screen: got %q, want %q - the model must never author back into the specification", doc.Raw, wantRaw)
	}
	if !reflect.DeepEqual(doc.Sections, wantSections) {
		t.Fatalf("doc.Sections changed after Screen: got %+v, want %+v - the model must never author back into the specification", doc.Sections, wantSections)
	}
	if !reflect.DeepEqual(doc.Criteria, wantCriteria) {
		t.Fatalf("doc.Criteria changed after Screen: got %+v, want %+v - the model must never author back into the specification", doc.Criteria, wantCriteria)
	}
	if doc.CardID != wantCardID {
		t.Fatalf("doc.CardID changed after Screen: got %q, want %q", doc.CardID, wantCardID)
	}
}

func TestModelRecommendationIsIgnored(t *testing.T) {
	text := `{"score":3,"rationale":"fundamentally unclear","findings":[],"recommendation":"proceed","approved":true}`
	fake := &fakeCompleter{completion: &Completion{Text: text}}
	s := NewScreener(fake, "test-model")

	report, err := s.Screen(context.Background(), testDocument(t))
	if err != nil {
		t.Fatalf("Screen returned unexpected error: %v", err)
	}
	if report.Score != ScoreFundamentalAmbiguity {
		t.Fatalf("Score = %d, want %d", report.Score, ScoreFundamentalAmbiguity)
	}
	if !report.RequiresHuman() {
		t.Fatal(`RequiresHuman() = false for a score-3 report despite the model's "approved":true and "recommendation":"proceed" - the model cannot decide this, only the score can, and score 3 always requires a human`)
	}
}

func TestRequiresHumanIsTrueForTwoAndThree(t *testing.T) {
	cases := []struct {
		score Score
		want  bool
	}{
		{ScoreMechanical, false},
		{ScoreMinorInterpretation, false},
		{ScoreMaterialAmbiguity, true},
		{ScoreFundamentalAmbiguity, true},
	}

	for _, tc := range cases {
		r := &Report{Score: tc.score}
		if got := r.RequiresHuman(); got != tc.want {
			t.Errorf("RequiresHuman() for score %d = %v, want %v", tc.score, got, tc.want)
		}
	}
}

func TestNilOrEmptyDocumentDoesNotCallTheModel(t *testing.T) {
	fake := &fakeCompleter{completion: &Completion{Text: `{"score":0,"rationale":"unused","findings":[]}`}}
	s := NewScreener(fake, "test-model")

	if _, err := s.Screen(context.Background(), nil); !errors.Is(err, ErrNoSpec) {
		t.Fatalf("error = %v, want ErrNoSpec for a nil document", err)
	}
	if fake.called {
		t.Fatal("the model was called for a nil document; spending a request to be told an empty document is ambiguous wastes intelligence on a mechanical question (spec §2.4)")
	}

	empty := &spec.Document{CardID: "card-1", Sections: map[string]string{}, Raw: nil}
	if _, err := s.Screen(context.Background(), empty); !errors.Is(err, ErrNoSpec) {
		t.Fatalf("error = %v, want ErrNoSpec for an empty document", err)
	}
	if fake.called {
		t.Fatal("the model was called for an empty document; spending a request to be told an empty document is ambiguous wastes intelligence on a mechanical question (spec §2.4)")
	}
}

func TestReportCarriesRawForEvidence(t *testing.T) {
	rawEvidence := []byte(`{"id":"resp_123","choices":[{"message":{"content":"{\"score\":1,\"rationale\":\"fine\",\"findings\":[]}"}}]}`)
	fake := &fakeCompleter{completion: &Completion{
		Text:  `{"score":1,"rationale":"fine","findings":[]}`,
		Model: "cheap-foreman-v1",
		Raw:   rawEvidence,
	}}
	s := NewScreener(fake, "test-model")

	report, err := s.Screen(context.Background(), testDocument(t))
	if err != nil {
		t.Fatalf("Screen returned unexpected error: %v", err)
	}
	if !bytes.Equal(report.Raw, rawEvidence) {
		t.Fatalf("report.Raw = %q, want %q - spec §20 requires the raw model call be kept as evidence", report.Raw, rawEvidence)
	}
	if report.Model != "cheap-foreman-v1" {
		t.Fatalf("report.Model = %q, want %q", report.Model, "cheap-foreman-v1")
	}
}
