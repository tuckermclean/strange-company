package spec

import (
	"strings"
	"testing"
)

// completeSpec is a realistic, fully-formed /specs/<card-id>.md document —
// the kind of thing an operator (or Fable, per spec §10.2) would actually
// write. It carries every required section and two list-style acceptance
// criteria, each with an explicit verification method.
const completeSpec = `# Specification: widget-404

## Context

The widget API currently returns a 500 error for unknown ids instead of a
404, which breaks client error handling and has already caused one
mis-routed retry storm in production.

## Task

Return 404 Not Found when a widget id does not exist, instead of the
current 500.

## Evidence available

- Production logs showing 500s for GET /widgets/{id} with unknown ids.
- api/widgets_test.go, which has no coverage for the unknown-id case today.

## Interfaces

- GET /api/widgets/{id}

## Constraints

Must not change the response shape or status code for the success case.

## Invariants

Existing widgets must continue to return 200 with their current payload
shape.

## Permitted actions

- Edit files under api/.
- Run go test ./api/....

## Forbidden actions

- Do not modify the database schema.
- Do not touch billing code.

## Acceptance criteria

- AC1: requesting an unknown widget id returns HTTP 404 — verified by: go test ./api -run TestWidget404
- AC2: requesting a known widget id still returns HTTP 200 with its existing payload — verified by: go test ./api -run TestWidgetOK

## Out of scope

Pagination and bulk widget lookups are not covered by this card.

## Failure behavior

If the datastore is unreachable, return 503, not 404.
`

func TestParse_CompleteDocument_ZeroProblems(t *testing.T) {
	doc, problems := Parse("card-widget-404", []byte(completeSpec))

	if len(problems) != 0 {
		t.Fatalf("problems = %v, want none: a fully-formed specification with every required section and a verified criterion for each requirement must pass the gate (§10)", problems)
	}
	if doc.CardID != "card-widget-404" {
		t.Fatalf("CardID = %q, want %q", doc.CardID, "card-widget-404")
	}
	if len(doc.Criteria) != 2 {
		t.Fatalf("len(Criteria) = %d, want 2", len(doc.Criteria))
	}
	for i, c := range doc.Criteria {
		if c.ID == "" || c.Text == "" || c.Verification == "" {
			t.Fatalf("Criteria[%d] = %+v, want every field populated: a well-formed document should parse with no gaps to fill in later", i, c)
		}
	}
	for _, name := range RequiredSections() {
		body, ok := doc.Sections[name]
		if !ok || strings.TrimSpace(body) == "" {
			t.Fatalf("Sections[%q] missing or empty in a document that supplied it, ok=%v body=%q", name, ok, body)
		}
	}
}

func TestParse_MissingRequiredSection_ReportsThatSectionByName(t *testing.T) {
	// Drop the "Out of scope" section entirely; every other required
	// section stays intact.
	withoutOutOfScope := strings.Replace(completeSpec,
		"## Out of scope\n\nPagination and bulk widget lookups are not covered by this card.\n\n",
		"", 1)
	if withoutOutOfScope == completeSpec {
		t.Fatalf("test fixture setup failed: the Out of scope section text to remove was not found")
	}

	_, problems := Parse("card-1", []byte(withoutOutOfScope))

	found := false
	for _, p := range problems {
		if p.Kind != "missing_section" {
			continue
		}
		if strings.Contains(p.Detail, "Out of scope") {
			found = true
		} else {
			t.Fatalf("unexpected missing_section problem for a section that IS present: %+v — the gate must name exactly the section that is missing, nothing else", p)
		}
	}
	if !found {
		t.Fatalf("problems = %v, want a missing_section problem naming \"Out of scope\": a section Fable never wrote must be reported by name (§10.2)", problems)
	}
}

func TestParse_HeadingWithEmptyBody_ReportedNotAccepted(t *testing.T) {
	// "## Constraints" is present as a heading but has nothing under it
	// before the next heading starts.
	withEmptyConstraints := strings.Replace(completeSpec,
		"## Constraints\n\nMust not change the response shape or status code for the success case.\n\n",
		"## Constraints\n\n", 1)
	if withEmptyConstraints == completeSpec {
		t.Fatalf("test fixture setup failed: the Constraints body to blank out was not found")
	}

	doc, problems := Parse("card-1", []byte(withEmptyConstraints))

	body, ok := doc.Sections["Constraints"]
	if !ok {
		t.Fatalf("Sections[\"Constraints\"] absent entirely; a heading that exists in the document must still show up as present (with an empty body), distinct from a section that was never written")
	}
	if strings.TrimSpace(body) != "" {
		t.Fatalf("Sections[\"Constraints\"] = %q, want empty", body)
	}

	wantEmpty := false
	wantMissing := false
	for _, p := range problems {
		if p.Kind == "empty_section" && strings.Contains(p.Detail, "Constraints") {
			wantEmpty = true
		}
		if p.Kind == "missing_section" && strings.Contains(p.Detail, "Constraints") {
			wantMissing = true
		}
	}
	if !wantEmpty {
		t.Fatalf("problems = %v, want an empty_section problem for \"Constraints\": a heading with nothing under it is exactly the box-ticking §10 exists to catch, not a satisfied section", problems)
	}
	if wantMissing {
		t.Fatalf("problems = %v, got a missing_section problem for \"Constraints\", but the heading IS present — empty and missing are different failures and must not be conflated", problems)
	}
}

