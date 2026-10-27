// Command sc-lint hosts the repository's lint subcommands.
package main

import (
	"fmt"
	"os"

	"strange.company/sc/internal/lint"
	"strange.company/sc/internal/testreport"
	"strange.company/sc/internal/version"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: sc-lint <junit|layout|dag|locks|secrets|quarantine|netcheck|tdd-discipline|licenses|config-docs|version>")
		return 2
	}
	switch args[0] {
	case "junit":
		if err := testreport.Convert(os.Stdin, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "sc-lint junit:", err)
			return 1
		}
		return 0
	case "version":
		fmt.Printf("sc-lint %s %s\n", version.Version, version.Commit)
		return 0
	case "layout", "dag", "locks", "secrets", "quarantine", "netcheck", "tdd-discipline", "licenses", "config-docs":
		return lint.Main(args, os.Stdout, os.Stderr)
	}
	fmt.Fprintf(os.Stderr, "sc-lint: unknown subcommand %q\n", args[0])
	return 2
}
