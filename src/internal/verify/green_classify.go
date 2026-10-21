package verify

import (
	"fmt"
	"sort"
)

// Outcomes of GreenClassification.
const (
	outcomeGreenPass = "green_pass"
	outcomeGreenFail = "green_fail"
	outcomeTamper    = "tamper_detected"
)

// ClassifyGreen applies rules G0-G9 of plan 22 section 14.7 to an observed GREEN. It is pure and deterministic. The
// first failing rule in table order decides the outcome; every failing rule contributes its reasons, in rule order.
// G0 identity failure and any unusable or inconsistent fact are errors carrying a *NotClassifiedError
// (verification_infra_failed), never a classification. Facts the library can recompute from the documents it holds
// (missing protected tests, manifest file status, the isolation file digest) are recomputed, never trusted.
func ClassifyGreen(in GreenInput) (GreenClassification, error) {
	if !in.Identity.Passed {
		return GreenClassification{}, &NotClassifiedError{Code: "verification_infra_failed"}
	}
	f, err := newGreenFacts(in)
	if err != nil {
		return GreenClassification{}, err
	}
	var groups []reasonGroup
	add := func(outcome string, reasons ...string) {
		if len(reasons) > 0 {
			groups = append(groups, reasonGroup{outcome: outcome, reasons: reasons})
		}
	}
	rules := []func(*greenFacts, func(string, ...string)) error{
		ruleG0, ruleG1, ruleG2, ruleG3, ruleG4, ruleG4b, ruleG4c, ruleG5, ruleG5b, ruleG6, ruleG7, ruleG8, ruleG9,
	}
	for _, r := range rules {
		if err := r(f, add); err != nil {
			return GreenClassification{}, err
		}
	}
	cl := GreenClassification{
		Outcome: outcomeGreenPass, Reasons: []string{}, Warnings: f.warnings,
		ComputedBy: "runner", ComputedAt: in.ComputedAt, PolicyRevision: in.PolicyRevision, VerifierLibVersion: verifierLibVersion,
	}
	if len(groups) > 0 {
		cl.Outcome = groups[0].outcome
		for _, g := range groups {
			cl.Reasons = appendUnique(cl.Reasons, g.reasons...)
		}
	}
	return cl, nil
}

type greenAdd = func(outcome string, reasons ...string)

// greenFacts holds the validated, decoded inputs the rules share.
type greenFacts struct {
	in           GreenInput
	reported     map[string]TestObservation
	headPresent  map[string]bool
	headSkipped  map[string]bool
	basePassing  map[string]bool
	manifestIDs  map[string]bool
	manifestList []string
	protected    []string
	headRuns     []RunSummary
	truncatedGap bool // a protected test is in the id set but not in the (truncated) tests list
	warnings     []string
}

var integrityStatuses = map[string]bool{"intact": true, "modified": true, "deleted": true, "renamed": true}

