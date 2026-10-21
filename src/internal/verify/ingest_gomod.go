package verify

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// go.mod is read as a fail-closed recogniser (decision D18 a): it is tokenised like golang.org/x/mod/modfile
// ("(" and ")" are tokens of their own, so `replace(` opens a block; `//` starts a comment; module paths may be
// quoted), and any line that is not a known directive, a block open, a block close or a block entry is an error.

var goModDirectives = map[string]bool{
	"module": true, "go": true, "toolchain": true, "godebug": true, "require": true, "exclude": true,
	"replace": true, "retract": true, "tool": true, "ignore": true,
}

// goModTokens splits one line into tokens; a comment ends the line.
func goModTokens(line string) ([]string, error) {
	var toks []string
	i := 0
	for i < len(line) {
		c := line[i]
		switch {
		case c == ' ' || c == '\t' || c == '\r':
			i++
		case c == '/' && strings.HasPrefix(line[i:], "//"):
			return toks, nil
		case c == '(' || c == ')':
			toks = append(toks, string(c))
			i++
		case c == '"':
			j := i + 1
			for j < len(line) && line[j] != '"' {
				if line[j] == '\\' {
					j++
				}
				j++
			}
			if j >= len(line) {
				return nil, fmt.Errorf("unterminated quoted string in %q", line)
			}
			u, err := strconv.Unquote(line[i : j+1])
			if err != nil {
				return nil, fmt.Errorf("bad quoted string in %q: %w", line, err)
			}
			toks = append(toks, u)
			i = j + 1
		case c == '`':
			j := strings.IndexByte(line[i+1:], '`')
			if j < 0 {
				return nil, fmt.Errorf("unterminated raw string in %q", line)
			}
			toks = append(toks, line[i+1:i+1+j])
			i += j + 2
		default:
			j := i
			for j < len(line) && !strings.ContainsRune(" \t\r()", rune(line[j])) && !strings.HasPrefix(line[j:], "//") {
				j++
			}
			toks = append(toks, line[i:j])
			i = j
		}
	}
	return toks, nil
}

// goModEntryOK checks the shape of one directive's arguments.
func goModEntryOK(verb string, args []string) error {
	for _, a := range args {
		if a == "(" || a == ")" {
			return fmt.Errorf("unexpected %q in %s", a, verb)
		}
	}
	switch verb {
	case "module", "go", "toolchain":
		if len(args) != 1 {
			return fmt.Errorf("%s takes one argument", verb)
		}
	case "require", "exclude":
		if len(args) != 2 {
			return fmt.Errorf("%s takes a path and a version", verb)
		}
	case "replace":
		arrow := -1
		for i, a := range args {
			if a == "=>" {
				arrow = i
			}
		}
		if (arrow != 1 && arrow != 2) || len(args)-arrow-1 < 1 || len(args)-arrow-1 > 2 {
			return fmt.Errorf("replace needs old [version] => new [version]")
		}
	default:
		if len(args) == 0 {
			return fmt.Errorf("%s needs an argument", verb)
		}
	}
	return nil
}

// goModValue returns the sorted items of a go.mod directive (module, replace, ...).
func goModValue(content []byte, key string) (any, bool, error) {
	var items []any
	block := ""
	for n, raw := range strings.Split(string(content), "\n") {
		toks, err := goModTokens(raw)
		if err != nil {
			return nil, false, fmt.Errorf("go.mod line %d: %w", n+1, err)
		}
		if len(toks) == 0 {
			continue
		}
		if block != "" {
			if len(toks) == 1 && toks[0] == ")" {
				block = ""
				continue
			}
			if err := goModEntryOK(block, toks); err != nil {
				return nil, false, fmt.Errorf("go.mod line %d: %w", n+1, err)
			}
			if block == key {
				items = append(items, strings.Join(toks, " "))
			}
			continue
		}
		verb := toks[0]
		if !goModDirectives[verb] {
			return nil, false, fmt.Errorf("go.mod line %d: unknown directive %q", n+1, verb)
		}
		if len(toks) == 2 && toks[1] == "(" {
			block = verb
			continue
		}
		if err := goModEntryOK(verb, toks[1:]); err != nil {
			return nil, false, fmt.Errorf("go.mod line %d: %w", n+1, err)
		}
		if verb == key {
			items = append(items, strings.Join(toks[1:], " "))
		}
	}
	if block != "" {
		return nil, false, fmt.Errorf("go.mod block %q is not closed", block)
	}
	if len(items) == 0 {
		return nil, false, nil
	}
	sort.Slice(items, func(i, j int) bool { return items[i].(string) < items[j].(string) })
	return items, true, nil
}
