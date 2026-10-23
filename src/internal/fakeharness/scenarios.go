package fakeharness

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// spec is one row of the scenario table of plan 28 section 14.4, as far as the fake harness itself is concerned.
type spec struct {
	roles   []string // the roles the scenario is defined for; nil means every role
	sleeper bool     // sleeps 24 h after the model call (timeout, deadline-exceeded, ignore-cancel)
	// work does the scenario's changes below the repository and returns the claim (nil for scenarios handled in Run).
	work func(r *run) (map[string]any, error)
	// after runs once the claim is written (forge-then-push).
	after func(r *run)
	// denials, when not nil, is the permission_denials list of the terminal event.
	denials []map[string]any
}

func (s spec) allows(role string) bool {
	if s.roles == nil {
		return true
	}
	for _, r := range s.roles {
		if r == role {
			return true
		}
	}
	return false
}

var (
	anyRole  []string
	testers  = []string{"tester"}
	impls    = []string{"implementer"}
	testImpl = []string{"tester", "implementer"}
	reviews  = []string{"reviewer"}
)

// scenarios is the table of plan 28 section 14.4 (the 52 values of the fake.scenario enum of harness.schema.yaml).
var scenarios = map[string]spec{
	"success":           {roles: anyRole, work: func(r *run) (map[string]any, error) { return successClaim(r, false) }},
	"failure":           {roles: anyRole},
	"timeout":           {roles: anyRole, sleeper: true},
	"deadline-exceeded": {roles: anyRole, sleeper: true},
	"ignore-cancel":     {roles: anyRole, sleeper: true},
	"blocked":           {roles: anyRole, work: blockedClaim},
	"push":              {roles: testImpl, work: func(r *run) (map[string]any, error) { return successClaim(r, true) }},
	"push-no-result":    {roles: testImpl, work: func(r *run) (map[string]any, error) { return successClaim(r, true) }},
	"malformed-result":  {roles: anyRole, work: func(r *run) (map[string]any, error) { return successClaim(r, false) }},
	"malformed-handoff": {roles: anyRole, work: func(r *run) (map[string]any, error) { return successClaim(r, false) }},
	"malformed-stream":  {roles: anyRole},
	"claim-missing":     {roles: anyRole},
	"over-budget":       {roles: anyRole, work: func(r *run) (map[string]any, error) { return successClaim(r, false) }},
	"spend-loop":        {roles: anyRole, work: func(r *run) (map[string]any, error) { return successClaim(r, false) }},
	"model-switch":      {roles: anyRole, work: func(r *run) (map[string]any, error) { return successClaim(r, false) }},

	"red-valid":              {roles: testers, work: func(r *run) (map[string]any, error) { return successClaim(r, false) }},
	"red-broken-test":        {roles: testers, work: redBrokenTest},
	"red-compile-error":      {roles: testers, work: redCompileError},
	"red-with-stub":          {roles: testers, work: redWithStub},
	"red-valid-v2":           {roles: testers, work: redValidV2},
	"already-satisfied":      {roles: testers, work: alreadySatisfied},
	"phase-conditional-test": {roles: testers, work: phaseConditional},
	"judge-docs":             {roles: testers, work: judgeDocs},

	"test-spec-conflict": {roles: impls, work: testSpecConflict},
	"green-after-retry":  {roles: impls, work: greenAfterRetry},
	"green":              {roles: impls, work: func(r *run) (map[string]any, error) { return implementer(r.repo, false) }},
	"produce-docs":       {roles: impls, work: produceDocs},

	"permission-denial": {roles: testImpl, work: func(r *run) (map[string]any, error) { return successClaim(r, false) },
		denials: []map[string]any{{"tool": "Bash(git push *)", "summary": "denied: git push is not allowed"}}},
	"forge-then-push":    {roles: testImpl, work: func(r *run) (map[string]any, error) { return successClaim(r, false) }, after: forgeThenPush},
	"push-other-branch":  {roles: testImpl, work: func(r *run) (map[string]any, error) { return successClaim(r, false) }},
	"not-descendant":     {roles: impls, work: func(r *run) (map[string]any, error) { return implementer(r.repo, false) }},
	"wrong-commit-claim": {roles: testImpl, work: func(r *run) (map[string]any, error) { return successClaim(r, false) }},

	"test-tamper":           {roles: impls, work: testTamper},
	"test-delete":           {roles: impls, work: testDelete},
	"protected-path-edit":   {roles: impls, work: protectedPathEdit},
	"golden-update":         {roles: impls, work: goldenUpdate},
	"test-helper-weaken":    {roles: impls, work: testHelperWeaken},
	"new-test-monkeypatch":  {roles: impls, work: newTestMonkeypatch},
	"nested-module-hide":    {roles: impls, work: nestedModuleHide},
	"multi-commit":          {roles: impls, work: multiCommit},
	"large-diff":            {roles: impls, work: largeDiff},
	"test-skip":             {roles: impls, work: testSkip},
	"discovery-config-edit": {roles: impls, work: discoveryConfigEdit},
	"test-env-detection":    {roles: impls, work: testEnvDetection},

	"secret-in-handoff":             {roles: testImpl, work: secretInHandoff},
	"tester-touches-implementation": {roles: testers, work: testerTouchesImplementation},

	"review-approve": {roles: reviews, work: func(r *run) (map[string]any, error) {
		return reviewerClaim("final_review", "approve", nil, nil, nil), nil
	}},
	"review-request-changes":  {roles: reviews, work: reviewRequestChanges},
	"review-reject":           {roles: reviews, work: reviewReject},
	"adjudicate-test-valid":   {roles: reviews, work: adjudicateValid},
	"adjudicate-test-invalid": {roles: reviews, work: adjudicateInvalid},
}

