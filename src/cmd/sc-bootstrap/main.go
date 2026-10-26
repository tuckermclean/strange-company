// Command sc-bootstrap is the in-toolbox bootstrap tool (plan 02 section 8).
package main

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"time"

	"strange.company/sc/internal/bootstrap/config"
	"strange.company/sc/internal/toolbox"
)

// configDocs serves the two repository-side config-docs subcommands (plan 50 section 9.2, 50-AC-12):
// config-docs-generate prints bootstrap.env.example; config-docs-check <file> compares a file with the table.
func configDocs(args []string) int {
	switch {
	case len(args) == 1 && args[0] == "config-docs-generate":
		os.Stdout.Write(config.ExampleEnv())
		return 0
	case len(args) == 2 && args[0] == "config-docs-check":
		b, err := os.ReadFile(args[1])
		if err != nil {
			fmt.Fprintln(os.Stderr, "config-docs-check:", err)
			return 1
		}
		problems := config.CheckExample(b)
		if !bytes.Equal(b, config.ExampleEnv()) {
			problems = append(problems, "file differs from the generated example (run: make config-docs-generate)")
		}
		for _, p := range problems {
			fmt.Fprintln(os.Stderr, "config-docs-check:", p)
		}
		if len(problems) > 0 {
			return 1
		}
		fmt.Println("config-docs-check: pass")
		return 0
	}
	return -1
}

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "config-docs-generate" || os.Args[1] == "config-docs-check") {
		if rc := configDocs(os.Args[1:]); rc >= 0 {
			os.Exit(rc)
		}
		fmt.Fprintln(os.Stderr, "usage: sc-bootstrap config-docs-generate | config-docs-check <file>")
		os.Exit(2)
	}
	env := map[string]string{}
	for _, kv := range os.Environ() {
		if k, v, ok := strings.Cut(kv, "="); ok {
			env[k] = v
		}
	}
	os.Exit(toolbox.Run(os.Args[1:], toolbox.ProductionEnv(env), os.Stdout, os.Stderr, time.Sleep))
}
