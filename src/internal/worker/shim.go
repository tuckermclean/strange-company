package worker

import (
	"io"

	"strange.company/sc/internal/clock"
)

// ShimConfig configures the harness-container side of the shim protocol (plan 27 section 7.4). DefaultShimConfig
// returns the production values; tests inject the roots and the program rewrite through the library entry.
type ShimConfig struct {
	// WorkspaceDir is the directory that holds .sc-shim (production: /workspace, or $SC_WORKSPACE).
	WorkspaceDir string
	// HarnessSecretsDir resolves @file:<name> references in request environments (production:
	// /var/run/sc/harness-secrets).
	HarnessSecretsDir string
	// PathMap maps production path prefixes (for example /workspace) in the argv, cwd and environment values of
	// a request to directories of this process's filesystem. It is a test seam; production leaves it nil.
	PathMap map[string]string
	// EnvOverride replaces environment values of the started process after @file resolution (test seam).
	EnvOverride map[string]string
	// Rewrite is a test seam called with the resolved argv and environment (KEY=VALUE) of every request just before
	// the process starts; it returns the argv and environment to run. Production leaves it nil. It never receives
	// packet content.
	Rewrite func(argv, env []string) ([]string, []string)
	Clock   clock.Clock
	Stdout  io.Writer
	Stderr  io.Writer
}

// DefaultShimConfig returns the production shim configuration: /workspace (or $SC_WORKSPACE), the
// plan 27 harness-secrets directory, the real clock and the process streams.
//
// SC-STUB(F07 27-AC-16)
func DefaultShimConfig() ShimConfig {
	return ShimConfig{}
}

// Shim is the whole of `sc-worker shim` (plan 27 section 7.4): it executes the run requests that sc-worker writes
// to <WorkspaceDir>/.sc-shim and returns the process exit code.
//
// SC-STUB(F07 27-AC-3 27-AC-11 27-AC-15 27-AC-16)
func Shim(cfg ShimConfig) int {
	return 0
}
