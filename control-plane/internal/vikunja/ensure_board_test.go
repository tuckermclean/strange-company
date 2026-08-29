package vikunja

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// ensureStub is a Vikunja with one project, one Kanban view, and whatever
// buckets a test seeds it with. It records positions so a test can assert the
// order a human would actually see.
type ensureStub struct {
	mu        sync.Mutex
	buckets   map[int64]*Bucket
	nextID    int64
	deletes   []int64
	positions []string
}

func newEnsureStub(seed map[int64]*Bucket) *ensureStub {
	s := &ensureStub{buckets: map[int64]*Bucket{}, nextID: 100}
	for id, b := range seed {
		b.ID = id
		s.buckets[id] = b
		if id >= s.nextID {
			s.nextID = id + 1
		}
	}
	return s
}

func (s *ensureStub) client(t *testing.T) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")

		path := r.URL.Path
		switch {
		case path == "/api/v1/projects" && r.Method == http.MethodGet:
			_, _ = io.WriteString(w, `[{"id":2,"title":"strange-company"}]`)

		case path == "/api/v1/projects/2/views":
			_, _ = io.WriteString(w, `[{"id":8,"view_kind":"kanban"}]`)

		case path == "/api/v1/projects/2/views/8/buckets" && r.Method == http.MethodGet:
			out := make([]*Bucket, 0, len(s.buckets))
			for _, b := range s.buckets {
				out = append(out, b)
			}
			sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
			_ = json.NewEncoder(w).Encode(out)

		case path == "/api/v1/projects/2/views/8/buckets" && r.Method == http.MethodPut:
			var body struct {
				Title    string  `json:"title"`
				Position float64 `json:"position"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			id := s.nextID
			s.nextID++
			s.buckets[id] = &Bucket{ID: id, Title: body.Title, Position: body.Position}
			_ = json.NewEncoder(w).Encode(s.buckets[id])

		case strings.HasPrefix(path, "/api/v1/projects/2/views/8/buckets/"):
			id, _ := strconv.ParseInt(strings.TrimPrefix(path, "/api/v1/projects/2/views/8/buckets/"), 10, 64)
			if r.Method == http.MethodDelete {
				s.deletes = append(s.deletes, id)
				w.WriteHeader(http.StatusNoContent)
				return
			}
			var body struct {
				Position float64 `json:"position"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if b, ok := s.buckets[id]; ok {
				b.Position = body.Position
				s.positions = append(s.positions, fmt.Sprintf("%s=%v", b.Title, body.Position))
			}
			_ = json.NewEncoder(w).Encode(s.buckets[id])

		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return New(srv.URL, "token", nil)
}

// order returns the column titles as a reader sees them, left to right.
func (s *ensureStub) order() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*Bucket, 0, len(s.buckets))
	for _, b := range s.buckets {
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Position != out[j].Position {
			return out[i].Position < out[j].Position
		}
		return out[i].ID < out[j].ID
	})
	titles := make([]string, 0, len(out))
	for _, b := range out {
		titles = append(titles, b.Title)
	}
	return titles
}

func canonical() []string {
	out := make([]string, 0, len(boardStates))
	for _, s := range boardStates {
		out = append(out, string(s))
	}
	return out
}

func TestAFreshProjectReadsInCanonicalOrder(t *testing.T) {
	stub := newEnsureStub(nil)

	if _, err := stub.client(t).EnsureBoard(context.Background(), "strange-company"); err != nil {
		t.Fatalf("EnsureBoard: %v", err)
	}
	if got, want := strings.Join(stub.order(), " "), strings.Join(canonical(), " "); got != want {
		t.Errorf("columns read %q, want %q", got, want)
	}
}

// The real board. Vikunja gives every new project three default buckets;
// "Done" matches one of ours by title and is adopted, while "To-Do" and
// "Doing" are left over at exactly the positions Backlog and Ready belong to.
// Ordering our seven is not enough while two strangers share their positions.
func TestVikunjasOwnDefaultColumnsDoNotInterleaveWithOurs(t *testing.T) {
	stub := newEnsureStub(map[int64]*Bucket{
		4: {Title: "To-Do", Position: 1},
		5: {Title: "Doing", Position: 2},
		6: {Title: "Done", Position: 3},
	})

	if _, err := stub.client(t).EnsureBoard(context.Background(), "strange-company"); err != nil {
		t.Fatalf("EnsureBoard: %v", err)
	}

	got := stub.order()
	if len(got) < len(canonical()) {
		t.Fatalf("columns = %v", got)
	}
	if head, want := strings.Join(got[:len(canonical())], " "), strings.Join(canonical(), " "); head != want {
		t.Errorf("the first seven columns read %q, want %q\nfull order: %v", head, want, got)
	}
	// "Done" is ours by title; only the two genuine leftovers move aside.
	if tail := got[len(canonical()):]; len(tail) != 2 {
		t.Errorf("stray columns = %v, want To-Do and Doing after ours", tail)
	}
}

// This code cannot tell Vikunja's leftovers from a column a human added on
// purpose. Deleting the wrong one destroys work; the worst case for moving one
// is a column further right than someone wanted.
func TestAColumnWeDoNotRecogniseIsMovedNeverDeleted(t *testing.T) {
	stub := newEnsureStub(map[int64]*Bucket{
		9: {Title: "Waiting on legal", Position: 1},
	})

	if _, err := stub.client(t).EnsureBoard(context.Background(), "strange-company"); err != nil {
		t.Fatalf("EnsureBoard: %v", err)
	}
	if len(stub.deletes) != 0 {
		t.Fatalf("deleted buckets %v; someone's own column is not ours to remove", stub.deletes)
	}
	got := stub.order()
	if got[len(got)-1] != "Waiting on legal" {
		t.Errorf("columns = %v, want the unrecognised one last", got)
	}
}

// A second pass must not shuffle anything: the reconciler calls this on every
// start, and a board that renumbers itself each time is a board that never
// settles.
func TestEnsureBoardIsIdempotent(t *testing.T) {
	stub := newEnsureStub(map[int64]*Bucket{
		4: {Title: "To-Do", Position: 1},
		6: {Title: "Done", Position: 3},
	})
	c := stub.client(t)

	if _, err := c.EnsureBoard(context.Background(), "strange-company"); err != nil {
		t.Fatalf("first pass: %v", err)
	}
	first := strings.Join(stub.order(), " ")
	stub.positions = nil

	if _, err := c.EnsureBoard(context.Background(), "strange-company"); err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if len(stub.positions) != 0 {
		t.Errorf("the second pass repositioned %v; the board never settles", stub.positions)
	}
	if second := strings.Join(stub.order(), " "); second != first {
		t.Errorf("order changed between passes:\n  %q\n  %q", first, second)
	}
}
