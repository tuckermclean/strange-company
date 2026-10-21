package verify

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

// TOML is read as a fail-closed recogniser of a canonical subset (decision D18 b): bare keys, dotted keys and
// quoted keys without a backslash; basic, literal and multi-line strings (decoded, so every spelling of a string has
// one value); arrays (which may span lines); single-line inline tables; and scalars compared by their source text.
// Table headers compare on the decoded key path. Anything else (a quoted key with a backslash, an inline table over
// several lines, a duplicate key, a malformed construct) is an error, and an error counts as a change.

// tomlRaw is a non-string scalar (number, boolean, date) kept as its source text; it hashes differently from a
// string with the same characters.
type tomlRaw string

func (r tomlRaw) MarshalJSON() ([]byte, error) {
	b, err := jsonMarshalNoEscape(map[string]string{"toml": string(r)})
	return b, err
}

type tomlParser struct {
	src string
	pos int
	bad error // the first comment with a control character (TOML allows only a tab), reported when the parse ends
}

func (p *tomlParser) errf(format string, args ...any) error {
	line := 1 + strings.Count(p.src[:p.pos], "\n")
	return fmt.Errorf("toml line %d: %s", line, fmt.Sprintf(format, args...))
}

func (p *tomlParser) eof() bool { return p.pos >= len(p.src) }

func (p *tomlParser) peek() byte {
	if p.eof() {
		return 0
	}
	return p.src[p.pos]
}

func (p *tomlParser) skipBlank() {
	for !p.eof() && (p.src[p.pos] == ' ' || p.src[p.pos] == '\t') {
		p.pos++
	}
}

// skipComment skips an optional comment up to (not including) the newline.
func (p *tomlParser) skipComment() {
	if p.peek() == '#' {
		for !p.eof() && p.src[p.pos] != '\n' {
			c := p.src[p.pos]
			crlf := c == '\r' && p.pos+1 < len(p.src) && p.src[p.pos+1] == '\n'
			if (c < 0x20 && c != '\t' && !crlf || c == 0x7f) && p.bad == nil {
				p.bad = p.errf("a comment contains a control character")
			}
			p.pos++
		}
	}
}

// skipSpace skips blanks, comments and newlines (statement level and inside arrays).
func (p *tomlParser) skipSpace() {
	for !p.eof() {
		switch p.src[p.pos] {
		case ' ', '\t', '\n', '\r':
			p.pos++
		case '#':
			p.skipComment()
		default:
			return
		}
	}
}

// endOfLine requires only blanks and a comment before the next newline or the end of the input.
func (p *tomlParser) endOfLine() error {
	p.skipBlank()
	p.skipComment()
	if p.peek() == '\r' {
		p.pos++
	}
	if p.eof() {
		return nil
	}
	if p.src[p.pos] != '\n' {
		return p.errf("unexpected %q after a statement", p.src[p.pos])
	}
	p.pos++
	return nil
}

func isBareKeyChar(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-'
}

func (p *tomlParser) keyPart() (string, error) {
	switch c := p.peek(); {
	case c == '"' || c == '\'':
		if strings.HasPrefix(p.src[p.pos:], string([]byte{c, c, c})) {
			return "", p.errf("a multi-line string is not a key")
		}
		end := strings.IndexByte(p.src[p.pos+1:], c)
		if end < 0 {
			return "", p.errf("unterminated quoted key")
		}
		k := p.src[p.pos+1 : p.pos+1+end]
		if strings.ContainsAny(k, "\\\n\r") {
			return "", p.errf("a quoted key with a backslash or a line break is not supported")
		}
		p.pos += end + 2
		return k, nil
	case isBareKeyChar(c):
		start := p.pos
		for !p.eof() && isBareKeyChar(p.src[p.pos]) {
			p.pos++
		}
		return p.src[start:p.pos], nil
	}
	return "", p.errf("expected a key")
}

func (p *tomlParser) key() ([]string, error) {
	var parts []string
	for {
		p.skipBlank()
		k, err := p.keyPart()
		if err != nil {
			return nil, err
		}
		parts = append(parts, k)
		p.skipBlank()
		if p.peek() != '.' {
			return parts, nil
		}
		p.pos++
	}
}

var tomlEscapes = map[byte]string{'b': "\b", 't': "\t", 'n': "\n", 'f': "\f", 'r': "\r", '"': "\"", '\\': "\\"}

