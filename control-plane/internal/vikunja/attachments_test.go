package vikunja

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type attachStub struct {
	list     string
	status   int
	body     string
	disabled bool

	gotField   string
	gotName    string
	gotContent []byte
	requests   []string
}

func (a *attachStub) client(t *testing.T) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.requests = append(a.requests, r.Method+" "+r.URL.Path)
		if a.disabled {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")

		if r.Method == http.MethodGet {
			_, _ = io.WriteString(w, a.list)
			return
		}

		_, params, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		mr := multipart.NewReader(r.Body, params["boundary"])
		if part, err := mr.NextPart(); err == nil {
			a.gotField = part.FormName()
			a.gotName = part.FileName()
			a.gotContent, _ = io.ReadAll(part)
		}

		if a.status != 0 {
			w.WriteHeader(a.status)
		}
		if a.body != "" {
			_, _ = io.WriteString(w, a.body)
			return
		}
		_, _ = io.WriteString(w, `{"errors":[],"success":[{"id":1,"file":{"name":"x","mime":"text/plain","size":3}}]}`)
	}))
	t.Cleanup(srv.Close)
	return New(srv.URL, "token", nil)
}

// VERIFIED against Vikunja v0.24.6: the field name is "files", plural, because
// the handler iterates form.File["files"]. Sending "file" uploads nothing and
// reports no error.
func TestTheUploadFieldIsNamedFiles(t *testing.T) {
	a := &attachStub{}

	if err := a.client(t).UploadAttachment(context.Background(), 10, "diff.patch", []byte("abc")); err != nil {
		t.Fatalf("UploadAttachment: %v", err)
	}
	if a.gotField != "files" {
		t.Errorf("form field = %q, want %q", a.gotField, "files")
	}
	if a.gotName != "diff.patch" {
		t.Errorf("filename = %q", a.gotName)
	}
	if string(a.gotContent) != "abc" {
		t.Errorf("content = %q", a.gotContent)
	}
	if a.requests[0] != "PUT /api/v1/tasks/10/attachments" {
		t.Errorf("request = %q", a.requests[0])
	}
}

// Vikunja answers 200 with a per-file errors array rather than failing the
// request. A caller that checks only the status code records a file it does
// not have -- structurally the same trap as the Hermes gateway reporting a
// backend outage as a successful completion.
func TestAPerFileErrorInA200IsAFailure(t *testing.T) {
	a := &attachStub{body: `{"errors":[{"message":"file is too large"}],"success":[]}`}

	err := a.client(t).UploadAttachment(context.Background(), 10, "huge.log", []byte("abc"))
	if err == nil {
		t.Fatal("a 200 carrying an error was read as success")
	}
	if !strings.Contains(err.Error(), "too large") {
		t.Errorf("error = %v, want it to carry the server's reason", err)
	}
}

// 200, no error, no file. Believing it records an artifact as delivered when
// nothing was stored.
func TestA200WithNeitherSuccessNorErrorIsAFailure(t *testing.T) {
	a := &attachStub{body: `{"errors":[],"success":[]}`}

	if err := a.client(t).UploadAttachment(context.Background(), 10, "x.md", []byte("abc")); err == nil {
		t.Fatal("an empty result was read as success")
	}
}

// An upload rejected for size is a silent hole in the audit trail. Truncating
// with a visible marker is worse than the whole file and much better than
// nothing.
func TestAnOversizedFileIsTruncatedWithAMarkerRatherThanRejected(t *testing.T) {
	a := &attachStub{}
	huge := strings.Repeat("x", MaxAttachmentBytes+5000)

	if err := a.client(t).UploadAttachment(context.Background(), 10, "run.log", []byte(huge)); err != nil {
		t.Fatalf("UploadAttachment: %v", err)
	}
	if len(a.gotContent) >= len(huge) {
		t.Fatalf("uploaded %d bytes; the cap did not apply", len(a.gotContent))
	}
	if !strings.Contains(string(a.gotContent), "truncated by the control plane") {
		t.Error("a truncated file carries no marker; a reader would think it was whole")
	}
}

// Attachments are optional in Vikunja, exactly as comments are.
func TestADisabledInstanceIsReportedAsSuchRatherThanAsAnError(t *testing.T) {
	a := &attachStub{disabled: true}
	c := a.client(t)

	if err := c.UploadAttachment(context.Background(), 10, "x.md", []byte("abc")); !errors.Is(err, ErrAttachmentsDisabled) {
		t.Errorf("upload error = %v, want ErrAttachmentsDisabled", err)
	}
	if _, err := c.ListAttachments(context.Background(), 10); !errors.Is(err, ErrAttachmentsDisabled) {
		t.Errorf("list error = %v, want ErrAttachmentsDisabled", err)
	}
}

// Idempotence rests entirely on reading back what is already there.
func TestListReportsWhatIsAlreadyAttached(t *testing.T) {
	a := &attachStub{list: `[{"id":3,"file":{"name":"spec.md","mime":"text/markdown","size":12}}]`}

	got, err := a.client(t).ListAttachments(context.Background(), 10)
	if err != nil {
		t.Fatalf("ListAttachments: %v", err)
	}
	if len(got) != 1 || got[0].File.Name != "spec.md" {
		t.Fatalf("attachments = %+v, want spec.md", got)
	}
}

// Vikunja pages at 50 and this read one page. With 269 artifacts on a card, 219
// looked missing on every reconcile pass and were uploaded again -- 32,283
// attachments on one task, and a board grown to eleven megabytes.
func TestListingWalksEveryPage(t *testing.T) {
	var pages []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pages = append(pages, r.URL.Query().Get("page"))
		w.Header().Set("Content-Type", "application/json")
		// Two full pages, then a short one.
		n := 50
		if r.URL.Query().Get("page") == "3" {
			n = 7
		}
		out := make([]map[string]any, 0, n)
		for i := 0; i < n; i++ {
			out = append(out, map[string]any{"id": i, "file": map[string]any{"name": "f", "mime": "text/plain", "size": 1}})
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
	t.Cleanup(srv.Close)

	got, err := New(srv.URL, "token", nil).ListAttachments(context.Background(), 10)
	if err != nil {
		t.Fatalf("ListAttachments: %v", err)
	}
	if len(got) != 107 {
		t.Errorf("read %d attachments across %v, want 107", len(got), pages)
	}
	if len(pages) != 3 {
		t.Errorf("requested pages %v, want three", pages)
	}
}

// A partial list makes present things look absent, which is the whole defect.
// Better to refuse than to answer wrongly.
func TestATaskWithMoreAttachmentsThanWeWillReadIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		out := make([]map[string]any, 0, 50)
		for i := 0; i < 50; i++ {
			out = append(out, map[string]any{"id": i, "file": map[string]any{"name": "f"}})
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
	t.Cleanup(srv.Close)

	if _, err := New(srv.URL, "token", nil).ListAttachments(context.Background(), 10); !errors.Is(err, ErrTooManyAttachments) {
		t.Fatalf("error = %v, want ErrTooManyAttachments rather than a partial list", err)
	}
}
