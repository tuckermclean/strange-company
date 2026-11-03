// Package provider is the compute-provider interface of plan 03 section 8: one implementation package per provider
// (ovh, existing, fake). Every operation is context-bound and returns typed errors with a Retryable classification.
package provider

import (
	"context"
	"errors"
)

// Capabilities describe what a provider can do (provider.schema.yaml, reduced to what the pipeline reads).
type Capabilities struct {
	Name string
	// Paid is true when Apply creates billable resources.
	Paid bool
	// CreatesNodes is true when Apply orders machines (false for the existing-nodes provider).
	CreatesNodes bool
}

// Config is the provider-facing slice of the normalized BootstrapConfig.
type Config struct {
	Cluster           string
	ExpectedNodeCount int
	Region            string
	MachineClass      string
}

// Finding is one preflight result row (PF-07, PF-08, PF-10, PF-11).
type Finding struct {
	ID      string
	Status  string // pass, warn or fail
	Code    string
	Message string
}

// Desired is the compute the pipeline asks for.
type Desired struct {
	Cluster   string
	NodeCount int
}

// Node is one node of a plan or an inventory. Names are node-01 .. node-09, stable for the life of the node.
type Node struct {
	Name       string
	InternalIP string
	// SSHHost, SSHPort and SSHUser are the SSH endpoint of a node the provider did not create (existing nodes); empty
	// for providers that create their nodes.
	SSHHost string
	SSHPort int
	SSHUser string
	// HostKeys are the SHA256 fingerprints of the node's SSH host keys (trust on first use or attested).
	HostKeys []string
	// Summary is a one-line description of the discovered machine for the confirmation footprint.
	Summary string
}

// ProviderPlan is what Plan returns and Apply executes exactly.
type ProviderPlan struct {
	Digest string
	Nodes  []Node
	// MonthlyCost is the catalogue estimate in the config currency (empty for providers without a price).
	MonthlyCost string
	// MachineClass is the catalogue productId the plan orders (empty for providers that order nothing from a catalogue).
	MachineClass string
	// Region is the region the plan creates nodes in, when the plan names it for the footprint (the recover plan).
	Region string
}

// ProviderOutput is the inventory (contracts/provider.schema.yaml, reduced).
type ProviderOutput struct {
	Cluster string
	Nodes   []Node
}

// Discovery is what a provider learned about its nodes by looking at them (plan 05 section 14, plan 18 section 9.2):
// the DNS resolvers a node uses, which provision writes into cluster-vars (SC_DNS_UPSTREAM_1..3, PF-28).
type Discovery struct {
	Resolvers []string
}

// Discoverer is implemented by providers that can report a Discovery after Apply.
type Discoverer interface {
	Discover(ctx context.Context) (*Discovery, error)
}

// Error is a provider failure with a stable code.
type Error struct {
	Code      string
	Message   string
	retryable bool
}

// NewError returns a provider error; retryable marks transient failures.
func NewError(code, message string, retryable bool) *Error {
	return &Error{Code: code, Message: message, retryable: retryable}
}

// Error returns the code and message.
func (e *Error) Error() string {
	if e.Message == "" {
		return e.Code
	}
	return e.Code + ": " + e.Message
}

// Retryable reports whether trying again can succeed.
func (e *Error) Retryable() bool { return e.retryable }

// IsRetryable reports whether err is a provider error marked retryable.
func IsRetryable(err error) bool {
	var pe *Error
	return errors.As(err, &pe) && pe.retryable
}

// Provider is the plan 03 section 8 interface.
type Provider interface {
	Capabilities() Capabilities
	// Validate runs the offline checks.
	Validate(cfg Config) error
	// Preflight runs the online read-only checks.
	Preflight(ctx context.Context, cfg Config) ([]Finding, error)
	// Plan produces the ProviderPlan; it mutates nothing.
	Plan(ctx context.Context, d Desired) (*ProviderPlan, error)
	// Apply applies exactly the confirmed plan.
	Apply(ctx context.Context, p *ProviderPlan) error
	// Inventory builds the ProviderOutput.
	Inventory(ctx context.Context) (*ProviderOutput, error)
	// OrphanScan lists resources of this install that are absent from state; a non-empty list blocks apply.
	OrphanScan(ctx context.Context) ([]string, error)
	// Expand plans with the node count raised; it may only add.
	Expand(ctx context.Context, targetCount int) (*ProviderPlan, error)
	// DestroyCompute produces a destroy-mode plan of the compute module only.
	DestroyCompute(ctx context.Context) (*ProviderPlan, error)
}

// StateNode is a compute node a provider holds in its managed state.
type StateNode struct {
	Name       string
	ProviderID string
	// Address is the plan address of the node (for example `fake_node.node["node-01"]`).
	Address string
}

// Recoverer is the optional capability of a provider the recover pipeline needs (plan 03 section 20.1, DR-12): to find
// the nodes whose API does not answer, move them out of managed state and plan replacements.
type Recoverer interface {
	// StateNodes lists the compute nodes of the managed state.
	StateNodes(ctx context.Context) ([]StateNode, error)
	// Probe asks the provider API for the node; an error means it does not answer (unreachable).
	Probe(ctx context.Context, n StateNode) error
	// Fence moves the node out of managed state (OpenTofu providers: `tofu state rm <address>`) and never reuses its name.
	Fence(ctx context.Context, n StateNode) error
	// PlanRecover is Plan for the recover pipeline: it keeps the nodes still in state, creates the missing ones under
	// new names in the configured region and judges the plan in guard recover mode with fenced, the plan addresses of
	// the fenced nodes.
	PlanRecover(ctx context.Context, d Desired, fenced []string) (*ProviderPlan, error)
}
