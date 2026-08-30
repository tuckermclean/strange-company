// Package config parses the control plane's configuration contract.
//
// The variable names below are fixed by the strange-company Helm chart, which
// resolves them identically whether each dependency is bundled or external.
// Renaming one is a breaking change to that contract.
package config

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Config is the resolved contract. Nothing here is provider-specific: the chart
// has already decided which endpoints these point at.
type Config struct {
	DatabaseHost     string
	DatabasePort     int
	DatabaseName     string
	DatabaseUser     string
	DatabasePassword string
	DatabaseSSLMode  string

	VikunjaURL   string
	VikunjaToken string

	// VikunjaBootstrapUsername and VikunjaBootstrapPassword are optional: when
	// VikunjaToken is unset, they let the control plane log in (registering
	// the account first if necessary) and mint its own long-lived API token
	// on first boot. When both are empty, bootstrap is simply skipped.
	// VikunjaBoardShareWith are usernames granted access to the generated
	// board. The control plane creates the project as its own bootstrap
	// user, so without this the board is private to a service account and
	// no human can see the cards.
	VikunjaBoardShareWith []string

	// VikunjaBoardPermission is the Vikunja permission those users get:
	// 0 read, 1 read & write, 2 admin. Defaults to read & write, because a
	// human moving a card is a real input (spec §4.3) and read-only would
	// silently break it.
	VikunjaBoardPermission int

	VikunjaBootstrapUsername string
	VikunjaBootstrapPassword string

	HermesGatewayURL   string
	HermesAPIKey       string
	HermesDashboardURL string

	// GitHubAPIURL is the API root, so GitHub Enterprise is configuration
	// rather than a code change.
	GitHubAPIURL string

	// GitHubToken authenticates issue ingestion. Empty disables it.
	GitHubToken string

	// GitHubRepositories are the "owner/name" repositories to ingest from.
	GitHubRepositories []string

	// GitHubIngestLabel is the label that makes an issue eligible (spec §25).
	GitHubIngestLabel string

	// GitTokenSecret and GitTokenKey name the Kubernetes Secret holding the
	// credential a coding Job pushes its agent branch with.
	//
	// Deliberately separate from the token the control plane itself uses:
	// this one is handed to a coding agent, so it should carry the narrowest
	// scope that lets it push a branch -- contents:write, and NOT
	// workflows:write. An agent that can rewrite CI can weaken the gates
	// that verify it.
	GitTokenSecret string
	GitTokenKey    string

	// GitUsername is the HTTPS username paired with the token. GitHub
	// ignores it; it must simply be non-empty.
	GitUsername string

	// GitAuthorName and GitAuthorEmail identify the bot in commit metadata.
	GitAuthorName  string
	GitAuthorEmail string

	// VerificationMode chooses which backend answers the §11.3 and §19
	// gates: "github-actions" reads the checks CI already produced,
	// "test-command" runs .strange-company/test-command in a Job.
	VerificationMode string

	// Autonomy is how much of the loop runs without a human (§10.2).
	//
	//   "manual" (default) -- every specification waits for a person, by
	//     Vikunja label or through the Hermes conversation.
	//   "auto-approve-specs" -- the control plane signs any specification
	//     that would pass the gate if a human signed it. Nothing else is
	//     bypassed: an incomplete spec, an unverifiable criterion or a
	//     missing allowlist still stops the card.
	//
	// There is deliberately no setting that removes the human entirely.
	// §19 makes the human the final merge authority without exception, and
	// changing that is a decision for whoever owns the spec, not a value in
	// a config file.
	Autonomy string

	// SpecApprovalLabel is the Vikunja label a human adds to approve a
	// specification (spec §10.2). The board is a surface no model can
	// reach, which is what makes a label there a human decision.
	SpecApprovalLabel string

	// AgentRunsNamespace is where coding Jobs run (spec §16). Empty
	// disables coding phases entirely: without a namespace there is nowhere
	// to run them, and guessing one would create Jobs in a namespace nobody
	// granted access to.
	AgentRunsNamespace string

	// RunnerImage is the image coding Jobs run. Empty disables coding
	// phases: there is no sensible default, and a wrong one fails at pod
	// start rather than at configuration time.
	RunnerImage string

	// ServiceAccountDir is where the projected Kubernetes service account
	// lives.
	ServiceAccountDir string

	// PolicyDir is where operator-supplied policy is mounted. Empty means use
	// the policy compiled into the binary.
	PolicyDir string

	// CredentialsDir is where provider Secrets are projected, one directory
	// per Secret. The control plane reads the credential for a phase it
	// runs itself from <dir>/<secret>/<key>; see internal/credentials.
	CredentialsDir string

	Port              int
	ReconcileInterval time.Duration
}

