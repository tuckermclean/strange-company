package verify

import (
	"errors"
	"testing"
)

// Decision D22 (S5 review 1). Facts are derived from real scratch repositories (B1) or built on the valid
// baseline of s5_helpers_test.go (B2, N1, N2, N3: classification inputs of the observed document). No testdata is
// needed: every input is a Go mutation of g5BaseFixture and stays valid under its contract (the manifest, the
// integrity and the observation are the baseline's), except where a case comment says otherwise.
//
// Reading of N1 that the existing fixtures force: a G5 fixture such as testdata/green/G5/existing_test_failing_assertion
// reports a failing test with counts of 0 and exit code 0 and expects green_fail, so "counts disagree with the reported
// tests" is encoded only in the direction counts > reported failures (the dropped-failure direction). Likewise
// testdata/green/G3/build_failed has exit code 1 with passing counts and report_found false, so the exit-code cases
// below keep report_found true and build_failed false.

func d22Infra(t *testing.T, name string, got GreenClassification, err error) {
	t.Helper()
	var nc *NotClassifiedError
	if !errors.As(err, &nc) {
		t.Errorf("%s: err = %v (classification %s %v), want a *NotClassifiedError verification_infra_failed", name, err, got.Outcome, got.Reasons)
		return
	}
	if nc.Code != "verification_infra_failed" {
		t.Errorf("%s: error code = %q, want verification_infra_failed", name, nc.Code)
	}
}

// ---- B1: a manifest file is modified when its mode or type changes (22-AC-27) ----

func d22ModifiedWithG2(t *testing.T, sc *g5Scenario, head string, integ Integrity) {
	t.Helper()
	if len(integ.ManifestFiles) == 0 {
		t.Fatalf("manifest_files is empty")
	}
	modified := false
	for _, f := range integ.ManifestFiles {
		modified = modified || f.Status == "modified"
	}
	if !modified {
		t.Errorf("manifest_files = %+v, want a file with status modified", integ.ManifestFiles)
	}
	in, got, err := sc.classify(t, head, integ, nil)
	g5Check(t, in, got, err, greenExpect{Outcome: "tamper_detected", Contains: []string{"manifest_file_changed"}})
}

func TestDeriveIntegrity_ManifestFileModeChange_D22_22AC27(t *testing.T) {
	sc := g5NewScenario(t, g5Langs[0])
	sc.s.write("pkg/impl.go", sc.lang.real)
	sc.s.chmod(sc.lang.testPath, 0o755)
	head := sc.s.commit("feat(sc-9): implement F")
	if sc.s.blobAt(sc.red, sc.lang.testPath) != sc.s.blobAt(head, sc.lang.testPath) || sc.s.modeAt(sc.red, sc.lang.testPath) == sc.s.modeAt(head, sc.lang.testPath) {
		t.Fatalf("scenario: want the same blob and a different mode (red %s, head %s)", sc.s.modeAt(sc.red, sc.lang.testPath), sc.s.modeAt(head, sc.lang.testPath))
	}
	integ := sc.integrity(t, head, []string{head}, nil)
	d22ModifiedWithG2(t, sc, head, integ)
}

