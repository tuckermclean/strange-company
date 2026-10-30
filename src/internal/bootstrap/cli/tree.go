package cli

import "strings"

// cmd is one node of the command tree. flags maps a flag name (no dashes) to whether it takes a value; lenient
// accepts any flag; maxArgs bounds the positional arguments (-1: unbounded); subs, when set, requires a
// subcommand word.
type cmd struct {
	subs    map[string]*cmd
	flags   map[string]bool
	opt     map[string][]string // optional-value flags: the values a following word may take (nil: any non-flag word)
	lenient bool
	maxArgs int
}

// leaf builds a command from a space-separated flag list: "json" is a boolean flag, "only=" takes a value, "*"
// accepts any flag.
func leaf(flags string, maxArgs int) *cmd {
	c := &cmd{flags: map[string]bool{}, maxArgs: maxArgs}
	for _, f := range strings.Fields(flags) {
		switch {
		case f == "*":
			c.lenient = true
		case strings.Contains(f, "?"):
			n, vals, _ := strings.Cut(f, "?")
			c.flags[n] = false
			if c.opt == nil {
				c.opt = map[string][]string{}
			}
			if vals != "" {
				c.opt[n] = strings.Split(vals, "|")
			} else {
				c.opt[n] = nil
			}
		case strings.HasSuffix(f, "="):
			c.flags[strings.TrimSuffix(f, "=")] = true
		default:
			c.flags[f] = false
		}
	}
	return c
}

// group builds a command that needs a subcommand word.
func group(subs map[string]*cmd) *cmd { return &cmd{subs: subs, flags: map[string]bool{}, maxArgs: -1} }

const pipeline = "from-phase= retry-phase= resume-after-emergency"

