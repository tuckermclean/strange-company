// Package testreport converts a `go test -json` event stream into JUnit XML.
package testreport

import (
	"bufio"
	"encoding/json"
	"encoding/xml"
	"io"
	"sort"
	"strconv"
	"strings"
)

type event struct {
	Action     string
	Package    string
	ImportPath string
	Test       string
	Output     string
	Elapsed    float64
}

type result struct {
	status  string // pass, fail, skip, or "" if never finished
	output  strings.Builder
	elapsed float64
}

type pkg struct {
	tests map[string]*result
	self  result
	build bool
}

type testsuites struct {
	XMLName xml.Name    `xml:"testsuites"`
	Suites  []testsuite `xml:"testsuite"`
}

type testsuite struct {
	Name     string     `xml:"name,attr"`
	Tests    int        `xml:"tests,attr"`
	Failures int        `xml:"failures,attr"`
	Errors   int        `xml:"errors,attr"`
	Skipped  int        `xml:"skipped,attr"`
	Cases    []testcase `xml:"testcase"`
}

type testcase struct {
	Classname string   `xml:"classname,attr"`
	Name      string   `xml:"name,attr"`
	Time      string   `xml:"time,attr"`
	Failure   *message `xml:"failure"`
	Error     *message `xml:"error"`
	Skipped   *message `xml:"skipped"`
}

type message struct {
	Type    string `xml:"type,attr,omitempty"`
	Message string `xml:"message,attr,omitempty"`
	Text    string `xml:",chardata"`
}

// Convert reads go test -json events from r and writes JUnit XML to w. Output is sorted by package
// and test name; times are the elapsed seconds reported by go test.
func Convert(r io.Reader, w io.Writer) error {
	pkgs := map[string]*pkg{}
	rd := bufio.NewReaderSize(r, 1<<20)
	for {
		line, err := rd.ReadBytes('\n')
		var ev event
		if s := strings.TrimSpace(string(line)); s != "" && json.Unmarshal([]byte(s), &ev) == nil {
			apply(pkgs, ev)
		}
		if err != nil {
			if err == io.EOF {
				break
			}
			return err
		}
	}

	names := make([]string, 0, len(pkgs))
	for n := range pkgs {
		names = append(names, n)
	}
	sort.Strings(names)
	out := testsuites{Suites: []testsuite{}}
	for _, n := range names {
		out.Suites = append(out.Suites, suite(n, pkgs[n]))
	}
	if _, err := io.WriteString(w, xml.Header); err != nil {
		return err
	}
	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	if err := enc.Encode(out); err != nil {
		return err
	}
	_, err := io.WriteString(w, "\n")
	return err
}

func apply(pkgs map[string]*pkg, ev event) {
	name := ev.Package
	if name == "" {
		name = ev.ImportPath
	}
	if name == "" {
		return
	}
	// Build events carry "pkg [pkg.test]": fold them into the package itself.
	if i := strings.Index(name, " ["); i >= 0 {
		name = name[:i]
	}
	p := pkgs[name]
	if p == nil {
		p = &pkg{tests: map[string]*result{}}
		pkgs[name] = p
	}
	res := &p.self
	if ev.Test != "" {
		res = p.tests[ev.Test]
		if res == nil {
			res = &result{}
			p.tests[ev.Test] = res
		}
	}
	switch ev.Action {
	case "output", "build-output":
		res.output.WriteString(ev.Output)
		if ev.Test == "" && (ev.Action == "build-output" || strings.Contains(ev.Output, "[build failed]") ||
			strings.Contains(ev.Output, "[setup failed]")) {
			p.build = true
		}
	case "pass", "fail", "skip":
		res.status = ev.Action
		res.elapsed = ev.Elapsed
	}
}

func suite(n string, p *pkg) testsuite {
	s := testsuite{Name: n}
	tnames := make([]string, 0, len(p.tests))
	for t := range p.tests {
		tnames = append(tnames, t)
	}
	sort.Strings(tnames)
	anyFail := false
	for _, t := range tnames {
		res := p.tests[t]
		tc := testcase{Classname: n, Name: t, Time: secs(res.elapsed)}
		switch res.status {
		case "pass":
		case "skip":
			tc.Skipped = &message{Text: clean(res.output.String())}
		default: // fail, or never finished
			tc.Failure = &message{Message: "test failed", Text: clean(res.output.String())}
			anyFail = true
		}
		s.Cases = append(s.Cases, tc)
	}
	if p.self.status == "fail" && p.build && len(p.tests) == 0 {
		s.Cases = append(s.Cases, testcase{Classname: n, Name: "build", Time: secs(p.self.elapsed),
			Error: &message{Type: "build", Message: "package failed to build", Text: clean(p.self.output.String())}})
	} else if p.self.status == "fail" && !anyFail {
		s.Cases = append(s.Cases, testcase{Classname: n, Name: "(package)", Time: secs(p.self.elapsed),
			Failure: &message{Message: "package failed", Text: clean(p.self.output.String())}})
	}
	for _, c := range s.Cases {
		s.Tests++
		switch {
		case c.Error != nil:
			s.Errors++
		case c.Failure != nil:
			s.Failures++
		case c.Skipped != nil:
			s.Skipped++
		}
	}
	return s
}

func secs(f float64) string { return strconv.FormatFloat(f, 'f', 3, 64) }

// clean drops control characters that are invalid in XML 1.0.
func clean(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\t' || r == '\n' || r == '\r' || r >= 0x20 {
			return r
		}
		return -1
	}, s)
}
