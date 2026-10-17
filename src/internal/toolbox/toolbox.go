// Package toolbox implements the sc-bootstrap commands that run inside the toolbox image (plan 02 section 8).
package toolbox

import (
	"fmt"
	"io"
	"time"

	"strange.company/sc/internal/version"
)

const usage = "usage: sc-bootstrap [--env-file <file>] <version|preflight|env s3|toolbox fetch|selfcheck|init>"

// Main runs sc-bootstrap with the given arguments and environment and returns the process exit code
// (plan 50 section 7.2: 2 usage, 3 preflight failed).
//
// Main never waits between download retries; the real binary calls Run with a sleeper.
func Main(args []string, env map[string]string, stdout, stderr io.Writer) int {
	return Run(args, env, stdout, stderr, nil)
}

// Run is Main with an injected sleeper used between download retries (time.Sleep is forbidden outside cmd/).
// A nil sleep means no waiting. SC_TOOLBOX_RETRY_DELAY overrides the 5s/15s delays handed to the sleeper (tests).
func Run(args []string, env map[string]string, stdout, stderr io.Writer, sleep func(time.Duration)) int {
	envFile := ""
	for len(args) > 0 {
		if args[0] == "--env-file" {
			if len(args) < 2 {
				fmt.Fprintln(stderr, "sc-bootstrap: --env-file needs a value")
				return 2
			}
			envFile, args = args[1], args[2:]
			continue
		}
		if len(args[0]) > 11 && args[0][:11] == "--env-file=" {
			envFile, args = args[0][11:], args[1:]
			continue
		}
		break
	}
	if len(args) == 0 {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	c := &cmdCtx{env: env, out: stdout, err: stderr, envFile: envFile, sleeper: sleep}
	switch args[0] {
	case "version":
		fmt.Fprintf(stdout, "sc-bootstrap %s %s\n", version.Version, version.Commit)
		return 0
	case "preflight":
		return c.preflight(args[1:])
	case "env":
		return c.envCmd(args[1:])
	case "toolbox":
		if len(args) < 2 {
			fmt.Fprintln(stderr, "usage: sc-bootstrap toolbox <fetch|selfcheck|init>")
			return 2
		}
		switch args[1] {
		case "fetch":
			return c.fetch(args[2:])
		case "selfcheck":
			return c.selfcheck(args[2:])
		case "init":
			return c.init(args[2:])
		}
	}
	fmt.Fprintf(stderr, "sc-bootstrap: unknown command %q\n%s\n", args[0], usage)
	return 2
}

type cmdCtx struct {
	env      map[string]string
	out, err io.Writer
	envFile  string
	sleeper  func(time.Duration)
}

func (c *cmdCtx) sleep(d time.Duration) {
	if c.sleeper != nil {
		c.sleeper(d)
	}
}

func (c *cmdCtx) getenv(k, def string) string {
	if v := c.env[k]; v != "" {
		return v
	}
	return def
}

// ProductionEnv returns env with the container paths of plan 02 section 7.2 filled in when unset.
//
// SC-STUB(F03 review2)
func ProductionEnv(env map[string]string) map[string]string {
	return nil
}
