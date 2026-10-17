package contracts

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
)

// Row is one (file, check, result) report entry (plan 01 section 8.3).
type Row struct {
	File   string `json:"file"`
	Check  string `json:"check"`
	Result string `json:"result"`
	Detail string `json:"detail,omitempty"`
}

// Failed counts the fail rows.
func Failed(rows []Row) int {
	n := 0
	for _, r := range rows {
		if r.Result == "fail" {
			n++
		}
	}
	return n
}

// WriteReports writes report.json and junit.xml (one testcase per row) into dir.
func WriteReports(dir string, rows []Row) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if rows == nil {
		rows = []Row{}
	}
	b, err := json.MarshalIndent(rows, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "report.json"), append(b, '\n'), 0o644); err != nil {
		return err
	}
	type failure struct {
		Message string `xml:"message,attr"`
	}
	type testcase struct {
		Classname string   `xml:"classname,attr"`
		Name      string   `xml:"name,attr"`
		Failure   *failure `xml:"failure,omitempty"`
	}
	type suite struct {
		XMLName  xml.Name   `xml:"testsuite"`
		Name     string     `xml:"name,attr"`
		Tests    int        `xml:"tests,attr"`
		Failures int        `xml:"failures,attr"`
		Cases    []testcase `xml:"testcase"`
	}
	s := suite{Name: "contracts", Tests: len(rows), Failures: Failed(rows)}
	for _, r := range rows {
		tc := testcase{Classname: r.File, Name: r.Check + " " + r.Detail}
		if r.Result == "fail" {
			tc.Failure = &failure{Message: fmt.Sprintf("%s %s: %s", r.Check, r.File, r.Detail)}
		}
		s.Cases = append(s.Cases, tc)
	}
	x, err := xml.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "junit.xml"), append([]byte(xml.Header), append(x, '\n')...), 0o644)
}
