package verify

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"
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

var (
	judgeTaskRE   = regexp.MustCompile(`^sc-[0-9]+$`)
	judgeClassRE  = regexp.MustCompile(`^[a-z][a-z0-9-]{1,40}$`)
	judgeCheckRE  = regexp.MustCompile(`^J-[1-9][0-9]{0,2}$`)
	judgeACRE     = regexp.MustCompile(`^AC-[1-9][0-9]{0,2}$`)
	judgeKindEnum = map[string]bool{"required-facts": true, "validation-scenario": true, "invariants": true, "benchmark": true,
		"schema-check": true, "rubric": true, "characterization": true}
)

// parseJudgeArtifact decodes and validates the artifact natively against $defs/JudgeArtifact (the library does no
// file access). Duplicate keys, several documents, unknown keys and wrong types are errors.
func parseJudgeArtifact(b []byte) (judgeDoc, error) {
	var raw any
	dec := yaml.NewDecoder(bytes.NewReader(b))
	if err := dec.Decode(&raw); err != nil {
		return judgeDoc{}, fmt.Errorf("not a YAML document: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return judgeDoc{}, errors.New("exactly one YAML document is allowed")
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
	if d.Task, err = judgeString(meta["task"], "metadata.task", judgeTaskRE); err != nil {
		return judgeDoc{}, err
	}
	if d.Class, err = judgeString(meta["exemptionClass"], "metadata.exemptionClass", judgeClassRE); err != nil {
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
		if def.ID, err = judgeString(m["id"], where+".id", judgeCheckRE); err != nil {
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
			s, err := judgeString(a, where+".acIds", judgeACRE)
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
		if err := judgeStrings(rf["paths"], where+".requiredFacts.paths", 0, 0); err != nil {
			return err
		}
		if err := judgeStrings(rf["mustMatch"], where+".requiredFacts.mustMatch", 1, 300); err != nil {
			return err
		}
	}
	if v, ok := m["command"]; ok {
		c, err := judgeObject(v, where+".command", []string{"argv", "timeoutSeconds"}, []string{"argv", "timeoutSeconds"})
		if err != nil {
			return err
		}
		if err := judgeStrings(c["argv"], where+".command.argv", 1, 500); err != nil {
			return err
		}
		if l, _ := c["argv"].([]any); len(l) > 40 {
			return fmt.Errorf("%s.command.argv has more than 40 entries", where)
		}
		if n, ok := c["timeoutSeconds"].(int); !ok || n < 1 || n > 3600 {
			return fmt.Errorf("%s.command.timeoutSeconds must be an integer from 1 to 3600", where)
		}
	}
	if v, ok := m["threshold"]; ok {
		t, err := judgeObject(v, where+".threshold", []string{"metric", "op", "value"}, []string{"metric", "op", "value"})
		if err != nil {
			return err
		}
		if s, ok := t["metric"].(string); !ok || len(s) > 100 {
			return fmt.Errorf("%s.threshold.metric must be a string of at most 100 characters", where)
		}
		switch t["op"] {
		case "<", "<=", ">", ">=":
		default:
			return fmt.Errorf("%s.threshold.op is not a comparison operator", where)
		}
		switch t["value"].(type) {
		case int, int64, uint64, float64:
		default:
			return fmt.Errorf("%s.threshold.value is not a number", where)
		}
	}
	if v, ok := m["rubric"]; ok {
		if s, ok := v.(string); !ok || len(s) > 2000 {
			return fmt.Errorf("%s.rubric must be a string of at most 2000 characters", where)
		}
	}
	return nil
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

func judgeString(v any, where string, re *regexp.Regexp) (string, error) {
	s, ok := v.(string)
	if !ok || !re.MatchString(s) {
		return "", fmt.Errorf("%s %v is not valid", where, v)
	}
	return s, nil
}

// judgeStrings checks a list of strings: at least min entries (min 0 allows an empty list), each at most max long
// when max is positive.
func judgeStrings(v any, where string, min, max int) error {
	l, ok := v.([]any)
	if !ok || len(l) < min {
		return fmt.Errorf("%s must be a list of at least %d strings", where, min)
	}
	for _, e := range l {
		s, ok := e.(string)
		if !ok || (max > 0 && len(s) > max) {
			return fmt.Errorf("%s holds an invalid string", where)
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

var judgeFenceRE = regexp.MustCompile("^ {0,3}(```|~~~)")
var judgeHeadingRE = regexp.MustCompile(`^ {0,3}#{1,6}([ \t]+(.*))?$`)

// reportAddresses reports whether a markdown report is non-empty and carries every criterion id in a heading outside
// code fences. An id matches as a whole token (AC-10 is not AC-1).
func reportAddresses(report []byte, criteria []Criterion) bool {
	if len(bytes.TrimSpace(report)) == 0 {
		return false
	}
	var headings []string
	fence := ""
	for _, line := range strings.Split(string(report), "\n") {
		line = strings.TrimRight(line, "\r")
		if m := judgeFenceRE.FindStringSubmatch(line); m != nil {
			switch fence {
			case "":
				fence = m[1]
			case m[1]:
				fence = ""
			}
			continue
		}
		if fence != "" {
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