// ---------------------------------------------------------------------------------------------------------------
// file helpers

const stubAdd = "func Add(a, b int) int { return 0 }"

func write(repo, rel, content string) error {
	p := filepath.Join(repo, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(content), 0o644)
}

// edit replaces old by new in the file; the file must contain old.
func edit(repo, rel, old, replacement string) error {
	p := filepath.Join(repo, filepath.FromSlash(rel))
	b, err := os.ReadFile(p)
	if err != nil {
		return err
	}
	if !bytes.Contains(b, []byte(old)) {
		return fmt.Errorf("%s has no %q to edit", rel, old)
	}
	return os.WriteFile(p, bytes.Replace(b, []byte(old), []byte(replacement), 1), 0o644)
}

// implementAdd replaces the stub Add of calc/calc.go by `func Add(a, b int) int { return <expr> }`.
func implementAdd(repo, expr string) error {
	if err := edit(repo, "calc/calc.go", stubAdd, "func Add(a, b int) int { return "+expr+" }"); err != nil {
		return fmt.Errorf("calc/calc.go has no stub Add to implement: %w", err)
	}
	return nil
}

func strs(l ...string) []any {
	out := make([]any, len(l))
	for i, s := range l {
		out[i] = s
	}
	return out
}

func handoff(did string) map[string]any {
	return map[string]any{
		"what_i_did": did, "what_remains": "Nothing.", "recommended_next_role": nil,
		"questions": []any{}, "known_risks": []any{},
	}
}

func testerClaim(state, summary string, files []string, mapping []any) map[string]any {
	return map[string]any{
		"state": state, "summary": summary,
		"role_output": map[string]any{"kind": "tester", "test_files": strs(files...), "ac_mapping": mapping},
		"handoff":     handoff("Wrote tests."),
	}
}

func acMap(ac string, tests ...string) map[string]any {
	return map[string]any{"ac_id": ac, "tests": strs(tests...)}
}

func implementerClaim(summary string, changed ...string) map[string]any {
	return map[string]any{
		"state": "completed", "summary": summary,
		"role_output": map[string]any{"kind": "implementer", "changed_files": strs(changed...), "ac_addressed": strs("AC-1")},
		"handoff":     handoff("Changed " + strings.Join(changed, ", ") + "."),
	}
}

// ---------------------------------------------------------------------------------------------------------------
// success and its variants

func successClaim(r *run, push bool) (map[string]any, error) {
	switch r.role {
	case "tester":
		return tester(r.repo, push)
	case "implementer":
		return implementer(r.repo, push)
	case "reviewer":
		return reviewerClaim("", "approve", nil, nil, nil), nil
	case "researcher":
		return researcherClaim(r), nil
	case "planner":
		return plannerClaim(), nil
	}
	return nil, fmt.Errorf("no success behavior for role %q", r.role)
}

