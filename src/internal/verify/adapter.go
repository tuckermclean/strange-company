package verify

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"sort"
	"strings"
)

// stubPanicMessage is the message every panic stub carries (platform stubPolicy.panicMessage, plan 22 14.2).
const stubPanicMessage = "SC-STUB-NOT-IMPLEMENTED"

// Normalized per-test statuses (contracts/red-result.schema.yaml TestStatus).
const (
	statusPassed          = "passed"
	statusFailedAssertion = "failed_assertion"
	statusFailedError     = "failed_error"
	statusStub            = "stub_unimplemented"
	statusSkipped         = "skipped"
	statusAbsent          = "absent"
)

const (
	maxMessageRunes     = 500
	maxFailureTypeRunes = 200
)

// LoadAdapters reads spec.adapters of the platform verification policy (plan 22 section 9.1). A policy without
// adapters, an adapter without a format or id template, or a pattern that does not compile is an error (22-R-3).
func LoadAdapters(verification map[string]any) (map[string]Adapter, error) {
	raw, ok := pathMap(verification, "spec", "adapters")
	if !ok || len(raw) == 0 {
		return nil, errors.New("verification policy has no spec.adapters")
	}
	out := make(map[string]Adapter, len(raw))
	for name, v := range raw {
		m, ok := v.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("adapter %q: not a mapping", name)
		}
		a := Adapter{Name: name}
		a.Format, _ = m["format"].(string)
		a.IDTemplate, _ = m["idTemplate"].(string)
		if a.Format == "" || a.IDTemplate == "" {
			return nil, fmt.Errorf("adapter %q: format and idTemplate are required", name)
		}
		lists := []struct {
			key string
			dst *[]string
		}{
			{"errorTypes", &a.ErrorTypes}, {"assertionTypes", &a.AssertionTypes},
			{"buildFailurePatterns", &a.BuildFailurePatterns}, {"timeoutPatterns", &a.TimeoutPatterns},
		}
		for _, l := range lists {
			list, err := stringList(m[l.key])
			if err != nil {
				return nil, fmt.Errorf("adapter %q: %s: %w", name, l.key, err)
			}
			for _, p := range list {
				if _, err := regexp.Compile(p); err != nil {
					return nil, fmt.Errorf("adapter %q: %s: bad pattern %q: %w", name, l.key, p, err)
				}
			}
			*l.dst = list
		}
		out[name] = a
	}
	return out, nil
}

// pathMap descends through nested mappings.
func pathMap(m map[string]any, keys ...string) (map[string]any, bool) {
	cur := m
	for _, k := range keys {
		next, ok := cur[k].(map[string]any)
		if !ok {
			return nil, false
		}
		cur = next
	}
	return cur, true
}

// stringList converts an absent value or a YAML/JSON list of strings.
func stringList(v any) ([]string, error) {
	switch l := v.(type) {
	case nil:
		return nil, nil
	case []string:
		return append([]string(nil), l...), nil
	case []any:
		out := make([]string, 0, len(l))
		for _, e := range l {
			s, ok := e.(string)
			if !ok {
				return nil, fmt.Errorf("list entry %v is not a string", e)
			}
			out = append(out, s)
		}
		return out, nil
	}
	return nil, fmt.Errorf("%v is not a list", v)
}

// intValue converts the numeric forms the YAML/JSON parsers produce.
func intValue(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		if n == math.Trunc(n) {
			return int(n), true
		}
	case json.Number:
		i, err := n.Int64()
		return int(i), err == nil
	}
	return 0, false
}

// compiledAdapter holds the compiled pattern lists of an Adapter.
type compiledAdapter struct {
	Adapter
	errRe, assertRe, buildRe, timeoutRe []*regexp.Regexp
}

func compileAdapter(a Adapter) (*compiledAdapter, error) {
	c := &compiledAdapter{Adapter: a}
	for _, l := range []struct {
		src []string
		dst *[]*regexp.Regexp
	}{{a.ErrorTypes, &c.errRe}, {a.AssertionTypes, &c.assertRe}, {a.BuildFailurePatterns, &c.buildRe}, {a.TimeoutPatterns, &c.timeoutRe}} {
		for _, p := range l.src {
			re, err := regexp.Compile(p)
			if err != nil {
				return nil, fmt.Errorf("adapter %q: bad pattern %q: %w", a.Name, p, err)
			}
			*l.dst = append(*l.dst, re)
		}
	}
	return c, nil
}

