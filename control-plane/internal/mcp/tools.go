// tools.go is the §9 tool surface: the registry of every tool Hermes may
// call, the JSON schema Hermes sees for each one, and the handlers for the
// ones M2 actually implements.
//
// Every handler validates its own arguments before touching a CardService or
// the in-memory records, and reports a validation failure as a *ToolError
// naming the offending field -- never a bare error string, and never a
// silent zero-value success. cards.claim in particular treats an empty
// worker_id as an error rather than a claim: an unattributed claim would
// corrupt the audit log (spec section 21).
package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/tuckermclean/strange-company/control-plane/internal/card"
)

// ErrNotImplementedYet is returned by tools whose contract is declared in M2
// but whose implementation belongs to a later milestone: coding.* and
// verification.run to M3's coding pipeline, cost.* to M3/M7's cost ledger,
// and portfolio.*/scrum.get_policy to M7's management bots. Declaring the
// tool now, rather than omitting it, means Hermes discovers a stable "not
// implemented yet" through tools/list today instead of an "unknown tool"
// that would look like a typo once M3 ships and starts implementing it.
var ErrNotImplementedYet = errors.New("mcp: tool not implemented yet")

// defaultLease matches the 10-minute lease in spec section 6, used whenever
// a claim or heartbeat call omits lease_seconds.
const defaultLease = 600 * time.Second

// --- tool errors ---------------------------------------------------------

// ToolError is returned by a tool handler when its arguments fail
// validation. It always names the offending field, so a caller learns
// exactly what to fix instead of receiving an opaque failure.
type ToolError struct {
	Field   string
	Message string
}

func (e *ToolError) Error() string {
	return fmt.Sprintf("%s: %s", e.Field, e.Message)
}

func newToolError(field, message string) *ToolError {
	return &ToolError{Field: field, Message: message}
}

// decodeArgs decodes raw tool arguments into v. Empty/absent arguments are
// treated as "no fields set" rather than an error, since a tool with no
// required fields (e.g. cards.list_ready) must still accept a call with no
// arguments at all; per-field requirements are enforced separately by each
// handler.
func decodeArgs(raw json.RawMessage, v any) error {
	if len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return newToolError("arguments", fmt.Sprintf("must be a JSON object: %v", err))
	}
	return nil
}

// requireUUID validates that raw is a non-empty, well-formed UUID, naming
// field in the returned error otherwise.
func requireUUID(raw, field string) (uuid.UUID, error) {
	if strings.TrimSpace(raw) == "" {
		return uuid.Nil, newToolError(field, "is required")
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		return uuid.Nil, newToolError(field, fmt.Sprintf("must be a valid UUID: %v", err))
	}
	return id, nil
}

// requireString validates that raw is non-blank, naming field otherwise.
func requireString(raw, field string) error {
	if strings.TrimSpace(raw) == "" {
		return newToolError(field, "is required")
	}
	return nil
}

// --- registry --------------------------------------------------------------

// toolHandlerFunc is the shape of every tool implementation, including the
// not-yet-implemented stubs.
type toolHandlerFunc func(ctx context.Context, s *Server, raw json.RawMessage) (any, error)

// toolSpec is one registered tool: its public descriptor plus the handler
// that implements it.
type toolSpec struct {
	Name        string
	Description string
	Schema      map[string]any
	Handler     toolHandlerFunc
}

// ToolDescriptor is the JSON shape returned by tools/list.
type ToolDescriptor struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

func schemaObject(properties map[string]any, required ...string) map[string]any {
	if required == nil {
		required = []string{}
	}
	return map[string]any{
		"type":       "object",
		"properties": properties,
		"required":   required,
	}
}

func stringProp(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}

func intProp(description string) map[string]any {
	return map[string]any{"type": "integer", "description": description}
}

