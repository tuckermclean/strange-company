package main

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

// F06 S7, 22-AC-29 (observer hardening, blind TDD-B1) and the run/emit contract it rests on (plan 22 sections 8
// and 14.1, plan 20 sections 8.4 and 8.5).

func fxSmokeRepo(t *testing.T) *fxRepo {
	t.Helper()
	return fxNewRepo(t, fxConfig{Fixture: "smoke", Suites: []fxSuite{fxGoSuite(nil)}})
}

// 22-AC-29: a RED run observes base and head with real `go test` and emit frames exactly that observation.
// This is the twin of every hardening case below: without it, a run that observes nothing would pass them.
func TestRunEmit_RedObservation_22AC29(t *testing.T) {
	r := fxSmokeRepo(t)
	res := r.run(fxRunOpts{Baseline: "run"})
	if res.Exit != 0 || res.TimedOut {
		t.Fatalf("run exit %d (timed out %v); stderr %q", res.Exit, res.TimedOut, fxTrunc(res.Stderr))
	}
	for _, p := range []string{"observed.json", "out/unit-base.log", "out/unit-head.log"} {
		if !fxExists(filepath.Join(r.record, p)) {
			t.Errorf("run left no %s in the record directory", p)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(r.record, "out", "unit-head.log")); !strings.Contains(string(b), `"Action":"fail"`) {
		t.Errorf("out/unit-head.log is not the suite's output: %q", fxTrunc(string(b)))
	}
	if strings.Contains(res.Stdout+res.Stderr, "::sc-evidence-") {
		t.Errorf("run printed an evidence marker; only emit frames evidence")
	}
	doc, _ := r.emitDoc(fxPhaseRed)
	o := doc.Observed
	bin, _ := os.ReadFile(fxBinary(t))
	if o.TaskID != fxTask || o.Repository != fxRepoName || o.Subject != "tests" || o.BaseSha != r.base || o.HeadSha != r.head {
		t.Errorf("identity = %q %q %q %q %q", o.TaskID, o.Repository, o.Subject, o.BaseSha, o.HeadSha)
	}
	if o.IntentID != r.intent {
		t.Errorf("intent_id = %q, want %q (set by emit from SC_INTENT_ID)", o.IntentID, r.intent)
	}
	if o.ValidationBlob != r.blob {
		t.Errorf("validation_blob = %q, want the blob of .strange-company/validation.yaml %q", o.ValidationBlob, r.blob)
	}
	if o.ToolchainImage != "registry.example.org/ci@sha256:"+strings.Repeat("a", 64) {
		t.Errorf("toolchain_image = %q", o.ToolchainImage)
	}
	if o.Verifier.Version == "" || o.Verifier.BinarySha256 != "sha256:"+fxSHA256(bin) {
		t.Errorf("verifier = %+v, want a version and the sha256 of the sc-verify binary", o.Verifier)
	}
	base, ok1 := doc.run("base", "unit")
	head, ok2 := doc.run("head", "unit")
	if !ok1 || !ok2 || len(o.Runs) != 2 {
		t.Fatalf("runs = %+v, want one base and one head run of suite unit", o.Runs)
	}
	if base.Commit != r.base || head.Commit != r.head || base.Adapter != "go-test-json" || !base.ReportFound || !head.ReportFound ||
		base.BuildFailed || head.BuildFailed || base.TimedOut || head.TimedOut || base.ExitCode != 0 || head.ExitCode != 1 {
		t.Errorf("base run %+v, head run %+v", base, head)
	}
	if base.Counts != (fxCounts{Total: 1, Passed: 1}) || head.Counts != (fxCounts{Total: 3, Passed: 2, FailedAssertion: 1}) {
		t.Errorf("counts base %+v head %+v", base.Counts, head.Counts)
	}
	if x, ok := doc.test("unit::fx.TestImplReturns42"); !ok || x.StatusBase != "absent" || x.StatusHead != "failed_assertion" {
		t.Errorf("TestImplReturns42 = %+v (%v)", x, ok)
	}
	if x, ok := doc.test("unit::fx.TestImplIsInt"); !ok || x.StatusBase != "absent" || x.StatusHead != "passed" {
		t.Errorf("TestImplIsInt = %+v (%v)", x, ok)
	}
	if _, ok := doc.test("unit::fx.TestBaseline"); ok {
		t.Errorf("TestBaseline passes in both trees and must not be listed")
	}
	if o.Truncated {
		t.Errorf("tests_truncated is true")
	}
	if got := fxDecodeIDSet(t, o.TestIDSets["base_passing"]); !reflect.DeepEqual(got, []string{"unit::fx.TestBaseline"}) {
		t.Errorf("base_passing = %v", got)
	}
	want := []string{"unit::fx.TestBaseline", "unit::fx.TestImplIsInt", "unit::fx.TestImplReturns42"}
	if got := fxDecodeIDSet(t, o.TestIDSets["head_present"]); !reflect.DeepEqual(got, want) {
		t.Errorf("head_present = %v, want %v", got, want)
	}
}

// 22-AC-29, plan 22 section 14.1 step 1: a head that is not SC_EXPECTED_SHA, or a missing or malformed intent
// variable, makes run exit 2 without an observation and without running any suite.
func TestRun_RefusesWrongHeadAndBadIntentVariables_22AC29(t *testing.T) {
	r := fxNewRepo(t, fxConfig{Fixture: "env", Shared: []string{"_envdump"}, Suites: []fxSuite{fxGoSuite(nil)}})
	cases := []struct {
		name string
		o    fxRunOpts
	}{
		{"expected_sha_is_not_head", fxRunOpts{Expected: r.base}},
		{"expected_sha_malformed", fxRunOpts{Expected: "HEAD"}},
		{"phase_missing", fxRunOpts{Unset: []string{"SC_PHASE"}}},
		{"phase_unknown", fxRunOpts{Unset: []string{"SC_PHASE"}, Extra: []string{"SC_PHASE=blue"}}},
		{"base_sha_missing", fxRunOpts{Unset: []string{"SC_BASE_SHA"}}},
		{"task_missing", fxRunOpts{Unset: []string{"SC_TASK_ID"}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_ = os.RemoveAll(r.out)
			_ = os.MkdirAll(r.out, 0o755)
			res := r.run(c.o)
			if res.Exit != 2 {
				t.Errorf("exit %d, want 2 (could not observe); stderr %q", res.Exit, fxTrunc(res.Stderr))
			}
			if fxExists(filepath.Join(r.record, "observed.json")) {
				t.Errorf("run wrote observed.json although it could not observe")
			}
			if fxExists(filepath.Join(r.out, "env.txt")) {
				t.Errorf("a suite ran although the identity checks failed")
			}
			if strings.Contains(res.Stdout+res.Stderr, "::sc-evidence-") {
				t.Errorf("an evidence marker was printed")
			}
		})
	}
}

// ---------------------------------------------------------------------------------------------------------------
// 22-AC-29 (a): a production init() spawns a detached process that rewrites the report after the suite exits.

func fxRewriteRepo(t *testing.T, mode, mark string) *fxRepo {
	t.Helper()
	return fxNewRepo(t, fxConfig{Fixture: "rewrite", Suites: []fxSuite{fxJUnitSuite(map[string]string{"FX_MODE": mode, "FX_MARK": mark})}})
}

func fxOriginalXML(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "observer", "rewrite", "base", "original.xml.fx"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// fxCheckObservedOriginal asserts that the observation reflects the original report and nothing the descendant
// wrote.
func fxCheckObservedOriginal(t *testing.T, doc fxDoc) {
	t.Helper()
	head, ok := doc.run("head", "unit")
	if !ok || !head.ReportFound || head.Adapter != "junit-xml" || head.Counts != (fxCounts{Total: 2, Passed: 1, FailedAssertion: 1}) {
		t.Errorf("head run = %+v (%v), want the 2 tests of the original report (1 passed, 1 failed_assertion)", head, ok)
	}
	if x, ok := doc.test("unit::fx.TestOriginalFail"); !ok || x.StatusHead != "failed_assertion" {
		t.Errorf("TestOriginalFail = %+v (%v), want failed_assertion as in the original report", x, ok)
	}
	for _, x := range doc.Observed.Tests {
		if strings.Contains(x.ID, "Rewritten") || strings.Contains(x.ID, "Outside") {
			t.Errorf("the observation holds %q, which only a rewritten or foreign report has", x.ID)
		}
	}
}

func TestRun_SealsReportBeforeParsing_22AC29(t *testing.T) {
	r := fxRewriteRepo(t, "", "FXDETACH_none")
	if res := r.run(fxRunOpts{}); res.Exit != 0 {
		t.Fatalf("run exit %d; stderr %q", res.Exit, fxTrunc(res.Stderr))
	}
	orig := fxOriginalXML(t)
	var sealed []string
	for _, f := range fxRegularFiles(filepath.Join(r.record, "sealed")) {
		if b, _ := os.ReadFile(f); fxSHA256(b) == fxSHA256(orig) {
			sealed = append(sealed, f)
		}
	}
	if len(sealed) != 1 {
		t.Fatalf("the record holds %d sealed copies of the report, want exactly 1 (files: %v)", len(sealed), fxRegularFiles(r.record))
	}
	if fi, err := os.Lstat(sealed[0]); err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm()&0o222 != 0 {
		t.Errorf("sealed copy %s is %v (%v), want a regular read-only file", sealed[0], fi.Mode(), err)
	}
	if rel, _ := filepath.Rel(r.ws, r.record); !strings.HasPrefix(rel, "..") {
		t.Errorf("the record directory %s is inside the workspace", r.record)
	}
	doc, _ := r.emitDoc(fxPhaseRed)
	fxCheckObservedOriginal(t, doc)
}

func TestRun_KillsDetachedDescendantsBeforeSealing_22AC29(t *testing.T) {
	for _, mode := range []string{"setsid", "setpgid", "double_fork", "sleeper"} {
		t.Run(mode, func(t *testing.T) {
			mark := "FXDETACH_" + mode + "_" + fxRandHex(t, 4)
			r := fxRewriteRepo(t, mode, mark)
			t.Cleanup(func() { fxKillAll(mark) })
			res := r.run(fxRunOpts{})
			if res.Exit != 0 || res.TimedOut {
				t.Fatalf("run exit %d (timed out %v); stderr %q", res.Exit, res.TimedOut, fxTrunc(res.Stderr))
			}
			if !fxExists(filepath.Join(r.out, "spawned-"+mark)) {
				t.Fatalf("the production init() did not spawn its detached process; the case proves nothing")
			}
			if pids := fxProcsWith(mark); len(pids) != 0 {
				t.Errorf("descendants of the suite survive run: pids %v", pids)
			}
			// The rewriter wakes 0.8 s after the suite's go command is gone; look well past that.
			fxSleep(1700 * time.Millisecond)
			for _, f := range []string{"awake-" + mark, "finished-" + mark} {
				if fxExists(filepath.Join(r.out, f)) {
					t.Errorf("the detached process outlived the suite and ran on (%s)", f)
				}
			}
			if pids := fxProcsWith(mark); len(pids) != 0 {
				t.Errorf("descendants of the suite are running after run exited: pids %v", pids)
			}
			orig := fxOriginalXML(t)
			if live, _ := os.ReadFile(filepath.Join(r.ws, "out", "junit.xml")); string(live) != string(orig) {
				t.Errorf("the live report was rewritten after the suite exited: %q", fxTrunc(string(live)))
			}
			if hits := fxFilesWith([]string{r.record}, "REWRITTEN_BY_DESCENDANT"); len(hits) != 0 {
				t.Errorf("the record directory was rewritten by the descendant: %v", hits)
			}
			var sealedOK bool
			for _, f := range fxRegularFiles(filepath.Join(r.record, "sealed")) {
				if b, _ := os.ReadFile(f); fxSHA256(b) == fxSHA256(orig) {
					sealedOK = true
				}
			}
			if !sealedOK {
				t.Errorf("no sealed copy equals the original report; files %v", fxRegularFiles(r.record))
			}
			doc, _ := r.emitDoc(fxPhaseRed)
			fxCheckObservedOriginal(t, doc)
		})
	}
}

// 22-AC-29 (a), symlinked report path: the suite leaves a symlink to a file outside the workspace as its report.
// The observation must not be built from that file (fail closed: no report, or no observation at all).
func TestRun_ReportSymlinkOutsideWorkspaceIsNotFollowed_22AC29(t *testing.T) {
	r := fxRewriteRepo(t, "symlink", "FXDETACH_symlink")
	res := r.run(fxRunOpts{})
	if res.Exit == 2 {
		return // could not observe: acceptable, nothing was claimed
	}
	if res.Exit != 0 {
		t.Fatalf("run exit %d; stderr %q", res.Exit, fxTrunc(res.Stderr))
	}
	if !fxExists(filepath.Join(r.out, "outside.xml")) {
		t.Fatalf("the fixture did not create its outside report")
	}
	doc, _ := r.emitDoc(fxPhaseRed)
	head, ok := doc.run("head", "unit")
	if !ok || head.ReportFound {
		t.Errorf("head run = %+v (%v); a report that is a symlink out of the workspace must count as not found", head, ok)
	}
	for _, x := range doc.Observed.Tests {
		if strings.Contains(x.ID, "Outside") || strings.Contains(x.ID, "Original") {
			t.Errorf("the observation holds %q read through the symlink", x.ID)
		}
	}
	if hits := fxFilesWith([]string{filepath.Join(r.record, "sealed")}, "TestOutside"); len(hits) != 0 {
		t.Errorf("the sealed directory holds a copy of the file the symlink points to: %v", hits)
	}
}

// 22-AC-29 (a), planted links: the suite (same uid) replaces the record's sealed directory and observed.json by
// symlinks to places it controls. run must not write through them: it refuses (exit 2) or replaces them.
func TestRun_PlantedRecordSymlinksAreNotFollowed_22AC29(t *testing.T) {
	r := fxRewriteRepo(t, "plant", "FXDETACH_plant")
	evil := filepath.Join(r.out, "evil-sealed")
	victim := filepath.Join(r.out, "victim.txt")
	if err := os.MkdirAll(evil, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(victim, []byte("VICTIM"), 0o644); err != nil {
		t.Fatal(err)
	}
	res := r.run(fxRunOpts{})
	if b, _ := os.ReadFile(victim); string(b) != "VICTIM" {
		t.Errorf("run wrote through a planted observed.json symlink: victim = %q", fxTrunc(string(b)))
	}
	if ents, _ := os.ReadDir(evil); len(ents) != 0 {
		t.Errorf("run wrote sealed copies into the directory the suite pointed it to: %d entries", len(ents))
	}
	if res.Exit == 2 {
		return
	}
	if res.Exit != 0 {
		t.Fatalf("run exit %d; stderr %q", res.Exit, fxTrunc(res.Stderr))
	}
	for _, p := range []string{"sealed", "observed.json"} {
		fi, err := os.Lstat(filepath.Join(r.record, p))
		if err != nil || fi.Mode()&os.ModeSymlink != 0 {
			t.Errorf("record/%s = %v (%v); it must be the real directory or file run created", p, fi, err)
		}
	}
	doc, _ := r.emitDoc(fxPhaseRed)
	fxCheckObservedOriginal(t, doc)
}

// ---------------------------------------------------------------------------------------------------------------
// 22-AC-29 (b): code under test reads /proc/<ppid>/environ (and every other process's environment).

func fxSpyRepo(t *testing.T) *fxRepo {
	t.Helper()
	return fxNewRepo(t, fxConfig{Fixture: "spy", Suites: []fxSuite{fxGoSuite(nil)}})
}

// fxSpyIsRun reports whether an ANCESTOR line of spy.txt is the sc-verify run process: told by its cmdline field
// (unlike exe, readable when the process is not dumpable; unlike comm, not "exe" after a re-execution).
func fxSpyIsRun(l string) bool {
	return strings.Contains(l, `\x00run\x00`) && strings.Contains(l, `\x00--validation\x00`) && strings.Contains(l, `\x00--record\x00`)
}

// fxSpyLines returns the ANCESTOR lines of spy.txt whose process is the sc-verify run process.
func fxSpyLines(t *testing.T, r *fxRepo) (all string, svLines []string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(r.out, "spy.txt"))
	if err != nil {
		t.Fatalf("the spy test recorded nothing: %v", err)
	}
	// An unreadable environ of the run process is as good as an empty one (D28 B2), and its exe link may be
	// unreadable too: the run process is told by its command line.
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, "ANCESTOR ") && fxSpyIsRun(l) {
			svLines = append(svLines, l)
		}
	}
	return string(b), svLines
}

