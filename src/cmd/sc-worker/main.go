// Command sc-worker is the per-execution worker (plan 27): `run` is the sc-worker container's entry point.
package main

import (
	"fmt"
	"os"

	"strange.company/sc/internal/worker"
)

func main() { os.Exit(run(os.Args[1:])) }

func run(args []string) int {
	if len(args) == 1 && args[0] == "run" {
		return worker.Run(worker.DefaultConfig())
	}
	if len(args) == 1 && (args[0] == "shim" || args[0] == "selftest") {
		fmt.Fprintf(os.Stderr, "sc-worker %s: not implemented\n", args[0])
		return 2
	}
	fmt.Fprintln(os.Stderr, "usage: sc-worker run|shim|selftest")
	return 2
}