// stubTool builds a toolSpec for a tool that is declared now (spec section
// 9's contract) but not implemented until a later milestone (see
// ErrNotImplementedYet).
func stubTool(name, description string, schema map[string]any) toolSpec {
	return toolSpec{Name: name, Description: description, Schema: schema, Handler: handleNotImplemented}
}

func handleNotImplemented(_ context.Context, _ *Server, _ json.RawMessage) (any, error) {
	return nil, ErrNotImplementedYet
}

// toolRegistry is the entire §9 tool surface -- the single source of truth
// ToolNames, tools/list and tools/call all read from. Nothing reachable
// through this server exists outside this slice.
var toolRegistry = []toolSpec{
	{
		Name:        "cards.list_ready",
		Description: "List every card currently in the Ready state: the deterministic queue Hermes chooses work from.",
		Schema:      schemaObject(map[string]any{}),
		Handler:     handleCardsListReady,
	},
	{
		Name:        "cards.get",
		Description: "Fetch one card by id.",
		Schema: schemaObject(map[string]any{
			"card_id": stringProp("UUID of the card to fetch."),
		}, "card_id"),
		Handler: handleCardsGet,
	},
	{
		Name:        "cards.claim",
		Description: "Atomically claim one claimable Ready card for worker_id. Returns no_work=true, not an error, when the queue is empty.",
		Schema: schemaObject(map[string]any{
			"worker_id":     stringProp("Identity of the claiming worker. Required: an unattributed claim would corrupt the audit log."),
			"lease_seconds": intProp("Lease duration in seconds. Defaults to 600."),
		}, "worker_id"),
		Handler: handleCardsClaim,
	},
	{
		Name:        "cards.heartbeat",
		Description: "Extend a held card's lease.",
		Schema: schemaObject(map[string]any{
			"card_id":       stringProp("UUID of the claimed card."),
			"worker_id":     stringProp("Identity of the claiming worker; must match the current claimant."),
			"lease_seconds": intProp("New lease duration in seconds. Defaults to 600."),
		}, "card_id", "worker_id"),
		Handler: handleCardsHeartbeat,
	},
	{
		Name:        "cards.release",
		Description: "Release a held card's claim back to Ready.",
		Schema: schemaObject(map[string]any{
			"card_id":   stringProp("UUID of the claimed card."),
			"worker_id": stringProp("Identity of the claiming worker; must match the current claimant."),
			"reason":    stringProp("Why the card is being released."),
		}, "card_id", "worker_id"),
		Handler: handleCardsRelease,
	},
	{
		Name:        "cards.transition",
		Description: "Move a card to a new state, subject to the card state machine (card.CanTransition). " +
			"The transition is always recorded as an agent: human-only moves (Review -> Done, Blocked -> Ready, " +
			"NeedsHuman -> Ready) are not reachable through this interface and will be refused.",
		Schema: schemaObject(map[string]any{
			"card_id":    stringProp("UUID of the card to transition."),
			"to":         stringProp("Target state, e.g. Review, Done, Blocked, NeedsHuman."),
			"actor_id": stringProp("Identity of the agent requesting the transition."),
			"reason":   stringProp("Why the transition is happening."),
		}, "card_id", "to", "actor_id"),
		Handler: handleCardsTransition,
	},
	{
		Name:        "cards.comment",
		Description: "Attach a human-readable comment to a card. M2 stores comments in an in-memory map, not Postgres; a durable table is a later migration.",
		Schema: schemaObject(map[string]any{
			"card_id": stringProp("UUID of the card."),
			"author":  stringProp("Who is commenting."),
			"body":    stringProp("Comment text."),
		}, "card_id", "author", "body"),
		Handler: handleCardsComment,
	},
	{
		Name:        "cards.request_human",
		Description: "Escalate a card to NeedsHuman.",
		Schema: schemaObject(map[string]any{
			"card_id":  stringProp("UUID of the card."),
			"reason":   stringProp("Why human attention is required."),
			"actor_id": stringProp(`Identity of the requesting agent. Defaults to "hermes".`),
		}, "card_id", "reason"),
		Handler: handleCardsRequestHuman,
	},
	{
		Name:        "artifacts.attach",
		Description: "Attach an artifact (spec, plan, diff, test output, etc.) to a card. M2 stores artifacts in an in-memory map, not Postgres; a durable table is a later migration.",
		Schema: schemaObject(map[string]any{
			"card_id":      stringProp("UUID of the card."),
			"type":         stringProp("Artifact type, e.g. spec, implementation-plan, diff, test-output."),
			"content":      stringProp("Artifact content, when small enough to store inline."),
			"content_type": stringProp("MIME type of content."),
			"storage_uri":  stringProp("External storage location, when content is not stored inline."),
			"actor":        stringProp("Who produced the artifact."),
		}, "card_id", "type"),
		Handler: handleArtifactsAttach,
	},
	{
		Name:        "artifacts.list",
		Description: "List every artifact attached to a card.",
		Schema: schemaObject(map[string]any{
			"card_id": stringProp("UUID of the card."),
		}, "card_id"),
		Handler: handleArtifactsList,
	},

	// The tools below are declared now so the contract Hermes sees is
	// stable, but M2 does not implement them: coding.* and
	// verification.run belong to M3's coding pipeline, cost.* to
	// M3/M7's cost ledger, and portfolio.*/scrum.get_policy to M7's
	// management bots. Implementing any of them now would be
	// speculative -- there is no coding pipeline, cost ledger or
	// management bot yet for them to drive.
	stubTool("coding.plan", "Produce an implementation plan for a card. Not implemented until M3.",
		schemaObject(map[string]any{"card_id": stringProp("UUID of the card.")}, "card_id")),
	stubTool("coding.write_tests", "Write acceptance tests for a card. Not implemented until M3.",
		schemaObject(map[string]any{"card_id": stringProp("UUID of the card.")}, "card_id")),
	stubTool("coding.implement",
		"Invoke a coding agent to implement a card. The control plane -- not the caller -- decides pod, image, credentials, model and sandbox. Not implemented until M3.",
		schemaObject(map[string]any{"card_id": stringProp("UUID of the card.")}, "card_id")),
	stubTool("coding.review", "Run automated review over a card's implementation. Not implemented until M3.",
		schemaObject(map[string]any{"card_id": stringProp("UUID of the card.")}, "card_id")),
	stubTool("verification.run", "Run a card's deterministic verification commands. Not implemented until M3.",
		schemaObject(map[string]any{"card_id": stringProp("UUID of the card.")}, "card_id")),
	stubTool("cost.get_card", "Get the cost ledger for a card. Not implemented until M3/M7.",
		schemaObject(map[string]any{"card_id": stringProp("UUID of the card.")}, "card_id")),
	stubTool("cost.get_run", "Get the cost ledger for a single model-invoking run. Not implemented until M3/M7.",
		schemaObject(map[string]any{"run_id": stringProp("UUID of the run.")}, "run_id")),
	stubTool("portfolio.scan", "Run the weekly portfolio scan and produce MOVE recommendations. Not implemented until M7.",
		schemaObject(map[string]any{})),
	stubTool("portfolio.get_policy", "Get the versioned portfolio rubric. Not implemented until M7.",
		schemaObject(map[string]any{})),
	stubTool("scrum.get_policy", "Get the versioned scrum policy. Not implemented until M7.",
		schemaObject(map[string]any{})),
}

