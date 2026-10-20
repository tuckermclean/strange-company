package verify

import (
	"fmt"
	"regexp"
	"strings"
)

// Line languages (Python, TypeScript/JavaScript, Rust): the stub check is an allowlist of canonical layouts
// (plan 22 section 14.2 "others by a per-language line regex"; decision D11). Every added head line of a stub-class
// file must, as a whole line (trailing whitespace ignored), belong to one of:
//
//   - a blank line or a full-line comment (`#` / `//`);
//   - a strict plain import (Python `import a.b` / `from a.b import c, d`; TS `import {a, b} from "x";` /
//     `import x from "x";`; Rust `use a::b;` / `use a::{b, c};`);
//   - a pure type declaration (TS `type X = T;`, `interface I { a: T; }` and its block form; Rust unit, tuple and
//     field structs and enums with plain variants, one-line or block form);
//   - a canonical stub: the marker comment, a strict declaration line (name, `ident` / `ident: SimpleType`
//     parameters, optional simple return type), then one zero-value or panic statement and the closing `}`
//     (Python: the dedent), or the one-line form `... { <statement> }` / `def f(): <statement>`.
//
// Anything else is a malformed stub fact. An added run of lines must also sit at the top level of the file, between
// two complete statements (decision D11(d): additions inside an existing declaration are malformed), and the names
// it binds must be new to the file.

// lineLang holds the patterns of one line language.
type lineLang struct {
	comment string
	brace   bool           // blocks are closed by `}` (TS, Rust) rather than by the dedent (Python)
	declRe  *regexp.Regexp // stub declaration: 1 name, 2 type parameters, 3 parameters, 4 return type, 5 rest
	zeroRe  *regexp.Regexp // zero-value statements
	panicRe string         // panic statement; %s is the quoted panic message
	param   func(string) bool
	typ     func(string) bool
	tparam  *regexp.Regexp
	imports func(string) ([]string, bool) // the names an import line binds; false when it is not a strict import
	typeOne func(string) bool             // a one-line pure type declaration
	typeBlk *regexp.Regexp                // the opening line of a block type declaration
	member  func(string) bool             // one member line of a block type declaration
	star    *regexp.Regexp                // a star import or re-export: a new name could shadow one of its names
}

const (
	reID   = `[A-Za-z_][A-Za-z0-9_]*`
	reTSID = `[A-Za-z_$][A-Za-z0-9_$]*`
	reVis  = `(?:pub(?:\(crate\))?\s+)?`
)

var lineLangs = map[string]*lineLang{
	"python":     pythonLang(),
	"typescript": tsLineLang(),
	"javascript": tsLineLang(),
	"rust":       rustLang(),
}

func pythonLang() *lineLang {
	dotted := reID + `(?:\.` + reID + `)*`
	importRe := regexp.MustCompile(`^import\s+(` + dotted + `(?:\s*,\s*` + dotted + `)*)$`)
	fromRe := regexp.MustCompile(`^from\s+(\.*` + dotted + `|\.+)\s+import\s+(` + reID + `(?:\s*,\s*` + reID + `)*)$`)
	paramRe := regexp.MustCompile(`^(` + reID + `)(?:\s*:\s*(.+))?$`)
	l := &lineLang{
		comment: "#",
		declRe:  regexp.MustCompile(`^def\s+(` + reID + `)\s*()\(([^()]*)\)\s*(?:->\s*([^:]+?)\s*)?:(.*)$`),
		zeroRe:  regexp.MustCompile(`^(?:return(?:\s+(?:None|0|0\.0|False|""|''|\[\]|\{\}|\(\)))?|pass|\.\.\.)$`),
		panicRe: `^raise\s+NotImplementedError\(\s*["']%s["']\s*\)$`,
		typ:     func(s string) bool { return simpleType(s, "_.[], |") },
		star:    regexp.MustCompile(`^from\s+\S+\s+import\s+\*`),
		typeOne: func(string) bool { return false },
		member:  func(string) bool { return false },
	}
	l.param = func(s string) bool {
		m := paramRe.FindStringSubmatch(s)
		return m != nil && (m[2] == "" || l.typ(m[2]))
	}
	l.imports = func(t string) ([]string, bool) {
		if m := importRe.FindStringSubmatch(t); m != nil {
			var names []string
			for _, d := range strings.Split(m[1], ",") {
				names = append(names, strings.SplitN(strings.TrimSpace(d), ".", 2)[0])
			}
			return names, true
		}
		if m := fromRe.FindStringSubmatch(t); m != nil && m[1] != "__future__" {
			return splitNames(m[2]), true
		}
		return nil, false
	}
	return l
}

