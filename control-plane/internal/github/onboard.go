package github

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// ErrLabelExists reports that the intake label was already there. Not a
// failure: importing a repository twice must be safe.
var ErrLabelExists = errors.New("github: the label already exists")

// EnsureLabel creates the intake label, and treats "already there" as success.
//
// GitHub answers 422 when a label of that name exists, which is the ordinary
// case on any repository imported more than once.
func (c *Client) EnsureLabel(ctx context.Context, repository, name, colour, description string) error {
	owner, repo, err := splitRepository(repository)
	if err != nil {
		return err
	}

	body := map[string]string{"name": name, "color": colour, "description": description}
	path := fmt.Sprintf("/repos/%s/%s/labels", url.PathEscape(owner), url.PathEscape(repo))

	_, err = c.request(ctx, repository, http.MethodPost, path, body)
	if err == nil {
		return nil
	}
	if strings.Contains(err.Error(), "422") {
		return ErrLabelExists
	}
	return err
}

// DefaultBranch reports the repository's default branch, which is the base a
// day-0 pull request targets and the ref the red gate compares against.
func (c *Client) DefaultBranch(ctx context.Context, repository string) (string, error) {
	owner, repo, err := splitRepository(repository)
	if err != nil {
		return "", err
	}

	raw, err := c.request(ctx, repository, http.MethodGet,
		fmt.Sprintf("/repos/%s/%s", url.PathEscape(owner), url.PathEscape(repo)), nil)
	if err != nil {
		return "", err
	}
	var out struct {
		DefaultBranch string `json:"default_branch"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("github: decoding repository: %w", err)
	}
	if out.DefaultBranch == "" {
		return "", errors.New("github: repository reports no default branch")
	}
	return out.DefaultBranch, nil
}

// FileExists reports whether path is present on ref.
//
// Used to leave an existing workflow alone. A repository that already gates
// agent branches has made a decision, and overwriting it during an import
// would replace working CI with a template.
func (c *Client) FileExists(ctx context.Context, repository, path, ref string) (bool, error) {
	owner, repo, err := splitRepository(repository)
	if err != nil {
		return false, err
	}

	p := fmt.Sprintf("/repos/%s/%s/contents/%s?ref=%s",
		url.PathEscape(owner), url.PathEscape(repo), path, url.QueryEscape(ref))

	_, err = c.request(ctx, repository, http.MethodGet, p, nil)
	if err == nil {
		return true, nil
	}
	if strings.Contains(err.Error(), "404") {
		return false, nil
	}
	return false, err
}

// CreateBranch points a new branch at ref's head.
func (c *Client) CreateBranch(ctx context.Context, repository, branch, fromRef string) error {
	owner, repo, err := splitRepository(repository)
	if err != nil {
		return err
	}

	raw, err := c.request(ctx, repository, http.MethodGet,
		fmt.Sprintf("/repos/%s/%s/git/ref/heads/%s",
			url.PathEscape(owner), url.PathEscape(repo), url.PathEscape(fromRef)), nil)
	if err != nil {
		return fmt.Errorf("github: reading %s: %w", fromRef, err)
	}
	var head struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		return fmt.Errorf("github: decoding ref: %w", err)
	}

	_, err = c.request(ctx, repository, http.MethodPost,
		fmt.Sprintf("/repos/%s/%s/git/refs", url.PathEscape(owner), url.PathEscape(repo)),
		map[string]string{"ref": "refs/heads/" + branch, "sha": head.Object.SHA})
	if err != nil && strings.Contains(err.Error(), "422") {
		// Already exists, which is what a second import looks like.
		return nil
	}
	return err
}

// PutFile commits content at path on branch.
//
// This is the call that needs a credential with `workflow` scope when path is
// under .github/workflows -- GitHub refuses the write otherwise, and that
// refusal is the whole reason day-0 is a separate, more privileged operation
// than anything an agent is allowed to do.
func (c *Client) PutFile(ctx context.Context, repository, path, branch, message string, content []byte) error {
	owner, repo, err := splitRepository(repository)
	if err != nil {
		return err
	}

	body := map[string]string{
		"message": message,
		"content": base64.StdEncoding.EncodeToString(content),
		"branch":  branch,
	}
	_, err = c.request(ctx, repository, http.MethodPut,
		fmt.Sprintf("/repos/%s/%s/contents/%s", url.PathEscape(owner), url.PathEscape(repo), path),
		body)
	return err
}