// d22SymlinkScenario: a manifest fixture whose bytes equal a path; with swap it is replaced by a symlink to that path
// (identical blob, mode 120000) and the implementation is real.
func d22SymlinkScenario(t *testing.T, swap bool) (*g5Scenario, string) {
	t.Helper()
	s := newScratch(t)
	s.write("README.md", "x\n")
	s.write("pkg/old.go", "package pkg\n")
	base := s.commit("base")
	s.write("pkg/impl.go", "package pkg\n\n// "+g5Marker+"\nfunc F() int { return 0 }\n")
	s.write("pkg/behavior_test.go", "package pkg\n")
	s.writeBytes("pkg/testdata/want", []byte("../../docs/want.txt"))
	red := s.commit("test(sc-9): x")
	man := s.s3Manifest(red, "pkg/behavior_test.go", "pkg/testdata/want")
	man.Files[1].Class = "fixture"
	fx := g5BaseFixture(t)
	man.Tests, man.ACMapping, man.CreatedAt, man.RedEvidenceDigest = fx.Manifest.Tests, fx.Manifest.ACMapping, fx.Manifest.CreatedAt, fx.Manifest.RedEvidenceDigest
	man.DiscoveryProtection, man.TestPaths = fx.Manifest.DiscoveryProtection, fx.Manifest.TestPaths
	man.BaseCommit, man.GenerationStartCommit = base, base
	man.Stubs = []ManifestStub{{Path: "pkg/impl.go", Symbol: "F", Marker: g5Marker, Shape: "zero_value", Language: "go", ACIDs: []string{"AC-2"}}}
	d, err := manifestDigest(man)
	if err != nil {
		t.Fatal(err)
	}
	man.Digest = d
	sc := &g5Scenario{s: s, base: base, red: red, man: man, lang: g5Langs[0]}
	s.write("pkg/impl.go", "package pkg\n\nfunc F() int { return 42 }\n")
	if swap {
		s.write("docs/want.txt", "whatever the implementer wants\n")
		s.remove("pkg/testdata/want")
		s.symlink("pkg/testdata/want", "../../docs/want.txt")
	}
	head := s.commit("feat(sc-9): implement F")
	return sc, head
}

func TestDeriveIntegrity_ManifestFileSymlinkSameBlob_D22_22AC27(t *testing.T) {
	sc, head := d22SymlinkScenario(t, true)
	if sc.s.blobAt(sc.red, "pkg/testdata/want") != sc.s.blobAt(head, "pkg/testdata/want") || sc.s.modeAt(head, "pkg/testdata/want") != "120000" {
		t.Fatalf("scenario: want an identical blob and mode 120000 (mode %s)", sc.s.modeAt(head, "pkg/testdata/want"))
	}
	integ := sc.integrity(t, head, []string{head}, nil)
	d22ModifiedWithG2(t, sc, head, integ)
}

// A manifest_file_changed violation naming a file whose blob and mode are unchanged still makes it modified: the
// file is never intact while CheckIngestion flags it. (The violation is fed by hand; CheckIngestion would not report
// this unchanged file, which is the point of the input.)
func TestDeriveIntegrity_ManifestFileViolationWins_D22_22AC27(t *testing.T) {
	sc := g5NewScenario(t, g5Langs[0])
	sc.s.write("pkg/impl.go", sc.lang.real)
	head := sc.s.commit("feat(sc-9): implement F")
	derive := func(v []Violation) Integrity {
		integ, err := DeriveIntegrity(IntegrityInput{Git: sc.s.repo(), Head: head, Manifest: sc.man, Violations: v, AcceptedCommits: []string{head}})
		if err != nil {
			t.Fatalf("DeriveIntegrity: %v", err)
		}
		return integ
	}
	integ := derive([]Violation{{Code: "TEST_TAMPER", SubCode: "manifest_file_changed", Path: sc.lang.testPath}})
	if len(integ.ManifestFiles) != 1 || integ.ManifestFiles[0].Status != "modified" {
		t.Errorf("manifest_files with a manifest_file_changed violation = %+v, want status modified", integ.ManifestFiles)
	}
	// Twin: no violation, nothing changed: intact.
	integ = derive(nil)
	if len(integ.ManifestFiles) != 1 || integ.ManifestFiles[0].Status != "intact" {
		t.Errorf("manifest_files without a violation = %+v, want status intact", integ.ManifestFiles)
	}
}

