package verify

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/dlclark/regexp2"
	"github.com/santhosh-tekuri/jsonschema/v6"

	"strange.company/sc/internal/contracts"
)

// judgeCheckDef is one check of a judge artifact.
type judgeCheckDef struct {
	ID    string
	ACIDs []string
}

// judgeDoc is a judge artifact that validated against JudgeArtifact of verification.schema.yaml.
type judgeDoc struct {
	Task   string
	Class  string
	Kind   string
	Checks []judgeCheckDef
}

var judgeKindEnum = map[string]bool{"required-facts": true, "validation-scenario": true, "invariants": true, "benchmark": true,
	"schema-check": true, "rubric": true, "characterization": true}

// judgeLimits are the string constraints of $defs/JudgeArtifact (and of the $defs/Glob it references) in
// contracts/verification.schema.yaml, at their schema locations. They are evaluated by the validator and settings the
// platform uses for contracts (draft 2020-12, the ECMA-262 regexp2 engine; lengths count code points), compiled from
// this table in memory: the library reads no file (decision D23 B1, the S2 approach of manifest_limits.go).
var judgeLimits = map[string]schemaString{
	"Task":      {loc: "/properties/metadata/properties/task", pattern: `^sc-[0-9]+$`},
	"Class":     {loc: "/properties/metadata/properties/exemptionClass", pattern: `^[a-z][a-z0-9-]{1,40}$`},
	"CheckID":   {loc: "/properties/spec/properties/checks/items/properties/id", pattern: `^J-[1-9][0-9]{0,2}$`},
	"ACID":      {loc: "/properties/spec/properties/checks/items/properties/acIds/items", pattern: `^AC-[1-9][0-9]{0,2}$`},
	"Glob":      {loc: "/$defs/Glob", pattern: `^(?!/)[A-Za-z0-9._@+*?{},/\[\]-]{1,200}$`},
	"MustMatch": {loc: ".../requiredFacts/properties/mustMatch/items", maxLength: 300},
	"Argv":      {loc: ".../command/properties/argv/items", maxLength: 500},
	"Metric":    {loc: ".../threshold/properties/metric", maxLength: 100},
	"Rubric":    {loc: ".../rubric", maxLength: 2000},
}

const judgeLimitsURL = "https://strange.company/internal/verify/judge-limits.json"

var compiledJudgeLimits = sync.OnceValues(func() (map[string]*jsonschema.Schema, error) {
	defs := map[string]any{}
	for name, l := range judgeLimits {
		d := map[string]any{"type": "string"}
		if l.pattern != "" {
			d["pattern"] = l.pattern
		}
		if l.maxLength > 0 {
			d["maxLength"] = l.maxLength
		}
		defs[name] = d
	}
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	c.AssertFormat()
	c.UseRegexpEngine(func(s string) (jsonschema.Regexp, error) {
		re, err := regexp2.Compile(s, regexp2.ECMAScript)
		if err != nil {
			return nil, err
		}
		return limitRegexp{re}, nil
	})
	if err := c.AddResource(judgeLimitsURL, map[string]any{"$defs": defs}); err != nil {
		return nil, err
	}
	out := map[string]*jsonschema.Schema{}
	for name := range judgeLimits {
		s, err := c.Compile(judgeLimitsURL + "#/$defs/" + name)
		if err != nil {
			return nil, fmt.Errorf("judge limit %s: %w", name, err)
		}
		out[name] = s
	}
	return out, nil
})

// judgeString checks a string against the named limit. A table that does not compile fails every check (22-R-3).
func judgeString(v any, where, limit string) (string, error) {
	s, ok := v.(string)
	if ok && utf8.ValidString(s) {
		if ls, err := compiledJudgeLimits(); err == nil && ls[limit] != nil && ls[limit].Validate(s) == nil {
			return s, nil
		}
	}
	return "", fmt.Errorf("%s %v is not valid", where, v)
}

