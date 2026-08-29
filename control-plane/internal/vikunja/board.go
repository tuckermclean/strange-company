package vikunja

import (
	"context"
	"fmt"
	"net/http"

	"github.com/tuckermclean/strange-company/control-plane/internal/card"
)

// boardStates is the seven card.State values, in the order their matching
// Kanban buckets should be created if missing. The bucket title for each
// state is exactly its string value (spec section 4.2 / 4.3): "Backlog",
// "Ready", "InProgress", "Review", "Done", "Blocked", "NeedsHuman".
var boardStates = []card.State{
	card.Backlog,
	card.Ready,
	card.InProgress,
	card.Review,
	card.Done,
	card.Blocked,
	card.NeedsHuman,
}

// Project mirrors the subset of Vikunja's Project JSON fields this package
// needs. See docs/reference/vikunja-api-notes.md section 1.
type Project struct {
	ID    int64  `json:"id"`
	Title string `json:"title"`
}

// View mirrors the subset of Vikunja's ProjectView JSON fields this package
// needs. See docs/reference/vikunja-api-notes.md section 2.
type View struct {
	ID              int64  `json:"id"`
	Title           string `json:"title"`
	ProjectID       int64  `json:"project_id"`
	ViewKind        string `json:"view_kind"`
	DefaultBucketID int64  `json:"default_bucket_id"`
	DoneBucketID    int64  `json:"done_bucket_id"`
}

// Bucket mirrors the subset of Vikunja's Bucket JSON fields this package
// needs. When read via ListBoardTasks, Tasks is populated with the tasks
// currently sitting in this bucket. See
// docs/reference/vikunja-api-notes.md section 2/3.
type Bucket struct {
	ID    int64   `json:"id"`
	Title string  `json:"title"`
	Tasks []*Task `json:"tasks,omitempty"`

	// Position is what orders the columns. Vikunja returns it as a float
	// (pkg/models/kanban.go), and reading it is what lets an existing,
	// wrongly-ordered board be corrected rather than only new ones.
	Position float64 `json:"position"`
}

// Task mirrors the subset of Vikunja's Task JSON fields this package needs.
//
// BucketID is only reliably populated when the task was read as part of a
// Bucket returned from ListBoardTasks (a Kanban view listing); it is NOT
// populated by a plain single-task read, and writing it on a task update is
// a silent no-op (Task.BucketID is xorm:"-" upstream). Moving a task between
// buckets must go through MoveTaskToBucket. See
// docs/reference/vikunja-api-notes.md section 3.
type Task struct {
	ID       int64  `json:"id"`
	Title    string `json:"title"`
	Done     bool   `json:"done"`
	BucketID int64  `json:"bucket_id,omitempty"`
}

// TaskBucket is the payload sent to, and the shape returned from, the
// dedicated bucket-move relation endpoint. Only TaskID is meaningful in a
// request; Bucket/Task are only populated in a response. See
// docs/reference/vikunja-api-notes.md section 3 ("How a task moves between
// Kanban buckets").
type TaskBucket struct {
	TaskID int64   `json:"task_id"`
	Bucket *Bucket `json:"bucket,omitempty"`
	Task   *Task   `json:"task,omitempty"`
}

// Board is the control plane's view of the single Vikunja project it owns:
// the project's id, the id of its Kanban view, and the mapping from each
// card.State to the id of the bucket that represents it on that view.
type Board struct {
	ProjectID     int64
	KanbanViewID  int64
	BucketByState map[card.State]int64
}

// StateForBucket returns the card.State whose bucket is bucketID, and
// whether one was found. A bucket present on the board but not created by
// EnsureBoard (for example one a human added by hand) has no matching
// state.
func (b *Board) StateForBucket(bucketID int64) (card.State, bool) {
	for state, id := range b.BucketByState {
		if id == bucketID {
			return state, true
		}
	}
	return "", false
}

