package vikunja

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// Permission levels on a Vikunja project.
//
// VERIFIED against Vikunja v2.5.0, pkg/models/project_users.go: the JSON field
// is "permission" and the documented values are 0 = Read only, 1 = Read &
// Write, 2 = Admin. Pre-2.x releases called the field "right"; sending that
// name is accepted and silently leaves the share read-only, which is why the
// tests assert the wire field rather than the behaviour.
const (
	PermissionRead      = 0
	PermissionReadWrite = 1
	PermissionAdmin     = 2
)

// ErrInvalidPermission is returned for a permission Vikunja does not define.
var ErrInvalidPermission = errors.New("vikunja: permission must be 0 (read), 1 (read & write) or 2 (admin)")

// projectUser is the v2.5.0 wire shape of a project <-> user share.
type projectUser struct {
	Username   string `json:"username"`
	Permission int    `json:"permission"`
}

// EnsureProjectShares grants each username access to a project, at permission.
//
// The control plane creates its board as its own bootstrap user, so without
// this the project is private to a service account and no human can see the
// cards at all.
//
// Idempotent, because this runs on every reconcile pass: an existing share at
// the right permission is left alone, and one at the wrong permission is
// corrected. Read-only is the case worth correcting rather than tolerating --
// the human sees the board and silently cannot move a card, which spec §4.3
// treats as a real input to the state machine.
//
// A username Vikunja does not know is reported but does not stop the rest:
// one typo must not deny access to everyone listed after it.
func (c *Client) EnsureProjectShares(ctx context.Context, projectID int64, usernames []string, permission int) error {
	if permission < PermissionRead || permission > PermissionAdmin {
		return fmt.Errorf("%w (got %d)", ErrInvalidPermission, permission)
	}

	wanted := make([]string, 0, len(usernames))
	for _, u := range usernames {
		// A blank entry comes from a values list with a stray comma, and
		// would ask Vikunja to share with a user named "".
		if u = strings.TrimSpace(u); u != "" {
			wanted = append(wanted, u)
		}
	}
	if len(wanted) == 0 {
		return nil
	}

	existing, err := c.listProjectShares(ctx, projectID)
	if err != nil {
		return err
	}

	var failures []string
	for _, username := range wanted {
		if have, ok := existing[username]; ok && have == permission {
			continue
		}
		if err := c.putProjectShare(ctx, projectID, username, permission); err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", username, err))
		}
	}

	if len(failures) > 0 {
		return fmt.Errorf("vikunja: sharing project %d: %s", projectID, strings.Join(failures, "; "))
	}
	return nil
}

// listProjectShares returns username -> permission for a project.
func (c *Client) listProjectShares(ctx context.Context, projectID int64) (map[string]int, error) {
	var users []projectUser
	if err := c.do(ctx, "GET", fmt.Sprintf("/api/v1/projects/%d/users", projectID), nil, &users); err != nil {
		return nil, fmt.Errorf("vikunja: list shares for project %d: %w", projectID, err)
	}

	byName := make(map[string]int, len(users))
	for _, u := range users {
		byName[u.Username] = u.Permission
	}
	return byName, nil
}

// putProjectShare creates or updates one share.
//
// PUT /projects/{id}/users, verified from the @Router annotation on
// ProjectUser.Create in Vikunja v2.5.0.
// putProjectShare gives username the requested permission on a project,
// whether or not they already have some access to it.
//
// Vikunja splits this across two routes: PUT on the collection creates a
// share, POST on the member updates one (pkg/routes/routes.go). Sending only
// the create means a user who already has access comes back 409 -- which is
// what the control plane logged as a startup error on every restart, in the
// one case where the share was already correct. It is also how a permission
// would silently fail to be corrected: §4.3 treats the human's ability to move
// a card as a real input to the state machine, so read-only access when admin
// was asked for is not something to tolerate.
func (c *Client) putProjectShare(ctx context.Context, projectID int64, username string, permission int) error {
	body := projectUser{Username: username, Permission: permission}
	err := c.do(ctx, http.MethodPut, fmt.Sprintf("/api/v1/projects/%d/users", projectID), body, nil)

	var reqErr *RequestError
	if errors.As(err, &reqErr) && reqErr.Status == http.StatusConflict {
		// Already shared. Update the permission instead of creating.
		return c.do(ctx, http.MethodPost,
			fmt.Sprintf("/api/v1/projects/%d/users/%s", projectID, url.PathEscape(username)), body, nil)
	}
	return err
}