// basicString decodes the body of a basic string starting after the opening quote(s); multi allows line breaks and
// the line-ending backslash.
func (p *tomlParser) basicString(multi bool) (string, error) {
	var b strings.Builder
	for !p.eof() {
		c := p.src[p.pos]
		switch {
		case c == '"':
			if !multi {
				p.pos++
				return b.String(), nil
			}
			if strings.HasPrefix(p.src[p.pos:], `"""`) {
				q := 3
				for q < 5 && p.pos+q < len(p.src) && p.src[p.pos+q] == '"' {
					q++
				}
				b.WriteString(strings.Repeat(`"`, q-3))
				p.pos += q
				return b.String(), nil
			}
			b.WriteByte(c)
			p.pos++
		case c == '\n' || c == '\r':
			if !multi {
				return "", p.errf("unterminated string")
			}
			b.WriteByte(c)
			p.pos++
		case c == '\\':
			p.pos++
			if p.eof() {
				return "", p.errf("unterminated escape")
			}
			e := p.src[p.pos]
			if r, ok := tomlEscapes[e]; ok {
				b.WriteString(r)
				p.pos++
				continue
			}
			switch e {
			case 'u', 'U':
				n := 4
				if e == 'U' {
					n = 8
				}
				if p.pos+1+n > len(p.src) {
					return "", p.errf("short unicode escape")
				}
				v, err := strconv.ParseUint(p.src[p.pos+1:p.pos+1+n], 16, 32)
				if err != nil || !utf8.ValidRune(rune(v)) {
					return "", p.errf("bad unicode escape")
				}
				b.WriteRune(rune(v))
				p.pos += 1 + n
			case ' ', '\t', '\n', '\r':
				if !multi {
					return "", p.errf("bad escape")
				}
				// A line-ending backslash trims the whitespace and line breaks that follow.
				q := p.pos
				for q < len(p.src) && (p.src[q] == ' ' || p.src[q] == '\t') {
					q++
				}
				if q >= len(p.src) || (p.src[q] != '\n' && p.src[q] != '\r') {
					return "", p.errf("bad escape")
				}
				for q < len(p.src) && strings.IndexByte(" \t\r\n", p.src[q]) >= 0 {
					q++
				}
				p.pos = q
			default:
				return "", p.errf("unsupported escape \\%c", e)
			}
		default:
			b.WriteByte(c)
			p.pos++
		}
	}
	return "", p.errf("unterminated string")
}

func (p *tomlParser) literalString(multi bool) (string, error) {
	if !multi {
		end := strings.IndexAny(p.src[p.pos:], "'\n")
		if end < 0 || p.src[p.pos+end] != '\'' {
			return "", p.errf("unterminated string")
		}
		s := p.src[p.pos : p.pos+end]
		p.pos += end + 1
		return s, nil
	}
	end := strings.Index(p.src[p.pos:], "'''")
	if end < 0 {
		return "", p.errf("unterminated multi-line string")
	}
	q := 0
	for q < 2 && p.pos+end+3+q < len(p.src) && p.src[p.pos+end+3+q] == '\'' {
		q++
	}
	s := p.src[p.pos : p.pos+end+q]
	p.pos += end + 3 + q
	return s, nil
}

func trimFirstNewline(s string) string {
	if strings.HasPrefix(s, "\r\n") {
		return s[2:]
	}
	return strings.TrimPrefix(s, "\n")
}

func (p *tomlParser) value() (any, error) {
	switch c := p.peek(); c {
	case '"', '\'':
		triple := strings.HasPrefix(p.src[p.pos:], string([]byte{c, c, c}))
		if triple {
			p.pos += 3
			var s string
			var err error
			if c == '"' {
				// The newline directly after the opener is trimmed before the line-ending backslash applies.
				p.pos += len(p.src[p.pos:]) - len(trimFirstNewline(p.src[p.pos:]))
				s, err = p.basicString(true)
			} else {
				s, err = p.literalString(true)
				s = trimFirstNewline(s)
			}
			return s, err
		}
		p.pos++
		if c == '"' {
			return p.basicString(false)
		}
		return p.literalString(false)
	case '[':
		p.pos++
		arr := []any{}
		for {
			p.skipSpace()
			if p.peek() == ']' {
				p.pos++
				return arr, nil
			}
			v, err := p.value()
			if err != nil {
				return nil, err
			}
			arr = append(arr, v)
			p.skipSpace()
			switch p.peek() {
			case ',':
				p.pos++
			case ']':
			default:
				return nil, p.errf("expected , or ] in an array")
			}
		}
	case '{':
		p.pos++
		m := map[string]any{}
		p.skipBlank()
		if p.peek() == '}' {
			p.pos++
			return m, nil
		}
		for {
			if err := p.assignment(m); err != nil {
				return nil, err
			}
			p.skipBlank()
			switch p.peek() {
			case ',':
				p.pos++
			case '}':
				p.pos++
				return m, nil
			default:
				return nil, p.errf("expected , or } in an inline table (an inline table must stay on one line)")
			}
		}
	}
	start := p.pos
	for !p.eof() && strings.IndexByte(" \t\r\n,]}#", p.src[p.pos]) < 0 {
		c := p.src[p.pos]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte("_+-.:", c) >= 0) {
			return nil, p.errf("unexpected %q in a value", c)
		}
		p.pos++
	}
	if p.pos == start {
		return nil, p.errf("expected a value")
	}
	return tomlRaw(p.src[start:p.pos]), nil
}

