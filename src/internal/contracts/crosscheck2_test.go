package contracts_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// Round-2 crosscheck tests (01-AC-22; plan 01 8.3 cross-implementation bullet; CD-60(b)).
//
// Interface under test: every validation-verdict row of out/contracts/report.json carries
//   "pair": {"schema": "<repo-relative schema file>#<JSON pointer of the subschema>",
//            "instance": "<repo-relative file>#<JSON pointer of the instance>"}
// (meta-validation: the schema side is the 2020-12 metaschema URI; OAS structure: the embedded OAS schema $id).
// test/contracts/crosscheck.py validates each pair in Python, compares with `result`, and fails when a row with
// `pair` cannot be evaluated or when there are zero pairs.

type pairRow struct {
	File   string `json:"file"`
	Check  string `json:"check"`
	Result string `json:"result"`
	Detail string `json:"detail"`
	Pair   *struct {
		Schema   string `json:"schema"`
		Instance string `json:"instance"`
	} `json:"pair"`
}

func readPairRows(t *testing.T, dir string) []pairRow {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "out", "contracts", "report.json"))
	if err != nil {
		t.Fatalf("report.json not written: %v", err)
	}
	var rows []pairRow
	if err := json.Unmarshal(b, &rows); err != nil {
		t.Fatalf("report.json: %v", err)
	}
	return rows
}

var pairCount = regexp.MustCompile(`\b([0-9]+) pairs?\b`)

// 01-AC-22: every verdict row of the listed checks has a non-empty pair; the crosscheck summary counts them all.
func TestCrosscheck2_RealTreeRowsCarryPairs_01AC22(t *testing.T) {
	dir := treeCopy(t)
	code, out := makeContracts(t, dir)
	if code != 0 {
		t.Fatalf("`make contracts` exited %d, want 0:\n%s", code, out)
	}
	rows := readPairRows(t, dir)
	withPair := 0
	missing := map[string]int{}
	for _, r := range rows {
		if r.Pair != nil {
			withPair++
		}
		var want bool
		switch r.Check {
		case "CONTRACT_EXAMPLE_INVALID", "CONTRACT_DEFS_EXAMPLE_INVALID", "CONTRACT_OPENAPI_EXAMPLE_INVALID",
			"CONTRACT_INSTANCE_INVALID", "CONTRACT_OPENAPI_INVALID":
			want = true
		case "CONTRACT_INVALID_EXAMPLE_PASSES":
			want = strings.HasPrefix(r.Detail, "x-invalid-examples[")
		case "CONTRACT_SCHEMA_INVALID":
			want = true
		}
		if !want {
			continue
		}
		if r.Pair == nil || r.Pair.Schema == "" || r.Pair.Instance == "" {
			missing[r.Check]++
			continue
		}
		switch r.Check {
		case "CONTRACT_EXAMPLE_INVALID":
			m := regexp.MustCompile(`^examples\[(\d+)\]`).FindStringSubmatch(r.Detail)
			if m != nil && (r.Pair.Schema != r.File+"#" || r.Pair.Instance != r.File+"#/examples/"+m[1]) {
				t.Errorf("%s %s: pair = %+v, want schema %q instance %q", r.File, r.Detail, *r.Pair, r.File+"#", r.File+"#/examples/"+m[1])
			}
		case "CONTRACT_DEFS_EXAMPLE_INVALID":
			m := regexp.MustCompile(`^x-defs-examples\.([^\[]+)\[(\d+)\]`).FindStringSubmatch(r.Detail)
			if m != nil && (r.Pair.Schema != r.File+"#/$defs/"+m[1] || r.Pair.Instance != r.File+"#/x-defs-examples/"+m[1]+"/"+m[2]) {
				t.Errorf("%s %s: pair = %+v, want $defs subschema %q", r.File, r.Detail, *r.Pair, r.File+"#/$defs/"+m[1])
			}
		case "CONTRACT_SCHEMA_INVALID":
			if !strings.Contains(r.Pair.Schema, "json-schema.org/draft/2020-12/schema") {
				t.Errorf("%s meta-validation pair.schema = %q, want the 2020-12 metaschema URI", r.File, r.Pair.Schema)
			}
		case "CONTRACT_OPENAPI_INVALID":
			if !strings.Contains(r.Pair.Schema, "spec.openapis.org/oas/3.1/schema/2022-10-07") {
				t.Errorf("%s OAS structure pair.schema = %q, want the embedded OAS 3.1 schema $id", r.File, r.Pair.Schema)
			}
		}
	}
	for chk, n := range missing {
		t.Errorf("%d %s rows lack a non-empty pair.schema/pair.instance", n, chk)
	}
	if withPair == 0 {
		t.Errorf("no report row carries a pair")
	}
	for _, l := range strings.Split(out, "\n") {
		if !strings.Contains(l, "crosscheck") {
			continue
		}
		if m := pairCount.FindStringSubmatch(l); m != nil {
			n, _ := strconv.Atoi(m[1])
			if n != withPair {
				t.Errorf("crosscheck summary reports %d pairs, report.json has %d rows with a pair: %q", n, withPair, l)
			}
			return
		}
	}
	t.Errorf("no crosscheck summary line with a pair count:\n%s", out)
}

