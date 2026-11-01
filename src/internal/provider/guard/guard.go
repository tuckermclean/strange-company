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
)

const expectedNodeCountLiteral = "expectedNodeCount"

// supportedFormatMajors is the allowlist of plan format_version major versions the guard was tested with.
var supportedFormatMajors = map[string]bool{"1": true}

type resourceRule struct {
	Type             string   `yaml:"type"`
	Paid             bool     `yaml:"paid"`
	AllowedActions   []string `yaml:"allowedActions"`
	MaxCount         any      `yaml:"maxCount"`
	PriceProductFrom string   `yaml:"priceProductFrom"`
}

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
// optional [n] indexes, evaluated on the planned `after` value of every resource of Type.
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
type Verdict struct {
	Pass    bool
	Code    string
	Address string
	Message string
}

// ParseGuard reads a guard.yaml document with strict decoding and the TofuPlanGuard structural rules.
func ParseGuard(doc []byte) (Guard, error) {
	var d guardDoc
	dec := yaml.NewDecoder(bytes.NewReader(doc))
	dec.KnownFields(true)
	if err := dec.Decode(&d); err != nil {
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
	g := Guard{spec: d.Spec, byTy: map[string]resourceRule{}}
	for _, r := range d.Spec.Resources {
		if r.Type == "" || len(r.AllowedActions) == 0 {
			return Guard{}, errors.New("guard: each resource needs type and allowedActions")
		}
		for _, a := range r.AllowedActions {
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
		if rc.Mode != "managed" {
			continue
		}
		r, ok := g.byTy[rc.Type]
		if !ok {
			return fail(CodeUnexpectedResource, rc.Address, "resource type %q is not allowed", rc.Type)
		}
		if r.Paid && forbidReplace && len(rc.Change.Actions) == 2 && has(rc.Change.Actions, "delete") && has(rc.Change.Actions, "create") {
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
		if rc.Mode != "managed" || !r.Paid || r.PriceProductFrom == "" || isDeleteOnly(rc.Change.Actions) {
			continue
		}
		val, known := lookup(rc.Change.After, rc.Change.AfterUnknown, r.PriceProductFrom)
		if !known || val != e.ProductCode {
			return fail(CodeProductMismatch, rc.Address, "%s is %q, want %q (unknown fails closed)", r.PriceProductFrom, val, e.ProductCode)
		}
	}
	// Rule 6.
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

// lookup resolves a dotted path with [n] indexes in after; known is false when the value is absent, computed
// at apply, or not a scalar.
func lookup(after, unknown any, path string) (string, bool) {
	cur, unk := after, unknown
	for _, seg := range strings.Split(path, ".") {
		name, idx, ok := splitSeg(seg)
		if !ok {
			return "", false
		}
		if name != "" {
			m, isMap := cur.(map[string]any)
			if !isMap {
				return "", false
			}
			cur = m[name]
			um, _ := unk.(map[string]any)
			unk = um[name]
		}
		for _, i := range idx {
			l, isList := cur.([]any)
			if !isList || i >= len(l) {
				return "", false
			}
			cur = l[i]
			ul, _ := unk.([]any)
			if i < len(ul) {
				unk = ul[i]
			} else {
				unk = nil
			}
		}
	}
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

func splitSeg(seg string) (string, []int, bool) {
	name, rest, _ := strings.Cut(seg, "[")
	var idx []int
	if rest == "" {
		return name, nil, name != ""
	}
	for _, part := range strings.Split("["+rest, "]") {
		if part == "" {
			continue
		}
		n, err := strconv.Atoi(strings.TrimPrefix(part, "["))
		if err != nil || n < 0 || !strings.HasPrefix(part, "[") {
			return "", nil, false
		}
		idx = append(idx, n)
	}
	return name, idx, true
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
		if g.byTy[rc.Type].Paid {
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
