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
}

// ProviderPlan is what Plan returns and Apply executes exactly.
type ProviderPlan struct {
	Digest string
	Nodes  []Node
	// MonthlyCost is the catalogue estimate in the config currency (empty for providers without a price).
	MonthlyCost string
}

// ProviderOutput is the inventory (contracts/provider.schema.yaml, reduced).
type ProviderOutput struct {
	Cluster string
	Nodes   []Node
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