func goZeroFailed(t *testing.T, dir, file string) {
	t.Helper()
	rows := readRows(t, dir)
	if n := countFile(rows, file, "fail"); n != 0 {
		t.Errorf("Go itself reports %d fail rows for %s, want 0 (only the crosscheck may reject it)", n, file)
	}
	if n := countFile(rows, file, "pass"); n == 0 {
		t.Errorf("Go has no pass rows for %s", file)
	}
}

// 01-AC-22: a schema example that Go accepts and Python rejects (named group in a format: regex value) fails the target.
func TestCrosscheck2_SchemaExampleDivergence_01AC22(t *testing.T) {
	f := "contracts/fx-xre.schema.yaml"
	body := strings.Replace(schemaHead, "ID", "fx-xre", 1) + `  re: {type: string, format: regex}
examples:
  - {name: a, re: "(?<y>x)"}
`
	dir, _ := expectFail(t, map[string]string{f: body}, "crosscheck", "fx-xre.schema.yaml")
	goZeroFailed(t, dir, f)
}

// 01-AC-22: the same for an OpenAPI schema example (these rows were never paired).
func TestCrosscheck2_OpenAPIExampleDivergence_01AC22(t *testing.T) {
	f := "contracts/fx-xre.openapi.yaml"
	body := `openapi: 3.1.0
info:
  title: Fixture
  version: v1alpha1
paths:
  /v1/things:
    get:
      operationId: listThings
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema:
                $ref: "#/components/schemas/S"
components:
  schemas:
    S:
      type: string
      format: regex
      examples: ["(?<y>x)"]
`
	dir, _ := expectFail(t, map[string]string{f: body}, "crosscheck", "fx-xre.openapi.yaml")
	goZeroFailed(t, dir, f)
}

func crosscheckPython() string {
	if _, err := os.Stat("/opt/sc/stage0-venv/bin/python"); err == nil {
		return "/opt/sc/stage0-venv/bin/python"
	}
	return "python3"
}

func runCrosscheck(t *testing.T, dir string) (int, string) {
	t.Helper()
	cmd := exec.Command(crosscheckPython(), "-I", "test/contracts/crosscheck.py", ".")
	cmd.Dir = dir
	b, err := cmd.CombinedOutput()
	if err == nil {
		return 0, string(b)
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode(), string(b)
	}
	t.Fatalf("running crosscheck.py: %v", err)
	return -1, ""
}

// 01-AC-22: a report with zero pairs fails the crosscheck.
func TestCrosscheck2_ZeroPairsFails_01AC22(t *testing.T) {
	dir := treeCopy(t)
	writeIn(t, dir, "out/contracts/report.json", "[]\n")
	code, out := runCrosscheck(t, dir)
	if code == 0 {
		t.Errorf("crosscheck.py exited 0 on a report with zero pairs, want non-zero:\n%s", out)
	}
}

// 01-AC-22: a report row whose pair cannot be evaluated (schema file does not exist) fails the crosscheck,
// even though other rows carry valid pairs.
func TestCrosscheck2_UnevaluablePairFails_01AC22(t *testing.T) {
	dir := treeCopy(t)
	if code, out := makeContracts(t, dir); code != 0 {
		t.Fatalf("`make contracts` exited %d, want 0:\n%s", code, out)
	}
	p := filepath.Join(dir, "out", "contracts", "report.json")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err := json.Unmarshal(b, &rows); err != nil {
		t.Fatal(err)
	}
	rows = append(rows, map[string]any{
		"file": "contracts/nope.schema.yaml", "check": "CONTRACT_EXAMPLE_INVALID", "result": "pass", "detail": "examples[0]",
		"pair": map[string]string{"schema": "contracts/nope.schema.yaml#", "instance": "contracts/nope.schema.yaml#/examples/0"},
	})
	nb, _ := json.Marshal(rows)
	if err := os.WriteFile(p, nb, 0o644); err != nil {
		t.Fatal(err)
	}
	code, out := runCrosscheck(t, dir)
	if code == 0 {
		t.Errorf("crosscheck.py exited 0 with an unevaluable pair, want non-zero:\n%s", out)
	}
}

// 01-AC-22: Python loads YAML like sc-contracts (YAML 1.2 core): `1:20` is a string, not the YAML 1.1
// sexagesimal integer 80, so the example passes in both validators.
func TestCrosscheck2_LoaderEquivalence_01AC22(t *testing.T) {
	f := "contracts/fx-yaml12.schema.yaml"
	body := strings.Replace(schemaHead, "ID", "fx-yaml12", 1) + `  t: {type: string}
examples:
  - {name: a, t: 1:20}
`
	dir := treeCopy(t)
	writeIn(t, dir, f, body)
	code, out := makeContracts(t, dir)
	goZeroFailed(t, dir, f)
	if code != 0 {
		t.Errorf("`make contracts` exited %d, want 0 (Go accepts the example):\n%s", code, out)
	}
	if !hasLine(out, "crosscheck", "0 mismatch") {
		t.Errorf("no crosscheck line with \"0 mismatch\":\n%s", out)
	}
	if hasLine(out, "MISMATCH", "fx-yaml12") {
		t.Errorf("crosscheck reports a mismatch for fx-yaml12 (Python loaded YAML 1.1 scalars):\n%s", out)
	}
}
