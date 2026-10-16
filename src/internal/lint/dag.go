package lint

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

type dagNode struct {
	id    string
	plans []string
	deps  []string
	after []string
}

type dagFile struct {
	nodes      []dagNode
	components map[string]string // tag -> introducing node
	hasComps   bool
}

// splitTop splits s on commas outside brackets and quotes.
func splitTop(s string) []string {
	var out []string
	depth, start := 0, 0
	var quote byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '[' || c == '{':
			depth++
		case c == ']' || c == '}':
			depth--
		case c == ',' && depth == 0:
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

func unquote(s string) string { return strings.Trim(strings.TrimSpace(s), `"'`) }

func parseList(v string) []string {
	v = strings.TrimSpace(v)
	v = strings.TrimSuffix(strings.TrimPrefix(v, "["), "]")
	var out []string
	for _, e := range splitTop(v) {
		if e = unquote(e); e != "" {
			out = append(out, e)
		}
	}
	return out
}

// parseFlowMap parses the flow mapping used for dag.yaml nodes: {k: v, k: [a, b]}.
func parseFlowMap(s string) (map[string]string, error) {
	s = strings.TrimSpace(s)
	i, j := strings.Index(s, "{"), strings.LastIndex(s, "}")
	if i < 0 || j < i {
		return nil, fmt.Errorf("not a flow mapping: %q", s)
	}
	m := map[string]string{}
	for _, part := range splitTop(s[i+1 : j]) {
		k, v, ok := strings.Cut(part, ":")
		if !ok {
			return nil, fmt.Errorf("bad entry %q", part)
		}
		m[unquote(k)] = strings.TrimSpace(v)
	}
	return m, nil
}

func parseDag(body string) (dagFile, error) {
	d := dagFile{components: map[string]string{}}
	section := ""
	for n, raw := range strings.Split(body, "\n") {
		t := strings.TrimSpace(raw)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		if raw[0] != ' ' && raw[0] != '-' {
			k, v, _ := strings.Cut(t, ":")
			section = k
			if k == "components" {
				d.hasComps = true
			}
			if v = strings.TrimSpace(v); v != "" && !strings.HasPrefix(v, "#") && k == "nodes" {
				return d, fmt.Errorf("line %d: nodes must be a block list", n+1)
			}
			continue
		}
		switch section {
		case "nodes":
			if !strings.HasPrefix(t, "- ") {
				return d, fmt.Errorf("line %d: expected a node entry", n+1)
			}
			m, err := parseFlowMap(strings.TrimPrefix(t, "- "))
			if err != nil {
				return d, fmt.Errorf("line %d: %v", n+1, err)
			}
			d.nodes = append(d.nodes, dagNode{id: unquote(m["id"]), plans: parseList(m["plans"]), deps: parseList(m["deps"]), after: parseList(m["after"])})
		case "components":
			k, v, ok := strings.Cut(t, ":")
			if !ok {
				return d, fmt.Errorf("line %d: expected tag: node", n+1)
			}
			if c := strings.Index(v, " #"); c >= 0 {
				v = v[:c]
			}
			d.components[unquote(k)] = unquote(v)
		}
	}
	return d, nil
}

var (
	gateRE   = regexp.MustCompile(`^#{1,6}\s+GATE-([A-Za-z][A-Za-z0-9]*)`)
	acDefRE  = regexp.MustCompile(`^[-*]\s+(?:\[[ xX]\]\s+)?\*\*(\d\d-AC-\d+[a-z]?)\*\*`)
	usesRE   = regexp.MustCompile(`<!--\s*uses:\s*([^>]*?)\s*-->`)
	planFile = regexp.MustCompile(`^(\d\d)-.*\.md$`)
)

// DagCheck validates plans/dag.yaml against the plans (plan 01 section 7.1, dag-check).
func DagCheck(root string) (Result, error) {
	var r Result
	for _, id := range []string{"DAG_CYCLE", "DAG_UNKNOWN_NODE", "DAG_GATE_MISSING", "DAG_GATE_UNLISTED", "DAG_AC_DUPLICATE"} {
		r.ran(id)
	}
	body, err := os.ReadFile(filepath.Join(root, "plans", "dag.yaml"))
	if err != nil {
		return r, err
	}
	d, err := parseDag(string(body))
	if err != nil {
		return r, fmt.Errorf("plans/dag.yaml: %v", err)
	}
	byID := map[string]*dagNode{}
	for i := range d.nodes {
		n := &d.nodes[i]
		if byID[n.id] != nil {
			r.add("DAG_UNKNOWN_NODE", "plans/dag.yaml", "duplicate node id %q", n.id)
		}
		byID[n.id] = n
	}

	// (a) known ids and acyclic.
	for _, n := range d.nodes {
		for _, ref := range append(append([]string{}, n.deps...), n.after...) {
			if byID[ref] == nil {
				r.add("DAG_UNKNOWN_NODE", "plans/dag.yaml", "node %s references unknown node %s", n.id, ref)
			}
		}
	}
	const (
		white = iota
		grey
		black
	)
	color := map[string]int{}
	var visit func(id string, stack []string)
	reported := false
	visit = func(id string, stack []string) {
		color[id] = grey
		for _, dep := range append(append([]string{}, byID[id].deps...), byID[id].after...) {
			if byID[dep] == nil {
				continue
			}
			switch color[dep] {
			case grey:
				if !reported {
					reported = true
					r.add("DAG_CYCLE", "plans/dag.yaml", "dependency cycle: %s -> %s", strings.Join(append(stack, id), " -> "), dep)
				}
			case white:
				visit(dep, append(stack, id))
			}
		}
		color[id] = black
	}
	for _, n := range d.nodes {
		if color[n.id] == white {
			visit(n.id, nil)
		}
	}

	// Plans: gate headings, AC definitions, uses markers.
	files, _ := filepath.Glob(filepath.Join(root, "plans", "*.md"))
	sort.Strings(files)
	gates := map[string]map[string]bool{} // plan -> node -> present
	acSeen := map[string]string{}
	type use struct{ plan, file, node, tags string }
	var uses []use
	for _, f := range files {
		m := planFile.FindStringSubmatch(filepath.Base(f))
		if m == nil {
			continue
		}
		plan := m[1]
		rel := relPath(root, f)
		b, err := os.ReadFile(f)
		if err != nil {
			return r, err
		}
		if gates[plan] == nil {
			gates[plan] = map[string]bool{}
		}
		node, inFence := "", false
		for _, l := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(l, "```") {
				inFence = !inFence
			}
			if inFence {
				continue
			}
			if g := gateRE.FindStringSubmatch(l); g != nil {
				node = g[1]
				gates[plan][node] = true
				continue
			} else if strings.HasPrefix(l, "#") {
				node = ""
			}
			if a := acDefRE.FindStringSubmatch(strings.TrimSpace(l)); a != nil {
				if prev, dup := acSeen[a[1]]; dup {
					r.add("DAG_AC_DUPLICATE", rel, "%s already defined in %s", a[1], prev)
				} else {
					acSeen[a[1]] = rel
				}
				if u := usesRE.FindStringSubmatch(l); u != nil && node != "" {
					uses = append(uses, use{plan, rel, node, u[1]})
				}
			}
		}
	}

	// (b) gate headings both ways.
	for _, n := range d.nodes {
		for _, p := range n.plans {
			if !gates[p][n.id] {
				r.add("DAG_GATE_MISSING", "plans/dag.yaml", "node %s lists plan %s but plan %s has no \"### GATE-%s\" section", n.id, p, p, n.id)
			}
		}
	}
	plansSorted := make([]string, 0, len(gates))
	for p := range gates {
		plansSorted = append(plansSorted, p)
	}
	sort.Strings(plansSorted)
	for _, p := range plansSorted {
		ids := make([]string, 0, len(gates[p]))
		for id := range gates[p] {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			listed := false
			if n := byID[id]; n != nil {
				for _, np := range n.plans {
					listed = listed || np == p
				}
			}
			if !listed {
				r.add("DAG_GATE_UNLISTED", "plans/"+p, "plan %s defines GATE-%s but node %s does not list plan %s", p, id, id, p)
			}
		}
	}

	// uses markers (warning only).
	if d.hasComps {
		for _, u := range uses {
			if byID[u.node] == nil {
				continue
			}
			up := upstream(byID, u.node)
			for _, tag := range parseList(u.tags) {
				owner, known := d.components[tag]
				switch {
				case !known:
					r.warn("DAG_USES_UPSTREAM", u.file, "node %s uses unknown component %q", u.node, tag)
				case owner != u.node && !up[owner]:
					r.warn("DAG_USES_UPSTREAM", u.file, "node %s uses component %q introduced by %s, which is not upstream", u.node, tag, owner)
				}
			}
		}
	}
	return r, nil
}

func upstream(byID map[string]*dagNode, id string) map[string]bool {
	seen := map[string]bool{}
	var walk func(string)
	walk = func(x string) {
		n := byID[x]
		if n == nil {
			return
		}
		for _, dep := range append(append([]string{}, n.deps...), n.after...) {
			if !seen[dep] {
				seen[dep] = true
				walk(dep)
			}
		}
	}
	walk(id)
	return seen
}
