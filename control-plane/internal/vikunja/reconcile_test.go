package vikunja

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/tuckermclean/strange-company/control-plane/internal/card"
	"github.com/tuckermclean/strange-company/control-plane/internal/store"
)

// --- fake Vikunja board server -------------------------------------------------

// Fixed bucket ids used by every test, matching the seven board states.
const (
	bucketBacklog    int64 = 1
	bucketReady      int64 = 2
	bucketInProgress int64 = 3
	bucketReview     int64 = 4
	bucketDone       int64 = 5
	bucketBlocked    int64 = 6
	bucketNeedsHuman int64 = 7

	testProjectID int64 = 1
	testViewID    int64 = 1
)

var bucketTitleByID = map[int64]string{
	bucketBacklog:    string(card.Backlog),
	bucketReady:      string(card.Ready),
	bucketInProgress: string(card.InProgress),
	bucketReview:     string(card.Review),
	bucketDone:       string(card.Done),
	bucketBlocked:    string(card.Blocked),
	bucketNeedsHuman: string(card.NeedsHuman),
}

// testBoard returns a Board wired to the fixed bucket ids above.
func testBoard() *Board {
	return &Board{
		ProjectID:    testProjectID,
		KanbanViewID: testViewID,
		BucketByState: map[card.State]int64{
			card.Backlog:    bucketBacklog,
			card.Ready:      bucketReady,
			card.InProgress: bucketInProgress,
			card.Review:     bucketReview,
			card.Done:       bucketDone,
			card.Blocked:    bucketBlocked,
			card.NeedsHuman: bucketNeedsHuman,
		},
	}
}

// fakeBoard is a minimal in-memory stand-in for the three Vikunja endpoints
// the reconciler uses: listing a Kanban view's tasks-by-bucket, creating a
// task, and moving a task between buckets via the dedicated relation
// endpoint. It records every request's "METHOD path" for assertions.
type fakeBoard struct {
	mu sync.Mutex

	server *httptest.Server

	requests []string

	// bucketTasks maps bucket id -> ordered task ids currently in it.
	bucketTasks map[int64][]int64
	// tasks maps task id -> title, for every task ever created or seeded.
	tasks map[int64]string
	// descriptions maps task id -> description, so a test can assert what a
	// human would actually read on the card.
	descriptions map[int64]string
	// comments maps task id -> the notes posted against it, in order.
	comments map[int64][]string
	// commentsDisabled makes the comment route 404, as it does on an
	// install with service.enabletaskcomments off.
	commentsDisabled bool

	nextTaskID int64

	// failCreateTitle, if non-empty, makes CreateTask fail for a task with
	// exactly this title.
	failCreateTitle string
}

func newFakeBoard(t *testing.T) *fakeBoard {
	t.Helper()

	f := &fakeBoard{
		bucketTasks: map[int64][]int64{
			bucketBacklog:    nil,
			bucketReady:      nil,
			bucketInProgress: nil,
			bucketReview:     nil,
			bucketDone:       nil,
			bucketBlocked:    nil,
			bucketNeedsHuman: nil,
		},
		tasks:        make(map[int64]string),
		descriptions: make(map[int64]string),
		comments:     make(map[int64][]string),
		nextTaskID:   1000,
	}

	mux := http.NewServeMux()
	mux.HandleFunc(fmt.Sprintf("/api/v1/projects/%d/views/%d/tasks", testProjectID, testViewID), f.handleListBoardTasks)
	mux.HandleFunc(fmt.Sprintf("/api/v1/projects/%d/tasks", testProjectID), f.handleCreateTask)
	mux.HandleFunc(fmt.Sprintf("/api/v1/projects/%d/views/%d/buckets/", testProjectID, testViewID), f.handleMoveTask)
	mux.HandleFunc("/api/v1/tasks/", f.handleTask)

	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)

	return f
}

func (f *fakeBoard) record(r *http.Request) {
	f.requests = append(f.requests, r.Method+" "+r.URL.Path)
}

// seedTask adds a task directly into bucketID, bypassing CreateTask, so
// tests can set up a board state as if it already existed before this
// reconciliation run.
func (f *fakeBoard) seedTask(bucketID, taskID int64, title string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tasks[taskID] = title
	f.bucketTasks[bucketID] = append(f.bucketTasks[bucketID], taskID)
	if taskID >= f.nextTaskID {
		f.nextTaskID = taskID + 1
	}
}

