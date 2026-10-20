package verify

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// LoadStubPolicy reads spec.stubPolicy of the platform verification policy (plan 22 section 14.2). Missing or
// ill-typed values are errors (22-R-3).
func LoadStubPolicy(verification map[string]any) (StubPolicy, error) {
	m, ok := pathMap(verification, "spec", "stubPolicy")
	if !ok {
		return StubPolicy{}, errors.New("verification policy has no spec.stubPolicy")
	}
	var sp StubPolicy
	sp.Marker, _ = m["marker"].(string)
	sp.PanicMessage, _ = m["panicMessage"].(string)
	var ok1, ok2 bool
	sp.MaxAddedLinesPerStub, ok1 = intValue(m["maxAddedLinesPerStub"])
	sp.MaxStubsPerTask, ok2 = intValue(m["maxStubsPerTask"])
	if sp.Marker == "" || sp.PanicMessage == "" || !ok1 || !ok2 || sp.MaxAddedLinesPerStub < 1 || sp.MaxStubsPerTask < 1 {
		return StubPolicy{}, errors.New("spec.stubPolicy: marker, panicMessage, maxAddedLinesPerStub and maxStubsPerTask are required")
	}
	langs, err := stringList(m["allowPanicStubsFor"])
	if err != nil {
		return StubPolicy{}, fmt.Errorf("spec.stubPolicy.allowPanicStubsFor: %w", err)
	}
	sp.AllowPanicStubsFor = langs
	return sp, nil
}

// stubCandidate is a top-level Go function or value declaration.
type stubCandidate struct {
	symbol     string
	key        string // base matching key: the symbol, "_" for blank names
	declLine   int    // first line of the declaration (1-based), after any doc comment
	endLine    int
	bodyLines  int
	shape      string
	commentTok string // line comment token of the language
	nonFunc    bool   // a variable, constant or assignment: never a well-formed stub (plan 22 section 14.2)
	reserved   bool   // a function that cannot be a stub: init, _, or a predeclared identifier it would shadow
	existing   bool   // the declaration is unchanged from base
}

// stubLanguages are the languages with a stub form: Go by the AST, the others by canonical line layouts.
var stubLanguages = map[string]bool{"go": true, "python": true, "typescript": true, "javascript": true, "rust": true}

// AnalyzeStubs derives the stub facts of one stub-path file (plan 22 section 14.2): every declaration that is new
// in Head is a stub candidate; it is well formed when the marker comment is on the line right before it, the body
// is zero-value or the panic form and not longer than maxAddedLinesPerStub, and the file change is additions only.
// Every other added line must carry no behavior (decisions D10, D11): Go is analysed through the AST, the other
// languages through an allowlist of canonical line layouts (stubs_lines.go); anything else is a malformed fact.
// A file that cannot be analysed (parse error, language without a stub form, a line terminator other than \n or
// \r\n) is an error, never an empty result (22-R-3). Facts are ordered by head line.
func AnalyzeStubs(p StubPolicy, f StubFile) ([]StubFact, error) {
	if !stubLanguages[f.Language] {
		return nil, fmt.Errorf("%s: language %q has no stub form", f.Path, f.Language)
	}
	if err := checkSourceText(f.Path, f.Head); err != nil {
		return nil, err
	}
	var facts []lineFact
	var additionsOnly bool
	var err error
	if f.Language == "go" {
		facts, additionsOnly, err = analyzeGo(p, f)
	} else {
		facts, additionsOnly, err = analyzeLines(p, f)
	}
	if err != nil {
		return nil, err
	}
	if len(facts) == 0 && !additionsOnly {
		// The file changed other than by additions and holds no new declaration: not a stub.
		facts = append(facts, lineFact{f: StubFact{Path: f.Path, Symbol: "(modified existing code)", Shape: "unknown"}})
	}
	sort.SliceStable(facts, func(i, j int) bool {
		if facts[i].line != facts[j].line {
			return facts[i].line < facts[j].line
		}
		return facts[i].f.Symbol < facts[j].f.Symbol
	})
	out := make([]StubFact, len(facts))
	for i, lf := range facts {
		out[i] = lf.f
	}
	return out, nil
}

