package verify

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
)

// ---------------------------------------------------------------------------------------------------------------
// The structural primitive of rules R1b, R12 and R13 (decision D15): a pre-existing Go test file passes only if its
// header is byte-identical, every base import spec is unchanged, and every base top-level declaration is unchanged
// except the functions of the rule's allowed set A. Only whole new declarations and new plain imports are added.

type goImport struct {
	text  string
	plain bool // not a `_` or `.` import
}

type goDecl struct {
	key  string // function key (receiver and name); empty for var/const/type declarations and init functions
	text string // exact source text, doc comment included
}

// goFileShape is a Go file split into header, import specs and top-level declarations. cgo holds the exact text of
// each import declaration that imports "C", doc comment (the cgo preamble) included.
type goFileShape struct {
	header  string
	imports []goImport
	cgo     []string
	decls   []goDecl
}

func parseGoFileShape(path string, src []byte) (goFileShape, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, src, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		return goFileShape{}, infraErr("%s does not parse: %v", path, err)
	}
	off := func(p token.Pos) int { return fset.Position(p).Offset }
	slice := func(a, b token.Pos) string { return string(src[off(a):off(b)]) }
	sh := goFileShape{header: string(src[:off(file.Name.End())])}
	for _, d := range file.Decls {
		switch d := d.(type) {
		case *ast.GenDecl:
			if d.Tok == token.IMPORT {
				// An import spec is compared with its doc comment and, when the declaration holds this one spec, with
				// the declaration's doc comment: cgo reads the preamble of `import "C"` from there (decision D16).
				declStart := d.Pos()
				if d.Doc != nil {
					declStart = d.Doc.Pos()
				}
				cgo := false
				for _, sp := range d.Specs {
					is := sp.(*ast.ImportSpec)
					plain := is.Name == nil || (is.Name.Name != "_" && is.Name.Name != ".")
					start := is.Pos()
					if is.Doc != nil {
						start = is.Doc.Pos()
					}
					text := slice(start, is.End())
					if len(d.Specs) == 1 && d.Doc != nil {
						text = slice(d.Doc.Pos(), d.Doc.End()) + "\n" + text
					}
					if p, err := strconv.Unquote(is.Path.Value); err != nil || p == "C" {
						cgo = true
					}
					sh.imports = append(sh.imports, goImport{text: text, plain: plain})
				}
				if cgo {
					sh.cgo = append(sh.cgo, slice(declStart, d.End()))
				}
				continue
			}
			start := d.Pos()
			if d.Doc != nil {
				start = d.Doc.Pos()
			}
			sh.decls = append(sh.decls, goDecl{text: slice(start, d.End())})
		case *ast.FuncDecl:
			start := d.Pos()
			if d.Doc != nil {
				start = d.Doc.Pos()
			}
			key := d.Name.Name
			if d.Recv != nil && len(d.Recv.List) > 0 {
				key = "(" + slice(d.Recv.List[0].Type.Pos(), d.Recv.List[0].Type.End()) + ")." + key
			}
			if d.Recv == nil && (key == "init" || key == "_") {
				key = ""
			}
			sh.decls = append(sh.decls, goDecl{key: key, text: slice(start, d.End())})
		}
	}
	return sh, nil
}

// structuralOK reports whether head is base plus whole new declarations and new plain imports, with the functions of
// allowed (plain function names) free to change and, with allowRemoval, to disappear.
func structuralOK(base, head goFileShape, allowed map[string]bool, allowRemoval bool) bool {
	if base.header != head.header {
		return false
	}
	// A cgo import declaration (preamble, specs and comments) is byte-identical, and none is added or removed.
	if len(base.cgo) != len(head.cgo) {
		return false
	}
	headCgo := map[string]int{}
	for _, t := range head.cgo {
		headCgo[t]++
	}
	for _, t := range base.cgo {
		if headCgo[t] == 0 {
			return false
		}
		headCgo[t]--
	}
	baseImports := map[string]bool{}
	for _, i := range base.imports {
		baseImports[i.text] = true
	}
	headImports := map[string]bool{}
	for _, i := range head.imports {
		headImports[i.text] = true
		if !baseImports[i.text] && !i.plain {
			return false
		}
	}
	for t := range baseImports {
		if !headImports[t] {
			return false
		}
	}
	funcs := map[string]string{}
	rest := map[string]int{}
	for _, d := range head.decls {
		if d.key != "" {
			funcs[d.key] = d.text
		} else {
			rest[d.text]++
		}
	}
	for _, d := range base.decls {
		if d.key == "" {
			if rest[d.text] == 0 {
				return false
			}
			rest[d.text]--
			continue
		}
		t, ok := funcs[d.key]
		if allowed[d.key] {
			if !ok && !allowRemoval {
				return false
			}
			continue
		}
		if !ok || t != d.text {
			return false
		}
	}
	return true
}

