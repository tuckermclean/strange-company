package lab

import (
	"net/http"
	"net/url"
)

// SC-STUB(F09 05-AC-6) NewS3Front returns the lab S3 TLS front's handler: bucket ?publicAccessBlock in memory, all else proxied to upstream.
func NewS3Front(upstream *url.URL) http.Handler {
	return nil
}