// EnsureBoard finds the Vikunja project titled projectTitle (creating it if
// it does not exist), locates its Kanban view, and ensures all seven
// board-state buckets exist on that view, creating any that are missing. It
// is idempotent: calling it again against an already-provisioned project
// creates nothing.
func (c *Client) EnsureBoard(ctx context.Context, projectTitle string) (*Board, error) {
	project, err := c.findOrCreateProject(ctx, projectTitle)
	if err != nil {
		return nil, fmt.Errorf("vikunja: ensure project %q: %w", projectTitle, err)
	}

	view, err := c.findKanbanView(ctx, project.ID)
	if err != nil {
		return nil, fmt.Errorf("vikunja: find kanban view for project %d: %w", project.ID, err)
	}

	buckets, err := c.ListBuckets(ctx, project.ID, view.ID)
	if err != nil {
		return nil, fmt.Errorf("vikunja: list buckets for view %d: %w", view.ID, err)
	}

	byTitle := make(map[string]int64, len(buckets))
	for _, b := range buckets {
		byTitle[b.Title] = b.ID
	}

	byPosition := make(map[string]float64, len(buckets))
	for _, b := range buckets {
		byPosition[b.Title] = b.Position
	}

	bucketByState := make(map[card.State]int64, len(boardStates))
	for i, state := range boardStates {
		title := string(state)
		// Positions are spaced rather than 0..n so a human can drag a
		// column between two of ours without Vikunja having to renumber.
		want := float64(i+1) * 100

		if id, ok := byTitle[title]; ok {
			bucketByState[state] = id
			// Correct an existing board rather than only new ones. A board
			// created before this stays scrambled forever otherwise, and
			// "delete your board" is not a fix.
			if byPosition[title] != want {
				if err := c.SetBucketPosition(ctx, project.ID, view.ID, id, title, want); err != nil {
					return nil, fmt.Errorf("vikunja: position bucket %q: %w", title, err)
				}
			}
			continue
		}

		created, err := c.CreateBucket(ctx, project.ID, view.ID, title, want)
		if err != nil {
			return nil, fmt.Errorf("vikunja: create bucket %q: %w", title, err)
		}
		bucketByState[state] = created.ID
	}

	return &Board{
		ProjectID:     project.ID,
		KanbanViewID:  view.ID,
		BucketByState: bucketByState,
	}, nil
}

// findOrCreateProject returns the project titled title, creating it if no
// such project exists yet. Vikunja auto-creates the default List/Gantt/
// Table/Kanban views for a newly created project, so no separate view
// creation step is needed here.
func (c *Client) findOrCreateProject(ctx context.Context, title string) (*Project, error) {
	projects, err := c.ListProjects(ctx)
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	for _, p := range projects {
		if p.Title == title {
			return p, nil
		}
	}
	return c.CreateProject(ctx, title)
}

// findKanbanView returns the Kanban-kind view for projectID. Vikunja views
// are matched by view_kind, never by title (the title is just a
// user-editable label). See docs/reference/vikunja-api-notes.md section 2.
func (c *Client) findKanbanView(ctx context.Context, projectID int64) (*View, error) {
	views, err := c.ListViews(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("list views: %w", err)
	}
	for _, v := range views {
		if v.ViewKind == "kanban" {
			return v, nil
		}
	}
	return nil, fmt.Errorf("project %d has no kanban view", projectID)
}

// ListProjects calls GET /api/v1/projects.
func (c *Client) ListProjects(ctx context.Context) ([]*Project, error) {
	var projects []*Project
	if err := c.do(ctx, http.MethodGet, "/api/v1/projects", nil, &projects); err != nil {
		return nil, err
	}
	return projects, nil
}

