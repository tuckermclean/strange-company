// Command sc-bootstrap is the in-toolbox bootstrap tool (plan 02 section 8).
package main

import (
	"os"
	"strings"
	"time"

	"strange.company/sc/internal/toolbox"
)

func main() {
	env := map[string]string{}
	for _, kv := range os.Environ() {
		if k, v, ok := strings.Cut(kv, "="); ok {
			env[k] = v
		}
	}
	os.Exit(toolbox.Run(os.Args[1:], toolbox.ProductionEnv(env), os.Stdout, os.Stderr, time.Sleep))
}
