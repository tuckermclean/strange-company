package verify

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"

	"strange.company/sc/internal/policy"
)

// verifierLibVersion is stamped on every classification (RedClassification.verifier_lib_version).
const verifierLibVersion = "0.1.0"

// maxIDSetBytes bounds the decompressed size of a test id set.
const maxIDSetBytes = 64 << 20

// Outcomes of RedClassification.
const (
	outcomeRedValid           = "red_valid"
	outcomeRedInvalid         = "red_invalid"
	outcomeAlreadySatisfied   = "already_satisfied"
	outcomePartiallySatisfied = "partially_satisfied"
	outcomeBaselineFailing    = "baseline_failing"
	outcomeInconclusive       = "inconclusive"
)

// Error returns the code of a RED that cannot be classified.
func (e *NotClassifiedError) Error() string {
	if e == nil {
		return "NOT_CLASSIFIED"
	}
	return "NOT_CLASSIFIED: " + e.Code
}

// infraErr reports facts the classification depends on that are unusable (22-R-3: never a default pass).
func infraErr(format string, args ...any) error {
	return fmt.Errorf("%s: %w", fmt.Sprintf(format, args...), &NotClassifiedError{Code: "verification_infra_failed"})
}

// reasonGroup is the outcome and reasons contributed by one rule of plan 22 section 14.3.
type reasonGroup struct {
	outcome string
	reasons []string
}

var validStatuses = map[string]bool{
	statusPassed: true, statusFailedAssertion: true, statusFailedError: true, statusStub: true, statusSkipped: true, statusAbsent: true,
}

// decodeIDSet decodes $defs/TestIdSet (sorted ids, newline separated, gzip, base64) and checks count and digest.
func decodeIDSet(name string, s *TestIDSet) (map[string]bool, error) {
	if s == nil {
		return nil, infraErr("observed document has no %s id set", name)
	}
	raw, err := base64.StdEncoding.DecodeString(s.Data)
	if err != nil {
		return nil, infraErr("%s id set: %v", name, err)
	}
	zr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, infraErr("%s id set: %v", name, err)
	}
	text, err := io.ReadAll(io.LimitReader(zr, maxIDSetBytes+1))
	if err != nil {
		return nil, infraErr("%s id set: %v", name, err)
	}
	if len(text) > maxIDSetBytes {
		return nil, infraErr("%s id set: too large", name)
	}
	sum := sha256.Sum256(text)
	if hex.EncodeToString(sum[:]) != s.Sha256 {
		return nil, infraErr("%s id set: sha256 mismatch", name)
	}
	var ids []string
	if len(text) > 0 {
		ids = strings.Split(string(text), "\n")
	}
	if len(ids) != s.Count {
		return nil, infraErr("%s id set: count %d but %d ids", name, s.Count, len(ids))
	}
	out := make(map[string]bool, len(ids))
	for i, id := range ids {
		if i > 0 && ids[i-1] >= id {
			return nil, infraErr("%s id set: ids are not strictly sorted and unique (%q after %q)", name, id, ids[i-1])
		}
		out[id] = true
	}
	return out, nil
}

func suiteOf(id string) string {
	if i := strings.Index(id, "::"); i >= 0 {
		return id[:i]
	}
	return ""
}

// languageOfPath maps a source path to the policy language name used by stubPanicLanguages.
func languageOfPath(p string) string {
	switch strings.ToLower(path.Ext(p)) {
	case ".go":
		return "go"
	case ".rs":
		return "rust"
	case ".py":
		return "python"
	case ".ts", ".tsx":
		return "typescript"
	case ".js", ".jsx", ".mjs", ".cjs":
		return "javascript"
	case ".java":
		return "java"
	case ".kt":
		return "kotlin"
	case ".cs":
		return "csharp"
	}
	return ""
}

// panicStubLanguages returns the languages in which a panic stub is RED evidence: the repository's tightened list
// when set, else the platform's allowPanicStubsFor.
func panicStubLanguages(in RedInput) (map[string]bool, error) {
	var list []string
	var err error
	if v, ok := in.Effective.Values["/spec/verification/stubPanicLanguages"]; ok {
		list, err = stringList(v)
	} else {
		var sp StubPolicy
		if sp, err = LoadStubPolicy(in.Platform.Verification); err == nil {
			list = sp.AllowPanicStubsFor
		}
	}
	if err != nil {
		return nil, infraErr("panic stub languages: %v", err)
	}
	out := map[string]bool{}
	for _, l := range list {
		out[l] = true
	}
	return out, nil
}

