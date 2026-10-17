package contracts

import (
	"bytes"
	"fmt"
	"strings"

	"go.yaml.in/yaml/v3"
)

// dupKeyError reports a repeated mapping key (CONTRACT_YAML_DUPLICATE_KEY).
type dupKeyError struct {
	key  string
	line int
}

func (e *dupKeyError) Error() string {
	return fmt.Sprintf("duplicate mapping key %q at line %d", e.key, e.line)
}

// parseYAML decodes one YAML document into JSON-compatible values (maps with string keys, []any, string,
// bool, int, float64, nil). Timestamps stay strings (YAML 1.2 core schema); duplicate keys are an error.
func parseYAML(b []byte) (any, error) {
	var n yaml.Node
	if err := yaml.NewDecoder(bytes.NewReader(b)).Decode(&n); err != nil {
		return nil, err
	}
	return convert(&n)
}

func convert(n *yaml.Node) (any, error) {
	switch n.Kind {
	case yaml.DocumentNode:
		if len(n.Content) == 0 {
			return nil, nil
		}
		return convert(n.Content[0])
	case yaml.AliasNode:
		return convert(n.Alias)
	case yaml.MappingNode:
		m := make(map[string]any, len(n.Content)/2)
		for i := 0; i+1 < len(n.Content); i += 2 {
			k := n.Content[i]
			if k.Kind != yaml.ScalarNode {
				return nil, fmt.Errorf("line %d: non-scalar mapping key", k.Line)
			}
			if _, dup := m[k.Value]; dup {
				return nil, &dupKeyError{key: k.Value, line: k.Line}
			}
			v, err := convert(n.Content[i+1])
			if err != nil {
				return nil, err
			}
			m[k.Value] = v
		}
		return m, nil
	case yaml.SequenceNode:
		s := make([]any, 0, len(n.Content))
		for _, c := range n.Content {
			v, err := convert(c)
			if err != nil {
				return nil, err
			}
			s = append(s, v)
		}
		return s, nil
	case yaml.ScalarNode:
		switch n.ShortTag() {
		case "!!timestamp", "!!binary":
			return n.Value, nil
		case "!!null":
			return nil, nil
		}
		var v any
		if err := n.Decode(&v); err != nil {
			return nil, err
		}
		return v, nil
	}
	return nil, fmt.Errorf("line %d: unsupported YAML node", n.Line)
}

// oneLine collapses an error message to one line for report details.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
