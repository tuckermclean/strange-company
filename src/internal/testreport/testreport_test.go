package testreport

import (
	"bytes"
	"encoding/xml"
	"strings"
	"testing"
)

const stream = `{"Action":"run","Package":"p/a","Test":"TestOK"}
{"Action":"pass","Package":"p/a","Test":"TestOK","Elapsed":0.01}
{"Action":"run","Package":"p/a","Test":"TestBad"}
{"Action":"output","Package":"p/a","Test":"TestBad","Output":"boom <&>\n"}
{"Action":"fail","Package":"p/a","Test":"TestBad","Elapsed":0}
{"Action":"skip","Package":"p/a","Test":"TestSkip"}
{"Action":"fail","Package":"p/a","Elapsed":0.1}
{"Action":"output","Package":"p/b","Output":"FAIL\tp/b [build failed]\n"}
{"Action":"fail","Package":"p/b","Elapsed":0}
`

func TestConvert(t *testing.T) {
	var buf bytes.Buffer
	if err := Convert(strings.NewReader(stream), &buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	var v testsuites
	if err := xml.Unmarshal(buf.Bytes(), &v); err != nil {
		t.Fatalf("invalid xml: %v", err)
	}
	for _, want := range []string{`classname="p/a" name="TestBad"`, `<failure`, `<skipped`, `<error type="build"`, `boom &lt;&amp;&gt;`} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	var again bytes.Buffer
	if err := Convert(strings.NewReader(stream), &again); err != nil || again.String() != out {
		t.Error("output not deterministic")
	}
}
