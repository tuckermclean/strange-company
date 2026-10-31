package s3client_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"strange.company/sc/internal/clock"
	"strange.company/sc/internal/s3client"
)

// S4 review 1, N5: S3 can answer a server-side copy with "200 OK" and an <Error> root element when the copy fails
// after the headers went out. That body is a failure; only a <CopyObjectResult> body (or none) is success.
func TestCopyVersion_Error200BodyIsAFailure_06AC8(t *testing.T) {
	body := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		if r.Method == http.MethodPut && r.Header.Get("x-amz-copy-source") != "" {
			if strings.Contains(body, "<CopyObjectResult") {
				w.Header().Set("x-amz-version-id", "ver-new")
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(body))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	c, err := s3client.New(s3client.Config{Endpoint: srv.URL, Region: "us-east-1", AccessKey: "AKIA" + "FIXTUREKEY0001", SecretKey: "fixture-secret-" + strings.Repeat("k", 24),
		PathStyle: true, Clock: clock.NewFake(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))})
	if err != nil {
		t.Fatal(err)
	}
	b := c.Bucket("state-bkt")
	ctx := context.Background()

	body = `<?xml version="1.0" encoding="UTF-8"?><CopyObjectResult><ETag>"abc"</ETag></CopyObjectResult>`
	if r, err := b.CopyVersion(ctx, "m/terraform.tfstate", "v1"); err != nil || r.VersionID != "ver-new" {
		t.Fatalf("a <CopyObjectResult> body: CopyVersion = %+v, %v; want success", r, err)
	}
	body = `<?xml version="1.0" encoding="UTF-8"?><Error><Code>InternalError</Code><Message>We encountered an internal error. Please try again.</Message><RequestId>r1</RequestId></Error>`
	r, err := b.CopyVersion(ctx, "m/terraform.tfstate", "v1")
	if err == nil {
		t.Fatalf("a 200 response with an <Error> body was accepted as success: %+v", r)
	}
	if r.VersionID != "" || r.ETag != "" {
		t.Errorf("a failed copy returned a result: %+v", r)
	}
	if !strings.Contains(err.Error(), "InternalError") {
		t.Errorf("error %q does not carry the S3 error code", err)
	}
}
