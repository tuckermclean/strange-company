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

// stubCandidate is a function-like declaration of a source file.
type stubCandidate struct {
	symbol     string
	declLine   int // first line of the declaration (1-based), after any doc comment
	endLine    int
	bodyLines  int
	shape      string
	commentTok string // line comment token of the language
}

// AnalyzeStubs derives the stub facts of one stub-path file (plan 22 section 14.2): every declaration that is new
// in Head is a stub candidate; it is well formed when the marker comment is on the line right before it, the body
// is zero-value or the panic form and not longer than maxAddedLinesPerStub, and the file change is additions only.
// Go is analysed through the AST, the other languages through line patterns. A file that cannot be analysed (parse
// error, language without a stub form) is an error, never an empty result (22-R-3).
func AnalyzeStubs(p StubPolicy, f StubFile) ([]StubFact, error) {
	lang, ok := stubLangs[f.Language]
	if !ok {
		return nil, fmt.Errorf("%s: language %q has no stub form", f.Path, f.Language)
	}
	var head, base []stubCandidate
	var err error
	if f.Language == "go" {
		if head, err = goCandidates(p, f.Path, f.Head); err != nil {
			return nil, err
		}
		if f.Base != nil {
			if base, err = goCandidates(p, f.Path, f.Base); err != nil {
				return nil, fmt.Errorf("base: %w", err)
			}
		}
	} else {
		if head, err = lineCandidates(p, lang, f.Path, f.Head); err != nil {
			return nil, err
		}
		if f.Base != nil {
			if base, err = lineCandidates(p, lang, f.Path, f.Base); err != nil {
				return nil, fmt.Errorf("base: %w", err)
			}
		}
	}
	baseSyms := map[string]bool{}
	for _, b := range base {
		baseSyms[b.symbol] = true
	}
	headLines := splitLines(f.Head)
	additionsOnly := true
	if f.Base != nil {
		unmatched := unmatchedLines(splitLines(f.Base), headLines)
		// An added line inside a declaration that already exists is a modification, not a stub.
		for _, h := range head {
			if !baseSyms[h.symbol] {
				continue
			}
			for ln := h.declLine; ln <= h.endLine; ln++ {
				if unmatched[ln] {
					additionsOnly = false
				}
			}
		}
		if !subsequence(splitLines(f.Base), headLines) {
			additionsOnly = false
		}
	}
	var facts []StubFact
	for _, h := range head {
		if baseSyms[h.symbol] {
			continue
		}
		marker := markerBefore(p, h.commentTok, headLines, h.declLine)
		wf := additionsOnly && marker != "" && markerFormat(p).MatchString(marker) &&
			h.shape != "unknown" && h.bodyLines <= p.MaxAddedLinesPerStub
		facts = append(facts, StubFact{Path: f.Path, Symbol: h.symbol, Marker: marker, Shape: h.shape, WellFormed: wf})
	}
	if len(facts) == 0 && !additionsOnly {
		// The file changed other than by additions and holds no new declaration: not a stub.
		facts = append(facts, StubFact{Path: f.Path, Symbol: "(modified existing code)", Shape: "unknown", WellFormed: false})
	}
	sort.SliceStable(facts, func(i, j int) bool { return facts[i].Symbol < facts[j].Symbol })
	return facts, nil
}

