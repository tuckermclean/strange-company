package verify

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"regexp"
	"strconv"
	"strings"
)

// goResidual makes the Go stub check fail closed (decisions D10, D11): every added head line that is not blank,
// comment-only, the package clause, a plain import or a type declaration, and not inside a function or value
// declaration (which has its own fact), is code the stub forms do not allow. Each run of such lines becomes one
// malformed fact. A type that exists at base gains no added line except a marked struct field (decision D13 B2).
func goResidual(p StubPolicy, f StubFile, head []stubCandidate, lines []string, added []bool) ([]lineFact, error) {
	n := len(lines)
	owned := make([]bool, n) // lines of a declaration: judged by its own fact (new) or by additions-only (existing)
	for _, c := range head {
		for ln := c.declLine; ln <= c.endLine && ln <= n; ln++ {
			owned[ln-1] = true
		}
	}
	baseTypes, err := goTypeNames(f.Path, f.Base)
	if err != nil {
		return nil, fmt.Errorf("base: %w", err)
	}
	benign, err := goBenign(p, f.Path, f.Head, n, baseTypes, added)
	if err != nil {
		return nil, err
	}
	var facts []lineFact
	start := -1
	flush := func() {
		if start >= 0 {
			facts = append(facts, lineFact{line: start + 1, f: StubFact{
				Path: f.Path, Symbol: fmt.Sprintf("(added code @%d)", start+1), Shape: "unknown", WellFormed: false,
			}})
			start = -1
		}
	}
	for k := 0; k < n; k++ {
		if added[k] && !benign[k] && !owned[k] && strings.TrimSpace(lines[k]) != "" {
			if start < 0 {
				start = k
			}
			continue
		}
		flush()
	}
	flush()
	return facts, nil
}

// goDirectiveRe matches the comments the Go toolchain reads as instructions: //go: directives (linkname, embed,
// build, generate), //line, cgo //export, gccgo //extern and legacy // +build constraints.
var goDirectiveRe = regexp.MustCompile(`^(?://go:|//line |/\*line |//export |//extern |//\s*\+build)`)

// goBenign marks (0-based) the lines of a Go file that carry no behavior: the package clause, plain imports, type
// declarations that shadow no predeclared identifier, and lines holding nothing but comment text (directives are
// code). Imports that run code — `_` and `.` imports and `import "C"` with its cgo preamble — are marked code last,
// after the comment pass, and so are the added lines of a type spec whose name exists at base (decision D13 B2): only
// a new spec is a pure type declaration; an existing one may gain nothing but marked struct fields (goFieldStubs).
func goBenign(p StubPolicy, path string, src []byte, n int, baseTypes map[string]bool, added []bool) ([]bool, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, src, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	tf := fset.File(file.Pos())
	benign := make([]bool, n)
	mark := func(from, to token.Pos, v bool) {
		for ln := tf.Line(from); ln <= tf.Line(to) && ln <= n; ln++ {
			benign[ln-1] = v
		}
	}
	mark(file.Package, file.Name.End(), true)
	type span struct{ from, to token.Pos }
	var code []span
	var codeLines []int // 1-based
	for _, d := range file.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok {
			continue
		}
		switch gd.Tok {
		case token.TYPE:
			shadows := false
			for _, sp := range gd.Specs {
				if ts, ok := sp.(*ast.TypeSpec); ok && goPredeclared[ts.Name.Name] {
					shadows = true
				}
			}
			if !shadows {
				mark(gd.Pos(), gd.End(), true)
			}
			for _, sp := range gd.Specs {
				ts, ok := sp.(*ast.TypeSpec)
				if !ok || !baseTypes[ts.Name.Name] {
					continue
				}
				from, to := tf.Line(ts.Pos()), tf.Line(ts.End())
				if !gd.Lparen.IsValid() {
					from = tf.Line(gd.Pos())
				}
				fields := goFieldStubs(p, tf, ts, added)
				for ln := from; ln <= to && ln <= n; ln++ {
					if added[ln-1] && !fields[ln] {
						codeLines = append(codeLines, ln)
					}
				}
			}
		case token.IMPORT:
			mark(gd.Pos(), gd.End(), true)
			for _, sp := range gd.Specs {
				is, ok := sp.(*ast.ImportSpec)
				if !ok {
					continue
				}
				p, _ := strconv.Unquote(is.Path.Value)
				if is.Name == nil && p != "C" || is.Name != nil && is.Name.Name != "_" && is.Name.Name != "." {
					continue
				}
				code = append(code, span{is.Pos(), is.End()})
				if is.Doc != nil {
					code = append(code, span{is.Doc.Pos(), is.Doc.End()})
				}
				if gd.Doc != nil {
					code = append(code, span{gd.Doc.Pos(), gd.Doc.End()})
				}
			}
		}
	}
	// A line is comment-only when every non-blank byte lies in a (non-directive) comment.
	inComment := make([]bool, len(src))
	for _, cg := range file.Comments {
		for _, c := range cg.List {
			if goDirectiveRe.MatchString(c.Text) {
				continue
			}
			for o := tf.Offset(c.Pos()); o < tf.Offset(c.End()) && o < len(src); o++ {
				inComment[o] = true
			}
		}
	}
	ln, onlyComment, sawComment := 0, true, false
	for o := 0; o <= len(src); o++ {
		if o == len(src) || src[o] == '\n' {
			if ln < n && onlyComment && sawComment {
				benign[ln] = true
			}
			ln, onlyComment, sawComment = ln+1, true, false
			continue
		}
		switch {
		case inComment[o]:
			sawComment = true
		case src[o] != ' ' && src[o] != '\t' && src[o] != '\r':
			onlyComment = false
		}
	}
	for _, s := range code {
		mark(s.from, s.to, false)
	}
	for _, ln := range codeLines {
		benign[ln-1] = false
	}
	return benign, nil
}

