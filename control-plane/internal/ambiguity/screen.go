// Package ambiguity implements the §10.1 "cheap ambiguity screening" step:
// the first model call anywhere in the control plane's pipeline.
//
// Everything before this point — parsing a /specs/<card-id>.md document
// (internal/spec), checking that every required section and acceptance
// criterion exists — is deterministic on purpose (spec §2.4: no model call
// is permitted when deterministic software can answer the question). This
// package is what comes after those checks pass mechanically but the
// specification's *content* might still be ambiguous, which is not a
// question deterministic software can answer.
//
// Spec §10.1 is emphatic about the shape of the model's job here: it "may
// classify" the specification's ambiguity on a 0-3 scale, and it "may
// recommend escalation." It may NOT resolve score 2-3 ambiguity by
// inventing requirements. In other words: the model may notice, but it may
// never decide and it may never author. This package enforces that
// boundary structurally, not just by convention:
//
//   - Screen fails closed. A model error, unparseable output, or an
//     out-of-range score never produces a Report — an error always does,
//     never a Report with Score 0 (spec §2.4/§2.5's spirit: a failed
//     screening is not evidence of "nothing ambiguous").
//   - Report carries only a rationale and findings — text that explains
//     what looks ambiguous, never anything shaped like a specification
//     section that could be mistaken for one. Screen never writes to, or
//     otherwise mutates, the *spec.Document it is given.
//   - RequiresHuman is derived from Score alone, by this package. Any
//     "recommendation", "approved", "proceed" or similar field the model's
//     JSON might contain is never even parsed, let alone consulted.
package ambiguity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/tuckermclean/strange-company/control-plane/internal/spec"
)

// Message is one turn of a chat-style completion request. It mirrors the
// shape declared by internal/modelclient so this package can depend on an
// interface rather than that package, per the M3 split of work.
type Message struct {
	Role    string
	Content string
}

// CompleteRequest is a single model completion request.
type CompleteRequest struct {
	Messages    []Message
	MaxTokens   int
	Temperature *float64
	JSONObject  bool
}

// Usage reports token accounting for a completion, for the cost ledger
// (spec §22).
type Usage struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
}

// Completion is a single model completion response.
type Completion struct {
	Text  string
	Model string
	Usage Usage
	Raw   []byte
}

// Completer performs a single model completion. It is declared here, not
// imported from internal/modelclient, so this package compiles and tests
// independently of that package's implementation.
type Completer interface {
	Complete(ctx context.Context, req CompleteRequest) (*Completion, error)
}

// Score is the §10.1 ambiguity classification.
type Score int

const (
	// ScoreMechanical means no material interpretation is required.
	ScoreMechanical Score = 0
	// ScoreMinorInterpretation means small judgment calls are needed, but
	// they are unlikely to change what "done" means.
	ScoreMinorInterpretation Score = 1
	// ScoreMaterialAmbiguity means real product/requirements questions
	// must be resolved before this specification can be implemented.
	ScoreMaterialAmbiguity Score = 2
	// ScoreFundamentalAmbiguity means the problem itself is unclear, or
	// the specification's requirements are contradictory or absent.
	ScoreFundamentalAmbiguity Score = 3
)

// Finding is one specific concern the model noticed. Section and Concern
// are both free-text explanations of *where* and *why* something looks
// ambiguous — never a proposed resolution, and never anything intended to
// be read back into a specification.
type Finding struct {
	Section string
	Concern string
}

// Report is the result of a §10.1 screening. It deliberately has no field
// that could carry specification text: only a rationale and findings that
// explain an ambiguity, and evidence (Raw) of the model call that produced
// them. Nothing on Report is ever written back into a *spec.Document —
// authoring a specification remains Fable's job, in a human conversation
// (spec §10.2), never this package's.
type Report struct {
	Score     Score
	Rationale string
	Findings  []Finding

	// Model is the model that produced this report, for the audit log
	// (spec §21) and cost ledger (spec §22).
	Model string
	Usage Usage

	// Raw is the model's raw completion, kept verbatim as evidence per
	// spec §20 (artifact type ambiguity-report). It is never parsed
	// again; it exists so a human can see exactly what the model said.
	Raw []byte
}

// RequiresHuman reports whether this screening result means the card must
// stay in Backlog or become Blocked with reason SPEC_REQUIRED, per spec
// §10.1: true for score 2 and 3.
//
// This is computed from Score alone. Any "recommendation", "approved",
// "proceed" or similarly-named field the model's JSON might have included
// is never parsed onto Report in the first place, so there is nothing here
// for such a field to override: the model cannot decide this question, only
// this package can.
func (r *Report) RequiresHuman() bool {
	return r.Score >= ScoreMaterialAmbiguity
}