func newGreenFacts(in GreenInput) (*greenFacts, error) {
	f := &greenFacts{in: in, reported: map[string]TestObservation{}, manifestIDs: map[string]bool{}}
	obs, man := in.Observed, in.Manifest

	// G0 identity of the document.
	if obs.Phase != "green" || obs.Subject != "tests" {
		return nil, infraErr("observed document is phase %q subject %q, want green/tests", obs.Phase, obs.Subject)
	}
	if obs.TaskID != man.TaskID || obs.Repository != man.Repository {
		return nil, infraErr("observed document is for %s/%s, the manifest for %s/%s", obs.Repository, obs.TaskID, man.Repository, man.TaskID)
	}
	if !objectIDRE.MatchString(obs.HeadSha) {
		return nil, infraErr("observed head_sha %q is not a commit id", obs.HeadSha)
	}
	for _, r := range obs.Runs {
		switch r.Tree {
		case "base":
		case "head":
			if r.Commit != obs.HeadSha {
				return nil, infraErr("head run of suite %q is for commit %q, not the head %q", r.Suite, r.Commit, obs.HeadSha)
			}
			f.headRuns = append(f.headRuns, r)
		default:
			return nil, infraErr("run of suite %q has unknown tree %q", r.Suite, r.Tree)
		}
	}
	if len(f.headRuns) == 0 {
		return nil, infraErr("observed document has no head run")
	}
	if d, err := manifestDigest(man); err != nil || d != man.Digest {
		return nil, infraErr("manifest digest %q does not match its content", man.Digest)
	}
	if in.Integrity.RedCommit != man.TestCommit || in.Integrity.ManifestDigest != man.Digest || in.Integrity.Generation != man.Generation {
		return nil, infraErr("integrity facts are for another manifest (generation %d, red %q, digest %q)", in.Integrity.Generation, in.Integrity.RedCommit, in.Integrity.ManifestDigest)
	}
	if in.Baseline.Phase != "red" || in.Baseline.TaskID != man.TaskID {
		return nil, infraErr("baseline is not the red observation of task %s", man.TaskID)
	}

	// Tests.
	for _, t := range obs.Tests {
		if !validStatuses[t.StatusHead] {
			return nil, infraErr("test %q has unknown status %q", t.ID, t.StatusHead)
		}
		if _, dup := f.reported[t.ID]; dup {
			return nil, infraErr("test %q is reported twice", t.ID)
		}
		f.reported[t.ID] = t
	}
	if obs.TestIDSets == nil {
		return nil, infraErr("observed document has no test_id_sets")
	}
	var err error
	if f.headPresent, err = decodeIDSet("head_present", obs.TestIDSets.HeadPresent); err != nil {
		return nil, err
	}
	if f.headSkipped, err = decodeIDSet("head_skipped", obs.TestIDSets.HeadSkipped); err != nil {
		return nil, err
	}
	if in.Baseline.TestIDSets == nil {
		return nil, infraErr("baseline has no test_id_sets")
	}
	if f.basePassing, err = decodeIDSet("base_passing", in.Baseline.TestIDSets.BasePassing); err != nil {
		return nil, err
	}
	baseSkipped, err := decodeIDSet("base_skipped", in.Baseline.TestIDSets.BaseSkipped)
	if err != nil {
		return nil, err
	}
	for id := range f.basePassing {
		if baseSkipped[id] {
			return nil, infraErr("baseline lists %q as both passing and skipped", id)
		}
	}
	for id := range f.headSkipped {
		if !f.headPresent[id] {
			return nil, infraErr("head_skipped lists %q, which head_present lacks", id)
		}
	}
	for _, t := range man.Tests {
		if f.manifestIDs[t.ID] {
			return nil, infraErr("manifest lists test %q twice", t.ID)
		}
		f.manifestIDs[t.ID] = true
		f.manifestList = append(f.manifestList, t.ID)
	}
	f.protected = uniqueSorted(in.ProtectedTestIDs)
	for _, id := range append(append([]string{}, f.manifestList...), f.protected...) {
		t, rep := f.reported[id]
		if rep && !f.headPresent[id] && t.StatusHead != statusAbsent {
			return nil, infraErr("test %q is reported %s but absent from head_present", id, t.StatusHead)
		}
	}
	return f, nil
}

func uniqueSorted(l []string) []string {
	m := map[string]bool{}
	for _, s := range l {
		m[s] = true
	}
	return sortedKeys(m)
}

// ruleG0: the validation blob equals the base revision's (the pipeline identity returned above).
func ruleG0(f *greenFacts, add greenAdd) error {
	if f.in.Observed.ValidationBlob == "" || f.in.Observed.ValidationBlob != f.in.Identity.BaseValidationBlob {
		add(outcomeGreenFail, "validation_config_mismatch")
	}
	return nil
}

// ruleG1: the head descends from the red commit.
func ruleG1(f *greenFacts, add greenAdd) error {
	if !f.in.Integrity.HeadDescendsFromRed {
		add(outcomeTamper, "not_descendant_of_red")
	}
	return nil
}

var staticFindingReason = map[string]string{
	"skip_marker_added": "skip_marker_added", "test_env_detection": "test_env_detection",
	"test_file_deleted": "test_file_deleted", "test_file_renamed": "test_file_deleted",
	"existing_test_modified": "existing_test_modified", "test_dependency_changed": "test_dependency_changed",
	"new_discovery_file": "discovery_config_changed",
	// A protected path is configuration of the pipeline or the policy; it reports as a config change.
	"protected_path_changed": "discovery_config_changed",
}

