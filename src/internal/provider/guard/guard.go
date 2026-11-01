// Package guard is the provider-neutral plan-JSON allowlist guard of plan 03 section 14.4 (ADR-0102): it judges
// `tofu show -json` output against a TofuPlanGuard file and computes the plan digest the operator confirms.
package guard

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Guard result codes (plan 03 section 14.4).
const (
	CodePlanFormatUnsupported = "GUARD_PLAN_FORMAT_UNSUPPORTED"
	CodeProviderForbidden     = "GUARD_PROVIDER_FORBIDDEN"
	CodeUnexpectedResource    = "GUARD_UNEXPECTED_RESOURCE"
	CodeActionForbidden       = "GUARD_ACTION_FORBIDDEN"
	CodeReplaceForbidden      = "GUARD_REPLACE_FORBIDDEN"
	CodeCountExceeded         = "GUARD_COUNT_EXCEEDED"
	CodeProductMismatch       = "GUARD_PRODUCT_MISMATCH"
	CodeRegionMismatch        = "GUARD_REGION_MISMATCH"
	CodeSSHKeyMismatch        = "GUARD_SSH_KEY_MISMATCH"
	// CodeRuleUndeclared is not a plan code: a paid guard was evaluated without declared rule 6 attributes.
	CodeRuleUndeclared = "GUARD_RULE_UNDECLARED"
)

const expectedNodeCountLiteral = "expectedNodeCount"

// supportedFormatMajors is the allowlist of plan format_version major versions the guard was tested with.
var supportedFormatMajors = map[string]bool{"1": true}

type resourceRule struct {
	Type             string   `yaml:"type"`
	Paid             *bool    `yaml:"paid"`
	AllowedActions   []string `yaml:"allowedActions"`
	MaxCount         any      `yaml:"maxCount"`
	PriceProductFrom string   `yaml:"priceProductFrom"`
}

func (r resourceRule) isPaid() bool { return r.Paid != nil && *r.Paid }

type guardSpec struct {
	RootModule       string         `yaml:"rootModule"`
	AllowedProviders []string       `yaml:"allowedProviders"`
	ForbidReplace    *bool          `yaml:"forbidReplace"`
	Resources        []resourceRule `yaml:"resources"`
}

type guardDoc struct {
	APIVersion string `yaml:"apiVersion"`
	Kind       string `yaml:"kind"`
	Metadata   struct {
		Name string `yaml:"name"`
	} `yaml:"metadata"`
	Spec guardSpec `yaml:"spec"`
}

// Guard is a parsed TofuPlanGuard (contracts/provider.schema.yaml).
type Guard struct {
	spec guardSpec
	byTy map[string]resourceRule
}

// Attr names a provider-specific attribute that must equal configuration (rule 6): Path is a dotted path with
// optional [n] indexes and [?key=="value"] selectors (the one list element whose key equals value; no match, several
// matches or an unknown candidate fail closed), evaluated on the planned `after` value of every resource of Type.
type Attr struct {
	Type string
	Path string
	Want string
}

// Expect is the configuration side of the guard decision.
type Expect struct {
	// ExpectedNodeCount resolves a maxCount of `expectedNodeCount`.
	ExpectedNodeCount int
	// ProductCode is the catalogue planCode of MACHINE_CLASS (rule 5).
	ProductCode string
	Region      Attr
	SSHKey      Attr
	// Destroy selects guard-destroy semantics (rule 9).
	Destroy bool
}

// Verdict is the guard result: Pass, or the first failing rule's Code and the offending resource Address.
// For GUARD_RULE_UNDECLARED the Address is the literal "rule6", not a resource address.
type Verdict struct {
	Pass    bool
	Code    string
	Address string
	Message string
}