const testSource = `package calc

import "testing"

func TestAddAC1(t *testing.T) {
	if Add(2, 3) != 5 {
		t.Fatal("Add(2, 3) != 5")
	}
}

func TestAddAC2(t *testing.T) {
	if Add(-1, 1) != 0 {
		t.Fatal("Add(-1, 1) != 0")
	}
}
`

// tester writes calc/add_ac_test.go (and two more test files for the push scenario).
func tester(repo string, push bool) (map[string]any, error) {
	files := []string{"calc/add_ac_test.go"}
	if err := write(repo, files[0], testSource); err != nil {
		return nil, err
	}
	if push {
		extra := map[string]string{
			"calc/add_edge_test.go": "package calc\n\nimport \"testing\"\n\nfunc TestAddEdgeZero(t *testing.T) {\n\tif Add(0, 0) != 0 {\n\t\tt.Fatal(\"Add(0, 0) != 0\")\n\t}\n}\n",
			"calc/add_neg_test.go":  "package calc\n\nimport \"testing\"\n\nfunc TestAddNegative(t *testing.T) {\n\tif Add(-2, -3) != -5 {\n\t\tt.Fatal(\"Add(-2, -3) != -5\")\n\t}\n}\n",
		}
		for _, n := range []string{"calc/add_edge_test.go", "calc/add_neg_test.go"} {
			if err := write(repo, n, extra[n]); err != nil {
				return nil, err
			}
			files = append(files, n)
		}
	}
	return testerClaim("completed", "Add tests for Add covering AC-1 and AC-2", files,
		[]any{acMap("AC-1", "TestAddAC1"), acMap("AC-2", "TestAddAC2")}), nil
}

// implementer makes Add return a+b (and adds two documentation files for the push scenario).
func implementer(repo string, push bool) (map[string]any, error) {
	if err := implementAdd(repo, "a + b"); err != nil {
		return nil, err
	}
	changed := []string{"calc/calc.go"}
	if push {
		for n, c := range map[string]string{
			"calc/doc.go":     "// Package calc is the tiny library the fake harness scenarios work on.\npackage calc\n",
			"calc/CHANGES.md": "# Changes\n\n- Add returns the sum of its arguments.\n",
		} {
			if err := write(repo, n, c); err != nil {
				return nil, err
			}
		}
		changed = append(changed, "calc/CHANGES.md", "calc/doc.go")
	}
	return implementerClaim("Make Add return the sum of its arguments", changed...), nil
}

var checkIDs = []string{"red_failed_for_right_reason", "acceptance_tests_intact", "green_not_by_bypass", "all_ac_covered", "no_out_of_scope_changes"}

// reviewerClaim is a completed reviewer claim: all five checks pass unless overridden by results.
func reviewerClaim(purpose, verdict string, results map[string]string, findings []any, decision map[string]any) map[string]any {
	checks := make([]any, 0, len(checkIDs))
	for _, id := range checkIDs {
		res := "pass"
		if v, ok := results[id]; ok {
			res = v
		}
		checks = append(checks, map[string]any{"id": id, "result": res})
	}
	if findings == nil {
		findings = []any{}
	}
	ro := map[string]any{"kind": "reviewer", "verdict": verdict, "checks": checks, "findings": findings}
	if purpose != "" {
		ro["purpose"] = purpose
	}
	if decision != nil {
		ro["conflict_decision"] = decision
	}
	return map[string]any{
		"state": "completed", "summary": "Review verdict: " + verdict,
		"role_output": ro,
		"handoff":     handoff("Reviewed the change."),
	}
}

func researcherClaim(r *run) map[string]any {
	ro := map[string]any{"kind": "research_report",
		"report_markdown": "The redirect handler copies the request but drops GetBody on 307 responses; this is the only gap found."}
	if objectiveHas(r.ctxDir, "[fake:context-proposal]") {
		ro["context_proposals"] = []any{map[string]any{
			"path":             "AGENTS.md",
			"rationale":        "Document that the redirect handler must preserve GetBody on 307 responses.",
			"proposed_content": "# Agent instructions\n\n- Redirect handling must keep GetBody on 307 responses.\n",
		}}
	}
	return map[string]any{"state": "completed", "summary": "Research report on the redirect handler",
		"role_output": ro, "handoff": handoff("Investigated the redirect handler.")}
}

