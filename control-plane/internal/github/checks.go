package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/tuckermclean/strange-company/control-plane/internal/redgate"
)

// ErrNoChecks means a ref has no check runs at all.
//
// The most dangerous case, because it looks like nothing failed. In practice it
// almost always means the repository's workflows do not trigger on `agent/*`
// branches, and treating it as green would promote work that nothing verified.
var ErrNoChecks = errors.New("github: this ref has no check runs; does the workflow trigger on agent/* branches?")

// checkRun is the subset of a check run the gates need.
//
// VERIFIED against the REST API: GET /repos/{owner}/{repo}/commits/{ref}/check-runs,
// where status is one of queued, in_progress, completed, waiting, requested,
// pending, and conclusion is success, failure, neutral, cancelled, skipped,
// timed_out, action_required, or null.
type checkRun struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
}

// ChecksFor reports whether a ref's checks passed, in the shape the §11.3 gate
// consumes.
//
// Returning redgate.RunOutcome is the point: the gate compares a baseline ref
// with a candidate ref and does not care whether the answer came from a script
// in the repository or from CI. Using CI means the tests are whatever the
// repository already says they are, with no second copy to drift.
func (c *Client) ChecksFor(ctx context.Context, repository, ref string) (redgate.RunOutcome, error) {
	owner, name, err := splitRepository(repository)
	if err != nil {
		return redgate.RunOutcome{}, err
	}

	path := fmt.Sprintf("/repos/%s/%s/commits/%s/check-runs",
		url.PathEscape(owner), url.PathEscape(name), url.PathEscape(ref))
	body, err := c.request(ctx, http.MethodGet, path, nil)
	if err != nil {
		return redgate.RunOutcome{}, err
	}

	var parsed struct {
		TotalCount int        `json:"total_count"`
		CheckRuns  []checkRun `json:"check_runs"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return redgate.RunOutcome{}, fmt.Errorf("github: decoding check runs: %w", err)
	}

	if len(parsed.CheckRuns) == 0 {
		return redgate.RunOutcome{}, fmt.Errorf("%w (%s at %s)", ErrNoChecks, repository, ref)
	}

	failed := false
	for _, run := range parsed.CheckRuns {
		if run.Status != "completed" {
			// Still running. Not a verdict, and redgate reads an
			// incomplete outcome as inconclusive rather than guessing.
			return redgate.RunOutcome{}, nil
		}
		switch run.Conclusion {
		case "success", "neutral", "skipped":
			// Not a failure. A lint job that had nothing to look at
			// must not fail a card.
		case "failure", "timed_out", "action_required":
			failed = true
		default:
			// "cancelled", and anything GitHub adds later. A cancelled
			// check says nothing about the code, and reading it as
			// failure would blame the work for someone pressing stop.
			return redgate.RunOutcome{}, nil
		}
	}

	out := redgate.RunOutcome{Completed: true}
	if failed {
		out.ExitCode = 1
	}
	return out, nil
}