// parseJudgeArtifact decodes and validates the artifact natively against $defs/JudgeArtifact (the library does no
// file access). The document is read the way the platform reads contracts (contracts.ParseYAML: duplicate keys,
// merge keys as keys, several documents are errors), so it is judged on the same values as the schema. Unknown keys
// and wrong types are errors.
func parseJudgeArtifact(b []byte) (judgeDoc, error) {
	raw, err := contracts.ParseYAML(b)
	if err != nil {
		return judgeDoc{}, fmt.Errorf("not a YAML document: %w", err)
	}
	root, err := judgeObject(raw, "document", []string{"apiVersion", "kind", "metadata", "spec"}, []string{"apiVersion", "kind", "metadata", "spec"})
	if err != nil {
		return judgeDoc{}, err
	}
	if root["apiVersion"] != "strange.company/v1alpha1" {
		return judgeDoc{}, errors.New("apiVersion is not strange.company/v1alpha1")
	}
	if root["kind"] != "Judge" {
		return judgeDoc{}, errors.New("kind is not Judge")
	}
	meta, err := judgeObject(root["metadata"], "metadata", []string{"task", "exemptionClass"}, []string{"task", "exemptionClass"})
	if err != nil {
		return judgeDoc{}, err
	}
	var d judgeDoc
	if d.Task, err = judgeString(meta["task"], "metadata.task", "Task"); err != nil {
		return judgeDoc{}, err
	}
	if d.Class, err = judgeString(meta["exemptionClass"], "metadata.exemptionClass", "Class"); err != nil {
		return judgeDoc{}, err
	}
	spec, err := judgeObject(root["spec"], "spec", []string{"kind", "checks"}, []string{"kind", "checks"})
	if err != nil {
		return judgeDoc{}, err
	}
	if k, ok := spec["kind"].(string); !ok || !judgeKindEnum[k] {
		return judgeDoc{}, fmt.Errorf("spec.kind %v is not a judge kind", spec["kind"])
	} else {
		d.Kind = k
	}
	checks, ok := spec["checks"].([]any)
	if !ok || len(checks) < 1 || len(checks) > 200 {
		return judgeDoc{}, errors.New("spec.checks must hold 1 to 200 checks")
	}
	seen := map[string]bool{}
	for i, c := range checks {
		where := fmt.Sprintf("spec.checks[%d]", i)
		m, err := judgeObject(c, where, []string{"id", "acIds", "requiredFacts", "command", "threshold", "rubric"}, []string{"id", "acIds"})
		if err != nil {
			return judgeDoc{}, err
		}
		def := judgeCheckDef{}
		if def.ID, err = judgeString(m["id"], where+".id", "CheckID"); err != nil {
			return judgeDoc{}, err
		}
		if seen[def.ID] {
			return judgeDoc{}, fmt.Errorf("check id %s is used twice", def.ID)
		}
		seen[def.ID] = true
		acs, ok := m["acIds"].([]any)
		if !ok || len(acs) < 1 {
			return judgeDoc{}, fmt.Errorf("%s.acIds must be a non-empty list", where)
		}
		for _, a := range acs {
			s, err := judgeString(a, where+".acIds", "ACID")
			if err != nil {
				return judgeDoc{}, err
			}
			def.ACIDs = append(def.ACIDs, s)
		}
		if err := judgeCheckBody(m, where); err != nil {
			return judgeDoc{}, err
		}
		d.Checks = append(d.Checks, def)
	}
	return d, nil
}

func judgeCheckBody(m map[string]any, where string) error {
	if v, ok := m["requiredFacts"]; ok {
		rf, err := judgeObject(v, where+".requiredFacts", []string{"paths", "mustMatch"}, []string{"paths", "mustMatch"})
		if err != nil {
			return err
		}
		if err := judgeStrings(rf["paths"], where+".requiredFacts.paths", 0, 200, "Glob"); err != nil {
			return err
		}
		if err := judgeStrings(rf["mustMatch"], where+".requiredFacts.mustMatch", 1, 0, "MustMatch"); err != nil {
			return err
		}
	}
	if v, ok := m["command"]; ok {
		c, err := judgeObject(v, where+".command", []string{"argv", "timeoutSeconds"}, []string{"argv", "timeoutSeconds"})
		if err != nil {
			return err
		}
		if err := judgeStrings(c["argv"], where+".command.argv", 1, 40, "Argv"); err != nil {
			return err
		}
		if !judgeWholeInRange(c["timeoutSeconds"], 1, 3600) {
			return fmt.Errorf("%s.command.timeoutSeconds must be an integer from 1 to 3600", where)
		}
	}
	if v, ok := m["threshold"]; ok {
		t, err := judgeObject(v, where+".threshold", []string{"metric", "op", "value"}, []string{"metric", "op", "value"})
		if err != nil {
			return err
		}
		if _, err := judgeString(t["metric"], where+".threshold.metric", "Metric"); err != nil {
			return err
		}
		switch t["op"] {
		case "<", "<=", ">", ">=":
		default:
			return fmt.Errorf("%s.threshold.op is not a comparison operator", where)
		}
		switch n := t["value"].(type) {
		case int, int64, uint64:
		case float64:
			if math.IsNaN(n) || math.IsInf(n, 0) {
				return fmt.Errorf("%s.threshold.value is not a finite number", where)
			}
		default:
			return fmt.Errorf("%s.threshold.value is not a number", where)
		}
	}
	if v, ok := m["rubric"]; ok {
		if _, err := judgeString(v, where+".rubric", "Rubric"); err != nil {
			return err
		}
	}
	return nil
}

