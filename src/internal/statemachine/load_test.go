package statemachine

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// idSet implements GuardRegistry and EffectRegistry from a set of identifiers (the real implementations arrive
// with Fire, plan 29 section 7.1; S1 only needs identifier membership).
type idSet map[string]bool

func (s idSet) HasGuard(id string) bool  { return s[id] }
func (s idSet) HasEffect(id string) bool { return s[id] }

var modes = []string{"test-first", "verification-first"}

func repoRoot(t testing.TB) string {
	t.Helper()
	r, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func contractsDir(t testing.TB) string { return filepath.Join(repoRoot(t), "contracts") }

func opts(t testing.TB) Options { return Options{ContractsDir: contractsDir(t)} }

func tablePath(t testing.TB) string { return filepath.Join(contractsDir(t), "state-machine.yaml") }

func baseBytes(t testing.TB) []byte {
	t.Helper()
	b, err := os.ReadFile(tablePath(t))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func parseMap(t testing.TB, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := yaml.Unmarshal(b, &m); err != nil {
		t.Fatalf("parse: %v", err)
	}
	return m
}

// registries builds guard and effect registries from the identifiers the (unmutated) table declares.
func registries(t testing.TB, doc map[string]any) (idSet, idSet) {
	t.Helper()
	spec := doc["spec"].(map[string]any)
	g, e := idSet{}, idSet{}
	for k := range spec["guards"].(map[string]any) {
		g[k] = true
	}
	for k := range spec["effects"].(map[string]any) {
		e[k] = true
	}
	return g, e
}

func loadBase(t testing.TB) (*Machine, error) {
	t.Helper()
	b := baseBytes(t)
	g, e := registries(t, parseMap(t, b))
	return Load(b, g, e, opts(t))
}

func codeOf(t testing.TB, err error) *LoadError {
	t.Helper()
	var le *LoadError
	if !errors.As(err, &le) {
		t.Fatalf("error is not a *LoadError: %T %v", err, err)
	}
	return le
}

// ---- 29-AC-1

func TestLoad_AcceptsContract_29AC1(t *testing.T) {
	m, err := loadBase(t)
	if err != nil {
		t.Fatalf("Load(contracts/state-machine.yaml) failed: %v", err)
	}
	if m == nil {
		t.Fatal("Load returned a nil Machine and no error")
	}
}

func TestRevision_29AC1(t *testing.T) {
	m, err := loadBase(t)
	if err != nil || m == nil {
		t.Fatalf("Load failed: %v", err)
	}
	want := parseMap(t, baseBytes(t))["metadata"].(map[string]any)["revision"].(int)
	if got := Revision(m); got != want || got < 1 {
		t.Fatalf("Revision = %d, want metadata.revision %d", got, want)
	}
}

// statesInMode counts the declared states available in a mode, computed independently of the code under test.
func statesInMode(t testing.TB, doc map[string]any, mode string) int {
	t.Helper()
	n := 0
	for _, s := range doc["spec"].(map[string]any)["states"].([]any) {
		for _, m := range s.(map[string]any)["modes"].([]any) {
			if m == mode {
				n++
			}
		}
	}
	return n
}

func TestReport_NoUnreachableNoNonTerminating_29AC1(t *testing.T) {
	m, err := loadBase(t)
	if err != nil || m == nil {
		t.Fatalf("Load failed: %v", err)
	}
	doc := parseMap(t, baseBytes(t))
	rep := m.Report()
	if len(rep.Modes) != len(modes) {
		t.Fatalf("report has %d modes, want %d: %+v", len(rep.Modes), len(modes), rep)
	}
	for i, mode := range modes {
		mr := rep.Modes[i]
		want := statesInMode(t, doc, mode)
		if mr.Mode != mode {
			t.Errorf("report mode %d = %q, want %q", i, mr.Mode, mode)
		}
		if mr.States != want || mr.Reachable != want {
			t.Errorf("%s: states=%d reachable=%d, want both %d", mode, mr.States, mr.Reachable, want)
		}
		if len(mr.Unreachable) != 0 {
			t.Errorf("%s: unreachable states %v", mode, mr.Unreachable)
		}
		if len(mr.NonTerminating) != 0 {
			t.Errorf("%s: non-terminating states %v", mode, mr.NonTerminating)
		}
	}
}

// Check is the Go entry point of `make state-machine-check` (the Make target and a thin main come with GREEN).
func TestCheck_ContractPassesAndReports_29AC1(t *testing.T) {
	var out bytes.Buffer
	if err := Check(&out, tablePath(t), opts(t)); err != nil {
		t.Fatalf("Check failed: %v", err)
	}
	for _, mode := range modes {
		re := regexp.MustCompile(`(?m)^mode ` + regexp.QuoteMeta(mode) + `: states=([0-9]+) reachable=([0-9]+) unreachable=0 non-terminating=0$`)
		mm := re.FindStringSubmatch(out.String())
		if mm == nil {
			t.Fatalf("output has no line `mode %s: states=N reachable=N unreachable=0 non-terminating=0`:\n%s", mode, out.String())
		}
		if mm[1] != mm[2] {
			t.Errorf("%s: states=%s reachable=%s differ", mode, mm[1], mm[2])
		}
	}
}

func TestCheck_InvalidTableFailsWithCode_29AC1(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "state-machine.yaml")
	bad := bytes.Replace(baseBytes(t), []byte("initialState: SPECIFIED"), []byte("initialState: GHOST"), 1)
	if err := os.WriteFile(p, bad, 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err := Check(&out, p, opts(t))
	if err == nil {
		t.Fatalf("Check accepted a table with an undeclared initialState; output:\n%s", out.String())
	}
	if le := codeOf(t, err); le.Code != CodeInitialStateInvalid {
		t.Fatalf("code = %s, want %s", le.Code, CodeInitialStateInvalid)
	}
}

func TestCheck_UnreadableTable_29AC1(t *testing.T) {
	err := Check(&bytes.Buffer{}, filepath.Join(t.TempDir(), "missing.yaml"), opts(t))
	if err == nil {
		t.Fatal("Check accepted a missing table")
	}
	if le := codeOf(t, err); le.Code != CodeUnreadable {
		t.Fatalf("code = %s, want %s", le.Code, CodeUnreadable)
	}
}

// ---- fail closed (29-AC-1, 29-AC-2)

func TestLoad_FailsClosedWithoutRegistries_29AC2(t *testing.T) {
	_, err := Load(baseBytes(t), nil, nil, opts(t))
	if err == nil {
		t.Fatal("Load accepted the table with nil guard and effect registries")
	}
	if le := codeOf(t, err); le.Code != CodeGuardUnknown {
		t.Fatalf("code = %s, want %s", le.Code, CodeGuardUnknown)
	}
}

func TestLoad_FailsClosedWithoutSchema_29AC2(t *testing.T) {
	b := baseBytes(t)
	g, e := registries(t, parseMap(t, b))
	for name, o := range map[string]Options{
		"empty contracts dir":   {},
		"contracts dir missing": {ContractsDir: filepath.Join(t.TempDir(), "none")},
	} {
		_, err := Load(b, g, e, o)
		if err == nil {
			t.Fatalf("%s: Load accepted the table without a schema", name)
		}
		if le := codeOf(t, err); le.Code != CodeSchemaUnavailable {
			t.Fatalf("%s: code = %s, want %s", name, le.Code, CodeSchemaUnavailable)
		}
	}
}

func TestLoad_RejectsEmptyAndNonMappingDocuments_29AC2(t *testing.T) {
	g, e := idSet{}, idSet{}
	for name, doc := range map[string]string{"empty": "", "scalar": "just a string\n", "list": "- a\n- b\n"} {
		_, err := Load([]byte(doc), g, e, opts(t))
		if err == nil {
			t.Fatalf("%s: Load accepted the document", name)
		}
		le := codeOf(t, err)
		if le.Code != CodeYAMLInvalid && le.Code != CodeSchemaInvalid {
			t.Fatalf("%s: code = %s, want %s or %s", name, le.Code, CodeYAMLInvalid, CodeSchemaInvalid)
		}
	}
}

// ---- 29-AC-2 invalid fixtures

type fixtureText struct {
	Old string `yaml:"old"`
	New string `yaml:"new"`
}

type fixtureOp struct {
	Op    string `yaml:"op"`
	Path  string `yaml:"path"`
	Value any    `yaml:"value"`
}

// fixture is one testdata/invalid/*.yaml file: the expected error code, an optional mode the error must name,
// exact-once text replacements on the base table and structural operations on the parsed base table.
type fixture struct {
	Code string        `yaml:"code"`
	Mode string        `yaml:"mode"`
	Text []fixtureText `yaml:"text"`
	Ops  []fixtureOp   `yaml:"ops"`
}

// resolveSeg finds the index a path segment selects in a list: a number, `key=value` (a mapping element whose
// key equals value) or `=value` (a scalar element).
func resolveSeg(s []any, seg string) (int, error) {
	if i, err := strconv.Atoi(seg); err == nil {
		if i < 0 || i >= len(s) {
			return 0, fmt.Errorf("index %d out of range (%d)", i, len(s))
		}
		return i, nil
	}
	k, v, ok := strings.Cut(seg, "=")
	if !ok {
		return 0, fmt.Errorf("segment %q selects nothing in a list", seg)
	}
	for i, el := range s {
		if k == "" {
			if fmt.Sprint(el) == v {
				return i, nil
			}
			continue
		}
		if m, ok := el.(map[string]any); ok && fmt.Sprint(m[k]) == v {
			return i, nil
		}
	}
	return 0, fmt.Errorf("no list element matches %q", seg)
}

func edit(node any, segs []string, o fixtureOp) (any, error) {
	seg := segs[0]
	last := len(segs) == 1
	switch n := node.(type) {
	case map[string]any:
		child, exists := n[seg]
		if !last {
			if !exists {
				return nil, fmt.Errorf("no key %q", seg)
			}
			nc, err := edit(child, segs[1:], o)
			if err != nil {
				return nil, err
			}
			n[seg] = nc
			return n, nil
		}
		switch o.Op {
		case "remove":
			if !exists {
				return nil, fmt.Errorf("remove: no key %q", seg)
			}
			delete(n, seg)
		case "replace":
			if !exists {
				return nil, fmt.Errorf("replace: no key %q", seg)
			}
			n[seg] = o.Value
		case "add":
			if exists {
				s, ok := child.([]any)
				if !ok {
					return nil, fmt.Errorf("add: %q exists and is not a list", seg)
				}
				n[seg] = append(s, o.Value)
			} else {
				n[seg] = o.Value
			}
		default:
			return nil, fmt.Errorf("unknown op %q", o.Op)
		}
		return n, nil
	case []any:
		i, err := resolveSeg(n, seg)
		if err != nil {
			return nil, err
		}
		if !last {
			nc, err := edit(n[i], segs[1:], o)
			if err != nil {
				return nil, err
			}
			n[i] = nc
			return n, nil
		}
		switch o.Op {
		case "remove":
			return append(n[:i:i], n[i+1:]...), nil
		case "replace":
			n[i] = o.Value
			return n, nil
		}
		return nil, fmt.Errorf("op %q on a list element", o.Op)
	}
	return nil, fmt.Errorf("cannot descend into %T at %q", node, seg)
}

// mutate applies a fixture to the base table and returns the table text.
func mutate(t *testing.T, base []byte, fx fixture) []byte {
	t.Helper()
	text := string(base)
	for _, r := range fx.Text {
		if strings.Count(text, r.Old) != 1 {
			t.Fatalf("fixture text %q must match the base exactly once", r.Old)
		}
		text = strings.Replace(text, r.Old, r.New, 1)
	}
	if len(fx.Ops) == 0 {
		return []byte(text)
	}
	var doc any
	if err := yaml.Unmarshal([]byte(text), &doc); err != nil {
		t.Fatalf("fixture base text: %v", err)
	}
	for _, o := range fx.Ops {
		var err error
		doc, err = edit(doc, strings.Split(o.Path, "/"), o)
		if err != nil {
			t.Fatalf("fixture op %s %s: %v", o.Op, o.Path, err)
		}
	}
	out, err := yaml.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func fixtureFiles(t testing.TB) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join("testdata", "invalid", "*.yaml"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no fixtures in testdata/invalid: %v", err)
	}
	sort.Strings(files)
	return files
}

func readFixture(t testing.TB, p string) fixture {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var fx fixture
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(&fx); err != nil {
		t.Fatalf("%s: %v", p, err)
	}
	if !strings.HasPrefix(fx.Code, "SM_") {
		t.Fatalf("%s: code %q is not an SM_ code", p, fx.Code)
	}
	return fx
}

// 29-AC-2: every fixture makes Load fail with exactly its expected code. Each fixture mutates the real table
// by one invariant only; registries come from the unmutated table, so identifiers a fixture adds to the
// catalogues are unknown to the registry. Load checks run in this order, so a fixture meets only its own check:
// YAML, schema, declarations, registries, per-transition rules, then graph rules (reachability, termination).
func TestInvalidFixtures_29AC2(t *testing.T) {
	t.Run("coverage", checkFixtureCoverage)
	base := baseBytes(t)
	g, e := registries(t, parseMap(t, base))
	for _, p := range fixtureFiles(t) {
		name := strings.TrimSuffix(filepath.Base(p), ".yaml")
		t.Run(name, func(t *testing.T) {
			fx := readFixture(t, p)
			table := mutate(t, base, fx)
			m, err := Load(table, g, e, opts(t))
			if err == nil {
				t.Fatalf("Load accepted the table (machine %v), want %s", m, fx.Code)
			}
			le := codeOf(t, err)
			if le.Code != fx.Code {
				t.Fatalf("code = %s (%s), want %s", le.Code, le.Message, fx.Code)
			}
			if fx.Mode != "" && le.Mode != fx.Mode {
				t.Fatalf("error mode = %q, want %q", le.Mode, fx.Mode)
			}
			if !strings.HasPrefix(err.Error(), fx.Code) {
				t.Fatalf("error text %q does not start with the code %s", err.Error(), fx.Code)
			}
		})
	}
}

// Every invariant of state-machine.schema.yaml and plan 29 section 8.2 has a fixture, and the four named by
// the acceptance criterion exist under their documented names.
func checkFixtureCoverage(t *testing.T) {
	t.Helper()
	byName := map[string]string{}
	codes := map[string]bool{}
	for _, p := range fixtureFiles(t) {
		fx := readFixture(t, p)
		byName[strings.TrimSuffix(filepath.Base(p), ".yaml")] = fx.Code
		codes[fx.Code] = true
	}
	for name, code := range map[string]string{
		"held_exit_runner_needs_human":       CodeHeldExitNotHuman,
		"human_targets_implementing":         CodeHumanTargetsExecution,
		"unknown_guard_id":                   CodeGuardUnknown,
		"outcome_missing_red_classified":     CodeOutcomeIncomplete,
		"outcome_missing_green_classified":   CodeOutcomeIncomplete,
		"outcome_missing_judge_classified":   CodeOutcomeIncomplete,
		"outcome_missing_judging_classified": CodeOutcomeIncomplete,
		"unreachable_in_one_mode":            CodeUnreachable,
		"non_terminating_in_one_mode":        CodeNonTerminating,
	} {
		if got := byName[name]; got != code {
			t.Errorf("fixture %s: code %q, want %s", name, got, code)
		}
	}
	for _, code := range []string{
		CodeYAMLInvalid, CodeYAMLAnchor, CodeSchemaInvalid, CodeStateUndeclared, CodeReturnTargetUndeclared,
		CodeProjectionStateUndecl, CodeInitialStateInvalid, CodeTerminalMismatch, CodeGuardUndeclared,
		CodeEffectUndeclared, CodeGuardUnknown, CodeEffectUnknown, CodeEventUndeclared, CodeTransitionIDDuplicate,
		CodeUnreachable, CodeNonTerminating, CodeTerminalHasOutgoing, CodeReturnFromNonExecution,
		CodeHeldExitNotHuman, CodeHumanTargetsExecution, CodeProjectionUncovered, CodeStallEscalateInvalid,
		CodeStallNoTransition, CodeHumanCommandMissing, CodeOutcomeIncomplete, CodeFailureClassIncomplete,
	} {
		if !codes[code] {
			t.Errorf("no invalid fixture expects %s", code)
		}
	}
}