func tsLineLang() *lineLang {
	str := `(?:"[A-Za-z0-9_@./-]+"|'[A-Za-z0-9_@./-]+')`
	namedRe := regexp.MustCompile(`^import\s*\{\s*(` + reTSID + `(?:\s*,\s*` + reTSID + `)*)\s*,?\s*\}\s*from\s*` + str + `\s*;?$`)
	defaultRe := regexp.MustCompile(`^import\s+(` + reTSID + `)\s+from\s*` + str + `\s*;?$`)
	paramRe := regexp.MustCompile(`^(` + reTSID + `)(?:\s*:\s*(.+))?$`)
	aliasRe := regexp.MustCompile(`^(?:export\s+)?type\s+` + reTSID + `\s*=\s*(.+?)\s*;?$`)
	ifaceRe := regexp.MustCompile(`^(?:export\s+)?interface\s+` + reTSID + `\s*\{(.*)\}$`)
	memberRe := regexp.MustCompile(`^(?:readonly\s+)?` + reTSID + `\??\s*:\s*(.+)$`)
	l := &lineLang{
		comment: "//",
		brace:   true,
		declRe: regexp.MustCompile(`^export\s+function\s+(` + reTSID + `)\s*(?:<([^<>(){}]*)>)?\s*\(([^()]*)\)\s*` +
			`(?::\s*([^{}()=;]+?)\s*)?\{(.*)$`),
		zeroRe:  regexp.MustCompile(`^return(?:\s+(?:null|undefined|0|false|""|''|\[\]|\{\}))?\s*;?$`),
		panicRe: `^throw\s+new\s+Error\(\s*["']%s["']\s*\)\s*;?$`,
		typ:     func(s string) bool { return simpleType(s, "_$.<>[], |&") },
		tparam:  regexp.MustCompile(`^\s*` + reTSID + `(?:\s*,\s*` + reTSID + `)*\s*$`),
		typeBlk: regexp.MustCompile(`^(?:export\s+)?interface\s+` + reTSID + `\s*\{$`),
		star:    regexp.MustCompile(`^export\s+\*`),
	}
	l.param = func(s string) bool {
		m := paramRe.FindStringSubmatch(s)
		return m != nil && (m[2] == "" || l.typ(m[2]))
	}
	l.imports = func(t string) ([]string, bool) {
		if m := namedRe.FindStringSubmatch(t); m != nil {
			return splitNames(m[1]), true
		}
		if m := defaultRe.FindStringSubmatch(t); m != nil {
			return []string{m[1]}, true
		}
		return nil, false
	}
	memberOK := func(s string) bool {
		m := memberRe.FindStringSubmatch(strings.TrimSpace(s))
		return m != nil && l.typ(m[1])
	}
	l.member = func(t string) bool { return memberOK(strings.TrimSuffix(strings.TrimSuffix(t, ";"), ",")) }
	l.typeOne = func(t string) bool {
		if m := aliasRe.FindStringSubmatch(t); m != nil {
			return l.typ(m[1])
		}
		if m := ifaceRe.FindStringSubmatch(t); m != nil {
			return membersOK(m[1], ";,", memberOK)
		}
		return false
	}
	return l
}

