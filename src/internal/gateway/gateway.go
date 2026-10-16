// Package gateway is the Model Gateway library (plan 53).
package gateway

import (
	"fmt"
	"net/url"
	"os"
	"strings"
)

// paidHosts are the real provider API hosts that SC_FORBID_PAID=1 refuses as upstreams (plan 52 section 9.4 D6).
var paidHosts = map[string]bool{
	"api.anthropic.com": true,
	"api.openai.com":    true,
}

// ValidateUpstream checks a provider upstream URL. With SC_FORBID_PAID=1 (read at call time) the real
// provider hosts are refused whatever the scheme, case, port or path; fake and loopback hosts stay usable.
func ValidateUpstream(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("gateway: invalid upstream URL %q: %w", rawURL, err)
	}
	if os.Getenv("SC_FORBID_PAID") != "1" {
		return nil
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if paidHosts[host] {
		return fmt.Errorf("gateway: upstream host %s refused: SC_FORBID_PAID=1 forbids paid provider hosts", host)
	}
	return nil
}
