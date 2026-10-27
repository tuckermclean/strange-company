package tofustate_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"strange.company/sc/internal/tofustate"
)

const (
	cluster = "lab-one"
	runID   = "20260304T050607Z-ab12"
	prefix  = cluster + "/conformance/" + runID + "/"
)

// goodProbe behaves like OpenTofu with a lock file it honors: it fails with the state-lock error naming the ID.
func goodProbe(m *memS3) *fakeProbe {
	return &fakeProbe{fn: func(p tofustate.ProbeParams) (tofustate.ProbeResult, error) {
		lock := m.content(p.Key + ".tflock")
		if p.LockID == "" || !strings.Contains(lock, p.LockID) {
			return tofustate.ProbeResult{ExitCode: 0}, nil // the lock was not written before the probe: tofu would plan
		}
		return tofustate.ProbeResult{ExitCode: 1, Stderr: "Error: Error acquiring the state lock\n\nLock Info:\n  ID:        " + p.LockID + "\n  Operation: OperationTypePlan\n"}, nil
	}}
}

func runConf(t *testing.T, m *memS3, probe tofustate.LockProbe, mod ...func(*tofustate.ConformanceParams)) tofustate.Report {
	t.Helper()
	p := tofustate.ConformanceParams{Store: m, Cluster: cluster, RunID: runID, Clock: m.clk, Probe: probe}
	for _, f := range mod {
		f(&p)
	}
	rep, err := tofustate.Conformance(context.Background(), p)
	if err != nil {
		t.Fatalf("Conformance: %v", err)
	}
	return rep
}

func outcomeOf(t *testing.T, rep tofustate.Report, id string) tofustate.TestResult {
	t.Helper()
	for _, r := range rep.Tests {
		if r.ID == id {
			return r
		}
	}
	t.Fatalf("report has no row %s; rows: %+v", id, rep.Tests)
	return tofustate.TestResult{}
}

func hasFatal(rep tofustate.Report, code string) bool {
	for _, f := range rep.Fatal {
		if f == code {
			return true
		}
	}
	return false
}

// 06-AC-1: against a conforming store, C1-C8 all pass, fatal is empty, and the flags are derived.
func TestConformance_AllPass_06AC1(t *testing.T) {
	m := newMemS3()
	probe := goodProbe(m)
	rep := runConf(t, m, probe)
	if len(rep.Fatal) != 0 {
		t.Errorf("Fatal = %v, want empty", rep.Fatal)
	}
	if len(rep.Tests) != 8 {
		t.Fatalf("report has %d rows, want 8 (C1..C8): %+v", len(rep.Tests), rep.Tests)
	}
	for i, r := range rep.Tests {
		if want := "C" + string(rune('1'+i)); r.ID != want {
			t.Errorf("row %d has ID %s, want %s", i, r.ID, want)
		}
		if r.Outcome != "pass" {
			t.Errorf("%s outcome = %q (code %q), want pass", r.ID, r.Outcome, r.Code)
		}
		if r.Name == "" {
			t.Errorf("%s has no name", r.ID)
		}
	}
	if !rep.Flags.IfMatchSupported || rep.Flags.SkipS3Checksum {
		t.Errorf("Flags = %+v, want if_match_supported=true skip_s3_checksum=false", rep.Flags)
	}
	if rep.Prefix != prefix || rep.RunID != runID {
		t.Errorf("Prefix/RunID = %q/%q, want %q/%q", rep.Prefix, rep.RunID, prefix, runID)
	}
	if n := m.attempts[prefix+"k2"]; n != 8 {
		t.Errorf("C3 made %d conditional PUTs on k2, want 8", n)
	}
	if len(probe.calls) != 1 {
		t.Fatalf("C7 probe called %d times, want 1", len(probe.calls))
	}
	if probe.calls[0].Key != prefix+"tofu.tfstate" {
		t.Errorf("C7 probe key = %q, want %q", probe.calls[0].Key, prefix+"tofu.tfstate")
	}
}