func TestParse_ListCriteria_AllFourSeparatorForms(t *testing.T) {
	doc := "## Acceptance criteria\n\n" +
		"- AC1: alpha does the thing — end to end\n" +
		"- AC2: beta does the other thing -- integration suite\n" +
		"- AC3: gamma handles the edge case verified by: unit test suite\n" +
		"- AC4: delta rejects bad input verification: fuzz test suite\n"

	got, _ := Parse("card-1", []byte(doc))

	want := []Criterion{
		{ID: "AC1", Text: "alpha does the thing", Verification: "end to end"},
		{ID: "AC2", Text: "beta does the other thing", Verification: "integration suite"},
		{ID: "AC3", Text: "gamma handles the edge case", Verification: "unit test suite"},
		{ID: "AC4", Text: "delta rejects bad input", Verification: "fuzz test suite"},
	}

	if len(got.Criteria) != len(want) {
		t.Fatalf("len(Criteria) = %d, want %d — got %+v", len(got.Criteria), len(want), got.Criteria)
	}
	for i := range want {
		if got.Criteria[i] != want[i] {
			t.Fatalf("Criteria[%d] = %+v, want %+v: every separator form the spec allows (\"—\", \"--\", \"verified by:\", \"verification:\") must split into the same clean {id, text, verification} shape", i, got.Criteria[i], want[i])
		}
	}
}

func TestParse_TableCriteria_ColumnsIdentifiedByHeaderNameNotPosition(t *testing.T) {
	// Columns are deliberately out of the "usual" id/text/verification
	// order, to prove the parser reads the header row rather than
	// assuming a fixed column position.
	doc := "## Acceptance criteria\n\n" +
		"| Verification | ID | Text |\n" +
		"|---|---|---|\n" +
		"| go test ./api -run TestA | AC1 | requesting an unknown id returns 404 |\n" +
		"| go test ./api -run TestB | AC2 | requesting a known id returns 200 |\n"

	got, _ := Parse("card-1", []byte(doc))

	want := []Criterion{
		{ID: "AC1", Text: "requesting an unknown id returns 404", Verification: "go test ./api -run TestA"},
		{ID: "AC2", Text: "requesting a known id returns 200", Verification: "go test ./api -run TestB"},
	}

	if len(got.Criteria) != len(want) {
		t.Fatalf("len(Criteria) = %d, want %d — got %+v", len(got.Criteria), len(want), got.Criteria)
	}
	for i := range want {
		if got.Criteria[i] != want[i] {
			t.Fatalf("Criteria[%d] = %+v, want %+v: a table's columns are identified by header name, so reordering them must not scramble which value lands in which field", i, got.Criteria[i], want[i])
		}
	}
}

func TestParse_CriterionWithoutVerification_ReportedAndNamed(t *testing.T) {
	doc := "## Acceptance criteria\n\n" +
		"- AC1: the widget deletes cleanly\n" +
		"- AC2: the widget lists correctly — verified by: go test ./api -run TestList\n"

	_, problems := Parse("card-1", []byte(doc))

	found := false
	for _, p := range problems {
		if p.Kind != "criterion_without_verification" {
			continue
		}
		if strings.Contains(p.Detail, "AC1") {
			found = true
		}
		if strings.Contains(p.Detail, "AC2") {
			t.Fatalf("unexpected criterion_without_verification problem naming AC2, which DOES state a verification method: %+v", p)
		}
	}
	if !found {
		t.Fatalf("problems = %v, want a criterion_without_verification problem naming AC1: a criterion nobody can verify is not an acceptance criterion (§10)", problems)
	}
}

func TestParse_ZeroCriteria_ReportsNoCriteria(t *testing.T) {
	doc := "## Acceptance criteria\n\nCriteria will be defined once the design is finalized.\n"

	got, problems := Parse("card-1", []byte(doc))

	if len(got.Criteria) != 0 {
		t.Fatalf("Criteria = %+v, want none: freeform prose with no list or table is not an acceptance criterion", got.Criteria)
	}
	found := false
	for _, p := range problems {
		if p.Kind == "no_criteria" {
			found = true
		}
	}
	if !found {
		t.Fatalf("problems = %v, want a no_criteria problem: zero acceptance criteria must never silently pass the gate (§10)", problems)
	}
}

