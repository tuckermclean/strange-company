package policy_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/tuckermclean/strange-company/control-plane/internal/policy"
)

// A card with no permitted-actions block cannot pass the §10 gate, so every
// card needs one at creation. The default has to load, or nothing is ever
// promotable.
func TestTheDefaultActionsPolicyLoads(t *testing.T) {
	p, _, err := policy.LoadOrDefaults("")
	if err != nil {
		t.Fatalf("LoadOrDefaults: %v", err)
	}
	actions := p.DefaultPermittedActions()
	if actions == nil {
		t.Fatal("no default permitted-actions policy")
	}
	if len(actions.Files.Include) == 0 {
		t.Error("the default permits no files at all, so no card could do any work")
	}
	if len(actions.Commands) == 0 {
		t.Error("the default permits no commands, so no card could run its tests")
	}
}

// §24's forbidden set is forbidden by absence from the allowlist. A default
// that reached outside the workspace, or allowed arbitrary network access,
// would quietly grant what §24 forbids.
func TestTheDefaultGrantsNoNetworkOrEndpoints(t *testing.T) {
	p, _, err := policy.LoadOrDefaults("")
	if err != nil {
		t.Fatalf("LoadOrDefaults: %v", err)
	}
	actions := p.DefaultPermittedActions()
	if len(actions.Network) != 0 {
		t.Errorf("the default grants network access: %v", actions.Network)
	}
	if len(actions.Endpoints) != 0 {
		t.Errorf("the default grants endpoints: %v", actions.Endpoints)
	}
}

// An operator overriding policy must be able to override this too, or the
// allowlist is only editable by rebuilding the image.
func TestAnOperatorCanSupplyTheirOwnActions(t *testing.T) {
	dir := t.TempDir()
	writePolicyDir(t, dir, `
files:
  include: ["src/**"]
  exclude: ["src/vendor/**"]
commands: ["test"]
endpoints: []
network: []
`)

	p, operatorSupplied, err := policy.LoadOrDefaults(dir)
	if err != nil {
		t.Fatalf("LoadOrDefaults: %v", err)
	}
	if !operatorSupplied {
		t.Fatal("operator policy was not reported as in force")
	}
	actions := p.DefaultPermittedActions()
	if len(actions.Files.Include) != 1 || actions.Files.Include[0] != "src/**" {
		t.Fatalf("include = %v", actions.Files.Include)
	}
	if len(actions.Files.Exclude) != 1 {
		t.Fatalf("exclude = %v", actions.Files.Exclude)
	}
}

// A policy directory that supplies providers and models but no actions is the
// common case for an operator who only wanted to change models. It must fall
// back to the shipped allowlist rather than leaving cards unpromotable.
func TestAMissingActionsFileFallsBackToTheDefault(t *testing.T) {
	dir := t.TempDir()
	writePolicyDir(t, dir, "")

	p, _, err := policy.LoadOrDefaults(dir)
	if err != nil {
		t.Fatalf("LoadOrDefaults: %v", err)
	}
	if p.DefaultPermittedActions() == nil {
		t.Fatal("no actions policy after falling back")
	}
}

// writePolicyDir writes a minimal valid policy directory, plus actions.yaml
// when actions is non-empty. providers and models are required by LoadDir;
// their content is irrelevant to these tests.
func writePolicyDir(t *testing.T, dir, actions string) {
	t.Helper()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("providers.yaml", `
providers:
  local:
    harness: hermes
    baseUrl: http://localhost:11434
`)
	write("models.yaml", `
aliases:
  cheap:
    provider: local
    model: a-model
phases:
  foreman:
    - model: cheap
      max_attempts: 1
`)
	if actions != "" {
		write("actions.yaml", actions)
	}
}
