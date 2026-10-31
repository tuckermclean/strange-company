package commit_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"strange.company/sc/internal/bootstrap/commit"
	"strange.company/sc/internal/clock"
)

const (
	cluster    = "lab-one"
	installUID = "6f1c2d3e-4b5a-4c6d-8e7f-0a1b2c3d4e5f"
)

// budgets builds a policy/budgets.yaml fixture (the fields the scope check reads; contracts/budget-policy.schema.yaml
// shapes emergencyStop as {level, reason}).
func budgets(monthly, level, reason string) []byte {
	s := "apiVersion: strange.company/v1alpha1\nkind: BudgetPolicy\nmetadata:\n  name: default\nspec:\n  currency: USD\n" +
		"  global:\n    hourlyUsd: \"20.00\"\n    dailyUsd: \"60.00\"\n    monthlyUsd: \"" + monthly + "\"\n  emergencyStop:\n    level: " + level + "\n"
	if reason != "" {
		s += "    reason: \"" + reason + "\"\n"
	}
	return []byte(s)
}

var (
	budgetsNone    = budgets("300.00", "none", "")
	budgetsWorkers = budgets("300.00", "workers", "black-start recovery lab-one")
)

func ch(path string, before, after []byte) commit.Change {
	return commit.Change{Path: path, Before: before, After: after}
}

func code(t *testing.T, err error) string {
	t.Helper()
	var ce *commit.Error
	if !errors.As(err, &ce) {
		t.Fatalf("error %v is not a *commit.Error", err)
	}
	return ce.Code
}

func wantViolation(t *testing.T, what string, err error) {
	t.Helper()
	if err == nil {
		t.Errorf("%s: scope check passed, want COMMIT_SCOPE_VIOLATION", what)
		return
	}
	if c := code(t, err); c != "COMMIT_SCOPE_VIOLATION" {
		t.Errorf("%s: code %s, want COMMIT_SCOPE_VIOLATION", what, c)
	}
}

// 50-AC-14: a fixture recovery commit that changes policy/budgets.yaml spec.emergencyStop to workers passes the scope
// check.
func TestCheckScope_RecoveryMayRaiseEmergencyStopToWorkers_50AC14(t *testing.T) {
	changes := []commit.Change{
		ch("policy/budgets.yaml", budgetsNone, budgetsWorkers),
		ch("gitops/clusters/lab-one/config/recovery.yaml", nil, []byte("generation: 2\n")),
		ch(".sops.yaml", []byte("creation_rules: []\n"), []byte("creation_rules:\n  - path_regex: x\n")),
	}
	if err := commit.CheckScope(cluster, commit.Recovery, false, changes); err != nil {
		t.Errorf("recovery commit raising emergencyStop to workers: %v", err)
	}
	// Re-indenting or reordering the file does not matter: the check compares parsed YAML nodes.
	reformatted := []byte("kind: BudgetPolicy\napiVersion: strange.company/v1alpha1\nmetadata: {name: default}\nspec:\n" +
		"    emergencyStop: {level: workers, reason: \"black-start recovery lab-one\"}\n    currency: USD\n" +
		"    global: {hourlyUsd: '20.00', dailyUsd: '60.00', monthlyUsd: '300.00'}\n")
	if err := commit.CheckScope(cluster, commit.Recovery, false, []commit.Change{ch("policy/budgets.yaml", budgetsNone, reformatted)}); err != nil {
		t.Errorf("reformatted file with only emergencyStop changed: %v", err)
	}
}

// 50-AC-14: the same commit also changing spec.global.monthlyUsd fails with COMMIT_SCOPE_VIOLATION.
func TestCheckScope_RecoveryChangingAnythingElseFails_50AC14(t *testing.T) {
	changes := []commit.Change{
		ch("policy/budgets.yaml", budgetsNone, budgets("900.00", "workers", "black-start recovery lab-one")),
		ch("gitops/clusters/lab-one/config/recovery.yaml", nil, []byte("generation: 2\n")),
	}
	err := commit.CheckScope(cluster, commit.Recovery, false, changes)
	wantViolation(t, "emergencyStop plus monthlyUsd", err)
	var ce *commit.Error
	if errors.As(err, &ce) && !containsStr(ce.Paths, "policy/budgets.yaml") {
		t.Errorf("violation paths %v do not name policy/budgets.yaml", ce.Paths)
	}
	// Adding a key anywhere else, or removing one, is a differing node too.
	extra := append(append([]byte(nil), budgetsWorkers...), []byte("  extra: 1\n")...)
	wantViolation(t, "extra key", commit.CheckScope(cluster, commit.Recovery, false, []commit.Change{ch("policy/budgets.yaml", budgetsNone, extra)}))
	wantViolation(t, "file created by the recovery commit", commit.CheckScope(cluster, commit.Recovery, false, []commit.Change{ch("policy/budgets.yaml", nil, budgetsWorkers)}))
	wantViolation(t, "file deleted", commit.CheckScope(cluster, commit.Recovery, false, []commit.Change{ch("policy/budgets.yaml", budgetsNone, nil)}))
	wantViolation(t, "unparseable file", commit.CheckScope(cluster, commit.Recovery, false, []commit.Change{ch("policy/budgets.yaml", budgetsNone, []byte("spec: [unclosed\n"))}))
}

