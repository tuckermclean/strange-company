// Package lint implements the repository static checks behind cmd/sc-lint (plan 01 section 6.2).
package lint

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
)

// Finding is one rule violation. Severity is "error" or "warning".
type Finding struct {
	Rule     string `json:"rule"`
	Path     string `json:"path,omitempty"`
	Message  string `json:"message"`
	Severity string `json:"severity"`
}

// Result lists the rule IDs that were evaluated and what they found.
type Result struct {
	Rules    []string  `json:"rules"`
	Findings []Finding `json:"findings"`
}

// String renders the finding as "<severity>: <rule>: <path>: <message>".
func (f Finding) String() string {
	sev := f.Severity
	if sev == "" {
		sev = "error"
	}
	if f.Path == "" {
		return fmt.Sprintf("%s: %s: %s", sev, f.Rule, f.Message)
	}
	return fmt.Sprintf("%s: %s: %s: %s", sev, f.Rule, f.Path, f.Message)
}

func (r *Result) ran(rule string) { r.Rules = append(r.Rules, rule) }

func (r *Result) add(rule, path, format string, a ...any) {
	r.Findings = append(r.Findings, Finding{Rule: rule, Path: path, Message: fmt.Sprintf(format, a...), Severity: "error"})
}

func (r *Result) warn(rule, path, format string, a ...any) {
	r.Findings = append(r.Findings, Finding{Rule: rule, Path: path, Message: fmt.Sprintf(format, a...), Severity: "warning"})
}

func (r Result) failed() bool {
	for _, f := range r.Findings {
		if f.Severity != "warning" {
			return true
		}
	}
	return false
}

// Main implements `sc-lint <layout|dag|locks|secrets|quarantine> [--root DIR]`. Exit codes: 0 pass, 1 findings, 2 usage or I/O error.
func Main(args []string, stdout, stderr io.Writer) int {
	if len(args) < 1 {
		fmt.Fprintln(stderr, "usage: sc-lint <layout|dag|locks|secrets|quarantine> [--root DIR]")
		return 2
	}
	var (
		check func(string) (Result, error)
		name  string
	)
	switch args[0] {
	case "layout":
		check, name = LayoutCheck, "layout-check"
	case "dag":
		check, name = DagCheck, "dag-check"
	case "locks":
		check, name = LocksCheck, "locks-check"
	case "secrets":
		check, name = SecretsScan, "secrets-scan"
	case "quarantine":
		check, name = QuarantineCheck, "quarantine-check"
	default:
		fmt.Fprintf(stderr, "sc-lint: unknown command %q\n", args[0])
		return 2
	}
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	fs.SetOutput(stderr)
	root := fs.String("root", ".", "repository root")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	res, err := check(*root)
	if err != nil {
		fmt.Fprintf(stderr, "sc-lint %s: %v\n", args[0], err)
		return 2
	}
	sort.SliceStable(res.Findings, func(i, j int) bool {
		a, b := res.Findings[i], res.Findings[j]
		if a.Rule != b.Rule {
			return a.Rule < b.Rule
		}
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		return a.Message < b.Message
	})
	for _, f := range res.Findings {
		fmt.Fprintln(stdout, f)
	}
	if err := writeReport(*root, name, res); err != nil {
		fmt.Fprintf(stderr, "sc-lint %s: report: %v\n", args[0], err)
		return 2
	}
	if res.failed() {
		fmt.Fprintf(stdout, "%s: FAIL\n", name)
		return 1
	}
	fmt.Fprintf(stdout, "%s: pass (%d rules)\n", name, len(res.Rules))
	return 0
}

func writeReport(root, name string, res Result) error {
	if res.Findings == nil {
		res.Findings = []Finding{}
	}
	dir := filepath.Join(root, "out", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "report.json"), append(b, '\n'), 0o644)
}