// Sentinel errors. Screen always returns one of these (wrapped with
// details) when it cannot produce a trustworthy Report — see FAIL CLOSED
// in the package doc.
var (
	// ErrScreenFailed means the model call itself failed, or its output
	// could not be understood as the expected JSON shape at all.
	ErrScreenFailed = errors.New("ambiguity screen failed")

	// ErrInvalidScore means the model's JSON was parseable but its score
	// field was missing, non-numeric, or outside the valid 0-3 range.
	ErrInvalidScore = errors.New("ambiguity screen returned an invalid score")

	// ErrNoSpec means Screen was asked to screen a nil or empty document.
	// The model is never called in this case: spending a request to be
	// told an empty document is ambiguous would itself be spending
	// intelligence on a mechanical question (spec §2.4).
	ErrNoSpec = errors.New("no specification to screen")
)

// ambiguityScreeningPrompt is the system prompt for the §10.1 cheap
// ambiguity screen. It is a named constant, not text assembled at call
// sites, so it is reviewable and diffable as policy (spec §2.5's spirit:
// rules governing model behavior should be readable, not buried).
const ambiguityScreeningPrompt = `You are the cheap ambiguity screen described in section 10.1 of the Strange
Company control plane specification. A specification document is about to be
gated for promotion from Backlog to Ready. Your ONLY job is to classify how
ambiguous that document is. You do not decide anything, and you do not write
any part of a specification.

Score the document on this 0-3 scale:

  0 - mechanical: no material interpretation is required to implement this.
  1 - minor interpretation: small judgment calls are needed, but they are
      unlikely to change what "done" means.
  2 - material ambiguity: real product or requirements questions must be
      resolved before this can be implemented correctly.
  3 - fundamental ambiguity: the problem itself is unclear, or the
      specification's requirements are contradictory, missing, or
      self-defeating.

You MUST NOT resolve any ambiguity you find. You MUST NOT invent, guess, or
fill in requirements, acceptance criteria, interfaces, constraints, or any
other content that could substitute for a specification. Your role is to
notice ambiguity and describe it, never to decide it away. When you score a
document 2 or 3, a human will hold an interactive conversation with a
specification agent to resolve it (spec section 10.2) — never you, and never
by inference.

You may recommend escalation in your rationale. You may not approve, reject,
or otherwise decide whether work should proceed: that determination is made
mechanically from your score alone by software you cannot see or influence,
so do not bother including an "approved" or "proceed" field — it will be
ignored.

Respond with exactly one JSON object and nothing else — no prose before or
after it, no markdown code fence. Use exactly this shape:

{
  "score": <integer 0-3>,
  "rationale": "<one paragraph explaining the score>",
  "findings": [
    {"section": "<the section this concerns, or 'general'>", "concern": "<what is ambiguous and why>"}
  ]
}

"findings" may be an empty array if you have no specific findings beyond the
rationale. Every field must be present; "score" in particular is required
and must be one of 0, 1, 2, or 3.
`

// defaultMaxTokens bounds the cheap screen's completion. Classification and
// a short rationale do not need a large budget, and this is a cost-control
// concern (spec §22/§23), not a correctness one.
// Sized for a reasoning model, not for the answer.
//
// The screen's output is a small JSON object -- a score, a rationale and a few
// findings -- and 1024 was ample for that. But a reasoning model spends
// completion tokens thinking first, billed against the same budget, so a cap
// sized for the answer alone is spent before the answer starts: empty content,
// finish_reason "length", and every card stalled at the specification gate.
//
// Headroom is cheap here and the failure it prevents is total.
const defaultMaxTokens = 8192

// screeningTemperature is fixed low so the classification is as
// repeatable as a model call can be made to be; this is a classification
// task, not a creative one.
var screeningTemperature = 0.0

// Screener runs the §10.1 cheap ambiguity screen using a Completer.
type Screener struct {
	completer Completer
	model     string
}

// NewScreener builds a Screener that screens specifications using c,
// recording model as the model identifier attributed to its reports when
// the completion itself does not name one. model is a policy alias (spec
// §2.3), not application logic: this package never branches on its value.
func NewScreener(c Completer, model string) *Screener {
	return &Screener{completer: c, model: model}
}

