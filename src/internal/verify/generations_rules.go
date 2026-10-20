package verify

import (
	"regexp"
	"strings"
)

// Size limits of rule R14 (plan 22 section 14.3): the serialized protected-test list and the schema bounds of
// contracts/test-manifest.schema.yaml.
const (
	maxProtectedListBytes = 60 * 1024
	maxManifestTests      = 1000
	maxManifestFiles      = 500
	maxManifestStubs      = 100
	maxManifestACMapping  = 50
	maxDiscoveryItems     = 200
)

// ClassifyRedGen applies rules R0-R11 (ClassifyRed) and the generation rules R1b, R11b, R12, R13 and R14 of plan 22
// section 14.3, each at its place of the rule order. It is pure and deterministic. Facts the generation rules need
// and that are unusable (missing repeat facts, a changed Go file that does not parse, an unknown opening, a
// strengthened or approved generation without its previous manifest) are errors carrying a *NotClassifiedError
// (verification_infra_failed), never a pass (22-R-3).
func ClassifyRedGen(in GenerationInput) (RedClassification, error) {
	switch in.Opening {
	case "", "strengthened", "test_change_approved":
	default:
		return RedClassification{}, infraErr("unknown generation opening %q", in.Opening)
	}
	if err := checkGenerationFacts(in); err != nil {
		return RedClassification{}, err
	}
	if in.Opening != "" && in.Previous == nil {
		return RedClassification{}, infraErr("generation opened by %q has no previous manifest", in.Opening)
	}
	if in.Opening == "test_change_approved" && len(in.ApprovedACIDs) == 0 {
		return RedClassification{}, infraErr("test_change_approved generation has no approved criteria")
	}
	repeat, err := repeatNewTests(in.RedInput)
	if err != nil {
		return RedClassification{}, err
	}
	rules := &genRules{
		early: func() ([]reasonGroup, error) { return ruleR1b(in) },
		late: func(f *redFacts) ([]reasonGroup, error) {
			var gs []reasonGroup
			if f.headOK {
				g, err := ruleR11b(in, repeat)
				if err != nil {
					return nil, err
				}
				gs = append(gs, g...)
			}
			switch in.Opening {
			case "strengthened":
				g, err := ruleR12(in, f)
				if err != nil {
					return nil, err
				}
				gs = append(gs, g...)
			case "test_change_approved":
				g, err := ruleR13(in, f)
				if err != nil {
					return nil, err
				}
				gs = append(gs, g...)
			}
			if f.headOK {
				g, err := ruleR14(in, f)
				if err != nil {
					return nil, err
				}
				gs = append(gs, g...)
			}
			return gs, nil
		},
	}
	return classifyRed(in.RedInput, rules)
}

// repeatNewTests is red.repeatNewTests of the platform verification policy. The repository policy has no spec.red
// (D15), so the effective values never lower it.
func repeatNewTests(in RedInput) (int, error) {
	red, ok := pathMap(in.Platform.Verification, "spec", "red")
	if !ok {
		return 0, infraErr("verification policy has no spec.red")
	}
	n, ok := intValue(red["repeatNewTests"])
	if !ok || n < 1 {
		return 0, infraErr("spec.red.repeatNewTests is not a positive integer")
	}
	return n, nil
}

var funcNameRE = regexp.MustCompile(`\.((?:Test|Example|Fuzz|Benchmark)[^/.]*)`)

// funcNameOfTest returns the Go function a declared test name or test id belongs to: the part after the package
// path (subtests dropped). A bare name is its own function.
func funcNameOfTest(name string) string {
	if i := strings.Index(name, "::"); i >= 0 {
		name = name[i+2:]
	}
	if m := funcNameRE.FindStringSubmatch(name); m != nil {
		return m[1]
	}
	if i := strings.IndexByte(name, '/'); i >= 0 && !strings.Contains(name[:i], ".") {
		name = name[:i]
	}
	if i := strings.LastIndexByte(name, '.'); i >= 0 {
		name = name[i+1:]
	}
	return name
}

// ---------------------------------------------------------------------------------------------------------------
// R11b

func ruleR11b(in GenerationInput, required int) ([]reasonGroup, error) {
	if in.Repeats == nil {
		return nil, infraErr("no repeat-run facts for rule R11b")
	}
	if in.Repeats.Runs < required {
		return nil, infraErr("%d repeat runs, red.repeatNewTests requires %d", in.Repeats.Runs, required)
	}
	var gs []reasonGroup
	if len(in.Repeats.Inconsistent) > 0 {
		gs = append(gs, reasonGroup{outcome: outcomeInconclusive, reasons: []string{"nondeterministic_test"}})
	}
	if len(in.Repeats.MarkerSensitive) > 0 {
		gs = append(gs, reasonGroup{outcome: outcomeRedInvalid, reasons: []string{"marker_conditional"}})
	}
	return gs, nil
}

