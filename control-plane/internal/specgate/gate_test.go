package specgate

import (
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/tuckermclean/strange-company/control-plane/internal/card"
	"github.com/tuckermclean/strange-company/control-plane/internal/spec"
)

// --- fixtures --------------------------------------------------------------

// strPtr returns a pointer to s, for building Card literals with pointer
// fields inline.
func strPtr(s string) *string { return &s }

// satisfiedInputs returns an Inputs that passes every deterministic check in
// spec section 10: a specification exists with no reported problems, the
// card names a repository, it has no open dependencies, and a
// permitted-actions policy exists. Each test starts here and breaks exactly
// the thing it means to test.
//
// Card is a fresh pointer each call so a test that mutates in.Card's fields
// never leaks into another test.
func satisfiedInputs() Inputs {
	return Inputs{
		Card: &card.Card{
			ID:      uuid.New(),
			Title:   "example card",
			RepoURL: strPtr("https://github.com/example/repo"),
		},
		Spec: &spec.Document{
			CardID: "card-1",
			Sections: map[string]string{
				"Context": "why this card exists",
				"Task":    "what to build",
			},
			Criteria: []spec.Criterion{
				{ID: "AC-1", Text: "does the thing", Verification: "go test ./..."},
			},
		},
		SpecProblems:     nil,
		SpecApproved:     true,
		Dependencies:     nil,
		PermittedActions: true,
	}
}

// countByReason tallies failures by Reason for set/count assertions that
// shouldn't be sensitive to slice order.
func countByReason(failures []Failure) map[Reason]int {
	counts := make(map[Reason]int, len(failures))
	for _, f := range failures {
		counts[f.Reason]++
	}
	return counts
}

// --- happy path --------------------------------------------------------------

func TestFullySatisfiedCardPassesWithZeroFailures(t *testing.T) {
	got := Evaluate(satisfiedInputs())
	if !got.Passed {
		t.Fatalf("§10: a card meeting every deterministic requirement must pass; got Passed=false, failures=%+v", got.Failures)
	}
	if len(got.Failures) != 0 {
		t.Fatalf("want zero failures for a fully satisfied card, got %d: %+v", len(got.Failures), got.Failures)
	}
}

// --- rule 1: nil Spec ----------------------------------------------------

func TestNilSpecIsSpecMissingAndStopsFurtherDocumentChecks(t *testing.T) {
	in := satisfiedInputs()
	in.Spec = nil
	// These would each produce a failure if the (nonexistent) document were
	// still examined. Rule 1 says it must not be: a nil Spec reports
	// exactly ReasonNoSpec and nothing else about the document.
	in.SpecProblems = []spec.Problem{
		{Kind: "missing_section", Detail: "Context"},
		{Kind: "no_criteria", Detail: ""},
		{Kind: "criterion_without_verification", Detail: "AC-1"},
	}

	got := Evaluate(in)

	if got.Passed {
		t.Fatal("§10: a card with no specification must not become Ready")
	}
	if len(got.Failures) != 1 || got.Failures[0].Reason != ReasonNoSpec {
		t.Fatalf("§10: a nil Spec must produce exactly one failure (ReasonNoSpec) and no document-derived failures; got %+v", got.Failures)
	}
}

// --- rule 2: missing_section / empty_section ------------------------------

func TestMissingOrEmptySectionBecomesSpecIncomplete(t *testing.T) {
	in := satisfiedInputs()
	in.SpecProblems = []spec.Problem{
		{Kind: "missing_section", Detail: "Interfaces"},
		{Kind: "empty_section", Detail: "Constraints"},
	}

	got := Evaluate(in)

	if got.Passed {
		t.Fatal("§10: a spec missing (or with an empty) required section must not become Ready")
	}
	wantSections := map[string]bool{"Interfaces": false, "Constraints": false}
	for _, f := range got.Failures {
		if f.Reason != ReasonSpecIncomplete {
			t.Fatalf("want only ReasonSpecIncomplete failures for missing/empty sections, also got %s (%q)", f.Reason, f.Detail)
		}
		if _, known := wantSections[f.Detail]; !known {
			t.Fatalf("ReasonSpecIncomplete failure names unexpected section %q", f.Detail)
		}
		wantSections[f.Detail] = true
	}
	for section, seen := range wantSections {
		if !seen {
			t.Errorf("want a ReasonSpecIncomplete failure naming section %q, got none (failures=%+v)", section, got.Failures)
		}
	}
}