const (
	defaultPort              = 8080
	defaultReconcileInterval = 60 * time.Second
	defaultSSLMode           = "disable"
	defaultCredentialsDir    = "/credentials"
	defaultBoardPermission   = 1
	defaultGitHubAPIURL      = "https://api.github.com"
	defaultIngestLabel       = "agent-ready"
	defaultServiceAcctDir    = "/var/run/secrets/kubernetes.io/serviceaccount"
	defaultSpecApprovalLabel = "spec-approved"

	// AutonomyManual is the default: every specification waits for a person.
	AutonomyManual = "manual"
	// AutonomyAutoApproveSpecs lets the control plane sign a specification
	// that would pass the gate if a human signed it.
	AutonomyAutoApproveSpecs = "auto-approve-specs"
	defaultGitUsername       = "strange-company"
	defaultGitAuthorName     = "strange-company agent"
	defaultGitAuthorEmail    = "agent@strange-company.invalid"

	// VerificationGitHubActions reads the checks the repository's own
	// workflows produced. The default: a repository that has CI has already
	// declared its tests, and a second declaration is the one that drifts.
	VerificationGitHubActions = "github-actions"

	// VerificationTestCommand runs .strange-company/test-command, for
	// repositories with no CI to read.
	VerificationTestCommand = "test-command"
)

// secretVars are never rendered verbatim by Redacted.
var secretVars = map[string]bool{
	"DATABASE_PASSWORD":          true,
	"VIKUNJA_TOKEN":              true,
	"VIKUNJA_BOOTSTRAP_PASSWORD": true,
	"GITHUB_TOKEN":               true,
	"HERMES_API_KEY":             true,
}

