// Package decompose implements the decomposition phase: deciding whether a
// specification is one piece of work or several, and splitting it when it is
// several.
//
// Nothing did this before, and the consequence was specific: an oversized
// specification burned the whole implementation ladder -- three cheap attempts,
// three mid, one frontier -- and escalated with "ladder exhausted", which reads
// as "the model failed seven times" when the truth is that nobody split the
// work. Slow, expensive, and blaming the wrong party.
//
// It runs BEFORE the tests phase on purpose. Splitting afterwards means
// discarding acceptance tests written against the whole, so the question has to
// be asked while the answer is still cheap.
package decompose

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"

	"github.com/tuckermclean/strange-company/control-plane/internal/card"
	"github.com/tuckermclean/strange-company/control-plane/internal/modelclient"
	"github.com/tuckermclean/strange-company/control-plane/internal/policy"
	"github.com/tuckermclean/strange-company/control-plane/internal/store"
	"github.com/tuckermclean/strange-company/control-plane/internal/worker"
)

// verdictPrefix is how the decomposer states its result.
//
// Structural, like the reviewer's: reading "this seems large" out of prose
// would make splitting depend on phrasing, and a model that merely sounded
// hesitant would fragment a perfectly ordinary card into pieces.
const verdictPrefix = "VERDICT:"

const (
	verdictSingle = "SINGLE"
	verdictSplit  = "SPLIT"
)

// maxDecomposeTokens is sized for thinking plus the answer, as everywhere else
// a reasoning model is asked a question.
const maxDecomposeTokens = 32768

// maxChildren bounds a split.
//
// Not a style preference. A model asked to break work down will keep breaking
// it down, and a specification shattered into twenty fragments is harder to
// deliver than the original -- every fragment needs its own spec, its own gate
// and its own review, and the dependencies between them become the plan nobody
// wrote. Past this the honest answer is that a human should scope it.
const maxChildren = 6

// Board is what this step reads.
type Board interface {
	GetSpec(ctx context.Context, cardID uuid.UUID) (*store.CardSpec, error)
}

// Cards is how children come into existence and are ordered.
type Cards interface {
	// CreateChild makes a child card carrying its own specification,
	// already approved: the parent's specification passed the human gate,
	// and a split of approved work does not silently become unapproved
	// work. §10.2's approval is of the intent, and the intent has not
	// changed.
	CreateChild(ctx context.Context, parent uuid.UUID, title, specText string) (uuid.UUID, error)

	// AddDependency records that a card must wait for another. §10's gate
	// already refuses to promote a card whose dependencies are unfinished,
	// so this is the whole of sequencing.
	AddDependency(ctx context.Context, cardID, dependsOn uuid.UUID) error
}

// Completer performs one model completion.
type Completer interface {
	Complete(ctx context.Context, req modelclient.CompleteRequest) (*modelclient.Completion, error)
}

// ClientFor builds a Completer for a resolved rung.
type ClientFor func(*policy.Resolution) (Completer, error)

// Artifacts records evidence.
type Artifacts interface {
	PutArtifact(ctx context.Context, a store.Artifact) (*store.Artifact, error)
}

// Step is the decomposition phase as a worker step.
type Step struct {
	board     Board
	cards     Cards
	artifacts Artifacts
	clientFor ClientFor
	log       *slog.Logger
}

// New builds the decomposition step.
func New(b Board, c Cards, a Artifacts, clientFor ClientFor, log *slog.Logger) *Step {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Step{board: b, cards: c, artifacts: a, clientFor: clientFor, log: log}
}

// Do decides whether this card is one piece of work, and splits it if not.
func (s *Step) Do(ctx context.Context, c *card.Card, res *policy.Resolution) (worker.Evidence, error) {
	cardSpec, err := s.board.GetSpec(ctx, c.ID)
	if err != nil {
		return worker.Evidence{}, fmt.Errorf("decompose: reading the specification: %w", err)
	}
	if cardSpec == nil || strings.TrimSpace(cardSpec.Content) == "" {
		// Nothing to judge. A card with no specification should not have
		// reached here, and guessing at its size would be worse than
		// carrying on: the tests phase will fail honestly instead.
		return worker.Evidence{
			Summary:   "no specification to decompose; continuing as one card",
			NextPhase: card.PhasePlanning,
		}, nil
	}

	client, err := s.clientFor(res)
	if err != nil {
		return worker.Evidence{}, fmt.Errorf("decompose: %w", err)
	}

	messages := []modelclient.Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: decomposeInput(c, cardSpec.Content)},
	}
	completion, err := client.Complete(ctx, modelclient.CompleteRequest{
		Messages: messages, MaxTokens: maxDecomposeTokens,
	})

	s.recordExchange(ctx, c, res, messages, completion, err)
	if err != nil {
		return worker.Evidence{}, fmt.Errorf("decompose: %w", err)
	}

	text := strings.TrimSpace(completion.Text)
	switch verdict(text) {
	case verdictSplit:
		return s.split(ctx, c, text)

	case verdictSingle:
		return worker.Evidence{
			Summary:   "one piece of work; continuing",
			Detail:    map[string]any{"model": completion.Model},
			NextPhase: card.PhasePlanning,
		}, nil

	default:
		// An unreadable verdict is not permission to split, and it is not
		// permission to carry on either -- both are decisions this step was
		// asked to make and did not.
		return worker.Evidence{
			Summary:   "the decomposition verdict could not be read",
			Detail:    map[string]any{"model": completion.Model},
			NextState: card.NeedsHuman,
		}, nil
	}
}