// 06-AC-1: everything the suite wrote lives under <cluster>/conformance/<run-id>/ and every version of it is
// deleted afterwards (section 14.3).
func TestConformance_CleansUpEveryVersion_06AC1(t *testing.T) {
	m := newMemS3()
	m.seed(cluster+"/storage/terraform.tfstate", "untouched")
	runConf(t, m, goodProbe(m))
	for _, k := range m.touchedKeys() {
		if !strings.HasPrefix(k, prefix) {
			t.Errorf("the suite wrote %q outside %s", k, prefix)
		}
	}
	if len(m.touchedKeys()) == 0 {
		t.Error("the suite wrote no objects")
	}
	if n := m.totalVersions(prefix); n != 0 {
		t.Errorf("%d versions remain under the conformance prefix, want 0", n)
	}
	if m.content(cluster+"/storage/terraform.tfstate") != "untouched" {
		t.Error("the suite changed an object outside its prefix")
	}
}

// 06-AC-1: the hand-written lock of C7 exists, with the lock ID, before tofu is asked to plan.
func TestConformance_C7WritesLockBeforeProbe_06AC1(t *testing.T) {
	m := newMemS3()
	var seen string
	probe := &fakeProbe{fn: func(p tofustate.ProbeParams) (tofustate.ProbeResult, error) {
		seen = m.content(p.Key + ".tflock")
		return goodProbe(m).fn(p)
	}}
	runConf(t, m, probe)
	if len(probe.calls) != 1 {
		t.Fatalf("probe calls = %d", len(probe.calls))
	}
	id := probe.calls[0].LockID
	var lock struct{ ID string }
	if id == "" || json.Unmarshal([]byte(seen), &lock) != nil || lock.ID != id {
		t.Errorf("lock object at probe time = %q, want JSON with ID %q", seen, id)
	}
}

// 06-AC-1 / 06-AC-2: C1 failure code.
func TestConformance_C1ConditionalPutUnsupported_06AC1(t *testing.T) {
	m := newMemS3()
	m.NoConditional = true
	rep := runConf(t, m, goodProbe(m))
	if len(rep.Fatal) == 0 || rep.Fatal[0] != "S3_CONDITIONAL_PUT_UNSUPPORTED" {
		t.Errorf("Fatal = %v, want S3_CONDITIONAL_PUT_UNSUPPORTED first", rep.Fatal)
	}
	if r := outcomeOf(t, rep, "C1"); r.Outcome != "fail" || r.Code != "S3_CONDITIONAL_PUT_UNSUPPORTED" {
		t.Errorf("C1 = %+v", r)
	}
}

// 06-AC-2 (library): a store that ignores If-None-Match fails C2 and C3 with one fatal PF_S3_LOCK_UNSUPPORTED,
// and the report is still produced and cleaned up.
func TestConformance_IgnoredIfNoneMatch_06AC2(t *testing.T) {
	m := newMemS3()
	m.IgnoreIfNoneMatch = true
	rep := runConf(t, m, goodProbe(m))
	n := 0
	for _, f := range rep.Fatal {
		if f == "PF_S3_LOCK_UNSUPPORTED" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("Fatal = %v, want PF_S3_LOCK_UNSUPPORTED exactly once", rep.Fatal)
	}
	if r := outcomeOf(t, rep, "C2"); r.Outcome != "fail" || r.Code != "PF_S3_LOCK_UNSUPPORTED" {
		t.Errorf("C2 = %+v", r)
	}
	if r := outcomeOf(t, rep, "C3"); r.Outcome != "fail" || r.Code != "PF_S3_LOCK_UNSUPPORTED" {
		t.Errorf("C3 = %+v", r)
	}
	if k := m.totalVersions(prefix); k != 0 {
		t.Errorf("%d versions remain under the conformance prefix after a failed run", k)
	}
}

