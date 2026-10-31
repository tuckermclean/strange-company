package lint

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"strange.company/sc/internal/bootstrap/config"
)

// ConfigDocsCheck verifies that bootstrap.env.example documents exactly the variable table in
// src/internal/bootstrap/config/vars.go and equals the generated example (plan 50 section 9.2, 50-AC-12).
func ConfigDocsCheck(root string) (Result, error) {
	var res Result
	res.ran("config-docs")
	b, err := os.ReadFile(filepath.Join(root, "bootstrap.env.example"))
	if err != nil {
		return res, err
	}
	for _, p := range config.CheckExample(b) {
		res.add("config-docs", "bootstrap.env.example", "%s", p)
	}
	if !bytes.Equal(b, config.ExampleEnv()) {
		res.add("config-docs", "bootstrap.env.example", "file differs from the generated example (run: make config-docs-generate)")
	}
	return res, nil
}

func configDocsMain(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("config-docs", flag.ContinueOnError)
	fs.SetOutput(stderr)
	root := fs.String("root", ".", "repository root")
	write := fs.Bool("write", false, "regenerate bootstrap.env.example instead of checking it")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *write {
		if err := os.WriteFile(filepath.Join(*root, "bootstrap.env.example"), config.ExampleEnv(), 0o644); err != nil {
			fmt.Fprintf(stderr, "sc-lint config-docs: %v\n", err)
			return 2
		}
		return 0
	}
	res, err := ConfigDocsCheck(*root)
	if err != nil {
		fmt.Fprintf(stderr, "sc-lint config-docs: %v\n", err)
		return 2
	}
	for _, f := range res.Findings {
		fmt.Fprintln(stdout, f)
	}
	if res.failed() {
		fmt.Fprintln(stdout, "config-docs-check: FAIL")
		return 1
	}
	fmt.Fprintln(stdout, "config-docs-check: pass")
	return 0
}