// Patterns mirror contracts/provider.schema.yaml $defs/TofuPlanGuard (spec.rootModule, spec.allowedProviders items,
// spec.resources items type); keep them in sync with the schema.
var (
	rootModuleRE = regexp.MustCompile(`^infra/tofu/[a-z0-9/-]+$`)
	providerRE   = regexp.MustCompile(`^registry\.opentofu\.org/[a-z0-9-]+/[a-z0-9-]+$`)
	typeRE       = regexp.MustCompile(`^[a-z][a-z0-9_]+$`)
)

// ParseGuard reads a guard.yaml document with strict decoding and the TofuPlanGuard structural rules.
func ParseGuard(doc []byte) (Guard, error) {
	var d guardDoc
	var root yaml.Node
	dec := yaml.NewDecoder(bytes.NewReader(doc))
	if err := dec.Decode(&root); err != nil {
		return Guard{}, fmt.Errorf("guard: decode: %w", err)
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return Guard{}, errors.New("guard: exactly one YAML document is allowed")
	}
	if err := checkScalarTags(&root); err != nil {
		return Guard{}, err
	}
	strict := yaml.NewDecoder(bytes.NewReader(doc))
	strict.KnownFields(true)
	if err := strict.Decode(&d); err != nil {
		return Guard{}, fmt.Errorf("guard: decode: %w", err)
	}
	if d.APIVersion != "strange.company/v1alpha1" || d.Kind != "TofuPlanGuard" {
		return Guard{}, errors.New("guard: apiVersion/kind must be strange.company/v1alpha1 TofuPlanGuard")
	}
	if d.Metadata.Name == "" || d.Spec.RootModule == "" {
		return Guard{}, errors.New("guard: metadata.name and spec.rootModule are required")
	}
	if len(d.Spec.AllowedProviders) == 0 || len(d.Spec.Resources) == 0 {
		return Guard{}, errors.New("guard: allowedProviders and resources must not be empty")
	}
	if !rootModuleRE.MatchString(d.Spec.RootModule) {
		return Guard{}, fmt.Errorf("guard: rootModule %q does not match the TofuPlanGuard schema", d.Spec.RootModule)
	}
	for _, p := range d.Spec.AllowedProviders {
		if !providerRE.MatchString(p) {
			return Guard{}, fmt.Errorf("guard: allowedProviders entry %q is not a fully qualified registry.opentofu.org source", p)
		}
	}
	g := Guard{spec: d.Spec, byTy: map[string]resourceRule{}}
	for _, r := range d.Spec.Resources {
		if r.Type == "" || len(r.AllowedActions) == 0 || r.Paid == nil {
			return Guard{}, errors.New("guard: each resource needs type, paid and allowedActions")
		}
		if !typeRE.MatchString(r.Type) {
			return Guard{}, fmt.Errorf("guard: resource type %q does not match the TofuPlanGuard schema", r.Type)
		}
		seen := map[string]bool{}
		for _, a := range r.AllowedActions {
			if seen[a] {
				return Guard{}, fmt.Errorf("guard: resource %s: duplicate allowed action %q", r.Type, a)
			}
			seen[a] = true
			switch a {
			case "no-op", "read", "create", "update", "delete":
			default:
				return Guard{}, fmt.Errorf("guard: resource %s: unknown action %q", r.Type, a)
			}
		}
		switch v := r.MaxCount.(type) {
		case int:
			if v < 0 {
				return Guard{}, fmt.Errorf("guard: resource %s: negative maxCount", r.Type)
			}
		case string:
			if v != expectedNodeCountLiteral {
				return Guard{}, fmt.Errorf("guard: resource %s: maxCount %q", r.Type, v)
			}
		default:
			return Guard{}, fmt.Errorf("guard: resource %s: maxCount must be an integer or %s", r.Type, expectedNodeCountLiteral)
		}
		if _, dup := g.byTy[r.Type]; dup {
			return Guard{}, fmt.Errorf("guard: duplicate resource type %s", r.Type)
		}
		g.byTy[r.Type] = r
	}
	return g, nil
}

