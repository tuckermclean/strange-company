package codingrun_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
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
	lastJob      any
	statusNames  []string
	deletedNames []string
	logNames     []string
}

// createdName is the object name the manifest actually carried.
func (f *fakeAPI) createdName() string {
	b, _ := json.Marshal(f.lastJob)
	var parsed struct {
		Metadata struct {
			Name string `json:"name"`
		} `json:"metadata"`
	}
	_ = json.Unmarshal(b, &parsed)
	return parsed.Metadata.Name
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
func (f *fakeAPI) DeleteJob(_ context.Context, _, name string) error {
	f.deleted++
	f.deletedNames = append(f.deletedNames, name)
	return nil
}
func (f *fakeAPI) JobStatus(_ context.Context, _, name string) (kube.JobPhase, error) {
	f.statusNames = append(f.statusNames, name)
	p := f.phases[f.phaseIdx]
	if f.phaseIdx < len(f.phases)-1 {
		f.phaseIdx++
	}
	return p, nil
}
func (f *fakeAPI) PodLogs(_ context.Context, _, name string) ([]byte, error) {
	f.logNames = append(f.logNames, name)
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

// opencode addresses a model as provider/model and needs to be told where the
// provider lives. The key travels by NAME so the secret reaches opencode
// through the environment Kubernetes injected, never through a config file
// this code wrote or a command line anyone in the container can read.
func TestAnOpenCodeRunCarriesItsProviderConfig(t *testing.T) {
	api := &fakeAPI{phases: []kube.JobPhase{kube.JobSucceeded}, logs: `{"type":"text","text":"done"}`}
	req := request()
	req.Resolution = &policy.Resolution{
		Phase: "implementation", Alias: "implement-cheap", ProviderName: "deepseek",
		Model: "deepseek-v4-pro", Harness: policy.Harness("opencode"),
		BaseURL: "https://api.deepseek.com",
		Env:     map[string]policy.CredentialRef{"DEEPSEEK_API_KEY": {Secret: "deepseek-credentials", Key: "api-key"}},
	}

	if _, err := service(api).Run(context.Background(), req); err != nil {
		t.Fatalf("Run: %v", err)
	}

	sent := api.lastArgv()
	for _, want := range []string{
		"SC_OPENCODE_PROVIDER", "deepseek",
		"SC_OPENCODE_BASE_URL", "https://api.deepseek.com",
		"SC_OPENCODE_API_KEY_ENV", "DEEPSEEK_API_KEY",
		"deepseek/deepseek-v4-pro",
	} {
		if !strings.Contains(sent, want) {
			t.Errorf("the job does not carry %q", want)
		}
	}
	// The key's VALUE must never appear in the manifest.
	if strings.Contains(sent, "sk-") {
		t.Error("a credential value reached the job manifest")
	}
}

// opencode cannot reach a provider it has no address for, and that is a policy
// mistake rather than a transient one.
func TestAnOpenCodeProviderWithoutABaseURLIsRefused(t *testing.T) {
	api := &fakeAPI{phases: []kube.JobPhase{kube.JobSucceeded}}
	req := request()
	req.Resolution = &policy.Resolution{
		ProviderName: "deepseek", Model: "m", Harness: policy.Harness("opencode"),
	}

	_, err := service(api).Run(context.Background(), req)
	if !errors.Is(err, codingrun.ErrNoAdapter) {
		t.Fatalf("error = %v, want ErrNoAdapter", err)
	}
	if api.created != 0 {
		t.Fatal("launched a job opencode could not have used")
	}
}

// jobs.Build slugifies and prefixes the object name ("coding-<run id>").
// Polling the raw run id asked Kubernetes about an object that never existed,
// so a perfectly healthy run came back 404 and was classified infra_error --
// on every card, forever, with the pod sitting there having done the work.
func TestTheJobIsPolledByTheNameItWasCreatedWith(t *testing.T) {
	api := &fakeAPI{phases: []kube.JobPhase{kube.JobSucceeded}, logs: claudeResult}

	if _, err := service(api).Run(context.Background(), request()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if api.statusNames == nil {
		t.Fatal("never polled for status")
	}
	created := api.createdName()
	for _, asked := range append(api.statusNames, api.deletedNames...) {
		if asked != created {
			t.Fatalf("created %q but asked about %q", created, asked)
		}
	}
	if api.logNames[0] != created {
		t.Fatalf("read logs for %q, created %q", api.logNames[0], created)
	}
}
