package verify

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"regexp"
	"strings"
)

// residualFacts makes the stub check fail closed (decision D10): in a stub-class file every added head line that is
// not blank, a comment, a plain import, part of a pure type declaration or inside a new function or declaration
// candidate is code the stub forms do not allow, and becomes one malformed fact per run of such lines.
func residualFacts(f StubFile, head []stubCandidate, baseSyms map[string]bool, headLines []string, unmatched map[int]bool) ([]StubFact, error) {
	n := len(headLines)
	benign := make([]bool, n+2) // 1-based
	skip := make([]bool, n+2)   // lines owned by candidates (new: judged on their own; old: judged by additionsOnly)
	for _, c := range head {
		for ln := c.declLine; ln <= c.endLine && ln <= n; ln++ {
			skip[ln] = true
		}
	}
	var err error
	if f.Language == "go" {
		err = goBenign(f.Path, f.Head, headLines, benign)
	} else {
		lineBenign(f.Language, headLines, skip, benign)
	}
	if err != nil {
		return nil, err
	}
	var facts []StubFact
	start := 0
	flush := func() {
		if start > 0 {
			facts = append(facts, StubFact{Path: f.Path, Symbol: fmt.Sprintf("(added code @%d)", start), Shape: "unknown", WellFormed: false})
			start = 0
		}
	}
	for ln := 1; ln <= n; ln++ {
		added := f.Base == nil || unmatched[ln]
		if added && !benign[ln] && !skip[ln] && strings.TrimSpace(headLines[ln-1]) != "" {
			if start == 0 {
				start = ln
			}
			continue
		}
		flush()
	}
	flush()
	return facts, nil
}

// goBenign marks the lines of a Go file that carry no behavior: the package clause, plain imports, type
// declarations, comments (except directives) and blank lines.
func goBenign(path string, src []byte, lines []string, benign []bool) error {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, src, parser.ParseComments)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	mark := func(from, to token.Pos) {
		for ln := fset.Position(from).Line; ln <= fset.Position(to).Line && ln < len(benign); ln++ {
			benign[ln] = true
		}
	}
	mark(file.Package, file.Name.End())
	for _, d := range file.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok {
			continue
		}
		switch gd.Tok {
		case token.TYPE:
			mark(gd.Pos(), gd.End())
		case token.IMPORT:
			mark(gd.Pos(), gd.End())
			for _, sp := range gd.Specs {
				if is, ok := sp.(*ast.ImportSpec); ok && is.Name != nil && (is.Name.Name == "_" || is.Name.Name == ".") {
					for ln := fset.Position(is.Pos()).Line; ln <= fset.Position(is.End()).Line; ln++ {
						benign[ln] = false
					}
					benign[fset.Position(gd.Pos()).Line] = benign[fset.Position(gd.Pos()).Line] && fset.Position(gd.Pos()).Line != fset.Position(is.Pos()).Line
				}
			}
		}
	}
	for _, cg := range file.Comments {
		for _, c := range cg.List {
			first, last := fset.Position(c.Pos()).Line, fset.Position(c.End()).Line
			if first >= len(lines)+1 || !strings.HasPrefix(strings.TrimSpace(lines[first-1]), "/") {
				continue // trailing comment of a code line
			}
			if strings.HasPrefix(c.Text, "//go:") || strings.HasPrefix(c.Text, "//export ") {
				continue
			}
			if last > first && !strings.HasSuffix(strings.TrimSpace(lines[last-1]), "*/") {
				continue
			}
			for ln := first; ln <= last && ln < len(benign); ln++ {
				benign[ln] = true
			}
		}
	}
	return nil
}

var (
	pyImportRe   = regexp.MustCompile(`^(?:import\s+\S|from\s+\S+\s+import\b)`)
	tsImportRe   = regexp.MustCompile(`^import(?:\s+|[{*"'])[^(\s]`)
	rustUseRe    = regexp.MustCompile(`^(?:pub(?:\([^)]*\))?\s+)?use\s`)
	tsTypeRe     = regexp.MustCompile(`^(?:export\s+)?(?:declare\s+)?(?:interface\s+[A-Za-z_$]|type\s+[A-Za-z_$][A-Za-z0-9_$]*\s*(?:<[^=]*>)?\s*=)`)
	rustTypeRe   = regexp.MustCompile(`^(?:pub(?:\([^)]*\))?\s+)?(?:struct|enum)\s+[A-Za-z_]`)
	rustAttrRe   = regexp.MustCompile(`^#!?\[`)
)