func TestRun_ProcEnvironOfRunHoldsNoNonce_22AC29(t *testing.T) {
	r := fxSpyRepo(t)
	if res := r.run(fxRunOpts{}); res.Exit != 0 {
		t.Fatalf("run exit %d; stderr %q", res.Exit, fxTrunc(res.Stderr))
	}
	all, sv := fxSpyLines(t, r)
	if len(sv) == 0 {
		t.Fatalf("the spy found no sc-verify process among its ancestors:\n%s", fxTrunc(all))
	}
	for _, l := range sv {
		if strings.Contains(l, "SC_EVIDENCE_NONCE") || strings.Contains(l, "SC_INTENT_ID") {
			t.Errorf("an ancestor sc-verify process shows the nonce or intent variable: %s", fxTrunc(l))
		}
	}
}

// 22-AC-29 (b), hostile launch: even when the step that runs `sc-verify run` is given the nonce and the intent
// id, no process the suite can read shows them (run drops them and re-executes, or refuses to run).
func TestRun_NonceInLaunchEnvironmentNeverReachesTheSuite_22AC29(t *testing.T) {
	r := fxSpyRepo(t)
	res := r.run(fxRunOpts{Hostile: true})
	roots := []string{r.record, r.ws, r.home, r.tmp}
	for _, secret := range []string{r.nonce, r.intent} {
		if hits := fxFilesWith(roots, secret); len(hits) != 0 {
			t.Errorf("a file holds %q: %v", secret, hits)
		}
		if strings.Contains(res.Stdout+res.Stderr, secret) {
			t.Errorf("run printed %q", secret)
		}
	}
	if res.Exit == 2 {
		if fxExists(filepath.Join(r.out, "spy.txt")) {
			t.Errorf("run refused (exit 2) but a suite ran")
		}
		return
	}
	if res.Exit != 0 {
		t.Fatalf("run exit %d; stderr %q", res.Exit, fxTrunc(res.Stderr))
	}
	all, sv := fxSpyLines(t, r)
	if len(sv) == 0 {
		t.Fatalf("the spy found no sc-verify process among its ancestors:\n%s", fxTrunc(all))
	}
	for _, secret := range []string{r.nonce, r.intent, "SC_EVIDENCE_NONCE=", "SC_INTENT_ID="} {
		if strings.Contains(all, secret) {
			t.Errorf("code under test could read %q from /proc", secret)
		}
	}
}