// Twins of B1: a real implementation with untouched manifest files passes (a file test and a fixture).
func TestClassifyGreen_ManifestFilesUntouched_D22Twin_22AC27(t *testing.T) {
	sc := g5NewScenario(t, g5Langs[0])
	sc.s.write("pkg/impl.go", sc.lang.real)
	head := sc.s.commit("feat(sc-9): implement F")
	integ := sc.integrity(t, head, []string{head}, nil)
	in, got, err := sc.classify(t, head, integ, nil)
	g5Check(t, in, got, err, greenExpect{Outcome: "green_pass", Reasons: &[]string{}})

	sc2, head2 := d22SymlinkScenario(t, false)
	integ = sc2.integrity(t, head2, []string{head2}, nil)
	in, got, err = sc2.classify(t, head2, integ, nil)
	g5Check(t, in, got, err, greenExpect{Outcome: "green_pass", Reasons: &[]string{}})
}

// ---- B2: a non-passing test dropped by truncation (22-AC-25) ----

func TestClassifyGreen_TruncatedHidesFailure_D22_22AC25(t *testing.T) {
	// Truncated report; every manifest and protected test is reported; the counts show one non-passing test more
	// than the report lists.
	for _, c := range []struct {
		name   string
		counts Counts
	}{
		{"failed_assertion", Counts{Total: 8, Passed: 6, FailedAssertion: 1, Skipped: 1}},
		{"failed_error", Counts{Total: 8, Passed: 6, FailedError: 1, Skipped: 1}},
		{"stub_unimplemented", Counts{Total: 8, Passed: 6, StubUnimplemented: 1, Skipped: 1}},
	} {
		t.Run(c.name, func(t *testing.T) {
			fx := g5BaseFixture(t)
			fx.Observed.TestsTruncated = true
			fx.Observed.Runs[0].Counts = c.counts
			fx.Observed.Runs[0].ExitCode = 1
			in := g5InputOf(t, fx)
			got, err := ClassifyGreen(in)
			g5Check(t, in, got, err, greenExpect{Outcome: "green_fail", Contains: []string{"existing_test_failing"}})
		})
	}
	// Twin: truncated, the counts match the reported failures (none): green_pass is not blocked by truncation alone.
	fx := g5BaseFixture(t)
	fx.Observed.TestsTruncated = true
	in := g5InputOf(t, fx)
	got, err := ClassifyGreen(in)
	g5Check(t, in, got, err, greenExpect{Outcome: "green_pass", Reasons: &[]string{}})
}

// ---- N1: a non-truncated report is checked against its own counts and exit code (22-AC-23) ----

func TestClassifyGreen_CountsAndExitCodeConsistency_D22_22AC23(t *testing.T) {
	infra := func(name string, mutate func(*greenFixture)) {
		t.Run(name, func(t *testing.T) {
			fx := g5BaseFixture(t)
			mutate(&fx)
			got, err := ClassifyGreen(g5InputOf(t, fx))
			d22Infra(t, name, got, err)
		})
	}
	for _, c := range []struct {
		name   string
		counts Counts
	}{
		{"counts_failed_error_nothing_reported", Counts{Total: 8, Passed: 6, FailedError: 1, Skipped: 1}},
		{"counts_failed_assertion_nothing_reported", Counts{Total: 8, Passed: 6, FailedAssertion: 1, Skipped: 1}},
		{"counts_stub_unimplemented_nothing_reported", Counts{Total: 8, Passed: 6, StubUnimplemented: 1, Skipped: 1}},
	} {
		infra(c.name, func(fx *greenFixture) {
			fx.Observed.Runs[0].Counts = c.counts
			fx.Observed.Runs[0].ExitCode = 1 // consistent with the counts: only the report disagrees
		})
	}
	infra("exit_1_all_counts_passing", func(fx *greenFixture) {
		fx.Observed.Runs[0].ExitCode = 1 // report_found true, build_failed false, timed_out false (baseline)
	})
	infra("exit_0_with_failing_count", func(fx *greenFixture) {
		fx.Observed.Tests = append(fx.Observed.Tests, TestObservation{ID: g5ID("TestOld2"), Suite: "unit", StatusHead: "failed_assertion"})
		fx.Observed.Runs[0].Counts = Counts{Total: 8, Passed: 6, FailedAssertion: 1, Skipped: 1} // matches the report
		fx.Observed.Runs[0].ExitCode = 0
	})

	// Twins: consistent counts, report and exit code. A reported failure with matching counts and exit 1 is
	// classified (G5), not refused; the baseline passes.
	fx := g5BaseFixture(t)
	fx.Observed.Tests = append(fx.Observed.Tests, TestObservation{ID: g5ID("TestOld2"), Suite: "unit", StatusHead: "failed_assertion"})
	fx.Observed.Runs[0].Counts = Counts{Total: 8, Passed: 6, FailedAssertion: 1, Skipped: 1}
	fx.Observed.Runs[0].ExitCode = 1
	in := g5InputOf(t, fx)
	got, err := ClassifyGreen(in)
	g5Check(t, in, got, err, greenExpect{Outcome: "green_fail", Contains: []string{"existing_test_failing"}})

	in = g5InputOf(t, g5BaseFixture(t))
	got, err = ClassifyGreen(in)
	g5Check(t, in, got, err, greenExpect{Outcome: "green_pass", Reasons: &[]string{}})
}

