package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// PullRequest is the pull request to open or update (spec §19, §25).
type PullRequest struct {
	Repository string // "owner/name"
	Head       string // the agent branch
	Base       string
	Title      string
	Body       string
}

// OpenPullRequest is the result.
type OpenPullRequest struct {
	Number int    `json:"number"`
	URL    string `json:"html_url"`
}

// EnsurePullRequest opens the pull request for an agent branch, or updates the
// one that is already open.
//
// Updating rather than opening again matters because a card is reviewed more
// than once: §18's CORRECTABLE sends it back into implementation, and the next
// pass arrives here with the same branch. A second pull request would be
// rejected by GitHub anyway, and if it were not, one piece of work would end up
// scattered across several reviews.
func (c *Client) EnsurePullRequest(ctx context.Context, pr PullRequest) (*OpenPullRequest, error) {
	owner, name, err := splitRepository(pr.Repository)
	if err != nil {
		return nil, err
	}
	var missing []string
	if strings.TrimSpace(pr.Head) == "" {
		missing = append(missing, "head branch")
	}
	if strings.TrimSpace(pr.Base) == "" {
		missing = append(missing, "base ref")
	}
	if strings.TrimSpace(pr.Title) == "" {
		missing = append(missing, "title")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("github: pull request needs a %s", strings.Join(missing, " and a "))
	}

	// Scoped to this branch: an unscoped search would adopt somebody else's
	// open pull request and overwrite its body.
	q := url.Values{
		"head":  {fmt.Sprintf("%s:%s", owner, pr.Head)},
		"state": {"open"},
	}
	listPath := fmt.Sprintf("/repos/%s/%s/pulls?%s", url.PathEscape(owner), url.PathEscape(name), q.Encode())
	body, err := c.request(ctx, pr.Repository, http.MethodGet, listPath, nil)
	if err != nil {
		return nil, err
	}

	var existing []OpenPullRequest
	if err := json.Unmarshal(body, &existing); err != nil {
		return nil, fmt.Errorf("github: decoding pull requests: %w", err)
	}

	if len(existing) > 0 {
		patchPath := fmt.Sprintf("/repos/%s/%s/pulls/%d",
			url.PathEscape(owner), url.PathEscape(name), existing[0].Number)
		updated, err := c.request(ctx, pr.Repository, http.MethodPatch, patchPath, map[string]string{
			"title": pr.Title,
			"body":  pr.Body,
		})
		if err != nil {
			return nil, err
		}
		var out OpenPullRequest
		if err := json.Unmarshal(updated, &out); err != nil {
			return nil, fmt.Errorf("github: decoding the updated pull request: %w", err)
		}
		return &out, nil
	}

	createPath := fmt.Sprintf("/repos/%s/%s/pulls", url.PathEscape(owner), url.PathEscape(name))
	created, err := c.request(ctx, pr.Repository, http.MethodPost, createPath, map[string]string{
		"title": pr.Title,
		"body":  pr.Body,
		"head":  pr.Head,
		"base":  pr.Base,
	})
	if err != nil {
		return nil, err
	}
	var out OpenPullRequest
	if err := json.Unmarshal(created, &out); err != nil {
		return nil, fmt.Errorf("github: decoding the new pull request: %w", err)
	}
	return &out, nil
}