// CreateProject calls PUT /api/v1/projects (v1 create verb; see
// docs/reference/vikunja-api-notes.md section 0).
func (c *Client) CreateProject(ctx context.Context, title string) (*Project, error) {
	req := struct {
		Title string `json:"title"`
	}{Title: title}

	var resp Project
	if err := c.do(ctx, http.MethodPut, "/api/v1/projects", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// ListViews calls GET /api/v1/projects/{project}/views.
func (c *Client) ListViews(ctx context.Context, projectID int64) ([]*View, error) {
	var views []*View
	path := fmt.Sprintf("/api/v1/projects/%d/views", projectID)
	if err := c.do(ctx, http.MethodGet, path, nil, &views); err != nil {
		return nil, err
	}
	return views, nil
}

// ListBuckets calls GET /api/v1/projects/{project}/views/{view}/buckets.
// The returned buckets do not include their tasks; use ListBoardTasks for
// that. See docs/reference/vikunja-api-notes.md section 2.
func (c *Client) ListBuckets(ctx context.Context, projectID, viewID int64) ([]*Bucket, error) {
	var buckets []*Bucket
	path := fmt.Sprintf("/api/v1/projects/%d/views/%d/buckets", projectID, viewID)
	if err := c.do(ctx, http.MethodGet, path, nil, &buckets); err != nil {
		return nil, err
	}
	return buckets, nil
}

// CreateBucket calls PUT /api/v1/projects/{project}/views/{view}/buckets
// (v1 create verb) with body {"title": title}.
func (c *Client) CreateBucket(ctx context.Context, projectID, viewID int64, title string, position float64) (*Bucket, error) {
	// position is sent explicitly. Without it Vikunja orders buckets however
	// it likes, and a board whose columns do not run Backlog to Done is one
	// a human has to read rather than glance at -- which is the whole point
	// of a board.
	req := struct {
		Title    string  `json:"title"`
		Position float64 `json:"position"`
	}{Title: title, Position: position}

	var resp Bucket
	path := fmt.Sprintf("/api/v1/projects/%d/views/%d/buckets", projectID, viewID)
	if err := c.do(ctx, http.MethodPut, path, req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// ListBoardTasks calls GET /api/v1/projects/{project}/views/{view}/tasks on
// a Kanban view. Unlike a flat task listing, this returns an array of
// Bucket objects each with its Tasks populated — this is the only endpoint
// that reliably reports which bucket each task currently sits in. See
// docs/reference/vikunja-api-notes.md section 3 ("How to read which bucket
// a task is currently in").
func (c *Client) ListBoardTasks(ctx context.Context, projectID, viewID int64) ([]*Bucket, error) {
	var buckets []*Bucket
	path := fmt.Sprintf("/api/v1/projects/%d/views/%d/tasks", projectID, viewID)
	if err := c.do(ctx, http.MethodGet, path, nil, &buckets); err != nil {
		return nil, err
	}
	return buckets, nil
}

// CreateTask calls PUT /api/v1/projects/{project}/tasks (v1 create verb) to
// create a new task titled title in projectID. The created task lands in
// the target view's default bucket; callers that need it in a specific
// bucket must follow up with MoveTaskToBucket.
func (c *Client) CreateTask(ctx context.Context, projectID int64, title string) (*Task, error) {
	req := struct {
		Title string `json:"title"`
	}{Title: title}

	var resp Task
	path := fmt.Sprintf("/api/v1/projects/%d/tasks", projectID)
	if err := c.do(ctx, http.MethodPut, path, req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// MoveTaskToBucket calls the dedicated bucket-move relation endpoint,
// v1: POST /api/v1/projects/{project}/views/{view}/buckets/{bucket}/tasks,
// with body {"task_id": taskID}. This is the ONLY reliable way to move a
// task between Kanban buckets: setting bucket_id on a plain task update is
// a silent no-op, because Task.BucketID is xorm:"-" upstream (not a real
// database column). See docs/reference/vikunja-api-notes.md section 3.
func (c *Client) MoveTaskToBucket(ctx context.Context, projectID, viewID, bucketID, taskID int64) error {
	req := TaskBucket{TaskID: taskID}
	path := fmt.Sprintf("/api/v1/projects/%d/views/%d/buckets/%d/tasks", projectID, viewID, bucketID)
	return c.do(ctx, http.MethodPost, path, req, nil)
}

// SetBucketPosition moves an existing bucket to a position.
//
// VERIFIED against Vikunja v2.5.0 (pkg/models/kanban.go): position is a
// float64, and a bucket is updated with
// POST /projects/{project}/views/{view}/buckets/{bucket}.
//
// Existing boards need this, not just new ones. A board created before the
// order was set stays scrambled forever otherwise, and telling an operator to
// delete their board to get readable columns is not a fix.
func (c *Client) SetBucketPosition(ctx context.Context, projectID, viewID, bucketID int64, title string, position float64) error {
	req := struct {
		Title    string  `json:"title"`
		Position float64 `json:"position"`
	}{Title: title, Position: position}

	path := fmt.Sprintf("/api/v1/projects/%d/views/%d/buckets/%d", projectID, viewID, bucketID)
	return c.do(ctx, http.MethodPost, path, req, nil)
}

// UpdateTask writes a task's title and description.
//
// The description is where a card stops being a bare title: what it is, where
// it came from, and why it is in the column it is in. A board of titles alone
// tells a human nothing they did not already know.
func (c *Client) UpdateTask(ctx context.Context, taskID int64, title, description string) error {
	req := struct {
		ID          int64  `json:"id"`
		Title       string `json:"title"`
		Description string `json:"description"`
	}{ID: taskID, Title: title, Description: description}

	path := fmt.Sprintf("/api/v1/tasks/%d", taskID)
	return c.do(ctx, http.MethodPost, path, req, nil)
}
