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

type boardStub struct {
	buckets  string
	requests []string
	bodies   []map[string]any
}

func (b *boardStub) client(t *testing.T) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		b.requests = append(b.requests, r.Method+" "+r.URL.Path)
		b.bodies = append(b.bodies, body)

		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/buckets") && r.Method == http.MethodGet:
			_, _ = io.WriteString(w, b.buckets)
		default:
			_, _ = io.WriteString(w, `{"id":1,"title":"x"}`)
		}
	}))
	t.Cleanup(srv.Close)
	return New(srv.URL, "token", nil)
}

// A board whose columns do not run Backlog to Done is one a human has to read
// rather than glance at, which defeats the point of a board.
func TestANewBucketCarriesItsPosition(t *testing.T) {
	s := &boardStub{buckets: `[]`}

	if _, err := s.client(t).CreateBucket(context.Background(), 2, 8, "Ready", 200); err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}
	if got := s.bodies[0]["position"]; got != float64(200) {
		t.Fatalf("position = %v; Vikunja would order the column however it liked", got)
	}
}

// Existing boards matter more than new ones: a board created before this stays
// scrambled forever otherwise, and "delete your board" is not a fix.
func TestAnExistingBucketCanBeRepositioned(t *testing.T) {
	s := &boardStub{buckets: `[]`}

	if err := s.client(t).SetBucketPosition(context.Background(), 2, 8, 5, "Ready", 200); err != nil {
		t.Fatalf("SetBucketPosition: %v", err)
	}
	if s.requests[0] != "POST /api/v1/projects/2/views/8/buckets/5" {
		t.Fatalf("request = %q", s.requests[0])
	}
	if got := s.bodies[0]["position"]; got != float64(200) {
		t.Fatalf("position = %v", got)
	}
}

// Spaced rather than 0..n so a human can drag a column between two of ours
// without Vikunja having to renumber everything.
func TestPositionsLeaveRoomBetweenColumns(t *testing.T) {
	s := &boardStub{buckets: `[]`}
	c := s.client(t)

	for i, title := range []string{"Backlog", "Ready"} {
		if _, err := c.CreateBucket(context.Background(), 2, 8, title, float64(i+1)*100); err != nil {
			t.Fatal(err)
		}
	}
	first, second := s.bodies[0]["position"].(float64), s.bodies[1]["position"].(float64)
	if second-first < 2 {
		t.Fatalf("positions %v and %v leave nowhere to drop a column between", first, second)
	}
}