func rustLang() *lineLang {
	path := reID + `(?:::` + reID + `)*`
	useRe := regexp.MustCompile(`^use\s+(` + path + `)\s*;$`)
	useGroupRe := regexp.MustCompile(`^use\s+(` + path + `)::\{\s*(` + reID + `(?:\s*,\s*` + reID + `)*)\s*,?\s*\}\s*;$`)
	paramRe := regexp.MustCompile(`^(?:self|&self|&mut self|` + reID + `\s*:\s*(.+))$`)
	tp := `(?:<([^<>(){}]*)>)?`
	unitRe := regexp.MustCompile(`^` + reVis + `struct\s+` + reID + `\s*` + tp + `\s*;$`)
	tupleRe := regexp.MustCompile(`^` + reVis + `struct\s+` + reID + `\s*` + tp + `\s*\(([^()]*)\)\s*;$`)
	structRe := regexp.MustCompile(`^` + reVis + `struct\s+` + reID + `\s*` + tp + `\s*\{(.*)\}$`)
	enumRe := regexp.MustCompile(`^` + reVis + `enum\s+` + reID + `\s*\{(.*)\}$`)
	fieldRe := regexp.MustCompile(`^` + reVis + reID + `\s*:\s*(.+)$`)
	tupleFieldRe := regexp.MustCompile(`^` + reVis + `(.+)$`)
	variantRe := regexp.MustCompile(`^` + reID + `$`)
	l := &lineLang{
		comment: "//",
		brace:   true,
		declRe: regexp.MustCompile(`^` + reVis + `fn\s+(` + reID + `)\s*` + tp + `\s*\(([^()]*)\)\s*` +
			`(?:->\s*([^{}()=;]+?)\s*)?\{(.*)$`),
		zeroRe: regexp.MustCompile(`^(?:return\s+)?(?:Default::default\(\)|None|\(\)|0|0\.0|false|""|String::new\(\)|Vec::new\(\)|` +
			`vec!\[\]|Ok\(\(\)\)|Ok\(Default::default\(\)\))\s*;?$`),
		panicRe: `^(?:todo|unimplemented|panic)!\(\s*"%s"\s*\)\s*;?$`,
		typ:     func(s string) bool { return simpleType(s, "_:<>&'[], *") },
		tparam:  regexp.MustCompile(`^\s*'?` + reID + `(?:\s*,\s*'?` + reID + `)*\s*$`),
		typeBlk: regexp.MustCompile(`^` + reVis + `(?:struct\s+` + reID + `\s*` + tp + `|enum\s+` + reID + `)\s*\{$`),
		star:    regexp.MustCompile(`^pub(?:\([^)]*\))?\s+use\s[^;]*\*`),
	}
	l.param = func(s string) bool {
		m := paramRe.FindStringSubmatch(s)
		return m != nil && (m[1] == "" && !strings.Contains(s, ":") || l.typ(m[1]))
	}
	l.imports = func(t string) ([]string, bool) {
		last := func(p string) string { return p[strings.LastIndex(p, ":")+1:] }
		if m := useRe.FindStringSubmatch(t); m != nil {
			return []string{last(m[1])}, true
		}
		if m := useGroupRe.FindStringSubmatch(t); m != nil {
			var names []string
			for _, n := range splitNames(m[2]) {
				if n == "self" {
					n = last(m[1])
				}
				names = append(names, n)
			}
			return names, true
		}
		return nil, false
	}
	field := func(s string) bool {
		m := fieldRe.FindStringSubmatch(strings.TrimSpace(s))
		return m != nil && l.typ(m[1])
	}
	tparamOK := func(s string) bool { return s == "" || l.tparam.MatchString(s) }
	l.member = func(t string) bool {
		t = strings.TrimSuffix(t, ",")
		return field(t) || variantRe.MatchString(strings.TrimSpace(t))
	}
	l.typeOne = func(t string) bool {
		if m := unitRe.FindStringSubmatch(t); m != nil {
			return tparamOK(m[1])
		}
		if m := tupleRe.FindStringSubmatch(t); m != nil {
			return tparamOK(m[1]) && membersOK(m[2], ",", func(s string) bool {
				f := tupleFieldRe.FindStringSubmatch(strings.TrimSpace(s))
				return f != nil && l.typ(f[1])
			})
		}
		if m := structRe.FindStringSubmatch(t); m != nil {
			return tparamOK(m[1]) && membersOK(m[2], ",", field)
		}
		if m := enumRe.FindStringSubmatch(t); m != nil {
			return membersOK(m[1], ",", func(s string) bool { return variantRe.MatchString(strings.TrimSpace(s)) })
		}
		return false
	}
	return l
}