func isGoFile(f TestFile) bool { return f.Language == "go" }

// checkGenerationFacts rejects Runner facts that contradict each other (decision D16 B2, 22-R-3): a previous
// manifest without an opening, a `.go` TestFile whose language is not exactly "go", two TestFiles for one path, and
// a ChangedFile whose change contradicts its TestFile (modified without both contents, added with a base or without
// a head, deleted with a head or without a base).
func checkGenerationFacts(in GenerationInput) error {
	if in.Previous != nil && in.Opening == "" {
		return infraErr("a previous manifest is given for a first generation (no opening)")
	}
	files := map[string]TestFile{}
	for _, f := range in.TestFiles {
		if strings.HasSuffix(strings.ToLower(f.Path), ".go") && f.Language != "go" {
			return infraErr("test file %s has language %q, not \"go\"", f.Path, f.Language)
		}
		if _, dup := files[f.Path]; dup {
			return infraErr("test file %s is given twice", f.Path)
		}
		files[f.Path] = f
	}
	for _, c := range in.Scope.ChangedFiles {
		f, ok := files[c.Path]
		if !ok {
			continue
		}
		var bad bool
		switch c.Change {
		case "modified":
			bad = f.Base == nil || f.Head == nil
		case "added":
			bad = f.Base != nil || f.Head == nil
		case "deleted":
			bad = f.Base == nil || f.Head != nil
		}
		if bad {
			return infraErr("changed file %s (%s) contradicts its contents (base present %v, head present %v)", c.Path, c.Change, f.Base != nil, f.Head != nil)
		}
	}
	return nil
}

func testFileByPath(in GenerationInput) map[string]TestFile {
	out := map[string]TestFile{}
	for _, f := range in.TestFiles {
		out[f.Path] = f
	}
	return out
}

func isProtectedClass(c string) bool { return c == "test" || c == "fixture" || c == "test_infra" }

// ---------------------------------------------------------------------------------------------------------------
// R1b

// ruleR1b: in an existing Go test file the file header, imports and every declaration stay as they were, except the
// functions that hold a test mapped to a change criterion. The identity of a test function is (file, function name),
// the file coming from the observation. Only Go is judged in this slice.
func ruleR1b(in GenerationInput) ([]reasonGroup, error) {
	files := testFileByPath(in)
	for _, c := range in.Scope.ChangedFiles {
		if !isProtectedClass(c.Class) || c.Change != "modified" {
			continue
		}
		if _, ok := files[c.Path]; ok {
			continue
		}
		for _, t := range in.Observed.Tests {
			if t.File == c.Path {
				return nil, infraErr("modified file %s holds tests but has no content entry", c.Path)
			}
		}
	}
	reported := map[string]TestObservation{}
	ids := map[string]bool{}
	for _, t := range in.Observed.Tests {
		reported[t.ID] = t
		if t.StatusHead != statusAbsent {
			ids[t.ID] = true
		}
	}
	if in.Observed.TestIDSets != nil {
		hp, err := decodeIDSet("head_present", in.Observed.TestIDSets.HeadPresent)
		if err != nil {
			return nil, err
		}
		for id := range hp {
			ids[id] = true
		}
	}
	allowed := map[string]map[string]bool{} // file -> plain function names
	for _, c := range in.Criteria {
		if c.Kind != "change" {
			continue
		}
		for _, name := range in.Declared[c.ID] {
			for _, id := range sortedKeys(ids) {
				if id != name && !strings.HasSuffix(id, "::"+name) {
					continue
				}
				obs, ok := reported[id]
				if !ok || obs.File == "" {
					return nil, infraErr("mapped test %q cannot be resolved to a file", id)
				}
				if allowed[obs.File] == nil {
					allowed[obs.File] = map[string]bool{}
				}
				allowed[obs.File][funcNameOfTest(id)] = true
			}
		}
	}
	// A previous manifest file of a strengthened or approved generation is judged by R12 or R13, which are stricter
	// for the declarations outside the approved functions and know the approved scope.
	judgedLater := map[string]bool{}
	if in.Previous != nil {
		for _, pf := range in.Previous.Files {
			judgedLater[pf.Path] = true
		}
	}
	weakened := false
	for _, f := range in.TestFiles {
		if !isGoFile(f) || f.Base == nil || f.Head == nil || judgedLater[f.Path] {
			continue
		}
		base, err := parseGoFileShape(f.Path, f.Base)
		if err != nil {
			return nil, err
		}
		head, err := parseGoFileShape(f.Path, f.Head)
		if err != nil {
			return nil, err
		}
		if !structuralOK(base, head, allowed[f.Path], true) {
			weakened = true
		}
	}
	if weakened {
		return []reasonGroup{{outcome: outcomeRedInvalid, reasons: []string{"existing_test_weakened"}}}, nil
	}
	return nil, nil
}