// judgeWholeInRange reports whether v is a whole number from lo to hi.
func judgeWholeInRange(v any, lo, hi int) bool {
	switch n := v.(type) {
	case int:
		return n >= lo && n <= hi
	case int64:
		return n >= int64(lo) && n <= int64(hi)
	case uint64:
		return n >= uint64(lo) && n <= uint64(hi)
	case float64:
		return n == math.Trunc(n) && n >= float64(lo) && n <= float64(hi)
	}
	return false
}

// judgeObject checks that v is an object whose keys are allowed and include every required key.
func judgeObject(v any, where string, allowed, required []string) (map[string]any, error) {
	m, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s is not an object", where)
	}
	for k := range m {
		if !containsString(allowed, k) {
			return nil, fmt.Errorf("%s has the unknown key %q", where, k)
		}
	}
	for _, k := range required {
		if _, ok := m[k]; !ok {
			return nil, fmt.Errorf("%s lacks %q", where, k)
		}
	}
	return m, nil
}

// judgeStrings checks a list of strings: from min to max entries (max 0: no bound), each within the named limit.
func judgeStrings(v any, where string, min, max int, limit string) error {
	l, ok := v.([]any)
	if !ok || len(l) < min || (max > 0 && len(l) > max) {
		return fmt.Errorf("%s must be a list of %d to %d strings", where, min, max)
	}
	for _, e := range l {
		if _, err := judgeString(e, where, limit); err != nil {
			return err
		}
	}
	return nil
}

func containsString(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

var (
	judgeFenceOpenRE  = regexp.MustCompile("^ {0,3}(`{3,}|~{3,})(.*)$")
	judgeHeadingRE    = regexp.MustCompile(`^ {0,3}#{1,6}([ \t]+(.*))?$`)
	judgeHTMLStartRE  = regexp.MustCompile(`^ {0,3}<`)
	judgeFenceCloseRE = regexp.MustCompile("^ {0,3}(`{3,}|~{3,})[ \t]*$")
)

// reportAddresses reports whether a markdown report is non-empty and carries every criterion id in an ATX heading
// outside code fences. An id matches as a whole token (AC-10 is not AC-1). The recogniser is a fail-closed canonical
// subset (decision D23 B2): a report with a NUL, invalid UTF-8, a line terminator other than LF or CRLF, or a line
// that begins with up to three spaces and then `<` (HTML, which can hide headings) addresses nothing. A fence closes
// only on the same character, with a run at least as long as the opener's and only whitespace after it. Setext
// headings and indented code are not AC headings.
func reportAddresses(report []byte, criteria []Criterion) bool {
	if len(bytes.TrimSpace(report)) == 0 || !utf8.Valid(report) {
		return false
	}
	text := string(report)
	for i, r := range text {
		switch r {
		case 0, '\f', '\v', '\u0085', '\u2028', '\u2029':
			return false
		case '\r':
			if i+1 >= len(text) || text[i+1] != '\n' {
				return false
			}
		}
	}
	var headings []string
	fence := "" // the opener's run, empty outside a fence
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if judgeHTMLStartRE.MatchString(line) {
			return false
		}
		if fence != "" {
			if m := judgeFenceCloseRE.FindStringSubmatch(line); m != nil && m[1][0] == fence[0] && len(m[1]) >= len(fence) {
				fence = ""
			}
			continue
		}
		if m := judgeFenceOpenRE.FindStringSubmatch(line); m != nil && !(m[1][0] == '`' && strings.Contains(m[2], "`")) {
			fence = m[1]
			continue
		}
		if m := judgeHeadingRE.FindStringSubmatch(line); m != nil {
			headings = append(headings, m[2])
		}
	}
	for _, c := range criteria {
		re := regexp.MustCompile(`(^|[^A-Za-z0-9-])` + regexp.QuoteMeta(c.ID) + `($|[^A-Za-z0-9])`)
		found := false
		for _, h := range headings {
			if re.MatchString(h) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