// toolIndex is built once at package init for O(1) tools/call dispatch. The
// panic on a duplicate name is a programmer-error guard, not something a
// caller can trigger: it would only fire if two entries in the literal
// toolRegistry above shared a name.
var toolIndex = func() map[string]toolSpec {
	idx := make(map[string]toolSpec, len(toolRegistry))
	for _, t := range toolRegistry {
		if _, dup := idx[t.Name]; dup {
			panic(fmt.Sprintf("mcp: duplicate tool name %q registered", t.Name))
		}
		idx[t.Name] = t
	}
	return idx
}()

func lookupTool(name string) (toolSpec, bool) {
	t, ok := toolIndex[name]
	return t, ok
}

// ToolNames returns the name of every registered tool, sorted. It is the
// enforcement point for spec section 9's security boundary: Hermes can only
// ever reach a tool listed here, which is what lets
// TestTheToolSurfaceIsExactlyTheSpecifiedSet fail loudly the instant anyone
// registers a kubernetes.*, exec, shell, or generic HTTP tool.
func ToolNames() []string {
	names := make([]string, 0, len(toolRegistry))
	for _, t := range toolRegistry {
		names = append(names, t.Name)
	}
	sort.Strings(names)
	return names
}

func toolDescriptors() []ToolDescriptor {
	names := ToolNames()
	out := make([]ToolDescriptor, 0, len(names))
	for _, name := range names {
		t := toolIndex[name]
		out = append(out, ToolDescriptor{Name: t.Name, Description: t.Description, InputSchema: t.Schema})
	}
	return out
}

