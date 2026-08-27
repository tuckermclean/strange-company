package specsession_test

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/tuckermclean/strange-company/control-plane/internal/ambiguity"
	"github.com/tuckermclean/strange-company/control-plane/internal/card"
	"github.com/tuckermclean/strange-company/control-plane/internal/spec"
	"github.com/tuckermclean/strange-company/control-plane/internal/specsession"
)

func testCard() *card.Card {
	repo := "https://github.com/example/thing"
	ref := "main"
	return &card.Card{
		ID:          uuid.MustParse("11111111-2222-3333-4444-555555555555"),
		Title:       "Add a health endpoint",
		RepoURL:     &repo,
		RepoBaseRef: &ref,
		State:       card.Backlog,
	}
}

func testReport() *ambiguity.Report {
	return &ambiguity.Report{
		Score:     ambiguity.ScoreMaterialAmbiguity,
		Rationale: "the endpoint's failure semantics are unstated",
		Findings: []ambiguity.Finding{
			{Section: "Acceptance criteria", Concern: "does unhealthy mean 503 or 200 with a body"},
			{Section: "Scope", Concern: "unclear whether the database is checked"},
		},
	}
}

func TestThePromptCarriesTheCardAndItsRepository(t *testing.T) {
	got, err := specsession.BuildSystemPrompt(testCard(), nil, testReport())
	if err != nil {
		t.Fatalf("BuildSystemPrompt: %v", err)
	}
	for _, want := range []string{
		"Add a health endpoint",
		"11111111-2222-3333-4444-555555555555",
		"https://github.com/example/thing",
		"main",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt is missing %q", want)
		}
	}
}

// The screening already found these; making the human rediscover them in
// conversation wastes the model call that produced them.
func TestThePromptCarriesEveryAmbiguityFinding(t *testing.T) {
	report := testReport()
	got, err := specsession.BuildSystemPrompt(testCard(), nil, report)
	if err != nil {
		t.Fatalf("BuildSystemPrompt: %v", err)
	}
	for _, f := range report.Findings {
		if !strings.Contains(got, f.Concern) {
			t.Errorf("prompt is missing finding %q", f.Concern)
		}
	}
	if !strings.Contains(got, report.Rationale) {
		t.Errorf("prompt is missing the rationale")
	}
}

// Document.Sections is a map, so an unsorted walk would produce a different
// prompt on every call. Two identical cards would then open conversations
// that cannot be compared, and the same input would not be reproducible in
// the audit log.
func TestThePromptIsDeterministic(t *testing.T) {
	doc := &spec.Document{
		CardID: "11111111-2222-3333-4444-555555555555",
		Sections: map[string]string{
			"Problem":             "health is unobservable",
			"Scope":               "one endpoint",
			"Acceptance criteria": "returns 200 when healthy",
			"Out of scope":        "metrics",
			"Risks":               "none",
		},
		Criteria: []spec.Criterion{{ID: "AC1", Text: "returns 200", Verification: "curl"}},
	}

	first, err := specsession.BuildSystemPrompt(testCard(), doc, testReport())
	if err != nil {
		t.Fatalf("BuildSystemPrompt: %v", err)
	}
	for i := 0; i < 32; i++ {
		again, err := specsession.BuildSystemPrompt(testCard(), doc, testReport())
		if err != nil {
			t.Fatalf("BuildSystemPrompt: %v", err)
		}
		if again != first {
			t.Fatalf("prompt differs between calls (iteration %d)", i)
		}
	}
}

func TestThePromptIncludesAnExistingDraftSpecification(t *testing.T) {
	doc := &spec.Document{
		CardID:   "11111111-2222-3333-4444-555555555555",
		Sections: map[string]string{"Problem": "health is unobservable"},
		Criteria: []spec.Criterion{{ID: "AC1", Text: "returns 200 when healthy", Verification: "curl"}},
	}

	got, err := specsession.BuildSystemPrompt(testCard(), doc, testReport())
	if err != nil {
		t.Fatalf("BuildSystemPrompt: %v", err)
	}
	for _, want := range []string{"health is unobservable", "returns 200 when healthy", "curl"} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt is missing %q from the existing draft", want)
		}
	}
}

// A card with no title has nothing to hold a conversation about, and an
// untitled session is unfindable in the dashboard -- which is the entire
// handoff.
func TestBuildSystemPromptRejectsACardWithNoTitle(t *testing.T) {
	c := testCard()
	c.Title = "   "
	if _, err := specsession.BuildSystemPrompt(c, nil, testReport()); err == nil {
		t.Fatal("expected an error for a card with no title")
	}
}

// Screening is what sends a card into this conversation; opening one without
// its report would silently drop the reason the human was called in.
func TestBuildSystemPromptRequiresAReport(t *testing.T) {
	if _, err := specsession.BuildSystemPrompt(testCard(), nil, nil); err == nil {
		t.Fatal("expected an error when no ambiguity report is supplied")
	}
}

// The dashboard session list shows titles, so it has to name the card a human
// is looking for.
func TestTitleNamesTheCard(t *testing.T) {
	got := specsession.Title(testCard())
	if !strings.Contains(got, "Add a health endpoint") {
		t.Fatalf("title %q does not name the card", got)
	}
	if got != specsession.Title(testCard()) {
		t.Fatal("title is not stable")
	}
}
