package kube_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tuckermclean/strange-company/control-plane/internal/jobs"
	"github.com/tuckermclean/strange-company/control-plane/internal/kube"
)

const token = "sa-token-never-in-an-error"

type apiStub struct {
	requests []*http.Request
	bodies   []string
	status   int
	reply    string
}

func (a *apiStub) client(t *testing.T) *kube.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		a.requests = append(a.requests, r)
		a.bodies = append(a.bodies, string(b))
		if a.status != 0 {
			w.WriteHeader(a.status)
		}
		_, _ = io.WriteString(w, a.reply)
	}))
	t.Cleanup(srv.Close)

	c, err := kube.New(srv.URL, token, srv.Client())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func aJob(t *testing.T) *jobs.Job {
	t.Helper()
	j, err := jobs.Build(jobs.Spec{
		CardID: "card-1", RunID: "run-1", Namespace: "agent-runs",
		Image: "runner:1", Harness: "claude-code", Model: "m",
		RepoURL: "https://github.com/example/repo", Branch: "agent/card-1",
		BaseRef: "main", Command: []string{"claude"}, Timeout: 60_000_000_000,
		Phase: "implementation", Attempt: 1,
	})
	if err != nil {
		t.Fatalf("jobs.Build: %v", err)
	}
	return j
}

func TestCreateJobPostsToTheNamespacedEndpoint(t *testing.T) {
	stub := &apiStub{status: http.StatusCreated, reply: `{"metadata":{"name":"run-1"}}`}
	c := stub.client(t)

	if err := c.CreateJob(context.Background(), "agent-runs", aJob(t)); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	req := stub.requests[0]
	if req.Method != http.MethodPost {
		t.Errorf("method = %s", req.Method)
	}
	if req.URL.Path != "/apis/batch/v1/namespaces/agent-runs/jobs" {
		t.Errorf("path = %s", req.URL.Path)
	}
	if req.Header.Get("Authorization") != "Bearer "+token {
		t.Errorf("authorization = %q", req.Header.Get("Authorization"))
	}
	// The manifest goes over the wire as sent, with §16.1's hardening
	// intact -- a client that re-marshalled a subset could silently drop it.
	var sent map[string]any
	if err := json.Unmarshal([]byte(stub.bodies[0]), &sent); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
	if sent["kind"] != "Job" {
		t.Errorf("kind = %v", sent["kind"])
	}
}

// A create that was actually applied but whose response was lost must not look
// like a failure on retry, or the run is abandoned or duplicated.
func TestAnAlreadyExistingJobIsDistinguishable(t *testing.T) {
	stub := &apiStub{status: http.StatusConflict, reply: `{"reason":"AlreadyExists"}`}
	c := stub.client(t)

	err := c.CreateJob(context.Background(), "agent-runs", aJob(t))
	if !errors.Is(err, kube.ErrAlreadyExists) {
		t.Fatalf("error = %v, want ErrAlreadyExists", err)
	}
}

func TestJobStatusReportsCompletion(t *testing.T) {
	for _, tc := range []struct {
		name  string
		reply string
		want  kube.JobPhase
	}{
		{"running", `{"status":{"active":1}}`, kube.JobRunning},
		{"succeeded", `{"status":{"succeeded":1}}`, kube.JobSucceeded},
		{"failed", `{"status":{"failed":1}}`, kube.JobFailed},
		{"not started", `{"status":{}}`, kube.JobPending},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := &apiStub{reply: tc.reply}
			got, err := stub.client(t).JobStatus(context.Background(), "agent-runs", "run-1")
			if err != nil {
				t.Fatalf("JobStatus: %v", err)
			}
			if got != tc.want {
				t.Fatalf("phase = %q, want %q", got, tc.want)
			}
		})
	}
}

// A run's output is the whole point; without logs a completed Job tells us
// only that it exited.
func TestPodLogsFindThePodByJobLabel(t *testing.T) {
	stub := &apiStub{reply: `{"items":[{"metadata":{"name":"run-1-abcde"}}]}`}
	c := stub.client(t)

	// First call lists pods, second fetches logs; the stub replies with the
	// pod list to both, which is enough to assert the request shapes.
	_, _ = c.PodLogs(context.Background(), "agent-runs", "run-1")

	if len(stub.requests) < 2 {
		t.Fatalf("made %d requests, want a list then a log fetch", len(stub.requests))
	}
	list := stub.requests[0]
	if list.URL.Path != "/api/v1/namespaces/agent-runs/pods" {
		t.Errorf("list path = %s", list.URL.Path)
	}
	if got := list.URL.Query().Get("labelSelector"); !strings.Contains(got, "job-name=run-1") {
		t.Errorf("label selector = %q", got)
	}
	logs := stub.requests[1]
	if !strings.HasSuffix(logs.URL.Path, "/pods/run-1-abcde/log") {
		t.Errorf("log path = %s", logs.URL.Path)
	}
}

// Errors are logged. A bearer token in one is a token in the log store.
func TestErrorsNeverContainTheToken(t *testing.T) {
	stub := &apiStub{status: http.StatusForbidden, reply: `{"message":"forbidden"}`}
	c := stub.client(t)

	err := c.CreateJob(context.Background(), "agent-runs", aJob(t))
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), token) {
		t.Fatalf("error leaks the token: %v", err)
	}
	if !strings.Contains(err.Error(), "403") {
		t.Errorf("error does not carry the status: %v", err)
	}
}

// In-cluster config must fail closed. Falling back to an anonymous client
// would turn "no permission" into a confusing 403 much later, or worse, work
// against the wrong cluster.
func TestInClusterFailsWhenThereIsNoServiceAccount(t *testing.T) {
	dir := t.TempDir()
	_, err := kube.InCluster(dir)
	if err == nil {
		t.Fatal("expected an error with no service account projected")
	}

	if err := os.WriteFile(filepath.Join(dir, "token"), []byte(token), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := kube.InCluster(dir); err == nil {
		t.Fatal("expected an error with no KUBERNETES_SERVICE_HOST")
	}
}
