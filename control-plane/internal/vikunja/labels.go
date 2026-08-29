package vikunja

import (
	"context"
	"fmt"
)

// Label is a Vikunja label as it appears on a task.
//
// VERIFIED against Vikunja v2.5.0: GET /tasks/{task}/labels returns the label
// objects, and DELETE /tasks/{task}/labels/{label} removes one by id
// (pkg/models/label_task.go).
type Label struct {
	ID    int64  `json:"id"`
	Title string `json:"title"`
}

// TaskLabels returns the labels on a task.
func (c *Client) TaskLabels(ctx context.Context, taskID int64) ([]Label, error) {
	var labels []Label
	if err := c.do(ctx, "GET", fmt.Sprintf("/api/v1/tasks/%d/labels", taskID), nil, &labels); err != nil {
		return nil, fmt.Errorf("vikunja: reading labels on task %d: %w", taskID, err)
	}
	return labels, nil
}

// RemoveTaskLabel takes a label off a task.
//
// Used after a label has been acted on. A label that stays put would be acted
// on again on the next pass -- and for an approval label that is not merely
// noisy but wrong: editing a specification revokes its approval, and a label
// nobody re-added would silently re-approve the new text.
func (c *Client) RemoveTaskLabel(ctx context.Context, taskID, labelID int64) error {
	if err := c.do(ctx, "DELETE", fmt.Sprintf("/api/v1/tasks/%d/labels/%d", taskID, labelID), nil, nil); err != nil {
		return fmt.Errorf("vikunja: removing label %d from task %d: %w", labelID, taskID, err)
	}
	return nil
}
