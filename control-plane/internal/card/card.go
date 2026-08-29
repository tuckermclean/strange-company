package card

import (
	"time"

	"github.com/google/uuid"
)

// Card is a unit of work tracked by the control plane, as described in spec
// section 5. It is the canonical, database-backed representation of a card;
// Vikunja mirrors a subset of it for human visibility (spec section 4.3).
//
// Pointer fields correspond to nullable columns in the cards table
// (control-plane/internal/store/migrations/0001_cards.sql).
type Card struct {
	ID                     uuid.UUID `json:"id"`
	VikunjaTaskID          *int64 `json:"vikunja_task_id,omitempty"`

	// VikunjaSyncedState is the state the reconciler last projected onto
	// the board. It is not workflow state -- it is how the reconciler tells
	// a board a human moved from a board that has not caught up yet. nil
	// means never synced.
	VikunjaSyncedState *string `json:"vikunja_synced_state,omitempty"`

	Title                  string `json:"title"`
	SourceType             string `json:"source_type"`
	SourceURL              *string `json:"source_url,omitempty"`
	SourceExternalID       *string `json:"source_external_id,omitempty"`
	RepoURL                *string `json:"repo_url,omitempty"`
	RepoBaseRef            *string `json:"repo_base_ref,omitempty"`
	Branch                 *string `json:"branch,omitempty"`
	State                  State `json:"state"`
	Phase                  Phase `json:"phase"`
	SpecURI                *string `json:"spec_uri,omitempty"`
	PlanURI                *string `json:"plan_uri,omitempty"`
	RiskClass              string `json:"risk_class"`
	EffectivePriority      int `json:"effective_priority"`
	ClaimedBy              *string `json:"claimed_by,omitempty"`
	LeaseExpiresAt         *time.Time `json:"lease_expires_at,omitempty"`
	ImplementationAttempt  int `json:"implementation_attempt"`
	InfrastructureFailures int `json:"infrastructure_failures"`
	MaxCostUSD             *float64 `json:"max_cost_usd,omitempty"`
	CostUSD                float64 `json:"cost_usd"`
	CreatedAt              time.Time `json:"created_at"`
	UpdatedAt              time.Time `json:"updated_at"`
}