// --- cards.* handlers --------------------------------------------------

func handleCardsListReady(ctx context.Context, s *Server, raw json.RawMessage) (any, error) {
	var args struct{}
	if err := decodeArgs(raw, &args); err != nil {
		return nil, err
	}

	all, err := s.cards.ListCards(ctx)
	if err != nil {
		return nil, err
	}
	ready := make([]*card.Card, 0, len(all))
	for _, c := range all {
		if c.State == card.Ready {
			ready = append(ready, c)
		}
	}
	return cardsListResult{Cards: ready}, nil
}

type cardsListResult struct {
	Cards []*card.Card `json:"cards"`
}

type cardsGetArgs struct {
	CardID string `json:"card_id"`
}

func handleCardsGet(ctx context.Context, s *Server, raw json.RawMessage) (any, error) {
	var args cardsGetArgs
	if err := decodeArgs(raw, &args); err != nil {
		return nil, err
	}
	id, err := requireUUID(args.CardID, "card_id")
	if err != nil {
		return nil, err
	}

	c, err := s.cards.GetCard(ctx, id)
	if err != nil {
		return nil, err
	}
	return c, nil
}

type cardsClaimArgs struct {
	WorkerID     string `json:"worker_id"`
	LeaseSeconds int    `json:"lease_seconds"`
}

// cardsClaimResult is the cards.claim result shape. NoWork is always
// present so a caller can tell "no card" (no_work=true) apart from a
// malformed response; Card is only set on an actual claim.
type cardsClaimResult struct {
	Card   *card.Card `json:"card,omitempty"`
	NoWork bool       `json:"no_work"`
}

func handleCardsClaim(ctx context.Context, s *Server, raw json.RawMessage) (any, error) {
	var args cardsClaimArgs
	if err := decodeArgs(raw, &args); err != nil {
		return nil, err
	}
	// An empty worker_id is an error, not a claim: an unattributed claim
	// would corrupt the audit log (spec section 21).
	if err := requireString(args.WorkerID, "worker_id"); err != nil {
		return nil, err
	}

	lease := defaultLease
	if args.LeaseSeconds > 0 {
		lease = time.Duration(args.LeaseSeconds) * time.Second
	}

	c, err := s.cards.ClaimReady(ctx, args.WorkerID, lease)
	if err != nil {
		if errors.Is(err, ErrNoWork) {
			return cardsClaimResult{NoWork: true}, nil
		}
		return nil, err
	}
	return cardsClaimResult{Card: c}, nil
}

type cardsHeartbeatArgs struct {
	CardID       string `json:"card_id"`
	WorkerID     string `json:"worker_id"`
	LeaseSeconds int    `json:"lease_seconds"`
}