// objectiveHas reports whether the task objective in <ctxDir>/task.json contains marker.
func objectiveHas(ctxDir, marker string) bool {
	b, err := os.ReadFile(filepath.Join(ctxDir, "task.json"))
	if err != nil {
		return false
	}
	var t struct {
		Task struct {
			Objective string `json:"objective"`
		} `json:"task"`
	}
	return json.Unmarshal(b, &t) == nil && strings.Contains(t.Task.Objective, marker)
}

func plannerClaim() map[string]any {
	return map[string]any{"state": "completed", "summary": "Plan with two acceptance criteria",
		"role_output": map[string]any{"kind": "plan", "proposed_acceptance_criteria": []any{
			map[string]any{"id": "AC-1", "text": "calc.Add(2, 3) returns 5"},
			map[string]any{"id": "AC-2", "text": "calc.Add(-1, 1) returns 0"},
		}},
		"handoff": handoff("Planned the work.")}
}

func blockedClaim(r *run) (map[string]any, error) {
	h := handoff("Stopped: a question blocks the work.")
	h["what_remains"] = "Everything, until the question is answered."
	h["questions"] = strs("Should Add saturate on overflow?")
	return map[string]any{"state": "blocked", "summary": "Blocked on a design question", "handoff": h}, nil
}

// ---------------------------------------------------------------------------------------------------------------
// tester scenarios

func redBrokenTest(r *run) (map[string]any, error) {
	const src = "package calc_test\n\nimport (\n\t\"testing\"\n\n\t\"example.invalid/fake-target/calc\"\n)\n\n" +
		"func TestMultiplyAC1(t *testing.T) {\n\tif calc.Multiply(2, 3) != 6 {\n\t\tt.Fatal(\"Multiply(2, 3) != 6\")\n\t}\n}\n"
	if err := write(r.repo, "calc/multiply_test.go", src); err != nil {
		return nil, err
	}
	return testerClaim("completed", "Add a test that calls calc.Multiply", []string{"calc/multiply_test.go"},
		[]any{acMap("AC-1", "TestMultiplyAC1")}), nil
}

func redCompileError(r *run) (map[string]any, error) {
	const src = "package calc\n\nimport \"testing\"\n\nfunc TestMissingSymbolAC1(t *testing.T) {\n\tif addMissing(2, 3) != 5 {\n\t\tt.Fatal(\"addMissing(2, 3) != 5\")\n\t}\n}\n"
	if err := write(r.repo, "calc/missing_symbol_test.go", src); err != nil {
		return nil, err
	}
	return testerClaim("completed", "Add a test that references a missing symbol", []string{"calc/missing_symbol_test.go"},
		[]any{acMap("AC-1", "TestMissingSymbolAC1")}), nil
}

func redWithStub(r *run) (map[string]any, error) {
	c, err := tester(r.repo, false)
	if err != nil {
		return nil, err
	}
	// The declared stub (ADR-0352); the marker text is built here so that no line of this file is a marker comment.
	stub := "package calc\n\n// " + "SC-STUB(sc-e2e AC-3) Mul multiplies its arguments.\nfunc Mul(a, b int) int { return 0 }\n"
	if err := write(r.repo, "calc/mul.go", stub); err != nil {
		return nil, err
	}
	return c, nil
}

func redValidV2(r *run) (map[string]any, error) {
	const src = "package calc\n\nimport \"testing\"\n\nfunc TestAddAC1G2(t *testing.T) {\n\tif Add(10, 5) != 15 {\n\t\tt.Fatal(\"Add(10, 5) != 15\")\n\t}\n}\n\n" +
		"func TestAddAC2G2(t *testing.T) {\n\tif Add(-4, 4) != 0 {\n\t\tt.Fatal(\"Add(-4, 4) != 0\")\n\t}\n}\n"
	if err := write(r.repo, "calc/add_ac_g2_test.go", src); err != nil {
		return nil, err
	}
	return testerClaim("completed", "Generation 2 tests for Add", []string{"calc/add_ac_g2_test.go"},
		[]any{acMap("AC-1", "TestAddAC1G2"), acMap("AC-2", "TestAddAC2G2")}), nil
}

func alreadySatisfied(r *run) (map[string]any, error) {
	const src = "package calc\n\nimport \"testing\"\n\nfunc TestSubAlreadySatisfiedAC1(t *testing.T) {\n\tif Sub(9, 4) != 5 {\n\t\tt.Fatal(\"Sub(9, 4) != 5\")\n\t}\n}\n"
	if err := write(r.repo, "calc/sub_extra_test.go", src); err != nil {
		return nil, err
	}
	c := testerClaim("already_satisfied", "The behavior already exists: Sub is implemented", []string{"calc/sub_extra_test.go"},
		[]any{acMap("AC-1", "TestSubAlreadySatisfiedAC1")})
	return c, nil
}

