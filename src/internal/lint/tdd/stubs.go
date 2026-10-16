package tdd

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"regexp"
	"strconv"
	"strings"
)

const maxStubBodyLines = 12

var (
	stubMarkerRe = regexp.MustCompile(`^//\s*SC-STUB\(\s*\S[^)]*\)\s*$`)
	// anyMarkerRe finds a marker comment, wherever it sits in a Go file.
	anyMarkerRe = regexp.MustCompile(`^//\s*SC-STUB\(`)
	hunkRe      = regexp.MustCompile(`^@@ -\d+(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)
)

// stubViolations judges the change of one non-test behavior-path file in RED commit sha against 8.3a.
func (c *checker) stubViolations(sha string, f change) []string {
	if !strings.HasSuffix(f.path, ".go") {
		return []string{fmt.Sprintf("%s: a non-Go behavior path has no stub form", f.path)}
	}
	if !strings.HasPrefix(f.path, "src/") {
		return []string{fmt.Sprintf("%s: declared stubs are allowed under src/ only", f.path)}
	}
	if f.status == 'D' {
		return []string{fmt.Sprintf("%s: a RED commit may not delete a behavior file", f.path)}
	}
	if f.status != 'A' && f.status != 'M' {
		return []string{fmt.Sprintf("%s: unsupported change %c in a RED commit", f.path, f.status)}
	}
	src, err := c.show(sha, f.path)
	if err != nil {
		return []string{err.Error()}
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, f.path, src, parser.ParseComments)
	if err != nil {
		return []string{fmt.Sprintf("%s: does not parse: %v", f.path, err)}
	}
	var added map[int]bool
	if f.status == 'M' {
		if added, err = c.addedLines(sha, f.path); err != nil {
			return []string{fmt.Sprintf("%s: %v", f.path, err)}
		}
	}
	var bad []string
	for _, d := range file.Decls {
		start := fset.Position(d.Pos()).Line
		if doc := declDoc(d); doc != nil {
			start = fset.Position(doc.Pos()).Line
		}
		end := fset.Position(d.End()).Line
		if f.status == 'M' {
			n := 0
			for l := start; l <= end; l++ {
				if added[l] {
					n++
				}
			}
			if n == 0 {
				continue
			}
			if gd, ok := d.(*ast.GenDecl); ok && gd.Tok == token.IMPORT {
				continue
			}
			if n < end-start+1 {
				bad = append(bad, fmt.Sprintf("%s:%d: the commit changes an existing declaration", f.path, start))
				continue
			}
		}
		bad = append(bad, declViolations(fset, f.path, d)...)
	}
	return bad
}

func declDoc(d ast.Decl) *ast.CommentGroup {
	switch x := d.(type) {
	case *ast.FuncDecl:
		return x.Doc
	case *ast.GenDecl:
		return x.Doc
	}
	return nil
}

// declViolations validates one added top-level declaration: imports and type declarations are fine, a
// function must be a declared stub, anything else (var, const) is not allowed.
func declViolations(fset *token.FileSet, path string, d ast.Decl) []string {
	pos := fset.Position(d.Pos())
	where := fmt.Sprintf("%s:%d", path, pos.Line)
	switch x := d.(type) {
	case *ast.GenDecl:
		switch x.Tok {
		case token.IMPORT, token.TYPE:
			return nil
		}
		return []string{fmt.Sprintf("%s: a %s declaration is not allowed in a RED commit", where, x.Tok)}
	case *ast.FuncDecl:
		var bad []string
		if x.Doc == nil || len(x.Doc.List) == 0 || !stubMarkerRe.MatchString(x.Doc.List[len(x.Doc.List)-1].Text) {
			bad = append(bad, fmt.Sprintf("%s: func %s is not directly preceded by a // SC-STUB(<id> <AC>) marker", where, x.Name.Name))
		}
		if x.Body == nil {
			return append(bad, fmt.Sprintf("%s: func %s has no body", where, x.Name.Name))
		}
		lines := fset.Position(x.Body.Rbrace).Line - fset.Position(x.Body.Lbrace).Line + 1
		if lines > maxStubBodyLines {
			bad = append(bad, fmt.Sprintf("%s: stub %s has %d body lines (max %d)", where, x.Name.Name, lines, maxStubBodyLines))
		}
		for _, st := range x.Body.List {
			ret, ok := st.(*ast.ReturnStmt)
			if !ok {
				bad = append(bad, fmt.Sprintf("%s: stub %s contains a statement other than return", where, x.Name.Name))
				break
			}
			for _, e := range ret.Results {
				if !isZeroExpr(e) {
					bad = append(bad, fmt.Sprintf("%s: stub %s returns a non-zero value", where, x.Name.Name))
					break
				}
			}
		}
		return bad
	}
	return []string{fmt.Sprintf("%s: unsupported declaration", where)}
}

// isZeroExpr reports whether e is the zero value spelled out: 0, "", false, nil or an empty composite literal.
func isZeroExpr(e ast.Expr) bool {
	switch x := e.(type) {
	case *ast.ParenExpr:
		return isZeroExpr(x.X)
	case *ast.Ident:
		return x.Name == "nil" || x.Name == "false"
	case *ast.CompositeLit:
		return len(x.Elts) == 0
	case *ast.BasicLit:
		switch x.Kind {
		case token.INT:
			n, err := strconv.ParseInt(x.Value, 0, 64)
			return err == nil && n == 0
		case token.FLOAT:
			f, err := strconv.ParseFloat(x.Value, 64)
			return err == nil && f == 0
		case token.STRING:
			s, err := strconv.Unquote(x.Value)
			return err == nil && s == ""
		case token.CHAR:
			r, _, _, err := strconv.UnquoteChar(x.Value[1:len(x.Value)-1], '\'')
			return err == nil && r == 0
		}
	}
	return false
}

// addedLines returns the new-file line numbers added by sha to path. An error says the commit also
// modified or deleted an existing line.
func (c *checker) addedLines(sha, path string) (map[int]bool, error) {
	out, err := c.git("diff", "-U0", "--no-color", "--no-ext-diff", "--no-renames", sha+"^", sha, "--", path)
	if err != nil {
		return nil, err
	}
	added := map[int]bool{}
	for _, line := range strings.Split(out, "\n") {
		m := hunkRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		if m[1] != "0" { // old length; absent means 1
			return nil, fmt.Errorf("the commit deletes or modifies existing lines (%s)", line)
		}
		start, _ := strconv.Atoi(m[2])
		n := 1
		if m[3] != "" {
			n, _ = strconv.Atoi(m[3])
		}
		for l := start; l < start+n; l++ {
			added[l] = true
		}
	}
	return added, nil
}

// markersAtHead implements the TDD_STUB_NOT_REPLACED check: no declared-stub marker may remain in a Go file
// the PR changed.
func (c *checker) markersAtHead(all []change) error {
	for _, f := range all {
		if f.status == 'D' || !strings.HasSuffix(f.path, ".go") {
			continue
		}
		src, err := c.show(c.headSHA, f.path)
		if err != nil {
			return err
		}
		if line := markerLine(f.path, src); line > 0 {
			c.fail(CodeStubNotReplaced, c.headSHA, "%s:%d: a declared-stub marker is still present at HEAD", f.path, line)
		}
	}
	return nil
}

// markerLine returns the line of the first marker comment of a Go file, or 0.
func markerLine(path string, src []byte) int {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, src, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil { // unparsable: fall back to line-start comments
		for i, l := range strings.Split(string(src), "\n") {
			if anyMarkerRe.MatchString(strings.TrimSpace(l)) {
				return i + 1
			}
		}
		return 0
	}
	for _, g := range file.Comments {
		for _, cm := range g.List {
			if anyMarkerRe.MatchString(cm.Text) {
				return fset.Position(cm.Pos()).Line
			}
		}
	}
	return 0
}

// changedTestBodies returns "package-path.TestName" for every top-level Test function of a modified
// *_test.go file of commit sha whose body differs from its parent's (comments and layout ignored).
func (c *checker) changedTestBodies(sha string, files []change) map[string]bool {
	out := map[string]bool{}
	for _, f := range files {
		if f.status != 'M' || !strings.HasSuffix(f.path, "_test.go") {
			continue
		}
		oldSrc, err1 := c.show(sha+"^", f.path)
		newSrc, err2 := c.show(sha, f.path)
		if err1 != nil || err2 != nil {
			continue
		}
		oldBodies := testBodies(oldSrc)
		pkg := c.packagePath(sha, f.path)
		for name, body := range testBodies(newSrc) {
			if old, ok := oldBodies[name]; ok && old != body {
				out[pkg+"."+name] = true
			}
		}
	}
	return out
}

// testBodies maps the name of every top-level Test function to its normalised body text.
func testBodies(src []byte) map[string]string {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "x_test.go", src, parser.SkipObjectResolution)
	if err != nil {
		return nil
	}
	m := map[string]string{}
	for _, d := range file.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Recv != nil || fd.Body == nil || !strings.HasPrefix(fd.Name.Name, "Test") {
			continue
		}
		var buf bytes.Buffer
		cfg := printer.Config{Mode: printer.UseSpaces | printer.TabIndent, Tabwidth: 8}
		if err := cfg.Fprint(&buf, fset, fd.Body); err != nil {
			continue
		}
		m[fd.Name.Name] = buf.String()
	}
	return m
}

// packagePath is the Go import path of the directory holding file at commit sha.
func (c *checker) packagePath(sha, file string) string {
	dir := file
	for {
		i := strings.LastIndex(dir, "/")
		if i < 0 {
			dir = ""
		} else {
			dir = dir[:i]
		}
		gomod := "go.mod"
		if dir != "" {
			gomod = dir + "/go.mod"
		}
		if c.exists(sha, gomod) {
			b, err := c.show(sha, gomod)
			if err != nil {
				return ""
			}
			mod := ""
			for _, l := range strings.Split(string(b), "\n") {
				if f := strings.Fields(l); len(f) == 2 && f[0] == "module" {
					mod = strings.Trim(f[1], `"`)
					break
				}
			}
			pkgDir := strings.TrimSuffix(file[:strings.LastIndex(file, "/")+1], "/")
			rel := strings.TrimPrefix(strings.TrimPrefix(pkgDir, dir), "/")
			if rel == "" {
				return mod
			}
			return mod + "/" + rel
		}
		if dir == "" {
			return ""
		}
	}
}
