// Package kube applies coding Jobs to the Kubernetes API.
//
// It speaks the REST API directly rather than through client-go, following
// what internal/jobs already chose: that package defines plain manifest
// structs instead of importing Kubernetes types, and the whole surface the
// control plane needs is four calls.
//
// Spec §16: coding runs are isolated Kubernetes Jobs. §29: the control plane's
// access is namespace-scoped and opt-in (the chart's agentRuns block is the
// only thing that grants it any Kubernetes API access at all).
package kube

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	defaultTimeout    = 60 * time.Second
	maxErrorBodyBytes = 512

	// maxLogBytes caps what one run's logs can be. The runner's own output
	// is already bounded; this stops a pathological pod from being read
	// into memory without limit.
	maxLogBytes = 8 << 20
)

// ErrAlreadyExists means the object was already there.
//
// Distinguished because a create that was applied but whose response was lost
// must not look like a failure on retry: treating it as one abandons or
// duplicates the run.
var ErrAlreadyExists = errors.New("kube: object already exists")

// JobPhase is a Job's coarse state.
type JobPhase string

const (
	JobPending   JobPhase = "pending"
	JobRunning   JobPhase = "running"
	JobSucceeded JobPhase = "succeeded"
	JobFailed    JobPhase = "failed"
)

// Client talks to one Kubernetes API server.
type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

// New builds a Client against an API base URL.
func New(baseURL, token string, h *http.Client) (*Client, error) {
	baseURL = strings.TrimSuffix(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return nil, errors.New("kube: API base URL is required")
	}
	if h == nil {
		h = &http.Client{Timeout: defaultTimeout}
	}
	return &Client{baseURL: baseURL, token: token, http: h}, nil
}

// InCluster builds a Client from a projected service account directory,
// conventionally /var/run/secrets/kubernetes.io/serviceaccount.
//
// Fails closed: a missing token, a missing CA or a missing KUBERNETES_SERVICE_HOST
// is an error rather than a fallback to an anonymous client. An anonymous
// client turns "this deployment was never granted Kubernetes access" into a
// confusing 403 much later, at the moment a card was about to be worked on.
func InCluster(dir string) (*Client, error) {
	token, err := os.ReadFile(filepath.Join(dir, "token"))
	if err != nil {
		return nil, fmt.Errorf("kube: no service account token (is agentRuns enabled?): %w", err)
	}

	host, port := os.Getenv("KUBERNETES_SERVICE_HOST"), os.Getenv("KUBERNETES_SERVICE_PORT")
	if host == "" || port == "" {
		return nil, errors.New("kube: KUBERNETES_SERVICE_HOST/PORT are unset; not running in a cluster")
	}

	pool := x509.NewCertPool()
	ca, err := os.ReadFile(filepath.Join(dir, "ca.crt"))
	if err != nil {
		return nil, fmt.Errorf("kube: no cluster CA: %w", err)
	}
	if !pool.AppendCertsFromPEM(ca) {
		return nil, errors.New("kube: cluster CA is not valid PEM")
	}

	return New(fmt.Sprintf("https://%s:%s", host, port), strings.TrimSpace(string(token)), &http.Client{
		Timeout: defaultTimeout,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
		},
	})
}

// CreateJob applies a Job manifest.
//
// The manifest is marshalled as given, so §16.1's hardening -- non-root,
// dropped capabilities, no service-account token, backoffLimit 0 -- travels
// exactly as internal/jobs built it. A client that re-marshalled a subset
// could silently drop any of it.
func (c *Client) CreateJob(ctx context.Context, namespace string, job any) error {
	body, err := json.Marshal(job)
	if err != nil {
		return fmt.Errorf("kube: encoding job: %w", err)
	}
	path := fmt.Sprintf("/apis/batch/v1/namespaces/%s/jobs", url.PathEscape(namespace))
	_, err = c.do(ctx, http.MethodPost, path, body)
	return err
}

// DeleteJob removes a Job and the pods it owns.
func (c *Client) DeleteJob(ctx context.Context, namespace, name string) error {
	path := fmt.Sprintf("/apis/batch/v1/namespaces/%s/jobs/%s?propagationPolicy=Background",
		url.PathEscape(namespace), url.PathEscape(name))
	_, err := c.do(ctx, http.MethodDelete, path, nil)
	return err
}