func splitNames(s string) []string {
	var out []string
	for _, n := range strings.Split(s, ",") {
		if n = strings.TrimSpace(n); n != "" {
			out = append(out, n)
		}
	}
	return out
}

// simpleType reports whether s is a simple type (decision D12): identifiers, paths, generics, references, arrays
// and unions; no call, block, default, arrow or literal. extra lists the punctuation the language allows.
func simpleType(s, extra string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	depth := 0
	for _, r := range s {
		alnum := r < 0x80 && isIdentByte(byte(r)) && r != '$'
		if !alnum && !strings.ContainsRune(extra, r) {
			return false
		}
		switch r {
		case '<', '[':
			depth++
		case '>', ']':
			if depth--; depth < 0 {
				return false
			}
		}
	}
	return depth == 0
}

// splitTop splits s at the separator bytes that are outside <...> and [...].
func splitTop(s, seps string) []string {
	var out []string
	depth, start := 0, 0
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '<' || c == '[':
			depth++
		case c == '>' || c == ']':
			depth--
		case depth == 0 && strings.IndexByte(seps, c) >= 0:
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

// membersOK checks the members of a one-line type declaration; a trailing separator is allowed.
func membersOK(s, seps string, ok func(string) bool) bool {
	parts := splitTop(s, seps)
	for i, p := range parts {
		if strings.TrimSpace(p) == "" {
			if i == len(parts)-1 {
				continue
			}
			return false
		}
		if !ok(p) {
			return false
		}
	}
	return true
}

// pyBuiltins are the names of Python's builtins module: a module-level stub or import with such a name would
// shadow the builtin for the existing code of the module (and for star importers).
var pyBuiltins = toSet(`ArithmeticError AssertionError AttributeError BaseException BaseExceptionGroup BlockingIOError
BrokenPipeError BufferError BytesWarning ChildProcessError ConnectionAbortedError ConnectionError ConnectionRefusedError
ConnectionResetError DeprecationWarning EOFError Ellipsis EncodingWarning EnvironmentError Exception ExceptionGroup
False FileExistsError FileNotFoundError FloatingPointError FutureWarning GeneratorExit IOError ImportError
ImportWarning IndentationError IndexError InterruptedError IsADirectoryError KeyError KeyboardInterrupt LookupError
MemoryError ModuleNotFoundError NameError None NotADirectoryError NotImplemented NotImplementedError OSError
OverflowError PendingDeprecationWarning PermissionError ProcessLookupError PythonFinalizationError RecursionError
ReferenceError ResourceWarning RuntimeError RuntimeWarning StopAsyncIteration StopIteration SyntaxError SyntaxWarning
SystemError SystemExit TabError TimeoutError True TypeError UnboundLocalError UnicodeDecodeError UnicodeEncodeError
UnicodeError UnicodeTranslateError UnicodeWarning UserWarning ValueError Warning ZeroDivisionError abs aiter all anext
any ascii bin bool breakpoint bytearray bytes callable chr classmethod compile complex copyright credits delattr dict
dir divmod enumerate eval exec exit filter float format frozenset getattr globals hasattr hash help hex id input int
isinstance issubclass iter len license list locals map max memoryview min next object oct open ord pow print property
quit range repr reversed round set setattr slice sorted staticmethod str sum super tuple type vars zip`)

func toSet(words string) map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(words) {
		m[w] = true
	}
	return m
}

var (
	pyCodingRe      = regexp.MustCompile(`^[ \t]*#.*?coding[:=]`)
	pyStringStartRe = regexp.MustCompile(`^(?:[rRbBuUfF]{1,2})?["']`)
	tsContinuers    = toSet(`in instanceof as satisfies of extends implements is keyof`)
)

// lineAnalysis is the canonical-layout check of one line-language file.
type lineAnalysis struct {
	p        StubPolicy
	lang     *lineLang
	language string
	path     string
	head     []string // head lines, trailing "\r" removed
	added    []bool
	lex      []lexLine
	existing map[string]bool // identifiers of the unchanged head lines (attribute names excluded)
	star     bool
	panicRe  *regexp.Regexp
	addsOnly bool
	facts    []lineFact
	badStart int // first line (0-based) of the open run of malformed lines, -1 when none
}