// ---------------------------------------------------------------------------------------------------------------
// 22-AC-29 (c): a test binary prints fake `--- PASS: TestProtected` lines.
//
// Measured on the ci image's go1.27.2 (plans/compatibility-baseline.yaml "Go toolchain"): test2json attributes
// such lines printed from inside a running test to that test's output, and a fake line printed before any test
// runs (TestMain before m.Run) yields a `pass` event that the real `fail` event of TestProtected then follows.
// The status of TestProtected stays failed_assertion. The fake line for a test that does not exist (TestGhost)
// does yield a `pass` event; that is the SI-07 residual and is deliberately not asserted here.

func TestRunEmit_ForgedPassLinesLeaveProtectedStatusUnchanged_22AC29(t *testing.T) {
	var statuses [2]fxTest
	for i, forge := range []string{"0", "1"} {
		r := fxNewRepo(t, fxConfig{Fixture: "forge", Suites: []fxSuite{fxGoSuite(map[string]string{"FX_FORGE": forge})}})
		if res := r.run(fxRunOpts{}); res.Exit != 0 {
			t.Fatalf("forge=%s: run exit %d; stderr %q", forge, res.Exit, fxTrunc(res.Stderr))
		}
		doc, _ := r.emitDoc(fxPhaseRed)
		head, ok := doc.run("head", "unit")
		if !ok || !head.ReportFound || head.ExitCode != 1 {
			t.Errorf("forge=%s: head run %+v (%v)", forge, head, ok)
		}
		x, ok := doc.test("unit::fx.TestProtected")
		if !ok || x.StatusHead != "failed_assertion" {
			t.Errorf("forge=%s: TestProtected = %+v (%v), want failed_assertion", forge, x, ok)
		}
		for _, id := range []string{"unit::fx.TestForger", "unit::fx.TestZAfter"} {
			if y, ok := doc.test(id); !ok || y.StatusHead != "passed" {
				t.Errorf("forge=%s: %s = %+v (%v), want passed", forge, id, y, ok)
			}
		}
		statuses[i] = x
	}
	if statuses[0] != statuses[1] {
		t.Errorf("TestProtected differs with and without the forged lines: %+v vs %+v", statuses[0], statuses[1])
	}
}

