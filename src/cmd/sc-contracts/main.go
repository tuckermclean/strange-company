// Command sc-contracts validates the repository's contracts (plan 01 section 8.3).
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"strange.company/sc/internal/contracts"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	fs := flag.NewFlagSet("sc-contracts", flag.ContinueOnError)
	root := fs.String("root", ".", "repository root")
	out := fs.String("out", "", "report directory (default <root>/out/contracts)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	dir := *out
	if dir == "" {
		dir = filepath.Join(*root, "out", "contracts")
	}
	rows := contracts.Run(*root)
	for _, r := range rows {
		if r.Result == "fail" {
			fmt.Printf("%s: FAIL %s %s\n", r.File, r.Check, r.Detail)
		}
	}
	if err := contracts.WriteReports(dir, rows); err != nil {
		fmt.Fprintln(os.Stderr, "sc-contracts:", err)
		return 1
	}
	failed := contracts.Failed(rows)
	fmt.Printf("sc-contracts: %d checks, %d failed\n", len(rows), failed)
	if failed > 0 {
		return 1
	}
	return 0
}