// Plan 50 section 13: only to the value workers (or all when the pipeline runs with --drill); never back to none.
func TestCheckScope_EmergencyStopValuesAndDrill_50AC14(t *testing.T) {
	all := budgets("300.00", "all", "black-start recovery drill lab-one")
	none2 := budgets("300.00", "none", "")
	if err := commit.CheckScope(cluster, commit.Recovery, false, []commit.Change{ch("policy/budgets.yaml", budgetsNone, all)}); err == nil {
		t.Error("level all accepted without --drill")
	} else if code(t, err) != "COMMIT_SCOPE_VIOLATION" {
		t.Errorf("code %s", code(t, err))
	}
	if err := commit.CheckScope(cluster, commit.Recovery, true, []commit.Change{ch("policy/budgets.yaml", budgetsNone, all)}); err != nil {
		t.Errorf("level all with --drill: %v", err)
	}
	if err := commit.CheckScope(cluster, commit.Recovery, true, []commit.Change{ch("policy/budgets.yaml", budgetsNone, budgetsWorkers)}); err != nil {
		t.Errorf("level workers with --drill: %v", err)
	}
	wantViolation(t, "level back to none", commit.CheckScope(cluster, commit.Recovery, false, []commit.Change{ch("policy/budgets.yaml", budgetsWorkers, none2)}))
	wantViolation(t, "an invented level", commit.CheckScope(cluster, commit.Recovery, false, []commit.Change{ch("policy/budgets.yaml", budgetsNone, budgets("300.00", "hard", "black-start recovery lab-one"))}))
}

// 50-AC-14: an install (non-recovery) commit touching policy/budgets.yaml fails.
func TestCheckScope_InstallCommitMayNotTouchBudgets_50AC14(t *testing.T) {
	changes := []commit.Change{
		ch("gitops/clusters/lab-one/bootstrap-config.yaml", nil, []byte("a: 1\n")),
		ch("policy/budgets.yaml", budgetsNone, budgetsWorkers),
	}
	wantViolation(t, "install commit with the emergencyStop change", commit.CheckScope(cluster, commit.Install, false, changes))
	wantViolation(t, "install commit with --drill", commit.CheckScope(cluster, commit.Install, true, changes))
}

// Plan 50 section 13: a generated commit may touch only gitops/clusters/<cluster>/**, secrets/offcluster/<cluster>/**,
// secrets/ci/<cluster>/** and .sops.yaml.
func TestCheckScope_AllowedAndForbiddenPaths_50AC14(t *testing.T) {
	file := func(p string) []commit.Change { return []commit.Change{ch(p, nil, []byte("x: 1\n"))} }
	for _, p := range []string{
		"gitops/clusters/lab-one/bootstrap-config.yaml",
		"gitops/clusters/lab-one/vars/cluster-vars.yaml",
		"gitops/clusters/lab-one/secrets/monitoring/grafana-admin.sops.yaml",
		"secrets/offcluster/lab-one/k3s-token.sops.yaml",
		"secrets/ci/lab-one/release-signing.sops.yaml",
		".sops.yaml",
	} {
		for _, k := range []commit.Kind{commit.Install, commit.Recovery} {
			if err := commit.CheckScope(cluster, k, false, file(p)); err != nil {
				t.Errorf("kind %d: %s rejected: %v", k, p, err)
			}
		}
	}
	for _, p := range []string{
		"gitops/clusters/other/bootstrap-config.yaml",
		"gitops/clusters/lab-one-evil/x.yaml",
		"gitops/clusters/lab-one",
		"gitops/clusters/lab-one/../other/x.yaml",
		"secrets/offcluster/other/k3s-token.sops.yaml",
		"secrets/ci/other/x.sops.yaml",
		"secrets/strange-company/x.sops.yaml",
		"gitops/layers/policies.yaml",
		"policy/model-routing.yaml",
		"policy/budgets.yaml",
		"infra/bootstrap/settings.yaml",
		"README.md",
		"sub/.sops.yaml",
		"/etc/passwd",
		"",
	} {
		for _, k := range []commit.Kind{commit.Install, commit.Recovery} {
			wantViolation(t, "kind-"+string(rune('0'+k))+" "+p, commit.CheckScope(cluster, k, false, file(p)))
		}
	}
	if err := commit.CheckScope(cluster, commit.Install, false, nil); err != nil {
		t.Errorf("an empty change set: %v", err)
	}
}

