package verify

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// Slice S6, 22-AC-3 (plan 22 section 22 "Determinism", section 16): classifying each fixture 100 times yields
// identical output bytes. The fixtures are the existing directories (read, never written) plus the judge cases; the
// output is the canonical JSON of the classification (or the error text), compared byte for byte with the first run.
// Inputs are built once per fixture, and the same value is classified 100 times, so a classification that depends on
// map order, a clock or state left by an earlier call differs between runs.

const s6Runs = 100

// s6Output is the canonical output bytes of one classification.
func s6Output(v any, err error) string {
	b, merr := json.Marshal(struct {
		Result any    `json:"result"`
		Error  string `json:"error,omitempty"`
	}{v, errText(err)})
	if merr != nil {
		return "unmarshalable: " + merr.Error()
	}
	return string(b)
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// s6Repeat runs classify s6Runs times and requires byte-identical output; it returns the first output.
func s6Repeat(t *testing.T, classify func() string) string {
	t.Helper()
	first := classify()
	for i := 1; i < s6Runs; i++ {
		if got := classify(); got != first {
			t.Fatalf("run %d differs from run 0:\n run 0: %s\n run %d: %s", i, first, i, got)
		}
	}
	return first
}

// s6SubDirs lists the case directories of testdata/<tree>/<rule>, rule directories sorted.
func s6SubDirs(t *testing.T, tree string) []string {
	t.Helper()
	rules, err := os.ReadDir(filepath.Join("testdata", tree))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, r := range rules {
		if !r.IsDir() {
			continue
		}
		cases, err := os.ReadDir(filepath.Join("testdata", tree, r.Name()))
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range cases {
			if c.IsDir() {
				out = append(out, r.Name()+"/"+c.Name())
			}
		}
	}
	sort.Strings(out)
	return out
}

func TestClassification_100xByteIdentical_22AC3(t *testing.T) {
	total := 0
	t.Run("red", func(t *testing.T) {
		cases := s6SubDirs(t, "red")
		for _, c := range cases {
			t.Run(strings.ReplaceAll(c, "/", "_"), func(t *testing.T) {
				fx, exp := loadRedFixture(t, c)
				in := redInputOf(t, fx)
				out := s6Repeat(t, func() string { got, err := ClassifyRed(in); return s6Output(got, err) })
				if exp.Error == "" && !strings.Contains(out, `"outcome":"`+exp.Outcome+`"`) {
					t.Errorf("output %s does not carry the expected outcome %q", out, exp.Outcome)
				}
			})
		}
		total += len(cases)
	})
	t.Run("gen", func(t *testing.T) {
		var cases []string
		for _, c := range s6SubDirs(t, "gen") {
			rule, name, _ := strings.Cut(c, "/")
			cases = append(cases, c)
			t.Run(rule+"_"+name, func(t *testing.T) {
				var in GenerationInput
				switch rule {
				case "R1b":
					in = r1bSpec(t, c, strings.HasPrefix(name, "mapped"))
				case "R12":
					in = r12Spec(t, c, nil, nil)
				case "R13":
					if _, err := os.Stat(filepath.Join("testdata", "gen", c, "a.base.go.txt")); err == nil {
						in = r13Two(t, c)
					} else {
						in = r13Shared(t, c, nil)
					}
				default:
					t.Fatalf("no builder for testdata/gen/%s", rule)
				}
				out := s6Repeat(t, func() string { got, err := ClassifyRedGen(in); return s6Output(got, err) })
				if !strings.Contains(out, `"outcome":"red_`) && !strings.Contains(out, `"error"`) {
					t.Errorf("output %s carries neither a classification nor an error", out)
				}
			})
		}
		total += len(cases)
	})
	t.Run("green", func(t *testing.T) {
		cases := s6SubDirs(t, "green")
		for _, c := range cases {
			rule, name, _ := strings.Cut(c, "/")
			t.Run(rule+"_"+name, func(t *testing.T) {
				fx, exp := g5Load(t, rule, name)
				in := g5InputOf(t, fx)
				out := s6Repeat(t, func() string { got, err := ClassifyGreen(in); return s6Output(got, err) })
				if exp.Error == "" && !strings.Contains(out, `"outcome":"`+exp.Outcome+`"`) {
					t.Errorf("output %s does not carry the expected outcome %q", out, exp.Outcome)
				}
			})
		}
		total += len(cases)
	})
	t.Run("ingest", func(t *testing.T) {
		cases := s6SubDirs(t, "ingest")
		for _, c := range cases {
			t.Run(strings.ReplaceAll(c, "/", "_"), func(t *testing.T) {
				dir := filepath.Join("testdata", "ingest", filepath.FromSlash(c))
				var exp ingestExpect
				readJSONFile(t, &exp, dir, "expect.json")
				s := newScratch(t)
				s.write("pkg/core.go", "package pkg\n\nfunc Core() int { return 0 }\n")
				s.write("pkg/core_test.go", s3CoreTest)
				writeFixtureTree(t, s, filepath.Join(dir, "base"))
				red := s.commit("red")
				applyOps(t, s, filepath.Join(dir, "ops"))
				writeFixtureTree(t, s, filepath.Join(dir, "head"))
				head := s.commit("implementation")
				man := s.s3Manifest(red, append([]string{"pkg/core_test.go"}, exp.Manifest...)...)
				in := s.ingestionInput(t, "implementer", red, head, man)
				out := s6Repeat(t, func() string { got, err := CheckIngestion(in); return s6Output(got, err) })
				if len(exp.Violations) > 0 && strings.Contains(out, `"result":[]`) {
					t.Errorf("output %s flags nothing on a fixture that expects %d violations", out, len(exp.Violations))
				}
			})
		}
		total += len(cases)
	})
	if total < 250 {
		t.Fatalf("only %d fixtures classified; the existing fixture trees hold more than 250", total)
	}
}

// 22-AC-3 for the judge rules: each judge fixture classified 100 times gives identical bytes, and the output is a
// real classification (or the expected error), so a stub that answers nothing cannot pass.
func TestJudgeFixtures_100xByteIdentical_22AC3(t *testing.T) {
	total := 0
	for _, rule := range s6SubDirsOf(t, "judge") {
		for _, name := range s6CaseDirs(t, rule) {
			total++
			t.Run(rule+"_"+name, func(t *testing.T) {
				fx, exp := s6Load(t, rule, name)
				var classify func() string
				var failed func() bool
				switch exp.Function {
				case "validate":
					in, _ := s6ValidateInput(t, fx)
					var gotErr error
					var gotOutcome string
					classify = func() string {
						got, err := ClassifyJudge(in)
						gotErr, gotOutcome = err, got.Outcome
						return s6Output(got, err)
					}
					failed = func() bool {
						return (gotErr != nil) != (exp.Error != "") || (exp.Error == "" && gotOutcome != exp.Outcome)
					}
				default:
					in := s6JudgingInput(t, fx)
					var gotErr error
					var gotOutcome string
					classify = func() string {
						got, err := ClassifyJudging(in)
						gotErr, gotOutcome = err, got.Outcome
						return s6Output(got, err)
					}
					failed = func() bool {
						return (gotErr != nil) != (exp.Error != "") || (exp.Error == "" && gotOutcome != exp.Outcome)
					}
				}
				out := s6Repeat(t, classify)
				if failed() {
					t.Errorf("output %s is not the expected %s%s", out, exp.Outcome, exp.Error)
				}
			})
		}
	}
	if total < 60 {
		t.Fatalf("only %d judge fixtures; want at least 60", total)
	}
}

func s6SubDirsOf(t *testing.T, tree string) []string {
	t.Helper()
	ents, err := os.ReadDir(filepath.Join("testdata", tree))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range ents {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}
