package verify

import (
	"fmt"
	"sort"
	"strings"
)

// iniValue reads an INI file as pytest's iniconfig does (decision D18 c): a comment line starts with # or ;, a
// section name is the raw text between "[" and "]" (not trimmed), a value is stripped at its ends only and keeps its
// interior whitespace, an indented line continues the previous value. A line iniconfig would reject, a duplicate
// section or a duplicate name is an error. It returns the sorted name=value items of the section named key.
func iniValue(content []byte, key string) (any, bool, error) {
	sections := map[string]map[string]string{}
	var cur map[string]string
	curName, lastName := "", ""
	for n, raw := range strings.Split(string(content), "\n") {
		raw = strings.TrimRight(raw, "\r")
		if c := strings.TrimLeft(raw, " \t\f\v"); strings.HasPrefix(c, "#") || strings.HasPrefix(c, ";") {
			continue
		}
		line := strings.TrimRight(raw, " \t\f\v")
		if line == "" {
			continue
		}
		switch {
		case line[0] == '[':
			head := line
			for _, c := range []string{"#", ";"} {
				head, _, _ = strings.Cut(head, c)
				head = strings.TrimRight(head, " \t\f\v")
			}
			if !strings.HasSuffix(head, "]") {
				return nil, false, fmt.Errorf("line %d: unterminated section header", n+1)
			}
			curName = head[1 : len(head)-1]
			if _, dup := sections[curName]; dup {
				return nil, false, fmt.Errorf("line %d: duplicate section %q", n+1, curName)
			}
			cur = map[string]string{}
			sections[curName] = cur
			lastName = ""
		case line[0] != ' ' && line[0] != '\t' && line[0] != '\f' && line[0] != '\v':
			if cur == nil {
				return nil, false, fmt.Errorf("line %d: no section header defined", n+1)
			}
			name, value, ok := strings.Cut(line, "=")
			if !ok || strings.Contains(name, ":") {
				name, value, ok = strings.Cut(line, ":")
			}
			if !ok {
				return nil, false, fmt.Errorf("line %d: unexpected line", n+1)
			}
			name = strings.TrimSpace(name)
			if _, dup := cur[name]; dup {
				return nil, false, fmt.Errorf("line %d: duplicate name %q", n+1, name)
			}
			cur[name] = strings.TrimSpace(value)
			lastName = name
		default:
			if cur == nil || lastName == "" {
				return nil, false, fmt.Errorf("line %d: continuation without a value", n+1)
			}
			cur[lastName] += "\n" + strings.TrimSpace(line)
		}
	}
	sec, ok := sections[key]
	if !ok {
		return nil, false, nil
	}
	items := make([]string, 0, len(sec))
	for k, v := range sec {
		items = append(items, k+"="+v)
	}
	sort.Strings(items)
	out := make([]any, len(items))
	for i, s := range items {
		out[i] = s
	}
	return out, true, nil
}
