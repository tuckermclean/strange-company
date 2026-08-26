// Package specgate implements the deterministic specification gate described
// in docs/specs/strange-company-control-plane-v1.md section 10:
//
//	A Backlog card cannot become Ready merely because an LLM says it is
//	ready.
//
// Section 4.2 defines "Ready" as: specification exists, acceptance criteria
// exist, and all required dependencies are satisfied. Section 10 expands
// that into the full checklist this package evaluates: specification
// exists; every required spec section exists; at least one acceptance
// criterion exists; every criterion has a stated verification method;
// repository exists; dependencies are Done; permitted-actions policy
// exists.
//
// Every one of those is a question deterministic software can answer, which
// is exactly why section 2.4 forbids spending a model call on any of it:
// "No model call is permitted when deterministic software can answer the
// question ... is a dependency Done." This package therefore contains no
// model client, makes no network call, and computes no ambiguity score.
// Ambiguity scoring (section 10.1's cheap screening) is a separate,
// later concern and is explicitly not a substitute for the checks here —
// this gate runs regardless of how a card's ambiguity was screened, because
// even an unambiguous request can still be missing a repository, a
// verification method or a Done dependency.
//
// This package does not parse specification documents itself; that is
// package spec's job (see spec.Parse and spec.RequiredSections). Evaluate
// consumes the Problems that package already found and translates them into
// Failures an operator or a UI can act on, so there is exactly one place
// that understands what a well-formed spec document looks like.
package specgate

import (
	"fmt"
	"strings"

	"github.com/tuckermclean/strange-company/control-plane/internal/card"
	"github.com/tuckermclean/strange-company/control-plane/internal/spec"
)

// Reason is a stable, machine-comparable identifier for why a card failed
// the specification gate. UIs and policy code should switch on Reason, not
// on Failure.Detail, which is free-text meant for a human.
type Reason string

// The complete set of reasons the gate can report. These correspond 1:1 to
// the checklist in spec section 10.
const (
	// ReasonNoSpec means no specification document exists at all for this
	// card (Inputs.Spec is nil).
	ReasonNoSpec Reason = "SPEC_MISSING"

	// ReasonSpecIncomplete means the specification exists but is missing a
	// required section, or a required section is present but empty.
	ReasonSpecIncomplete Reason = "SPEC_INCOMPLETE"

	// ReasonNoCriteria means the specification has no acceptance criteria
	// at all.
	ReasonNoCriteria Reason = "NO_ACCEPTANCE_CRITERIA"

	// ReasonUnverifiableCriteria means at least one acceptance criterion
	// has no stated verification method.
	ReasonUnverifiableCriteria Reason = "CRITERION_WITHOUT_VERIFICATION"

	// ReasonNoRepo means the card names no repository to work in.
	ReasonNoRepo Reason = "REPO_MISSING"

	// ReasonDependenciesOpen means at least one card this card depends on
	// is not in the Done state.
	ReasonDependenciesOpen Reason = "DEPENDENCIES_NOT_DONE"

	// ReasonNoPermittedActions means no permitted-actions policy exists for
	// this card (spec section 5's permitted_actions block).
	ReasonNoPermittedActions Reason = "PERMITTED_ACTIONS_MISSING"
)

// Failure is one reason a card did not pass the gate. Detail is
// operator-facing free text naming the offending section, criterion or
// dependency, so a human (or a UI) can go fix the right thing without
// reading this package's source.
type Failure struct {
	Reason Reason
	Detail string
}

// Result is the outcome of evaluating a card against the specification
// gate. Passed is true if and only if Failures is empty — there is no
// partial-credit state, matching section 10's "cannot become Ready merely
// because an LLM says it is ready": either every deterministic requirement
// holds, or the card is not Ready.
type Result struct {
	Passed   bool
	Failures []Failure
}

// Error renders a stable, human-readable summary of every failure, in the
// same order they appear in Failures. It exists so a caller can log or
// surface the whole gate result with one call, and — since Evaluate always
// produces Failures in a fixed order for the same Inputs — repeated calls to
// Error on the same Result always produce the same string, which is what
// lets a UI diff results without flickering (spec 10).
func (r Result) Error() string {
	if len(r.Failures) == 0 {
		return "specification gate: passed"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "specification gate: %d failure(s):", len(r.Failures))
	for _, f := range r.Failures {
		b.WriteString("\n  - ")
		b.WriteString(string(f.Reason))
		if f.Detail != "" {
			b.WriteString(": ")
			b.WriteString(f.Detail)
		}
	}
	return b.String()
}

