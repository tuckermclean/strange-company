package vikunja

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type labelServer struct {
	labels  string
	deleted []string
	paths   []string
}

func (l *labelServer) client(t *testing.T) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		l.paths = append(l.paths, r.Method+" "+r.URL.Path)
		if r.Method == http.MethodDelete {
			l.deleted = append(l.deleted, r.URL.Path)
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, l.labels)
	}))
	t.Cleanup(srv.Close)
	return New(srv.URL, "token", nil)
}

// Verified against Vikunja v2.5.0 (pkg/models/label_task.go):
// GET /tasks/{task}/labels, DELETE /tasks/{task}/labels/{label}.
func TestTaskLabelsAreReadFromTheTask(t *testing.T) {
	s := &labelServer{labels: `[{"id":3,"title":"spec-approved"},{"id":9,"title":"urgent"}]`}
	c := s.client(t)

	labels, err := c.TaskLabels(context.Background(), 42)
	if err != nil {
		t.Fatalf("TaskLabels: %v", err)
	}
	if len(labels) != 2 || labels[0].Title != "spec-approved" || labels[0].ID != 3 {
		t.Fatalf("labels = %+v", labels)
	}
	if s.paths[0] != "GET /api/v1/tasks/42/labels" {
		t.Fatalf("path = %q", s.paths[0])
	}
}

func TestRemovingALabelAddressesItByID(t *testing.T) {
	s := &labelServer{labels: `[]`}
	c := s.client(t)

	if err := c.RemoveTaskLabel(context.Background(), 42, 3); err != nil {
		t.Fatalf("RemoveTaskLabel: %v", err)
	}
	if len(s.deleted) != 1 || !strings.HasSuffix(s.deleted[0], "/tasks/42/labels/3") {
		t.Fatalf("deleted = %v", s.deleted)
	}
}

func TestAnEmptyLabelListIsNotAnError(t *testing.T) {
	s := &labelServer{labels: `[]`}

	labels, err := s.client(t).TaskLabels(context.Background(), 42)
	if err != nil {
		t.Fatalf("TaskLabels: %v", err)
	}
	if len(labels) != 0 {
		t.Fatalf("labels = %+v", labels)
	}
}

func TestLabelsDecodeIndependentlyOfFieldOrder(t *testing.T) {
	s := &labelServer{labels: `[{"title":"spec-approved","id":3}]`}

	labels, err := s.client(t).TaskLabels(context.Background(), 42)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(labels)
	if !strings.Contains(string(b), `"spec-approved"`) {
		t.Fatalf("labels = %s", b)
	}
}