// ---- N2: the baseline and the observation are bound to the task's manifest (22-AC-23) ----

func TestClassifyGreen_BaselineBoundToManifest_D22_22AC23(t *testing.T) {
	other := "2222222222222222222222222222222222222222"
	for _, c := range []struct {
		name   string
		mutate func(*GreenInput)
	}{
		{"baseline_head_sha", func(in *GreenInput) { in.Baseline.HeadSha = other }},
		{"baseline_base_sha", func(in *GreenInput) { in.Baseline.BaseSha = other }},
		{"baseline_repository", func(in *GreenInput) { in.Baseline.Repository = "evil/other" }},
		{"observed_base_sha", func(in *GreenInput) { in.Observed.BaseSha = other }},
	} {
		t.Run(c.name, func(t *testing.T) {
			in := g5InputOf(t, g5BaseFixture(t))
			c.mutate(&in)
			got, err := ClassifyGreen(in)
			d22Infra(t, c.name, got, err)
		})
	}
	// Twin: the baseline's identity equals the manifest's.
	in := g5InputOf(t, g5BaseFixture(t))
	if in.Baseline.HeadSha != in.Manifest.TestCommit || in.Baseline.BaseSha != in.Manifest.BaseCommit ||
		in.Baseline.Repository != in.Manifest.Repository || in.Observed.BaseSha != in.Manifest.BaseCommit {
		t.Fatalf("baseline does not match the manifest: %+v", in.Baseline)
	}
	got, err := ClassifyGreen(in)
	g5Check(t, in, got, err, greenExpect{Outcome: "green_pass", Reasons: &[]string{}})
}

// ---- N3: a section 14.6 finding cannot be downgraded to a warning (22-AC-27) ----

func TestClassifyGreen_StaticFindingDowngradedToWarning_D22_22AC27(t *testing.T) {
	for _, code := range []string{"existing_test_modified", "test_file_deleted", "test_dependency_changed", "skip_marker_added"} {
		t.Run(code, func(t *testing.T) {
			fx := g5BaseFixture(t)
			fx.Integrity.StaticFindings = []IntegrityFinding{{Code: code, Path: "pkg/old_test.go", Severity: "warning"}}
			got, err := ClassifyGreen(g5InputOf(t, fx))
			d22Infra(t, code, got, err)
		})
	}
	// Twin: a warning-only code (not a section 14.6 code) is tolerated.
	fx := g5BaseFixture(t)
	fx.Integrity.StaticFindings = []IntegrityFinding{{Code: "test_literal_leakage", Path: "pkg/impl.go", Line: 3, Severity: "warning"}}
	in := g5InputOf(t, fx)
	got, err := ClassifyGreen(in)
	g5Check(t, in, got, err, greenExpect{Outcome: "green_pass", Reasons: &[]string{}})
}