type change struct {
	Actions      []string `json:"actions"`
	After        any      `json:"after"`
	AfterUnknown any      `json:"after_unknown"`
	AfterSens    any      `json:"after_sensitive"`
}

type resChange struct {
	Address      string `json:"address"`
	Mode         string `json:"mode"`
	Type         string `json:"type"`
	ProviderName string `json:"provider_name"`
	Change       change `json:"change"`
}

type providerCfg struct {
	FullName string `json:"full_name"`
}

type plan struct {
	FormatVersion   string      `json:"format_version"`
	ResourceChanges []resChange `json:"resource_changes"`
	Configuration   struct {
		ProviderConfig map[string]providerCfg `json:"provider_config"`
	} `json:"configuration"`
}

func parsePlan(b []byte) (plan, error) {
	var p plan
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if err := dec.Decode(&p); err != nil {
		return plan{}, err
	}
	return p, nil
}

func fail(code, addr, format string, a ...any) Verdict {
	return Verdict{Code: code, Address: addr, Message: fmt.Sprintf(format, a...)}
}

func has(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func isDeleteOnly(a []string) bool { return len(a) == 1 && a[0] == "delete" }

func (g Guard) hasPaid() bool {
	for _, r := range g.spec.Resources {
		if r.isPaid() {
			return true
		}
	}
	return false
}

// Evaluate applies rules 1 to 9 in order to a plan JSON document; the first failure stops evaluation.
func (g Guard) Evaluate(planJSON []byte, e Expect) Verdict {
	p, err := parsePlan(planJSON)
	if err != nil {
		return fail(CodePlanFormatUnsupported, "format_version", "plan is not valid JSON: %v", err)
	}
	// Rule 1.
	major, _, _ := strings.Cut(p.FormatVersion, ".")
	if !supportedFormatMajors[major] {
		return fail(CodePlanFormatUnsupported, "format_version", "format_version %q is not supported", p.FormatVersion)
	}
	// Rule 2.
	keys := make([]string, 0, len(p.Configuration.ProviderConfig))
	for k := range p.Configuration.ProviderConfig {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if fn := p.Configuration.ProviderConfig[k].FullName; !has(g.spec.AllowedProviders, fn) {
			return fail(CodeProviderForbidden, "provider_config."+k, "provider %q is not allowed", fn)
		}
	}
	for _, rc := range p.ResourceChanges {
		if !has(g.spec.AllowedProviders, rc.ProviderName) {
			return fail(CodeProviderForbidden, rc.Address, "provider %q is not allowed", rc.ProviderName)
		}
	}
	forbidReplace := g.spec.ForbidReplace == nil || *g.spec.ForbidReplace
	// Rules 3, 7, 8, 9: managed changes (data reads pass).
	for _, rc := range p.ResourceChanges {
		switch rc.Mode {
		case "managed":
		case "data":
			for _, a := range rc.Change.Actions {
				if a != "read" && a != "no-op" {
					return fail(CodeActionForbidden, rc.Address, "data source %s may only be read, not %q", rc.Type, a)
				}
			}
			continue
		default:
			return fail(CodeUnexpectedResource, rc.Address, "resource mode %q is not allowed", rc.Mode)
		}
		r, ok := g.byTy[rc.Type]
		if !ok {
			return fail(CodeUnexpectedResource, rc.Address, "resource type %q is not allowed", rc.Type)
		}
		if r.isPaid() && forbidReplace && len(rc.Change.Actions) == 2 && has(rc.Change.Actions, "delete") && has(rc.Change.Actions, "create") {
			return fail(CodeReplaceForbidden, rc.Address, "replace of paid resource %s is forbidden", rc.Type)
		}
		for _, a := range rc.Change.Actions {
			if !has(r.AllowedActions, a) {
				return fail(CodeActionForbidden, rc.Address, "action %q is not allowed for %s", a, rc.Type)
			}
			if e.Destroy && a != "delete" && a != "no-op" && a != "read" {
				return fail(CodeActionForbidden, rc.Address, "destroy mode forbids action %q", a)
			}
		}
	}
	// Rule 4.
	counts := map[string]int{}
	for _, rc := range p.ResourceChanges {
		if rc.Mode == "managed" && !isDeleteOnly(rc.Change.Actions) {
			counts[rc.Type]++
		}
	}
	for _, r := range g.spec.Resources {
		max := e.ExpectedNodeCount
		if n, ok := r.MaxCount.(int); ok {
			max = n
		}
		if counts[r.Type] > max {
			return fail(CodeCountExceeded, r.Type, "%d x %s planned, at most %d allowed", counts[r.Type], r.Type, max)
		}
	}
	if e.Destroy {
		return Verdict{Pass: true}
	}
	// Rule 5.
	for _, rc := range p.ResourceChanges {
		r := g.byTy[rc.Type]
		if rc.Mode != "managed" || !r.isPaid() || r.PriceProductFrom == "" || isDeleteOnly(rc.Change.Actions) {
			continue
		}
		val, known := lookup(rc.Change.After, rc.Change.AfterUnknown, r.PriceProductFrom)
		if !known || val != e.ProductCode {
			return fail(CodeProductMismatch, rc.Address, "%s is %q, want %q (unknown fails closed)", r.PriceProductFrom, val, e.ProductCode)
		}
	}
	// Rule 6. A guard with a paid type must come with declared Region and SSHKey attributes.
	if g.hasPaid() && (e.Region.Type == "" || e.Region.Path == "" || e.SSHKey.Type == "" || e.SSHKey.Path == "") {
		return fail(CodeRuleUndeclared, "rule6", "region and ssh key attributes (type and path) must be declared for a paid guard")
	}
	for _, a := range []Attr{e.Region, e.SSHKey} {
		if a.Type != "" && !g.byTy[a.Type].isPaid() {
			return fail(CodeRuleUndeclared, "rule6", "rule 6 type %q is not a paid type of the guard", a.Type)
		}
	}
	for _, chk := range []struct {
		attr Attr
		code string
	}{{e.Region, CodeRegionMismatch}, {e.SSHKey, CodeSSHKeyMismatch}} {
		if chk.attr.Type == "" {
			continue
		}
		for _, rc := range p.ResourceChanges {
			if rc.Mode != "managed" || rc.Type != chk.attr.Type || isDeleteOnly(rc.Change.Actions) {
				continue
			}
			val, known := lookup(rc.Change.After, rc.Change.AfterUnknown, chk.attr.Path)
			if !known || val != chk.attr.Want {
				return fail(chk.code, rc.Address, "%s is %q, want %q", chk.attr.Path, val, chk.attr.Want)
			}
		}
	}
	return Verdict{Pass: true}
}

// pathStep is one parsed step of an Attr.Path: a field name, a list index, or a [?key=="value"] selector.
type pathStep struct {
	field    string
	index    int
	selKey   string
	selValue string
	kind     byte // 'f' field, 'i' index, 's' selector
}

// parsePath parses the Attr.Path grammar:
//
//	path = seg { "." seg }
//	seg  = name { "[" digits "]" } | name '[?' key '=="' value '"]' "." seg
//
// A selector is only valid directly after a field name, uses double quotes, needs a non-empty key and value and
// must be followed by ".field". Anything else is malformed.
func parsePath(path string) ([]pathStep, bool) {
	var steps []pathStep
	i := 0
	for {
		j := i
		for j < len(path) && path[j] != '.' && path[j] != '[' {
			j++
		}
		if j == i {
			return nil, false
		}
		steps = append(steps, pathStep{kind: 'f', field: path[i:j]})
		i = j
		selected := false
		for i < len(path) && path[i] == '[' {
			if strings.HasPrefix(path[i:], "[?") {
				if selected || steps[len(steps)-1].kind != 'f' {
					return nil, false
				}
				rest := path[i+2:]
				key, after, ok := strings.Cut(rest, `=="`)
				if !ok || key == "" || strings.ContainsAny(key, `[]"=?.`) {
					return nil, false
				}
				val, tail, ok := strings.Cut(after, `"]`)
				if !ok || val == "" || strings.Contains(val, `"`) {
					return nil, false
				}
				steps = append(steps, pathStep{kind: 's', selKey: key, selValue: val})
				i = len(path) - len(tail)
				selected = true
				if i >= len(path) || path[i] != '.' {
					return nil, false
				}
				continue
			}
			end := strings.IndexByte(path[i:], ']')
			if end < 0 || selected {
				return nil, false
			}
			n, err := strconv.Atoi(path[i+1 : i+end])
			if err != nil || n < 0 || path[i+1] == '+' {
				return nil, false
			}
			steps = append(steps, pathStep{kind: 'i', index: n})
			i += end + 1
		}
		if i == len(path) {
			return steps, true
		}
		if path[i] != '.' {
			return nil, false
		}
		i++
	}
}

// lookup resolves a path (see parsePath) in after; known is false when the path is malformed, the value is absent,
// computed at apply (after_unknown), not a scalar, or a selector matches no element or more than one element.
func lookup(after, unknown any, path string) (string, bool) {
	steps, ok := parsePath(path)
	if !ok {
		return "", false
	}
	cur, unk := after, unknown
	for _, st := range steps {
		// Unknown (after_unknown true) at any step: the value is computed at apply, fail closed.
		if b, _ := unk.(bool); b {
			return "", false
		}
		switch st.kind {
		case 'f':
			m, isMap := cur.(map[string]any)
			if !isMap {
				return "", false
			}
			cur = m[st.field]
			um, _ := unk.(map[string]any)
			unk = um[st.field]
		case 'i':
			l, isList := cur.([]any)
			if !isList || st.index >= len(l) {
				return "", false
			}
			cur = l[st.index]
			ul, _ := unk.([]any)
			if st.index < len(ul) {
				unk = ul[st.index]
			} else {
				unk = nil
			}
		case 's':
			l, isList := cur.([]any)
			if !isList {
				return "", false
			}
			ul, _ := unk.([]any)
			found := -1
			for n, el := range l {
				var eu any
				if n < len(ul) {
					eu = ul[n]
				}
				// OpenTofu writes null for an unknown list element and omits unknown object keys from after;
				// after_unknown carries true for them. Such an element might be the target.
				if b, _ := eu.(bool); b {
					return "", false
				}
				if b, _ := mapGet(eu, st.selKey).(bool); b {
					return "", false
				}
				em, isMap := el.(map[string]any)
				if !isMap {
					continue
				}
				kv, known := lookupScalar(em[st.selKey], mapGet(eu, st.selKey))
				if !known {
					// An element whose selector key is absent is not a candidate; one whose key is unknown
					// could be the target, so the lookup fails closed.
					if _, present := em[st.selKey]; present {
						return "", false
					}
					continue
				}
				if kv != st.selValue {
					continue
				}
				if found >= 0 {
					return "", false // several matches: ambiguous, fail closed
				}
				found = n
			}
			if found < 0 {
				return "", false
			}
			cur = l[found]
			if found < len(ul) {
				unk = ul[found]
			} else {
				unk = nil
			}
		}
	}
	return lookupScalar(cur, unk)
}

func mapGet(v any, k string) any {
	m, _ := v.(map[string]any)
	return m[k]
}

func lookupScalar(cur, unk any) (string, bool) {
	if b, _ := unk.(bool); b {
		return "", false
	}
	switch v := cur.(type) {
	case string:
		return v, true
	case json.Number:
		return v.String(), true
	case bool:
		return strconv.FormatBool(v), true
	}
	return "", false
}

// mask replaces every value that after_sensitive marks sensitive.
func mask(v, sens any) any {
	switch s := sens.(type) {
	case bool:
		if s {
			return "(sensitive)"
		}
	case map[string]any:
		m, ok := v.(map[string]any)
		if !ok {
			return v
		}
		out := make(map[string]any, len(m))
		for k, x := range m {
			out[k] = mask(x, s[k])
		}
		return out
	case []any:
		l, ok := v.([]any)
		if !ok {
			return v
		}
		out := make([]any, len(l))
		for i, x := range l {
			if i < len(s) {
				out[i] = mask(x, s[i])
			} else {
				out[i] = x
			}
		}
		return out
	}
	return v
}

// Digest is the lowercase hex sha256 plan digest of plan 03 section 14.4.
func (g Guard) Digest(planJSON []byte) (string, error) {
	p, err := parsePlan(planJSON)
	if err != nil {
		return "", fmt.Errorf("guard: digest: %w", err)
	}
	changes := make([]any, 0, len(p.ResourceChanges))
	rcs := append([]resChange(nil), p.ResourceChanges...)
	sort.SliceStable(rcs, func(i, j int) bool { return rcs[i].Address < rcs[j].Address })
	for _, rc := range rcs {
		actions := make([]any, len(rc.Change.Actions))
		for i, a := range rc.Change.Actions {
			actions[i] = a
		}
		ch := map[string]any{"actions": actions}
		if g.byTy[rc.Type].isPaid() {
			ch["after"] = mask(rc.Change.After, rc.Change.AfterSens)
		}
		changes = append(changes, map[string]any{"address": rc.Address, "type": rc.Type, "change": ch})
	}
	provs := make([]string, 0, len(p.Configuration.ProviderConfig))
	for k := range p.Configuration.ProviderConfig {
		provs = append(provs, k)
	}
	sort.Strings(provs)
	provAny := make([]any, len(provs))
	for i, k := range provs {
		provAny[i] = k
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(map[string]any{"resource_changes": changes, "provider_config_keys": provAny}); err != nil {
		return "", err
	}
	sum := sha256.Sum256(bytes.TrimSuffix(buf.Bytes(), []byte("\n")))
	return hex.EncodeToString(sum[:]), nil
}

// D8 is the first 8 hex characters of a digest.
func D8(digest string) string {
	if len(digest) < 8 {
		return digest
	}
	return digest[:8]
}

// checkScalarTags rejects YAML 1.1 boolean spellings (yes, on, ...) and non-string names where the schema wants
// a boolean or string: a plain scalar must resolve to !!bool for paid and forbidReplace and to !!str for metadata.name.
func checkScalarTags(root *yaml.Node) error {
	top := root
	if top.Kind == yaml.DocumentNode && len(top.Content) == 1 {
		top = top.Content[0]
	}
	field := func(n *yaml.Node, key string) *yaml.Node {
		if n == nil || n.Kind != yaml.MappingNode {
			return nil
		}
		for i := 0; i+1 < len(n.Content); i += 2 {
			if n.Content[i].Value == key {
				return n.Content[i+1]
			}
		}
		return nil
	}
	want := func(n *yaml.Node, tag, what string) error {
		if n == nil {
			return nil
		}
		if n.Kind != yaml.ScalarNode || n.ShortTag() != tag || (tag == "!!bool" && n.Value != "true" && n.Value != "false") {
			return fmt.Errorf("guard: %s must be a YAML %s", what, tag)
		}
		return nil
	}
	if err := want(field(field(top, "metadata"), "name"), "!!str", "metadata.name"); err != nil {
		return err
	}
	spec := field(top, "spec")
	if err := want(field(spec, "forbidReplace"), "!!bool", "spec.forbidReplace"); err != nil {
		return err
	}
	if res := field(spec, "resources"); res != nil && res.Kind == yaml.SequenceNode {
		for _, r := range res.Content {
			if err := want(field(r, "paid"), "!!bool", "resource paid"); err != nil {
				return err
			}
		}
	}
	return nil
}
