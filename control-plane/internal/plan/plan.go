// Package plan implements §11.1, the planning phase.
//
// The planner reads an approved specification and produces an implementation
// plan: which criterion maps to which work, which files are likely, which
// migrations and interfaces are involved, how each criterion will be verified,
// and what is risky. It may declare the specification insufficient. §11.1 is
// explicit that it may not guess.
package plan

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

// insufficientPrefix is how a planner declares it cannot proceed.
//
// A prefix rather than a judgement about the prose: whether a specification is
// sufficient is the model's to state and nobody's to interpret. Reading intent
// out of free text would make the difference between "planned" and "returned
// to a human" depend on phrasing.
const insufficientPrefix = "SPEC_INSUFFICIENT"

// maxPlanTokens bounds one plan. A plan longer than this is not a plan --
// but a reasoning model's thinking is billed against the same budget, so this
// covers thinking plus the plan rather than the plan alone (see
// internal/ambiguity for what a budget sized for the answer alone does).
const maxPlanTokens = 8192

// Completer performs one model completion.
type Completer interface {
	Complete(ctx context.Context, req modelclient.CompleteRequest) (*modelclient.Completion, error)
}

// ClientFor builds a Completer for a resolved rung. Injected so this package
// never decides which provider or model to use -- that is policy's job.
type ClientFor func(*policy.Resolution) (Completer, error)

// Specs reads a card's specification.
type Specs interface {
	GetSpec(ctx context.Context, cardID uuid.UUID) (*store.CardSpec, error)
}

// Artifacts records evidence.
type Artifacts interface {
	PutArtifact(ctx context.Context, a store.Artifact) (*store.Artifact, error)
}

// Step is the §11.1 planning phase as a worker step.
type Step struct {
	specs     Specs
	artifacts Artifacts
	clientFor ClientFor
	log       *slog.Logger
}

// New builds the planning step.
func New(s Specs, a Artifacts, clientFor ClientFor, log *slog.Logger) *Step {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Step{specs: s, artifacts: a, clientFor: clientFor, log: log}
}

// Do plans one card.
func (s *Step) Do(ctx context.Context, c *card.Card, res *policy.Resolution) (worker.Evidence, error) {
	// Read first. Planning with no specification is the guess §11.1 forbids,
	// and discovering it should not cost a model call.
	spec, err := s.specs.GetSpec(ctx, c.ID)
	if err != nil {
		return worker.Evidence{}, fmt.Errorf("plan: reading the specification: %w", err)
	}
	if spec == nil || strings.TrimSpace(spec.Content) == "" {
		return worker.Evidence{}, errors.New("plan: the card has no specification to plan from")
	}

	client, err := s.clientFor(res)
	if err != nil {
		return worker.Evidence{}, fmt.Errorf("plan: no model client: %w", err)
	}

	completion, err := client.Complete(ctx, modelclient.CompleteRequest{
		Messages: []modelclient.Message{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userPrompt(c, spec.Content)},
		},
		MaxTokens: maxPlanTokens,
	})
	if err != nil {
		// Deliberately records nothing. A provider outage is not a failed
		// plan, and an empty artifact would send the test-writer off a
		// document the model never produced.
		return worker.Evidence{}, fmt.Errorf("plan: %w", err)
	}

	text := strings.TrimSpace(completion.Text)
	if text == "" {
		return worker.Evidence{}, errors.New("plan: the model returned no plan")
	}

	if strings.HasPrefix(text, insufficientPrefix) {
		reason := strings.TrimSpace(strings.TrimPrefix(text, insufficientPrefix))
		reason = strings.TrimPrefix(reason, ":")
		if _, err := s.artifacts.PutArtifact(ctx, store.Artifact{
			CardID:      c.ID,
			Type:        store.ArtifactFailureSummary,
			Actor:       res.ProviderName,
			Model:       completion.Model,
			ContentType: "text/markdown",
			Content:     text,
		}); err != nil {
			return worker.Evidence{}, fmt.Errorf("plan: recording the declaration: %w", err)
		}
		return worker.Evidence{
			Summary:   "planner declared the specification insufficient: " + strings.TrimSpace(reason),
			NextState: card.NeedsHuman,
		}, nil
	}

	if _, err := s.artifacts.PutArtifact(ctx, store.Artifact{
		CardID:      c.ID,
		Type:        store.ArtifactImplementationPlan,
		Actor:       res.ProviderName,
		Model:       completion.Model,
		ContentType: "text/markdown",
		Content:     text,
	}); err != nil {
		return worker.Evidence{}, fmt.Errorf("plan: storing the plan: %w", err)
	}

	return worker.Evidence{
		Summary:   "implementation plan written",
		NextPhase: card.PhaseTests,
	}, nil
}

// systemPrompt states what §11.1 requires of a plan, and the one thing it
// forbids.
const systemPrompt = `You are planning one unit of engineering work.

Produce an implementation plan that:
- maps each acceptance criterion to the work that satisfies it
- names the files likely to change
- identifies migrations and interface changes
- gives the command that verifies each criterion
- identifies dependencies and calls out risk

Do not change the scope of the specification. Do not guess at anything it
leaves unstated.

If the specification does not contain enough to plan from, reply with exactly
"SPEC_INSUFFICIENT: " followed by what is missing, and nothing else. Declaring
this is always better than planning around a gap.

Reply with the plan itself. No preamble, and no account of your reasoning.`

// userPrompt carries §11.1's inputs: the specification, the repository and the
// branch the work starts from.
func userPrompt(c *card.Card, spec string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Card\n\n- Title: %s\n- ID: %s\n", strings.TrimSpace(c.Title), c.ID)
	if c.RepoURL != nil && *c.RepoURL != "" {
		fmt.Fprintf(&b, "- Repository: %s\n", *c.RepoURL)
	}
	if c.RepoBaseRef != nil && *c.RepoBaseRef != "" {
		fmt.Fprintf(&b, "- Base ref: %s\n", *c.RepoBaseRef)
	}
	fmt.Fprintf(&b, "\n# Specification\n\n%s\n", strings.TrimSpace(spec))
	return b.String()
}