var g2Order = []string{"manifest_file_changed", "discovery_config_changed", "skip_marker_added", "test_env_detection",
	"test_file_deleted", "existing_test_modified", "test_dependency_changed", "foreign_commit"}

// ruleG2: integrity (manifest files, discovery, static findings, protected paths, foreign commits).
func ruleG2(f *greenFacts, add greenAdd) error {
	integ := f.in.Integrity
	got := map[string]bool{}
	seen := map[string]bool{}
	for _, mf := range f.in.Manifest.Files {
		seen[mf.Path] = false
	}
	for _, e := range integ.ManifestFiles {
		if !integrityStatuses[e.Status] {
			return infraErr("manifest file %q has unknown status %q", e.Path, e.Status)
		}
		was, ok := seen[e.Path]
		if !ok || was {
			return infraErr("integrity lists manifest file %q, which the manifest does not list once", e.Path)
		}
		seen[e.Path] = true
		for _, mf := range f.in.Manifest.Files {
			if mf.Path == e.Path && mf.BlobSha != e.ExpectedBlob {
				return infraErr("integrity expects blob %q for %q, the manifest records %q", e.ExpectedBlob, e.Path, mf.BlobSha)
			}
		}
		// Recompute the status from the blobs: a forged "intact" never vouches.
		if e.Status != "intact" || e.ActualBlob == nil || *e.ActualBlob != e.ExpectedBlob {
			got["manifest_file_changed"] = true
		}
	}
	for p, ok := range seen {
		if !ok {
			return infraErr("integrity does not list manifest file %q", p)
		}
	}
	for _, d := range integ.DiscoveryChanges {
		if d.Kind != "whole_file_changed" && d.Kind != "semantic_key_changed" {
			return infraErr("discovery change at %q has unknown kind %q", d.Path, d.Kind)
		}
		got["discovery_config_changed"] = true
	}
	for _, s := range integ.StaticFindings {
		switch s.Severity {
		case "warning":
			f.warnings = append(f.warnings, truncRunes(fmt.Sprintf("%s: %s", s.Code, s.Path), 300))
		case "violation":
			r, ok := staticFindingReason[s.Code]
			if !ok {
				return infraErr("static finding %q at %q has no tamper reason", s.Code, s.Path)
			}
			got[r] = true
		default:
			return infraErr("static finding at %q has unknown severity %q", s.Path, s.Severity)
		}
	}
	for _, c := range integ.ForeignCommits {
		if !objectIDRE.MatchString(c) {
			return infraErr("foreign commit %q is not a commit id", c)
		}
		got["foreign_commit"] = true
	}
	var reasons []string
	for _, r := range g2Order {
		if got[r] {
			reasons = append(reasons, r)
		}
	}
	add(outcomeTamper, reasons...)
	return nil
}

// ruleG3: every suite built, reported, not timed out.
func ruleG3(f *greenFacts, add greenAdd) error {
	ran := map[string]bool{}
	var build, report, timeout bool
	for _, r := range f.headRuns {
		ran[r.Suite] = true
		switch {
		case r.BuildFailed:
			build = true
		case !r.ReportFound:
			report = true
		}
		timeout = timeout || r.TimedOut
	}
	for _, id := range f.manifestList {
		if !ran[suiteOf(id)] {
			report = true
		}
	}
	var reasons []string
	if build {
		reasons = append(reasons, "build_failed")
	}
	if report {
		reasons = append(reasons, "report_missing")
	}
	if timeout {
		reasons = append(reasons, "suite_timed_out")
	}
	add(outcomeGreenFail, reasons...)
	return nil
}

