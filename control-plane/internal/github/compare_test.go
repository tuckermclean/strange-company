package github_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tuckermclean/strange-company/control-plane/internal/github"
)

type diffStub struct {
	body   string
	status int
	accept string
	path   string
}

func (d *diffStub) client(t *testing.T) *github.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.accept = r.Header.Get("Accept")
		d.path = r.URL.Path
		if d.status != 0 {
			w.WriteHeader(d.status)
		}
		_, _ = io.WriteString(w, d.body)
	}))
	t.Cleanup(srv.Close)
	c, err := github.New(srv.URL, "ghp_token", nil)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

const aDiff = `diff --git a/src/math.js b/src/math.js
new file mode 100644
--- /dev/null
+++ b/src/math.js
@@ -0,0 +1,5 @@
+function mean(nums) {
`

// §18 says the reviewer receives the final diff. It never did: nothing wrote a
// diff artifact, so the reviewer read the spec and plan and described code it
// had never seen -- confidently, and wrongly.
func TestTheDiffIsFetchedAsAUnifiedDiff(t *testing.T) {
	s := &diffStub{body: aDiff}

	got, err := s.client(t).CompareDiff(context.Background(), "example/repo", "main", "agent/card-1")
	if err != nil {
		t.Fatalf("CompareDiff: %v", err)
	}
	if !strings.Contains(got, "function mean") {
		t.Fatalf("diff = %q", got)
	}
	// The media type is what makes this a diff rather than a JSON summary
	// of one; without it the reviewer would get metadata and no code.
	if s.accept != "application/vnd.github.diff" {
		t.Errorf("Accept = %q", s.accept)
	}
	if s.path != "/repos/example/repo/compare/main...agent/card-1" {
		t.Errorf("path = %q", s.path)
	}
}

// An empty comparison means the branch changed nothing. That is a fact worth
// surfacing, not an empty string the reviewer silently reviews around.
func TestAnEmptyComparisonIsReported(t *testing.T) {
	s := &diffStub{body: ""}

	_, err := s.client(t).CompareDiff(context.Background(), "example/repo", "main", "agent/card-1")
	if !errors.Is(err, github.ErrEmptyDiff) {
		t.Fatalf("error = %v, want ErrEmptyDiff", err)
	}
}

func TestCompareNeedsBothRefs(t *testing.T) {
	s := &diffStub{body: aDiff}
	c := s.client(t)

	for _, tc := range [][2]string{{"", "head"}, {"base", ""}} {
		if _, err := c.CompareDiff(context.Background(), "example/repo", tc[0], tc[1]); err == nil {
			t.Errorf("accepted base=%q head=%q", tc[0], tc[1])
		}
	}
}