// ---------------------------------------------------------------------------------------------------------------
// emit: the nonce only here; the sealed observation is verified before it is framed.

func TestEmit_RequiresWellFormedNonceAndIntent_22AC29(t *testing.T) {
	r := fxSmokeRepo(t)
	if res := r.run(fxRunOpts{}); res.Exit != 0 {
		t.Fatalf("run exit %d; stderr %q", res.Exit, fxTrunc(res.Stderr))
	}
	cases := []struct{ name, nonce, intent string }{
		{"nonce_missing", "", r.intent},
		{"intent_missing", r.nonce, ""},
		{"nonce_short", r.nonce[:31], r.intent},
		{"nonce_uppercase", strings.ToUpper(r.nonce), r.intent},
		{"nonce_not_hex", strings.Repeat("g", 32), r.intent},
		{"intent_with_space", r.nonce, "vi x"},
		{"intent_with_newline", r.nonce, "vi\n::sc-evidence-end v1 intent=vi::"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res := r.emit(c.nonce, c.intent)
			if res.Exit == 0 || strings.Contains(res.Stdout, "::sc-evidence-") {
				t.Errorf("exit %d, stdout %q; emit must print no block without a well-formed nonce and intent id", res.Exit, fxTrunc(res.Stdout))
			}
		})
	}
	doc, blk := r.emitDoc(fxPhaseRed)
	if doc.Observed.IntentID != r.intent || blk.Nonce != r.nonce {
		t.Errorf("twin: emit with a well-formed nonce and intent printed intent %q nonce %q", doc.Observed.IntentID, blk.Nonce)
	}
	if hits := fxFilesWith([]string{r.record, r.ws}, r.nonce); len(hits) != 0 {
		t.Errorf("emit stored the nonce in a file: %v", hits)
	}
}

