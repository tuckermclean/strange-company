package verify

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
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

// repeatNewTests is red.repeatNewTests: the effective value when the repository set one, else the platform's.
func repeatNewTests(in RedInput) (int, error) {
	if v, ok := in.Effective.Values["/spec/red/repeatNewTests"]; ok {
		if n, ok := intValue(v); ok && n >= 1 {
			return n, nil
		}
		return 0, infraErr("effective /spec/red/repeatNewTests is not a positive integer")
	}
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

// ---------------------------------------------------------------------------------------------------------------
// Go function extraction.

// goFuncs maps a function key (name, or `(Recv).Name` for methods; repeated keys such as init get a `#n` suffix) to
// the exact source text of the declaration, from the `func` keyword to the closing brace.
type goFuncs struct {
	text  map[string]string
	order []string
	plain map[string]bool // key is a plain function (no receiver)
}

func parseGoFuncs(path string, src []byte) (goFuncs, error) {
	out := goFuncs{text: map[string]string{}, plain: map[string]bool{}}
	if src == nil {
		return out, nil
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, src, parser.SkipObjectResolution)
	if err != nil {
		return out, infraErr("%s does not parse: %v", path, err)
	}
	slice := func(a, b token.Pos) string {
		return string(src[fset.Position(a).Offset:fset.Position(b).Offset])
	}
	for _, d := range file.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok {
			continue
		}
		key := fd.Name.Name
		if fd.Recv != nil && len(fd.Recv.List) > 0 {
			key = "(" + slice(fd.Recv.List[0].Type.Pos(), fd.Recv.List[0].Type.End()) + ")." + key
		}
		base, n := key, 1
		for {
			if _, dup := out.text[key]; !dup {
				break
			}
			n++
			key = fmt.Sprintf("%s#%d", base, n)
		}
		out.text[key] = slice(fd.Pos(), fd.End())
		out.order = append(out.order, key)
		out.plain[key] = fd.Recv == nil
	}
	return out, nil
}

func isGoFile(f TestFile) bool { return f.Language == "go" }

func isTestFuncName(name string) bool {
	for _, p := range []string{"Test", "Example", "Fuzz", "Benchmark"} {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
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
// R1b

// ruleR1b: in an existing Go test file, every test function that exists at the generation start and is not mapped
// to a change criterion keeps its declaration and body byte for byte. Only Go is judged in this slice; helper
// functions are not judged.
func ruleR1b(in GenerationInput) ([]reasonGroup, error) {
	mapped := map[string]bool{}
	kinds := map[string]string{}
	for _, c := range in.Criteria {
		kinds[c.ID] = c.Kind
	}
	for ac, names := range in.Declared {
		if kinds[ac] != "change" {
			continue
		}
		for _, n := range names {
			mapped[funcNameOfTest(n)] = true
		}
	}
	weakened := false
	for _, f := range in.TestFiles {
		if !isGoFile(f) || f.Base == nil || f.Head == nil {
			continue
		}
		base, err := parseGoFuncs(f.Path, f.Base)
		if err != nil {
			return nil, err
		}
		head, err := parseGoFuncs(f.Path, f.Head)
		if err != nil {
			return nil, err
		}
		for _, k := range base.order {
			if !base.plain[k] || !isTestFuncName(k) || mapped[k] {
				continue
			}
			if t, ok := head.text[k]; !ok || t != base.text[k] {
				weakened = true
			}
		}
	}
	if weakened {
		return []reasonGroup{{outcome: outcomeRedInvalid, reasons: []string{"existing_test_weakened"}}}, nil
	}
	return nil, nil
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

func prevFilePaths(m *Manifest) map[string]bool {
	out := map[string]bool{}
	for _, f := range m.Files {
		out[f.Path] = true
	}
	return out
}

// unchangedFunctions reports whether every function of base that is not exempt exists in head with identical text.
func unchangedFunctions(f TestFile, exempt func(key string) bool) (bool, error) {
	base, err := parseGoFuncs(f.Path, f.Base)
	if err != nil {
		return false, err
	}
	head, err := parseGoFuncs(f.Path, f.Head)
	if err != nil {
		return false, err
	}
	for _, k := range base.order {
		if exempt != nil && exempt(k) {
			continue
		}
		if t, ok := head.text[k]; !ok || t != base.text[k] {
			return false, nil
		}
	}
	return true, nil
}

// ruleR12: every previous manifest test is still declared and present, and every previous manifest file keeps all
// its functions byte for byte (only whole new functions or files are added). A changed previous file that is not Go
// cannot gain functions: any change to it is a violation.
func ruleR12(in GenerationInput, f *redFacts) ([]reasonGroup, error) {
	bad := false
	for _, t := range in.Previous.Tests {
		if !f.headPresent[t.ID] || !f.declaredResolved[t.ID] {
			bad = true
		}
	}
	prev := prevFilePaths(in.Previous)
	for _, tf := range in.TestFiles {
		if !prev[tf.Path] || tf.Base == nil || bytes.Equal(tf.Base, tf.Head) {
			continue
		}
		if tf.Head == nil || !isGoFile(tf) {
			bad = true
			continue
		}
		ok, err := unchangedFunctions(tf, nil)
		if err != nil {
			return nil, err
		}
		bad = bad || !ok
	}
	if bad {
		return []reasonGroup{{outcome: outcomeRedInvalid, reasons: []string{"manifest_not_superset"}}}, nil
	}
	return nil, nil
}

// ruleR13: every previous manifest test not mapped to an approved criterion is present, and every previous
// manifest file is byte-identical, except that a Go file holding an approved test may change its approved test
// functions and gain new functions.
func ruleR13(in GenerationInput, f *redFacts) ([]reasonGroup, error) {
	approved := map[string]bool{}
	for _, ac := range in.ApprovedACIDs {
		approved[ac] = true
	}
	approvedFuncs := map[string]bool{}
	bad := false
	for _, t := range in.Previous.Tests {
		isApproved := false
		for _, ac := range t.ACIDs {
			isApproved = isApproved || approved[ac]
		}
		if isApproved {
			approvedFuncs[funcNameOfTest(t.ID)] = true
		} else if !f.headPresent[t.ID] {
			bad = true
		}
	}
	prev := prevFilePaths(in.Previous)
	for _, tf := range in.TestFiles {
		if !prev[tf.Path] || tf.Base == nil || bytes.Equal(tf.Base, tf.Head) {
			continue
		}
		if tf.Head == nil || !isGoFile(tf) {
			bad = true
			continue
		}
		base, err := parseGoFuncs(tf.Path, tf.Base)
		if err != nil {
			return nil, err
		}
		holdsApproved := false
		for _, k := range base.order {
			holdsApproved = holdsApproved || (base.plain[k] && approvedFuncs[k])
		}
		if !holdsApproved {
			bad = true
			continue
		}
		ok, err := unchangedFunctions(tf, func(k string) bool { return base.plain[k] && approvedFuncs[k] })
		if err != nil {
			return nil, err
		}
		bad = bad || !ok
	}
	if bad {
		return []reasonGroup{{outcome: outcomeRedInvalid, reasons: []string{"unapproved_test_change"}}}, nil
	}
	return nil, nil
}

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
	if size > maxProtectedListBytes || len(ids) > maxManifestTests || files > maxManifestFiles ||
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