type ackResult struct {
	Status string `json:"status"`
}

func handleCardsHeartbeat(ctx context.Context, s *Server, raw json.RawMessage) (any, error) {
	var args cardsHeartbeatArgs
	if err := decodeArgs(raw, &args); err != nil {
		return nil, err
	}
	id, err := requireUUID(args.CardID, "card_id")
	if err != nil {
		return nil, err
	}
	if err := requireString(args.WorkerID, "worker_id"); err != nil {
		return nil, err
	}

	lease := defaultLease
	if args.LeaseSeconds > 0 {
		lease = time.Duration(args.LeaseSeconds) * time.Second
	}

	if err := s.cards.Heartbeat(ctx, id, args.WorkerID, lease); err != nil {
		return nil, err
	}
	return ackResult{Status: "ok"}, nil
}

type cardsReleaseArgs struct {
	CardID   string `json:"card_id"`
	WorkerID string `json:"worker_id"`
	Reason   string `json:"reason"`
}

func handleCardsRelease(ctx context.Context, s *Server, raw json.RawMessage) (any, error) {
	var args cardsReleaseArgs
	if err := decodeArgs(raw, &args); err != nil {
		return nil, err
	}
	id, err := requireUUID(args.CardID, "card_id")
	if err != nil {
		return nil, err
	}
	if err := requireString(args.WorkerID, "worker_id"); err != nil {
		return nil, err
	}

	if err := s.cards.Release(ctx, id, args.WorkerID, args.Reason); err != nil {
		return nil, err
	}
	return ackResult{Status: "released"}, nil
}

type cardsTransitionArgs struct {
	CardID    string `json:"card_id"`
	To        string `json:"to"`
	// ActorType is deliberately absent: see handleCardsTransition.
	ActorID string `json:"actor_id"`
	Reason    string `json:"reason"`
}

func handleCardsTransition(ctx context.Context, s *Server, raw json.RawMessage) (any, error) {
	var args cardsTransitionArgs
	if err := decodeArgs(raw, &args); err != nil {
		return nil, err
	}
	id, err := requireUUID(args.CardID, "card_id")
	if err != nil {
		return nil, err
	}
	if err := requireString(args.To, "to"); err != nil {
		return nil, err
	}
	if err := requireString(args.ActorID, "actor_id"); err != nil {
		return nil, err
	}

	// Stamped, never taken from the caller. Everything reaching the control
	// plane through MCP is an agent, and a model able to name itself
	// "human" would inherit the entire human-only column of the state
	// machine -- §18's "automated review cannot move a card to Done", and
	// the rule that an agent must never un-block itself.
	if err := s.cards.Transition(ctx, id, card.State(args.To), card.ActorAgent, args.ActorID, args.Reason); err != nil {
		// card.CanTransition's error already says exactly which rule
		// fired and why; surface it verbatim (on the "to" field, since
		// that is what made the request illegal) rather than hiding it
		// behind a generic failure, so the caller learns WHY.
		if errors.Is(err, card.ErrIllegalTransition) {
			return nil, newToolError("to", err.Error())
		}
		return nil, err
	}

	c, err := s.cards.GetCard(ctx, id)
	if err != nil {
		return nil, err
	}
	return c, nil
}

type cardsCommentArgs struct {
	CardID string `json:"card_id"`
	Author string `json:"author"`
	Body   string `json:"body"`
}

func handleCardsComment(ctx context.Context, s *Server, raw json.RawMessage) (any, error) {
	var args cardsCommentArgs
	if err := decodeArgs(raw, &args); err != nil {
		return nil, err
	}
	id, err := requireUUID(args.CardID, "card_id")
	if err != nil {
		return nil, err
	}
	if err := requireString(args.Author, "author"); err != nil {
		return nil, err
	}
	if err := requireString(args.Body, "body"); err != nil {
		return nil, err
	}

	if _, err := s.cards.GetCard(ctx, id); err != nil {
		return nil, err
	}

	c := Comment{
		ID:        uuid.New(),
		CardID:    id,
		Author:    args.Author,
		Body:      args.Body,
		CreatedAt: time.Now().UTC(),
	}
	s.records.addComment(c)
	return c, nil
}