// tree is the plan 50 section 7.1 / 7.1.1 command tree. A command listed here parses; one that sc-bootstrap does
// not implement yet exits 2 with COMMAND_NOT_IMPLEMENTED (CD-70(e)).
var tree = map[string]*cmd{
	"run":              leaf(pipeline, 0),
	"preflight":        leaf("", 0),
	"plan":             leaf("", 0),
	"status":           leaf("json", 0),
	"verify":           leaf("json only= check= ack-notifications", 1),
	"expand":           leaf(pipeline, 0),
	"destroy":          leaf("dry-run skip-final-backup accept-unmirrored-loss", 0),
	"recover":          leaf(pipeline+" scope= bundle= old-installation= no-backups from-capsule= pitr-target= skip-longhorn-restore= drill drill-source= step=", 0),
	"build":            leaf("no-cache platform= f= target=", 0),
	"report":           leaf("run-id= json", 0),
	"unlock":           leaf("run-id=", 0),
	"capsule":          group(map[string]*cmd{"publish": leaf("", 0)}),
	"version":          leaf("", 0),
	"configure":        leaf("nodes= tags= check e=", 0),
	"edge-bypass":      leaf("", 0),
	"seed":             leaf("emergency-source commit=", 0),
	"seed-gc":          leaf("", 0),
	"flux-bootstrap":   leaf("", 0),
	"forge-init":       leaf("recover", 0),
	"sso-init":         leaf("rotate-breakglass check-bootstrap-secret from=", 0),
	"ci-init":          leaf("recover", 0),
	"handoff-rollback": leaf("", 0),
	"abandon-identity": leaf("", 0),
	"sync-local":       leaf("", 0),
	"pre-seed":         leaf("", 0),
	"seal":             leaf("", 0),
	"telemetry-decide": leaf("verify", 0),
	"rotate": group(map[string]*cmd{
		"recovery-bundle": leaf("", 0), "tofu-passphrase": leaf("reencrypt-history", 0), "age-cluster": leaf("abort", 0),
		"age-recovery": leaf("", 0), "age-ci": leaf("", 0), "refresh-bundle": leaf("from-version=", 0),
		"secret": leaf("emergency stage?add|switch|retire value-from-staging new-public-key= abort", 1),
	}),
	"state": group(map[string]*cmd{
		"status": leaf("module= json", 0), "versions": leaf("module= json", 0),
		"restore": leaf("module= version-id=", 0), "unlock": leaf("module=", 0), "verify": leaf("module= json", 0),
		"import": leaf("module=", 0), "migrate-backend": leaf("", 0),
	}),
	"node": group(map[string]*cmd{
		"add": leaf("", 1), "remove": leaf("allow-two-to-one not-joined", 1), "exec": leaf("", -1),
	}),
	"upgrade":    group(map[string]*cmd{"k3s": leaf("to= allow-patch-downgrade force-binary-only", 0)}),
	"storage":    group(map[string]*cmd{"apply": leaf("migrate", 0)}),
	"kubeconfig": group(map[string]*cmd{"refresh": leaf("via-ssh? node= identity=", 0)}),
	"recovery-bundle": group(map[string]*cmd{
		"status": leaf("json", 0), "verify": leaf("full", 0), "rebuild": leaf("from-recovery-identity black-start", 0),
	}),
	"supply-chain": group(map[string]*cmd{"init": leaf("abort", 0), "rotate-signing-key": leaf("stage?add", 0)}),
	"dns": group(map[string]*cmd{
		"plan": leaf("provider= mode=", 0), "diff": leaf("provider=", 0), "apply": leaf("exclude=", 0),
	}),
	"backup": group(map[string]*cmd{
		"now": leaf("component=", 0), "status": leaf("*", 0), "open-bundle": leaf("manifest=", 0),
	}),
	"estop": group(map[string]*cmd{"set": leaf("level= reason=", 0)}),
	"forge": group(map[string]*cmd{
		"init": leaf("only= recover", 0), "apply": leaf("*", -1), "verify": leaf("*", 0), "diff": leaf("*", 0),
		"finalize": leaf("*", 0), "api": leaf("*", 2), "key-hash": leaf("source=", 0), "hostkey": leaf("*", -1),
		"breakglass": leaf("*", -1), "admin-password": leaf("token-from-stdin", 1), "clone-test": leaf("as=", 0), "statuses": leaf("since=", 0),
	}),
	"toolbox": group(map[string]*cmd{
		"fetch": leaf("lock= arch= dest= set=", 0), "selfcheck": leaf("json embedded-only", 0), "init": leaf("", 0),
	}),
	"env": leaf("", 2),
	"secrets": group(map[string]*cmd{
		"generate": leaf("*", -1), "status": leaf("*", -1), "compare": leaf("*", -1), "record": leaf("*", -1),
		"scan": leaf("*", -1), "lint": leaf("*", -1), "reencrypt": leaf("*", -1), "rotate": leaf("*", -1), "gen": leaf("*", -1),
	}),
	"provider": group(map[string]*cmd{"delete": leaf("id= confirm-fenced=", 0)}),
	"bundle": group(map[string]*cmd{
		"open": leaf("show= extract= from-offcluster= to=", 0), "manifest": leaf("*", 0), "export-env": leaf("*", 0),
		"stage-rotation": leaf("abort", 1),
	}),
	"sops-age": group(map[string]*cmd{
		"install": leaf("exact", 0), "fingerprint": leaf("*", 0), "rewrap-job": leaf("new-recipients=", 0),
	}),
	"k3s":  group(map[string]*cmd{"secrets-encrypt-rotate": leaf("", 0)}),
	"etcd": group(map[string]*cmd{"snapshot": leaf("name=", 0)}),
	"mirror": group(map[string]*cmd{
		"status": leaf("json", 0), "sync": leaf("*", 0), "verify": leaf("run-id=", 0), "check-github": leaf("init-canary repository=", 0),
		"recreate": leaf("token-from=", 0), "freshness": leaf("*", 0), "compare-refs": leaf("*", 0),
		"delete-github-tag": leaf("*", 1), "gh-api": leaf("*", 1),
	}),
	"access": group(map[string]*cmd{
		"join": leaf("email= name= role=", 1), "leave": leaf("*", 1), "reset-mfa": leaf("*", 1), "revoke-sessions": leaf("*", 1),
	}),
	"authentik":  group(map[string]*cmd{"key-hash": leaf("source=", 0), "blueprints": leaf("*", 0), "login-test": leaf("*", 0)}),
	"breakglass": group(map[string]*cmd{"drill": leaf("only= phase=", 0)}),
	"probes":     group(map[string]*cmd{"serve": leaf("config= listen=", 0)}),
	"ci": group(map[string]*cmd{
		"init": leaf("only= rotate=", 0), "login": leaf("*", 1), "verify": leaf("post-upgrade", 0), "rotate": leaf("*", 1),
		"api": leaf("*", 2), "apply": leaf("*", 0), "smoke": leaf("*", 0),
	}),
	"ci-secrets": group(map[string]*cmd{"apply": leaf("delete? delete-all-release", 0)}),
	"tunnel":     group(map[string]*cmd{"ensure": leaf("*", 0), "rotate-token": leaf("*", 0)}),
	"gen":        group(map[string]*cmd{"public-hosts": leaf("*", 0)}),
	"objstore":   group(map[string]*cmd{"render-lifecycle": leaf("bucket=", 0)}),
	"buckets":    group(map[string]*cmd{"inventory": leaf("*", 0), "audit": leaf("since=", 0)}),
	"drill":      leaf("type= *", -1),
	"runner": group(map[string]*cmd{
		"pause": leaf("*", 0), "cancel": leaf("all", 0), "dispatch": leaf("*", 1),
	}),
	"ledger":        group(map[string]*cmd{"reconcile": leaf("provider= since=", 0)}),
	"checks":        leaf("", 1),
	"notify":        group(map[string]*cmd{"test": leaf("", 0)}),
	"notifications": group(map[string]*cmd{"render": leaf("cluster=", 0)}),
	"evidence":      group(map[string]*cmd{"put": leaf("attach=", 2), "collect": leaf("incident= since= resource=", 0)}),
}
