package verify

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"
)

// Semantic keys of discovery-protected configuration files (plan 22 sections 14.5 and 14.6). A key's value is
// reduced to a canonical JSON value and compared by its sha256, so a rewrite that keeps every protected value
// (reformatting, key order, comments, other keys) is not a change, and anything else is. A file that cannot be
// parsed has no value: callers treat it as changed (fail closed).

// semanticValueHash returns the sha256 of the canonical JSON of the value at key in the file content, nil when the
// key is absent. A nil content is an absent file. format is "json", "ini" or "toml"; go.mod uses the directive form.
func semanticValueHash(format, file string, content []byte, key string) (*string, error) {
	if content == nil {
		return nil, nil
	}
	var v any
	var found bool
	var err error
	switch {
	case path.Base(file) == "go.mod":
		v, found, err = goModValue(content, key)
	case format == "json":
		v, found, err = jsonValue(content, key)
	case format == "toml":
		v, found, err = tomlValue(content, key)
	case format == "ini":
		v, found, err = iniValue(content, key)
	default:
		return nil, fmt.Errorf("unknown semantic key format %q", format)
	}
	if err != nil || !found {
		return nil, err
	}
	h, err := canonicalHash(v)
	if err != nil {
		return nil, err
	}
	return &h, nil
}

// canonicalHash is sha256 over the compact JSON of v (map keys sorted, no HTML escaping).
func canonicalHash(v any) (string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return "", err
	}
	sum := sha256.Sum256(bytes.TrimSuffix(buf.Bytes(), []byte("\n")))
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func walkKey(root any, key string) (any, bool) {
	cur := root
	for _, part := range strings.Split(strings.Trim(key, "[]"), ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = m[part]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

func jsonValue(content []byte, key string) (any, bool, error) {
	dec := json.NewDecoder(bytes.NewReader(content))
	dec.UseNumber()
	var root any
	if err := dec.Decode(&root); err != nil {
		return nil, false, err
	}
	if dec.More() {
		return nil, false, fmt.Errorf("trailing data after the JSON document")
	}
	if _, ok := root.(map[string]any); !ok {
		return nil, false, fmt.Errorf("the JSON document is not an object")
	}
	v, ok := walkKey(root, key)
	return v, ok, nil
}

// goModValue returns the sorted, whitespace-normalised items of a go.mod directive (module, replace, ...).
func goModValue(content []byte, key string) (any, bool, error) {
	var items []any
	inBlock := ""
	for _, raw := range strings.Split(string(content), "\n") {
		line := raw
		if i := strings.Index(line, "//"); i >= 0 {
			line = line[:i]
		}
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		if inBlock != "" {
			if f[0] == ")" {
				inBlock = ""
				continue
			}
			if inBlock == key {
				items = append(items, strings.Join(f, " "))
			}
			continue
		}
		dir := f[0]
		switch {
		case len(f) == 2 && f[1] == "(":
			inBlock = dir
		case dir == key:
			items = append(items, strings.Join(f[1:], " "))
		}
	}
	if inBlock != "" {
		return nil, false, fmt.Errorf("go.mod block %q is not closed", inBlock)
	}
	if len(items) == 0 {
		return nil, false, nil
	}
	sort.Slice(items, func(i, j int) bool { return items[i].(string) < items[j].(string) })
	return items, true, nil
}

// iniValue returns the normalised key=value items of the section named key.
func iniValue(content []byte, key string) (any, bool, error) {
	var items []string
	found := false
	in := false
	last := -1
	for _, raw := range strings.Split(string(content), "\n") {
		line := strings.TrimRight(raw, "\r")
		trim := strings.TrimSpace(line)
		if trim == "" || strings.HasPrefix(trim, "#") || strings.HasPrefix(trim, ";") {
			continue
		}
		if strings.HasPrefix(trim, "[") {
			if !strings.HasSuffix(trim, "]") {
				return nil, false, fmt.Errorf("malformed section header %q", trim)
			}
			in = strings.TrimSpace(trim[1:len(trim)-1]) == key
			found = found || in
			last = -1
			continue
		}
		if !in {
			continue
		}
		if (line[0] == ' ' || line[0] == '\t') && last >= 0 {
			items[last] += " " + strings.Join(strings.Fields(trim), " ")
			continue
		}
		k, v, ok := strings.Cut(trim, "=")
		if !ok {
			k, v, ok = strings.Cut(trim, ":")
		}
		if !ok {
			items = append(items, strings.TrimSpace(trim))
		} else {
			items = append(items, strings.TrimSpace(k)+"="+strings.Join(strings.Fields(v), " "))
		}
		last = len(items) - 1
	}
	if !found {
		return nil, false, nil
	}
	sort.Strings(items)
	out := make([]any, len(items))
	for i, s := range items {
		out[i] = s
	}
	return out, true, nil
}

// ---- TOML (the subset the protected keys need: tables, arrays of tables, dotted keys, inline tables, arrays and
// scalars; scalar and array values are compared by their whitespace-free source text) ----

// tomlStatements splits TOML source into logical statements: comments dropped, newlines inside brackets and
// multi-line strings kept. It fails on an unterminated string or bracket.
func tomlStatements(src string) ([]string, error) {
	var out []string
	var cur strings.Builder
	depth := 0
	flush := func() {
		if s := strings.TrimSpace(cur.String()); s != "" {
			out = append(out, s)
		}
		cur.Reset()
	}
	i := 0
	for i < len(src) {
		c := src[i]
		switch {
		case c == '#':
			for i < len(src) && src[i] != '\n' {
				i++
			}
			continue
		case c == '"' || c == '\'':
			q := string(c)
			if strings.HasPrefix(src[i:], q+q+q) {
				end := strings.Index(src[i+3:], q+q+q)
				if end < 0 {
					return nil, fmt.Errorf("unterminated multi-line string")
				}
				cur.WriteString(src[i : i+3+end+3])
				i += 3 + end + 3
				continue
			}
			j := i + 1
			for j < len(src) && src[j] != '\n' && src[j] != c {
				if c == '"' && src[j] == '\\' {
					j++
				}
				j++
			}
			if j >= len(src) || src[j] != c {
				return nil, fmt.Errorf("unterminated string")
			}
			cur.WriteString(src[i : j+1])
			i = j + 1
			continue
		case c == '[' || c == '{':
			depth++
		case c == ']' || c == '}':
			depth--
			if depth < 0 {
				// A table header closes its own bracket: handled below by the header branch.
				depth = 0
			}
		case c == '\n':
			if depth == 0 {
				flush()
				i++
				continue
			}
		}
		cur.WriteByte(c)
		i++
	}
	if depth != 0 {
		return nil, fmt.Errorf("unbalanced brackets")
	}
	flush()
	return out, nil
}

// tomlSplitDotted splits a (possibly quoted) dotted key.
func tomlSplitDotted(k string) ([]string, error) {
	var parts []string
	var cur strings.Builder
	for i := 0; i < len(k); i++ {
		c := k[i]
		switch {
		case c == '"' || c == '\'':
			j := strings.IndexByte(k[i+1:], c)
			if j < 0 {
				return nil, fmt.Errorf("unterminated quoted key")
			}
			cur.WriteString(k[i+1 : i+1+j])
			i += 1 + j
		case c == '.':
			parts = append(parts, strings.TrimSpace(cur.String()))
			cur.Reset()
		case c == ' ' || c == '\t':
		default:
			cur.WriteByte(c)
		}
	}
	parts = append(parts, strings.TrimSpace(cur.String()))
	for _, p := range parts {
		if p == "" {
			return nil, fmt.Errorf("empty key part in %q", k)
		}
	}
	return parts, nil
}

// tomlCompact removes whitespace outside strings.
func tomlCompact(v string) string {
	var b strings.Builder
	for i := 0; i < len(v); i++ {
		c := v[i]
		switch {
		case c == '"' || c == '\'':
			q := string(c)
			if strings.HasPrefix(v[i:], q+q+q) {
				end := strings.Index(v[i+3:], q+q+q)
				if end < 0 {
					end = len(v) - i - 6
				}
				b.WriteString(v[i : i+3+end+3])
				i += 3 + end + 2
				continue
			}
			j := i + 1
			for j < len(v) && v[j] != c {
				if c == '"' && v[j] == '\\' {
					j++
				}
				j++
			}
			if j >= len(v) {
				j = len(v) - 1
			}
			b.WriteString(v[i : j+1])
			i = j
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// tomlSplitTop splits s at the separator outside strings and brackets.
func tomlSplitTop(s string, sep byte) []string {
	var parts []string
	depth, start := 0, 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"' || c == '\'':
			j := i + 1
			for j < len(s) && s[j] != c {
				if c == '"' && s[j] == '\\' {
					j++
				}
				j++
			}
			i = j
		case c == '[' || c == '{':
			depth++
		case c == ']' || c == '}':
			depth--
		case c == sep && depth == 0:
			parts = append(parts, s[start:i])
			start = i + 1
		}
	}
	return append(parts, s[start:])
}

// tomlValueOf parses a value: an inline table becomes a map, anything else its compact source text.
func tomlValueOf(v string) (any, error) {
	v = strings.TrimSpace(v)
	if strings.HasPrefix(v, "{") {
		if !strings.HasSuffix(v, "}") {
			return nil, fmt.Errorf("malformed inline table")
		}
		m := map[string]any{}
		inner := strings.TrimSpace(v[1 : len(v)-1])
		if inner == "" {
			return m, nil
		}
		for _, item := range tomlSplitTop(inner, ',') {
			if strings.TrimSpace(item) == "" {
				continue
			}
			if err := tomlAssign(m, item); err != nil {
				return nil, err
			}
		}
		return m, nil
	}
	return tomlCompact(v), nil
}

// tomlAssign handles `dotted.key = value` inside the table m.
func tomlAssign(m map[string]any, stmt string) error {
	eq := -1
	for i := 0; i < len(stmt) && eq < 0; i++ {
		c := stmt[i]
		switch {
		case c == '"' || c == '\'':
			j := strings.IndexByte(stmt[i+1:], c)
			if j < 0 {
				return fmt.Errorf("unterminated quoted key")
			}
			i += 1 + j
		case c == '=':
			eq = i
		}
	}
	if eq < 0 {
		return fmt.Errorf("expected key = value, got %q", stmt)
	}
	keys, err := tomlSplitDotted(stmt[:eq])
	if err != nil {
		return err
	}
	val, err := tomlValueOf(stmt[eq+1:])
	if err != nil {
		return err
	}
	cur := m
	for _, k := range keys[:len(keys)-1] {
		next, ok := cur[k].(map[string]any)
		if !ok {
			next = map[string]any{}
			cur[k] = next
		}
		cur = next
	}
	cur[keys[len(keys)-1]] = val
	return nil
}

func tomlParse(content []byte) (map[string]any, error) {
	stmts, err := tomlStatements(string(content))
	if err != nil {
		return nil, err
	}
	root := map[string]any{}
	cur := root
	descend := func(m map[string]any, keys []string) (map[string]any, error) {
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
	for _, st := range stmts {
		switch {
		case strings.HasPrefix(st, "[["):
			if !strings.HasSuffix(st, "]]") {
				return nil, fmt.Errorf("malformed table array header %q", st)
			}
			keys, err := tomlSplitDotted(st[2 : len(st)-2])
			if err != nil {
				return nil, err
			}
			parent, err := descend(root, keys[:len(keys)-1])
			if err != nil {
				return nil, err
			}
			nm := map[string]any{}
			list, _ := parent[keys[len(keys)-1]].([]any)
			parent[keys[len(keys)-1]] = append(list, nm)
			cur = nm
		case strings.HasPrefix(st, "["):
			if !strings.HasSuffix(st, "]") {
				return nil, fmt.Errorf("malformed table header %q", st)
			}
			keys, err := tomlSplitDotted(st[1 : len(st)-1])
			if err != nil {
				return nil, err
			}
			if cur, err = descend(root, keys); err != nil {
				return nil, err
			}
		default:
			if err := tomlAssign(cur, st); err != nil {
				return nil, err
			}
		}
	}
	return root, nil
}

func tomlValue(content []byte, key string) (any, bool, error) {
	root, err := tomlParse(content)
	if err != nil {
		return nil, false, err
	}
	v, ok := walkKey(root, key)
	return v, ok, nil
}