// ---------------------------------------------------------------------------------------------------------------
// R12 and R13

// prevFileJudgment checks every previous manifest file that the red range touches and reports whether one breaks
// the rule. allowedFor returns the functions of a Go file that may change (nil: none).
func prevFileJudgment(in GenerationInput, allowedFor func(path string) map[string]bool, mustBeIdentical func(path string) bool) (bool, error) {
	prev := map[string]bool{}
	for _, f := range in.Previous.Files {
		prev[f.Path] = true
	}
	files := testFileByPath(in)
	bad := false
	for _, c := range in.Scope.ChangedFiles {
		if !prev[c.Path] {
			continue
		}
		switch c.Change {
		case "deleted":
			bad = true
		case "modified":
			if _, ok := files[c.Path]; !ok {
				return false, infraErr("previous manifest file %s is modified but has no content entry", c.Path)
			}
		}
	}
	for _, tf := range in.TestFiles {
		if !prev[tf.Path] || tf.Base == nil {
			continue
		}
		if tf.Head == nil {
			bad = true
			continue
		}
		if string(tf.Base) == string(tf.Head) {
			continue
		}
		if !isGoFile(tf) || (mustBeIdentical != nil && mustBeIdentical(tf.Path)) {
			bad = true
			continue
		}
		base, err := parseGoFileShape(tf.Path, tf.Base)
		if err != nil {
			return false, err
		}
		head, err := parseGoFileShape(tf.Path, tf.Head)
		if err != nil {
			return false, err
		}
		if !structuralOK(base, head, allowedFor(tf.Path), false) {
			bad = true
		}
	}
	return bad, nil
}

// ruleR12: every previous manifest test is still declared and present, and every previous manifest file keeps its
// header, imports and declarations byte for byte (A is empty): only whole new declarations, plain imports and files
// are added. A changed previous file that is not Go, or a deleted one, is a violation.
func ruleR12(in GenerationInput, f *redFacts) ([]reasonGroup, error) {
	bad := false
	for _, t := range in.Previous.Tests {
		if obs, ok := f.reported[t.ID]; ok && obs.File == "" {
			return nil, infraErr("previous manifest test %q cannot be resolved to a file", t.ID)
		}
		if !f.headPresent[t.ID] || !f.declaredResolved[t.ID] {
			bad = true
		}
	}
	fb, err := prevFileJudgment(in, func(string) map[string]bool { return nil }, nil)
	if err != nil {
		return nil, err
	}
	if bad || fb {
		return []reasonGroup{{outcome: outcomeRedInvalid, reasons: []string{"manifest_not_superset"}}}, nil
	}
	return nil, nil
}

// ruleR13: every previous manifest test not mapped to an approved criterion is present, and every previous manifest
// file keeps its header, imports and declarations, except the functions whose every previous test is an approved
// one, identified by (file, function). A changed file that holds no such function stays byte-identical.
func ruleR13(in GenerationInput, f *redFacts) ([]reasonGroup, error) {
	approved := map[string]bool{}
	for _, ac := range in.ApprovedACIDs {
		approved[ac] = true
	}
	type fnRef struct{ file, fn string }
	hasApproved, hasOther := map[fnRef]bool{}, map[fnRef]bool{}
	bad := false
	for _, t := range in.Previous.Tests {
		isApproved := false
		for _, ac := range t.ACIDs {
			isApproved = isApproved || approved[ac]
		}
		if !isApproved && !f.headPresent[t.ID] {
			bad = true
		}
		obs, ok := f.reported[t.ID]
		if !ok && !f.headPresent[t.ID] {
			continue // absent: no file to resolve
		}
		if !ok || obs.File == "" {
			return nil, infraErr("previous manifest test %q cannot be resolved to a file", t.ID)
		}
		ref := fnRef{obs.File, funcNameOfTest(t.ID)}
		if isApproved {
			hasApproved[ref] = true
		} else {
			hasOther[ref] = true
		}
	}
	allowedIn := map[string]map[string]bool{}
	for r := range hasApproved {
		if !hasOther[r] {
			if allowedIn[r.file] == nil {
				allowedIn[r.file] = map[string]bool{}
			}
			allowedIn[r.file][r.fn] = true
		}
	}
	fb, err := prevFileJudgment(in, func(p string) map[string]bool { return allowedIn[p] },
		func(p string) bool { return len(allowedIn[p]) == 0 })
	if err != nil {
		return nil, err
	}
	if bad || fb {
		return []reasonGroup{{outcome: outcomeRedInvalid, reasons: []string{"unapproved_test_change"}}}, nil
	}
	return nil, nil
}