// JobStatus reports a Job's coarse phase.
func (c *Client) JobStatus(ctx context.Context, namespace, name string) (JobPhase, error) {
	path := fmt.Sprintf("/apis/batch/v1/namespaces/%s/jobs/%s", url.PathEscape(namespace), url.PathEscape(name))
	body, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return "", err
	}

	var parsed struct {
		Status struct {
			Active    int `json:"active"`
			Succeeded int `json:"succeeded"`
			Failed    int `json:"failed"`
		} `json:"status"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("kube: decoding job status: %w", err)
	}

	// Succeeded and failed are checked before active: a Job with
	// backoffLimit 0 that has already failed may still report an active pod
	// for a moment, and reading that as "running" would wait for a run that
	// is over.
	switch {
	case parsed.Status.Succeeded > 0:
		return JobSucceeded, nil
	case parsed.Status.Failed > 0:
		return JobFailed, nil
	case parsed.Status.Active > 0:
		return JobRunning, nil
	default:
		return JobPending, nil
	}
}

// PodLogs returns the logs of the pod a Job created.
//
// Without them a completed Job tells us only that it exited, and the run's
// entire output -- the stream the runner adapters parse -- is the point.
func (c *Client) PodLogs(ctx context.Context, namespace, jobName string) ([]byte, error) {
	listPath := fmt.Sprintf("/api/v1/namespaces/%s/pods?labelSelector=%s",
		url.PathEscape(namespace), url.QueryEscape("job-name="+jobName))
	body, err := c.do(ctx, http.MethodGet, listPath, nil)
	if err != nil {
		return nil, err
	}

	var list struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, fmt.Errorf("kube: decoding pod list: %w", err)
	}
	if len(list.Items) == 0 {
		return nil, fmt.Errorf("kube: no pod found for job %q", jobName)
	}

	logPath := fmt.Sprintf("/api/v1/namespaces/%s/pods/%s/log",
		url.PathEscape(namespace), url.PathEscape(list.Items[0].Metadata.Name))
	return c.do(ctx, http.MethodGet, logPath, nil)
}

func (c *Client) do(ctx context.Context, method, path string, body []byte) ([]byte, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, defaultTimeout)
		defer cancel()
	}

	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return nil, fmt.Errorf("kube: building request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("kube: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusConflict {
		return nil, fmt.Errorf("%w: %s", ErrAlreadyExists, path)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
		// The path and status, never a header: errors are logged.
		return nil, fmt.Errorf("kube: %s %s: status %d: %s",
			method, path, resp.StatusCode, strings.TrimSpace(string(snippet)))
	}

	return io.ReadAll(io.LimitReader(resp.Body, maxLogBytes))
}

// CreateSecret writes an Opaque Secret holding string data.
//
// Used for the per-run credential a coding Job pushes with. A minted token has
// to live somewhere the Job can reference, and putting it in the Job's own
// spec would print it in every `kubectl get job -o yaml` for the life of the
// run.
func (c *Client) CreateSecret(ctx context.Context, namespace, name string, data map[string]string) error {
	body, err := json.Marshal(map[string]any{
		"apiVersion": "v1",
		"kind":       "Secret",
		"metadata": map[string]any{
			"name":      name,
			"namespace": namespace,
			"labels": map[string]string{
				// So a leftover from a crashed run is identifiable as ours
				// rather than as something an operator has to reason about.
				"app.kubernetes.io/managed-by": "strange-company",
				"strange-company.io/ephemeral": "true",
			},
		},
		"type":       "Opaque",
		"stringData": data,
	})
	if err != nil {
		return fmt.Errorf("kube: encoding secret: %w", err)
	}

	path := fmt.Sprintf("/api/v1/namespaces/%s/secrets", url.PathEscape(namespace))
	_, err = c.do(ctx, http.MethodPost, path, body)
	return err
}

// DeleteSecret removes a Secret, treating "already gone" as success.
//
// The credential outliving the run it was minted for is the thing worth
// avoiding, so a delete that finds nothing has achieved what it wanted.
func (c *Client) DeleteSecret(ctx context.Context, namespace, name string) error {
	path := fmt.Sprintf("/api/v1/namespaces/%s/secrets/%s",
		url.PathEscape(namespace), url.PathEscape(name))
	_, err := c.do(ctx, http.MethodDelete, path, nil)
	if err != nil && strings.Contains(err.Error(), "404") {
		return nil
	}
	return err
}