// writeRequests returns only the requests that were not simple GETs, i.e.
// the ones that would have changed board state.
func (f *fakeBoard) writeRequests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, req := range f.requests {
		if !strings.HasPrefix(req, "GET ") {
			out = append(out, req)
		}
	}
	return out
}

func (f *fakeBoard) tasksInBucket(bucketID int64) []int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]int64, len(f.bucketTasks[bucketID]))
	copy(out, f.bucketTasks[bucketID])
	return out
}

func (f *fakeBoard) handleListBoardTasks(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record(r)

	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	ids := []int64{bucketBacklog, bucketReady, bucketInProgress, bucketReview, bucketDone, bucketBlocked, bucketNeedsHuman}
	resp := make([]*Bucket, 0, len(ids))
	for _, id := range ids {
		b := &Bucket{ID: id, Title: bucketTitleByID[id]}
		for _, taskID := range f.bucketTasks[id] {
			b.Tasks = append(b.Tasks, &Task{
				ID:          taskID,
				Title:       f.tasks[taskID],
				BucketID:    id,
				Description: f.descriptions[taskID],
			})
		}
		resp = append(resp, b)
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func (f *fakeBoard) handleCreateTask(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record(r)

	if r.Method != http.MethodPut {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	var body struct {
		Title string `json:"title"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)

	if f.failCreateTitle != "" && body.Title == f.failCreateTitle {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"message":"forced create failure"}`))
		return
	}

	id := f.nextTaskID
	f.nextTaskID++
	f.tasks[id] = body.Title

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(&Task{ID: id, Title: body.Title})
}

// handleTask serves both POST /tasks/{id} (update) and PUT
// /tasks/{id}/comments (comment), which share a path prefix.
func (f *fakeBoard) handleTask(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record(r)

	rest := strings.TrimPrefix(r.URL.Path, "/api/v1/tasks/")
	if idPart, ok := strings.CutSuffix(rest, "/comments"); ok {
		if f.commentsDisabled {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Method != http.MethodPut {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		id, err := strconv.ParseInt(idPart, 10, 64)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var body struct {
			Comment string `json:"comment"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.comments[id] = append(f.comments[id], body.Comment)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":1}`))
		return
	}

	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	id, err := strconv.ParseInt(rest, 10, 64)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	var body Task
	_ = json.NewDecoder(r.Body).Decode(&body)
	f.descriptions[id] = body.Description
	if body.Title != "" {
		f.tasks[id] = body.Title
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(&Task{ID: id, Title: f.tasks[id], Description: f.descriptions[id]})
}

func (f *fakeBoard) descriptionOf(taskID int64) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.descriptions[taskID]
}

func (f *fakeBoard) commentsOn(taskID int64) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.comments[taskID]...)
}

func (f *fakeBoard) handleMoveTask(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record(r)

	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	prefix := fmt.Sprintf("/api/v1/projects/%d/views/%d/buckets/", testProjectID, testViewID)
	suffix := strings.TrimPrefix(r.URL.Path, prefix)
	parts := strings.Split(suffix, "/")
	if len(parts) != 2 || parts[1] != "tasks" {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	bucketID, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	var body TaskBucket
	_ = json.NewDecoder(r.Body).Decode(&body)

	for bid, ids := range f.bucketTasks {
		for i, id := range ids {
			if id == body.TaskID {
				f.bucketTasks[bid] = append(ids[:i], ids[i+1:]...)
				break
			}
		}
	}
	f.bucketTasks[bucketID] = append(f.bucketTasks[bucketID], body.TaskID)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(&TaskBucket{
		TaskID: body.TaskID,
		Bucket: &Bucket{ID: bucketID, Title: bucketTitleByID[bucketID]},
		Task:   &Task{ID: body.TaskID, Title: f.tasks[body.TaskID], BucketID: bucketID},
	})
}

// --- in-memory CardRepo --------------------------------------------------------

type transitionCall struct {
	id      uuid.UUID
	to      card.State
	actor   card.ActorType
	actorID string
	reason  string
}

type setTaskIDCall struct {
	id     uuid.UUID
	taskID int64
}

// memRepo is a hand-written in-memory CardRepo. Tests configure cards
// directly and read back the recorded calls to assert behaviour.
type memRepo struct {
	mu sync.Mutex

	cards []*card.Card

	transitions []transitionCall
	setTaskIDs  []setTaskIDCall

	// transitionErr, if non-nil, is returned by every call to Transition.
	transitionErr error
	// setTaskIDErrFor maps a card id to an error SetVikunjaTaskID should
	// return for that specific card, letting a test fail one card without
	// affecting others.
	setTaskIDErrFor map[uuid.UUID]error

	// evidence is what the workers would have recorded about each card.
	evidence map[uuid.UUID][]store.CardEvidence

	syncedStates []syncedStateCall
}

type syncedStateCall struct {
	id    uuid.UUID
	state card.State
}

func (m *memRepo) SetVikunjaSyncedState(_ context.Context, id uuid.UUID, state card.State) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.syncedStates = append(m.syncedStates, syncedStateCall{id: id, state: state})
	for _, c := range m.cards {
		if c.ID == id {
			s := string(state)
			c.VikunjaSyncedState = &s
		}
	}
	return nil
}

