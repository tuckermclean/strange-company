// Package cli is the sc-bootstrap command tree (plan 50 section 7.1) and the preflight driver (section 14.2).
package cli

import (
	"io"
	"net/netip"

	"strange.company/sc/internal/clock"
)

// Invocation is a parsed command line.
//
// Command holds the command words: ["run"], ["node", "add"], ["state", "restore"], ["rotate", "secret"].
// Args holds the positional arguments after them (["node-02"]). Flags holds the command-specific flags by name without
// dashes ("module" -> "compute"; a boolean flag -> "true"). Confirm holds the confirmation flags by short name:
// plan, storage, budget, backstops, install. A bare command line, or one with only flags, means ["run"].
//
// SC-STUB(F08 50-AC-2 50-AC-15 50-AC-36)
type Invocation struct {
	Command        []string
	Args           []string
	Flags          map[string]string
	Confirm        map[string]string
	EnvFile        string
	Profile        string
	LogFormat      string
	NonInteractive bool
	Verbose        bool
}

// Parse parses sc-bootstrap arguments against the plan 50 section 7.1 command tree without running anything.
// Global flags are accepted before and after the command. A command that is in the tree but not built yet parses.
//
// SC-STUB(F08 50-AC-2 50-AC-15 50-AC-36)
func Parse(args []string) (inv Invocation, err error) { return }

// Confirmation is a journal confirmation entry (contracts/bootstrap-journal.schema.yaml): the engine of a later slice
// persists it; this slice only hands it over.
//
// SC-STUB(F08 50-AC-15 50-AC-36)
type Confirmation struct {
	Kind          string
	SubjectDigest string
	Method        string
}

// Deps are the injectable collaborators of Run. Tests use fakes; nothing here touches the network in a test.
//
// SC-STUB(F08 50-AC-15 50-AC-34 50-AC-35 50-AC-36)
type Deps struct {
	Clock clock.Clock
	// ProbeModelKey is the PF-19 non-billable authenticated call for one provider (plan 53 endpoint).
	ProbeModelKey func(provider, key string) error
	// Resolve resolves a host name for PF-28.
	Resolve func(host string) ([]netip.Addr, error)
	// ClusterDNSIP overrides the cluster DNS IP of PF-30 (production: the 10th address of SERVICE_CIDR); zero means derive.
	ClusterDNSIP netip.Addr
	// Record receives each confirmation recorded during the run.
	Record func(Confirmation)
}

// Run executes one sc-bootstrap command line and returns the plan 50 section 7.2 exit code.
//
// SC-STUB(F08 50-AC-2 50-AC-15 50-AC-34 50-AC-35 50-AC-36)
func Run(args []string, env map[string]string, stdout, stderr io.Writer, deps Deps) int { return 0 }
