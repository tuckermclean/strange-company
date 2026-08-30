// Package onboard performs day-0 setup on a repository.
//
// Day-0 is deliberately not something an agent can do. The runner refuses to
// commit any change touching .github/workflows, because an agent able to
// rewrite CI can weaken the very checks that gate it -- so preparing a
// repository has to happen from outside the loop, with a credential the agents
// never see.
//
// That credential needs GitHub's `workflow` scope, which is more power than
// anything else this system holds. It is optional: without it, importing is
// simply not offered and an operator prepares repositories by hand.
package onboard

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/tuckermclean/strange-company/control-plane/internal/github"
)

// workflowPath is where the gate's workflow goes. Under .github/workflows,
// which is exactly the path agents are forbidden from touching.
const workflowPath = ".github/workflows/agent.yml"

// importBranch is the branch a day-0 pull request comes from.
const importBranch = "strange-company/day-0"

// Result reports what an import did and what is still missing.
//
// Separated on purpose: an import that did nothing because everything was
// already in place should read differently from one that failed, and both
// differently from one that succeeded but still needs a human to merge.
type Result struct {
	Repository    string   `json:"repository"`
	LabelCreated  bool     `json:"label_created"`
	LabelExisted  bool     `json:"label_existed"`
	WorkflowFound bool     `json:"workflow_already_present"`
	PullRequest   string   `json:"pull_request,omitempty"`
	DefaultBranch string   `json:"default_branch,omitempty"`
	Remaining     []string `json:"remaining"`
}

// Client is the GitHub surface day-0 needs.
type Client interface {
	EnsureLabel(ctx context.Context, repository, name, colour, description string) error
	DefaultBranch(ctx context.Context, repository string) (string, error)
	FileExists(ctx context.Context, repository, path, ref string) (bool, error)
	CreateBranch(ctx context.Context, repository, branch, fromRef string) error
	PutFile(ctx context.Context, repository, path, branch, message string, content []byte) error
	EnsurePullRequest(ctx context.Context, pr github.PullRequest) (*github.OpenPullRequest, error)
}

// Importer prepares repositories.
type Importer struct {
	gh       Client
	label    string
	workflow []byte
	log      *slog.Logger
}

// New builds an Importer. workflow is the CI definition to propose; label is
// the intake label the ingester watches.
func New(gh Client, label string, workflow []byte, log *slog.Logger) *Importer {
	if log == nil {
		log = slog.Default()
	}
	if len(workflow) == 0 {
		workflow = []byte(DefaultWorkflow)
	}
	return &Importer{gh: gh, label: label, workflow: workflow, log: log}
}

// Import prepares repository and reports what a human still has to do.
//
// Everything here is idempotent: importing twice is safe, and is the ordinary
// case when an operator re-runs it after merging the pull request.
func (i *Importer) Import(ctx context.Context, repository string) (*Result, error) {
	res := &Result{Repository: repository}

	switch err := i.gh.EnsureLabel(ctx, repository, i.label, "0e8a16",
		"Cards the autonomous engine may pick up"); {
	case err == nil:
		res.LabelCreated = true
	case errors.Is(err, github.ErrLabelExists):
		res.LabelExisted = true
	default:
		return nil, fmt.Errorf("onboard: creating the %q label: %w", i.label, err)
	}

	base, err := i.gh.DefaultBranch(ctx, repository)
	if err != nil {
		return nil, fmt.Errorf("onboard: reading the default branch: %w", err)
	}
	res.DefaultBranch = base

	// A repository that already gates agent branches has made a decision.
	// Replacing working CI with a template during an import would be the
	// worst thing this could do.
	present, err := i.gh.FileExists(ctx, repository, workflowPath, base)
	if err != nil {
		return nil, fmt.Errorf("onboard: looking for %s: %w", workflowPath, err)
	}
	if present {
		res.WorkflowFound = true
		res.Remaining = append(res.Remaining,
			fmt.Sprintf("%s already exists; confirm it runs on agent/** or the red and green gates have nothing to read", workflowPath))
		return res, nil
	}

	if err := i.gh.CreateBranch(ctx, repository, importBranch, base); err != nil {
		return nil, fmt.Errorf("onboard: creating %s: %w", importBranch, err)
	}
	if err := i.gh.PutFile(ctx, repository, workflowPath, importBranch,
		"ci: run tests on agent branches", i.workflow); err != nil {
		return nil, fmt.Errorf("onboard: writing %s (does the day-0 credential carry the workflow scope?): %w", workflowPath, err)
	}

	// A pull request rather than a push. §19 makes the human the final merge
	// authority, and there is no reason day-0 should be the one place that
	// stops being true -- least of all for a change to the checks themselves.
	pr, err := i.gh.EnsurePullRequest(ctx, github.PullRequest{
		Repository: repository,
		Head:       importBranch,
		Base:       base,
		Title:      "Run tests on agent branches",
		Body:       pullRequestBody,
	})
	if err != nil {
		return nil, fmt.Errorf("onboard: opening the day-0 pull request: %w", err)
	}
	res.PullRequest = pr.URL
	res.Remaining = append(res.Remaining,
		"merge the day-0 pull request; until then agent branches produce no checks and every card stalls at its red gate",
		fmt.Sprintf("add %q to GITHUB_REPOSITORIES so the ingester watches it", repository))

	return res, nil
}
