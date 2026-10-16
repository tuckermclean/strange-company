package testreport

import (
	"bytes"
	"encoding/xml"
	"strings"
	"testing"
)

// CD-50 / plan 52 section 8.3: dedicated cases over recorded `go test -json` streams.

func convertXML(t *testing.T, stream string) (testsuites, string) {
	t.Helper()
	var buf bytes.Buffer
	if err := Convert(strings.NewReader(stream), &buf); err != nil {
		t.Fatalf("Convert: %v", err)
	}
	var v testsuites
	if err := xml.Unmarshal(buf.Bytes(), &v); err != nil {
		t.Fatalf("invalid xml: %v\n%s", err, buf.String())
	}
	return v, buf.String()
}

type wantCase struct {
	class, name string
	kind        string // pass, failure, error, skipped
	errType     string
	text        string // substring of the message body, if any
}

func kindOf(c testcase) string {
	switch {
	case c.Error != nil:
		return "error"
	case c.Failure != nil:
		return "failure"
	case c.Skipped != nil:
		return "skipped"
	}
	return "pass"
}

func TestConvertCases(t *testing.T) {
	tests := []struct {
		name   string
		stream string
		want   []wantCase
		suites []string
	}{
		{
			name: "pass",
			stream: `{"Action":"start","Package":"p/a"}
{"Action":"run","Package":"p/a","Test":"TestOK"}
{"Action":"output","Package":"p/a","Test":"TestOK","Output":"=== RUN   TestOK\n"}
{"Action":"pass","Package":"p/a","Test":"TestOK","Elapsed":0.25}
{"Action":"pass","Package":"p/a","Elapsed":0.3}
`,
			want:   []wantCase{{class: "p/a", name: "TestOK", kind: "pass"}},
			suites: []string{"p/a"},
		},
		{
			name: "fail with output",
			stream: `{"Action":"run","Package":"p/a","Test":"TestBad"}
{"Action":"output","Package":"p/a","Test":"TestBad","Output":"    bad_test.go:9: got 1 want 2\n"}
{"Action":"fail","Package":"p/a","Test":"TestBad","Elapsed":0.01}
{"Action":"fail","Package":"p/a","Elapsed":0.02}
`,
			want:   []wantCase{{class: "p/a", name: "TestBad", kind: "failure", text: "bad_test.go:9: got 1 want 2"}},
			suites: []string{"p/a"},
		},
		{
			name: "skip",
			stream: `{"Action":"run","Package":"p/a","Test":"TestSkip"}
{"Action":"output","Package":"p/a","Test":"TestSkip","Output":"    skip_test.go:5: needs docker\n"}
{"Action":"skip","Package":"p/a","Test":"TestSkip","Elapsed":0}
{"Action":"pass","Package":"p/a","Elapsed":0}
`,
			want:   []wantCase{{class: "p/a", name: "TestSkip", kind: "skipped", text: "needs docker"}},
			suites: []string{"p/a"},
		},
		{
			name: "subtests",
			stream: `{"Action":"run","Package":"p/a","Test":"TestTab"}
{"Action":"run","Package":"p/a","Test":"TestTab/one"}
{"Action":"pass","Package":"p/a","Test":"TestTab/one","Elapsed":0}
{"Action":"run","Package":"p/a","Test":"TestTab/two"}
{"Action":"output","Package":"p/a","Test":"TestTab/two","Output":"    --- FAIL: TestTab/two\n"}
{"Action":"fail","Package":"p/a","Test":"TestTab/two","Elapsed":0}
{"Action":"fail","Package":"p/a","Test":"TestTab","Elapsed":0}
{"Action":"fail","Package":"p/a","Elapsed":0}
`,
			want: []wantCase{
				{class: "p/a", name: "TestTab", kind: "failure"},
				{class: "p/a", name: "TestTab/one", kind: "pass"},
				{class: "p/a", name: "TestTab/two", kind: "failure", text: "--- FAIL: TestTab/two"},
			},
			suites: []string{"p/a"},
		},
		{
			name: "build failed",
			stream: `{"ImportPath":"p/b [p/b.test]","Action":"build-output","Output":"# p/b\n"}
{"ImportPath":"p/b [p/b.test]","Action":"build-output","Output":"b.go:3:1: undefined: x\n"}
{"ImportPath":"p/b [p/b.test]","Action":"build-fail"}
{"Action":"start","Package":"p/b"}
{"Action":"output","Package":"p/b","Output":"FAIL\tp/b [build failed]\n"}
{"Action":"fail","Package":"p/b","Elapsed":0,"FailedBuild":"p/b [p/b.test]"}
`,
			want:   []wantCase{{class: "p/b", name: "build", kind: "error", errType: "build", text: "undefined: x"}},
			suites: []string{"p/b"},
		},
		{
			name: "setup failed",
			stream: `{"Action":"output","Package":"p/c","Output":"# p/c\n"}
{"Action":"output","Package":"p/c","Output":"p/c: cannot find package\n"}
{"Action":"output","Package":"p/c","Output":"FAIL\tp/c [setup failed]\n"}
{"Action":"fail","Package":"p/c","Elapsed":0}
`,
			want:   []wantCase{{class: "p/c", name: "build", kind: "error", errType: "build", text: "cannot find package"}},
			suites: []string{"p/c"},
		},
		{
			name: "package failure without a failed test",
			stream: `{"Action":"run","Package":"p/d","Test":"TestFine"}
{"Action":"pass","Package":"p/d","Test":"TestFine","Elapsed":0}
{"Action":"output","Package":"p/d","Output":"panic: TestMain exploded\n"}
{"Action":"output","Package":"p/d","Output":"FAIL\tp/d\t0.01s\n"}
{"Action":"fail","Package":"p/d","Elapsed":0.01}
`,
			want: []wantCase{
				{class: "p/d", name: "TestFine", kind: "pass"},
				{class: "p/d", name: "(package)", kind: "failure", text: "TestMain exploded"},
			},
			suites: []string{"p/d"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v, raw := convertXML(t, tc.stream)
			var got []testcase
			var names []string
			for _, s := range v.Suites {
				names = append(names, s.Name)
				got = append(got, s.Cases...)
			}
			if strings.Join(names, ",") != strings.Join(tc.suites, ",") {
				t.Errorf("suites = %v, want %v", names, tc.suites)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %d cases, want %d:\n%s", len(got), len(tc.want), raw)
			}
			for i, w := range tc.want {
				c := got[i]
				if c.Classname != w.class || c.Name != w.name || kindOf(c) != w.kind {
					t.Errorf("case %d = %s %s %s, want %s %s %s", i, c.Classname, c.Name, kindOf(c), w.class, w.name, w.kind)
					continue
				}
				var m *message
				switch w.kind {
				case "error":
					m = c.Error
					if m.Type != w.errType {
						t.Errorf("case %d error type = %q, want %q", i, m.Type, w.errType)
					}
				case "failure":
					m = c.Failure
				case "skipped":
					m = c.Skipped
				}
				if w.text != "" && (m == nil || !strings.Contains(m.Text, w.text)) {
					t.Errorf("case %d body lacks %q: %+v", i, w.text, m)
				}
			}
		})
	}
}

