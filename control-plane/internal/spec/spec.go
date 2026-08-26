// Package spec parses /specs/<card-id>.md documents and answers a single,
// deterministic question: is this document a specification, or a wish?
//
// Spec §10 (Specification Gate) forbids promoting a Backlog card to Ready
// on an LLM's say-so; the checks are deterministic software, not a model
// call. Spec §2.4 makes the same point generally: no model call is
// permitted when deterministic software can answer the question. Whether a
// document has every required §10.2 section, and whether every acceptance
// criterion states how it will be verified, is exactly the kind of thing
// software can answer on its own — so this package answers it, and nothing
// in here ever calls a model.
package spec

import (
	"fmt"
	"regexp"
	"strings"
)

// requiredSections lists the sections §10.2 requires Fable to produce in
// /specs/<card-id>.md, in that order. The order only governs iteration
// (RequiredSections, and the sequence in which missing_section problems
// are reported); heading matching itself does not care what order
// sections appear in the document.
var requiredSections = []string{
	"Context",
	"Task",
	"Evidence available",
	"Interfaces",
	"Constraints",
	"Invariants",
	"Permitted actions",
	"Forbidden actions",
	"Acceptance criteria",
	"Out of scope",
	"Failure behavior",
}

// acceptanceCriteriaSection is the canonical Sections key whose body is
// parsed into Criteria.
const acceptanceCriteriaSection = "Acceptance criteria"

// RequiredSections returns the section names §10.2 requires, in the order
// Fable is expected to produce them. Callers get a fresh slice each call so
// they cannot mutate the package's own notion of what is required.
func RequiredSections() []string {
	out := make([]string, len(requiredSections))
	copy(out, requiredSections)
	return out
}

// normalizedRequired maps each required section's normalized heading text
// to its canonical name, so heading matching is a single map lookup.
var normalizedRequired = buildNormalizedRequired()

func buildNormalizedRequired() map[string]string {
	m := make(map[string]string, len(requiredSections))
	for _, s := range requiredSections {
		m[normalizeHeading(s)] = s
	}
	return m
}

// nonAlnumSpace matches any run of characters that are not a lowercase
// letter, digit or space. Heading matching must be punctuation-insensitive
// (spec: "## Acceptance Criteria" and "### acceptance criteria:" both
// match), so these are replaced with a space rather than deleted outright —
// deleting them would glue "Evidence-available" into "evidenceavailable"
// and break the match.
var nonAlnumSpace = regexp.MustCompile(`[^a-z0-9 ]+`)

// multiSpace collapses whitespace runs left behind by nonAlnumSpace.
var multiSpace = regexp.MustCompile(`\s+`)

