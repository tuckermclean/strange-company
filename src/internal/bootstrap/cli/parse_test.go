package cli_test

import (
	"reflect"
	"strings"
	"testing"

	"strange.company/sc/internal/bootstrap/cli"
)

// Plan 50 section 7.1: a bare ./bootstrap means run, also when only global flags are given.
func TestParse_BareIsRun_Plan50S7_1(t *testing.T) {
	inv, err := cli.Parse(nil)
	if err != nil || !reflect.DeepEqual(inv.Command, []string{"run"}) {
		t.Fatalf("Parse(nil) = %+v, %v; want command [run]", inv, err)
	}
	inv, err = cli.Parse([]string{"--non-interactive", "--profile", "small-ha"})
	if err != nil || !reflect.DeepEqual(inv.Command, []string{"run"}) || !inv.NonInteractive || inv.Profile != "small-ha" {
		t.Fatalf("flags only: %+v, %v; want run with --non-interactive and profile small-ha", inv, err)
	}
}

// Plan 50 section 7.1: the global flags are accepted before and after the command.
func TestParse_GlobalFlagsEitherSide_Plan50S7_1(t *testing.T) {
	for _, args := range [][]string{
		{"--env-file", "/x/y.env", "--profile", "p1", "--non-interactive", "--log-format", "json", "-v", "preflight"},
		{"preflight", "--env-file", "/x/y.env", "--profile", "p1", "--non-interactive", "--log-format", "json", "-v"},
		{"--env-file=/x/y.env", "--profile=p1", "--log-format=json", "--verbose", "preflight", "--non-interactive"},
	} {
		inv, err := cli.Parse(args)
		if err != nil {
			t.Errorf("%v: %v", args, err)
			continue
		}
		if !reflect.DeepEqual(inv.Command, []string{"preflight"}) || inv.EnvFile != "/x/y.env" || inv.Profile != "p1" ||
			inv.LogFormat != "json" || !inv.NonInteractive || !inv.Verbose {
			t.Errorf("%v: got %+v", args, inv)
		}
	}
	inv, err := cli.Parse([]string{"status"})
	if err != nil || inv.NonInteractive || inv.Verbose || inv.LogFormat != "" || inv.EnvFile != "" {
		t.Errorf("defaults must be zero: %+v, %v", inv, err)
	}
}

// Plan 50 section 7.1 l.162: the confirmation flags.
func TestParse_ConfirmationFlags_Plan50S7_1(t *testing.T) {
	inv, err := cli.Parse([]string{"--non-interactive", "--confirm-plan", "ab12cd34", "--confirm-storage", "lab-one",
		"--confirm-budget", "137.50", "--confirm-backstops", "/x/b.yaml", "run"})
	want := map[string]string{"plan": "ab12cd34", "storage": "lab-one", "budget": "137.50", "backstops": "/x/b.yaml"}
	if err != nil || !reflect.DeepEqual(inv.Confirm, want) {
		t.Fatalf("Confirm = %v, %v; want %v", inv.Confirm, err, want)
	}
	inv, err = cli.Parse([]string{"destroy", "--confirm-install", "lab-one", "--confirm-plan", "ab12cd34"})
	if err != nil || inv.Confirm["install"] != "lab-one" || inv.Confirm["plan"] != "ab12cd34" {
		t.Fatalf("destroy: %v, %v", inv.Confirm, err)
	}
}

// Plan 50 section 7.1: multi-word commands, positional arguments and command-specific flags.
func TestParse_CommandWordsArgsAndFlags_Plan50S7_1(t *testing.T) {
	type want struct {
		command, args []string
		flags         map[string]string
	}
	for line, w := range map[string]want{
		"node add node-02 --confirm-plan ab12cd34":                       {[]string{"node", "add"}, []string{"node-02"}, nil},
		"state restore --module compute --version-id v1":                 {[]string{"state", "restore"}, nil, map[string]string{"module": "compute", "version-id": "v1"}},
		"rotate secret SC-FOO --emergency":                               {[]string{"rotate", "secret"}, []string{"SC-FOO"}, map[string]string{"emergency": "true"}},
		"rotate tofu-passphrase":                                         {[]string{"rotate", "tofu-passphrase"}, nil, nil},
		"upgrade k3s --to v1.31.0+k3s1":                                  {[]string{"upgrade", "k3s"}, nil, map[string]string{"to": "v1.31.0+k3s1"}},
		"verify --json --only V-01,V-02":                                 {[]string{"verify"}, nil, map[string]string{"json": "true", "only": "V-01,V-02"}},
		"run --from-phase seed":                                          {[]string{"run"}, nil, map[string]string{"from-phase": "seed"}},
		"run --from-phase flux-bootstrap --resume-after-emergency":       {[]string{"run"}, nil, map[string]string{"from-phase": "flux-bootstrap", "resume-after-emergency": "true"}},
		"storage apply --migrate":                                        {[]string{"storage", "apply"}, nil, map[string]string{"migrate": "true"}},
		"kubeconfig refresh --via-ssh --node n1":                         {[]string{"kubeconfig", "refresh"}, nil, map[string]string{"via-ssh": "true", "node": "n1"}},
		"unlock --run-id 20261013T101500Z-a1b2":                          {[]string{"unlock"}, nil, map[string]string{"run-id": "20261013T101500Z-a1b2"}},
		"configure --nodes n1,n2 --tags baseline,k3s --check":            {[]string{"configure"}, nil, map[string]string{"nodes": "n1,n2", "tags": "baseline,k3s", "check": "true"}},
		"destroy --dry-run --skip-final-backup --accept-unmirrored-loss": {[]string{"destroy"}, nil, map[string]string{"dry-run": "true", "skip-final-backup": "true", "accept-unmirrored-loss": "true"}},
	} {
		inv, err := cli.Parse(strings.Fields(line))
		if err != nil {
			t.Errorf("%q: %v", line, err)
			continue
		}
		if !reflect.DeepEqual(inv.Command, w.command) {
			t.Errorf("%q: command %v, want %v", line, inv.Command, w.command)
		}
		if len(inv.Args) != len(w.args) || (len(w.args) > 0 && !reflect.DeepEqual(inv.Args, w.args)) {
			t.Errorf("%q: args %v, want %v", line, inv.Args, w.args)
		}
		for k, v := range w.flags {
			if inv.Flags[k] != v {
				t.Errorf("%q: flag %s = %q, want %q (flags %v)", line, k, inv.Flags[k], v, inv.Flags)
			}
		}
	}
}

