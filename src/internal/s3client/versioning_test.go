package s3client_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"strange.company/sc/internal/s3client"
)

// GetBucketVersioning (plan 50 plan.storage-apply done check): Enabled is true; Suspended and never-configured are false.
func TestVersioning_ReportsEnabledOnly(t *testing.T) {
	for _, tc := range []struct {
		body string
		want bool
	}{
		{`<VersioningConfiguration><Status>Enabled</Status></VersioningConfiguration>`, true},
		{`<VersioningConfiguration><Status>Suspended</Status></VersioningConfiguration>`, false},
		{`<VersioningConfiguration/>`, false},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet || r.URL.Path != "/bkt" || !r.URL.Query().Has("versioning") {
				http.Error(w, "unexpected request "+r.Method+" "+r.URL.String(), http.StatusBadRequest)
				return
			}
			_, _ = w.Write([]byte(tc.body))
		}))
		c, err := s3client.New(s3client.Config{Endpoint: srv.URL, Region: "us-east-1", AccessKey: "fixture-access-id", SecretKey: "fixture-secret-key", PathStyle: true})
		if err != nil {
			t.Fatal(err)
		}
		got, err := c.Versioning(context.Background(), "bkt")
		srv.Close()
		if err != nil || got != tc.want {
			t.Errorf("Versioning on %s = %v, %v; want %v", tc.body, got, err, tc.want)
		}
	}
}