// normalizeHeading canonicalizes heading text for case- and
// punctuation-insensitive comparison.
func normalizeHeading(s string) string {
	s = strings.ToLower(s)
	s = nonAlnumSpace.ReplaceAllString(s, " ")
	s = multiSpace.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

// headingPattern matches an ATX markdown heading at any level (spec:
// "at any level (#, ##, ###)"). It intentionally does not require the
// content after the hashes to be non-empty, so a bare "###" boundary still
// closes whatever section came before it.
var headingPattern = regexp.MustCompile(`^(#{1,6})\s+(.*)$`)

// Criterion is one acceptance criterion parsed from a specification's
// Acceptance criteria section. Per §10, a criterion nobody can verify is
// not an acceptance criterion at all, so Verification being empty is
// always surfaced as a Problem — see (*Document).Problems.
type Criterion struct {
	ID           string
	Text         string
	Verification string
}

// Document is what Parse manages to understand from a specification
// document, whether or not that document is actually complete. Parse
// always returns a usable *Document, even a mostly-empty one, so a caller
// can show an operator what WAS understood alongside what is missing.
type Document struct {
	CardID string

	// Sections maps a canonical required-section name (see
	// RequiredSections) to that section's body text. A required section
	// with no matching heading anywhere in the document simply has no
	// entry here; a heading that matched but had an empty or
	// whitespace-only body IS present here, mapped to that empty body —
	// Problems() is what turns that into an empty_section problem, not
	// the absence of the map entry.
	Sections map[string]string

	Criteria []Criterion

	// Raw is the exact bytes Parse was given, kept for audit and so a
	// parsing mistake here can be diagnosed against the original
	// document rather than a lossy summary of it.
	Raw []byte
}

// Problem is one deterministic reason a specification document fails the
// gate described in spec §10.
type Problem struct {
	// Kind is one of: "missing_section", "no_criteria",
	// "criterion_without_verification", "empty_section", "malformed".
	Kind string

	// Detail is operator-facing and names the offending thing: which
	// section, which criterion, what was malformed.
	Detail string
}

// Parse reads a specification document and reports every deterministic
// problem with it per spec §10 and §10.2. It never panics, on any input —
// nil, empty, binary junk, or a document that is only headings — and it
// never returns a nil Document: even a document that fails every check
// still comes back as a Document recording whatever Parse could
// understand, alongside the Problems explaining what is missing.
func Parse(cardID string, doc []byte) (*Document, []Problem) {
	d := &Document{
		CardID:   cardID,
		Sections: make(map[string]string),
		Raw:      doc,
	}

	type sectionSpan struct {
		canonical string
		bodyLines []string
	}

	var current *sectionSpan
	var spans []sectionSpan

	flush := func() {
		if current != nil {
			spans = append(spans, *current)
			current = nil
		}
	}

	for _, rawLine := range strings.Split(string(doc), "\n") {
		line := strings.TrimRight(rawLine, "\r")
		trimmedLeft := strings.TrimLeft(line, " \t")

		if m := headingPattern.FindStringSubmatch(trimmedLeft); m != nil {
			headingText := strings.TrimSpace(m[2])
			// Tolerate a closing ATX sequence, e.g. "## Heading ##".
			headingText = strings.TrimRight(headingText, "# \t")
			norm := normalizeHeading(headingText)

			flush()
			if canon, ok := normalizedRequired[norm]; ok {
				current = &sectionSpan{canonical: canon}
			}
			// An unrecognized heading still ends whatever section came
			// before it; it just doesn't start a new tracked one.
			continue
		}

		if current != nil {
			current.bodyLines = append(current.bodyLines, line)
		}
	}
	flush()

	for _, sp := range spans {
		// If a required heading appears more than once, the last
		// occurrence wins. Fable is not expected to emit a section
		// twice; picking one deterministically beats silently
		// concatenating two unrelated bodies together.
		d.Sections[sp.canonical] = strings.Join(sp.bodyLines, "\n")
	}

	d.Criteria = parseCriteria(d.Sections[acceptanceCriteriaSection])

	return d, d.Problems()
}

// Problems re-derives every deterministic problem with d from its already
// -parsed Sections and Criteria (and, for malformed-table detection, the
// raw Acceptance criteria section text). Parse calls this itself so its
// two return values are always consistent, but callers may also call it
// again later against a Document they already have.
func (d *Document) Problems() []Problem {
	var problems []Problem

	for _, name := range requiredSections {
		body, ok := d.Sections[name]
		if !ok {
			problems = append(problems, Problem{
				Kind:   "missing_section",
				Detail: fmt.Sprintf("required section %q is missing (spec §10.2)", name),
			})
			continue
		}
		if strings.TrimSpace(body) == "" {
			problems = append(problems, Problem{
				Kind:   "empty_section",
				Detail: fmt.Sprintf("section %q is present but has no content (spec §10)", name),
			})
		}
	}

	if acBody, ok := d.Sections[acceptanceCriteriaSection]; ok {
		if strings.TrimSpace(acBody) != "" {
			if p := malformedTableProblem(acBody); p != nil {
				problems = append(problems, *p)
			}
		}
	}

	if len(d.Criteria) == 0 {
		problems = append(problems, Problem{
			Kind:   "no_criteria",
			Detail: "no acceptance criteria were found (spec §10)",
		})
	}

	for i, c := range d.Criteria {
		if strings.TrimSpace(c.Verification) != "" {
			continue
		}
		name := c.ID
		if name == "" {
			name = c.Text
		}
		if name == "" {
			name = fmt.Sprintf("criterion #%d", i+1)
		}
		problems = append(problems, Problem{
			Kind:   "criterion_without_verification",
			Detail: fmt.Sprintf("acceptance criterion %q has no stated verification method (spec §10)", name),
		})
	}

	return problems
}

// parseCriteria parses the body of an Acceptance criteria section into
// Criterion values, accepting either shape the spec allows: a markdown
// list (parseCriteriaList) or a markdown table (parseCriteriaTable). A
// table takes precedence when one is present.
func parseCriteria(body string) []Criterion {
	if strings.TrimSpace(body) == "" {
		return nil
	}
	if rows, ok := parseCriteriaTable(body); ok {
		return rows
	}
	return parseCriteriaList(body)
}

// bulletPattern matches one markdown list item: "-", "*", "+", or a
// numbered item like "1." / "2)".
var bulletPattern = regexp.MustCompile(`^(?:[-*+]|\d+[.)])\s+(.*)$`)

func parseCriteriaList(body string) []Criterion {
	var out []Criterion
	for _, raw := range strings.Split(body, "\n") {
		line := strings.TrimSpace(raw)
		m := bulletPattern.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		content := m[1]
		text, verification := splitCriterionLine(content)
		id, text := extractID(text)
		out = append(out, Criterion{ID: id, Text: text, Verification: verification})
	}
	return out
}

// verificationLabelPattern matches an explicit verification label,
// consuming any preceding dash separator along with it, e.g.
// "... widget — verified by: `cmd`" splits before "—" because the label
// itself is the strongest, least ambiguous signal for where the
// verification method starts.
var verificationLabelPattern = regexp.MustCompile(`(?i)\s*(?:—|--)?\s*(?:verified\s+by|verification)\s*:\s*`)

// dashSeparatorPattern matches a bare em-dash or double-hyphen separator,
// used when no explicit "verified by:"/"verification:" label is present.
var dashSeparatorPattern = regexp.MustCompile(`\s(?:—|--)\s`)

// splitCriterionLine splits one list-item's content into its descriptive
// text and its verification method, per the separator rules in the spec:
// "—", "--", "verified by:", "verification:", case-insensitive.
func splitCriterionLine(content string) (text, verification string) {
	if loc := verificationLabelPattern.FindStringIndex(content); loc != nil {
		return strings.TrimSpace(content[:loc[0]]), strings.TrimSpace(content[loc[1]:])
	}
	if loc := dashSeparatorPattern.FindStringIndex(content); loc != nil {
		return strings.TrimSpace(content[:loc[0]]), strings.TrimSpace(content[loc[1]:])
	}
	return strings.TrimSpace(content), ""
}

// idPattern recognizes a short leading identifier like "AC1:" at the start
// of a criterion's text.
var idPattern = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9_-]{0,20}):\s*(.*)$`)

func extractID(text string) (id, rest string) {
	if m := idPattern.FindStringSubmatch(text); m != nil {
		return m[1], strings.TrimSpace(m[2])
	}
	return "", strings.TrimSpace(text)
}

// isTableRow reports whether raw looks like one row of a markdown table:
// a line whose only meaningful content, once trimmed, starts with "|".
func isTableRow(raw string) bool {
	trimmed := strings.TrimSpace(raw)
	return strings.HasPrefix(trimmed, "|")
}

// splitTableRow splits one markdown table row into trimmed cells,
// tolerating either leading/trailing pipes or their absence.
func splitTableRow(raw string) []string {
	trimmed := strings.TrimSpace(raw)
	trimmed = strings.TrimPrefix(trimmed, "|")
	trimmed = strings.TrimSuffix(trimmed, "|")
	parts := strings.Split(trimmed, "|")
	for i, p := range parts {
		parts[i] = strings.TrimSpace(p)
	}
	return parts
}

// isSeparatorRow reports whether cells is a markdown table's header
// separator row (e.g. "---", ":---:") rather than a data row.
func isSeparatorRow(cells []string) bool {
	if len(cells) == 0 {
		return false
	}
	for _, c := range cells {
		if c == "" {
			return false
		}
		for _, r := range c {
			if r != '-' && r != ':' {
				return false
			}
		}
	}
	return true
}

// parseCriteriaTable parses body as a markdown table with id/text/
// verification columns identified by header name, not position (spec:
// "column order by header name, not position"). ok is false when body
// contains no table with a recognizable "text" column at all, so the
// caller can fall back to list parsing instead.
func parseCriteriaTable(body string) (rows []Criterion, ok bool) {
	lines := strings.Split(body, "\n")

	start := -1
	for i, raw := range lines {
		if isTableRow(raw) {
			start = i
			break
		}
	}
	if start == -1 {
		return nil, false
	}

	header := splitTableRow(lines[start])
	col := make(map[string]int, len(header))
	for i, h := range header {
		col[normalizeHeading(h)] = i
	}
	idIdx, hasID := col["id"]
	textIdx, hasText := col["text"]
	verIdx, hasVer := col["verification"]
	if !hasText {
		// No recognizable text column: this isn't an id/text/verification
		// criteria table at all, so let the caller try list parsing.
		return nil, false
	}

	get := func(cells []string, idx int, present bool) string {
		if !present || idx >= len(cells) {
			return ""
		}
		return strings.TrimSpace(cells[idx])
	}

	var out []Criterion
	for i := start + 1; i < len(lines); i++ {
		raw := lines[i]
		if !isTableRow(raw) {
			if strings.TrimSpace(raw) == "" {
				continue
			}
			break
		}
		cells := splitTableRow(raw)
		if isSeparatorRow(cells) {
			continue
		}
		out = append(out, Criterion{
			ID:           get(cells, idIdx, hasID),
			Text:         get(cells, textIdx, hasText),
			Verification: get(cells, verIdx, hasVer),
		})
	}
	return out, true
}

// malformedTableProblem detects an Acceptance criteria section that
// contains a table missing a required column outright — a structural
// defect distinct from a criterion that simply has an empty verification
// cell (that's a criterion_without_verification problem instead, reported
// per-row by Problems).
func malformedTableProblem(body string) *Problem {
	lines := strings.Split(body, "\n")

	start := -1
	for i, raw := range lines {
		if isTableRow(raw) {
			start = i
			break
		}
	}
	if start == -1 {
		return nil
	}

	header := splitTableRow(lines[start])
	col := make(map[string]bool, len(header))
	for _, h := range header {
		col[normalizeHeading(h)] = true
	}
	if !col["text"] || !col["verification"] {
		return &Problem{
			Kind:   "malformed",
			Detail: "acceptance criteria table is missing a required column (need id, text and verification)",
		}
	}
	return nil
}
