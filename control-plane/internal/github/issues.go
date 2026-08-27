// Package github reads work items from GitHub.
//
// Spec §25: an issue labelled `agent-ready` is eligible for ingestion. This
// package only reads; deciding what an issue becomes is internal/ingest's job.
package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	defaultTimeout = 30 * time.Second
	perPage        = 100

	// maxPages bounds a pass. A repository with more than ten thousand
	// eligible issues is a misconfigured label, not a backlog, and walking
	// it forever would wedge the supervisor.
	maxPages = 100

	maxErrorBodyBytes = 512
)

// ErrBadRepository is returned for anything that is not exactly "owner/name".
var ErrBadRepository = errors.New("github: repository must be owner/name")

// Client reads issues from a GitHub API.
type Client struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

// New builds a Client. baseURL is the API root, so GitHub Enterprise works by
// configuration rather than by code change.
func New(baseURL, token string, h *http.Client) (*Client, error) {
	baseURL = strings.TrimSuffix(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return nil, errors.New("github: base URL is required")
	}
	if h == nil {
		h = &http.Client{Timeout: defaultTimeout}
	}
	return &Client{baseURL: baseURL, token: token, httpClient: h}, nil
}

// Issue is the subset of a GitHub issue ingestion needs.
type Issue struct {
	Repository string `json:"-"`

	Number  int    `json:"number"`
	Title   string `json:"title"`
	Body    string `json:"body"`
	HTMLURL string `json:"html_url"`
	State   string `json:"state"`

	// PullRequest is non-nil when this "issue" is really a pull request.
	// GitHub's issues endpoint returns both, and ingesting a PR would turn
	// every open pull request into a card asking an agent to implement it.
	PullRequest *struct {
		URL string `json:"url"`
	} `json:"pull_request"`
}

// ExternalID is the identity ingestion stores.
//
// It includes the repository on purpose: issue #7 exists in every repository
// on GitHub, and two of them are not the same piece of work.
func (i Issue) ExternalID() string {
	return fmt.Sprintf("%s#%d", i.Repository, i.Number)
}

// ListLabeledIssues returns every open issue in repository carrying label,
// following pagination.
func (c *Client) ListLabeledIssues(ctx context.Context, repository, label string) ([]Issue, error) {
	owner, name, err := splitRepository(repository)
	if err != nil {
		return nil, err
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, defaultTimeout)
		defer cancel()
	}

	var issues []Issue
	for page := 1; page <= maxPages; page++ {
		q := url.Values{
			"labels":   {label},
			"state":    {"open"},
			"per_page": {fmt.Sprint(perPage)},
			"page":     {fmt.Sprint(page)},
		}
		path := fmt.Sprintf("/repos/%s/%s/issues?%s", url.PathEscape(owner), url.PathEscape(name), q.Encode())

		batch, next, err := c.getIssues(ctx, path)
		if err != nil {
			return nil, err
		}
		for _, issue := range batch {
			// Every pull request is an issue in this API.
			if issue.PullRequest != nil {
				continue
			}
			issue.Repository = repository
			issues = append(issues, issue)
		}
		// GitHub advertises the next page in a Link header. Deciding from
		// the item count alone stops early on any page that happens to be
		// short, which silently truncates a backlog.
		if next == "" {
			break
		}
	}
	return issues, nil
}

// nextPageLink returns the URL marked rel="next" in a Link header, or "".
func nextPageLink(header string) string {
	for _, part := range strings.Split(header, ",") {
		segments := strings.Split(part, ";")
		if len(segments) < 2 {
			continue
		}
		isNext := false
		for _, s := range segments[1:] {
			if strings.Contains(strings.ToLower(s), `rel="next"`) {
				isNext = true
				break
			}
		}
		if !isNext {
			continue
		}
		url := strings.TrimSpace(segments[0])
		url = strings.TrimPrefix(url, "<")
		url = strings.TrimSuffix(url, ">")
		if url != "" {
			return url
		}
	}
	return ""
}

func (c *Client) getIssues(ctx context.Context, path string) ([]Issue, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, "", fmt.Errorf("github: building request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		// net/http error strings can include the request URL but never a
		// header, so the token cannot appear here.
		return nil, "", fmt.Errorf("github: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
		return nil, "", fmt.Errorf("github: %s: status %d: %s", path, resp.StatusCode, strings.TrimSpace(string(snippet)))
	}

	var issues []Issue
	if err := json.NewDecoder(resp.Body).Decode(&issues); err != nil {
		return nil, "", fmt.Errorf("github: decoding issues: %w", err)
	}
	return issues, nextPageLink(resp.Header.Get("Link")), nil
}

// splitRepository requires exactly "owner/name".
func splitRepository(repository string) (owner, name string, err error) {
	parts := strings.Split(strings.TrimSpace(repository), "/")
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return "", "", fmt.Errorf("%w (got %q)", ErrBadRepository, repository)
	}
	return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]), nil
}
