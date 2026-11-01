// Package ovh is the OVH provider code of plan 04 section 8: request signing, normalization of the OVH API
// responses into a ProviderOutput, the orphan scan and the credential scope check.
package ovh

import (
	"context"
	"net/http"

	"strange.company/sc/internal/provider"
)

// SC-STUB(F09 04-AC-3 04-AC-4 04-AC-5)
// Credentials are the OVH application key, application secret and consumer key (SC-INFRA-PROVIDER).
type Credentials struct {
	ApplicationKey    string
	ApplicationSecret string
	ConsumerKey       string
}

// SC-STUB(F09 04-AC-3 04-AC-4 04-AC-5)
// Sign returns the X-Ovh-Signature value "$1$"+hex(sha1(AS+"+"+CK+"+"+method+"+"+url+"+"+body+"+"+timestamp)).
// url is the full request URL including the query.
func Sign(c Credentials, method, url, body string, timestamp int64) string { return "" }

// SC-STUB(F09 04-AC-3 04-AC-4 04-AC-5)
// SignedHeaders returns X-Ovh-Application, X-Ovh-Consumer, X-Ovh-Timestamp and X-Ovh-Signature.
func SignedHeaders(c Credentials, method, url, body string, timestamp int64) http.Header { return nil }

// SC-STUB(F09 04-AC-3 04-AC-4 04-AC-5)
// ClientConfig configures the signed read-only API client; Endpoint is the API root, for example
// https://eu.api.ovh.com/1.0.
type ClientConfig struct {
	Endpoint    string
	Credentials Credentials
	HTTPClient  *http.Client
}

// SC-STUB(F09 04-AC-3 04-AC-4 04-AC-5)
// Client is the signed OVH API client; its timestamp comes from GET /auth/time, never from the wall clock.
type Client struct{}

// SC-STUB(F09 04-AC-3 04-AC-4 04-AC-5)
// NewClient returns a client for cfg.
func NewClient(cfg ClientConfig) *Client { return nil }

// SC-STUB(F09 04-AC-4)
// Orphan is one resource of this install that is absent from state.
type Orphan struct {
	// Code is ORPHAN_PAID_RESOURCE or ORPHAN_PENDING_ORDER.
	Code string
	// ProviderID is the VPS service name (empty for a pending order without a service).
	ProviderID string
	// OrderID is the OVH order id, when the orphan was found through an order.
	OrderID string
}

// SC-STUB(F09 04-AC-4)
// ScanConfig configures the orphan scan (plan 04 section 14.4).
type ScanConfig struct {
	Cluster string
	// InstallCreatedAt is the RFC 3339 instant passed as date.from to GET /me/order.
	InstallCreatedAt string
	// PlanCodes are the VPS plan codes of the catalogue.
	PlanCodes []string
	// StateIDs are the provider ids (service names) present in state.
	StateIDs []string
}

// SC-STUB(F09 04-AC-4)
// Scanner is the read-only OVH orphan scan.
type Scanner struct{}

// SC-STUB(F09 04-AC-4)
// NewScanner returns a scanner that reads through c.
func NewScanner(c *Client, cfg ScanConfig) *Scanner { return nil }

// SC-STUB(F09 04-AC-4)
// Scan returns delivered orphans, pending orders and delivered unnamed orders; it only issues GET requests.
func (s *Scanner) Scan(ctx context.Context) ([]Orphan, error) { return nil, nil }

// SC-STUB(F09 04-AC-5)
// AccessRule is one rule of GET /auth/currentCredential (auth.AccessRule).
type AccessRule struct {
	Method string `json:"method"`
	Path   string `json:"path"`
}

// SC-STUB(F09 04-AC-5)
// CurrentCredential is the response of GET /auth/currentCredential (auth.ApiCredential, reduced).
type CurrentCredential struct {
	Rules  []AccessRule `json:"rules"`
	Status string       `json:"status"`
}

// SC-STUB(F09 04-AC-5)
// CheckCredentialScope is PF-07 step 4: every rule must be within the plan 04 section 10 list. cloudProjectID is
// the OVH_CLOUD_PROJECT_ID of a public-cloud install, empty for vps.
func CheckCredentialScope(cred CurrentCredential, cloudProjectID string) provider.Finding {
	return provider.Finding{}
}

// SC-STUB(F09 04-AC-5)
// CredentialScope reads GET /auth/currentCredential through c and applies CheckCredentialScope.
func (c *Client) CredentialScope(ctx context.Context, cloudProjectID string) (provider.Finding, error) {
	return provider.Finding{}, nil
}

// SC-STUB(F09 04-AC-3)
// HardwareSpec is the catalogue hardware of the machine class, used before the SSH discovery has run.
type HardwareSpec struct {
	CPUCount    int
	MemoryBytes int64
	DiskBytes   int64
	DiskClass   string
}

// SC-STUB(F09 04-AC-3)
// NodeInput is the raw API material of one node.
type NodeInput struct {
	// Name is node-NN from the resource key.
	Name string
	// VPS is the body of GET /vps/{serviceName}; IPs the body of GET /vps/{serviceName}/ips.
	VPS      []byte
	IPs      []byte
	Hardware HardwareSpec
}

// SC-STUB(F09 04-AC-3)
// NormalizeInput is everything Normalize needs; GeneratedAt is injected (no wall clock).
type NormalizeInput struct {
	Cluster               string
	GeneratedAt           string
	ImplementationVersion string
	ProductLine           string
	Region                string
	MachineClass          string
	PlanCode              string
	Currency              string
	MonthlyTotal          string
	MaxMonthlyCost        string
	CatalogVersion        string
	StateSerial           int
	StateLineage          string
	Nodes                 []NodeInput
}

// SC-STUB(F09 04-AC-3)
// Normalize maps the OVH API responses to a ProviderOutput JSON document (plan 04 section 7.2).
func Normalize(in NormalizeInput) ([]byte, error) { return nil, nil }