// 06-AC-1: 409 is accepted only when the provider documents it.
func TestConformance_409OnlyWhenAccepted_06AC1(t *testing.T) {
	m := newMemS3()
	m.Status409 = true
	if rep := runConf(t, m, goodProbe(m)); !hasFatal(rep, "PF_S3_LOCK_UNSUPPORTED") {
		t.Errorf("a 409 without Accept409: Fatal = %v, want PF_S3_LOCK_UNSUPPORTED", rep.Fatal)
	}
	m2 := newMemS3()
	m2.Status409 = true
	rep := runConf(t, m2, goodProbe(m2), func(p *tofustate.ConformanceParams) { p.Accept409 = true })
	if len(rep.Fatal) != 0 {
		t.Errorf("a documented 409: Fatal = %v, want empty", rep.Fatal)
	}
}

// C4.
func TestConformance_VersioningUnsupported_06AC1(t *testing.T) {
	m := newMemS3()
	m.NoVersioning = true
	rep := runConf(t, m, goodProbe(m))
	if !hasFatal(rep, "S3_VERSIONING_UNSUPPORTED") {
		t.Errorf("Fatal = %v, want S3_VERSIONING_UNSUPPORTED", rep.Fatal)
	}
	if r := outcomeOf(t, rep, "C4"); r.Outcome != "fail" {
		t.Errorf("C4 = %+v", r)
	}
}

// C5: a store without If-Match is not fatal; the flag records it and the row is a warning.
func TestConformance_IfMatchUnsupportedIsNotFatal_06AC1(t *testing.T) {
	m := newMemS3()
	m.IgnoreIfMatch = true
	rep := runConf(t, m, goodProbe(m))
	if len(rep.Fatal) != 0 {
		t.Errorf("Fatal = %v, want empty", rep.Fatal)
	}
	if rep.Flags.IfMatchSupported {
		t.Error("Flags.IfMatchSupported = true for a store that ignores If-Match")
	}
	if r := outcomeOf(t, rep, "C5"); r.Outcome != "warn" {
		t.Errorf("C5 = %+v, want outcome warn", r)
	}
}

// C6.
func TestConformance_ChecksumFlag_06AC1(t *testing.T) {
	m := newMemS3()
	m.RejectChecksum = true
	rep := runConf(t, m, goodProbe(m))
	if len(rep.Fatal) != 0 {
		t.Errorf("Fatal = %v, want empty (plain PUTs work)", rep.Fatal)
	}
	if !rep.Flags.SkipS3Checksum {
		t.Error("Flags.SkipS3Checksum = false for a store that rejects checksummed PUTs")
	}
	if r := outcomeOf(t, rep, "C6"); r.Outcome != "pass" {
		t.Errorf("C6 = %+v, want pass with the flag set", r)
	}
}

func TestConformance_ChecksumIncompatible_06AC1(t *testing.T) {
	m := newMemS3()
	m.RejectChecksum = true
	m.FailPlainAfterReject = true
	rep := runConf(t, m, goodProbe(m))
	if !hasFatal(rep, "S3_CHECKSUM_INCOMPATIBLE") {
		t.Errorf("Fatal = %v, want S3_CHECKSUM_INCOMPATIBLE when both variants fail", rep.Fatal)
	}
}

// C7.
func TestConformance_C7(t *testing.T) {
	t.Run("lock ignored, tofu exits 0 (06-AC-1)", func(t *testing.T) {
		m := newMemS3()
		rep := runConf(t, m, &fakeProbe{fn: func(tofustate.ProbeParams) (tofustate.ProbeResult, error) {
			return tofustate.ProbeResult{ExitCode: 0}, nil
		}})
		if !hasFatal(rep, "TOFU_LOCKFILE_NOT_HONORED") {
			t.Errorf("Fatal = %v, want TOFU_LOCKFILE_NOT_HONORED", rep.Fatal)
		}
	})
	t.Run("fails for another reason (06-AC-1)", func(t *testing.T) {
		m := newMemS3()
		rep := runConf(t, m, &fakeProbe{fn: func(tofustate.ProbeParams) (tofustate.ProbeResult, error) {
			return tofustate.ProbeResult{ExitCode: 1, Stderr: "Error: Failed to load state: AccessDenied\n"}, nil
		}})
		if !hasFatal(rep, "TOFU_LOCKFILE_NOT_HONORED") {
			t.Errorf("Fatal = %v, want TOFU_LOCKFILE_NOT_HONORED when stderr does not name the lock ID", rep.Fatal)
		}
	})
	t.Run("no probe skips C7 without a fatal (06-AC-1)", func(t *testing.T) {
		m := newMemS3()
		rep := runConf(t, m, nil)
		if len(rep.Fatal) != 0 {
			t.Errorf("Fatal = %v, want empty", rep.Fatal)
		}
		if r := outcomeOf(t, rep, "C7"); r.Outcome != "skip" {
			t.Errorf("C7 = %+v, want outcome skip", r)
		}
	})
}