// --- rule 3: no_criteria ---------------------------------------------------

func TestNoCriteriaBecomesNoAcceptanceCriteria(t *testing.T) {
	in := satisfiedInputs()
	in.SpecProblems = []spec.Problem{{Kind: "no_criteria", Detail: "no acceptance criteria found"}}

	got := Evaluate(in)

	if got.Passed {
		t.Fatal("§10: a spec with no acceptance criteria must not become Ready")
	}
	if len(got.Failures) != 1 || got.Failures[0].Reason != ReasonNoCriteria {
		t.Fatalf("want exactly one ReasonNoCriteria failure, got %+v", got.Failures)
	}
}

// --- rule 4: criterion_without_verification -------------------------------

func TestCriterionWithoutVerificationNamesTheCriterion(t *testing.T) {
	in := satisfiedInputs()
	in.SpecProblems = []spec.Problem{{Kind: "criterion_without_verification", Detail: "AC-2"}}

	got := Evaluate(in)

	if got.Passed {
		t.Fatal("§10: an acceptance criterion without a stated verification method must not become Ready")
	}
	if len(got.Failures) != 1 || got.Failures[0].Reason != ReasonUnverifiableCriteria || got.Failures[0].Detail != "AC-2" {
		t.Fatalf("want exactly one ReasonUnverifiableCriteria failure naming %q, got %+v", "AC-2", got.Failures)
	}
}

// --- rule 5: empty Card.RepoURL --------------------------------------------

func TestEmptyRepoURLIsRepoMissing(t *testing.T) {
	cases := []struct {
		name    string
		repoURL *string
	}{
		{"nil pointer", nil},
		{"pointer to empty string", strPtr("")},
		{"pointer to whitespace only", strPtr("   ")},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := satisfiedInputs()
			cardCopy := *in.Card
			cardCopy.RepoURL = c.repoURL
			in.Card = &cardCopy

			got := Evaluate(in)

			if got.Passed {
				t.Fatalf("§10: a card without a repository must not become Ready (case: %s)", c.name)
			}
			if counts := countByReason(got.Failures); counts[ReasonNoRepo] != 1 {
				t.Fatalf("want exactly one ReasonNoRepo failure (case: %s), got %+v", c.name, got.Failures)
			}
		})
	}
}

func TestNonEmptyRepoURLPassesTheRepoCheck(t *testing.T) {
	got := Evaluate(satisfiedInputs())
	for _, f := range got.Failures {
		if f.Reason == ReasonNoRepo {
			t.Fatalf("a card with a non-empty RepoURL must not report ReasonNoRepo, got %+v", f)
		}
	}
}

// --- rule 6: dependencies must be Done -------------------------------------

func TestEveryNonDoneDependencyStateBlocksReady(t *testing.T) {
	states := []card.State{
		card.Backlog,
		card.Ready,
		card.InProgress,
		card.Review,
		card.Blocked,
		card.NeedsHuman,
	}

	for _, st := range states {
		t.Run(string(st), func(t *testing.T) {
			in := satisfiedInputs()
			depID := uuid.New()
			in.Dependencies = []*card.Card{{ID: depID, State: st}}

			got := Evaluate(in)

			if got.Passed {
				t.Fatalf("§10: a card whose dependency is not Done must not become Ready (dependency state: %s)", st)
			}
			var match *Failure
			for i := range got.Failures {
				if got.Failures[i].Reason == ReasonDependenciesOpen {
					match = &got.Failures[i]
					break
				}
			}
			if match == nil {
				t.Fatalf("want a ReasonDependenciesOpen failure for dependency in state %s, got failures %+v", st, got.Failures)
			}
			if !strings.Contains(match.Detail, depID.String()) {
				t.Errorf("failure detail %q does not name the offending dependency %s", match.Detail, depID)
			}
			if !strings.Contains(match.Detail, string(st)) {
				t.Errorf("failure detail %q does not name the dependency's state %s", match.Detail, st)
			}
		})
	}
}

