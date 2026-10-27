package commit_test

import (
	"context"
	"testing"

	"strange.company/sc/internal/bootstrap/commit"
)

type commitChange = commit.Change

var commitCreate = commit.Create

func commitCheckScope(cluster string, c []commitChange) error {
	return commit.CheckScope(cluster, commit.Recovery, false, c)
}

// Review 1 of S5 (plan 50 section 13, 50-AC-14): the scope check judges the whole file, not the first YAML document.

// 50-AC-14: a policy/budgets.yaml with more than one YAML document, before or after, is refused.
func TestCheckScope_RecoveryMultiDocumentBudgetsFails_50AC14(t *testing.T) {
	extra := "---\nspec:\n  global:\n    monthlyUsd: \"99999\"\n"
	for name, c := range map[string]struct{ before, after []byte }{
		"appended document after":        {budgetsNone, append(append([]byte(nil), budgetsWorkers...), extra...)},
		"appended document on both":      {append(append([]byte(nil), budgetsNone...), extra...), append(append([]byte(nil), budgetsWorkers...), extra...)},
		"document appended before only":  {append(append([]byte(nil), budgetsNone...), extra...), budgetsWorkers},
		"document ahead of the real one": {budgetsNone, append([]byte(extra), budgetsWorkers...)},
	} {
		err := checkRecovery(c.before, c.after)
		wantViolation(t, name, err)
	}
}

// 50-AC-14 and contracts/budget-policy.schema.yaml (spec.emergencyStop: additionalProperties false, properties level
// and reason): under emergencyStop only level and reason may change or appear.
func TestCheckScope_RecoveryExtraEmergencyStopKeyFails_50AC14(t *testing.T) {
	after := append(append([]byte(nil), budgetsWorkers...), "    unlockAll: true\n"...)
	wantViolation(t, "extra emergencyStop key", checkRecovery(budgetsNone, after))
	before := append(append([]byte(nil), budgetsNone...), "    unlockAll: true\n"...)
	wantViolation(t, "extra emergencyStop key on both sides", checkRecovery(before, after))
}

// 50-AC-14 on a real repository: a refused commit leaves nothing staged even when one path of Paths exists neither
// on disk nor in the index (a single `git restore --staged` over such a pathspec fails and unstages nothing).
func TestCreate_RefusalUnstagesEverythingWithAbsentPath_50AC14(t *testing.T) {
	repo := newRepo(t)
	head := git(t, repo, "rev-parse", "HEAD")
	write(t, repo, "gitops/clusters/lab-one/bootstrap-config.yaml", []byte("a: 1\n"))
	write(t, repo, "policy/budgets.yaml", budgetsWorkers) // out of scope for an install commit
	r := req(repo)
	r.Paths = []string{"gitops/clusters/lab-one", ".sops.yaml", "policy/budgets.yaml"} // .sops.yaml is absent
	if _, err := commitCreate(context.Background(), r); err == nil {
		t.Fatal("Create committed a policy/budgets.yaml change in an install commit")
	} else if c := code(t, err); c != "COMMIT_SCOPE_VIOLATION" {
		t.Errorf("code %s, want COMMIT_SCOPE_VIOLATION", c)
	}
	if got := git(t, repo, "rev-parse", "HEAD"); got != head {
		t.Errorf("HEAD moved from %s to %s", head, got)
	}
	if got := git(t, repo, "diff", "--cached", "--name-only"); got != "" {
		t.Errorf("the index still holds staged paths after the refusal: %q", got)
	}
}

func checkRecovery(before, after []byte) error {
	return commitCheckScope(cluster, []commitChange{ch("policy/budgets.yaml", before, after)})
}
