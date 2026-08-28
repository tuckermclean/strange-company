package codingrun_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/tuckermclean/strange-company/control-plane/internal/codingrun"
	"github.com/tuckermclean/strange-company/control-plane/internal/kube"
)

func verifyRequest() codingrun.VerifyRequest {
	return codingrun.VerifyRequest{
		CardID:  "11111111-2222-3333-4444-555555555555",
		RunID:   "verify-1",
		RepoURL: "https://github.com/example/repo",
		BaseRef: "main",
		Branch:  "agent/card-1",
		Phase:   "tests",
		Attempt: 1,
	}
}

// The gates need an exit code from the repository's own tests, and §11.3 is
// explicit that no model grades them -- so this runs a command, not a harness.
func TestAPassingVerificationReportsExitZero(t *testing.T) {
	api := &fakeAPI{phases: []kube.JobPhase{kube.JobSucceeded}}

	out, err := service(api).Verify(context.Background(), verifyRequest())
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !out.Completed || out.ExitCode != 0 {
		t.Fatalf("outcome = %+v", out)
	}
}

func TestAFailingVerificationReportsANonZeroExit(t *testing.T) {
	api := &fakeAPI{phases: []kube.JobPhase{kube.JobFailed}}

	out, err := service(api).Verify(context.Background(), verifyRequest())
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !out.Completed || out.ExitCode == 0 {
		t.Fatalf("outcome = %+v; a failed Job must not read as a passing suite", out)
	}
}

// A run that never finished is not a verdict about the tests. redgate.Evaluate
// treats an incomplete run as inconclusive, and it can only do that if this
// reports honestly rather than defaulting to an exit code.
func TestAnIncompleteVerificationIsNotCompleted(t *testing.T) {
	api := &fakeAPI{phases: []kube.JobPhase{kube.JobRunning}}
	svc := codingrun.New(api, "agent-runs", "runner:1", time.Millisecond, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	out, err := svc.Verify(ctx, verifyRequest())
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if out.Completed {
		t.Fatalf("a run that never finished reported a verdict: %+v", out)
	}
}

// The command comes from a file the test-writing agent commits, so the gates
// run whatever that repository's tests actually are rather than a language
// this control plane guessed at.
func TestTheCommandRunsTheRepositorysOwnTestScript(t *testing.T) {
	api := &fakeAPI{phases: []kube.JobPhase{kube.JobSucceeded}}

	if _, err := service(api).Verify(context.Background(), verifyRequest()); err != nil {
		t.Fatal(err)
	}
	if api.created != 1 {
		t.Fatalf("created %d jobs", api.created)
	}
	if !strings.Contains(api.lastArgv(), codingrun.TestCommandPath) {
		t.Errorf("the job does not run %s: %s", codingrun.TestCommandPath, api.lastArgv())
	}
}

// A missing test script must be distinguishable from a failing suite: one is a
// malformed test phase, the other is a red gate doing its job.
func TestAMissingTestScriptHasItsOwnExitCode(t *testing.T) {
	if codingrun.ExitNoTestCommand == 0 || codingrun.ExitNoTestCommand == 1 {
		t.Fatalf("ExitNoTestCommand = %d; it must not collide with success or an ordinary failure",
			codingrun.ExitNoTestCommand)
	}
	if !strings.Contains(codingrun.VerifyScript(), "exit "+itoa(codingrun.ExitNoTestCommand)) {
		t.Errorf("the script does not exit %d when the command is missing:\n%s",
			codingrun.ExitNoTestCommand, codingrun.VerifyScript())
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
