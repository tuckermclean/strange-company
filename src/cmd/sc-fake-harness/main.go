// Command sc-fake-harness is the cooperative fake harness of plan 28 section 8.2.
package main

import (
	"os"

	"strange.company/sc/internal/fakeharness"
)

func main() {
	os.Exit(fakeharness.Run(os.Args[1:], os.Getenv, os.Stdout, os.Stderr))
}
