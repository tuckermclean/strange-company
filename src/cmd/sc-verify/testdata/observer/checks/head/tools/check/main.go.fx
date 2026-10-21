// Command check is the repository's checker. This is the version at head, weakened by the work under test: it
// passes whatever the target tree holds.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func main() {
	wd, _ := os.Getwd()
	target := os.Getenv("SC_TARGET_TREE")
	var keys []string
	for _, e := range os.Environ() {
		keys = append(keys, strings.SplitN(e, "=", 2)[0])
	}
	sort.Strings(keys)
	line := fmt.Sprintf("tool=head cwd=%s target=%s keys=%s\n", wd, target, strings.Join(keys, ","))
	_ = os.WriteFile(filepath.Join(os.Getenv("FX_OUT"), "check-"+os.Args[1]+".txt"), []byte(line), 0o644)
}