func (m *memRepo) ListEvidence(_ context.Context, cardID uuid.UUID) ([]store.CardEvidence, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.evidence[cardID], nil
}

func (m *memRepo) ListCards(_ context.Context) ([]*card.Card, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*card.Card, len(m.cards))
	copy(out, m.cards)
	return out, nil
}

func (m *memRepo) Transition(_ context.Context, id uuid.UUID, to card.State, actor card.ActorType, actorID, reason string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.transitions = append(m.transitions, transitionCall{id: id, to: to, actor: actor, actorID: actorID, reason: reason})

	if m.transitionErr != nil {
		return m.transitionErr
	}

	for _, c := range m.cards {
		if c.ID == id {
			c.State = to
		}
	}
	return nil
}

func (m *memRepo) SetVikunjaTaskID(_ context.Context, id uuid.UUID, taskID int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.setTaskIDs = append(m.setTaskIDs, setTaskIDCall{id: id, taskID: taskID})

	if err, ok := m.setTaskIDErrFor[id]; ok {
		return err
	}

	for _, c := range m.cards {
		if c.ID == id {
			tid := taskID
			c.VikunjaTaskID = &tid
		}
	}
	return nil
}

// --- test helpers ---------------------------------------------------------

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func int64Ptr(v int64) *int64 { return &v }

func newCard(title string, state card.State, taskID *int64) *card.Card {
	return &card.Card{
		ID:            uuid.New(),
		Title:         title,
		SourceType:    "manual",
		State:         state,
		Phase:         card.PhaseImplementation,
		RiskClass:     "R1",
		VikunjaTaskID: taskID,
	}
}

func newTestReconciler(t *testing.T, board *fakeBoard, repo *memRepo) *Reconciler {
	t.Helper()
	client := New(board.server.URL, "test-token", nil)
	return NewReconciler(client, testBoard(), repo, discardLogger())
}

// --- tests ------------------------------------------------------------------

func TestReconcilerCreatesATaskForAnUnlinkedCard(t *testing.T) {
	board := newFakeBoard(t)
	c := newCard("do the thing", card.Backlog, nil)
	repo := &memRepo{cards: []*card.Card{c}}

	r := newTestReconciler(t, board, repo)
	result, err := r.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce() error = %v, want nil", err)
	}

	if want := 1; result.Pushed != want {
		t.Errorf("result.Pushed = %d, want %d", result.Pushed, want)
	}
	if want := 1; result.Checked != want {
		t.Errorf("result.Checked = %d, want %d", result.Checked, want)
	}
	if result.Accepted != 0 || result.Rejected != 0 {
		t.Errorf("result = %+v, want Accepted=0 Rejected=0", result)
	}

	if len(repo.setTaskIDs) != 1 {
		t.Fatalf("len(repo.setTaskIDs) = %d, want 1", len(repo.setTaskIDs))
	}
	if repo.setTaskIDs[0].id != c.ID {
		t.Errorf("SetVikunjaTaskID called for card %s, want %s", repo.setTaskIDs[0].id, c.ID)
	}
	newTaskID := repo.setTaskIDs[0].taskID

	inBacklog := board.tasksInBucket(bucketBacklog)
	if len(inBacklog) != 1 || inBacklog[0] != newTaskID {
		t.Errorf("bucketBacklog tasks = %v, want [%d]", inBacklog, newTaskID)
	}
}

