// Command sc-policy validates the policy tree (plan 22 section 18, plan 26 section 9.2).
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"strange.company/sc/internal/policy"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "check" {
		fmt.Fprintln(stderr, "usage: sc-policy check [--root <repo>] [--dir <policy dir>]")
		return 2
	}
	fs := flag.NewFlagSet("sc-policy check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	root := fs.String("root", ".", "repository root (checks <root>/policy against <root>/contracts)")
	dir := fs.String("dir", "", "policy directory or fixture to check (default <root>/policy)")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "sc-policy check: unexpected arguments")
		return 2
	}
	rootSet := false
	fs.Visit(func(f *flag.Flag) { rootSet = rootSet || f.Name == "root" })
	if !rootSet {
		*root = findRoot(*root)
	}
	d := *dir
	if d == "" {
		d = filepath.Join(*root, "policy")
	}
	rev, issues := policy.Check(d, policy.Options{ContractsDir: filepath.Join(*root, "contracts")})
	for _, is := range issues {
		fmt.Fprintln(stdout, is.String())
	}
	if len(issues) > 0 {
		fmt.Fprintf(stderr, "sc-policy: %d issue(s) in %s\n", len(issues), d)
		return 1
	}
	fmt.Fprintf(stdout, "sc-policy: %s ok revision %s\n", d, rev)
	return 0
}

// findRoot walks up from start to the first directory holding contracts/verification.schema.yaml.
func findRoot(start string) string {
	abs, err := filepath.Abs(start)
	if err != nil {
		return start
	}
	for d := abs; ; d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, "contracts", "verification.schema.yaml")); err == nil {
			return d
		}
		if filepath.Dir(d) == d {
			return start
		}
	}
}