// Load reads the contract using the supplied lookup function.
//
// Every problem is collected before returning, so an operator can fix a broken
// deployment in one pass instead of one restart per missing variable.
func Load(getenv func(string) string) (*Config, error) {
	var problems []string

	required := func(name string) string {
		v := strings.TrimSpace(getenv(name))
		if v == "" {
			problems = append(problems, name+" is required")
		}
		return v
	}

	cfg := &Config{
		DatabaseHost:     required("DATABASE_HOST"),
		DatabaseName:     required("DATABASE_NAME"),
		DatabaseUser:     required("DATABASE_USER"),
		DatabasePassword: required("DATABASE_PASSWORD"),

		VikunjaURL:       strings.TrimSpace(getenv("VIKUNJA_URL")),
		HermesGatewayURL: required("HERMES_GATEWAY_URL"),

		// Optional: a fresh install has no Vikunja token until the control
		// plane provisions one, and Hermes needs no provider credentials to be
		// reachable.
		VikunjaToken:       strings.TrimSpace(getenv("VIKUNJA_TOKEN")),
		HermesAPIKey:       strings.TrimSpace(getenv("HERMES_API_KEY")),
		HermesDashboardURL: strings.TrimSpace(getenv("HERMES_DASHBOARD_URL")),

		// Optional: only needed to bootstrap a Vikunja token on first boot.
		// When VikunjaToken is already set, or when both of these are absent,
		// bootstrap is skipped entirely.
		GitTokenSecret:           strings.TrimSpace(getenv("GIT_TOKEN_SECRET")),
		GitTokenKey:              strings.TrimSpace(getenv("GIT_TOKEN_KEY")),
		GitUsername:              valueOr(getenv("GIT_USERNAME"), defaultGitUsername),
		GitAuthorName:            valueOr(getenv("GIT_AUTHOR_NAME"), defaultGitAuthorName),
		GitAuthorEmail:           valueOr(getenv("GIT_AUTHOR_EMAIL"), defaultGitAuthorEmail),
		VerificationMode:         valueOr(getenv("VERIFICATION_MODE"), VerificationGitHubActions),
		Autonomy:                 valueOr(getenv("AUTONOMY"), AutonomyManual),
		SpecApprovalLabel:        valueOr(getenv("SPEC_APPROVAL_LABEL"), defaultSpecApprovalLabel),
		AgentRunsNamespace:       strings.TrimSpace(getenv("AGENT_RUNS_NAMESPACE")),
		RunnerImage:              strings.TrimSpace(getenv("RUNNER_IMAGE")),
		ServiceAccountDir:        valueOr(getenv("SERVICE_ACCOUNT_DIR"), defaultServiceAcctDir),
		GitHubAPIURL:             valueOr(getenv("GITHUB_API_URL"), defaultGitHubAPIURL),
		GitHubToken:              strings.TrimSpace(getenv("GITHUB_TOKEN")),
		GitHubRepositories:       splitList(getenv("GITHUB_REPOSITORIES")),
		GitHubIngestLabel:        valueOr(getenv("GITHUB_INGEST_LABEL"), defaultIngestLabel),
		VikunjaBoardShareWith:    splitList(getenv("VIKUNJA_BOARD_SHARE_WITH")),
		VikunjaBootstrapUsername: strings.TrimSpace(getenv("VIKUNJA_BOOTSTRAP_USERNAME")),
		VikunjaBootstrapPassword: strings.TrimSpace(getenv("VIKUNJA_BOOTSTRAP_PASSWORD")),

		PolicyDir:         strings.TrimSpace(getenv("POLICY_DIR")),
		CredentialsDir:    valueOr(getenv("CREDENTIALS_DIR"), defaultCredentialsDir),
		DatabaseSSLMode:   valueOr(getenv("DATABASE_SSLMODE"), defaultSSLMode),
		ReconcileInterval: defaultReconcileInterval,
	}

	cfg.DatabasePort = intVar("DATABASE_PORT", getenv, 0, &problems)
	if cfg.DatabasePort == 0 {
		problems = append(problems, "DATABASE_PORT is required")
	}
	cfg.Port = intVar("PORT", getenv, defaultPort, &problems)
	cfg.VikunjaBoardPermission = intVar("VIKUNJA_BOARD_PERMISSION", getenv, defaultBoardPermission, &problems)

	if d := strings.TrimSpace(getenv("RECONCILE_INTERVAL")); d != "" {
		parsed, err := time.ParseDuration(d)
		if err != nil {
			problems = append(problems, "RECONCILE_INTERVAL is not a duration: "+err.Error())
		} else {
			cfg.ReconcileInterval = parsed
		}
	}

	// VIKUNJA_URL is optional: an install that has retired the board runs
	// without one, and the control plane must not merely tolerate that but
	// stop depending on it -- see the readiness checks in main, where a
	// Vikunja that is not configured must not be probed.
	for _, name := range []string{"VIKUNJA_URL", "HERMES_GATEWAY_URL"} {
		raw := strings.TrimSpace(getenv(name))
		if raw == "" {
			continue // absent, or already reported as missing
		}
		u, err := url.Parse(raw)
		if err != nil || u.Scheme == "" || u.Host == "" {
			problems = append(problems, name+" is not an absolute URL: "+raw)
		}
	}

	// A typo here would silently leave the loop in manual and the operator
	// wondering why nothing self-approves. Refusing to start says so once,
	// at the moment it can still be fixed.
	switch cfg.Autonomy {
	case AutonomyManual, AutonomyAutoApproveSpecs:
	default:
		problems = append(problems, fmt.Sprintf("AUTONOMY is %q; want %q or %q",
			cfg.Autonomy, AutonomyManual, AutonomyAutoApproveSpecs))
	}

	if len(problems) > 0 {
		return nil, fmt.Errorf("invalid configuration:\n  - %s", strings.Join(problems, "\n  - "))
	}
	return cfg, nil
}