func TestEmit_RefusesTamperedRecord_22AC29(t *testing.T) {
	r := fxNewRepo(t, fxConfig{Fixture: "rewrite", Suites: []fxSuite{fxJUnitSuite(map[string]string{"FX_MODE": "", "FX_MARK": "FXDETACH_t"})}})
	if res := r.run(fxRunOpts{}); res.Exit != 0 {
		t.Fatalf("run exit %d; stderr %q", res.Exit, fxTrunc(res.Stderr))
	}
	targets := []string{filepath.Join(r.record, "observed.json")}
	targets = append(targets, fxRegularFiles(filepath.Join(r.record, "sealed"))...)
	if len(targets) < 2 {
		t.Fatalf("the record holds no sealed copy: %v", fxRegularFiles(r.record))
	}
	sort.Strings(targets)
	for _, f := range targets {
		orig, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		mode, dirMode := fxMode(t, f), fxMode(t, filepath.Dir(f))
		for _, how := range []string{"append", "truncate", "delete"} {
			_ = os.Chmod(filepath.Dir(f), 0o755)
			_ = os.Chmod(f, 0o644)
			var err error
			switch how {
			case "append":
				err = os.WriteFile(f, append(append([]byte(nil), orig...), '\n', ' '), 0o644)
			case "truncate":
				err = os.WriteFile(f, orig[:len(orig)/2], 0o644)
			case "delete":
				err = os.Remove(f)
			}
			if err != nil {
				t.Fatal(err)
			}
			res := r.emit(r.nonce, r.intent)
			if res.Exit == 0 || strings.Contains(res.Stdout, "::sc-evidence-") {
				t.Errorf("%s %s: exit %d, stdout %q; emit must refuse a record whose files changed since run", how, filepath.Base(f), res.Exit, fxTrunc(res.Stdout))
			}
			if err := os.WriteFile(f, orig, 0o644); err != nil {
				t.Fatal(err)
			}
			_ = os.Chmod(f, mode)
			_ = os.Chmod(filepath.Dir(f), dirMode)
		}
	}
	doc, _ := r.emitDoc(fxPhaseRed)
	fxCheckObservedOriginal(t, doc)
}

