package verify

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"path"
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
	if err := checkLineTerminators(content); err != nil {
		return nil, err
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
	b, err := jsonMarshalNoEscape(v)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// jsonMarshalNoEscape is the compact JSON of v without HTML escaping and without the trailing newline.
func jsonMarshalNoEscape(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
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
	if _, err := dec.Token(); err != io.EOF {
		return nil, false, fmt.Errorf("trailing data after the JSON document")
	}
	if _, ok := root.(map[string]any); !ok {
		return nil, false, fmt.Errorf("the JSON document is not an object")
	}
	v, ok := walkKey(root, key)
	return v, ok, nil
}

// checkLineTerminators rejects a file with a line terminator other than "\n" or "\r\n" (a lone CR, VT, FF,
// U+001C to U+001E, U+0085, U+2028, U+2029) or a NUL (decision D19 a): readers disagree on where such a file breaks
// into lines, so no recogniser trusts one. A U+2028 inside a JSON string is rejected too (fails closed).
func checkLineTerminators(content []byte) error {
	for i := 0; i < len(content); i++ {
		switch c := content[i]; {
		case c == 0:
			return fmt.Errorf("a NUL byte at offset %d", i)
		case c == '\r':
			if i+1 >= len(content) || content[i+1] != '\n' {
				return fmt.Errorf("a lone CR at offset %d", i)
			}
		case c == '\v' || c == '\f' || c >= 0x1c && c <= 0x1e:
			return fmt.Errorf("a line terminator (0x%02x) at offset %d", c, i)
		}
	}
	if bytes.Contains(content, []byte("\u0085")) || bytes.Contains(content, []byte("\u2028")) || bytes.Contains(content, []byte("\u2029")) {
		return fmt.Errorf("a Unicode line terminator (U+0085, U+2028 or U+2029)")
	}
	return nil
}
