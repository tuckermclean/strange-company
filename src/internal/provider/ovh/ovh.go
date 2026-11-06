// Package ovh is the OVH provider code of plan 04 section 8: request signing, normalization of the OVH API
// responses into a ProviderOutput, the orphan scan and the credential scope check.
package ovh

import (
	"crypto/sha1" //nolint:gosec // OVH's documented request signature is SHA-1
	"encoding/hex"
	"net/http"
	"strconv"
)

// Credentials are the OVH application key, application secret and consumer key (SC-INFRA-PROVIDER).
type Credentials struct {
	ApplicationKey    string
	ApplicationSecret string
	ConsumerKey       string
}

// Sign returns the X-Ovh-Signature value "$1$"+hex(sha1(AS+"+"+CK+"+"+method+"+"+url+"+"+body+"+"+timestamp)).
// url is the full request URL including the query.
func Sign(c Credentials, method, url, body string, timestamp int64) string {
	in := c.ApplicationSecret + "+" + c.ConsumerKey + "+" + method + "+" + url + "+" + body + "+" + strconv.FormatInt(timestamp, 10)
	sum := sha1.Sum([]byte(in)) //nolint:gosec
	return "$1$" + hex.EncodeToString(sum[:])
}

// SignedHeaders returns X-Ovh-Application, X-Ovh-Consumer, X-Ovh-Timestamp and X-Ovh-Signature.
func SignedHeaders(c Credentials, method, url, body string, timestamp int64) http.Header {
	h := http.Header{}
	h.Set("X-Ovh-Application", c.ApplicationKey)
	h.Set("X-Ovh-Consumer", c.ConsumerKey)
	h.Set("X-Ovh-Timestamp", strconv.FormatInt(timestamp, 10))
	h.Set("X-Ovh-Signature", Sign(c, method, url, body, timestamp))
	return h
}