// goFieldStubs returns the lines (1-based) of the field stubs added to an existing struct type spec (decision D13 B2,
// the phase-1 rule of CD-59 without tags): a named field without a tag (its type holds no tagged field either) whose
// doc comment ends with the marker line (`// <marker>(<task-id> AC-n...)`), every line of it and of its doc comment
// added. Embedded fields, tagged fields and interface methods are never field stubs, and a line shared with any
// other field is not a field stub line.
func goFieldStubs(p StubPolicy, tf *token.File, ts *ast.TypeSpec, added []bool) map[int]bool {
	out := map[int]bool{}
	st, ok := ts.Type.(*ast.StructType)
	if !ok || st.Fields == nil {
		return out
	}
	isAdded := func(ln int) bool { return ln >= 1 && ln <= len(added) && added[ln-1] }
	other := map[int]bool{}
	for _, fld := range st.Fields.List {
		from, to := tf.Line(fld.Pos()), tf.Line(fld.End())
		if fld.Doc != nil {
			from = tf.Line(fld.Doc.Pos())
		}
		ok := len(fld.Names) > 0 && fld.Tag == nil && fld.Doc != nil && len(fld.Doc.List) > 0 && !goHasTag(fld.Type)
		if ok {
			last := fld.Doc.List[len(fld.Doc.List)-1].Text
			ok = strings.HasPrefix(last, "//") && markerFormat(p).MatchString(strings.TrimSpace(strings.TrimPrefix(last, "//")))
		}
		for ln := from; ok && ln <= to; ln++ {
			ok = isAdded(ln)
		}
		for ln := from; ln <= to; ln++ {
			if ok {
				out[ln] = true
			} else {
				other[ln] = true
			}
		}
	}
	for ln := range other {
		delete(out, ln)
	}
	return out
}

// goHasTag reports whether a field type declares a tagged field (an inline struct type).
func goHasTag(t ast.Expr) bool {
	found := false
	ast.Inspect(t, func(n ast.Node) bool {
		if f, ok := n.(*ast.Field); ok && f.Tag != nil {
			found = true
		}
		return !found
	})
	return found
}

// goTypeNames returns the names of the type specs of a Go source (none for a new file).
func goTypeNames(path string, src []byte) (map[string]bool, error) {
	names := map[string]bool{}
	if src == nil {
		return names, nil
	}
	file, err := parser.ParseFile(token.NewFileSet(), path, src, parser.SkipObjectResolution)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	for _, d := range file.Decls {
		if gd, ok := d.(*ast.GenDecl); ok && gd.Tok == token.TYPE {
			for _, sp := range gd.Specs {
				if ts, ok := sp.(*ast.TypeSpec); ok {
					names[ts.Name.Name] = true
				}
			}
		}
	}
	return names, nil
}
