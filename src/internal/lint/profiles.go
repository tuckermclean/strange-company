package lint

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Rule ID of `sc-lint profiles` (plan 03 section 9.1, 03-AC-2).
const ruleProfilesMatch = "profiles-match"

// profileBlocks maps each profile file to the SPEC heading prefix whose first yaml block it must equal.
var profileBlocks = []struct{ file, heading string }{
	{"infra/profiles/self-hosting-minimum.yaml", "# 11. "},
	{"infra/profiles/small-ha.yaml", "# 12. "},
}

// specYAMLBlock returns the first ```yaml fenced block after the heading line starting with heading.
func specYAMLBlock(spec, heading string) (string, error) {
	lines := strings.Split(spec, "\n")
	for i, l := range lines {
		if !strings.HasPrefix(l, heading) {
			continue
		}
		var block []string
		in := false
		for _, m := range lines[i+1:] {
			switch {
			case !in && strings.HasPrefix(m, "# "):
				return "", fmt.Errorf("no yaml block under %q", strings.TrimSpace(l))
			case !in && strings.TrimSpace(m) == "```yaml":
				in = true
			case in && strings.HasPrefix(m, "```"):
				return strings.Join(block, "\n") + "\n", nil
			case in:
				block = append(block, m)
			}
		}
		return "", fmt.Errorf("unterminated yaml block under %q", strings.TrimSpace(l))
	}
	return "", fmt.Errorf("heading %q not found", strings.TrimSpace(heading))
}

// ProfilesCheck compares infra/profiles/{self-hosting-minimum,small-ha}.yaml with the SPEC section 11/12 blocks.
// The comparison is on parsed YAML, so comments, key order and layout are free.
func ProfilesCheck(root string) (Result, error) {
	var r Result
	r.ran(ruleProfilesMatch)
	spec, err := os.ReadFile(filepath.Join(root, "SPEC.md"))
	if err != nil {
		return r, err
	}
	for _, p := range profileBlocks {
		block, err := specYAMLBlock(string(spec), p.heading)
		if err != nil {
			r.add(ruleProfilesMatch, "SPEC.md", "%v", err)
			continue
		}
		var want any
		if err := yaml.Unmarshal([]byte(block), &want); err != nil {
			r.add(ruleProfilesMatch, "SPEC.md", "block %q: %v", strings.TrimSpace(p.heading), err)
			continue
		}
		b, err := os.ReadFile(filepath.Join(root, p.file))
		if err != nil {
			r.add(ruleProfilesMatch, p.file, "cannot read profile: %v", err)
			continue
		}
		var got any
		if err := yaml.Unmarshal(b, &got); err != nil {
			r.add(ruleProfilesMatch, p.file, "invalid YAML: %v", err)
			continue
		}
		if !reflect.DeepEqual(got, want) {
			r.add(ruleProfilesMatch, p.file, "differs from the SPEC.md %ssection block", p.heading)
		}
	}
	return r, nil
}
