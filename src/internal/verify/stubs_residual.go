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
// malformed fact.
func goResidual(f StubFile, head []stubCandidate, lines []string, added []bool) ([]lineFact, error) {
	n := len(lines)
	owned := make([]bool, n) // lines of a declaration: judged by its own fact (new) or by additions-only (existing)
	for _, c := range head {
		for ln := c.declLine; ln <= c.endLine && ln <= n; ln++ {
			owned[ln-1] = true
		}
	}
	benign, err := goBenign(f.Path, f.Head, n)
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
// after the comment pass.
func goBenign(path string, src []byte, n int) ([]bool, error) {
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
	return benign, nil
}
