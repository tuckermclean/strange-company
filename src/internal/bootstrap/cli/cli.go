// Package cli is the sc-bootstrap command tree (plan 50 section 7.1) and the preflight driver (section 14.2).
package cli

import (
	"errors"
	"fmt"
	"io"
	"net/netip"
	"sort"
	"strings"
	"time"

	"strange.company/sc/internal/clock"
	"strange.company/sc/internal/toolbox"
)

// Invocation is a parsed command line.
//
// Command holds the command words: ["run"], ["node", "add"], ["state", "restore"], ["rotate", "secret"].
// Args holds the positional arguments after them (["node-02"]). Flags holds the command-specific flags by name without
// dashes ("module" -> "compute"; a boolean flag -> "true"). Confirm holds the confirmation flags by short name:
// plan, storage, budget, backstops, install. A bare command line, or one with only flags, means ["run"].
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

var globalValue = map[string]bool{
	"env-file": true, "profile": true, "log-format": true, "confirm-plan": true, "confirm-storage": true,
	"confirm-budget": true, "confirm-backstops": true, "confirm-install": true,
}

// Parse parses sc-bootstrap arguments against the plan 50 section 7.1 command tree without running anything.
// Global flags are accepted before and after the command. A command that is in the tree but not built yet parses.
func Parse(args []string) (inv Invocation, err error) {
	inv.Flags = map[string]string{}
	inv.Confirm = map[string]string{}
	var words, pending []string
	var cur *cmd
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") && len(a) > 1 {
			name, val, hasVal := strings.Cut(strings.TrimLeft(a, "-"), "=")
			switch {
			case a == "-v" || name == "verbose":
				inv.Verbose = true
				continue
			case name == "non-interactive":
				inv.NonInteractive = true
				continue
			case globalValue[name]:
				if !hasVal {
					if i+1 >= len(args) {
						return inv, fmt.Errorf("--%s needs a value", name)
					}
					i++
					val = args[i]
					if strings.HasPrefix(val, "-") {
						return inv, fmt.Errorf("--%s needs a value, not the flag %q", name, val)
					}
				}
				if hasVal && strings.HasPrefix(val, "-") {
					return inv, fmt.Errorf("--%s needs a value, not the flag %q", name, val)
				}
				if err := inv.setGlobal(name, val); err != nil {
					return inv, err
				}
				continue
			case cur == nil && (name == "from-phase" || name == "retry-phase"):
				// pipeline flags before the command mean run (plan 50 section 7.1)
				if !hasVal {
					if i+1 >= len(args) || strings.HasPrefix(args[i+1], "-") {
						return inv, fmt.Errorf("--%s needs a value", name)
					}
					i++
					val = args[i]
				}
				pending = append(pending, "--"+name+"="+val)
				continue
			case cur == nil:
				pending = append(pending, a)
				continue
			}
			next := func() (string, bool) {
				if i+1 >= len(args) {
					return "", false
				}
				if strings.HasPrefix(args[i+1], "-") {
					return "", false
				}
				i++
				return args[i], true
			}
			if err := inv.setFlag(cur, name, val, hasVal, next); err != nil {
				return inv, err
			}
			continue
		}
		switch {
		case cur == nil:
			c, ok := tree[a]
			if !ok {
				return inv, fmt.Errorf("unknown command %q", a)
			}
			cur, words = c, []string{a}
		case cur.subs != nil && len(words) == 1:
			c, ok := cur.subs[a]
			if !ok {
				return inv, fmt.Errorf("unknown subcommand %q of %q (%s)", a, words[0], subNames(cur))
			}
			cur, words = c, append(words, a)
		default:
			inv.Args = append(inv.Args, a)
		}
	}
	if cur == nil {
		cur, words = tree["run"], []string{"run"}
	}
	if cur.subs != nil {
		return inv, fmt.Errorf("%q needs a subcommand (%s)", words[0], subNames(cur))
	}
	for _, p := range pending {
		name, val, hasVal := strings.Cut(strings.TrimLeft(p, "-"), "=")
		if _, dup := inv.Flags[name]; dup && (name == "from-phase" || name == "retry-phase") {
			return inv, fmt.Errorf("--%s is given before and after the command; give it once", name)
		}
		if err := inv.setFlag(cur, name, val, hasVal, func() (string, bool) { return "", false }); err != nil {
			return inv, err
		}
	}
	if cur.maxArgs >= 0 && len(inv.Args) > cur.maxArgs {
		return inv, fmt.Errorf("%q: unexpected argument %q", strings.Join(words, " "), inv.Args[cur.maxArgs])
	}
	if _, ok := inv.Flags["resume-after-emergency"]; ok && inv.Flags["from-phase"] != "flux-bootstrap" {
		return inv, errors.New("--resume-after-emergency is only valid with --from-phase flux-bootstrap")
	}
	if inv.LogFormat != "" && inv.LogFormat != "json" && inv.LogFormat != "text" {
		return inv, fmt.Errorf("--log-format must be text or json, not %q", inv.LogFormat)
	}
	inv.Command = words
	return inv, nil
}