// ruleG4: every manifest test is discovered, not skipped and passed.
func ruleG4(f *greenFacts, add greenAdd) error {
	recorded := map[string]bool{}
	for _, id := range f.in.Integrity.ProtectedTestsMissing {
		if !f.manifestIDs[id] {
			return infraErr("integrity lists %q as a missing protected test, which the manifest does not name", id)
		}
		recorded[id] = true
	}
	var missing, skipped, failing bool
	for _, id := range f.manifestList {
		t, rep := f.reported[id]
		switch {
		case recorded[id] || (!rep && !f.headPresent[id]) || (rep && t.StatusHead == statusAbsent):
			missing = true
		case !rep:
			if !f.in.Observed.TestsTruncated {
				return infraErr("manifest test %q is in head_present but not reported, and the report is not truncated", id)
			}
		case t.StatusHead == statusSkipped || f.headSkipped[id]:
			skipped = true
		case t.StatusHead != statusPassed:
			failing = true
		}
	}
	var tamper []string
	if missing {
		tamper = append(tamper, "protected_test_missing")
	}
	if skipped {
		tamper = append(tamper, "protected_test_skipped")
	}
	add(outcomeTamper, tamper...)
	if failing {
		add(outcomeGreenFail, "protected_test_failing")
	}
	return nil
}

// ruleG4b: the manifest tests pass in head minus the test files added after the red commit.
func ruleG4b(f *greenFacts, add greenAdd) error {
	iso := f.in.Observed.Isolation
	if iso == nil {
		return infraErr("observed document has no isolation run")
	}
	want := f.in.Isolation
	files := append([]string{}, want.Files...)
	sort.Strings(files)
	if recomputed := isolationFacts(files); recomputed.Count != want.Count || recomputed.Sha256 != want.Sha256 {
		return infraErr("isolation facts are inconsistent with their files")
	}
	if iso.RemovedFiles != want.Count || iso.RemovedFilesSha256 != want.Sha256 {
		return infraErr("the isolation run removed %d files (%s), the Runner derived %d (%s)", iso.RemovedFiles, iso.RemovedFilesSha256, want.Count, want.Sha256)
	}
	status, err := replayStatuses("isolation", iso.Tests)
	if err != nil {
		return err
	}
	for _, id := range f.manifestList {
		if status[id] != statusPassed {
			add(outcomeTamper, "protected_pass_depends_on_new_test_files")
			break
		}
	}
	return nil
}

// replayStatuses indexes the tests of an isolation or replay run; a repeated id or unknown status is unusable.
func replayStatuses(what string, tests []ReplayTest) (map[string]string, error) {
	out := make(map[string]string, len(tests))
	for _, t := range tests {
		if !validStatuses[t.Status] {
			return nil, infraErr("%s run: test %q has unknown status %q", what, t.ID, t.Status)
		}
		if _, dup := out[t.ID]; dup {
			return nil, infraErr("%s run: test %q is reported twice", what, t.ID)
		}
		out[t.ID] = t.Status
	}
	return out, nil
}

// ruleG4c: every manifest test passes in all repeatProtectedTests runs.
func ruleG4c(f *greenFacts, add greenAdd) error {
	rep := f.in.Observed.Repeats
	if rep == nil {
		return infraErr("observed document has no repeat runs")
	}
	green, ok := pathMap(f.in.Platform.Verification, "spec", "green")
	if !ok {
		return infraErr("verification policy has no spec.green")
	}
	need, ok := intValue(green["repeatProtectedTests"])
	if !ok || need < 1 {
		return infraErr("spec.green.repeatProtectedTests is not a positive integer")
	}
	if rep.Runs < need {
		return infraErr("%d repeat runs, green.repeatProtectedTests requires %d", rep.Runs, need)
	}
	bad := map[string]bool{}
	for _, id := range rep.Inconsistent {
		bad[id] = true
	}
	for _, id := range f.manifestList {
		if bad[id] {
			add(outcomeGreenFail, "nondeterministic_test")
			break
		}
	}
	return nil
}

