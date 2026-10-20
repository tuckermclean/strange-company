package verify

import (
	"encoding/json"
	"path/filepath"
	"sort"
	"testing"
)

// Adapter fixtures (plan 22 section 14.1 step 5, section 22 "Adapter fixtures"). Every case directory holds
// `adapter` (the adapter name in policy/verification.yaml), the report (`report.json` or `report.xml`) and
// `expect.json`. See testdata/adapter/README.md for how the reports were produced (D5: hand-written reports in
// the documented formats; re-recording with real tools is 22-AC-12).

type adapterExpect struct {
	Error       bool `json:"error"`
	BuildFailed bool `json:"build_failed"`
	TimedOut    bool `json:"timed_out"`
	Tests       *[]struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	} `json:"tests"`
}

// adapterCases names every directory under testdata/adapter this file iterates.
func adapterCases() []string {
	return []string{
		"go_mixed", "go_panic", "go_stub_panic", "go_build_failed", "go_timeout", "go_neither", "go_not_json", "go_empty",
		"junit_mixed", "junit_not_xml",
		"pytest_assertion", "pytest_importerror", "pytest_nameerror_message_only", "pytest_neither", "pytest_stub",
		"pytest_timeout", "pytest_collection_error",
		"jest_assertion", "jest_referenceerror", "jest_neither", "jest_suite_failed",
		"vitest_mixed", "nextest_mixed", "nextest_compile_error",
	}
}

func reportFile(t testing.TB, dir string) []byte {
	t.Helper()
	for _, n := range []string{"report.json", "report.xml"} {
		if b, err := readIfExists(filepath.Join(dir, n)); err == nil {
			return b
		}
	}
	t.Fatalf("%s: no report file", dir)
	return nil
}

func loadAdapter(t testing.TB, name string) Adapter {
	t.Helper()
	ads, err := LoadAdapters(platformPolicy(t).Verification)
	if err != nil {
		t.Fatalf("LoadAdapters: %v", err)
	}
	a, ok := ads[name]
	if !ok {
		t.Fatalf("LoadAdapters: no adapter %q (have %d)", name, len(ads))
	}
	return a
}

// 22-AC-5 and plan 22 section 14.1 step 5: normalized statuses per adapter; a failure matching neither list is
// failed_error (fail closed); unparsable or empty reports are errors.
func TestNormalizeReport_Fixtures_22AC5(t *testing.T) {
	for _, name := range adapterCases() {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join("testdata", "adapter", name)
			var exp adapterExpect
			readJSONFile(t, &exp, dir, "expect.json")
			adName := string(trimNL(mustRead(t, dir, "adapter")))
			rep, err := NormalizeReport(loadAdapter(t, adName), "unit", reportFile(t, dir))
			if exp.Error {
				if err == nil {
					t.Fatalf("want an error (fail closed), got report %+v", rep)
				}
				return
			}
			if err != nil {
				t.Fatalf("NormalizeReport: %v", err)
			}
			if rep.BuildFailed != exp.BuildFailed {
				t.Errorf("BuildFailed = %v, want %v", rep.BuildFailed, exp.BuildFailed)
			}
			if rep.TimedOut != exp.TimedOut {
				t.Errorf("TimedOut = %v, want %v", rep.TimedOut, exp.TimedOut)
			}
			if exp.Tests == nil {
				return
			}
			got := map[string]string{}
			for _, r := range rep.Tests {
				got[r.ID] = r.Status
			}
			want := map[string]string{}
			for _, e := range *exp.Tests {
				want[e.ID] = e.Status
			}
			if len(got) != len(rep.Tests) {
				t.Errorf("duplicate test ids in %+v", rep.Tests)
			}
			if !sameMap(got, want) {
				t.Errorf("statuses = %s\nwant       %s", jsonOf(got), jsonOf(want))
			}
		})
	}
}

// 22-AC-5: the platform adapters are loaded from policy/verification.yaml, including every list the
// normalization needs.
func TestLoadAdapters_PlatformPolicy_22AC5(t *testing.T) {
	ads, err := LoadAdapters(platformPolicy(t).Verification)
	if err != nil {
		t.Fatalf("LoadAdapters: %v", err)
	}
	var names []string
	for n := range ads {
		names = append(names, n)
	}
	sort.Strings(names)
	want := []string{"cargo-nextest-junit", "go-test-json", "jest-junit", "junit-xml", "pytest-junit", "vitest-junit"}
	if jsonOf(names) != jsonOf(want) {
		t.Fatalf("adapters = %v, want %v", names, want)
	}
	py := ads["pytest-junit"]
	if py.IDTemplate != "{file}::{name}" || py.Format != "junit-xml" || len(py.ErrorTypes) == 0 || len(py.AssertionTypes) == 0 {
		t.Errorf("pytest-junit = %+v", py)
	}
}

// 22-AC-5 / 22-R-3: an unknown adapter or a policy without adapters is an error, never a default.
func TestLoadAdapters_FailsClosed_22AC5(t *testing.T) {
	if _, err := LoadAdapters(map[string]any{"spec": map[string]any{}}); err == nil {
		t.Error("a policy without spec.adapters loaded without error")
	}
	if _, err := NormalizeReport(Adapter{Name: "tap", Format: "tap"}, "unit", []byte("1..1\nok 1\n")); err == nil {
		t.Error("an adapter with an unknown format normalized a report")
	}
}

func jsonOf(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func sameMap(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