func TestDependenciesAllDonePassesTheDependencyCheck(t *testing.T) {
	in := satisfiedInputs()
	in.Dependencies = []*card.Card{
		{ID: uuid.New(), State: card.Done},
		{ID: uuid.New(), State: card.Done},
	}

	got := Evaluate(in)

	if !got.Passed {
		t.Fatalf("§10: dependencies that are all Done must not block Ready; got failures %+v", got.Failures)
	}
}

func TestNoDependenciesPassesTheDependencyCheck(t *testing.T) {
	in := satisfiedInputs()
	in.Dependencies = nil

	got := Evaluate(in)

	if !got.Passed {
		t.Fatalf("§10: a card with no dependencies must pass the dependency check; got failures %+v", got.Failures)
	}
}

// --- rule 7: permitted-actions policy ---------------------------------------

func TestNoPermittedActionsPolicyBlocksReady(t *testing.T) {
	in := satisfiedInputs()
	in.PermittedActions = false

	got := Evaluate(in)

	if got.Passed {
		t.Fatal("§10: a card without a permitted-actions policy must not become Ready")
	}
	if len(got.Failures) != 1 || got.Failures[0].Reason != ReasonNoPermittedActions {
		t.Fatalf("want exactly one ReasonNoPermittedActions failure, got %+v", got.Failures)
	}
}

// --- rule 9: every failure is reported, not just the first ------------------

func TestMultipleSimultaneousFailuresAreAllReported(t *testing.T) {
	in := satisfiedInputs()
	in.SpecProblems = []spec.Problem{
		{Kind: "missing_section", Detail: "Interfaces"},
		{Kind: "no_criteria", Detail: ""},
		{Kind: "criterion_without_verification", Detail: "AC-1"},
	}
	cardCopy := *in.Card
	cardCopy.RepoURL = nil
	in.Card = &cardCopy
	in.Dependencies = []*card.Card{{ID: uuid.New(), State: card.Blocked}}
	in.PermittedActions = false

	got := Evaluate(in)

	if got.Passed {
		t.Fatal("§10: a card failing multiple checks at once must not become Ready")
	}
	if len(got.Failures) != 6 {
		t.Fatalf("§10: an operator fixing a spec must see the whole list of problems in one pass; want 6 failures, got %d: %+v", len(got.Failures), got.Failures)
	}
	wantCounts := map[Reason]int{
		ReasonSpecIncomplete:       1,
		ReasonNoCriteria:           1,
		ReasonUnverifiableCriteria: 1,
		ReasonNoRepo:               1,
		ReasonDependenciesOpen:     1,
		ReasonNoPermittedActions:   1,
	}
	gotCounts := countByReason(got.Failures)
	for reason, want := range wantCounts {
		if gotCounts[reason] != want {
			t.Errorf("want %d failure(s) of reason %s, got %d (failures=%+v)", want, reason, gotCounts[reason], got.Failures)
		}
	}
	if len(gotCounts) != len(wantCounts) {
		t.Errorf("got unexpected reasons in failure set: %+v", gotCounts)
	}
}

// --- rule 8 / property: Passed is true iff Failures is empty -----------------