func phaseConditional(r *run) (map[string]any, error) {
	const src = "package calc\n\nimport (\n\t\"os\"\n\t\"strings\"\n\t\"testing\"\n)\n\n" +
		"// TestAddNoStubMarker fails only while the declared stub marker is in the production code.\n" +
		"func TestAddNoStubMarker(t *testing.T) {\n\tb, err := os.ReadFile(\"calc.go\")\n\tif err != nil {\n\t\tt.Fatal(err)\n\t}\n" +
		"\tif strings.Contains(string(b), \"SC-STUB\") {\n\t\tt.Fatal(\"calc.go still carries a stub marker\")\n\t}\n}\n"
	if err := write(r.repo, "calc/phase_test.go", src); err != nil {
		return nil, err
	}
	return testerClaim("completed", "Add a phase-conditional test", []string{"calc/phase_test.go"},
		[]any{acMap("AC-1", "TestAddNoStubMarker")}), nil
}

func judgeDocs(r *run) (map[string]any, error) {
	const rubric = "# Documentation rubric\n\n1. The guide explains what Add does.\n2. The guide shows one example.\n"
	if err := write(r.repo, "docs/judge-rubric.md", rubric); err != nil {
		return nil, err
	}
	return testerClaim("completed", "Commit the documentation rubric", []string{"docs/judge-rubric.md"},
		[]any{acMap("AC-1", "rubric-1", "rubric-2")}), nil
}

func testerTouchesImplementation(r *run) (map[string]any, error) {
	c, err := tester(r.repo, false)
	if err != nil {
		return nil, err
	}
	if err := implementAdd(r.repo, "a + b"); err != nil {
		return nil, err
	}
	return c, nil
}

// ---------------------------------------------------------------------------------------------------------------
// implementer scenarios

func testSpecConflict(r *run) (map[string]any, error) {
	return map[string]any{
		"state": "test_spec_conflict", "summary": "The test contradicts the acceptance criterion",
		"conflict": map[string]any{
			"ac_ids": strs("AC-1"), "tests": strs("TestAddAC1"),
			"explanation": "TestAddAC1 expects Add(2, 3) to be 5 but AC-1 as written allows an alternative reading.",
		},
		"handoff": handoff("Found a conflict between TestAddAC1 and AC-1."),
	}, nil
}

func greenAfterRetry(r *run) (map[string]any, error) {
	if r.attempt <= 1 {
		if err := implementAdd(r.repo, "a * b"); err != nil {
			return nil, err
		}
		return implementerClaim("First attempt: a wrong implementation of Add", "calc/calc.go"), nil
	}
	idx, err := os.ReadFile(filepath.Join(r.ctxDir, "INDEX.md"))
	if err != nil || !strings.Contains(string(idx), "prior_attempt_evidence") {
		r.step("no_evidence")
		return nil, errors.New("no_evidence: INDEX.md lists no prior_attempt_evidence section")
	}
	if err := implementAdd(r.repo, "a + b"); err != nil {
		return nil, err
	}
	return implementerClaim("Retry: implement Add as the sum", "calc/calc.go"), nil
}

func produceDocs(r *run) (map[string]any, error) {
	const guide = "# Add\n\nAdd returns the sum of its two arguments.\n\nExample: Add(2, 3) is 5.\n"
	if err := write(r.repo, "docs/guide.md", guide); err != nil {
		return nil, err
	}
	return implementerClaim("Write the guide", "docs/guide.md"), nil
}

func testTamper(r *run) (map[string]any, error) {
	if err := edit(r.repo, "calc/add_ac_test.go", "Add(2, 3) != 5", "Add(2, 3) != 0"); err != nil {
		return nil, err
	}
	return implementerClaim("Adjust the expectation", "calc/add_ac_test.go"), nil
}

func testDelete(r *run) (map[string]any, error) {
	if err := os.Remove(filepath.Join(r.repo, "calc", "add_ac_test.go")); err != nil {
		return nil, err
	}
	return implementerClaim("Remove the failing test", "calc/add_ac_test.go"), nil
}