func splitLines(b []byte) []string {
	if len(b) == 0 {
		return nil
	}
	s := strings.TrimSuffix(string(b), "\n")
	return strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
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

// unmatchedLines returns the 1-based head lines not matched by the greedy in-order alignment of base.
func unmatchedLines(base, head []string) map[int]bool {
	out := map[int]bool{}
	i := 0
	for n, h := range head {
		if i < len(base) && base[i] == h {
			i++
			continue
		}
		out[n+1] = true
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

// ---- Go (AST) ----

func goCandidates(p StubPolicy, path string, src []byte) ([]stubCandidate, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, src, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	var out []stubCandidate
	for _, d := range file.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok {
			continue
		}
		c := stubCandidate{symbol: goSymbol(fn), commentTok: "//", shape: "unknown"}
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

// ---- other languages (line patterns) ----

type stubLang struct {
	comment   string
	indent    bool // block by indentation (Python) instead of braces
	fnRe      *regexp.Regexp
	zeroRe    *regexp.Regexp
	panicTmpl string // %s = quoted-regexp of the panic message
}

var stubLangs = map[string]stubLang{
	"go": {comment: "//"},
	"rust": {
		comment:   "//",
		fnRe:      regexp.MustCompile(`^\s*(?:pub(?:\([^)]*\))?\s+)?(?:(?:async|const|unsafe)\s+)*fn\s+([A-Za-z_][A-Za-z0-9_]*)`),
		zeroRe:    regexp.MustCompile(`^(?:return\s+)?(?:Default::default\(\)|None|\(\)|0|0\.0|false|""|String::new\(\)|Vec::new\(\)|vec!\[\]|Ok\(\(\)\)|Ok\(Default::default\(\)\))\s*;?$`),
		panicTmpl: `^(?:todo|unimplemented|panic)!\(\s*"%s"\s*\)\s*;?$`,
	},
	"typescript": tsLang(),
	"javascript": tsLang(),
	"python": {
		comment:   "#",
		indent:    true,
		fnRe:      regexp.MustCompile(`^\s*(?:async\s+)?def\s+([A-Za-z_][A-Za-z0-9_]*)\s*\(`),
		zeroRe:    regexp.MustCompile(`^(?:return(?:\s+(?:None|0|0\.0|False|""|''|\[\]|\{\}|\(\)))?|pass|\.\.\.)$`),
		panicTmpl: `^raise\s+NotImplementedError\(\s*["']%s["']\s*\)$`,
	},
}

func tsLang() stubLang {
	return stubLang{
		comment:   "//",
		fnRe:      regexp.MustCompile(`^\s*(?:export\s+)?(?:default\s+)?(?:async\s+)?function\s*\*?\s*([A-Za-z_$][A-Za-z0-9_$]*)`),
		zeroRe:    regexp.MustCompile(`^return(?:\s+(?:null|undefined|0|false|""|''|\[\]|\{\}))?\s*;?$`),
		panicTmpl: `^throw\s+new\s+Error\(\s*["']%s["']\s*\)\s*;?$`,
	}
}

func lineCandidates(p StubPolicy, lang stubLang, path string, src []byte) ([]stubCandidate, error) {
	lines := splitLines(src)
	panicRe, err := regexp.Compile(fmt.Sprintf(lang.panicTmpl, regexp.QuoteMeta(p.PanicMessage)))
	if err != nil {
		return nil, err
	}
	var out []stubCandidate
	for i := 0; i < len(lines); i++ {
		m := lang.fnRe.FindStringSubmatch(lines[i])
		if m == nil {
			continue
		}
		c := stubCandidate{symbol: m[1], declLine: i + 1, commentTok: lang.comment}
		var inner []string
		end := i
		if lang.indent {
			end, inner = pythonBody(lines, i)
		} else {
			var err error
			end, inner, err = braceBody(lines, i)
			if err != nil {
				return nil, fmt.Errorf("%s:%d: %w", path, i+1, err)
			}
		}
		c.endLine = end + 1
		c.bodyLines = len(inner)
		if c.bodyLines == 0 {
			c.bodyLines = 1
		}
		c.shape = "unknown"
		switch {
		case len(inner) == 0:
			c.shape = "zero_value"
		case len(inner) == 1 && panicRe.MatchString(inner[0]):
			c.shape = "panic"
		case len(inner) == 1 && lang.zeroRe.MatchString(inner[0]):
			c.shape = "zero_value"
		}
		out = append(out, c)
		i = end
	}
	return out, nil
}

// braceBody finds the body of a brace-delimited function starting on line start: the index of the closing line and
// the trimmed non-empty inner statements (the text after the opening brace and before the closing one included).
func braceBody(lines []string, start int) (int, []string, error) {
	depth := 0
	opened := false
	var inner []string
	for i := start; i < len(lines); i++ {
		text := lines[i]
		var cur strings.Builder
		for _, r := range text {
			switch r {
			case '{':
				depth++
				if depth == 1 && !opened {
					opened = true
					cur.Reset()
					continue
				}
			case '}':
				depth--
				if depth == 0 && opened {
					if s := strings.TrimSpace(cur.String()); s != "" {
						inner = append(inner, s)
					}
					return i, inner, nil
				}
			}
			if opened {
				cur.WriteRune(r)
			}
		}
		if opened {
			if s := strings.TrimSpace(cur.String()); s != "" {
				inner = append(inner, s)
			}
		} else if strings.HasSuffix(strings.TrimSpace(text), ";") {
			return 0, nil, errors.New("function declaration without a body")
		}
	}
	return 0, nil, errors.New("unterminated function body")
}

// pythonBody returns the end line index and the trimmed non-empty body lines of a def starting on line start.
func pythonBody(lines []string, start int) (int, []string) {
	indentOf := func(s string) int { return len(s) - len(strings.TrimLeft(s, " \t")) }
	base := indentOf(lines[start])
	end := start
	var inner []string
	for i := start + 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "" {
			continue
		}
		if indentOf(lines[i]) <= base {
			break
		}
		end = i
		s := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(s, "#") {
			inner = append(inner, s)
		}
	}
	return end, inner
}