type cardsRequestHumanArgs struct {
	CardID  string `json:"card_id"`
	Reason  string `json:"reason"`
	ActorID string `json:"actor_id"`
}

// defaultEscalationActorID names the actor recorded on a
// cards.request_human transition when the caller doesn't supply one.
const defaultEscalationActorID = "hermes"

func handleCardsRequestHuman(ctx context.Context, s *Server, raw json.RawMessage) (any, error) {
	var args cardsRequestHumanArgs
	if err := decodeArgs(raw, &args); err != nil {
		return nil, err
	}
	id, err := requireUUID(args.CardID, "card_id")
	if err != nil {
		return nil, err
	}
	// An escalation with no reason leaves the human with nothing to act
	// on, so it is rejected the same way an unattributed claim is.
	if err := requireString(args.Reason, "reason"); err != nil {
		return nil, err
	}

	actorID := strings.TrimSpace(args.ActorID)
	if actorID == "" {
		actorID = defaultEscalationActorID
	}

	if err := s.cards.Transition(ctx, id, card.NeedsHuman, card.ActorAgent, actorID, args.Reason); err != nil {
		if errors.Is(err, card.ErrIllegalTransition) {
			return nil, newToolError("card_id", err.Error())
		}
		return nil, err
	}

	c, err := s.cards.GetCard(ctx, id)
	if err != nil {
		return nil, err
	}
	return c, nil
}

// --- artifacts.* handlers ------------------------------------------------

type artifactsAttachArgs struct {
	CardID      string `json:"card_id"`
	Type        string `json:"type"`
	Content     string `json:"content"`
	ContentType string `json:"content_type"`
	StorageURI  string `json:"storage_uri"`
	Actor       string `json:"actor"`
}

func handleArtifactsAttach(ctx context.Context, s *Server, raw json.RawMessage) (any, error) {
	var args artifactsAttachArgs
	if err := decodeArgs(raw, &args); err != nil {
		return nil, err
	}
	id, err := requireUUID(args.CardID, "card_id")
	if err != nil {
		return nil, err
	}
	if err := requireString(args.Type, "type"); err != nil {
		return nil, err
	}
	if strings.TrimSpace(args.Content) == "" && strings.TrimSpace(args.StorageURI) == "" {
		return nil, newToolError("content", "either content or storage_uri is required")
	}

	if _, err := s.cards.GetCard(ctx, id); err != nil {
		return nil, err
	}

	var sha string
	if args.Content != "" {
		sum := sha256.Sum256([]byte(args.Content))
		sha = hex.EncodeToString(sum[:])
	}

	a := Artifact{
		ID:          uuid.New(),
		CardID:      id,
		Type:        args.Type,
		ContentType: args.ContentType,
		Actor:       args.Actor,
		StorageURI:  args.StorageURI,
		SHA256:      sha,
		CreatedAt:   time.Now().UTC(),
	}
	s.records.addArtifact(a)
	return a, nil
}

type artifactsListArgs struct {
	CardID string `json:"card_id"`
}

type artifactsListResult struct {
	Artifacts []Artifact `json:"artifacts"`
}

func handleArtifactsList(ctx context.Context, s *Server, raw json.RawMessage) (any, error) {
	var args artifactsListArgs
	if err := decodeArgs(raw, &args); err != nil {
		return nil, err
	}
	id, err := requireUUID(args.CardID, "card_id")
	if err != nil {
		return nil, err
	}

	if _, err := s.cards.GetCard(ctx, id); err != nil {
		return nil, err
	}

	return artifactsListResult{Artifacts: s.records.listArtifacts(id)}, nil
}
