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
				gs = append(gs, ruleR14(in, f)...)
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
// and the manifest stays within the schema's array bounds.
func ruleR14(in GenerationInput, f *redFacts) []reasonGroup {
	ids := append([]string(nil), f.newTests...)
	seen := map[string]bool{}
	for _, id := range ids {
		seen[id] = true
	}
	kinds := map[string]string{}
	for _, id := range ids {
		kinds[id] = protectedKind(f.reported, id)
	}
	if in.Previous != nil {
		for _, t := range in.Previous.Tests {
			if !seen[t.ID] && f.headPresent[t.ID] {
				seen[t.ID] = true
				ids = append(ids, t.ID)
				kinds[t.ID] = t.Kind
			}
		}
	}
	size := 0
	for _, id := range ids {
		size += len(kinds[id]) + 1 + len(id) + 1
	}
	files := len(manifestFiles(in.Scope.ChangedFiles, in.Previous))
	mapped := 0
	for _, c := range in.Criteria {
		if len(in.Declared[c.ID]) > 0 {
			mapped++
		}
	}
	bad := false
	for _, id := range ids {
		bad = bad || !validTestID(id)
	}
	for _, p := range manifestFiles(in.Scope.ChangedFiles, in.Previous) {
		bad = bad || !validRepoPath(p.Path)
	}
	for _, st := range in.Scope.Stubs {
		bad = bad || !validRepoPath(st.Path)
	}
	if bad || size > maxProtectedListBytes || len(ids) > maxManifestTests || files > maxManifestFiles ||
		len(in.Scope.Stubs) > maxManifestStubs || mapped > maxManifestACMapping {
		return []reasonGroup{{outcome: outcomeRedInvalid, reasons: []string{"manifest_too_large"}}}
	}
	return nil
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