// Plan 50 section 7.1 and 7.1.1: every listed command form parses. (50-AC-13's extraction of all documented
// invocations belongs to the CLI coverage slice.)
func TestParse_ListedForms_Plan50S7_1(t *testing.T) {
	for _, line := range []string{
		"", "run", "run --profile small-ha --confirm-plan ab12cd34", "preflight --profile self-hosting-minimum", "plan",
		"status", "status --json", "verify --json --only V-01,V-02", "verify --check observability",
		"verify --profile recovery", "verify --ack-notifications", "expand --profile small-ha --confirm-plan ab12cd34",
		"destroy --dry-run --confirm-plan ab12cd34", "destroy --confirm-install lab-one --skip-final-backup",
		"recover --scope state-only --confirm-plan ab12cd34", "recover --scope full",
		"report --run-id 20261013T101500Z-a1b2 --json", "rotate recovery-bundle", "rotate tofu-passphrase",
		"rotate age-cluster", "rotate age-recovery", "rotate age-ci", "rotate refresh-bundle",
		"rotate secret SC-FOO --emergency", "rotate secret SC-FOO --stage", "rotate secret SC-FOO --value-from-staging",
		"state status", "state versions", "state restore --module compute --version-id v1", "state unlock",
		"state verify", "state import --module storage", "state migrate-backend",
		"unlock --run-id 20261013T101500Z-a1b2", "version",
		"configure --nodes n1,n2 --tags baseline,k3s --check -e k3s_force_cluster_init=true",
		"node add node-02 --confirm-plan ab12cd34", "node remove node-02 --allow-two-to-one --not-joined",
		"upgrade k3s --to v1.31.0+k3s1 --allow-patch-downgrade --force-binary-only", "edge-bypass",
		"storage apply --migrate", "forge-init --recover", "sso-init --rotate-breakglass --check-bootstrap-secret",
		"ci-init --recover", "handoff-rollback", "kubeconfig refresh --via-ssh --node n1",
		"recovery-bundle verify --full", "supply-chain init", "abandon-identity", "sync-local",
		"dns plan --provider manual", "run --from-phase seed", "run --retry-phase provision",
		"run --from-phase flux-bootstrap --resume-after-emergency", "expand --from-phase plan", "recover --retry-phase provision",
		"--non-interactive --log-format json -v --profile p preflight", "preflight --non-interactive --log-format text --verbose",
		"--env-file /x/y.env preflight", "--env-file=/x/y.env preflight",
		"--confirm-storage lab-one --confirm-budget 100 --confirm-backstops /x/f run",
		"toolbox selfcheck --json", "env s3", "backup now --component pg", "estop set --level workers",
		"seed", "flux-bootstrap", "forge init --only s",
	} {
		inv, err := cli.Parse(strings.Fields(line))
		if err != nil {
			t.Errorf("%q must parse: %v", line, err)
			continue
		}
		if len(inv.Command) == 0 {
			t.Errorf("%q parsed without a command", line)
			continue
		}
		if first := commandWord(line); first != "" && inv.Command[0] != first {
			t.Errorf("%q: command %v, want it to start with %q", line, inv.Command, first)
		}
	}
}

// commandWord is the first word of a command line that is not a global flag or its value ("" for a bare line).
func commandWord(line string) string {
	words := strings.Fields(line)
	for n := 0; n < len(words); n++ {
		w := words[n]
		switch {
		case !strings.HasPrefix(w, "-"):
			return w
		case w == "--env-file" || w == "--profile" || w == "--log-format" || w == "--confirm-plan" ||
			w == "--confirm-storage" || w == "--confirm-budget" || w == "--confirm-backstops":
			n++
		}
	}
	return ""
}

// Plan 50 section 7.1: unknown commands, unknown flags, missing values and misplaced pipeline-control flags are
// usage errors.
func TestParse_UsageErrors_Plan50S7_1(t *testing.T) {
	for _, line := range []string{
		"bogus", "bogus --profile p", "preflight --nope", "node", "node frobnicate", "state", "state frobnicate",
		"rotate", "rotate frobnicate", "--log-format xml preflight", "--log-format", "preflight --profile",
		"run --from-phase", "--resume-after-emergency", "run --resume-after-emergency --from-phase seed",
		"status --from-phase seed", "status --retry-phase provision", "unlock --run-id", "--env-file",
	} {
		if inv, err := cli.Parse(strings.Fields(line)); err == nil {
			t.Errorf("%q must be a usage error, parsed as %+v", line, inv)
		}
	}
}