// checkSourceText rejects text whose line structure a language could read differently from the stub check
// (decision D11(a)): a line terminator other than \n or \r\n (bare \r, U+2028, U+2029, NEL, form feed, vertical
// tab), any other control character except tab, and invalid UTF-8.
func checkSourceText(path string, b []byte) error {
	if !utf8.Valid(b) {
		return fmt.Errorf("%s: not valid UTF-8", path)
	}
	s := string(b)
	for i, r := range s {
		switch {
		case r == '\n' || r == '\t':
		case r == '\r':
			if i+1 >= len(s) || s[i+1] != '\n' {
				return fmt.Errorf("%s: bare carriage return at byte %d", path, i)
			}
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) || r == 0x2028 || r == 0x2029:
			return fmt.Errorf("%s: line terminator or control character %U at byte %d", path, r, i)
		}
	}
	return nil
}

// splitLines splits source text into lines without their "\n" or "\r\n" terminator.
func splitLines(b []byte) []string {
	if len(b) == 0 {
		return nil
	}
	lines := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSuffix(l, "\r")
	}
	return lines
}

// subsequence reports whether every base line appears in head in order.
func subsequence(base, head []string) bool {
	i := 0
	for _, h := range head {
		if i < len(base) && base[i] == h {
			i++
		}
	}
	return i == len(base)
}

// addedLines marks the head lines (0-based) not matched by the greedy in-order alignment of base; every line when
// the file is new.
func addedLines(base, head []string, newFile bool) []bool {
	out := make([]bool, len(head))
	i := 0
	for n, h := range head {
		if !newFile && i < len(base) && base[i] == h {
			i++
			continue
		}
		out[n] = true
	}
	return out
}

// markerBefore returns the marker comment text on the line immediately before declLine ("" when that line is not
// a marker comment).
func markerBefore(p StubPolicy, commentTok string, lines []string, declLine int) string {
	if declLine < 2 || declLine-2 >= len(lines) {
		return ""
	}
	s := strings.TrimSpace(lines[declLine-2])
	if !strings.HasPrefix(s, commentTok) {
		return ""
	}
	s = strings.TrimSpace(strings.TrimPrefix(s, commentTok))
	if !strings.HasPrefix(s, p.Marker) {
		return ""
	}
	return s
}

// markerFormat is `<marker>(<task-id> AC-n[ AC-m...])`.
func markerFormat(p StubPolicy) *regexp.Regexp {
	return regexp.MustCompile(`^` + regexp.QuoteMeta(p.Marker) + `\([A-Za-z0-9._-]+( AC-[1-9][0-9]{0,2})+\)$`)
}

// goPredeclared are Go's predeclared identifiers: a package-level function or type with such a name would shadow
// it for every existing file of the package.
var goPredeclared = toSet(`any append bool byte cap clear close comparable complex complex128 complex64 copy delete
error false float32 float64 imag int int16 int32 int64 int8 iota len make max min new nil panic print println real
recover rune string true uint uint16 uint32 uint64 uint8 uintptr`)

// analyzeGo derives the facts of a Go file through the AST.
func analyzeGo(p StubPolicy, f StubFile) ([]lineFact, bool, error) {
	head, err := goCandidates(p, f.Path, f.Head)
	if err != nil {
		return nil, false, err
	}
	baseKeys := map[string]bool{}
	if f.Base != nil {
		base, err := goCandidates(p, f.Path, f.Base)
		if err != nil {
			return nil, false, fmt.Errorf("base: %w", err)
		}
		for _, b := range base {
			baseKeys[b.key] = true
		}
	}
	headLines, baseLines := splitLines(f.Head), splitLines(f.Base)
	added := addedLines(baseLines, headLines, f.Base == nil)
	additionsOnly := f.Base == nil || subsequence(baseLines, headLines)
	for i := range head {
		h := &head[i]
		// Unchanged: its first line is matched to base and base declares the symbol. A second `func init` with an
		// added first line is new even though base has an init.
		h.existing = f.Base != nil && !added[h.declLine-1] && baseKeys[h.key]
		for ln := h.declLine; h.existing && ln <= h.endLine && ln <= len(added); ln++ {
			if added[ln-1] {
				additionsOnly = false // an added line inside an existing declaration is a modification
			}
		}
	}
	var facts []lineFact
	for _, h := range head {
		if h.existing {
			continue
		}
		marker := markerBefore(p, h.commentTok, headLines, h.declLine)
		wf := !h.nonFunc && !h.reserved && additionsOnly && marker != "" && markerFormat(p).MatchString(marker) &&
			h.shape != "unknown" && h.bodyLines <= p.MaxAddedLinesPerStub
		facts = append(facts, lineFact{line: h.declLine, f: StubFact{Path: f.Path, Symbol: h.symbol, Marker: marker, Shape: h.shape, WellFormed: wf}})
	}
	residual, err := goResidual(f, head, headLines, added)
	if err != nil {
		return nil, false, err
	}
	return append(facts, residual...), additionsOnly, nil
}