// --- a real repository

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "HOME="+dir, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_AUTHOR_NAME=fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid", "GIT_COMMITTER_NAME=fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func write(t *testing.T, repo, rel string, data []byte) {
	t.Helper()
	p := filepath.Join(repo, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func newRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	git(t, repo, "init", "-q", "-b", "main")
	write(t, repo, "policy/budgets.yaml", budgetsNone)
	write(t, repo, "README.md", []byte("fixture\n"))
	git(t, repo, "add", "-A")
	git(t, repo, "commit", "-q", "-m", "fixture base")
	return repo
}

func req(repo string) commit.Request {
	return commit.Request{
		Repo: repo, Cluster: cluster, Kind: commit.Install, BaseDomain: "example.test",
		InstallUID: installUID, RunID: "20261027T120000Z-0102",
		Clock: clock.NewFake(time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)),
		Paths: []string{"gitops/clusters/lab-one", ".sops.yaml"},
	}
}

// Plan 50 section 14.3: the install commit has author sc-bootstrap <sc-bootstrap@<baseDomain>>, subject
// bootstrap(<cluster>): installation config and the trailers SC-Install-Uid and SC-Run-Id.
func TestCreate_InstallCommitShape_50AC14(t *testing.T) {
	repo := newRepo(t)
	write(t, repo, "gitops/clusters/lab-one/bootstrap-config.yaml", []byte("a: 1\n"))
	write(t, repo, "gitops/clusters/lab-one/vars/cluster-vars.yaml", []byte("b: 2\n"))
	write(t, repo, ".sops.yaml", []byte("creation_rules: []\n"))
	sha, err := commit.Create(context.Background(), req(repo))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if sha == "" || sha != git(t, repo, "rev-parse", "HEAD") {
		t.Fatalf("Create returned %q, HEAD is %q", sha, git(t, repo, "rev-parse", "HEAD"))
	}
	if got := git(t, repo, "log", "-1", "--format=%s"); got != "bootstrap(lab-one): installation config" {
		t.Errorf("subject %q", got)
	}
	if got := git(t, repo, "log", "-1", "--format=%an <%ae>"); got != "sc-bootstrap <sc-bootstrap@example.test>" {
		t.Errorf("author %q", got)
	}
	tr := git(t, repo, "log", "-1", "--format=%B")
	cmd := exec.Command("git", "interpret-trailers", "--parse")
	cmd.Dir = repo
	cmd.Stdin = strings.NewReader(tr)
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"SC-Install-Uid: " + installUID, "SC-Run-Id: 20261027T120000Z-0102"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("trailers %q lack %q", out, want)
		}
	}
	if got := git(t, repo, "log", "-1", "--format=%aI"); !strings.HasPrefix(got, "2026-10-27T12:00:00") {
		t.Errorf("author date %q, want the injected clock's time", got)
	}
	files := git(t, repo, "show", "--name-only", "--format=", "HEAD")
	for _, f := range []string{"gitops/clusters/lab-one/bootstrap-config.yaml", "gitops/clusters/lab-one/vars/cluster-vars.yaml", ".sops.yaml"} {
		if !strings.Contains(files, f) {
			t.Errorf("commit lacks %s: %q", f, files)
		}
	}
	// the Done check: HEAD's history carries the install uid
	if got, found, err := commit.FindInstall(context.Background(), repo, installUID); err != nil || !found || got != sha {
		t.Errorf("FindInstall = %q %v %v, want %s", got, found, err, sha)
	}
	if _, found, _ := commit.FindInstall(context.Background(), repo, "00000000-0000-4000-8000-000000000000"); found {
		t.Error("FindInstall found a commit for another install uid")
	}
}