// lineBenign classifies the lines of a non-Go file (see residualFacts).
func lineBenign(language string, lines []string, skip, benign []bool) {
	lang := stubLangs[language]
	n := len(lines)
	for i := 0; i < n; i++ {
		ln := i + 1
		if skip[ln] {
			continue
		}
		t := strings.TrimSpace(lines[i])
		switch {
		case t == "" || strings.HasPrefix(t, lang.comment):
			benign[ln] = true
			continue
		case !lang.indent && strings.HasPrefix(t, "/*"):
			j := i
			for j < n && !strings.Contains(lines[j], "*/") {
				j++
			}
			if j < n && strings.HasSuffix(strings.TrimSpace(lines[j]), "*/") {
				for k := i; k <= j; k++ {
					benign[k+1] = true
				}
				i = j
			}
			continue
		}
		var re *regexp.Regexp
		needSemi := false
		switch language {
		case "python":
			re = pyImportRe
		case "typescript", "javascript":
			if tsImportRe.MatchString(t) {
				re = tsImportRe
			} else {
				re = tsTypeRe
			}
		case "rust":
			switch {
			case rustAttrRe.MatchString(t):
				benign[ln] = true
				continue
			case rustUseRe.MatchString(t):
				re, needSemi = rustUseRe, true
			default:
				re = rustTypeRe
			}
		}
		if re == nil || !re.MatchString(t) {
			continue
		}
		end, ok := consumeStmt(language, lines, i, needSemi)
		if !ok {
			continue
		}
		for k := i; k <= end; k++ {
			if !skip[k+1] {
				benign[k+1] = true
			}
		}
		i = end
	}
}

// consumeStmt returns the last line of the statement starting on line i, balancing brackets, skipping string
// literals and comments. ok is false when other code shares the closing line or the statement is unterminated.
func consumeStmt(language string, lines []string, i int, needSemi bool) (int, bool) {
	depth := 0
	pyStmt := language == "python"
	for j := i; j < len(lines); j++ {
		s := lines[j]
		semi := false
		var q rune
		last := ""
	scan:
		for k, r := range s {
			if q != 0 {
				if r == q && (k == 0 || s[k-1] != '\\') {
					q = 0
				}
				continue
			}
			switch r {
			case '"':
				q = r
			case '\'', '`':
				if language != "rust" {
					q = r
				}
			case '#':
				if pyStmt {
					break scan
				}
			case '/':
				if !pyStmt && strings.HasPrefix(s[k:], "//") {
					break scan
				}
			case '{', '[', '(':
				depth++
			case '}', ']', ')':
				depth--
			case ';':
				if depth <= 0 {
					semi = true
					if rest := strings.TrimSpace(s[k+1:]); rest != "" && !strings.HasPrefix(rest, "//") && !(pyStmt && strings.HasPrefix(rest, "#")) {
						return 0, false
					}
					break scan
				}
			}
			last = strings.TrimSpace(s[:k+1])
		}
		if pyStmt && semi {
			return 0, false
		}
		if depth > 0 {
			continue
		}
		if semi {
			return j, true
		}
		if needSemi {
			continue
		}
		// code after the closing brace of a block statement
		if j+1 < len(lines) && continuesStmt(language, last, lines[j+1]) {
			continue
		}
		if k := strings.LastIndex(last, "}"); k >= 0 && k != len(last)-1 && !strings.HasSuffix(last, "};") {
			return 0, false
		}
		return j, true
	}
	return 0, false
}

// continuesStmt reports whether a type alias or import continues on the next line.
func continuesStmt(language, last, next string) bool {
	if language == "python" {
		return strings.HasSuffix(last, "\\")
	}
	if strings.HasSuffix(last, "=") || strings.HasSuffix(last, "|") || strings.HasSuffix(last, "&") || strings.HasSuffix(last, ",") {
		return true
	}
	nt := strings.TrimSpace(next)
	return strings.HasPrefix(nt, "|") || strings.HasPrefix(nt, "&")
}
