package verify

import (
	"strings"
	"unicode/utf8"
)

// The stub check of the line languages (Python, TypeScript/JavaScript, Rust) judges added lines by whole-line
// patterns (stubs_lines.go). Whether an added run of lines sits at the top level of the file — not inside an
// existing declaration, bracket, string literal or block comment (decision D11(d)) — is the one question a line
// pattern cannot answer, so a small lexer tracks bracket depth, string literals and comments over the head file.
// It fails closed: input it cannot follow (an unterminated single-line string, a closing bracket without an
// opening one) marks the rest of the file as not top level.

const (
	lexCode = iota
	lexBlockComment
	lexString
	lexTemplate // TypeScript/JavaScript template literal text
)

// lexLine is what the stub check knows about one head line.
type lexLine struct {
	clean    bool   // the line starts at the top level: no open bracket, string, comment or continuation
	inString bool   // the line starts inside a string literal or template text
	code     string // the line with comment text blanked (string literals kept)
	logical  int    // Python: index of the line on which the logical line ending on this line began
}

type lexer struct {
	lang     string
	depth    int
	mode     int
	quote    string // closing delimiter of the open string
	raw      bool   // Rust raw string: no escapes
	nest     int    // Rust nested block comment depth
	tmpl     []int  // TypeScript/JavaScript: bracket depth of each open template substitution
	cont     bool   // Python: the previous line ended in a backslash continuation
	prev     byte   // last significant code byte (regex-literal detection)
	prevWord string // last identifier (regex-literal detection)
	escEOL   bool   // the line ended in a backslash inside a string literal
	broken   bool
}

// lexLines lexes the head lines of a line-language file.
func lexLines(lang string, lines []string) []lexLine {
	lx := &lexer{lang: lang}
	out := make([]lexLine, len(lines))
	logical := 0
	for i, s := range lines {
		clean := !lx.broken && lx.mode == lexCode && lx.depth == 0 && !lx.cont
		inString := lx.mode == lexString || lx.mode == lexTemplate
		lx.cont = false
		code := lx.line(s)
		if clean && strings.TrimSpace(code) != "" {
			logical = i
		}
		out[i] = lexLine{clean: clean, inString: inString, code: code, logical: logical}
	}
	return out
}

func isIdentByte(c byte) bool {
	return c == '_' || c == '$' || c >= 0x80 || ('0' <= c && c <= '9') || ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z')
}

// line lexes one line and returns it with comment bytes replaced by spaces.
func (lx *lexer) line(s string) string {
	b := []byte(s)
	lx.escEOL = false
	blank := func(from, to int) {
		for k := from; k < to && k < len(b); k++ {
			b[k] = ' '
		}
	}
	i := 0
	for i < len(s) && !lx.broken {
		switch lx.mode {
		case lexBlockComment:
			start := i
			for i < len(s) {
				if lx.lang == "rust" && strings.HasPrefix(s[i:], "/*") {
					lx.nest++
					i += 2
					continue
				}
				if strings.HasPrefix(s[i:], "*/") {
					i += 2
					if lx.nest--; lx.nest == 0 {
						lx.mode = lexCode
						break
					}
					continue
				}
				i++
			}
			blank(start, i)
		case lexString:
			i = lx.stringBody(s, i)
		case lexTemplate:
			for i < len(s) {
				c := s[i]
				if c == '\\' {
					i += 2
					continue
				}
				if c == '`' {
					i++
					lx.mode, lx.prev = lexCode, '`'
					break
				}
				if strings.HasPrefix(s[i:], "${") {
					i += 2
					lx.depth++
					lx.tmpl = append(lx.tmpl, lx.depth)
					lx.mode, lx.prev = lexCode, '{'
					break
				}
				i++
			}
		default:
			if n := lx.commentStart(s, i); n != 0 {
				if n < 0 { // line comment
					blank(i, len(s))
					return string(b)
				}
				blank(i, i+2)
				i += 2
				lx.mode, lx.nest = lexBlockComment, 1
				continue
			}
			i = lx.code(s, i)
		}
	}
	if lx.mode == lexString && lx.lang != "rust" && len(lx.quote) == 1 && !lx.escEOL {
		// A single-line string literal is still open at the end of the line without a backslash continuation.
		lx.broken = true
	}
	return string(b)
}

// commentStart returns -1 for a line comment, 1 for a block comment starting at s[i], 0 otherwise.
func (lx *lexer) commentStart(s string, i int) int {
	if lx.lang == "python" {
		if s[i] == '#' {
			return -1
		}
		return 0
	}
	switch {
	case strings.HasPrefix(s[i:], "//"):
		return -1
	case strings.HasPrefix(s[i:], "/*"):
		return 1
	}
	return 0
}

