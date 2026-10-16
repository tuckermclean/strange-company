// Package gateway is the Model Gateway library (plan 53).
package gateway

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
)

// ValidateUpstream checks a provider upstream URL. With SC_FORBID_PAID=1 (read at call time) only fake
// upstreams are accepted (plan 52 section 9.4 D6, CD-53): an http(s) URL whose host is loopback, localhost,
// or a name under the reserved .invalid or .test TLDs. Everything else, including malformed values, is refused.
func ValidateUpstream(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("gateway: invalid upstream URL %q: %w", rawURL, err)
	}
	if os.Getenv("SC_FORBID_PAID") != "1" {
		return nil
	}
	scheme := strings.ToLower(u.Scheme)
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if (scheme != "http" && scheme != "https") || host == "" || !fakeHost(host) {
		return fmt.Errorf("gateway: upstream %q refused: SC_FORBID_PAID=1 accepts only http(s) loopback, localhost, *.invalid and *.test hosts (host %q)", rawURL, host)
	}
	return nil
}

func fakeHost(host string) bool {
	if host == "localhost" || strings.HasSuffix(host, ".invalid") || strings.HasSuffix(host, ".test") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
