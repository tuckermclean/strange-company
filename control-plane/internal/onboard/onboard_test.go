package onboard_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/tuckermclean/strange-company/control-plane/internal/github"
	"github.com/tuckermclean/strange-company/control-plane/internal/onboard"
)

type fakeGH struct {
	labelErr   error
	exists     bool
	existsErr  error
	putErr     error
	files      map[string][]byte
	branches   []string
	pr         *github.OpenPullRequest
	labelCalls int
}

func (f *fakeGH) EnsureLabel(context.Context, string, string, string, string) error {
	f.labelCalls++
	return f.labelErr
}
func (f *fakeGH) DefaultBranch(context.Context, string) (string, error) { return "main", nil }
func (f *fakeGH) FileExists(context.Context, string, string, string) (bool, error) {
	return f.exists, f.existsErr
}
func (f *fakeGH) CreateBranch(_ context.Context, _, branch, _ string) error {
	f.branches = append(f.branches, branch)
	return nil
}
func (f *fakeGH) PutFile(_ context.Context, _, path, _, _ string, content []byte) error {
	if f.putErr != nil {
		return f.putErr
	}
	if f.files == nil {
		f.files = map[string][]byte{}
	}
	f.files[path] = content
	return nil
}
func (f *fakeGH) EnsurePullRequest(context.Context, github.PullRequest) (*github.OpenPullRequest, error) {
	if f.pr == nil {
		f.pr = &github.OpenPullRequest{Number: 1, URL: "https://github.com/o/r/pull/1"}
	}
	return f.pr, nil
}

func importer(gh onboard.Client) *onboard.Importer {
	return onboard.New(gh, "agent-ready", nil, nil)
}

// The whole point: a repository that has never seen this engine gets a label
// and a proposed workflow, and is told what a human still has to do.
func TestImportingAFreshRepositoryProposesTheGate(t *testing.T) {
	gh := &fakeGH{}

	res, err := importer(gh).Import(context.Background(), "o/r")
	if err != nil {
		t.Fatalf("Import: %v", err)
	}

	if !res.LabelCreated {
		t.Error("no intake label was created")
	}
	if res.PullRequest == "" {
		t.Fatal("no pull request was opened")
	}
	wf := string(gh.files[".github/workflows/agent.yml"])
	if !strings.Contains(wf, "agent/**") {
		t.Errorf("the proposed workflow does not run on agent branches:\n%s", wf)
	}
	// The baseline matters: a suite already failing before the agent touched
	// anything is not evidence of anything the agent did.
	if !strings.Contains(wf, "main") {
		t.Error("the proposed workflow does not run on the default branch, so the red gate has no baseline")
	}
	if len(res.Remaining) == 0 {
		t.Error("the import reports nothing left for a human to do, but the PR is unmerged")
	}
}

// A repository that already gates agent branches has made a decision.
// Replacing working CI with a template is the worst thing this could do.
func TestAnExistingWorkflowIsLeftAlone(t *testing.T) {
	gh := &fakeGH{exists: true}

	res, err := importer(gh).Import(context.Background(), "o/r")
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if len(gh.files) != 0 {
		t.Fatalf("an existing workflow was overwritten: %v", gh.files)
	}
	if !res.WorkflowFound {
		t.Error("the result does not say the workflow was already there")
	}
	if len(res.Remaining) == 0 {
		t.Error("nothing told the operator to check that the existing workflow runs on agent/**")
	}
}

// Importing twice is the ordinary case: an operator re-runs it after merging.
func TestImportingTwiceIsSafe(t *testing.T) {
	gh := &fakeGH{labelErr: github.ErrLabelExists}

	res, err := importer(gh).Import(context.Background(), "o/r")
	if err != nil {
		t.Fatalf("a second import failed: %v", err)
	}
	if !res.LabelExisted || res.LabelCreated {
		t.Errorf("an existing label was reported as created: %+v", res)
	}
}

// GitHub refuses a write under .github/workflows without the workflow scope,
// and that refusal is the whole reason day-0 is separate from anything an
// agent may do. The error has to say so.
func TestAMissingWorkflowScopeIsExplained(t *testing.T) {
	gh := &fakeGH{putErr: errors.New("status 403: refusing to allow an OAuth App to create or update workflow")}

	_, err := importer(gh).Import(context.Background(), "o/r")
	if err == nil {
		t.Fatal("Import succeeded without permission to write the workflow")
	}
	if !strings.Contains(err.Error(), "workflow scope") {
		t.Errorf("error = %v; an operator cannot tell which permission is missing", err)
	}
}