func TestParse_HeadingMatching_CaseAndPunctuationInsensitive(t *testing.T) {
	doc := "### acceptance criteria:\n\n" +
		"- AC1: it works — verified by: go test\n\n" +
		"## CONTEXT\n\n" +
		"Some context.\n\n" +
		"#  Out-of-Scope!!  \n\n" +
		"Nothing else.\n"

	got, _ := Parse("card-1", []byte(doc))

	if body, ok := got.Sections["Acceptance criteria"]; !ok || strings.TrimSpace(body) == "" {
		t.Fatalf("Sections[\"Acceptance criteria\"] not recognized from heading \"### acceptance criteria:\" — heading matching must ignore case and surrounding punctuation")
	}
	if body, ok := got.Sections["Context"]; !ok || strings.TrimSpace(body) == "" {
		t.Fatalf("Sections[\"Context\"] not recognized from heading \"## CONTEXT\" — heading matching must ignore case")
	}
	if body, ok := got.Sections["Out of scope"]; !ok || strings.TrimSpace(body) == "" {
		t.Fatalf("Sections[\"Out of scope\"] not recognized from heading \"#  Out-of-Scope!!  \" — heading matching must ignore surrounding punctuation and whitespace")
	}
}

func TestParse_TableMissingColumn_ReportedAsMalformed(t *testing.T) {
	doc := "## Acceptance criteria\n\n" +
		"| ID | Text |\n" +
		"|----|------|\n" +
		"| AC1 | requesting an unknown id returns 404 |\n"

	_, problems := Parse("card-1", []byte(doc))

	found := false
	for _, p := range problems {
		if p.Kind == "malformed" {
			found = true
		}
	}
	if !found {
		t.Fatalf("problems = %v, want a malformed problem: a criteria table missing its verification column is a structural defect, not just an unverified criterion", problems)
	}
}

func TestParse_NeverPanics(t *testing.T) {
	cases := []struct {
		name string
		doc  []byte
	}{
		{"nil", nil},
		{"empty", []byte{}},
		{"binary junk", []byte{0x00, 0xff, 0xfe, 'h', 'i', 0x01, '#', '#', 0x02, 0xc3, 0x28}},
		{"headings only", []byte("# Context\n## Task\n### Evidence available\n#### Interfaces\n##### Constraints\n###### Invariants\n# Permitted actions\n# Forbidden actions\n# Acceptance criteria\n# Out of scope\n# Failure behavior\n")},
		{"criteria section only", []byte("## Acceptance criteria\n\n- AC1: it works — verified by: go test\n")},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("Parse panicked on %s input: %v — a specification gate that crashes on malformed input is worse than one that just reports problems", tc.name, r)
				}
			}()
			doc, problems := Parse("card-1", tc.doc)
			if doc == nil {
				t.Fatalf("Parse(%s) returned a nil Document; callers need a usable partial Document even for input this bad", tc.name)
			}
			_ = problems
		})
	}
}

func TestParse_ProblemsReturnedAlongsideUsablePartialDocument(t *testing.T) {
	raw := []byte("## Context\n\nWe need to fix the widget lookup.\n\n## Task\n\nReturn 404 for unknown ids.\n")

	doc, problems := Parse("card-partial", raw)

	if doc == nil {
		t.Fatalf("Document = nil, want a usable partial Document even though this input fails most of the gate")
	}
	if doc.CardID != "card-partial" {
		t.Fatalf("CardID = %q, want %q", doc.CardID, "card-partial")
	}
	if string(doc.Raw) != string(raw) {
		t.Fatalf("Raw = %q, want the exact original bytes for audit purposes", doc.Raw)
	}
	if got, ok := doc.Sections["Context"]; !ok || strings.TrimSpace(got) == "" {
		t.Fatalf("Sections[\"Context\"] = %q (ok=%v), want the section this document DID provide to still be usable", got, ok)
	}
	if got, ok := doc.Sections["Task"]; !ok || strings.TrimSpace(got) == "" {
		t.Fatalf("Sections[\"Task\"] = %q (ok=%v), want the section this document DID provide to still be usable", got, ok)
	}

	if len(problems) == 0 {
		t.Fatalf("problems = %v, want several: this document is missing most of §10.2's required sections and has no acceptance criteria at all", problems)
	}

	missingCount := 0
	for _, name := range RequiredSections() {
		if name == "Context" || name == "Task" {
			continue
		}
		for _, p := range problems {
			if p.Kind == "missing_section" && strings.Contains(p.Detail, name) {
				missingCount++
				break
			}
		}
	}
	wantMissing := len(RequiredSections()) - 2
	if missingCount != wantMissing {
		t.Fatalf("found %d missing_section problems for the sections this document omitted, want %d", missingCount, wantMissing)
	}

	sawNoCriteria := false
	for _, p := range problems {
		if p.Kind == "no_criteria" {
			sawNoCriteria = true
		}
	}
	if !sawNoCriteria {
		t.Fatalf("problems = %v, want a no_criteria problem: this document has no Acceptance criteria section at all", problems)
	}
}

func TestRequiredSections_ReturnsIndependentCopy(t *testing.T) {
	a := RequiredSections()
	if len(a) == 0 {
		t.Fatalf("RequiredSections() returned no sections")
	}
	a[0] = "tampered"

	b := RequiredSections()
	if b[0] == "tampered" {
		t.Fatalf("RequiredSections() returned a slice backed by shared state: mutating one caller's result must not affect another's")
	}
}