func TestReconcilerAcceptsALegalHumanMove(t *testing.T) {
	board := newFakeBoard(t)
	taskID := int64(501)
	// Card thinks it's Ready, but a human dragged the task into Blocked in
	// the Vikunja UI. Ready -> Blocked is legal for any actor.
	board.seedTask(bucketBlocked, taskID, "seeded card")
	c := newCard("seeded card", card.Ready, int64Ptr(taskID))
	repo := &memRepo{cards: []*card.Card{c}}

	r := newTestReconciler(t, board, repo)
	result, err := r.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce() error = %v, want nil", err)
	}

	if want := 1; result.Accepted != want {
		t.Errorf("result.Accepted = %d, want %d", result.Accepted, want)
	}
	if result.Rejected != 0 || result.Pushed != 0 {
		t.Errorf("result = %+v, want Rejected=0 Pushed=0", result)
	}

	if len(repo.transitions) != 1 {
		t.Fatalf("len(repo.transitions) = %d, want 1", len(repo.transitions))
	}
	got := repo.transitions[0]
	if got.id != c.ID || got.to != card.Blocked || got.actor != card.ActorHuman {
		t.Errorf("transition = %+v, want {id:%s to:Blocked actor:human}", got, c.ID)
	}
	if got.actorID != "vikunja" {
		t.Errorf("transition.actorID = %q, want %q", got.actorID, "vikunja")
	}

	// The task should NOT have been moved back — the move became canonical.
	inBlocked := board.tasksInBucket(bucketBlocked)
	if len(inBlocked) != 1 || inBlocked[0] != taskID {
		t.Errorf("bucketBlocked tasks = %v, want [%d]", inBlocked, taskID)
	}
}

func TestReconcilerRejectsAnIllegalHumanMove(t *testing.T) {
	board := newFakeBoard(t)
	taskID := int64(502)
	// Card thinks it's Backlog, but a human dragged the task straight into
	// Done in the Vikunja UI. Backlog -> Done is not a permitted transition
	// for any actor.
	board.seedTask(bucketDone, taskID, "seeded card")
	c := newCard("seeded card", card.Backlog, int64Ptr(taskID))
	repo := &memRepo{cards: []*card.Card{c}}

	r := newTestReconciler(t, board, repo)
	result, err := r.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce() error = %v, want nil", err)
	}

	if want := 1; result.Rejected != want {
		t.Errorf("result.Rejected = %d, want %d", result.Rejected, want)
	}
	if result.Accepted != 0 || result.Pushed != 0 {
		t.Errorf("result = %+v, want Accepted=0 Pushed=0", result)
	}

	if len(repo.transitions) != 0 {
		t.Errorf("repo.transitions = %+v, want none (illegal move must not become canonical)", repo.transitions)
	}

	inDone := board.tasksInBucket(bucketDone)
	if len(inDone) != 0 {
		t.Errorf("bucketDone tasks = %v, want empty (task should have been moved back)", inDone)
	}
	inBacklog := board.tasksInBucket(bucketBacklog)
	if len(inBacklog) != 1 || inBacklog[0] != taskID {
		t.Errorf("bucketBacklog tasks = %v, want [%d] (task should have been moved back)", inBacklog, taskID)
	}
}

func TestReconcilerIsANoOpWhenBoardAndDatabaseAgree(t *testing.T) {
	board := newFakeBoard(t)
	taskID := int64(503)
	board.seedTask(bucketInProgress, taskID, "seeded card")
	c := newCard("seeded card", card.InProgress, int64Ptr(taskID))
	repo := &memRepo{cards: []*card.Card{c}}

	r := newTestReconciler(t, board, repo)
	// Seed the description this card already has, so this stays a test of
	// "nothing to do" rather than of the first description write.
	board.descriptions[taskID] = r.describe(context.Background(), c)

	result, err := r.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce() error = %v, want nil", err)
	}

	if result.Pushed != 0 || result.Accepted != 0 || result.Rejected != 0 {
		t.Errorf("result = %+v, want all-zero except Checked", result)
	}
	if want := 1; result.Checked != want {
		t.Errorf("result.Checked = %d, want %d", result.Checked, want)
	}

	if writes := board.writeRequests(); len(writes) != 0 {
		t.Errorf("writeRequests() = %v, want none", writes)
	}
	if len(repo.transitions) != 0 || len(repo.setTaskIDs) != 0 {
		t.Errorf("repo recorded writes it should not have: transitions=%v setTaskIDs=%v", repo.transitions, repo.setTaskIDs)
	}
}