func analyzeLines(p StubPolicy, f StubFile) ([]lineFact, bool, error) {
	lang := lineLangs[f.Language]
	panicRe, err := regexp.Compile(fmt.Sprintf(lang.panicRe, regexp.QuoteMeta(p.PanicMessage)))
	if err != nil {
		return nil, false, err
	}
	head, base := splitLines(f.Head), splitLines(f.Base)
	a := &lineAnalysis{
		p: p, lang: lang, language: f.Language, path: f.Path, head: head, panicRe: panicRe,
		added:    addedLines(base, head, f.Base == nil),
		lex:      lexLines(f.Language, head),
		addsOnly: f.Base == nil || subsequence(base, head),
		existing: map[string]bool{},
		badStart: -1,
	}
	for i, l := range a.lex {
		if !a.added[i] {
			addWords(a.existing, l.code)
		}
		if l.clean && lang.star.MatchString(l.code) {
			a.star = true
		}
	}
	for i := 0; i < len(head); {
		if !a.added[i] {
			i++
			continue
		}
		j := i
		for j < len(head) && a.added[j] {
			j++
		}
		a.run(i, j)
		i = j
	}
	return a.facts, a.addsOnly, nil
}

// addWords adds the identifiers of a code line to set, except attribute and method names (preceded by '.') and
// string prefixes (Python f"", Rust b"").
func addWords(set map[string]bool, code string) {
	for i := 0; i < len(code); {
		if !isIdentByte(code[i]) {
			i++
			continue
		}
		j := i
		for j < len(code) && isIdentByte(code[j]) {
			j++
		}
		prefix := j-i <= 2 && strings.Trim(code[i:j], "rRbBuUfF") == "" && j < len(code) && (code[j] == '"' || code[j] == '\'') &&
			(i == 0 || code[i-1] != '"' && code[i-1] != '\'')
		if (i == 0 || code[i-1] != '.') && !prefix {
			set[code[i:j]] = true
		}
		i = j
	}
}

func hasCode(code string) bool { return strings.TrimSpace(code) != "" }

func firstByte(s string) byte {
	if s = strings.TrimSpace(s); s != "" {
		return s[0]
	}
	return 0
}

func lastByte(s string) byte {
	if s = strings.TrimSpace(s); s != "" {
		return s[len(s)-1]
	}
	return 0
}

func (a *lineAnalysis) bad(k int) {
	if a.badStart < 0 {
		a.badStart = k
	}
}

func (a *lineAnalysis) flush() {
	if a.badStart >= 0 {
		a.facts = append(a.facts, lineFact{line: a.badStart + 1, f: StubFact{
			Path: a.path, Symbol: fmt.Sprintf("(added code @%d)", a.badStart+1), Shape: "unknown", WellFormed: false,
		}})
		a.badStart = -1
	}
}

// run checks one run [i, j) of consecutive added head lines.
func (a *lineAnalysis) run(i, j int) {
	blankOnly := true
	for k := i; k < j; k++ {
		if strings.TrimSpace(a.head[k]) != "" {
			blankOnly = false
		}
	}
	if blankOnly {
		if a.lex[i].inString { // blank lines added inside a string literal change its value
			a.bad(i)
			a.flush()
		}
		return
	}
	ctx := a.contextOK(i, j)
	before := len(a.facts)
	for k := i; k < j; {
		k = a.unit(k, i, j, ctx)
	}
	a.flush()
	if !ctx && len(a.facts) == before {
		a.facts = append(a.facts, lineFact{line: i + 1, f: StubFact{
			Path: a.path, Symbol: fmt.Sprintf("(added code @%d)", i+1), Shape: "unknown", WellFormed: false,
		}})
	}
}