// ruleG5: every other test passes or is skipped, nothing that passed at base is skipped now, every protectedTestIds
// entry passes.
func ruleG5(f *greenFacts, add greenAdd) error {
	var failing, newSkip bool
	for _, t := range f.in.Observed.Tests {
		if f.manifestIDs[t.ID] {
			continue
		}
		switch t.StatusHead {
		case statusFailedAssertion, statusFailedError, statusStub, statusAbsent:
			failing = true
		}
	}
	for id := range f.basePassing {
		if t, rep := f.reported[id]; (rep && t.StatusHead == statusSkipped) || f.headSkipped[id] {
			newSkip = true
		}
	}
	for _, id := range f.protected {
		t, rep := f.reported[id]
		switch {
		case rep:
			if t.StatusHead != statusPassed || f.headSkipped[id] {
				failing = true
			}
		case f.headPresent[id]:
			if !f.in.Observed.TestsTruncated {
				return infraErr("protected test %q is in head_present but not reported, and the report is not truncated", id)
			}
		default:
			failing = true // absent counts as not passing
		}
	}
	if newSkip {
		add(outcomeTamper, "new_skip_vs_base")
	}
	if failing {
		add(outcomeGreenFail, "existing_test_failing")
	}
	return nil
}

// ruleG5b: every test of the full RED baseline passing set is present and not skipped at the head, unless an approved
// generation deleted its file.
func ruleG5b(f *greenFacts, add greenAdd) error {
	approved := map[string]bool{}
	for _, id := range f.in.ApprovedRemovedTests {
		approved[id] = true
	}
	for id := range f.basePassing {
		if (!f.headPresent[id] && !approved[id]) || f.headSkipped[id] {
			add(outcomeTamper, "preexisting_test_removed")
			break
		}
	}
	return nil
}

// ruleG6: every required check ran, exited 0 and did not time out. A check is required when the document says so or
// the validation.yaml of the base revision names it.
func ruleG6(f *greenFacts, add greenAdd) error {
	required := map[string]bool{}
	for _, n := range f.in.RequiredChecks {
		required[n] = true
	}
	ran := map[string]bool{}
	failed := false
	for _, c := range f.in.Observed.Checks {
		if ran[c.Name] {
			return infraErr("check %q is reported twice", c.Name)
		}
		ran[c.Name] = true
		if (c.Required || required[c.Name]) && (c.ExitCode != 0 || c.TimedOut) {
			failed = true
		}
	}
	for n := range required {
		if !ran[n] {
			failed = true
		}
	}
	if failed {
		add(outcomeGreenFail, "required_check_failed")
	}
	return nil
}

// ruleG7: no manifest stub marker remains.
func ruleG7(f *greenFacts, add greenAdd) error {
	if len(f.in.Integrity.StubsRemaining) > 0 {
		add(outcomeGreenFail, "stub_not_replaced")
	}
	return nil
}

// ruleG8: a truncated test list must still report every protected test.
func ruleG8(f *greenFacts, add greenAdd) error {
	if !f.in.Observed.TestsTruncated {
		return nil
	}
	for _, id := range append(append([]string{}, f.manifestList...), f.protected...) {
		if _, rep := f.reported[id]; !rep {
			add(outcomeGreenFail, "tests_truncated")
			return nil
		}
	}
	return nil
}

// ruleG9: against the red export without stub markers, every change-criterion manifest test still fails by assertion
// (or as a declared stub). A pass cannot tell a test that keys on the commit subject, SC_PHASE or the marker from
// any other non-reproduction, so the reason is red_not_reproducible.
func ruleG9(f *greenFacts, add greenAdd) error {
	rp := f.in.Observed.RedReplay
	if rp == nil {
		return infraErr("observed document has no RED replay")
	}
	if rp.RedCommit != f.in.Manifest.TestCommit {
		return infraErr("the RED replay is for commit %q, the manifest's red commit is %q", rp.RedCommit, f.in.Manifest.TestCommit)
	}
	status, err := replayStatuses("red replay", rp.Tests)
	if err != nil {
		return err
	}
	change := map[string]bool{}
	for _, ac := range f.in.Manifest.ACMapping {
		if ac.Kind == "change" {
			for _, id := range ac.Tests {
				change[id] = true
			}
		}
	}
	for _, id := range sortedKeys(change) {
		if s := status[id]; s != statusFailedAssertion && s != statusStub {
			add(outcomeTamper, "red_not_reproducible")
			return nil
		}
	}
	return nil
}