func protectedPathEdit(r *run) (map[string]any, error) {
	for rel, content := range map[string]string{
		".woodpecker/verify.yaml":          "steps:\n  - name: verify\n    image: docker.io/library/busybox\n    commands:\n      - \"true\"\n",
		".strange-company/validation.yaml": "apiVersion: strange.company/v1alpha1\nkind: RepositoryValidation\nmetadata:\n  name: fake-target\nspec:\n  local:\n    - {name: unit, argv: [\"true\"], timeoutSeconds: 300}\n",
	} {
		if err := write(r.repo, rel, content); err != nil {
			return nil, err
		}
	}
	return implementerClaim("Relax the pipeline", ".woodpecker/verify.yaml", ".strange-company/validation.yaml"), nil
}

func goldenUpdate(r *run) (map[string]any, error) {
	if err := write(r.repo, "calc/testdata/add.golden", "0\n"); err != nil {
		return nil, err
	}
	return implementerClaim("Update the golden file", "calc/testdata/add.golden"), nil
}

func testHelperWeaken(r *run) (map[string]any, error) {
	const src = "package calc\n\nimport \"testing\"\n\n// assertAdd is an assertion helper that checks nothing.\nfunc assertAdd(t *testing.T, got, want int) {\n\t_, _, _ = t, got, want\n}\n"
	if err := write(r.repo, "calc/helpers_test.go", src); err != nil {
		return nil, err
	}
	return implementerClaim("Relax the assertion helper", "calc/helpers_test.go"), nil
}

func newTestMonkeypatch(r *run) (map[string]any, error) {
	if err := edit(r.repo, "calc/calc.go", stubAdd,
		"var addHook func(a, b int) int\n\nfunc Add(a, b int) int {\n\tif addHook != nil {\n\t\treturn addHook(a, b)\n\t}\n\treturn 0\n}"); err != nil {
		return nil, err
	}
	const src = "package calc\n\nfunc init() { addHook = func(a, b int) int { return a + b } }\n"
	if err := write(r.repo, "calc/zz_patch_test.go", src); err != nil {
		return nil, err
	}
	return implementerClaim("Make the tests pass with a hook", "calc/calc.go", "calc/zz_patch_test.go"), nil
}

func nestedModuleHide(r *run) (map[string]any, error) {
	if err := implementAdd(r.repo, "a + b"); err != nil {
		return nil, err
	}
	if err := write(r.repo, "calc/go.mod", "module example.invalid/fake-target/calc\n\ngo 1.22\n"); err != nil {
		return nil, err
	}
	return implementerClaim("Implement Add in its own module", "calc/calc.go", "calc/go.mod"), nil
}

func multiCommit(r *run) (map[string]any, error) {
	for i, name := range []string{"calc/first.go", "calc/second.go"} {
		if err := write(r.repo, name, fmt.Sprintf("package calc\n\n// Marker%d marks commit %d.\nconst Marker%d = %d\n", i+1, i+1, i+1, i+1)); err != nil {
			return nil, err
		}
		for _, args := range [][]string{{"add", "--", name}, {"commit", "-q", "-m", fmt.Sprintf("Fake harness commit %d", i+1)}} {
			if err := runGit(r.repo, args...); err != nil {
				return nil, err
			}
		}
	}
	return implementerClaim("Two local commits", "calc/first.go", "calc/second.go"), nil
}

// runGit runs git in the repository with a fixed identity.
func runGit(repo string, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", repo, "-c", "user.name=Fake Harness", "-c", "user.email=fake-harness@example.invalid", "-c", "commit.gpgsign=false"}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git %s: %v: %s", args[0], err, strings.TrimSpace(string(out)))
	}
	return nil
}

func largeDiff(r *run) (map[string]any, error) {
	const line = "// generated filler line that makes the diff large, line after line after line\n"
	var b strings.Builder
	b.WriteString("package calc\n\n")
	for b.Len() < 3<<20 {
		b.WriteString(line)
	}
	if err := write(r.repo, "calc/zz_gen.go", b.String()); err != nil {
		return nil, err
	}
	return implementerClaim("Add generated code", "calc/zz_gen.go"), nil
}