// contextOK reports whether the run [i, j) is inserted at the top level between two complete statements of the
// unchanged code: A, the last unchanged code line before it, and B, the first one after it.
func (a *lineAnalysis) contextOK(i, j int) bool {
	if !a.lex[i].clean {
		return false
	}
	prev, next, last := -1, -1, -1
	for k := i - 1; k >= 0 && prev < 0; k-- {
		if !a.added[k] && hasCode(a.lex[k].code) {
			prev = k
		}
	}
	for k := j; k < len(a.head) && next < 0; k++ {
		if !a.added[k] && hasCode(a.lex[k].code) {
			next = k
		}
	}
	for k := j - 1; k >= i && last < 0; k-- {
		if hasCode(a.lex[k].code) {
			last = k
		}
	}
	endsStmt := func(k int) bool { c := lastByte(a.lex[k].code); return c == ';' || c == '}' }
	switch a.language {
	case "python":
		// Not after a decorator or a block header; the next statement is at the top level (column 0) and is not
		// a string (a module docstring stops being one when code is put before it).
		if prev >= 0 && (firstByte(a.lex[a.lex[prev].logical].code) == '@' || lastByte(a.lex[prev].code) == ':') {
			return false
		}
		if next >= 0 && (a.head[next][0] == ' ' || a.head[next][0] == '\t' || pyStringStartRe.MatchString(strings.TrimSpace(a.lex[next].code))) {
			return false
		}
	case "rust":
		// After a complete item (an attribute line would apply to the inserted item instead).
		if prev >= 0 && !endsStmt(prev) {
			return false
		}
	default:
		// After an explicit statement end; after a `}` the next line must not continue an expression (ASI).
		// A string at the next line could be a directive prologue.
		if prev >= 0 && (!endsStmt(prev) || lastByte(a.lex[prev].code) == '}' && next >= 0 && !tsStatementStart(a.lex[next].code)) {
			return false
		}
		if next >= 0 && (last >= 0 && !endsStmt(last) || strings.ContainsRune("\"'`", rune(firstByte(a.lex[next].code)))) {
			return false
		}
	}
	return true
}

// tsStatementStart reports whether a line cannot continue the expression of the line before it.
func tsStatementStart(code string) bool {
	t := strings.TrimSpace(code)
	if t == "" || !(isIdentByte(t[0]) || t[0] == '@') {
		return false
	}
	k := 0
	for k < len(t) && isIdentByte(t[k]) {
		k++
	}
	return !tsContinuers[t[:k]]
}

// fresh reports whether a name bound by added code is new to the file and shadows no builtin.
func (a *lineAnalysis) fresh(name string) bool {
	if a.existing[name] {
		return false
	}
	if a.language == "python" && (pyBuiltins[name] || strings.HasPrefix(name, "__") && strings.HasSuffix(name, "__")) {
		return false
	}
	return true
}

func (a *lineAnalysis) isComment(t string) bool {
	return strings.HasPrefix(strings.TrimSpace(t), a.lang.comment)
}

// commentOK rejects the comments that are directives: a Python encoding declaration (lines 1-2) and TS/JS
// triple-slash directives and `@` pragmas (`// @jsx`, `// @ts-nocheck`).
func (a *lineAnalysis) commentOK(k int, t string) bool {
	switch a.language {
	case "python":
		return k > 1 || !pyCodingRe.MatchString(t)
	case "typescript", "javascript":
		rest := strings.TrimPrefix(strings.TrimSpace(t), "//")
		return !strings.HasPrefix(rest, "/") && !strings.HasPrefix(strings.TrimSpace(rest), "@")
	}
	return true
}

// unit consumes the canonical unit starting at line k of the run [i, j) and returns the next line.
func (a *lineAnalysis) unit(k, i, j int, ctx bool) int {
	t := strings.TrimRight(a.head[k], " \t")
	switch {
	case t == "":
		a.flush()
		return k + 1
	case a.isComment(t):
		if !ctx || !a.commentOK(k, t) {
			a.bad(k)
		} else {
			a.flush()
		}
		return k + 1
	}
	if names, ok := a.lang.imports(t); ok {
		good := ctx
		for _, n := range names {
			good = good && a.fresh(n)
		}
		if good {
			a.flush()
		} else {
			a.bad(k)
		}
		return k + 1
	}
	if a.lang.typeOne(t) {
		if ctx {
			a.flush()
		} else {
			a.bad(k)
		}
		return k + 1
	}
	if a.lang.typeBlk != nil && a.lang.typeBlk.MatchString(t) {
		return a.typeBlock(k, j, ctx)
	}
	if m := a.lang.declRe.FindStringSubmatch(t); m != nil {
		a.flush()
		return a.stub(k, i, j, ctx, m)
	}
	a.bad(k)
	return k + 1
}

