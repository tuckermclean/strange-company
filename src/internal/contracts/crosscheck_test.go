package contracts_test

import (
	"regexp"
	"strings"
	"testing"
)

var crosscheckPairs = regexp.MustCompile(`\b([1-9][0-9]*) pairs?\b`)

// 01-AC-22: on the real tree `make contracts` runs the Python crosscheck, which compares a non-trivial number
// of (schema, instance) pairs and reports zero mismatches. Expected output line (from test/contracts/crosscheck.py
// or its wrapper): contains "crosscheck", "<N> pairs" with N > 0, and "0 mismatch".
func TestMakeContracts_CrosscheckRuns_01AC22(t *testing.T) {
	dir := treeCopy(t)
	code, out := makeContracts(t, dir)
	if code != 0 {
		t.Fatalf("`make contracts` exited %d, want 0:\n%s", code, out)
	}
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "crosscheck") && strings.Contains(l, "0 mismatch") && crosscheckPairs.MatchString(l) {
			return
		}
	}
	t.Errorf("no crosscheck summary line (containing \"crosscheck\", \"<N> pairs\" with N>0 and \"0 mismatch\"):\n%s", out)
}

// 01-AC-22: a fixture on which only one validator rejects fails the target on the crosscheck line.
// `format: uri` is asserted by the Go validator (santhosh-tekuri/jsonschema v6) but not by Python
// jsonschema 4.19.2 without optional packages, so "not a uri" is rejected by Go and accepted by Python.
// The invalid example makes the Go side deterministic too; the crosscheck must name the file.
func TestMakeContracts_CrosscheckDivergenceFails_01AC22(t *testing.T) {
	f := "contracts/fx-diverge-uri.schema.yaml"
	body := strings.Replace(schemaHead, "ID", "fx-diverge-uri", 1) + `  site: {type: string, format: uri}
examples:
  - {name: a, site: "not a uri"}
`
	expectFail(t, map[string]string{f: body}, "crosscheck", "fx-diverge-uri.schema.yaml")
}