// C8: skew beyond 60 seconds in either direction is fatal, at or under 60 is fine.
func TestConformance_C8ClockSkew_06AC1(t *testing.T) {
	for _, tc := range []struct {
		skew  time.Duration
		fatal bool
	}{{61 * time.Second, true}, {-61 * time.Second, true}, {60 * time.Second, false}, {-59 * time.Second, false}, {0, false}} {
		m := newMemS3()
		m.Skew = tc.skew
		rep := runConf(t, m, goodProbe(m))
		if got := hasFatal(rep, "S3_CLOCK_SKEW"); got != tc.fatal {
			t.Errorf("skew %v: S3_CLOCK_SKEW fatal = %v, want %v (Fatal %v)", tc.skew, got, tc.fatal, rep.Fatal)
		}
	}
}

func TestConformance_RejectsMissingStore_06AC1(t *testing.T) {
	_, err := tofustate.Conformance(context.Background(), tofustate.ConformanceParams{Cluster: cluster, RunID: runID})
	if err == nil {
		t.Error("Conformance with a nil Store returned no error")
	}
}

// 06-AC-1: s3-conformance.json has fatal as an array (never null), one row per test with latency, and the flags.
func TestReport_JSONShape_06AC1(t *testing.T) {
	m := newMemS3()
	rep := runConf(t, m, goodProbe(m))
	raw, err := rep.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("Marshal output is not a JSON object: %v\n%s", err, raw)
	}
	if string(doc["fatal"]) != "[]" {
		t.Errorf("fatal = %s, want []", doc["fatal"])
	}
	var rows []map[string]any
	if err := json.Unmarshal(doc["tests"], &rows); err != nil || len(rows) != 8 {
		t.Fatalf("tests = %s (%v), want 8 rows", doc["tests"], err)
	}
	for _, r := range rows {
		for _, k := range []string{"id", "name", "outcome", "latency_ms"} {
			if _, ok := r[k]; !ok {
				t.Errorf("row %v lacks %q", r, k)
			}
		}
	}
	var flags map[string]bool
	if err := json.Unmarshal(doc["flags"], &flags); err != nil || !flags["if_match_supported"] {
		t.Errorf("flags = %s (%v)", doc["flags"], err)
	}
	empty, err := tofustate.Report{}.Marshal()
	if err != nil || !strings.Contains(string(empty), `"fatal":[]`) && !strings.Contains(string(empty), `"fatal": []`) {
		t.Errorf("a zero Report marshals to %s (%v); fatal must still be []", empty, err)
	}
}

func TestWriteReport_CreatesParentsAndWritesTheReport_06AC1(t *testing.T) {
	m := newMemS3()
	rep := runConf(t, m, goodProbe(m))
	path := filepath.Join(t.TempDir(), ".bootstrap", "runs", runID, "s3-conformance.json")
	if err := tofustate.WriteReport(path, rep); err != nil {
		t.Fatalf("WriteReport: %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("report file: %v", err)
	}
	want, _ := rep.Marshal()
	var a, c any
	if json.Unmarshal(b, &a) != nil || json.Unmarshal(want, &c) != nil {
		t.Fatalf("file %q or Marshal output %q is not JSON", b, want)
	}
	ab, _ := json.Marshal(a)
	cb, _ := json.Marshal(c)
	if string(ab) != string(cb) || len(b) == 0 {
		t.Errorf("file content differs from Marshal output:\nfile: %s\nwant: %s", b, want)
	}
}
