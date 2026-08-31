package reviewstep_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/tuckermclean/strange-company/control-plane/internal/card"
	"github.com/tuckermclean/strange-company/control-plane/internal/github"
	"github.com/tuckermclean/strange-company/control-plane/internal/modelclient"
	"github.com/tuckermclean/strange-company/control-plane/internal/policy"
	"github.com/tuckermclean/strange-company/control-plane/internal/runner"
	"github.com/tuckermclean/strange-company/control-plane/internal/reviewstep"
	"github.com/tuckermclean/strange-company/control-plane/internal/store"
)

type fakeBoard struct {
	spec      *store.CardSpec
	artifacts []*store.Artifact
	put       []store.Artifact
	attempts  []store.AttemptRecord
}

func (f *fakeBoard) RecordAttempt(_ context.Context, rec store.AttemptRecord) (*store.AttemptOutcome, error) {
	f.attempts = append(f.attempts, rec)
	return &store.AttemptOutcome{}, nil
}

func (f *fakeBoard) GetSpec(context.Context, uuid.UUID) (*store.CardSpec, error) { return f.spec, nil }
func (f *fakeBoard) ListArtifacts(context.Context, uuid.UUID) ([]*store.Artifact, error) {
	return f.artifacts, nil
}
func (f *fakeBoard) PutArtifact(_ context.Context, a store.Artifact) (*store.Artifact, error) {
	f.put = append(f.put, a)
	return &a, nil
}

type fakeModel struct {
	prompt string
	reply  string

	// budgets records the max_tokens of every call, so a test can see the
	// retry ask for more rather than repeating a number that just failed.
	budgets []int
	// errs is returned call-by-call; a nil entry means answer normally.
	errs []error
	err  error
}

func (f *fakeModel) Complete(_ context.Context, req modelclient.CompleteRequest) (*modelclient.Completion, error) {
	for _, m := range req.Messages {
		f.prompt += m.Content + "\n"
	}
	i := len(f.budgets)
	f.budgets = append(f.budgets, req.MaxTokens)

	if i < len(f.errs) && f.errs[i] != nil {
		return nil, f.errs[i]
	}
	if f.err != nil {
		return nil, f.err
	}
	return &modelclient.Completion{Text: f.reply, Model: "claude-sonnet-5"}, nil
}

type fakePulls struct {
	opened   github.PullRequest
	calls    int
	diff     string
	diffErr  error
	diffRefs [2]string
}

func (f *fakePulls) CompareDiff(_ context.Context, _, base, head string) (string, error) {
	f.diffRefs = [2]string{base, head}
	if f.diffErr != nil {
		return "", f.diffErr
	}
	if f.diff == "" {
		return "diff --git a/src/math.js b/src/math.js\n+function mean(nums) {}\n", nil
	}
	return f.diff, nil
}

func (f *fakePulls) EnsurePullRequest(_ context.Context, pr github.PullRequest) (*github.OpenPullRequest, error) {
	f.calls++
	f.opened = pr
	return &github.OpenPullRequest{Number: 7, URL: "https://github.com/example/repo/pull/7"}, nil
}

func board() *fakeBoard {
	return &fakeBoard{
		spec: &store.CardSpec{Content: "# Context\n\nx\n\n# Acceptance criteria\n\n- AC1: returns 200 — verified by: `t`", Approved: true},
		artifacts: []*store.Artifact{
			{Type: store.ArtifactImplementationPlan, Content: "1. add a handler"},
			{Type: store.ArtifactDiff, Content: "+func healthz() {}"},
			{Type: store.ArtifactTestOutput, Content: "ok  all tests passed"},
			{Type: store.ArtifactFailureSummary, Content: "PRIVATE REASONING: I wondered at length whether"},
		},
	}
}

func testCard() *card.Card {
	repo, ref := "https://github.com/example/repo", "main"
	return &card.Card{
		ID: uuid.New(), Title: "Add a health endpoint",
		State: card.InProgress, Phase: card.PhaseReview,
		RepoURL: &repo, RepoBaseRef: &ref,
		SourceExternalID: strptr("example/repo#7"),
	}
}

func strptr(s string) *string { return &s }

func res() *policy.Resolution {
	return &policy.Resolution{Phase: "review", Alias: "review", ProviderName: "anthropic-api", Model: "claude-sonnet-5"}
}

func step(b *fakeBoard, m *fakeModel, p *fakePulls) *reviewstep.Step {
	return reviewstep.New(b, b, b, p, func(*policy.Resolution) (reviewstep.Completer, error) { return m, nil }, nil)
}