func TestReconcilerIgnoresTasksWithNoCard(t *testing.T) {
	board := newFakeBoard(t)
	board.seedTask(bucketReady, 999, "orphan task")
	repo := &memRepo{}

	r := newTestReconciler(t, board, repo)
	result, err := r.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce() error = %v, want nil", err)
	}

	if want := 1; result.Checked != want {
		t.Errorf("result.Checked = %d, want %d", result.Checked, want)
	}
	if result.Pushed != 0 || result.Accepted != 0 || result.Rejected != 0 {
		t.Errorf("result = %+v, want all-zero except Checked", result)
	}
	if len(repo.transitions) != 0 || len(repo.setTaskIDs) != 0 {
		t.Errorf("repo recorded writes it should not have: transitions=%v setTaskIDs=%v", repo.transitions, repo.setTaskIDs)
	}
	// The orphan task must not have been moved or otherwise touched.
	inReady := board.tasksInBucket(bucketReady)
	if len(inReady) != 1 || inReady[0] != 999 {
		t.Errorf("bucketReady tasks = %v, want [999] (untouched)", inReady)
	}
}

func TestReconcilerContinuesAfterOneCardFails(t *testing.T) {
	board := newFakeBoard(t)
	board.failCreateTitle = "will fail"

	failing := newCard("will fail", card.Backlog, nil)
	succeeding := newCard("will succeed", card.Backlog, nil)
	repo := &memRepo{cards: []*card.Card{failing, succeeding}}

	r := newTestReconciler(t, board, repo)
	result, err := r.RunOnce(context.Background())
	if err == nil {
		t.Fatal("RunOnce() error = nil, want non-nil (one card failed)")
	}

	if want := 1; result.Pushed != want {
		t.Errorf("result.Pushed = %d, want %d (the surviving card should still be processed)", result.Pushed, want)
	}
	if want := 2; result.Checked != want {
		t.Errorf("result.Checked = %d, want %d", result.Checked, want)
	}

	if len(repo.setTaskIDs) != 1 {
		t.Fatalf("len(repo.setTaskIDs) = %d, want 1", len(repo.setTaskIDs))
	}
	if repo.setTaskIDs[0].id != succeeding.ID {
		t.Errorf("SetVikunjaTaskID called for card %s, want the surviving card %s", repo.setTaskIDs[0].id, succeeding.ID)
	}
}

func TestRunOnceReportsCounts(t *testing.T) {
	board := newFakeBoard(t)

	unlinked := newCard("unlinked", card.Backlog, nil)

	noopTaskID := int64(601)
	board.seedTask(bucketReview, noopTaskID, "no-op card")
	noop := newCard("no-op card", card.Review, int64Ptr(noopTaskID))

	acceptedTaskID := int64(602)
	board.seedTask(bucketBlocked, acceptedTaskID, "accepted card") // was Ready
	accepted := newCard("accepted card", card.Ready, int64Ptr(acceptedTaskID))

	rejectedTaskID := int64(603)
	board.seedTask(bucketDone, rejectedTaskID, "rejected card") // was Backlog
	rejected := newCard("rejected card", card.Backlog, int64Ptr(rejectedTaskID))

	board.seedTask(bucketReady, 999, "orphan task")

	repo := &memRepo{cards: []*card.Card{unlinked, noop, accepted, rejected}}

	r := newTestReconciler(t, board, repo)
	result, err := r.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce() error = %v, want nil", err)
	}

	want := Result{Checked: 5, Pushed: 1, Accepted: 1, Rejected: 1}
	if result != want {
		t.Errorf("RunOnce() result = %+v, want %+v", result, want)
	}
}

// sanity check that CanTransition itself agrees with the fixtures used
// above, so a change to the state machine surfaces here rather than as a
// confusing failure in the tests that depend on it.
func TestFixturesAgreeWithStateMachine(t *testing.T) {
	if err := card.CanTransition(card.Ready, card.Blocked, card.ActorHuman); err != nil {
		t.Fatalf("expected Ready -> Blocked to be legal for a human, got %v", err)
	}
	if err := card.CanTransition(card.Backlog, card.Done, card.ActorHuman); err == nil || !errors.Is(err, card.ErrIllegalTransition) {
		t.Fatalf("expected Backlog -> Done to be illegal for a human, got %v", err)
	}
}