// Screen classifies how ambiguous doc is, per spec §10.1.
//
// A nil or empty document returns ErrNoSpec without calling the model at
// all (spec §2.4: do not spend intelligence on a mechanical question). Any
// other failure — the model call erroring, the model's output not being
// recognizable JSON, or a JSON object with a missing, non-numeric, or
// out-of-range score — returns an error and never a Report. This is
// deliberate: a failed screen must not read as "nothing ambiguous" — that
// would silently promote exactly the cards that most need a human.
//
// Screen never mutates doc. It only reads doc.Raw to build the request.
func (s *Screener) Screen(ctx context.Context, doc *spec.Document) (*Report, error) {
	if doc == nil || len(doc.Raw) == 0 {
		return nil, ErrNoSpec
	}

	temp := screeningTemperature
	req := CompleteRequest{
		Messages: []Message{
			{Role: "system", Content: ambiguityScreeningPrompt},
			{Role: "user", Content: string(doc.Raw)},
		},
		MaxTokens:   defaultMaxTokens,
		Temperature: &temp,
		JSONObject:  true,
	}

	completion, err := s.completer.Complete(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("%w: model call failed: %v", ErrScreenFailed, err)
	}
	if completion == nil {
		return nil, fmt.Errorf("%w: model returned no completion", ErrScreenFailed)
	}

	jsonText, ok := extractJSONObject(completion.Text)
	if !ok {
		return nil, fmt.Errorf("%w: no JSON object found in model output: %q", ErrScreenFailed, completion.Text)
	}

	var parsed rawScreeningResult
	if err := json.Unmarshal([]byte(jsonText), &parsed); err != nil {
		return nil, fmt.Errorf("%w: model output was not valid JSON: %v", ErrScreenFailed, err)
	}

	score, err := parseScore(parsed.Score)
	if err != nil {
		return nil, err
	}

	findings := make([]Finding, 0, len(parsed.Findings))
	for _, f := range parsed.Findings {
		findings = append(findings, Finding{Section: f.Section, Concern: f.Concern})
	}

	model := completion.Model
	if model == "" {
		model = s.model
	}

	return &Report{
		Score:     score,
		Rationale: parsed.Rationale,
		Findings:  findings,
		Model:     model,
		Usage:     completion.Usage,
		Raw:       completion.Raw,
	}, nil
}

// rawFinding is the wire shape of one finding in the model's JSON.
type rawFinding struct {
	Section string `json:"section"`
	Concern string `json:"concern"`
}

// rawScreeningResult is the wire shape Screen expects the model's JSON
// object to have. It intentionally has no field for "recommendation",
// "approved", "proceed" or anything similar: such fields are not merely
// ignored by policy, they are structurally absent from what this package
// is even capable of decoding, so THE MODEL CANNOT DECIDE by any name it
// might choose for such a field.
//
// Score is decoded as json.RawMessage rather than int so parseScore can
// distinguish "absent" (spec requires this fail closed) from "present but
// the wrong shape" from "present as a numeric string" (spec: models do
// both, tolerate it), and reject anything else.
type rawScreeningResult struct {
	Score     json.RawMessage `json:"score"`
	Rationale string          `json:"rationale"`
	Findings  []rawFinding    `json:"findings"`
}

// parseScore decodes a score field that may be a JSON number or a numeric
// JSON string, and rejects anything else, including an absent field
// (len(raw) == 0, since json.Unmarshal never touches the field when its
// key is missing from the object) and any in-range-looking value outside
// 0..3.
func parseScore(raw json.RawMessage) (Score, error) {
	if len(raw) == 0 {
		return 0, fmt.Errorf("%w: \"score\" field is missing", ErrInvalidScore)
	}

	var n int
	if err := json.Unmarshal(raw, &n); err == nil {
		return validateScoreRange(n)
	}

	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		s = strings.TrimSpace(s)
		n, err := strconv.Atoi(s)
		if err != nil {
			return 0, fmt.Errorf("%w: score %q is not a number", ErrInvalidScore, s)
		}
		return validateScoreRange(n)
	}

	return 0, fmt.Errorf("%w: score field %q is neither a number nor a numeric string", ErrInvalidScore, string(raw))
}

// validateScoreRange rejects any score outside the 0..3 range §10.1
// defines. This is the ONLY place a Score value is manufactured from model
// output, so it is the single choke point that guarantees Screen never
// returns a Score outside that range.
func validateScoreRange(n int) (Score, error) {
	if n < int(ScoreMechanical) || n > int(ScoreFundamentalAmbiguity) {
		return 0, fmt.Errorf("%w: score %d is outside the valid range 0-3", ErrInvalidScore, n)
	}
	return Score(n), nil
}

// extractJSONObject finds the first balanced {...} object in s, tolerating
// surrounding prose and ```json code fences: it simply looks for the first
// '{' and returns everything up to its matching '}', respecting string
// literals (so a '}' inside a quoted string does not end the object early).
// ok is false if no balanced object is found at all.
func extractJSONObject(s string) (result string, ok bool) {
	start := strings.IndexByte(s, '{')
	if start == -1 {
		return "", false
	}

	depth := 0
	inString := false
	escaped := false

	for i := start; i < len(s); i++ {
		c := s[i]

		if inString {
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}

		switch c {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[start : i+1], true
			}
		}
	}

	return "", false
}