// typeBlock consumes a block type declaration: member lines (blank and comment lines allowed) and a closing `}`.
func (a *lineAnalysis) typeBlock(k, j int, ctx bool) int {
	good := ctx
	c := k + 1
	for ; c < j; c++ {
		t := strings.TrimRight(a.head[c], " \t")
		if t == "}" {
			break
		}
		if strings.TrimSpace(t) != "" && !a.isComment(t) && !a.lang.member(t) {
			good = false
		}
	}
	if c == j {
		good = false
		c = j - 1
	}
	if good {
		a.flush()
		return c + 1
	}
	for n := k; n <= c; n++ {
		a.bad(n)
	}
	return c + 1
}

// stub consumes a stub declaration starting at line k and records its fact.
func (a *lineAnalysis) stub(k, i, j int, ctx bool, m []string) int {
	name, tparams, params, ret, rest := m[1], m[2], m[3], m[4], strings.TrimSpace(m[5])
	canonical := (tparams == "" || a.lang.tparam != nil && a.lang.tparam.MatchString(tparams)) &&
		(ret == "" || a.lang.typ(ret))
	if strings.TrimSpace(params) != "" {
		for _, prm := range splitTop(params, ",") {
			canonical = canonical && a.lang.param(strings.TrimSpace(prm))
		}
	}
	end, bodyLines, shape := k, 1, "unknown"
	switch {
	case rest != "" && a.lang.brace:
		if strings.HasSuffix(rest, "}") {
			shape = a.shape([]string{strings.TrimSpace(strings.TrimSuffix(rest, "}"))}, true, false)
		}
	case rest != "":
		shape = a.shape([]string{rest}, false, false)
	case a.lang.brace:
		c := k + 1
		for c < j && strings.TrimRight(a.head[c], " \t") != "}" {
			c++
		}
		if c < j {
			shape = a.shape(a.head[k+1:c], true, true)
		} else {
			c = j - 1
		}
		end, bodyLines = c, max(1, nonBlank(a.head[k+1:c]))
	default: // Python: the body is the following indented, blank and comment lines
		c := k + 1
		for c < j && (strings.TrimSpace(a.head[c]) == "" || a.head[c][0] == ' ' || a.head[c][0] == '\t') {
			if strings.TrimSpace(a.head[c]) != "" {
				end = c
			}
			c++
		}
		if end > k {
			shape = a.shape(a.head[k+1:end+1], false, true)
		}
		bodyLines = max(1, nonBlank(a.head[k+1:end+1]))
	}
	marker := ""
	if k > i {
		marker = markerBefore(a.p, a.lang.comment, a.head, k+1)
	}
	wf := canonical && shape != "unknown" && ctx && a.addsOnly && !a.star && a.fresh(name) &&
		marker != "" && markerFormat(a.p).MatchString(marker) && bodyLines <= a.p.MaxAddedLinesPerStub
	a.facts = append(a.facts, lineFact{line: k + 1, f: StubFact{Path: a.path, Symbol: name, Marker: marker, Shape: shape, WellFormed: wf}})
	return end + 1
}

// shape classifies a stub body: exactly one zero-value or panic statement (an empty brace body is zero-value).
// Blank and full-line comment lines of a multi-line body are ignored; a one-line form has no comment.
func (a *lineAnalysis) shape(body []string, emptyOK, skipComments bool) string {
	var stmts []string
	for _, l := range body {
		if t := strings.TrimSpace(l); t != "" && !(skipComments && a.isComment(t)) {
			stmts = append(stmts, t)
		}
	}
	switch {
	case len(stmts) == 0 && emptyOK:
		return "zero_value"
	case len(stmts) != 1:
		return "unknown"
	case a.panicRe.MatchString(stmts[0]):
		return "panic"
	case a.lang.zeroRe.MatchString(stmts[0]):
		return "zero_value"
	}
	return "unknown"
}

func nonBlank(lines []string) int {
	n := 0
	for _, l := range lines {
		if strings.TrimSpace(l) != "" {
			n++
		}
	}
	return n
}

// lineFact is a stub fact with the head line it starts on (facts are ordered by line).
type lineFact struct {
	line int
	f    StubFact
}
