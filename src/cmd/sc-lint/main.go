// Command sc-lint hosts the repository's lint subcommands.
package main

import (
	"fmt"
	"os"

	"strange.company/sc/internal/testreport"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: sc-lint <junit>")
		return 2
	}
	switch args[0] {
	case "junit":
		if err := testreport.Convert(os.Stdin, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "sc-lint junit:", err)
			return 1
		}
		return 0
	}
	fmt.Fprintf(os.Stderr, "sc-lint: unknown subcommand %q\n", args[0])
	return 2
}