// assignment parses `dotted.key = value` into the table m.
func (p *tomlParser) assignment(m map[string]any) error {
	keys, err := p.key()
	if err != nil {
		return err
	}
	p.skipBlank()
	if p.peek() != '=' {
		return p.errf("expected =")
	}
	p.pos++
	p.skipBlank()
	v, err := p.value()
	if err != nil {
		return err
	}
	cur := m
	for _, k := range keys[:len(keys)-1] {
		switch n := cur[k].(type) {
		case nil:
			nm := map[string]any{}
			cur[k] = nm
			cur = nm
		case map[string]any:
			cur = n
		default:
			return p.errf("key %q is not a table", k)
		}
	}
	last := keys[len(keys)-1]
	if _, dup := cur[last]; dup {
		return p.errf("duplicate key %q", last)
	}
	cur[last] = v
	return nil
}

func tomlDescend(m map[string]any, keys []string) (map[string]any, error) {
	for _, k := range keys {
		switch n := m[k].(type) {
		case nil:
			nm := map[string]any{}
			m[k] = nm
			m = nm
		case map[string]any:
			m = n
		case []any:
			if len(n) == 0 {
				return nil, fmt.Errorf("empty table array %q", k)
			}
			last, ok := n[len(n)-1].(map[string]any)
			if !ok {
				return nil, fmt.Errorf("key %q is not a table", k)
			}
			m = last
		default:
			return nil, fmt.Errorf("key %q is not a table", k)
		}
	}
	return m, nil
}

func tomlParse(content []byte) (map[string]any, error) {
	p := &tomlParser{src: string(content)}
	root, err := p.document()
	if err == nil {
		err = p.bad
	}
	if err != nil {
		return nil, err
	}
	return root, nil
}

func (p *tomlParser) document() (map[string]any, error) {
	root := map[string]any{}
	cur := root
	seen := map[string]bool{} // table headers defined so far, "[" or "[[" plus the decoded key path
	for {
		p.skipSpace()
		if p.eof() {
			return root, nil
		}
		if p.peek() == '[' {
			array := strings.HasPrefix(p.src[p.pos:], "[[")
			p.pos++
			if array {
				p.pos++
			}
			keys, err := p.key()
			if err != nil {
				return nil, err
			}
			closer := "]"
			if array {
				closer = "]]"
			}
			if !strings.HasPrefix(p.src[p.pos:], closer) {
				return nil, p.errf("expected %s", closer)
			}
			p.pos += len(closer)
			id := strings.Join(keys, "\x00")
			if seen["["+id] || !array && seen["[["+id] {
				return nil, p.errf("table %q is defined twice", strings.Join(keys, "."))
			}
			if array {
				seen["[["+id] = true
			} else {
				seen["["+id] = true
			}
			parent, err := tomlDescend(root, keys[:len(keys)-1])
			if err != nil {
				return nil, p.errf("%v", err)
			}
			last := keys[len(keys)-1]
			if array {
				nm := map[string]any{}
				switch l := parent[last].(type) {
				case nil:
					parent[last] = []any{nm}
				case []any:
					parent[last] = append(l, nm)
				default:
					return nil, p.errf("key %q is not a table array", last)
				}
				cur = nm
			} else if cur, err = tomlDescend(parent, []string{last}); err != nil {
				return nil, p.errf("%v", err)
			}
		} else if err := p.assignment(cur); err != nil {
			return nil, err
		}
		if err := p.endOfLine(); err != nil {
			return nil, err
		}
	}
}

func tomlValue(content []byte, key string) (any, bool, error) {
	root, err := tomlParse(content)
	if err != nil {
		return nil, false, err
	}
	v, ok := walkKey(root, key)
	return v, ok, nil
}
