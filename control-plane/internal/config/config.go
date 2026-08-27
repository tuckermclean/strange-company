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
	VikunjaBootstrapUsername string
	VikunjaBootstrapPassword string

	HermesGatewayURL   string
	HermesAPIKey       string
	HermesDashboardURL string

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
)

// secretVars are never rendered verbatim by Redacted.
var secretVars = map[string]bool{
	"DATABASE_PASSWORD":          true,
	"VIKUNJA_TOKEN":              true,
	"VIKUNJA_BOOTSTRAP_PASSWORD": true,
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

		VikunjaURL:       required("VIKUNJA_URL"),
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

	if d := strings.TrimSpace(getenv("RECONCILE_INTERVAL")); d != "" {
		parsed, err := time.ParseDuration(d)
		if err != nil {
			problems = append(problems, "RECONCILE_INTERVAL is not a duration: "+err.Error())
		} else {
			cfg.ReconcileInterval = parsed
		}
	}

	for _, name := range []string{"VIKUNJA_URL", "HERMES_GATEWAY_URL"} {
		raw := strings.TrimSpace(getenv(name))
		if raw == "" {
			continue // already reported as missing
		}
		u, err := url.Parse(raw)
		if err != nil || u.Scheme == "" || u.Host == "" {
			problems = append(problems, name+" is not an absolute URL: "+raw)
		}
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
		"HERMES_GATEWAY_URL":         c.HermesGatewayURL,
		"HERMES_DASHBOARD_URL":       c.HermesDashboardURL,
		"PORT":                       strconv.Itoa(c.Port),
		"RECONCILE_INTERVAL":         c.ReconcileInterval.String(),
	}
	for name, value := range map[string]string{
		"DATABASE_PASSWORD":          c.DatabasePassword,
		"VIKUNJA_TOKEN":              c.VikunjaToken,
		"VIKUNJA_BOOTSTRAP_PASSWORD": c.VikunjaBootstrapPassword,
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
