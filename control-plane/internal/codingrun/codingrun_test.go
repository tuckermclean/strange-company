package codingrun_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/tuckermclean/strange-company/control-plane/internal/codingrun"
	"github.com/tuckermclean/strange-company/control-plane/internal/kube"
	"github.com/tuckermclean/strange-company/control-plane/internal/policy"
	"github.com/tuckermclean/strange-company/control-plane/internal/runner"
)

// A minimal Claude Code stream-json result line, which the real adapter parses.
const claudeResult = `{"type":"result","subtype":"success","is_error":false,` +
	`"duration_ms":1200,"num_turns":3,"total_cost_usd":0.04,` +
	`"usage":{"input_tokens":10,"output_tokens":20},"result":"wrote the tests"}`

type fakeAPI struct {
	created   int
	deleted   int
	phases    []kube.JobPhase
	phaseIdx  int
	logs      string
	createErr error
	logsErr   error
	lastJob   any
}

func (f *fakeAPI) CreateJob(_ context.Context, _ string, job any) error {
	f.created++
	f.lastJob = job
	return f.createErr
}

// lastArgv renders the argv of the last Job created, for tests that care what
// the container was actually told to run.
func (f *fakeAPI) lastArgv() string {
	b, _ := json.Marshal(f.lastJob)
	return string(b)
}
func (f *fakeAPI) DeleteJob(context.Context, string, string) error { f.deleted++; return nil }
func (f *fakeAPI) JobStatus(context.Context, string, string) (kube.JobPhase, error) {
	p := f.phases[f.phaseIdx]
	if f.phaseIdx < len(f.phases)-1 {
		f.phaseIdx++
	}
	return p, nil
}
func (f *fakeAPI) PodLogs(context.Context, string, string) ([]byte, error) {
	if f.logsErr != nil {
		return nil, f.logsErr
	}
	return []byte(f.logs), nil
}

func request() codingrun.Request {
	return codingrun.Request{
		CardID: "11111111-2222-3333-4444-555555555555",
		RunID:  "run-1",
		Task:   "write the acceptance tests",
		Resolution: &policy.Resolution{
			Phase: "tests", Alias: "tests", ProviderName: "anthropic-api",
			Model: "claude-sonnet-5", Harness: policy.Harness("claude-code"),
		},
		RepoURL: "https://github.com/example/repo",
		BaseRef: "main",
		Branch:  "agent/card-1",
		Phase:   "tests",
		Attempt: 1,
	}
}

func service(api *fakeAPI) *codingrun.Service {
	return codingrun.New(api, "agent-runs", "runner:1", time.Millisecond, nil)
}

func TestASucceededRunIsParsedByItsHarnessAdapter(t *testing.T) {
	api := &fakeAPI{phases: []kube.JobPhase{kube.JobRunning, kube.JobSucceeded}, logs: claudeResult}

	res, err := service(api).Run(context.Background(), request())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != runner.StatusCompleted {
		t.Fatalf("status = %q", res.Status)
	}
	if res.Harness != "claude-code" {
		t.Errorf("harness = %q", res.Harness)
	}
	if api.created != 1 {
		t.Errorf("created %d jobs", api.created)
	}
}

// A harness with no adapter cannot be parsed, so launching it would spend a
// model call on output nothing can read.
func TestAnUnknownHarnessNeverLaunchesAJob(t *testing.T) {
	api := &fakeAPI{phases: []kube.JobPhase{kube.JobSucceeded}, logs: claudeResult}
	req := request()
	req.Resolution.Harness = policy.Harness("telepathy")

	if _, err := service(api).Run(context.Background(), req); err == nil {
		t.Fatal("expected an error")
	}
	if api.created != 0 {
		t.Fatal("launched a Job whose output nothing could parse")
	}
}

// §12.1: a run that never finished is an infrastructure failure, not a failed
// implementation attempt. Recording it as an attempt burns a rung of the
// ladder on a problem no model was asked to solve.
func TestARunThatNeverFinishesIsATimeoutNotAFailure(t *testing.T) {
	api := &fakeAPI{phases: []kube.JobPhase{kube.JobRunning}, logs: claudeResult}
	svc := codingrun.New(api, "agent-runs", "runner:1", time.Millisecond, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	res, err := svc.Run(ctx, request())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != runner.StatusTimeout {
		t.Fatalf("status = %q, want timeout", res.Status)
	}
}

// Logs we cannot read are an infrastructure failure too: the run may well have
// done the work, and calling it a failed attempt would be a guess.
func TestUnreadableLogsAreAnInfrastructureFailure(t *testing.T) {
	api := &fakeAPI{phases: []kube.JobPhase{kube.JobSucceeded}, logsErr: errors.New("pod gone")}

	res, err := service(api).Run(context.Background(), request())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != runner.StatusInfraError {
		t.Fatalf("status = %q, want infra_error", res.Status)
	}
}

// A Job that could not even be created did not run. Reporting anything else
// would attribute a scheduling problem to the model.
func TestAJobThatCannotBeCreatedIsAnInfrastructureFailure(t *testing.T) {
	api := &fakeAPI{phases: []kube.JobPhase{kube.JobSucceeded}, createErr: errors.New("forbidden")}

	res, err := service(api).Run(context.Background(), request())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != runner.StatusInfraError {
		t.Fatalf("status = %q", res.Status)
	}
}

// The Job is cleaned up once its output has been collected: the logs and the
// result are already recorded as artifacts, and leaving Jobs behind fills the
// namespace with completed pods nobody reads.
func TestTheJobIsDeletedOnceItsOutputIsCollected(t *testing.T) {
	api := &fakeAPI{phases: []kube.JobPhase{kube.JobSucceeded}, logs: claudeResult}

	if _, err := service(api).Run(context.Background(), request()); err != nil {
		t.Fatal(err)
	}
	if api.deleted != 1 {
		t.Fatalf("deleted %d jobs, want 1", api.deleted)
	}
}

// A create whose response was lost must not launch a second run of the same
// work; the run id is stable, so the existing Job is the run.
func TestAnAlreadyExistingJobIsAdopted(t *testing.T) {
	api := &fakeAPI{
		phases:    []kube.JobPhase{kube.JobSucceeded},
		logs:      claudeResult,
		createErr: kube.ErrAlreadyExists,
	}

	res, err := service(api).Run(context.Background(), request())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != runner.StatusCompleted {
		t.Fatalf("status = %q; an existing Job for this run id was not adopted", res.Status)
	}
}