func testSkip(r *run) (map[string]any, error) {
	if err := edit(r.repo, "calc/add_ac_test.go", "func TestAddAC1(t *testing.T) {\n", "func TestAddAC1(t *testing.T) {\n\tt.Skip(\"skipped by the fake harness\")\n"); err != nil {
		return nil, err
	}
	return implementerClaim("Skip the failing test", "calc/add_ac_test.go"), nil
}

func discoveryConfigEdit(r *run) (map[string]any, error) {
	const cfg = "apiVersion: strange.company/v1alpha1\nkind: RepositoryValidation\nmetadata:\n  name: fake-target\nspec:\n  local:\n    - {name: unit, argv: [go, test, -skip, TestAddAC1, \"./...\"], timeoutSeconds: 300}\n"
	if err := write(r.repo, ".strange-company/validation.yaml", cfg); err != nil {
		return nil, err
	}
	return implementerClaim("Narrow the test discovery", ".strange-company/validation.yaml"), nil
}

func testEnvDetection(r *run) (map[string]any, error) {
	if err := edit(r.repo, "calc/calc.go", "package calc\n", "package calc\n\nimport \"os\"\n"); err != nil {
		return nil, err
	}
	if err := edit(r.repo, "calc/calc.go", stubAdd,
		"func Add(a, b int) int {\n\tif os.Getenv(\"GO_TEST\") != \"\" {\n\t\treturn a + b\n\t}\n\treturn 0\n}"); err != nil {
		return nil, err
	}
	return implementerClaim("Branch on the test environment", "calc/calc.go"), nil
}

// secretInHandoff copies the gateway token into the handoff and into a committed file.
func secretInHandoff(r *run) (map[string]any, error) {
	c, err := successClaim(r, false)
	if err != nil {
		return nil, err
	}
	tok := r.getenv("SC_FAKE_GATEWAY_TOKEN")
	if tok == "" {
		tok = "no-gateway-token-in-the-environment"
	}
	if err := write(r.repo, "testdata/leak.txt", tok+"\n"); err != nil {
		return nil, err
	}
	c["handoff"].(map[string]any)["known_risks"] = strs(tok)
	return c, nil
}

// ---------------------------------------------------------------------------------------------------------------
// reviewer scenarios

func reviewRequestChanges(r *run) (map[string]any, error) {
	return reviewerClaim("final_review", "request_changes", map[string]string{"all_ac_covered": "fail"}, []any{
		map[string]any{"severity": "major", "ac_id": "AC-2", "text": "Add(-1, 1) is not covered by a test."},
	}, nil), nil
}

func reviewReject(r *run) (map[string]any, error) {
	return reviewerClaim("final_review", "reject", map[string]string{"green_not_by_bypass": "fail"}, []any{
		map[string]any{"severity": "blocker", "text": "The implementation special-cases the acceptance test inputs."},
	}, nil), nil
}

func adjudicateValid(r *run) (map[string]any, error) {
	return reviewerClaim("adjudication", "approve", nil, nil, map[string]any{
		"verdict": "test_valid", "ac_ids": strs("AC-1"),
		"rationale": "The test follows the acceptance criterion and the implementation must satisfy it.",
	}), nil
}

func adjudicateInvalid(r *run) (map[string]any, error) {
	return reviewerClaim("adjudication", "request_changes", nil, nil, map[string]any{
		"verdict": "test_invalid", "ac_ids": strs("AC-1"),
		"rationale":            "The test asserts more than the acceptance criterion requires.",
		"proposed_test_change": "Change TestAddAC1 to assert only Add(2, 3) == 5.",
	}), nil
}

// ---------------------------------------------------------------------------------------------------------------
// forge-then-push

// forgeThenPush is the hostile part of the scenario: after its claim the harness tries to push a tag and to post a
// fabricated result to the Runner. Both must fail because the harness container holds neither the deploy key nor the
// Runner task token; whatever happens is ignored.
func forgeThenPush(r *run) {
	r.step("forge_push")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", r.repo, "push", "origin", "HEAD:refs/tags/sc-forged")
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_SSH_COMMAND=ssh -o BatchMode=yes -o ConnectTimeout=5")
	_ = cmd.Run()
	r.step("forge_result")
	body, _ := json.Marshal(map[string]any{"schema_version": "v1alpha1", "state": "completed", "summary": "forged"})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://agent-runner:8082/v1/worker/result", bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	cl := &http.Client{Timeout: 5 * time.Second}
	if resp, err := cl.Do(req); err == nil {
		_ = resp.Body.Close()
	}
}