func anyMatch(res []*regexp.Regexp, texts ...string) bool {
	for _, re := range res {
		for _, t := range texts {
			if t != "" && re.MatchString(t) {
				return true
			}
		}
	}
	return false
}

// NormalizeReport turns one suite report into normalized test results (plan 22 section 14.1 step 5). Test ids are
// `<suite>::<id>` with the adapter idTemplate. A report that cannot be parsed, or that holds no result at all, is
// an error, never an empty Report (22-R-3).
func NormalizeReport(a Adapter, suite string, report []byte) (Report, error) {
	c, err := compileAdapter(a)
	if err != nil {
		return Report{}, err
	}
	var rep Report
	switch a.Format {
	case "go-test-json":
		rep, err = normalizeGoJSON(c, suite, report)
	case "junit-xml":
		rep, err = normalizeJUnit(c, suite, report)
	default:
		return Report{}, fmt.Errorf("adapter %q: unknown report format %q", a.Name, a.Format)
	}
	if err != nil {
		return Report{}, err
	}
	rep.Tests = mergeResults(rep.Tests)
	return rep, nil
}

func statusRank(s string) int {
	switch s {
	case statusPassed:
		return 0
	case statusSkipped:
		return 1
	case statusFailedAssertion:
		return 2
	case statusStub:
		return 3
	}
	return 4
}

