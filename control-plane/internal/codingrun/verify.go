package codingrun

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/tuckermclean/strange-company/control-plane/internal/jobs"
	"github.com/tuckermclean/strange-company/control-plane/internal/kube"
	"github.com/tuckermclean/strange-company/control-plane/internal/policy"
	"github.com/tuckermclean/strange-company/control-plane/internal/redgate"
)

// TestCommandPath is the file the test-writing phase must commit: an
// executable script that runs this repository's tests.
//
// A file in the repository rather than configuration here, because §11.2 makes
// the test command an OUTPUT of the test-writing phase. The alternative is
// this control plane guessing a language and a runner for every repository it
// is ever pointed at, which is exactly the kind of guess §11.1 forbids
// elsewhere.
const TestCommandPath = ".strange-company/test-command"

// ExitNoTestCommand is what a verification run exits when the script is
// missing.
//
// Distinct from 0 and 1 so a missing test command is distinguishable from a
// failing suite: one is a malformed test phase, the other is the red gate
// working. 64 is EX_USAGE from sysexits.h -- conventional for "the caller set
// this up wrong".
const ExitNoTestCommand = 64

// VerifyRequest is one execution of a repository's tests.
//
// It carries what BOTH verification backends need. The script backend clones
// and runs; the GitHub Actions backend reads the checks CI already produced for
// a ref. Repository and Ref are for the latter, and are ignored by the former.
type VerifyRequest struct {
	CardID  string
	RunID   string

	// Repository is "owner/name", and Ref is the commit or branch whose
	// checks answer the question. Used by the GitHub Actions backend.
	Repository string
	Ref        string

	RepoURL string
	BaseRef string
	Branch  string
	Phase   string
	Attempt int

	GitToken    *policy.CredentialRef
	GitUsername string

	CPULimit    string
	MemoryLimit string
	Timeout     time.Duration
}

// VerifyScript is the shell the verification Job runs.
//
// It carries no model, no harness and no interpretation: §11.3 requires the
// runner to execute the tests and report, and everything that decides anything
// lives in internal/redgate.
func VerifyScript() string {
	return fmt.Sprintf(`set -eu
if [ ! -f %[1]s ]; then
  echo "no %[1]s in this repository; the test-writing phase must commit one" >&2
  exit %[2]d
fi
sh %[1]s`, TestCommandPath, ExitNoTestCommand)
}

// Verify runs a repository's tests and reports the outcome.
//
// It returns redgate.RunOutcome directly, because that is the only thing that
// consumes it and the honesty of Completed is what lets the gate distinguish
// "the tests failed" from "the run never happened" (§12.1).
func (s *Service) Verify(ctx context.Context, req VerifyRequest) (redgate.RunOutcome, error) {
	timeout := req.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}

	job, err := jobs.Build(jobs.Spec{
		CardID: req.CardID, RunID: req.RunID, Namespace: s.namespace,
		Image: s.image, Harness: "verification", Model: "none",
		RepoURL: req.RepoURL, Branch: req.Branch, BaseRef: req.BaseRef,
		Command: []string{"sh", "-c", VerifyScript()},
		Phase:   req.Phase, Attempt: req.Attempt,
		CommitSummary: "verification run (no changes expected)",
		GitToken:      req.GitToken, GitUsername: req.GitUsername,
		CPULimit: req.CPULimit, MemoryLimit: req.MemoryLimit,
		Timeout: timeout,
	})
	if err != nil {
		return redgate.RunOutcome{}, fmt.Errorf("codingrun: building the verification job: %w", err)
	}

	if err := s.api.CreateJob(ctx, s.namespace, job); err != nil && !errors.Is(err, kube.ErrAlreadyExists) {
		return redgate.RunOutcome{}, fmt.Errorf("codingrun: creating the verification job: %w", err)
	}

	defer func() {
		if err := s.api.DeleteJob(context.WithoutCancel(ctx), s.namespace, req.RunID); err != nil {
			s.log.Warn("could not delete the verification job", "run_id", req.RunID, "error", err)
		}
	}()

	phase, err := s.wait(ctx, req.RunID)
	if err != nil {
		// Completed stays false. redgate.Evaluate reads that as
		// inconclusive, which is the honest answer: a run that never
		// finished says nothing about the tests.
		s.log.Warn("verification run did not complete", "run_id", req.RunID, "error", err)
		return redgate.RunOutcome{}, nil
	}

	// A Job's status carries no exit code, only success or failure. That is
	// all either gate needs: redgate compares outcomes, and 1 is a
	// stand-in for "non-zero" rather than a claim about which code it was.
	out := redgate.RunOutcome{Completed: true}
	if phase == kube.JobFailed {
		out.ExitCode = 1
	}
	return out, nil
}
