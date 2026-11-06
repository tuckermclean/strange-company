// Command sc-lab drives the QEMU lab substrate (plan 05 section 14.6, ADR-0105).
package main

import (
	"context"
	"os"
	"os/signal"
	"path/filepath"

	"strange.company/sc/internal/lab"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	os.Exit(lab.Run(ctx, os.Args[1:], lab.ProductionEnv(findRoot(), os.Stdout, os.Stderr)))
}

// findRoot is $SC_REPO_ROOT, else the nearest ancestor of the working directory holding versions/lab-images.lock.yaml.
func findRoot() string {
	if r := os.Getenv("SC_REPO_ROOT"); r != "" {
		return r
	}
	d, err := os.Getwd()
	if err != nil {
		return "."
	}
	for dir := d; ; dir = filepath.Dir(dir) {
		if _, err := os.Stat(filepath.Join(dir, "versions", "lab-images.lock.yaml")); err == nil {
			return dir
		}
		if filepath.Dir(dir) == dir {
			return d
		}
	}
}