// stringBody consumes string literal text from s[i] up to and including the closing delimiter.
func (lx *lexer) stringBody(s string, i int) int {
	for i < len(s) {
		if !lx.raw && s[i] == '\\' {
			lx.escEOL = i == len(s)-1
			i += 2
			continue
		}
		if strings.HasPrefix(s[i:], lx.quote) {
			lx.mode, lx.prev, lx.prevWord = lexCode, '"', ""
			return i + len(lx.quote)
		}
		i++
	}
	return len(s)
}

// code consumes one code token at s[i].
func (lx *lexer) code(s string, i int) int {
	c := s[i]
	switch {
	case c == ' ' || c == '\t':
		return i + 1
	case c == '"' || c == '\'' && lx.lang != "rust":
		lx.mode, lx.raw, lx.quote = lexString, false, string(c)
		if lx.lang == "python" && strings.HasPrefix(s[i:], strings.Repeat(string(c), 3)) {
			lx.quote = strings.Repeat(string(c), 3)
		}
		return i + len(lx.quote)
	case c == '\'': // Rust: char literal or lifetime
		return lx.rustQuote(s, i)
	case c == '`' && lx.lang != "python" && lx.lang != "rust":
		lx.mode = lexTemplate
		return i + 1
	case c == '/' && lx.lang != "python" && lx.lang != "rust" && lx.regexAllowed():
		return lx.regex(s, i)
	case c == '\\' && lx.lang == "python" && i == len(s)-1:
		lx.cont = true
		return i + 1
	case c == '(' || c == '[' || c == '{':
		lx.depth++
	case c == ')' || c == ']' || c == '}':
		if c == '}' && len(lx.tmpl) > 0 && lx.tmpl[len(lx.tmpl)-1] == lx.depth {
			lx.tmpl = lx.tmpl[:len(lx.tmpl)-1]
			lx.depth--
			lx.mode = lexTemplate
			return i + 1
		}
		if lx.depth--; lx.depth < 0 {
			lx.broken = true
		}
	case isIdentByte(c):
		j := i
		for j < len(s) && isIdentByte(s[j]) {
			j++
		}
		w := s[i:j]
		if lx.lang == "rust" && j < len(s) {
			if (w == "r" || w == "br") && (s[j] == '#' || s[j] == '"') {
				k := j
				for k < len(s) && s[k] == '#' {
					k++
				}
				if k < len(s) && s[k] == '"' {
					lx.mode, lx.raw, lx.quote = lexString, true, "\""+strings.Repeat("#", k-j)
					return k + 1
				}
			}
			if w == "b" && s[j] == '\'' {
				return lx.rustQuote(s, j)
			}
		}
		lx.prev, lx.prevWord = 'a', w
		return j
	}
	lx.prev, lx.prevWord = c, ""
	return i + 1
}

// rustQuote consumes a Rust char literal ('a', '\n', '\u{1F600}') or the quote of a lifetime ('a, 'static).
func (lx *lexer) rustQuote(s string, i int) int {
	if i+1 < len(s) && s[i+1] == '\\' {
		j := i + 3
		for j < len(s) && s[j] != '\'' {
			j++
		}
		if j >= len(s) {
			lx.broken = true
			return len(s)
		}
		lx.prev = '"'
		return j + 1
	}
	if i+1 < len(s) {
		_, n := utf8.DecodeRuneInString(s[i+1:])
		if i+1+n < len(s) && s[i+1+n] == '\'' {
			lx.prev = '"'
			return i + 2 + n
		}
	}
	return i + 1 // lifetime or label
}

// regexAllowed reports whether a '/' starts a regular expression literal rather than a division.
func (lx *lexer) regexAllowed() bool {
	switch lx.prevWord {
	case "return", "typeof", "instanceof", "in", "of", "new", "delete", "void", "throw", "case", "do", "else", "yield", "await":
		return true
	}
	return !(isIdentByte(lx.prev) || lx.prev == ')' || lx.prev == ']' || lx.prev == '}' || lx.prev == '"' || lx.prev == '\'' || lx.prev == '`')
}

// regex consumes a regular expression literal starting at s[i]; one that does not end on its line breaks the lexer.
func (lx *lexer) regex(s string, i int) int {
	inClass := false
	for j := i + 1; j < len(s); j++ {
		switch s[j] {
		case '\\':
			j++
		case '[':
			inClass = true
		case ']':
			inClass = false
		case '/':
			if !inClass {
				lx.prev, lx.prevWord = 'a', ""
				return j + 1
			}
		}
	}
	lx.broken = true
	return len(s)
}