func fxMode(t *testing.T, f string) os.FileMode {
	t.Helper()
	fi, err := os.Stat(f)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm()
}

// ---------------------------------------------------------------------------------------------------------------
// Usage errors fail closed (plan 22 section 7.1: exit 2 = could not observe).

func TestCLI_UsageErrorsExitTwoWithoutEvidence_22AC29(t *testing.T) {
	bin := fxBinary(t)
	env := []string{"PATH=/usr/bin:/bin", "HOME=" + t.TempDir()}
	for _, args := range [][]string{
		nil,
		{"frobnicate"},
		{"run"},
		{"emit"},
		{"classify"},
		{"classify", "--policy", "/nonexistent/p.yaml", "--doc", "/nonexistent/d.json"},
		{"run", "--no-such-flag"},
	} {
		res := fxExec(t, bin, t.TempDir(), env, args)
		if res.Exit != 2 || strings.Contains(res.Stdout, "::sc-evidence-") || strings.Contains(res.Stdout+res.Stderr, "goroutine ") {
			t.Errorf("sc-verify %v: exit %d, stdout %q, stderr %q; want exit 2 and no evidence", args, res.Exit, fxTrunc(res.Stdout), fxTrunc(res.Stderr))
		}
	}
	res := fxExec(t, bin, t.TempDir(), env, []string{"classify", "--policy", "/nonexistent/p.yaml", "--doc", "/nonexistent/d.json", "--facts", "/nonexistent/f.json"})
	if res.Exit == 0 || strings.Contains(res.Stdout+res.Stderr, "goroutine ") {
		t.Errorf("classify of missing files: exit %d, stderr %q; want a non-zero exit without a panic", res.Exit, fxTrunc(res.Stderr))
	}
}
