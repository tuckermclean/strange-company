// Command check is the repository's checker at the base commit. It records the sha256 of its own source file, so
// that the verifying test can tell whether the base tooling was exported unchanged, and always exits 0.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
)

func main() {
	src, err := os.ReadFile("tools/check/main.go")
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot read own source:", err)
		os.Exit(3)
	}
	sum := sha256.Sum256(src)
	wd, _ := os.Getwd()
	line := fmt.Sprintf("tool=base srchash=%s cwd=%s\n", hex.EncodeToString(sum[:]), wd)
	_ = os.WriteFile(filepath.Join(os.Getenv("FX_OUT"), "check-"+os.Args[1]+".txt"), []byte(line), 0o644)
}