// ---------------------------------------------------------------------------------------------------------------
// R12 and R13

// ---------------------------------------------------------------------------------------------------------------
// R14

// protectedKind is the `<kind>` of a protected-test line: new when absent at base, modified otherwise.
func protectedKind(reported map[string]TestObservation, id string) string {
	if t, ok := reported[id]; ok && t.StatusBase != statusAbsent {
		return "modified"
	}
	return "new"
}

// ruleR14: the serialized protected-test list (`<kind>\t<id>\n` lines, the tests of the manifest) is at most 60 KiB,
// the manifest stays within the schema's array bounds, and every item the manifest would record satisfies the
// schema's per-item limits (manifestLimits, decision D16 N1): the same item lists BuildManifest builds (files, tests
// with their suites, ac_mapping, stubs, test_paths) are checked with the same function. An ill-typed test-paths
// list is an unusable fact (error).
func ruleR14(in GenerationInput, f *redFacts) ([]reasonGroup, error) {
	ids := append([]string(nil), f.newTests...)
	seen := map[string]bool{}
	for _, id := range ids {
		seen[id] = true
	}
	tests := make([]ManifestTest, 0, len(ids))
	for _, id := range ids {
		obs := f.reported[id]
		suite := obs.Suite
		if suite == "" {
			suite = suiteOf(id)
		}
		tests = append(tests, ManifestTest{ID: id, Suite: suite, Kind: protectedKind(f.reported, id), ACIDs: declaredACs(in.RedInput, id), RedStatus: obs.StatusHead})
	}
	if in.Previous != nil {
		for _, t := range in.Previous.Tests {
			if !seen[t.ID] && f.headPresent[t.ID] {
				seen[t.ID] = true
				tests = append(tests, t)
			}
		}
	}
	size := 0
	for _, t := range tests {
		size += len(t.Kind) + 1 + len(t.ID) + 1
	}
	files := manifestFiles(in.Scope.ChangedFiles, in.Previous)
	mapped := 0
	for _, c := range in.Criteria {
		if len(in.Declared[c.ID]) > 0 {
			mapped++
		}
	}
	paths, err := manifestTestPaths(in.Effective.Values)
	if err != nil {
		return nil, infraErr("%v", err)
	}
	items := Manifest{
		Files: files, Tests: tests, ACMapping: manifestACMapping(in.Criteria, tests), Stubs: manifestStubs(in.Scope.Stubs),
		TestPaths: paths,
	}
	bad := len(itemViolations(items, false)) > 0
	if bad || size > maxProtectedListBytes || len(tests) > maxManifestTests || len(files) > maxManifestFiles ||
		len(in.Scope.Stubs) > maxManifestStubs || mapped > maxManifestACMapping {
		return []reasonGroup{{outcome: outcomeRedInvalid, reasons: []string{"manifest_too_large"}}}, nil
	}
	return nil, nil
}

// declaredACs are the criteria, in criteria order, whose declared names resolve to the test id (exactly or by
// `::`-suffix), as BuildManifest records them.
func declaredACs(in RedInput, id string) []string {
	out := []string{}
	for _, c := range in.Criteria {
		for _, name := range in.Declared[c.ID] {
			if id == name || strings.HasSuffix(id, "::"+name) {
				out = appendUnique(out, c.ID)
				break
			}
		}
	}
	return out
}

// manifestFiles is the files list of a manifest: the protected files of this generation's scope (test, fixture and
// test_infra class, added or modified), then the previous generation's other files. A path the previous manifest
// lists as added stays added (changes are relative to the base commit).
func manifestFiles(changed []ChangedFile, prev *Manifest) []ManifestFile {
	out := []ManifestFile{}
	have := map[string]bool{}
	prevChange := map[string]string{}
	if prev != nil {
		for _, f := range prev.Files {
			prevChange[f.Path] = f.Change
		}
	}
	for _, c := range changed {
		if c.Class != "test" && c.Class != "fixture" && c.Class != "test_infra" {
			continue
		}
		if c.Change != "added" && c.Change != "modified" {
			continue
		}
		change := c.Change
		if prevChange[c.Path] == "added" {
			change = "added"
		}
		out = append(out, ManifestFile{Path: c.Path, BlobSha: c.BlobSha, Class: c.Class, Change: change})
		have[c.Path] = true
	}
	if prev != nil {
		for _, f := range prev.Files {
			if !have[f.Path] {
				out = append(out, f)
			}
		}
	}
	return out
}