// §19: when all gates pass the pull request is created and the card moves to
// Review, where a human is the final merge authority.
func TestAPassingReviewOpensThePullRequestAndMovesToReview(t *testing.T) {
	b, m, p := board(), &fakeModel{reply: "VERDICT: PASS\n\nLooks correct."}, &fakePulls{}

	ev, err := step(b, m, p).Do(context.Background(), testCard(), res())
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if ev.NextState != card.Review {
		t.Fatalf("next state = %q, want Review", ev.NextState)
	}
	if p.calls != 1 {
		t.Fatalf("opened %d pull requests", p.calls)
	}
	if !strings.Contains(p.opened.Body, "AC1") {
		t.Errorf("the pull request body has no acceptance-criterion checklist:\n%s", p.opened.Body)
	}
}

// §18: "The reviewer does NOT receive the implementer's private reasoning."
func TestTheReviewerNeverSeesTheImplementersReasoning(t *testing.T) {
	b, m := board(), &fakeModel{reply: "VERDICT: PASS"}
	// The diff now comes from the compare API rather than from an artifact:
	// what a human reviewer would see, from the same source of truth.
	p := &fakePulls{diff: "+func healthz() {}"}

	if _, err := step(b, m, p).Do(context.Background(), testCard(), res()); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(m.prompt, "PRIVATE REASONING") {
		t.Errorf("the reviewer received the implementer's reasoning:\n%s", m.prompt)
	}
	// It must still receive what §18 says it does.
	for _, want := range []string{"AC1", "add a handler", "+func healthz()", "all tests passed"} {
		if !strings.Contains(m.prompt, want) {
			t.Errorf("the reviewer is missing %q", want)
		}
	}
}

// §18: CORRECTABLE sends the card back into implementation.
func TestACorrectableReviewReturnsTheCardToImplementation(t *testing.T) {
	b, m, p := board(), &fakeModel{reply: "VERDICT: CORRECTABLE\n\nThe handler ignores the database."}, &fakePulls{}

	ev, err := step(b, m, p).Do(context.Background(), testCard(), res())
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if ev.NextPhase != card.PhaseImplementation {
		t.Fatalf("next phase = %q", ev.NextPhase)
	}
	if ev.NextState != "" {
		t.Errorf("moved the card as well: %q", ev.NextState)
	}
}

// §18: BLOCKING sends the card to NeedsHuman.
func TestABlockingReviewGoesToAHuman(t *testing.T) {
	b, m, p := board(), &fakeModel{reply: "VERDICT: BLOCKING\n\nThis changes the auth model."}, &fakePulls{}

	ev, err := step(b, m, p).Do(context.Background(), testCard(), res())
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if ev.NextState != card.NeedsHuman {
		t.Fatalf("next state = %q", ev.NextState)
	}
}

// A verdict nobody can read is not a pass. Fail closed: a human looks.
func TestAnUnreadableVerdictIsNotAPass(t *testing.T) {
	b, m, p := board(), &fakeModel{reply: "I think it's fine, ship it"}, &fakePulls{}

	ev, err := step(b, m, p).Do(context.Background(), testCard(), res())
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if ev.NextState != card.NeedsHuman {
		t.Fatalf("next state = %q; an unreadable verdict must not proceed", ev.NextState)
	}
	if p.calls != 0 {
		t.Error("opened a pull request on a verdict nobody could read")
	}
}

// §18, in bold: "Automated review cannot move a card to Done."
func TestReviewNeverReachesDone(t *testing.T) {
	for _, reply := range []string{"VERDICT: PASS", "VERDICT: CORRECTABLE", "VERDICT: BLOCKING", "nonsense"} {
		b, m, p := board(), &fakeModel{reply: reply}, &fakePulls{}
		ev, err := step(b, m, p).Do(context.Background(), testCard(), res())
		if err != nil {
			t.Fatalf("%q: %v", reply, err)
		}
		if ev.NextState == card.Done {
			t.Fatalf("%q moved the card to Done", reply)
		}
	}
}

// The review itself is evidence: a card in Review with no record of what the
// reviewer said leaves the human approving it with nothing to read.
func TestTheReviewIsRecorded(t *testing.T) {
	b, m, p := board(), &fakeModel{reply: "VERDICT: PASS\n\nLooks correct."}, &fakePulls{}

	if _, err := step(b, m, p).Do(context.Background(), testCard(), res()); err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, a := range b.put {
		if a.Type == store.ArtifactReview && strings.Contains(a.Content, "Looks correct") {
			found = true
		}
	}
	if !found {
		t.Fatalf("the review was not recorded: %+v", b.put)
	}
}

