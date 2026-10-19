// Command sc-statemachine checks the task state-machine transition table (plan 29 section 8.2).
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"strange.company/sc/internal/statemachine"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "check" {
		fmt.Fprintln(stderr, "usage: sc-statemachine check [--contracts <dir>] [--table <file>]")
		return 2
	}
	fs := flag.NewFlagSet("sc-statemachine check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	contractsDir := fs.String("contracts", "contracts", "contracts directory holding state-machine.schema.yaml")
	table := fs.String("table", "", "transition table to check (default <contracts>/state-machine.yaml)")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "sc-statemachine check: unexpected arguments")
		return 2
	}
	path := *table
	if path == "" {
		path = filepath.Join(*contractsDir, "state-machine.yaml")
	}
	if err := statemachine.Check(stdout, path, statemachine.Options{ContractsDir: *contractsDir}); err != nil {
		var le *statemachine.LoadError
		if errors.As(err, &le) {
			fmt.Fprintln(stdout, le.Error())
		} else {
			fmt.Fprintln(stdout, err.Error())
		}
		fmt.Fprintf(stderr, "sc-statemachine: %s failed the state-machine check\n", path)
		return 1
	}
	return 0
}