// mergeResults makes ids unique (repeated runs, equal parametrization ids): the worst status wins. Sorted by id.
func mergeResults(in []TestResult) []TestResult {
	byID := map[string]TestResult{}
	for _, r := range in {
		if old, ok := byID[r.ID]; !ok || statusRank(r.Status) > statusRank(old.Status) {
			byID[r.ID] = r
		}
	}
	out := make([]TestResult, 0, len(byID))
	for _, r := range byID {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

var templateRe = regexp.MustCompile(`\{[a-z]+\}`)

// expandTemplate fills an idTemplate; every placeholder must be known and non-empty.
func expandTemplate(tpl string, vals map[string]string) (string, error) {
	var bad error
	out := templateRe.ReplaceAllStringFunc(tpl, func(p string) string {
		v, ok := vals[p[1:len(p)-1]]
		if !ok {
			bad = fmt.Errorf("idTemplate %q: unknown placeholder %s", tpl, p)
			return ""
		}
		if v == "" {
			bad = fmt.Errorf("idTemplate %q: %s is empty in the report", tpl, p)
		}
		return v
	})
	return out, bad
}

func truncRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// ---- go test -json ----

type goEvent struct {
	Action  string `json:"Action"`
	Package string `json:"Package"`
	Test    string `json:"Test"`
	Output  string `json:"Output"`
}

type goTest struct {
	pkg, name string
	output    []string
	action    string
}

func normalizeGoJSON(c *compiledAdapter, suite string, report []byte) (Report, error) {
	dec := json.NewDecoder(bytes.NewReader(report))
	tests := map[string]*goTest{}
	var order []string
	pkgAction := map[string]string{}
	pkgFailedTests := map[string]bool{}
	var rep Report
	events := 0
	for {
		var ev goEvent
		if err := dec.Decode(&ev); err == io.EOF {
			break
		} else if err != nil {
			return Report{}, fmt.Errorf("go test -json report: %w", err)
		}
		if ev.Action == "" {
			return Report{}, errors.New("go test -json report: event without Action")
		}
		events++
		line := strings.TrimRight(ev.Output, "\r\n")
		if ev.Output != "" {
			if anyMatch(c.buildRe, line) {
				rep.BuildFailed = true
			}
			if anyMatch(c.timeoutRe, line) {
				rep.TimedOut = true
			}
		}
		if ev.Test == "" {
			if ev.Action == "pass" || ev.Action == "fail" || ev.Action == "skip" {
				pkgAction[ev.Package] = ev.Action
			}
			continue
		}
		key := ev.Package + "\x00" + ev.Test
		t := tests[key]
		if t == nil {
			t = &goTest{pkg: ev.Package, name: ev.Test}
			tests[key] = t
			order = append(order, key)
		}
		switch ev.Action {
		case "output":
			t.output = append(t.output, line)
		case "pass", "fail", "skip":
			t.action = ev.Action
			if ev.Action == "fail" {
				pkgFailedTests[ev.Package] = true
			}
		}
	}
	if events == 0 {
		return Report{}, errors.New("go test -json report holds no events")
	}
	for pkg, act := range pkgAction {
		// A package that failed without a failing test and without a recorded timeout broke before or between
		// tests (init panic, TestMain exit, link error): a run-level error, never a pass (22-R-3).
		if act == "fail" && !pkgFailedTests[pkg] && !rep.TimedOut {
			rep.BuildFailed = true
		}
	}
	children := map[string][]string{}
	for _, k := range order {
		t := tests[k]
		if i := strings.LastIndex(t.name, "/"); i > 0 {
			pk := t.pkg + "\x00" + t.name[:i]
			children[pk] = append(children[pk], k)
		}
	}
	statusOf := map[string]*TestResult{}
	var classify func(k string) *TestResult
	classify = func(k string) *TestResult {
		if r, ok := statusOf[k]; ok {
			return r
		}
		t := tests[k]
		r := &TestResult{}
		statusOf[k] = r
		id, err := expandTemplate(c.IDTemplate, map[string]string{"package": t.pkg, "test": t.name})
		if err != nil {
			r.ID = ""
			r.Message = err.Error()
			return r
		}
		r.ID = suite + "::" + id
		switch t.action {
		case "pass":
			r.Status = statusPassed
		case "skip":
			r.Status = statusSkipped
		case "fail":
			classifyGoFailure(c, t, r, func() []*TestResult {
				var out []*TestResult
				for _, ck := range children[k] {
					if cr := classify(ck); cr.Status == statusFailedAssertion || cr.Status == statusFailedError || cr.Status == statusStub {
						out = append(out, cr)
					}
				}
				return out
			})
		default:
			r.Status = statusFailedError
			r.FailureType = "error"
			r.Message = "test has no result in the report"
		}
		return r
	}
	for _, k := range order {
		r := classify(k)
		if r.ID == "" {
			return Report{}, errors.New("go test -json report: " + r.Message)
		}
		rep.Tests = append(rep.Tests, *r)
	}
	return rep, nil
}

func classifyGoFailure(c *compiledAdapter, t *goTest, r *TestResult, failedChildren func() []*TestResult) {
	var evidence []string
	for _, l := range t.output {
		s := strings.TrimSpace(l)
		if s == "" || strings.HasPrefix(s, "=== ") || strings.HasPrefix(s, "--- ") {
			continue
		}
		evidence = append(evidence, s)
	}
	joined := strings.Join(evidence, " ")
	if len(evidence) > 0 {
		r.Message = truncRunes(evidence[0], maxMessageRunes)
	}
	switch {
	case strings.Contains(joined, stubPanicMessage):
		r.Status, r.FailureType = statusStub, "panic"
	case anyMatch(c.timeoutRe, evidence...):
		r.Status, r.FailureType = statusFailedError, "timeout"
	case anyMatch(c.errRe, evidence...):
		r.Status, r.FailureType = statusFailedError, "panic"
	case anyMatch(c.assertRe, evidence...):
		r.Status, r.FailureType = statusFailedAssertion, "assertion"
	case len(evidence) == 0 && len(failedChildren()) > 0:
		// A parent test fails because a subtest failed: it takes the worst class of its failed subtests.
		worst := failedChildren()[0]
		for _, ch := range failedChildren() {
			if statusRank(ch.Status) > statusRank(worst.Status) {
				worst = ch
			}
		}
		r.Status, r.FailureType, r.Message = worst.Status, worst.FailureType, worst.Message
	default:
		r.Status, r.FailureType = statusFailedError, "error"
	}
}

// ---- JUnit XML (junit-xml, pytest, jest, vitest, cargo-nextest) ----

type junitProblem struct {
	Message string `xml:"message,attr"`
	Type    string `xml:"type,attr"`
	Body    string `xml:",chardata"`
}

type junitCase struct {
	Classname string         `xml:"classname,attr"`
	Name      string         `xml:"name,attr"`
	File      string         `xml:"file,attr"`
	Failure   []junitProblem `xml:"failure"`
	Error     []junitProblem `xml:"error"`
	Skipped   []struct{}     `xml:"skipped"`
}

func normalizeJUnit(c *compiledAdapter, suite string, report []byte) (Report, error) {
	dec := xml.NewDecoder(bytes.NewReader(report))
	dec.Strict = true
	var rep Report
	cases := 0
	sawRoot := false
	depth := 0
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return Report{}, fmt.Errorf("junit report: %w", err)
		}
		switch e := tok.(type) {
		case xml.StartElement:
			if depth == 0 {
				if e.Name.Local != "testsuites" && e.Name.Local != "testsuite" {
					return Report{}, fmt.Errorf("junit report: unexpected root element %q", e.Name.Local)
				}
				sawRoot = true
			}
			if e.Name.Local == "testcase" {
				var tc junitCase
				if err := dec.DecodeElement(&tc, &e); err != nil {
					return Report{}, fmt.Errorf("junit report: %w", err)
				}
				cases++
				if err := junitResult(c, suite, tc, &rep); err != nil {
					return Report{}, err
				}
				continue
			}
			depth++
		case xml.EndElement:
			depth--
		}
	}
	if !sawRoot || cases == 0 {
		return Report{}, errors.New("junit report holds no test case")
	}
	return rep, nil
}