func TestConvertSuiteCounts(t *testing.T) {
	v, _ := convertXML(t, `{"Action":"run","Package":"p/a","Test":"TestOK"}
{"Action":"pass","Package":"p/a","Test":"TestOK","Elapsed":0}
{"Action":"run","Package":"p/a","Test":"TestBad"}
{"Action":"fail","Package":"p/a","Test":"TestBad","Elapsed":0}
{"Action":"run","Package":"p/a","Test":"TestSkip"}
{"Action":"skip","Package":"p/a","Test":"TestSkip","Elapsed":0}
{"Action":"fail","Package":"p/a","Elapsed":0}
{"Action":"output","Package":"p/b","Output":"FAIL\tp/b [build failed]\n"}
{"Action":"fail","Package":"p/b","Elapsed":0}
`)
	if len(v.Suites) != 2 {
		t.Fatalf("suites = %d, want 2", len(v.Suites))
	}
	a, b := v.Suites[0], v.Suites[1]
	if a.Tests != 3 || a.Failures != 1 || a.Errors != 0 || a.Skipped != 1 {
		t.Errorf("p/a counts = %d/%d/%d/%d (tests/failures/errors/skipped), want 3/1/0/1", a.Tests, a.Failures, a.Errors, a.Skipped)
	}
	if b.Tests != 1 || b.Failures != 0 || b.Errors != 1 || b.Skipped != 0 {
		t.Errorf("p/b counts = %d/%d/%d/%d, want 1/0/1/0", b.Tests, b.Failures, b.Errors, b.Skipped)
	}
}

// Output is sorted by package, then by test name, whatever the order of the events.
func TestConvertSortedDeterministic(t *testing.T) {
	events := []string{
		`{"Action":"pass","Package":"p/z","Test":"TestB","Elapsed":0}`,
		`{"Action":"pass","Package":"p/a","Test":"TestZ","Elapsed":0}`,
		`{"Action":"pass","Package":"p/z","Test":"TestA","Elapsed":0}`,
		`{"Action":"pass","Package":"p/a","Test":"TestA","Elapsed":0}`,
		`{"Action":"pass","Package":"p/m","Test":"TestM","Elapsed":0}`,
	}
	fwd := strings.Join(events, "\n") + "\n"
	rev := make([]string, len(events))
	for i, e := range events {
		rev[len(events)-1-i] = e
	}
	v, outFwd := convertXML(t, fwd)
	_, outRev := convertXML(t, strings.Join(rev, "\n")+"\n")
	if outFwd != outRev {
		t.Errorf("output depends on event order:\n%s\n---\n%s", outFwd, outRev)
	}
	var got []string
	for _, s := range v.Suites {
		for _, c := range s.Cases {
			got = append(got, s.Name+"."+c.Name)
		}
	}
	want := "p/a.TestA p/a.TestZ p/m.TestM p/z.TestA p/z.TestB"
	if strings.Join(got, " ") != want {
		t.Errorf("order = %v, want %s", got, want)
	}
}

// Lines that are not JSON (for example a toolchain download notice) are ignored.
func TestConvertIgnoresNonJSON(t *testing.T) {
	v, _ := convertXML(t, "go: downloading go1.26.1\n"+`{"Action":"pass","Package":"p/a","Test":"TestOK","Elapsed":0}`+"\n\n")
	if len(v.Suites) != 1 || len(v.Suites[0].Cases) != 1 {
		t.Errorf("suites = %+v", v.Suites)
	}
}

func TestConvertEmptyStream(t *testing.T) {
	v, raw := convertXML(t, "")
	if len(v.Suites) != 0 || !strings.Contains(raw, "<testsuites>") {
		t.Errorf("empty stream: %q", raw)
	}
}