// Inputs is everything Evaluate needs to judge one card. Every field is
// pre-fetched by the caller — this package performs no I/O of its own, so
// that it can be tested (and reasoned about) as pure, deterministic logic.
type Inputs struct {
	// Card is the card being evaluated. Only its RepoURL is consulted here;
	// everything else the gate needs travels through the other fields.
	Card *card.Card

	// Spec is the parsed specification document for this card, or nil when
	// no specification exists yet.
	Spec *spec.Document

	// SpecProblems is whatever spec.Parse found wrong with Spec. It is
	// meaningless (and ignored) when Spec is nil: rule 1 of this package is
	// that a missing document is reported as ReasonNoSpec and nothing else
	// about the document is evaluated.
	SpecProblems []spec.Problem

	// Dependencies are the cards this card depends on. A nil or empty slice
	// means the card has no dependencies, which trivially satisfies the
	// dependency check.
	Dependencies []*card.Card

	// PermittedActions reports whether a permitted-actions policy exists
	// for this card (spec section 5's permitted_actions block). This
	// package does not know how to load or validate that policy; it only
	// asks whether one exists.
	PermittedActions bool
}

// Evaluate runs every deterministic check in spec section 10 against in and
// returns every failure found — never just the first — so that, exactly
// like the config loader (internal/config.Load) and the policy loader
// (internal/policy.Policy.Validate), an operator fixing a broken card sees
// the whole list in one pass instead of one failure per retry.
//
// Evaluate never panics: a nil Card, a nil Spec, nil slices and a nil entry
// within Dependencies are all treated as "requirement not met", never as a
// crash.
func Evaluate(in Inputs) Result {
	var failures []Failure

	// Rule 1: a missing specification is reported on its own. Nothing else
	// about the document — required sections, acceptance criteria, or
	// verification methods — is even evaluated, because there is no
	// document to evaluate it from.
	if in.Spec == nil {
		failures = append(failures, Failure{
			Reason: ReasonNoSpec,
			Detail: "no specification document exists for this card",
		})
	} else {
		// Grouped into three passes (rather than one pass switching on
		// Kind) so the reported order is always incomplete-sections, then
		// no-criteria, then unverifiable-criteria, regardless of what order
		// spec.Parse happened to discover them in. That keeps Evaluate's
		// output order fixed by this package's own rules, not by spec's
		// internal traversal order.
		for _, p := range in.SpecProblems {
			if p.Kind == "missing_section" || p.Kind == "empty_section" {
				failures = append(failures, Failure{Reason: ReasonSpecIncomplete, Detail: p.Detail})
			}
		}
		for _, p := range in.SpecProblems {
			if p.Kind == "no_criteria" {
				failures = append(failures, Failure{Reason: ReasonNoCriteria, Detail: p.Detail})
			}
		}
		for _, p := range in.SpecProblems {
			if p.Kind == "criterion_without_verification" {
				failures = append(failures, Failure{Reason: ReasonUnverifiableCriteria, Detail: p.Detail})
			}
		}
		// Anything spec.Parse reports that this package does not recognise still
		// blocks promotion. A gate that silently ignores problems it was not
		// taught about fails OPEN, which is the worst possible direction for it
		// to fail -- and spec.Parse already emits a "malformed" kind that none of
		// the passes above match. New problem kinds should make CI notice, not
		// quietly widen what counts as a valid specification.
		for _, p := range in.SpecProblems {
			switch p.Kind {
			case "missing_section", "empty_section", "no_criteria", "criterion_without_verification":
				// handled above
			default:
				failures = append(failures, Failure{
					Reason: ReasonSpecIncomplete,
					Detail: fmt.Sprintf("%s: %s", p.Kind, p.Detail),
				})
			}
		}
	}

	// Rule 5: the card must name a repository to work in.
	if repoURL(in.Card) == "" {
		failures = append(failures, Failure{
			Reason: ReasonNoRepo,
			Detail: "card has no repository URL (spec section 5: repo.url is required)",
		})
	}

	// Rule 6: every dependency must be Done. A card with no dependencies
	// passes this check by construction — the loop below simply does
	// nothing.
	for _, dep := range in.Dependencies {
		if dep == nil {
			continue
		}
		if dep.State != card.Done {
			failures = append(failures, Failure{
				Reason: ReasonDependenciesOpen,
				Detail: fmt.Sprintf("dependency %s is %s, not Done", dep.ID, dep.State),
			})
		}
	}

	// Rule 7: a permitted-actions policy must exist.
	if !in.PermittedActions {
		failures = append(failures, Failure{
			Reason: ReasonNoPermittedActions,
			Detail: "no permitted-actions policy exists for this card (spec section 5: permitted_actions)",
		})
	}

	return Result{
		Passed:   len(failures) == 0,
		Failures: failures,
	}
}

// repoURL safely extracts a trimmed repository URL from c, treating a nil
// Card, a nil RepoURL pointer, and a pointer to an empty or whitespace-only
// string identically as "no repository" (rule 5).
func repoURL(c *card.Card) string {
	if c == nil || c.RepoURL == nil {
		return ""
	}
	return strings.TrimSpace(*c.RepoURL)
}