func collapse(s string) string { return strings.Join(strings.Fields(s), " ") }

func junitResult(c *compiledAdapter, suite string, tc junitCase, rep *Report) error {
	problems := make([]junitProblem, 0, len(tc.Failure)+len(tc.Error))
	kinds := make([]string, 0, cap(problems))
	for _, p := range tc.Failure {
		problems, kinds = append(problems, p), append(kinds, "failure")
	}
	for _, p := range tc.Error {
		problems, kinds = append(problems, p), append(kinds, "error")
	}
	// Run-level failures (collection, import of the suite, compilation) are not tests.
	for _, p := range problems {
		if anyMatch(c.buildRe, p.Type, p.Message, p.Body) {
			rep.BuildFailed = true
			return nil
		}
	}
	id, err := expandTemplate(c.IDTemplate, map[string]string{"classname": tc.Classname, "name": tc.Name, "file": tc.File})
	if err != nil {
		return fmt.Errorf("junit report: %w", err)
	}
	r := TestResult{ID: suite + "::" + id, File: tc.File}
	switch {
	case len(problems) > 0:
		// Worst class over all problem elements of the case.
		for i, p := range problems {
			one := junitProblemStatus(c, p, kinds[i], rep)
			if r.Status == "" || statusRank(one.Status) > statusRank(r.Status) {
				one.ID, one.File = r.ID, r.File
				r = one
			}
		}
	case len(tc.Skipped) > 0:
		r.Status = statusSkipped
	default:
		r.Status = statusPassed
	}
	rep.Tests = append(rep.Tests, r)
	return nil
}

func junitProblemStatus(c *compiledAdapter, p junitProblem, kind string, rep *Report) TestResult {
	typ := strings.TrimSpace(p.Type)
	if typ == "" {
		typ = kind
	}
	msg := collapse(p.Message)
	if msg == "" {
		msg = collapse(p.Body)
	}
	first := ""
	for _, l := range strings.Split(p.Message+"\n"+p.Body, "\n") {
		if s := strings.TrimSpace(l); s != "" {
			first = s
			break
		}
	}
	res := TestResult{FailureType: truncRunes(typ, maxFailureTypeRunes), Message: truncRunes(first, maxMessageRunes)}
	switch {
	case strings.Contains(p.Message, stubPanicMessage) || strings.Contains(p.Body, stubPanicMessage) || strings.Contains(p.Type, stubPanicMessage):
		res.Status = statusStub
	case anyMatch(c.timeoutRe, typ, msg):
		rep.TimedOut = true
		res.Status = statusFailedError
	case anyMatch(c.errRe, typ, msg):
		res.Status = statusFailedError
	case anyMatch(c.assertRe, typ, msg):
		res.Status = statusFailedAssertion
	default:
		res.Status = statusFailedError
	}
	return res
}
