package decompose_test

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/tuckermclean/strange-company/control-plane/internal/card"
	"github.com/tuckermclean/strange-company/control-plane/internal/decompose"
	"github.com/tuckermclean/strange-company/control-plane/internal/modelclient"
	"github.com/tuckermclean/strange-company/control-plane/internal/policy"
	"github.com/tuckermclean/strange-company/control-plane/internal/store"
)

type fakeBoard struct{ spec *store.CardSpec }

func (f *fakeBoard) GetSpec(context.Context, uuid.UUID) (*store.CardSpec, error) {
	return f.spec, nil
}

type createdChild struct {
	Parent uuid.UUID
	Title  string
	Spec   string
	ID     uuid.UUID
}

type dependency struct{ Card, DependsOn uuid.UUID }

type fakeCards struct {
	children []createdChild
	deps     []dependency
}

func (f *fakeCards) CreateChild(_ context.Context, parent uuid.UUID, title, spec string) (uuid.UUID, error) {
	id := uuid.New()
	f.children = append(f.children, createdChild{Parent: parent, Title: title, Spec: spec, ID: id})
	return id, nil
}
func (f *fakeCards) AddDependency(_ context.Context, cardID, dependsOn uuid.UUID) error {
	f.deps = append(f.deps, dependency{Card: cardID, DependsOn: dependsOn})
	return nil
}

type fakeArtifacts struct{ put []store.Artifact }

func (f *fakeArtifacts) PutArtifact(_ context.Context, a store.Artifact) (*store.Artifact, error) {
	f.put = append(f.put, a)
	return &a, nil
}

type fakeModel struct{ reply string }

func (f *fakeModel) Complete(context.Context, modelclient.CompleteRequest) (*modelclient.Completion, error) {
	return &modelclient.Completion{Text: f.reply, Model: "a-model"}, nil
}

func step(reply string, cards *fakeCards) (*decompose.Step, *fakeArtifacts) {
	arts := &fakeArtifacts{}
	s := decompose.New(
		&fakeBoard{spec: &store.CardSpec{Content: "# Context\n\nsomething\n"}},
		cards, arts,
		func(*policy.Resolution) (decompose.Completer, error) { return &fakeModel{reply: reply}, nil },
		nil,
	)
	return s, arts
}

func testCard() *card.Card {
	return &card.Card{ID: uuid.New(), Title: "do a big thing", Phase: card.PhaseDecomposition}
}

func res() *policy.Resolution {
	return &policy.Resolution{Phase: "decomposition", Alias: "plan", ProviderName: "hermes", Model: "m"}
}

// Most work is one card, and splitting one that did not need it costs more than
// leaving it whole.
func TestASingleVerdictJustCarriesOn(t *testing.T) {
	cards := &fakeCards{}
	s, _ := step("VERDICT: SINGLE\n\nIt is one coherent change.", cards)

	ev, err := s.Do(context.Background(), testCard(), res())
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if ev.NextPhase != card.PhasePlanning {
		t.Errorf("next phase = %q, want planning", ev.NextPhase)
	}
	if len(cards.children) != 0 {
		t.Errorf("a SINGLE verdict created %d children", len(cards.children))
	}
}

const splitReply = `VERDICT: SPLIT

## CARD: Add the parser

# Context

The config is unparsed.

# Acceptance criteria

- parses a config — verified by: ` + "`go test ./...`" + `

## CARD: Use the parser at startup

# Context

Startup ignores config.

# Acceptance criteria

- startup reads the config — verified by: ` + "`go test ./...`" + `
`

// The whole point: an oversized specification becomes real cards instead of
// burning seven implementation attempts and escalating "ladder exhausted".
func TestASplitCreatesChildrenInOrder(t *testing.T) {
	cards := &fakeCards{}
	s, _ := step(splitReply, cards)
	parent := testCard()

	ev, err := s.Do(context.Background(), parent, res())
	if err != nil {
		t.Fatalf("Do: %v", err)
	}

	if len(cards.children) != 2 {
		t.Fatalf("created %d children, want 2: %+v", len(cards.children), cards.children)
	}
	if cards.children[0].Title != "Add the parser" {
		t.Errorf("first child = %q", cards.children[0].Title)
	}
	if !strings.Contains(cards.children[0].Spec, "Acceptance criteria") {
		t.Error("a child was created without its specification")
	}

	// Sequenced: the second waits for the first. §10's gate enforces this
	// already, so an edge is the whole of ordering.
	if len(cards.deps) != 1 {
		t.Fatalf("dependencies = %+v, want the second waiting for the first", cards.deps)
	}
	if cards.deps[0].Card != cards.children[1].ID || cards.deps[0].DependsOn != cards.children[0].ID {
		t.Errorf("dependency is the wrong way round: %+v", cards.deps[0])
	}

	// The parent represents an outcome, not a unit of work: nothing
	// downstream knows how to implement "the feature exists".
	if ev.NextState != card.NeedsHuman {
		t.Errorf("parent went to %q, want NeedsHuman", ev.NextState)
	}
	if !strings.Contains(ev.Summary, "split into 2") {
		t.Errorf("summary = %q; a human cannot tell this from a failure", ev.Summary)
	}
}

// A model asked to break work down will keep breaking it down, and a
// specification shattered into fragments is harder to deliver than the original.
func TestAnExcessiveSplitAsksAHumanToScopeIt(t *testing.T) {
	var b strings.Builder
	b.WriteString("VERDICT: SPLIT\n")
	for i := 0; i < 12; i++ {
		b.WriteString("\n## CARD: piece\n\n# Context\n\nx\n")
	}
	cards := &fakeCards{}
	s, _ := step(b.String(), cards)

	ev, err := s.Do(context.Background(), testCard(), res())
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if len(cards.children) != 0 {
		t.Errorf("created %d children past the cap", len(cards.children))
	}
	if ev.NextState != card.NeedsHuman {
		t.Errorf("next state = %q, want NeedsHuman", ev.NextState)
	}
}

// An unreadable verdict is not permission to split, and not permission to carry
// on either. Both are decisions this step was asked to make and did not.
func TestAnUnreadableVerdictGoesToAHuman(t *testing.T) {
	cards := &fakeCards{}
	s, _ := step("I think maybe this could be broken up somehow?", cards)

	ev, err := s.Do(context.Background(), testCard(), res())
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if ev.NextState != card.NeedsHuman {
		t.Errorf("next state = %q, want NeedsHuman", ev.NextState)
	}
	if len(cards.children) != 0 {
		t.Error("an unreadable verdict split the card anyway")
	}
}

// The question is as worth keeping as the answer: §18's lesson applied here.
func TestTheExchangeIsRecorded(t *testing.T) {
	cards := &fakeCards{}
	s, arts := step("VERDICT: SINGLE\n\nfine", cards)

	if _, err := s.Do(context.Background(), testCard(), res()); err != nil {
		t.Fatalf("Do: %v", err)
	}
	for _, a := range arts.put {
		if a.Type == store.ArtifactModelExchange && strings.Contains(a.Content, "do a big thing") {
			return
		}
	}
	t.Error("the decomposition exchange was not recorded")
}
