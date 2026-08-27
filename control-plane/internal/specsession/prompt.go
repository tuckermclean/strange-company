// Package specsession assembles the opening context for the §10.2
// specification conversation.
//
// Everything here is deterministic. Choosing what a human is asked is not a
// model's job, and the same card must always open the same conversation, both
// so two runs are comparable and so the audit log can be reproduced.
package specsession

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/tuckermclean/strange-company/control-plane/internal/ambiguity"
	"github.com/tuckermclean/strange-company/control-plane/internal/card"
	"github.com/tuckermclean/strange-company/control-plane/internal/spec"
)

var (
	// ErrNoTitle is returned for a card with nothing to hold a
	// conversation about. An untitled session is also unfindable in the
	// dashboard, which is the entire handoff.
	ErrNoTitle = errors.New("specsession: card has no title")

	// ErrNoReport is returned when no ambiguity report is supplied.
	// Screening is what sends a card into this conversation, so opening
	// one without its report silently drops the reason a human was called
	// in at all.
	ErrNoReport = errors.New("specsession: an ambiguity report is required")
)

// Title names the conversation in the dashboard session list.
func Title(c *card.Card) string {
	if c == nil {
		return ""
	}
	return fmt.Sprintf("spec: %s", strings.TrimSpace(c.Title))
}

// BuildSystemPrompt assembles the system prompt seeding the conversation:
// the card, its repository, whatever draft specification exists, and the
// concerns screening already found.
//
// doc may be nil -- a card can reach this conversation with no draft at all.
// report may not: see ErrNoReport.
func BuildSystemPrompt(c *card.Card, doc *spec.Document, report *ambiguity.Report) (string, error) {
	if c == nil || strings.TrimSpace(c.Title) == "" {
		return "", ErrNoTitle
	}
	if report == nil {
		return "", ErrNoReport
	}

	var b strings.Builder

	b.WriteString("You are helping a human write the specification for one unit of work.\n\n")
	b.WriteString("The specification is checked by a deterministic gate and approved by a\n")
	b.WriteString("human. You do not decide when it is finished, and saying that it is\n")
	b.WriteString("does not make it so. Your job is to ask the questions that resolve the\n")
	b.WriteString("concerns below, and to write down what the human tells you.\n\n")

	b.WriteString("## Card\n\n")
	fmt.Fprintf(&b, "- Title: %s\n", strings.TrimSpace(c.Title))
	fmt.Fprintf(&b, "- ID: %s\n", c.ID)
	writeOptional(&b, "Repository", c.RepoURL)
	writeOptional(&b, "Base ref", c.RepoBaseRef)

	b.WriteString("\n## Concerns raised by screening\n\n")
	fmt.Fprintf(&b, "Ambiguity score: %d\n\n", int(report.Score))
	if r := strings.TrimSpace(report.Rationale); r != "" {
		fmt.Fprintf(&b, "%s\n\n", r)
	}
	if len(report.Findings) == 0 {
		b.WriteString("No specific findings were recorded.\n")
	}
	// Findings keep the order the screening produced; that order is part of
	// the evidence and is not this package's to re-rank.
	for _, f := range report.Findings {
		fmt.Fprintf(&b, "- **%s**: %s\n", strings.TrimSpace(f.Section), strings.TrimSpace(f.Concern))
	}

	if doc != nil {
		b.WriteString("\n## Draft specification so far\n\n")

		// Sections is a map. Walking it unsorted would word the same card's
		// conversation differently on every call.
		names := make([]string, 0, len(doc.Sections))
		for name := range doc.Sections {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			fmt.Fprintf(&b, "### %s\n\n%s\n\n", name, strings.TrimSpace(doc.Sections[name]))
		}

		if len(doc.Criteria) > 0 {
			b.WriteString("### Acceptance criteria recognised by the gate\n\n")
			for _, cr := range doc.Criteria {
				fmt.Fprintf(&b, "- %s: %s (verified by: %s)\n",
					strings.TrimSpace(cr.ID), strings.TrimSpace(cr.Text), strings.TrimSpace(cr.Verification))
			}
		}
	}

	return b.String(), nil
}

func writeOptional(b *strings.Builder, label string, value *string) {
	if value == nil || strings.TrimSpace(*value) == "" {
		return
	}
	fmt.Fprintf(b, "- %s: %s\n", label, strings.TrimSpace(*value))
}