func intVar(name string, getenv func(string) string, fallback int, problems *[]string) int {
	raw := strings.TrimSpace(getenv(name))
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		*problems = append(*problems, name+" is not a number: "+raw)
		return fallback
	}
	if n < 1 || n > 65535 {
		*problems = append(*problems, name+" is out of range: "+raw)
		return fallback
	}
	return n
}

func valueOr(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return strings.TrimSpace(v)
}

// DSN returns a libpq-style connection string. It contains the password, so it
// must never be logged or served; use Redacted for anything operator-facing.
func (c *Config) DSN() string {
	return fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=%s",
		url.QueryEscape(c.DatabaseUser),
		url.QueryEscape(c.DatabasePassword),
		c.DatabaseHost,
		c.DatabasePort,
		url.PathEscape(c.DatabaseName),
		c.DatabaseSSLMode,
	)
}

// Redacted renders the contract for humans. Secrets are reported as configured
// or not, never by value, so /config can help debug a missing token without
// becoming a way to read one.
func (c *Config) Redacted() map[string]string {
	out := map[string]string{
		"DATABASE_HOST":              c.DatabaseHost,
		"DATABASE_PORT":              strconv.Itoa(c.DatabasePort),
		"DATABASE_NAME":              c.DatabaseName,
		"DATABASE_USER":              c.DatabaseUser,
		"DATABASE_SSLMODE":           c.DatabaseSSLMode,
		"VIKUNJA_URL":                c.VikunjaURL,
		"VIKUNJA_BOOTSTRAP_USERNAME": c.VikunjaBootstrapUsername,
		"CREDENTIALS_DIR":            c.CredentialsDir,
		"GIT_TOKEN_SECRET":           c.GitTokenSecret,
		"GIT_TOKEN_KEY":              c.GitTokenKey,
		"GIT_AUTHOR_NAME":            c.GitAuthorName,
		"VERIFICATION_MODE":          c.VerificationMode,
		"AUTONOMY":                   c.Autonomy,
		"SPEC_APPROVAL_LABEL":        c.SpecApprovalLabel,
		"AGENT_RUNS_NAMESPACE":       c.AgentRunsNamespace,
		"RUNNER_IMAGE":               c.RunnerImage,
		"GITHUB_API_URL":             c.GitHubAPIURL,
		"GITHUB_REPOSITORIES":        strings.Join(c.GitHubRepositories, ","),
		"GITHUB_INGEST_LABEL":        c.GitHubIngestLabel,
		"VIKUNJA_BOARD_SHARE_WITH":   strings.Join(c.VikunjaBoardShareWith, ","),
		"HERMES_GATEWAY_URL":         c.HermesGatewayURL,
		"HERMES_DASHBOARD_URL":       c.HermesDashboardURL,
		"PORT":                       strconv.Itoa(c.Port),
		"RECONCILE_INTERVAL":         c.ReconcileInterval.String(),
	}
	for name, value := range map[string]string{
		"DATABASE_PASSWORD":          c.DatabasePassword,
		"VIKUNJA_TOKEN":              c.VikunjaToken,
		"VIKUNJA_BOOTSTRAP_PASSWORD": c.VikunjaBootstrapPassword,
		"GITHUB_TOKEN":               c.GitHubToken,
		"HERMES_API_KEY":             c.HermesAPIKey,
	} {
		if !secretVars[name] {
			continue
		}
		if value == "" {
			out[name] = "(unset)"
		} else {
			out[name] = "***"
		}
	}
	return out
}

// splitList parses a comma-separated environment value, dropping blanks.
//
// A stray comma in a Helm values list is the common way to get an empty entry,
// and an empty username would ask Vikunja to share a project with "".
func splitList(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}
