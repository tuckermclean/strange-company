// Package codingrun runs one coding Job and collects its result.
//
// It is the piece between internal/jobs, which builds a hardened Job
// manifest, and internal/runner, whose adapters parse a harness's output into
// a CodingRunResult. Neither had ever been connected to the other, so the
// coding runner M3 delivered had never actually run.
//
// The classification here matters more than the plumbing. Spec §12.1 counts an
// implementation attempt only when the agent did work, the runner regained
// control, verification ran, and verification failed. Everything else -- a run
// that never finished, logs that cannot be read, a Job that would not schedule
// -- is infrastructure, and calling any of it a failed attempt burns a rung of
// the escalation ladder on a problem no model was asked to solve.
package codingrun

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/tuckermclean/strange-company/control-plane/internal/jobs"
	"github.com/tuckermclean/strange-company/control-plane/internal/kube"
	"github.com/tuckermclean/strange-company/control-plane/internal/policy"
	"github.com/tuckermclean/strange-company/control-plane/internal/runner"
)

// JobAPI is the part of Kubernetes this package uses.
type JobAPI interface {
	CreateJob(ctx context.Context, namespace string, job any) error
	DeleteJob(ctx context.Context, namespace, name string) error
	JobStatus(ctx context.Context, namespace, name string) (kube.JobPhase, error)
	PodLogs(ctx context.Context, namespace, jobName string) ([]byte, error)
}

// Request is one coding run.
type Request struct {
	CardID string
	RunID  string
	Task   string

	// Resolution names the harness, model and credentials. This package
	// never chooses any of them.
	Resolution *policy.Resolution

	RepoURL string
	BaseRef string
	Branch  string

	// AllowedTools is the card's §24 allowlist, passed to the harness.
	AllowedTools []string

	Phase   string
	Attempt int

	GitToken       *policy.CredentialRef
	GitUsername    string
	GitAuthorName  string
	GitAuthorEmail string

	CPULimit    string
	MemoryLimit string
	Timeout     time.Duration
}

// Service runs coding Jobs.
type Service struct {
	api       JobAPI
	namespace string
	image     string
	poll      time.Duration
	log       *slog.Logger

	adapters map[string]runner.Adapter
}

// New builds a Service. poll is how often a running Job's status is checked.
func New(api JobAPI, namespace, image string, poll time.Duration, log *slog.Logger) *Service {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	if poll <= 0 {
		poll = 5 * time.Second
	}
	return &Service{
		api: api, namespace: namespace, image: image, poll: poll, log: log,
		adapters: map[string]runner.Adapter{
			"claude-code": runner.ClaudeCodeAdapter{},
			"codex":       runner.CodexAdapter{},
		},
	}
}

// defaultTimeout bounds a run that named none.
const defaultTimeout = 30 * time.Minute

// Run performs one coding run and returns its result.
//
// An infrastructure problem comes back as a result with an infrastructure
// status, not as an error: the caller has to record it either way, and §12.1's
// counters distinguish the two. An error is reserved for a request this
// package cannot act on at all.
func (s *Service) Run(ctx context.Context, req Request) (*runner.CodingRunResult, error) {
	harness := string(req.Resolution.Harness)
	adapter, ok := s.adapters[harness]
	if !ok {
		// Refused before anything launches: a Job whose output nothing can
		// parse is a model call spent for nothing.
		return nil, fmt.Errorf("codingrun: no adapter for harness %q", harness)
	}

	timeout := req.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}

	argv := adapter.Command(runner.Request{
		Task:         req.Task,
		Model:        req.Resolution.Model,
		AllowedTools: req.AllowedTools,
	})

	job, err := jobs.Build(jobs.Spec{
		CardID: req.CardID, RunID: req.RunID, Namespace: s.namespace,
		Image: s.image, Harness: harness, Model: req.Resolution.Model,
		RepoURL: req.RepoURL, Branch: req.Branch, BaseRef: req.BaseRef,
		Command: argv, Phase: req.Phase, Attempt: req.Attempt,
		GitToken: req.GitToken, GitUsername: req.GitUsername,
		GitAuthorName: req.GitAuthorName, GitAuthorEmail: req.GitAuthorEmail,
		Env: req.Resolution.Env, PlainEnv: req.Resolution.PlainEnv,
		CPULimit: req.CPULimit, MemoryLimit: req.MemoryLimit,
		Timeout: timeout,
	})
	if err != nil {
		return nil, fmt.Errorf("codingrun: building the job: %w", err)
	}

	started := time.Now()

	if err := s.api.CreateJob(ctx, s.namespace, job); err != nil {
		// An existing Job for this run id IS this run: the id is stable, so
		// a create whose response was lost must be adopted rather than
		// starting the same work twice.
		if !errors.Is(err, kube.ErrAlreadyExists) {
			s.log.Error("could not create the coding job", "run_id", req.RunID, "error", err)
			return infra(req, harness, started, fmt.Sprintf("could not create the job: %v", err)), nil
		}
		s.log.Info("adopting an existing job for this run", "run_id", req.RunID)
	}

	// Cleaned up once the output is collected: the logs and the parsed
	// result are recorded as artifacts, and Jobs left behind fill the
	// namespace with completed pods nobody reads.
	defer func() {
		if err := s.api.DeleteJob(context.WithoutCancel(ctx), s.namespace, req.RunID); err != nil {
			s.log.Warn("could not delete the coding job", "run_id", req.RunID, "error", err)
		}
	}()

	phase, err := s.wait(ctx, req.RunID)
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return timedOut(req, harness, started), nil
	case err != nil:
		return infra(req, harness, started, fmt.Sprintf("could not read the job's status: %v", err)), nil
	}

	logs, err := s.api.PodLogs(ctx, s.namespace, req.RunID)
	if err != nil {
		// The run may well have done the work. Calling this a failed
		// attempt would be a guess about something we cannot see.
		return infra(req, harness, started, fmt.Sprintf("could not read the run's logs: %v", err)), nil
	}

	exitCode := 0
	if phase == kube.JobFailed {
		exitCode = 1
	}

	result, err := adapter.Parse(logs, exitCode, time.Since(started))
	if err != nil {
		// Unparseable output is not a verdict. §12.1's attempt counter must
		// not move on a stream we could not read.
		return infra(req, harness, started, fmt.Sprintf("could not parse the run's output: %v", err)), nil
	}
	return result, nil
}

// wait polls until the Job stops running or ctx ends.
func (s *Service) wait(ctx context.Context, name string) (kube.JobPhase, error) {
	ticker := time.NewTicker(s.poll)
	defer ticker.Stop()

	for {
		phase, err := s.api.JobStatus(ctx, s.namespace, name)
		if err != nil {
			return "", err
		}
		if phase == kube.JobSucceeded || phase == kube.JobFailed {
			return phase, nil
		}

		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-ticker.C:
		}
	}
}

func infra(req Request, harness string, started time.Time, summary string) *runner.CodingRunResult {
	return &runner.CodingRunResult{
		Status: runner.StatusInfraError, Harness: harness,
		Model: req.Resolution.Model, Summary: summary,
		Duration: time.Since(started),
	}
}

func timedOut(req Request, harness string, started time.Time) *runner.CodingRunResult {
	return &runner.CodingRunResult{
		Status: runner.StatusTimeout, Harness: harness,
		Model:    req.Resolution.Model,
		Summary:  "the run did not finish within its wall-clock budget",
		Duration: time.Since(started),
	}
}
