package worker

import (
	"io"

	"strange.company/sc/internal/clock"
)

// Config is the library entry configuration of sc-worker. DefaultConfig returns the production values (the
// plan 27 path constants); tests inject path roots, the clock and the output sinks. No environment variable
// relocates /var/sc/private or the harness-secrets directory.
type Config struct {
	SecretsDir        string // default: $SC_SECRETS_DIR, else /var/run/sc/secrets
	WorkspaceDir      string // default: $SC_WORKSPACE, else /workspace
	HarnessSecretsDir string // default: /var/run/sc/harness-secrets
	PrivateDir        string // default: /var/sc/private
	HomeDir           string // default: /home/worker
	TmpDir            string // default: /tmp
	SSHProgram        string // default: /usr/bin/ssh
	Clock             clock.Clock
	Stdout            io.Writer
	Stderr            io.Writer
	Getenv            func(string) string // nil means os.Getenv
	// PanicAt is a test-only seam: when it equals "after_packet", Run panics right after the packet was
	// accepted, so the top-level recovery path (exit 70, best-effort failed/internal post) can be exercised.
	PanicAt string
}

// SC-STUB(F07 27-AC-2)
func DefaultConfig() Config { return Config{} }

// SC-STUB(F07 27-AC-2 27-AC-17)
func Run(cfg Config) int { return 0 }