// A criterion written without an "AC1:" prefix parses with an empty id, and
// the checklist rendered "- [x] : the criterion" for every line of the first
// successful run's pull request.
func TestTheChecklistOmitsAnAbsentCriterionID(t *testing.T) {
	b := board()
	b.spec = &store.CardSpec{Approved: true, Content: "# Context\n\nx\n\n" +
		"# Acceptance criteria\n\n- `clamp(5, 0, 10)` returns `5` — verified by: `node --test`"}
	m, p := &fakeModel{reply: "VERDICT: PASS"}, &fakePulls{}

	if _, err := step(b, m, p).Do(context.Background(), testCard(), res()); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(p.opened.Body, "[x] :") {
		t.Errorf("checklist renders an empty id:\n%s", p.opened.Body)
	}
	if !strings.Contains(p.opened.Body, "clamp(5, 0, 10)") {
		t.Errorf("the criterion is missing entirely:\n%s", p.opened.Body)
	}
}

// §18: "The reviewer receives the approved spec, implementation plan,
// acceptance criteria, final diff and passing verification summary."
//
// It never received the diff. Nothing wrote a diff artifact and the reviewer
// read one that was not there, so it reviewed the specification and described
// an implementation it had never seen -- passing a change while asserting an
// export the change did not contain.
func TestTheReviewerReceivesTheActualDiff(t *testing.T) {
	b, m := board(), &fakeModel{reply: "VERDICT: PASS"}
	p := &fakePulls{diff: "diff --git a/src/math.js\n+function mean(nums) { return 1 }\n"}

	if _, err := step(b, m, p).Do(context.Background(), testCard(), res()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(m.prompt, "function mean(nums) { return 1 }") {
		t.Fatalf("the reviewer did not receive the code it is judging:\n%s", m.prompt)
	}
	// Compared against the card's base, not against itself.
	if p.diffRefs[0] != "main" || !strings.HasPrefix(p.diffRefs[1], "agent/") {
		t.Errorf("compared %q with %q", p.diffRefs[0], p.diffRefs[1])
	}
}

// A reviewer with no code does not decline to review; it reviews the
// specification and invents the rest. So no diff is a refusal, not a warning.
func TestNoDiffMeansNoReview(t *testing.T) {
	b, m := board(), &fakeModel{reply: "VERDICT: PASS"}
	p := &fakePulls{diffErr: errors.New("compare failed")}

	if _, err := step(b, m, p).Do(context.Background(), testCard(), res()); err == nil {
		t.Fatal("reviewed without the diff")
	}
	if m.prompt != "" {
		t.Error("spent a model call on a review with no code in it")
	}
	if p.calls != 0 {
		t.Error("opened a pull request off a review that never happened")
	}
}

// §20: the diff is evidence in its own right, and §21's "what happened to card
// X?" cannot be answered from a review that quotes code nobody kept.
func TestTheDiffIsRecordedAsAnArtifact(t *testing.T) {
	b, m, p := board(), &fakeModel{reply: "VERDICT: PASS"}, &fakePulls{}

	if _, err := step(b, m, p).Do(context.Background(), testCard(), res()); err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, a := range b.put {
		if a.Type == store.ArtifactDiff {
			found = true
		}
	}
	if !found {
		t.Fatalf("no diff artifact recorded: %+v", b.put)
	}
}

// A reasoning model bills its thinking against max_tokens, so an exhausted
// budget is not the model failing -- it is the model not having been given room
// to answer. On a live card DeepSeek spent the whole budget thinking and
// returned nothing, and only a retry that happened to fit saved the run.
func TestAnExhaustedBudgetIsRetriedWithMoreRoom(t *testing.T) {
	b, p := board(), &fakePulls{}
	m := &fakeModel{
		reply: "VERDICT: PASS\n\nFine.",
		errs:  []error{modelclient.ErrBudgetExhausted},
	}

	ev, err := step(b, m, p).Do(context.Background(), testCard(), res())
	if err != nil {
		t.Fatalf("Do: %v; the retry did not happen", err)
	}
	if len(m.budgets) != 2 {
		t.Fatalf("model called %d times, want a retry", len(m.budgets))
	}
	if m.budgets[1] <= m.budgets[0] {
		t.Errorf("retried with max_tokens %d after %d; repeating a number that just failed is not a retry",
			m.budgets[1], m.budgets[0])
	}
	if ev.NextState != card.Review {
		t.Errorf("NextState = %q, want Review", ev.NextState)
	}
}

// Retried once, not forever. A model that cannot answer within the larger
// budget is not going to, and spending more is throwing money at it.
func TestTheBudgetIsRaisedOnceAndOnlyOnce(t *testing.T) {
	b, p := board(), &fakePulls{}
	m := &fakeModel{err: modelclient.ErrBudgetExhausted}

	if _, err := step(b, m, p).Do(context.Background(), testCard(), res()); err == nil {
		t.Fatal("Do() succeeded; want the second exhaustion reported")
	}
	if len(m.budgets) != 2 {
		t.Errorf("model called %d times, want exactly 2", len(m.budgets))
	}
}

// A review is a model call that costs money like any other. Recording only the
// implementation phase left a card that failed review three times looking like
// it had never been reviewed at all.
func TestAReviewReachesTheLedger(t *testing.T) {
	b, p := board(), &fakePulls{}
	m := &fakeModel{reply: "VERDICT: PASS\n\nFine."}

	if _, err := step(b, m, p).Do(context.Background(), testCard(), res()); err != nil {
		t.Fatalf("Do: %v", err)
	}
	if len(b.attempts) != 1 {
		t.Fatalf("recorded %d attempts, want 1", len(b.attempts))
	}
	if got := b.attempts[0].Phase; got != string(card.PhaseReview) {
		t.Errorf("phase = %q, want %q", got, card.PhaseReview)
	}
}

// §12.1: a provider that could not answer is not the model failing the work,
// and must not burn a rung of the escalation ladder.
func TestAReviewThatCouldNotRunIsRecordedAsInfrastructure(t *testing.T) {
	b, p := board(), &fakePulls{}
	m := &fakeModel{err: modelclient.ErrBudgetExhausted}

	_, _ = step(b, m, p).Do(context.Background(), testCard(), res())

	if len(b.attempts) != 1 {
		t.Fatalf("recorded %d attempts for a failed review, want 1", len(b.attempts))
	}
	if got := b.attempts[0].Result.Status; got != runner.StatusInfraError {
		t.Errorf("status = %q, want %q so it does not burn a rung", got, runner.StatusInfraError)
	}
}

// A live card went round this loop three times in forty minutes -- each cycle
// a coding Job and a reasoning call -- with implementation_attempt still
// reading zero. §12.1 burns an attempt only on a run that FAILED, and both runs
// here succeed: the coding run reaches its terminal event and the review call
// returns a verdict. So nothing counted and nothing bounded it.
func TestACorrectableVerdictSpendsAnImplementationAttempt(t *testing.T) {
	b, p := board(), &fakePulls{}
	m := &fakeModel{reply: "VERDICT: CORRECTABLE\n\nThe error path is untested."}

	ev, err := step(b, m, p).Do(context.Background(), testCard(), res())
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if ev.NextPhase != card.PhaseImplementation {
		t.Fatalf("next phase = %q, want implementation", ev.NextPhase)
	}

	var spent *store.AttemptRecord
	for i := range b.attempts {
		if b.attempts[i].Result.Status == runner.StatusFailed {
			spent = &b.attempts[i]
		}
	}
	if spent == nil {
		t.Fatal("a correctable verdict burned nothing; the loop is unbounded")
	}

	// Against the implementation ladder, because what was found wanting is
	// the implementation and that is the ladder §12.3 escalates from.
	if spent.Phase != string(card.PhaseImplementation) {
		t.Errorf("attempt recorded against %q, want implementation", spent.Phase)
	}
	if !strings.Contains(spent.Result.Summary, "did not pass review") {
		t.Errorf("summary = %q; a reader cannot tell why the attempt was spent", spent.Result.Summary)
	}
}

// A passing review must not spend one: the work was accepted.
func TestAPassingReviewSpendsNoAttempt(t *testing.T) {
	b, p := board(), &fakePulls{}
	m := &fakeModel{reply: "VERDICT: PASS\n\nFine."}

	if _, err := step(b, m, p).Do(context.Background(), testCard(), res()); err != nil {
		t.Fatalf("Do: %v", err)
	}
	for _, a := range b.attempts {
		if a.Result.Status == runner.StatusFailed {
			t.Error("a passing review spent an implementation attempt")
		}
	}
}