// ErrNoChildren reports a SPLIT verdict that named nothing to split into.
var ErrNoChildren = errors.New("decompose: a SPLIT verdict with no children")

// split creates the children and hands the parent to a human.
//
// The parent leaves the pipeline deliberately. It represents an OUTCOME rather
// than a unit of work, and nothing downstream knows how to implement an
// outcome: there is no diff for "the feature exists", and a review of one would
// have nothing to read. Its children carry the work; a human decides when the
// whole thing has landed, which is §19's shape applied one level up.
func (s *Step) split(ctx context.Context, c *card.Card, text string) (worker.Evidence, error) {
	children := parseChildren(text)
	if len(children) == 0 {
		return worker.Evidence{}, ErrNoChildren
	}
	if len(children) > maxChildren {
		return worker.Evidence{
			Summary: fmt.Sprintf(
				"proposed %d pieces, past the %d this creates automatically; a human should scope this",
				len(children), maxChildren),
			NextState: card.NeedsHuman,
		}, nil
	}

	var created []uuid.UUID
	var titles []string
	for _, ch := range children {
		id, err := s.cards.CreateChild(ctx, c.ID, ch.Title, ch.Spec)
		if err != nil {
			// Children already made stay: they are real work, correctly
			// specified, and discarding them because a later sibling failed
			// would throw away the part that succeeded.
			return worker.Evidence{}, fmt.Errorf("decompose: creating %q: %w", ch.Title, err)
		}
		// In order: each waits for the one before it. §10's gate already
		// refuses to promote a card whose dependencies are unfinished, so
		// this is the whole of sequencing -- no new gate, no scheduler.
		if len(created) > 0 {
			if err := s.cards.AddDependency(ctx, id, created[len(created)-1]); err != nil {
				return worker.Evidence{}, fmt.Errorf("decompose: ordering %q: %w", ch.Title, err)
			}
		}
		created = append(created, id)
		titles = append(titles, ch.Title)
	}

	return worker.Evidence{
		Summary: fmt.Sprintf("split into %d cards: %s", len(created), strings.Join(titles, "; ")),
		Detail: map[string]any{
			"children": len(created),
			"split":    true,
		},
		NextState: card.NeedsHuman,
	}, nil
}

func (s *Step) recordExchange(ctx context.Context, c *card.Card, res *policy.Resolution,
	msgs []modelclient.Message, completion *modelclient.Completion, callErr error) {
	if s.artifacts == nil {
		return
	}
	if _, err := s.artifacts.PutArtifact(ctx, store.Artifact{
		CardID: c.ID, Type: store.ArtifactModelExchange, Actor: res.ProviderName,
		Model: res.Model, ContentType: "text/markdown",
		Content: modelclient.Transcript(
			modelclient.CompleteRequest{Messages: msgs, MaxTokens: maxDecomposeTokens}, completion, callErr),
	}); err != nil {
		s.log.Error("could not record the decomposition exchange", "card_id", c.ID, "error", err)
	}
}

// verdict reads the structural line.
func verdict(text string) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, verdictPrefix) {
			continue
		}
		switch v := strings.TrimSpace(strings.TrimPrefix(line, verdictPrefix)); v {
		case verdictSingle, verdictSplit:
			return v
		}
	}
	return ""
}

// child is one piece of a split.
type child struct {
	Title string
	Spec  string
}

// parseChildren reads the proposed pieces.
//
// Each is a "## CARD: <title>" heading followed by a specification in the same
// shape the gate already validates -- so a child is checked by exactly the same
// rules as a card a human wrote, rather than through a weaker path invented
// here.
func parseChildren(text string) []child {
	const marker = "## CARD:"

	var out []child
	var current *child
	var body strings.Builder

	flush := func() {
		if current == nil {
			return
		}
		if content := strings.TrimSpace(body.String()); current.Title != "" && content != "" {
			out = append(out, child{Title: current.Title, Spec: content})
		}
		body.Reset()
	}

	for _, line := range strings.Split(text, "\n") {
		if trimmed := strings.TrimSpace(line); strings.HasPrefix(trimmed, marker) {
			flush()
			current = &child{Title: strings.TrimSpace(strings.TrimPrefix(trimmed, marker))}
			continue
		}
		if current != nil {
			body.WriteString(line)
			body.WriteString("\n")
		}
	}
	flush()

	return out
}

// decomposeInput is what the decomposer is given.
func decomposeInput(c *card.Card, specText string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Card\n\n%s\n\n# Specification\n\n%s\n",
		strings.TrimSpace(c.Title), strings.TrimSpace(specText))
	return b.String()
}