// inGoScope reports whether the base test id belongs to the packages the head run was limited to.
func inGoScope(id string, pkgs []string) bool {
	rest := id
	if i := strings.Index(id, "::"); i >= 0 {
		rest = id[i+2:]
	}
	for _, p := range pkgs {
		if !strings.HasPrefix(rest, p+".") {
			continue
		}
		name := rest[len(p)+1:]
		for _, pre := range []string{"Test", "Example", "Fuzz", "Benchmark"} {
			if strings.HasPrefix(name, pre) {
				return true
			}
		}
	}
	return false
}

func appendUnique(dst []string, add ...string) []string {
	for _, a := range add {
		dup := false
		for _, d := range dst {
			dup = dup || d == a
		}
		if !dup {
			dst = append(dst, a)
		}
	}
	return dst
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// protectedPaths returns the platform spec.protectedPaths plus the effective /spec/protectedPaths (plan 22 section
// 9.3). The platform list is required; an ill-typed effective list is an error (22-R-3).
func protectedPaths(in RedInput) ([]string, error) {
	spec, ok := pathMap(in.Platform.Verification, "spec")
	if !ok {
		return nil, errors.New("verification policy has no spec")
	}
	list, err := stringList(spec["protectedPaths"])
	if err != nil {
		return nil, fmt.Errorf("spec.protectedPaths: %w", err)
	}
	if len(list) == 0 {
		return nil, errors.New("verification policy has no spec.protectedPaths")
	}
	if v, ok := in.Effective.Values["/spec/protectedPaths"]; ok {
		extra, err := stringList(v)
		if err != nil {
			return nil, fmt.Errorf("effective /spec/protectedPaths: %w", err)
		}
		list = append(list, extra...)
	}
	return list, nil
}

// ClassifyRed applies rules R0-R11 of plan 22 section 14.3 to an observed RED. It is pure and deterministic. The
// first failing blocking rule (in table order) decides the outcome; every failing rule contributes its reasons, in
// rule order. R10 (already / partially satisfied) decides only when no other rule fails. R0 identity failure and any
// unusable fact are errors carrying a *NotClassifiedError (verification_infra_failed), never a classification.
// R1b, R11b, R12-R14 belong to the generations slice.
func ClassifyRed(in RedInput) (RedClassification, error) {
	if !in.Identity.Passed {
		return RedClassification{}, &NotClassifiedError{Code: "verification_infra_failed"}
	}
	obs := in.Observed

	// ---- facts and their validation (fail closed) ----
	kinds := map[string]string{}
	for _, c := range in.Criteria {
		if c.Kind != "change" && c.Kind != "preservation" {
			return RedClassification{}, infraErr("criterion %q has unknown kind %q", c.ID, c.Kind)
		}
		if _, dup := kinds[c.ID]; dup || c.ID == "" {
			return RedClassification{}, infraErr("criterion id %q is empty or repeated", c.ID)
		}
		kinds[c.ID] = c.Kind
	}
	hasChange := false
	for _, c := range in.Criteria {
		hasChange = hasChange || c.Kind == "change"
	}
	if !hasChange {
		return RedClassification{}, infraErr("the frozen criteria hold no change criterion")
	}
	for ac := range in.Declared {
		if _, ok := kinds[ac]; !ok {
			return RedClassification{}, infraErr("declared mapping names unknown criterion %q", ac)
		}
	}
	if obs.TestIDSets == nil {
		return RedClassification{}, infraErr("observed document has no test_id_sets")
	}
	basePassing, err := decodeIDSet("base_passing", obs.TestIDSets.BasePassing)
	if err != nil {
		return RedClassification{}, err
	}
	baseSkipped, err := decodeIDSet("base_skipped", obs.TestIDSets.BaseSkipped)
	if err != nil {
		return RedClassification{}, err
	}
	headPresent, err := decodeIDSet("head_present", obs.TestIDSets.HeadPresent)
	if err != nil {
		return RedClassification{}, err
	}
	reported := map[string]TestObservation{}
	for _, t := range obs.Tests {
		if !validStatuses[t.StatusHead] || !validStatuses[t.StatusBase] {
			return RedClassification{}, infraErr("test %q has unknown status (base %q, head %q)", t.ID, t.StatusBase, t.StatusHead)
		}
		if _, dup := reported[t.ID]; dup {
			return RedClassification{}, infraErr("test %q is reported twice", t.ID)
		}
		reported[t.ID] = t
	}
	changedPaths := map[string]bool{}
	for _, f := range in.Scope.ChangedFiles {
		switch f.Change {
		case "added", "modified", "deleted", "renamed":
		default:
			return RedClassification{}, infraErr("changed file %q has unknown change %q", f.Path, f.Change)
		}
		switch f.Class {
		case "test", "fixture", "test_infra", "stub", "judge", "other":
		default:
			return RedClassification{}, infraErr("changed file %q has unknown class %q", f.Path, f.Class)
		}
		changedPaths[f.Path] = true
	}
	allowPanic, err := panicStubLanguages(in)
	if err != nil {
		return RedClassification{}, err
	}
	sp, err := LoadStubPolicy(in.Platform.Verification)
	if err != nil {
		return RedClassification{}, infraErr("%v", err)
	}
	protected, err := protectedPaths(in)
	if err != nil {
		return RedClassification{}, infraErr("%v", err)
	}
	var baseRuns, headRuns []RunSummary
	for _, r := range obs.Runs {
		switch r.Tree {
		case "base":
			baseRuns = append(baseRuns, r)
		case "head":
			headRuns = append(headRuns, r)
		default:
			return RedClassification{}, infraErr("run of suite %q has unknown tree %q", r.Suite, r.Tree)
		}
	}
	if len(baseRuns) == 0 {
		return RedClassification{}, infraErr("observed document has no base run")
	}

	var groups []reasonGroup
	add := func(outcome string, reasons ...string) {
		if len(reasons) > 0 {
			groups = append(groups, reasonGroup{outcome: outcome, reasons: reasons})
		}
	}

	// R0: the validation blob equals the base revision's (pipeline identity itself returned above).
	if obs.ValidationBlob == "" || obs.ValidationBlob != in.Identity.BaseValidationBlob {
		add(outcomeRedInvalid, "validation_config_mismatch")
	}

	// R1: scope (section 14.2), tester deletions, stubs.
	var r1 []string
	for _, f := range in.Scope.ChangedFiles {
		if f.Class == "other" || (f.Change == "deleted" && f.Class != "test" && f.Class != "fixture" && f.Class != "test_infra") {
			r1 = appendUnique(r1, "scope_violation")
		}
	}
	for _, f := range in.Scope.ChangedFiles {
		for _, g := range protected {
			if policy.MatchGlob(g, f.Path) {
				r1 = appendUnique(r1, "scope_violation")
				break
			}
		}
	}
	for _, f := range in.Scope.ChangedFiles {
		if f.Change == "deleted" || f.Change == "renamed" {
			r1 = appendUnique(r1, "tester_deletion")
		}
	}
	if len(in.Scope.Stubs) > sp.MaxStubsPerTask {
		r1 = appendUnique(r1, "stub_malformed")
	}
	// Every stub-class file holds at least one stub fact and every fact belongs to a stub-class file.
	stubFiles := map[string]bool{}
	for _, f := range in.Scope.ChangedFiles {
		if f.Class == "stub" {
			stubFiles[f.Path] = true
		}
	}
	factFiles := map[string]bool{}
	for _, s := range in.Scope.Stubs {
		factFiles[s.Path] = true
		if !stubFiles[s.Path] {
			r1 = appendUnique(r1, "stub_malformed")
		}
	}
	for p := range stubFiles {
		if !factFiles[p] {
			r1 = appendUnique(r1, "stub_malformed")
		}
	}
	for _, s := range in.Scope.Stubs {
		if !s.WellFormed || (s.Shape != "zero_value" && s.Shape != "panic") {
			r1 = appendUnique(r1, "stub_malformed")
		}
	}
	for _, s := range in.Scope.Stubs {
		if s.Shape == "panic" && !allowPanic[languageOfPath(s.Path)] {
			r1 = appendUnique(r1, "panic_stub_not_allowed")
		}
	}
	add(outcomeRedInvalid, r1...)

	// R2: base run is clean.
	var baseBroken bool
	for _, r := range baseRuns {
		baseBroken = baseBroken || r.BuildFailed || !r.ReportFound || r.TimedOut
	}
	for suite := range in.SuiteLanguages {
		found := false
		for _, r := range baseRuns {
			found = found || r.Suite == suite
		}
		baseBroken = baseBroken || !found
	}
	var flaky bool
	for _, t := range obs.Tests {
		switch t.StatusBase {
		case statusFailedAssertion, statusFailedError, statusStub:
			if t.StatusHead == statusPassed && t.File != "" && !changedPaths[t.File] {
				flaky = true
			} else {
				baseBroken = true
			}
		}
	}
	if baseBroken {
		add(outcomeBaselineFailing, "baseline_failing")
	}
	if flaky {
		o := outcomeInconclusive
		if baseBroken {
			o = outcomeBaselineFailing
		}
		add(o, "flaky_baseline")
	}

	// R3: head run built, reported, not timed out.
	var r3 []string
	for _, r := range headRuns {
		if r.BuildFailed {
			r3 = appendUnique(r3, "build_failed")
		}
	}
	for _, r := range headRuns {
		if !r.ReportFound {
			r3 = appendUnique(r3, "report_missing")
		}
	}
	for suite := range in.SuiteLanguages {
		found := false
		for _, r := range headRuns {
			found = found || r.Suite == suite
		}
		if !found {
			r3 = appendUnique(r3, "report_missing")
		}
	}
	if len(headRuns) == 0 {
		r3 = appendUnique(r3, "report_missing")
	}
	for _, r := range headRuns {
		if r.TimedOut {
			r3 = appendUnique(r3, "suite_timed_out")
		}
	}
	add(outcomeRedInvalid, r3...)

	// Declared tests resolved against the head ids (exact or `::`-suffix).
	headIDs := sortedKeys(headPresent)
	resolved := map[string][]string{} // criterion -> ids
	declaredResolved := map[string]bool{}
	var notDiscovered bool
	for _, c := range in.Criteria {
		set := map[string]bool{}
		for _, name := range in.Declared[c.ID] {
			found := false
			for _, id := range headIDs {
				if id == name || strings.HasSuffix(id, "::"+name) {
					set[id] = true
					found = true
				}
			}
			if !found {
				notDiscovered = true
			}
		}
		resolved[c.ID] = sortedKeys(set)
		for id := range set {
			declaredResolved[id] = true
		}
	}

	// N: absent at base and present at head. M: declared, passed at base, file changed in the red range.
	baseSeen := func(id string) bool {
		if basePassing[id] || baseSkipped[id] {
			return true
		}
		t, ok := reported[id]
		return ok && t.StatusBase != statusAbsent
	}
	nm := map[string]bool{}
	for id := range headPresent {
		if !baseSeen(id) {
			nm[id] = true
		}
	}
	m := map[string]bool{}
	for id := range declaredResolved {
		if t, ok := reported[id]; ok && basePassing[id] && t.File != "" && changedPaths[t.File] {
			m[id] = true
			nm[id] = true
		}
	}
	newTests := sortedKeys(nm)
	if !obs.TestsTruncated {
		for _, id := range newTests {
			if _, ok := reported[id]; !ok && headPresent[id] {
				return RedClassification{}, infraErr("new test %q is present at head but missing from tests of a complete document", id)
			}
		}
	}

	headStatus := func(id string) string {
		if t, ok := reported[id]; ok {
			return t.StatusHead
		}
		if !headPresent[id] {
			return statusAbsent
		}
		if basePassing[id] {
			return statusPassed
		}
		return "unknown"
	}
	isFailing := func(id string) bool {
		s := headStatus(id)
		if s == statusFailedAssertion {
			return true
		}
		return s == statusStub && allowPanic[languageOfSuite(in.SuiteLanguages, id)]
	}

	acResults := make([]ACResult, 0, len(in.Criteria))
	satisfiedChange, unsatisfiedChange := 0, 0
	for _, c := range in.Criteria {
		ids := resolved[c.ID]
		var failing, passed, errored bool
		for _, id := range ids {
			failing = failing || isFailing(id)
			passed = passed || headStatus(id) == statusPassed
			s := headStatus(id)
			errored = errored || s == statusFailedError || (s == statusStub && !isFailing(id))
		}
		out := "missing"
		switch {
		case c.Kind == "change" && failing:
			out = "failing_assertion"
		case c.Kind == "preservation" && passed:
			out = "passing"
		case c.Kind == "preservation" && failing:
			out = "failing_assertion"
		case errored:
			out = "error"
		case passed:
			out = "passing"
		}
		if ids == nil {
			ids = []string{}
		}
		acResults = append(acResults, ACResult{ACID: c.ID, Kind: c.Kind, Tests: ids, Outcome: out})
		if c.Kind == "change" {
			if out == "failing_assertion" {
				satisfiedChange++
			} else {
				unsatisfiedChange++
			}
		}
	}

	headOK := len(r3) == 0
	if headOK {
		// R4: new tests exist.
		if len(nm) == 0 {
			add(outcomeRedInvalid, "no_new_tests")
		}
		// R5: every declared test is discovered, new (N or M) and not skipped.
		var r5 []string
		if notDiscovered {
			r5 = append(r5, "declared_test_not_discovered")
		}
		for _, id := range sortedKeys(declaredResolved) {
			if !nm[id] {
				r5 = appendUnique(r5, "declared_test_not_new")
			}
		}
		for _, id := range sortedKeys(declaredResolved) {
			if nm[id] && headStatus(id) == statusSkipped {
				r5 = appendUnique(r5, "new_test_skipped")
			}
		}
		add(outcomeRedInvalid, r5...)
		// R6: coverage of every criterion.
		for _, c := range in.Criteria {
			if len(in.Declared[c.ID]) == 0 {
				add(outcomeRedInvalid, "ac_not_covered")
				break
			}
		}
		// R7: no error-class result among the new tests.
		var r7 []string
		for _, id := range newTests {
			switch headStatus(id) {
			case statusFailedError:
				r7 = appendUnique(r7, "error_class_failure")
			}
		}
		for _, id := range newTests {
			if headStatus(id) == statusStub && !allowPanic[languageOfSuite(in.SuiteLanguages, id)] {
				r7 = appendUnique(r7, "panic_stub_not_allowed")
			}
		}
		add(outcomeRedInvalid, r7...)
		// R8: pre-existing tests of the head scope still pass.
		var r8 []string
		for _, id := range sortedKeys(basePassing) {
			if m[id] || !inHeadScope(in, id) {
				continue
			}
			switch {
			case !headPresent[id] || headStatus(id) == statusAbsent:
				r8 = appendUnique(r8, "preexisting_test_removed")
			case headStatus(id) != statusPassed && headStatus(id) != "unknown":
				r8 = appendUnique(r8, "preexisting_test_broken")
			}
		}
		sort.SliceStable(r8, func(i, j int) bool { return r8[i] == "preexisting_test_broken" && r8[j] != "preexisting_test_broken" })
		add(outcomeRedInvalid, r8...)
		// R9: preservation criteria pass at head.
		for i, c := range in.Criteria {
			if c.Kind == "preservation" && acResults[i].Outcome != "passing" {
				add(outcomeRedInvalid, "preservation_ac_not_passing")
				break
			}
		}
		// R11: truncation must not hide a test the verdict depends on.
		if obs.TestsTruncated {
			hidden := false
			for _, id := range newTests {
				_, ok := reported[id]
				hidden = hidden || !ok
			}
			for id := range declaredResolved {
				_, ok := reported[id]
				hidden = hidden || !ok
			}
			if hidden {
				add(outcomeInconclusive, "tests_truncated")
			}
		}
	}

	cl := RedClassification{
		Reasons: []string{}, ACResults: acResults, NewTests: newTests,
		ComputedBy: "runner", ComputedAt: in.ComputedAt, PolicyRevision: in.PolicyRevision, VerifierLibVersion: verifierLibVersion,
	}
	if cl.NewTests == nil {
		cl.NewTests = []string{}
	}
	if len(groups) > 0 {
		if headOK && unsatisfiedChange > 0 {
			// R10 never decides here, but its reason is still listed (plan 22 section 14.3).
			groups = append(groups, reasonGroup{outcome: outcomeAlreadySatisfied, reasons: []string{"change_ac_not_failing"}})
		}
		cl.Outcome = groups[0].outcome
		for _, g := range groups {
			cl.Reasons = appendUnique(cl.Reasons, g.reasons...)
		}
		return cl, nil
	}
	// R10: change criteria. Every change criterion failing by assertion: red_valid.
	switch {
	case unsatisfiedChange == 0:
		cl.Outcome = outcomeRedValid
	case satisfiedChange == 0:
		cl.Outcome = outcomeAlreadySatisfied
		cl.Reasons = []string{"change_ac_not_failing"}
	default:
		cl.Outcome = outcomePartiallySatisfied
		cl.Reasons = []string{"change_ac_not_failing"}
	}
	return cl, nil
}

func languageOfSuite(suites map[string]string, id string) string { return suites[suiteOf(id)] }

// inHeadScope reports whether a base test belongs to what the head run executed: everything when unscoped; with a
// Go package scope, only the tests of the listed packages (every other suite runs unscoped at RED).
func inHeadScope(in RedInput, id string) bool {
	suite := suiteOf(id)
	pkgs := in.HeadScope[suite]
	if len(pkgs) == 0 || in.SuiteLanguages[suite] != "go" {
		return true
	}
	return inGoScope(id, pkgs)
}