// ---- Go (AST) ----

func goCandidates(p StubPolicy, path string, src []byte) ([]stubCandidate, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, src, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	var out []stubCandidate
	for _, d := range file.Decls {
		if gd, ok := d.(*ast.GenDecl); ok && (gd.Tok == token.VAR || gd.Tok == token.CONST) {
			for _, sp := range gd.Specs {
				vs, ok := sp.(*ast.ValueSpec)
				if !ok {
					continue
				}
				line := fset.Position(vs.Pos()).Line
				name, key := "_@"+strconv.Itoa(line), "_"
				if len(vs.Names) > 0 && vs.Names[0].Name != "_" {
					name, key = vs.Names[0].Name, vs.Names[0].Name
				}
				out = append(out, stubCandidate{
					symbol: name, key: key, declLine: line, endLine: fset.Position(vs.End()).Line,
					bodyLines: 1, shape: "unknown", commentTok: "//", nonFunc: true,
				})
			}
			continue
		}
		fn, ok := d.(*ast.FuncDecl)
		if !ok {
			continue
		}
		c := stubCandidate{symbol: goSymbol(fn), commentTok: "//", shape: "unknown"}
		c.key = c.symbol
		c.reserved = fn.Recv == nil && (fn.Name.Name == "init" || fn.Name.Name == "_" || goPredeclared[fn.Name.Name])
		c.declLine = fset.Position(fn.Pos()).Line
		c.endLine = fset.Position(fn.End()).Line
		if fn.Body != nil {
			open, closeLn := fset.Position(fn.Body.Lbrace).Line, fset.Position(fn.Body.Rbrace).Line
			c.bodyLines = closeLn - open - 1
			if closeLn == open {
				c.bodyLines = 1
			}
			c.shape = goShape(p, fn.Body)
		}
		out = append(out, c)
	}
	return out, nil
}

func goSymbol(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return fn.Name.Name
	}
	t := fn.Recv.List[0].Type
	for {
		switch x := t.(type) {
		case *ast.StarExpr:
			t = x.X
			continue
		case *ast.ParenExpr:
			t = x.X
			continue
		case *ast.IndexExpr:
			t = x.X
			continue
		case *ast.IndexListExpr:
			t = x.X
			continue
		case *ast.Ident:
			return x.Name + "." + fn.Name.Name
		}
		return "?." + fn.Name.Name
	}
}

func goShape(p StubPolicy, body *ast.BlockStmt) string {
	if len(body.List) == 0 {
		return "zero_value"
	}
	if len(body.List) != 1 {
		return "unknown"
	}
	switch s := body.List[0].(type) {
	case *ast.ReturnStmt:
		for _, r := range s.Results {
			if !goZero(r) {
				return "unknown"
			}
		}
		return "zero_value"
	case *ast.ExprStmt:
		call, ok := s.X.(*ast.CallExpr)
		if !ok || len(call.Args) != 1 {
			return "unknown"
		}
		if id, ok := call.Fun.(*ast.Ident); !ok || id.Name != "panic" {
			return "unknown"
		}
		if lit, ok := call.Args[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
			if v, err := strconv.Unquote(lit.Value); err == nil && v == p.PanicMessage {
				return "panic"
			}
		}
	}
	return "unknown"
}

func goZero(e ast.Expr) bool {
	switch x := e.(type) {
	case *ast.Ident:
		return x.Name == "nil" || x.Name == "false"
	case *ast.BasicLit:
		switch x.Kind {
		case token.STRING:
			v, err := strconv.Unquote(x.Value)
			return err == nil && v == ""
		case token.INT:
			v, err := strconv.ParseInt(strings.ReplaceAll(x.Value, "_", ""), 0, 64)
			return err == nil && v == 0
		case token.FLOAT:
			v, err := strconv.ParseFloat(strings.ReplaceAll(x.Value, "_", ""), 64)
			return err == nil && v == 0
		}
	case *ast.CompositeLit:
		return len(x.Elts) == 0
	case *ast.ParenExpr:
		return goZero(x.X)
	}
	return false
}