func TestPassedIsTrueOnlyWhenFailuresIsEmpty(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Inputs)
	}{
		{"fully satisfied", func(in *Inputs) {}},
		{"nil spec", func(in *Inputs) { in.Spec = nil }},
		{"spec incomplete", func(in *Inputs) {
			in.SpecProblems = []spec.Problem{{Kind: "missing_section", Detail: "Context"}}
		}},
		{"no criteria", func(in *Inputs) {
			in.SpecProblems = []spec.Problem{{Kind: "no_criteria", Detail: ""}}
		}},
		{"unverifiable criterion", func(in *Inputs) {
			in.SpecProblems = []spec.Problem{{Kind: "criterion_without_verification", Detail: "AC-1"}}
		}},
		{"no repo", func(in *Inputs) {
			cardCopy := *in.Card
			cardCopy.RepoURL = nil
			in.Card = &cardCopy
		}},
		{"open dependency", func(in *Inputs) {
			in.Dependencies = []*card.Card{{ID: uuid.New(), State: card.InProgress}}
		}},
		{"no permitted actions", func(in *Inputs) { in.PermittedActions = false }},
		{"everything broken at once", func(in *Inputs) {
			in.Spec = nil
			cardCopy := *in.Card
			cardCopy.RepoURL = nil
			in.Card = &cardCopy
			in.Dependencies = []*card.Card{{ID: uuid.New(), State: card.Backlog}}
			in.PermittedActions = false
		}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := satisfiedInputs()
			c.mutate(&in)

			got := Evaluate(in)

			if got.Passed != (len(got.Failures) == 0) {
				t.Fatalf("Result.Passed=%v is inconsistent with %d failures; Passed must be true if and only if Failures is empty (failures=%+v)", got.Passed, len(got.Failures), got.Failures)
			}
		})
	}
}

// --- rule 11: Evaluate never panics ------------------------------------------

func TestEvaluateNeverPanicsOnDegenerateInputs(t *testing.T) {
	cases := []struct {
		name string
		in   Inputs
	}{
		{"zero-value Inputs", Inputs{}},
		{"nil Card", Inputs{Spec: &spec.Document{}, PermittedActions: true}},
		{"nil Spec", Inputs{Card: &card.Card{}, PermittedActions: true}},
		{"nil SpecProblems on a non-nil Spec", Inputs{
			Card:         &card.Card{RepoURL: strPtr("x")},
			Spec:         &spec.Document{},
			SpecProblems: nil,
		}},
		{"nil Dependencies slice", Inputs{
			Card:         &card.Card{RepoURL: strPtr("x")},
			Spec:         &spec.Document{},
			Dependencies: nil,
		}},
		{"Dependencies slice containing nil entries", Inputs{
			Card:         &card.Card{RepoURL: strPtr("x")},
			Spec:         &spec.Document{},
			Dependencies: []*card.Card{nil, nil},
		}},
		{"Card with a nil RepoURL pointer", Inputs{
			Card: &card.Card{RepoURL: nil},
			Spec: &spec.Document{},
		}},
		{"nil Card and nil Spec and nil Dependencies together", Inputs{}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("§10: Evaluate must never panic; panicked on input %q: %v", c.name, r)
				}
			}()
			_ = Evaluate(c.in)
		})
	}
}

// --- rule 10: stable ordering -------------------------------------------------

func TestFailureOrderIsStableAcrossRuns(t *testing.T) {
	in := satisfiedInputs()
	in.SpecProblems = []spec.Problem{
		{Kind: "criterion_without_verification", Detail: "AC-1"},
		{Kind: "missing_section", Detail: "Interfaces"},
		{Kind: "no_criteria", Detail: ""},
	}
	cardCopy := *in.Card
	cardCopy.RepoURL = nil
	in.Card = &cardCopy
	in.PermittedActions = false

	first := Evaluate(in)
	second := Evaluate(in)

	if !reflect.DeepEqual(first.Failures, second.Failures) {
		t.Fatalf("§10: failure ordering must be stable across runs so a UI diffing them does not flicker; first=%+v second=%+v", first.Failures, second.Failures)
	}
}

// --- Result.Error --------------------------------------------------------

func TestResultErrorListsEveryFailureAndIsStable(t *testing.T) {
	in := satisfiedInputs()
	in.Spec = nil
	in.PermittedActions = false

	got := Evaluate(in)
	if len(got.Failures) < 2 {
		t.Fatalf("test setup: want at least 2 failures to exercise Error(), got %d", len(got.Failures))
	}

	first := got.Error()
	second := got.Error()
	if first != second {
		t.Fatalf("Result.Error() is not stable across repeated calls: %q != %q", first, second)
	}
	for _, f := range got.Failures {
		if !strings.Contains(first, string(f.Reason)) {
			t.Errorf("Error() output %q does not mention failure reason %s", first, f.Reason)
		}
	}
}

