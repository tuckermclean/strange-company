// Package ghverify answers the §11.3 and §19 gates from GitHub Actions.
//
// A repository that standardises on Actions already declares its tests there.
// Asking it to also commit a test-command script is a second copy of the same
// fact -- and the copy that drifts, because nobody edits it when they edit the
// workflow. This reads the checks CI already produced instead.
package ghverify

import (
	"context"
	"log/slog"
	"time"

	"github.com/tuckermclean/strange-company/control-plane/internal/codingrun"
	"github.com/tuckermclean/strange-company/control-plane/internal/redgate"
)

// Checks reads a ref's check runs.
type Checks interface {
	ChecksFor(ctx context.Context, repository, ref string) (redgate.RunOutcome, error)
}

// Verifier answers a gate from a ref's checks.
type Verifier struct {
	checks Checks
	poll   time.Duration
	wait   time.Duration
	log    *slog.Logger
}

// New builds a Verifier. poll is how often the checks are re-read; wait bounds
// how long a ref's checks may take before the answer is "not yet".
func New(c Checks, poll, wait time.Duration, log *slog.Logger) *Verifier {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	if poll <= 0 {
		poll = 15 * time.Second
	}
	if wait <= 0 {
		wait = 20 * time.Minute
	}
	return &Verifier{checks: c, poll: poll, wait: wait, log: log}
}

// Verify reports whether a ref's checks passed, in the shape the gates consume.
//
// It waits for the checks to finish. CI takes minutes, and reading "still
// running" as a verdict would have redgate call every attempt inconclusive --
// a card that stalls forever while its tests are perfectly healthy.
//
// A ref whose checks never finish stays incomplete rather than becoming a
// failure. Waiting past the budget would hold the card's lease; inventing a
// verdict would blame the work for CI being slow.
func (v *Verifier) Verify(ctx context.Context, req codingrun.VerifyRequest) (redgate.RunOutcome, error) {
	deadline := time.Now().Add(v.wait)
	ticker := time.NewTicker(v.poll)
	defer ticker.Stop()

	for {
		out, err := v.checks.ChecksFor(ctx, req.Repository, req.Ref)
		if err != nil {
			// Including ErrNoChecks, which the caller must see: no checks
			// at all looks exactly like nothing failing.
			return redgate.RunOutcome{}, err
		}
		if out.Completed {
			return out, nil
		}

		if time.Now().After(deadline) {
			v.log.Warn("checks did not finish within the budget",
				"repository", req.Repository, "ref", req.Ref, "waited", v.wait)
			return redgate.RunOutcome{}, nil
		}

		select {
		case <-ctx.Done():
			return redgate.RunOutcome{}, nil
		case <-ticker.C:
		}
	}
}
