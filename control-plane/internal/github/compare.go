package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// ErrEmptyDiff means the two refs are identical.
//
// Surfaced rather than returned as an empty string: a branch that changed
// nothing is a fact a reviewer needs, and an empty diff silently omitted from
// the review input is indistinguishable from no diff being fetched at all --
// which is exactly the failure this call exists to end.
var ErrEmptyDiff = errors.New("github: the two refs are identical")

// maxDiffBytes bounds what is fetched into memory and handed to a model. A
// change larger than this is not something a single review pass can hold.
const maxDiffBytes = 1 << 20

// CompareDiff returns the unified diff between two refs.
//
// §18: "The reviewer receives the approved spec, implementation plan,
// acceptance criteria, final diff and passing verification summary." The diff
// is fetched here rather than reconstructed from artifacts because this is
// what a human reviewer would see -- the same comparison, from the same
// source of truth.
func (c *Client) CompareDiff(ctx context.Context, repository, base, head string) (string, error) {
	owner, name, err := splitRepository(repository)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(base) == "" || strings.TrimSpace(head) == "" {
		return "", errors.New("github: comparing needs both a base and a head ref")
	}

	path := fmt.Sprintf("/repos/%s/%s/compare/%s...%s",
		url.PathEscape(owner), url.PathEscape(name), base, head)

	// The media type is what makes this a diff rather than a JSON summary of
	// one. Without it the reviewer gets file metadata and no code.
	body, err := c.requestAccept(ctx, http.MethodGet, path, "application/vnd.github.diff")
	if err != nil {
		return "", err
	}
	if len(body) > maxDiffBytes {
		body = body[:maxDiffBytes]
	}

	diff := strings.TrimSpace(string(body))
	if diff == "" {
		return "", fmt.Errorf("%w (%s...%s)", ErrEmptyDiff, base, head)
	}
	return diff, nil
}