func TestResultErrorOnPassingResult(t *testing.T) {
	got := Evaluate(satisfiedInputs())
	if !strings.Contains(got.Error(), "passed") {
		t.Errorf("want Error() on a passing Result to say so, got %q", got.Error())
	}
}

// A gate that ignores problems it was not taught about fails OPEN, which is the
// worst direction for a gate to fail. spec.Parse already emits a "malformed"
// kind that none of the explicit passes match.
func TestUnrecognisedSpecProblemStillBlocksPromotion(t *testing.T) {
	in := satisfiedInputs()
	in.SpecProblems = []spec.Problem{
		{Kind: "malformed", Detail: "document is not valid markdown"},
	}

	got := Evaluate(in)

	if got.Passed {
		t.Fatal("an unrecognised spec problem must block promotion, not be ignored")
	}
	if !strings.Contains(got.Error(), "malformed") {
		t.Errorf("the failure must name the unrecognised kind so it can be fixed, got %q", got.Error())
	}
}

func TestAFutureProblemKindFailsClosed(t *testing.T) {
	in := satisfiedInputs()
	in.SpecProblems = []spec.Problem{
		{Kind: "some_kind_added_later", Detail: "whatever it turns out to be"},
	}

	if Evaluate(in).Passed {
		t.Fatal("a problem kind added to spec.Parse later must block until this package handles it deliberately")
	}
}

// --- spec §10.2: human approval of the completed spec -----------------------

// TestUnapprovedSpecBlocksReadyEvenWhenPerfect checks that a spec passing
// every other check still blocks the gate when no human has approved it: a
// perfectly-formatted spec nobody read is exactly the failure §10.2 exists
// to prevent.
func TestUnapprovedSpecBlocksReadyEvenWhenPerfect(t *testing.T) {
	in := satisfiedInputs()
	in.SpecApproved = false

	got := Evaluate(in)

	if got.Passed {
		t.Fatal("spec §10.2: a card must not become Ready on an unapproved specification, however well-formed")
	}
	if len(got.Failures) != 1 || got.Failures[0].Reason != ReasonSpecNotApproved {
		t.Fatalf("want exactly one ReasonSpecNotApproved failure, got %+v", got.Failures)
	}
}

// TestApprovedSpecPassesTheApprovalCheck checks the positive case: with
// SpecApproved true and everything else satisfied, the gate passes.
func TestApprovedSpecPassesTheApprovalCheck(t *testing.T) {
	in := satisfiedInputs()
	in.SpecApproved = true

	got := Evaluate(in)

	if !got.Passed {
		t.Fatalf("§10.2: an approved, otherwise-satisfied spec must pass the gate, got failures %+v", got.Failures)
	}
}

// TestUnapprovedSpecIsReportedAlongsideOtherFailures checks that the
// approval check does not short-circuit the others: an unapproved spec with
// other unrelated problems reports all of them in one pass, per rule 9.
func TestUnapprovedSpecIsReportedAlongsideOtherFailures(t *testing.T) {
	in := satisfiedInputs()
	in.SpecApproved = false
	in.PermittedActions = false
	cardCopy := *in.Card
	cardCopy.RepoURL = nil
	in.Card = &cardCopy

	got := Evaluate(in)

	if got.Passed {
		t.Fatal("§10: multiple simultaneous failures, including an unapproved spec, must not pass")
	}
	wantCounts := map[Reason]int{
		ReasonSpecNotApproved:    1,
		ReasonNoRepo:             1,
		ReasonNoPermittedActions: 1,
	}
	gotCounts := countByReason(got.Failures)
	for reason, want := range wantCounts {
		if gotCounts[reason] != want {
			t.Errorf("want %d failure(s) of reason %s, got %d (failures=%+v)", want, reason, gotCounts[reason], got.Failures)
		}
	}
	if len(gotCounts) != len(wantCounts) {
		t.Errorf("got unexpected reasons in failure set: %+v", gotCounts)
	}
}
