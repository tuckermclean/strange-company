package ovh_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"strange.company/sc/internal/provider/ovh"
)

const fixtures = "../../../../test/fixtures/ovh"

const authTime = 1700000000

func fixture(t testing.TB, rel string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(fixtures, rel))
	if err != nil {
		t.Fatalf("fixture %s: %v", rel, err)
	}
	return b
}

// serverCreds are the credentials of the fake OVH API; the server verifies every signature against the documented
// formula, independently of the code under test.
var serverCreds = ovh.Credentials{ApplicationKey: "app-key-invented", ApplicationSecret: "app-secret-invented", ConsumerKey: "consumer-key-invented"}

type fakeOVH struct {
	srv    *httptest.Server
	mu     sync.Mutex
	routes map[string]json.RawMessage
	calls  []string
}

// newFakeOVH serves routes ("GET /vps": body) under /1.0. GET /auth/time answers authTime. Requests other than
// GET, with a wrong signature or an unknown route fail the test.
func newFakeOVH(t *testing.T, routes map[string]json.RawMessage) *fakeOVH {
	t.Helper()
	f := &fakeOVH{routes: routes}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.calls = append(f.calls, r.Method+" "+r.URL.RequestURI())
		f.mu.Unlock()
		if r.Method != http.MethodGet {
			t.Errorf("read-only scan issued %s %s", r.Method, r.URL.RequestURI())
			http.Error(w, "method", http.StatusMethodNotAllowed)
			return
		}
		path := strings.TrimPrefix(r.URL.Path, "/1.0")
		if path == "/auth/time" {
			_, _ = w.Write([]byte("1700000000"))
			return
		}
		url := f.srv.URL + r.URL.RequestURI()
		want := "$1$" + hexSHA1(serverCreds.ApplicationSecret+"+"+serverCreds.ConsumerKey+"+GET+"+url+"++1700000000")
		if got := r.Header.Get("X-Ovh-Signature"); got != want {
			t.Errorf("GET %s: X-Ovh-Signature = %q, want %q", r.URL.RequestURI(), got, want)
		}
		if r.Header.Get("X-Ovh-Application") != serverCreds.ApplicationKey || r.Header.Get("X-Ovh-Consumer") != serverCreds.ConsumerKey {
			t.Errorf("GET %s: application/consumer headers wrong", path)
		}
		if r.Header.Get("X-Ovh-Timestamp") != "1700000000" {
			t.Errorf("GET %s: X-Ovh-Timestamp = %q, want the GET /auth/time value", path, r.Header.Get("X-Ovh-Timestamp"))
		}
		if path == "/me/order" && r.URL.Query().Get("date.from") == "" {
			t.Errorf("GET /me/order without date.from")
		}
		body, ok := f.routes["GET "+path]
		if !ok {
			t.Errorf("unexpected request GET %s", path)
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeOVH) client() *ovh.Client {
	return ovh.NewClient(ovh.ClientConfig{Endpoint: f.srv.URL + "/1.0", Credentials: serverCreds, HTTPClient: f.srv.Client()})
}

func loadRoutes(t *testing.T, rel string) map[string]json.RawMessage {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(fixture(t, rel), &m); err != nil {
		t.Fatalf("fixture %s: %v", rel, err)
	}
	return m
}
