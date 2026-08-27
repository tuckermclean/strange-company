// Package ingest turns externally-sourced work items into cards.
//
// Spec §25: an issue labelled `agent-ready` becomes a board card. This runs on
// the reconcile timer rather than on a webhook, for the reason §4.3 gives
// about Vikunja -- do not assume a delivery worked, reconcile until it has.
// A webhook can make it faster later; it cannot make it correct.
package ingest

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"
	"github.com/tuckermclean/strange-company/control-plane/internal/github"
	"github.com/tuckermclean/strange-company/control-plane/internal/store"
)

// sourceType is what these cards record as their origin.
const sourceType = "github"

// Source lists the eligible work items in a repository.
type Source interface {
	ListLabeledIssues(ctx context.Context, repository, label string) ([]github.Issue, error)
}

// Board is the part of the store this package writes.
type Board interface {
	UpsertSourceCard(ctx context.Context, in store.SourceCard) (uuid.UUID, bool, error)
}

// Result summarises one pass, for the operator's log.
type Result struct {
	Seen    int
	Created int
	Updated int
	Failed  int
}

// Reconciler ingests labelled issues from a fixed set of repositories.
type Reconciler struct {
	source Source
	board  Board
	repos  []string
	label  string

	// actions is the allowlist stamped onto every card this creates.
	// Without one, §10's gate refuses the card forever and it can never be
	// promoted -- so a card created without an allowlist is a card that can
	// never be worked on.
	actions []byte

	log *slog.Logger
}

// New builds a Reconciler.
func New(s Source, b Board, repositories []string, label string, actions []byte, log *slog.Logger) *Reconciler {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Reconciler{source: s, board: b, repos: repositories, label: label, actions: actions, log: log}
}

// RunOnce ingests every eligible issue once.
//
// Failures are counted and logged, never returned: one unreachable repository
// must not stop the others, and a card that could not be written is retried on
// the next pass. An error would abort the pass and lose the repositories after
// the failure.
func (r *Reconciler) RunOnce(ctx context.Context) (Result, error) {
	var res Result

	for _, repo := range r.repos {
		if strings.TrimSpace(repo) == "" {
			continue
		}

		issues, err := r.source.ListLabeledIssues(ctx, repo, r.label)
		if err != nil {
			r.log.Error("could not read issues", "repository", repo, "label", r.label, "error", err)
			res.Failed++
			continue
		}

		for _, issue := range issues {
			res.Seen++

			_, created, err := r.board.UpsertSourceCard(ctx, cardFor(issue, r.actions))
			if err != nil {
				r.log.Error("could not ingest issue",
					"repository", repo, "issue", issue.Number, "error", err)
				res.Failed++
				continue
			}
			if created {
				res.Created++
				r.log.Info("ingested a new issue",
					"repository", repo, "issue", issue.Number, "title", issue.Title)
				continue
			}
			res.Updated++
		}
	}

	return res, nil
}

// cardFor maps an issue onto what the source owns of a card.
//
// The issue body becomes the specification. That is the whole §10 pipeline's
// input: the deterministic gate reads it, screening reads it, and a human
// rewrites it in conversation when it is not enough.
func cardFor(i github.Issue, actions []byte) store.SourceCard {
	return store.SourceCard{
		SourceType: sourceType,
		ExternalID: i.ExternalID(),
		URL:        i.HTMLURL,
		Title:      i.Title,
		Body:       i.Body,

		// The coding runner has to know what to clone; an ingested card
		// with no repository is a card nothing can act on.
		RepoURL: fmt.Sprintf("https://github.com/%s", i.Repository),

		PermittedActions: actions,
	}
}
