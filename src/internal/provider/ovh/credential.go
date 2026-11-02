package ovh

import (
	"context"
	"strings"

	"strange.company/sc/internal/provider"
)

// CodeCredentialTooBroad is the PF-07 failure code.
const CodeCredentialTooBroad = "OVH_CREDENTIAL_TOO_BROAD"

// AccessRule is one rule of GET /auth/currentCredential (auth.AccessRule).
type AccessRule struct {
	Method string `json:"method"`
	Path   string `json:"path"`
}

// CurrentCredential is the response of GET /auth/currentCredential (auth.ApiCredential, reduced).
type CurrentCredential struct {
	Rules  []AccessRule `json:"rules"`
	Status string       `json:"status"`
}

// allowedRules is the plan 04 section 10 consumer key list (public-cloud rules are added per project).
var allowedRules = map[string]bool{
	"GET /auth/time": true, "GET /me": true, "GET /me/*": true,
	"GET /order/catalog/*": true,
	"POST /order/cart":     true, "POST /order/cart/*": true, "GET /order/cart/*": true, "DELETE /order/cart/*": true,
	"GET /me/order": true, "GET /me/order/*": true, "POST /me/order/*/payWithRegisteredPaymentMean": true,
	"GET /vps": true, "GET /vps/*": true, "PUT /vps/*": true, "POST /vps/*": true, "DELETE /vps/*": true,
	"POST /vps/*/terminate": true, "POST /vps/*/confirmTermination": true,
}

var cloudMethods = map[string]bool{"GET": true, "POST": true, "PUT": true, "DELETE": true}

func ruleAllowed(r AccessRule, cloudProjectID string) bool {
	m := strings.ToUpper(r.Method)
	if allowedRules[m+" "+r.Path] {
		return true
	}
	return cloudProjectID != "" && cloudMethods[m] && r.Path == "/cloud/project/"+cloudProjectID+"/*"
}

// CheckCredentialScope is PF-07 step 4: every rule must be within the plan 04 section 10 list. cloudProjectID is
// the OVH_CLOUD_PROJECT_ID of a public-cloud install, empty for vps.
func CheckCredentialScope(cred CurrentCredential, cloudProjectID string) provider.Finding {
	var broad []string
	for _, r := range cred.Rules {
		if !ruleAllowed(r, cloudProjectID) {
			broad = append(broad, r.Method+" "+r.Path)
		}
	}
	if len(broad) > 0 {
		return provider.Finding{ID: "PF-07", Status: "fail", Code: CodeCredentialTooBroad,
			Message: "consumer key has rules outside the plan 04 section 10 list: " + strings.Join(broad, ", ")}
	}
	return provider.Finding{ID: "PF-07", Status: "pass", Message: "consumer key rules are within the plan 04 section 10 list"}
}

// CredentialScope reads GET /auth/currentCredential through c and applies CheckCredentialScope.
func (c *Client) CredentialScope(ctx context.Context, cloudProjectID string) (provider.Finding, error) {
	var cred CurrentCredential
	if err := c.Get(ctx, "/auth/currentCredential", &cred); err != nil {
		return provider.Finding{}, err
	}
	return CheckCredentialScope(cred, cloudProjectID), nil
}
