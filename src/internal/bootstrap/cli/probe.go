package cli

import "net/http"

// NewModelKeyProbe builds the production PF-19 probe from the policy: GET <BaseURL>/models with the key in the header
// that AuthScheme names. It refuses a non-https BaseURL without sending and never follows a redirect.
// SC-STUB(F08 50-AC-15)
func NewModelKeyProbe(client *http.Client, targets map[string]ProbeTarget) func(provider, key string) error {
	return nil
}

// ProbeTarget is the policy view of one provider for NewModelKeyProbe.
// SC-STUB(F08 50-AC-15)
type ProbeTarget struct {
	BaseURL    string
	AuthScheme string
}