func TestFindInstall_NoCommitYet_50AC14(t *testing.T) {
	repo := newRepo(t)
	if sha, found, err := commit.FindInstall(context.Background(), repo, installUID); err != nil || found {
		t.Errorf("FindInstall on a repository without the commit = %q %v %v", sha, found, err)
	}
}

// 50-AC-14 on a real repository: a scope violation aborts before any commit exists, with the working tree untouched.
func TestCreate_ScopeViolationAbortsWithoutCommit_50AC14(t *testing.T) {
	repo := newRepo(t)
	head := git(t, repo, "rev-parse", "HEAD")
	write(t, repo, "gitops/clusters/lab-one/bootstrap-config.yaml", []byte("a: 1\n"))
	write(t, repo, "policy/budgets.yaml", budgetsWorkers) // an install commit touching policy/budgets.yaml
	r := req(repo)
	r.Paths = []string{"gitops/clusters/lab-one", "policy/budgets.yaml"}
	sha, err := commit.Create(context.Background(), r)
	if err == nil {
		t.Fatalf("Create committed %s although policy/budgets.yaml changed in an install commit", sha)
	}
	if c := code(t, err); c != "COMMIT_SCOPE_VIOLATION" {
		t.Errorf("code %s, want COMMIT_SCOPE_VIOLATION", c)
	}
	if got := git(t, repo, "rev-parse", "HEAD"); got != head {
		t.Errorf("HEAD moved from %s to %s", head, got)
	}
	if got := git(t, repo, "diff", "--cached", "--name-only"); got != "" {
		t.Errorf("the index still holds staged paths: %q", got)
	}
	b, _ := os.ReadFile(filepath.Join(repo, "policy/budgets.yaml"))
	if string(b) != string(budgetsWorkers) {
		t.Error("the working tree file was reverted or rewritten")
	}
}

// Staged files outside Paths count too: the check runs on the whole staged diff, so a stray pre-staged file aborts.
func TestCreate_PreStagedStrayFileAborts_50AC14(t *testing.T) {
	repo := newRepo(t)
	write(t, repo, "gitops/clusters/lab-one/bootstrap-config.yaml", []byte("a: 1\n"))
	write(t, repo, "README.md", []byte("changed\n"))
	git(t, repo, "add", "README.md")
	head := git(t, repo, "rev-parse", "HEAD")
	if _, err := commit.Create(context.Background(), req(repo)); err == nil {
		t.Fatal("Create committed a pre-staged file outside the allowed paths")
	} else if code(t, err) != "COMMIT_SCOPE_VIOLATION" {
		t.Errorf("code %s", code(t, err))
	}
	if got := git(t, repo, "rev-parse", "HEAD"); got != head {
		t.Error("HEAD moved")
	}
}

// 50-AC-14 on a real repository: a recovery commit may raise emergencyStop; a recovery commit that also changes the
// monthly cap is refused.
func TestCreate_RecoveryCommitScope_50AC14(t *testing.T) {
	repo := newRepo(t)
	head := git(t, repo, "rev-parse", "HEAD")
	write(t, repo, "gitops/clusters/lab-one/config/recovery.yaml", []byte("generation: 2\n"))
	write(t, repo, "policy/budgets.yaml", budgets("900.00", "workers", "black-start recovery lab-one"))
	r := req(repo)
	r.Kind = commit.Recovery
	r.Subject = "bootstrap(lab-one): recovery overlay gen 2"
	r.Paths = []string{"gitops/clusters/lab-one", "policy/budgets.yaml"}
	if _, err := commit.Create(context.Background(), r); err == nil {
		t.Fatal("a recovery commit changing monthlyUsd was committed")
	} else if code(t, err) != "COMMIT_SCOPE_VIOLATION" {
		t.Errorf("code %s", code(t, err))
	}
	if git(t, repo, "rev-parse", "HEAD") != head {
		t.Fatal("HEAD moved after a refused commit")
	}

	write(t, repo, "policy/budgets.yaml", budgetsWorkers)
	sha, err := commit.Create(context.Background(), r)
	if err != nil {
		t.Fatalf("recovery commit raising only emergencyStop: %v", err)
	}
	if got := git(t, repo, "log", "-1", "--format=%s"); got != "bootstrap(lab-one): recovery overlay gen 2" {
		t.Errorf("subject %q", got)
	}
	if sha != git(t, repo, "rev-parse", "HEAD") {
		t.Error("Create did not return the new HEAD")
	}
}

func containsStr(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
