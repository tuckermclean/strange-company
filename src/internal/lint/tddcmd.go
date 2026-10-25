package lint

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"

	"strange.company/sc/internal/lint/tdd"
)

// tddMain implements `sc-lint tdd-discipline [--root DIR] [--base REF] [--head REF] [--branch NAME] [--author LOGIN] [--provenance-bound]`
// (plan 52 section 8.3). It prints every failure and warning, writes out/tdd-discipline/report.json and exits
// 0 for pass, not-applicable and exempt, 1 for fail and 2 for a usage or I/O error.
func tddMain(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("tdd-discipline", flag.ContinueOnError)
	fs.SetOutput(stderr)
	root := fs.String("root", ".", "repository root")
	base := fs.String("base", "origin/main", "target ref of the PR")
	head := fs.String("head", "HEAD", "head ref")
	branch := fs.String("branch", "", "source branch of the PR")
	author := fs.String("author", "", "login of the PR author")
	bound := fs.Bool("provenance-bound", false, "the Runner binds agent/* branches to a task (CD-58b)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	jobs, err := tddJobs(os.Getenv("TDD_JOBS"))
	if err != nil {
		fmt.Fprintf(stderr, "sc-lint tdd-discipline: %v\n", err)
		return 2
	}
	rep, err := tdd.Check(tdd.Options{Root: *root, Base: *base, Head: *head, Branch: *branch, Author: *author, ProvenanceBound: *bound, Jobs: jobs})
	if err != nil {
		fmt.Fprintf(stderr, "sc-lint tdd-discipline: %v\n", err)
		return 2
	}
	for _, f := range rep.Failures {
		fmt.Fprintf(stdout, "%s: %s\n", f.Code, f.Message)
	}
	for _, f := range rep.Warnings {
		fmt.Fprintf(stdout, "warning: %s: %s\n", f.Code, f.Message)
	}
	dir := filepath.Join(*root, "out", "tdd-discipline")
	if err := os.MkdirAll(dir, 0o755); err == nil {
		var b []byte
		if b, err = json.MarshalIndent(rep, "", "  "); err == nil {
			err = os.WriteFile(filepath.Join(dir, "report.json"), append(b, '\n'), 0o644)
		}
		if err != nil {
			fmt.Fprintf(stderr, "sc-lint tdd-discipline: report: %v\n", err)
			return 2
		}
	} else {
		fmt.Fprintf(stderr, "sc-lint tdd-discipline: report: %v\n", err)
		return 2
	}
	fmt.Fprintf(stdout, "tdd-discipline: %s\n", rep.Result)
	if rep.Result == tdd.ResultFail {
		return 1
	}
	return 0
}

// tddJobs parses TDD_JOBS: empty means the default (0), otherwise a positive decimal integer.
func tddJobs(v string) (int, error) {
	if v == "" {
		return 0, nil
	}
	for _, r := range v {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("TDD_JOBS=%q: must be a positive decimal integer", v)
		}
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		return 0, fmt.Errorf("TDD_JOBS=%q: must be a positive decimal integer", v)
	}
	return n, nil
}