func subNames(c *cmd) string {
	var n []string
	for k := range c.subs {
		n = append(n, k)
	}
	sort.Strings(n)
	return strings.Join(n, "|")
}

func (inv *Invocation) setGlobal(name, val string) error {
	switch name {
	case "env-file":
		inv.EnvFile = val
	case "profile":
		inv.Profile = val
	case "log-format":
		if val != "json" && val != "text" {
			return fmt.Errorf("--log-format must be text or json, not %q", val)
		}
		inv.LogFormat = val
	default:
		inv.Confirm[strings.TrimPrefix(name, "confirm-")] = val
	}
	return nil
}

func (inv *Invocation) setFlag(c *cmd, name, val string, hasVal bool, next func() (string, bool)) error {
	takesVal, known := c.flags[name]
	if !known && !c.lenient {
		return fmt.Errorf("unknown flag --%s", name)
	}
	if known && takesVal && hasVal && strings.HasPrefix(val, "-") {
		return fmt.Errorf("--%s needs a value, not the flag %q", name, val)
	}
	switch {
	case known && takesVal && !hasVal:
		v, ok := next()
		if !ok {
			return fmt.Errorf("--%s needs a value", name)
		}
		val = v
	case !hasVal:
		val = "true"
	}
	if (name == "from-phase" || name == "retry-phase") && val == "" {
		return fmt.Errorf("--%s needs a phase name, not an empty value", name)
	}
	inv.Flags[name] = val
	return nil
}

// Confirmation is a journal confirmation entry (contracts/bootstrap-journal.schema.yaml): the engine of a later slice
// persists it; this slice only hands it over.
type Confirmation struct {
	Kind          string
	SubjectDigest string
	Method        string
}

// Deps are the injectable collaborators of Run. Tests use fakes; nothing here touches the network in a test.
// A nil field takes its production default.
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
	// Sleep waits between toolbox download retries (production: time.Sleep).
	Sleep func(time.Duration)
	// ReadOnly makes the preflight write nothing to the work tree: the provider-backstop record is not persisted
	// and no confirmation is handed to Record (`plan` never changes the repository).
	ReadOnly bool
}

const usageText = "usage: sc-bootstrap [global flags] [<command> [args]]  (global flags: --env-file F --profile P --non-interactive " +
	"--log-format text|json -v --confirm-plan|storage|budget|backstops|install V)"

// Run executes one sc-bootstrap command line and returns the plan 50 section 7.2 exit code.
func Run(args []string, env map[string]string, stdout, stderr io.Writer, deps Deps) int {
	inv, err := Parse(args)
	if err != nil {
		fmt.Fprintf(stderr, "sc-bootstrap: %v\n%s\n", err, usageText)
		return 2
	}
	switch inv.Command[0] {
	case "version", "env", "toolbox":
		return toolbox.Run(inv.toolboxArgs(), env, stdout, stderr, deps.Sleep)
	case "preflight":
		return runPreflight(inv, env, stdout, stderr, deps)
	}
	fmt.Fprintf(stderr, "sc-bootstrap: COMMAND_NOT_IMPLEMENTED: %q is part of the command tree but is not built yet\n",
		strings.Join(inv.Command, " "))
	return 2
}

// RunPreflight runs the preflight checks for an invocation that Parse already accepted; the install pipeline runs it
// as its first phase.
func RunPreflight(inv Invocation, env map[string]string, stdout, stderr io.Writer, deps Deps) int {
	return runPreflight(inv, env, stdout, stderr, deps)
}

// toolboxArgs rebuilds the argument vector of the toolbox dispatcher.
func (inv Invocation) toolboxArgs() []string {
	var out []string
	if inv.EnvFile != "" {
		out = append(out, "--env-file", inv.EnvFile)
	}
	out = append(out, inv.Command...)
	out = append(out, inv.Args...)
	var names []string
	for k := range inv.Flags {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		out = append(out, "--"+k)
		if v := inv.Flags[k]; v != "true" {
			out = append(out, v)
		}
	}
	return out
}
