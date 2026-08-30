package config

import (
	"strings"
	"testing"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func completeEnv() map[string]string {
	return map[string]string{
		"DATABASE_HOST":      "pg",
		"DATABASE_PORT":      "5432",
		"DATABASE_NAME":      "strange-company",
		"DATABASE_USER":      "strange-company",
		"DATABASE_PASSWORD":  "hunter2",
		"VIKUNJA_URL":        "http://strange-company-vikunja:3456",
		"HERMES_GATEWAY_URL": "http://strange-company-hermes:8642",
	}
}

func TestLoadRequiresDatabaseHost(t *testing.T) {
	m := completeEnv()
	delete(m, "DATABASE_HOST")

	_, err := Load(env(m))

	if err == nil || !strings.Contains(err.Error(), "DATABASE_HOST") {
		t.Fatalf("want an error naming DATABASE_HOST, got %v", err)
	}
}

// An operator should be able to fix every missing variable in one pass rather
// than discovering them one restart at a time.
func TestLoadReportsEveryMissingVariableAtOnce(t *testing.T) {
	m := completeEnv()
	delete(m, "DATABASE_HOST")
	delete(m, "HERMES_GATEWAY_URL")

	_, err := Load(env(m))

	if err == nil {
		t.Fatal("want an error, got nil")
	}
	for _, want := range []string{"DATABASE_HOST", "HERMES_GATEWAY_URL"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should name %s, got %q", want, err.Error())
		}
	}
}

// An install that has retired the board runs without a Vikunja at all.
// Requiring the URL meant the control plane refused to start for want of a
// projection nobody had asked for.
func TestVikunjaIsOptional(t *testing.T) {
	m := completeEnv()
	delete(m, "VIKUNJA_URL")

	cfg, err := Load(env(m))
	if err != nil {
		t.Fatalf("Load without VIKUNJA_URL: %v", err)
	}
	if cfg.VikunjaURL != "" {
		t.Errorf("VikunjaURL = %q, want empty", cfg.VikunjaURL)
	}
}

// Still validated when supplied: a typo in a URL that IS configured is worth
// refusing to start over.
func TestAConfiguredVikunjaURLIsStillValidated(t *testing.T) {
	m := completeEnv()
	m["VIKUNJA_URL"] = "not-a-url"

	if _, err := Load(env(m)); err == nil {
		t.Fatal("Load accepted a malformed VIKUNJA_URL")
	}
}

// A fresh install legitimately has no Vikunja token yet: the control plane
// provisions one on first boot. It must not refuse to start.
func TestLoadSucceedsWithoutOptionalCredentials(t *testing.T) {
	cfg, err := Load(env(completeEnv()))
	if err != nil {
		t.Fatalf("optional credentials must not be required: %v", err)
	}
	if cfg.VikunjaToken != "" {
		t.Errorf("want empty VikunjaToken, got %q", cfg.VikunjaToken)
	}
}

func TestLoadRejectsANonNumericDatabasePort(t *testing.T) {
	m := completeEnv()
	m["DATABASE_PORT"] = "not-a-port"

	_, err := Load(env(m))

	if err == nil || !strings.Contains(err.Error(), "DATABASE_PORT") {
		t.Fatalf("want an error naming DATABASE_PORT, got %v", err)
	}
}

func TestRedactedNeverLeaksSecretValues(t *testing.T) {
	m := completeEnv()
	m["VIKUNJA_TOKEN"] = "vik-token"
	m["HERMES_API_KEY"] = "hermes-key"

	cfg, err := Load(env(m))
	if err != nil {
		t.Fatal(err)
	}

	secrets := []string{"hunter2", "vik-token", "hermes-key"}
	for k, v := range cfg.Redacted() {
		for _, s := range secrets {
			if v == s {
				t.Errorf("Redacted()[%q] leaked the secret value", k)
			}
		}
	}
}

// Redaction must still tell an operator whether a credential is configured,
// otherwise /config cannot help them debug a missing token.
func TestRedactedDistinguishesSetFromUnset(t *testing.T) {
	m := completeEnv()
	m["VIKUNJA_TOKEN"] = "vik-token"

	cfg, err := Load(env(m))
	if err != nil {
		t.Fatal(err)
	}
	r := cfg.Redacted()

	if r["VIKUNJA_TOKEN"] == r["HERMES_API_KEY"] {
		t.Fatalf("a set credential must render differently from an unset one, both were %q", r["VIKUNJA_TOKEN"])
	}
}

// A fresh install has no bootstrap credentials configured either -- the
// operator has supplied VIKUNJA_TOKEN directly, or intends to set one later.
// Bootstrap is then simply skipped, and config loading must not fail.
func TestLoadSucceedsWithoutBootstrapCredentials(t *testing.T) {
	cfg, err := Load(env(completeEnv()))
	if err != nil {
		t.Fatalf("bootstrap credentials must not be required: %v", err)
	}
	if cfg.VikunjaBootstrapUsername != "" || cfg.VikunjaBootstrapPassword != "" {
		t.Errorf("want empty bootstrap credentials, got username=%q password=%q",
			cfg.VikunjaBootstrapUsername, cfg.VikunjaBootstrapPassword)
	}
}

// The bootstrap password is as sensitive as any other credential here: it
// must never come back verbatim from Redacted().
func TestRedactedNeverLeaksTheBootstrapPassword(t *testing.T) {
	m := completeEnv()
	m["VIKUNJA_BOOTSTRAP_USERNAME"] = "strange-company-bootstrap"
	m["VIKUNJA_BOOTSTRAP_PASSWORD"] = "super-secret-bootstrap-password"

	cfg, err := Load(env(m))
	if err != nil {
		t.Fatal(err)
	}

	r := cfg.Redacted()
	for k, v := range r {
		if v == "super-secret-bootstrap-password" {
			t.Errorf("Redacted()[%q] leaked the bootstrap password", k)
		}
	}
	if r["VIKUNJA_BOOTSTRAP_PASSWORD"] != "***" {
		t.Errorf("want VIKUNJA_BOOTSTRAP_PASSWORD rendered as ***, got %q", r["VIKUNJA_BOOTSTRAP_PASSWORD"])
	}
	if r["VIKUNJA_BOOTSTRAP_USERNAME"] != "strange-company-bootstrap" {
		t.Errorf("want the bootstrap username reported plainly, got %q", r["VIKUNJA_BOOTSTRAP_USERNAME"])
	}
}

func TestDSNContainsHostPortAndDatabase(t *testing.T) {
	cfg, err := Load(env(completeEnv()))
	if err != nil {
		t.Fatal(err)
	}

	got := cfg.DSN()

	for _, want := range []string{"pg", "5432", "strange-company"} {
		if !strings.Contains(got, want) {
			t.Errorf("DSN %q should contain %q", got, want)
		}
	}
}
