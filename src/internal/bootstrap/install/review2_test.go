package install_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// Review 2 of S6a (progress.md, "Review 1 (566f0ca)"): behaviors the first implementation got wrong.

// argsWith is i.args with another provider-backstop attestation file.
func (i *inst) argsWith(backstops string, a ...string) []string {
	return append([]string{"--env-file", i.envFile, "--confirm-budget", budgetMonthly, "--confirm-backstops", backstops}, a...)
}

func (i *inst) backstopRecord() string {
	return filepath.Join(i.repo, "gitops", "clusters", clusterName, "config", "provider-backstops.yaml")
}

func countConfirmations(j journal, kind string) int {
	n := 0
	for _, c := range j.Confirmations {
		if c.Kind == kind {
			n++
		}
	}
	return n
}

// B1 (plan 50 section 14.2 "preflight (always re-run; read-only)", section 16 retry ceiling, section 17.2): a preflight
// that fails for a missing confirmation exits 5 every time and never trips RETRY_CEILING; once the cause is fixed the
// run succeeds.
func TestPreflightFailure_NeverTripsRetryCeiling_RerunSucceeds_B1(t *testing.T) {
	i := newInst(t)
	noBudget := []string{"--env-file", i.envFile, "--confirm-backstops", i.backstopFile(), "--non-interactive", "--confirm-storage", clusterName}
	for n := 1; n <= 7; n++ {
		code, out := i.run("", merge(i.passphraseFile(), stopAfter("plan.state-migrate")), noBudget)
		if code != 5 {
			t.Fatalf("failed preflight #%d exits %d, want 5 (section 7.2: confirmation missing):\n%s", n, code, out)
		}
		if strings.Contains(out, "RETRY_CEILING") {
			t.Fatalf("failed preflight #%d reports RETRY_CEILING; preflight is always re-run (section 14.2):\n%s", n, out)
		}
	}
	code, out := i.run("", merge(i.passphraseFile(), stopAfter("plan.state-migrate")), i.args("--non-interactive", "--confirm-storage", clusterName))
	requireExit(t, code, 0, out)
	if strings.Contains(out, "RETRY_CEILING") {
		t.Errorf("the fixed run reports RETRY_CEILING:\n%s", out)
	}
}

// B2 (plan 50 section 17.1 rule 3 "unchanged input digest is a no-op", PF-04 "no tracked modifications"): a rerun with the
// same --confirm-backstops input leaves the work tree clean, keeps attestedAt, and records no second confirmation. A
// changed attestation does update the record.
func TestRerun_SameBackstops_NoChange_B2(t *testing.T) {
	i := newInst(t)
	env := merge(i.passphraseFile(), stopAfter("plan.state-migrate"))
	args := i.args("--non-interactive", "--confirm-storage", clusterName)
	code, out := i.run("", env, args)
	requireExit(t, code, 0, out)
	first, err := os.ReadFile(i.backstopRecord())
	if err != nil {
		t.Fatalf("the first run did not write %s: %v", i.backstopRecord(), err)
	}
	if i.git("status", "--porcelain") != "" {
		// The record belongs to the operator's tree; commit it as the operator would before the next run.
		i.git("add", "-A")
		i.git("-c", "user.name=fixture", "-c", "user.email=fixture@example.net", "commit", "-q", "-m", "operator: provider backstops")
	}
	before := countConfirmations(i.journal(), "provider-backstop")
	if before != 1 {
		t.Fatalf("provider-backstop confirmations after the first run = %d, want 1", before)
	}

	code, out = i.run("", env, args)
	requireExit(t, code, 0, out)
	if st := i.git("status", "--porcelain"); st != "" {
		t.Errorf("a rerun with unchanged input dirtied the work tree (PF-04 would refuse the next run):\n%s", st)
	}
	second, _ := os.ReadFile(i.backstopRecord())
	if string(first) != string(second) {
		t.Errorf("a rerun with the same attestation rewrote the record:\n--- first\n%s--- second\n%s", first, second)
	}
	if after := countConfirmations(i.journal(), "provider-backstop"); after != before {
		t.Errorf("provider-backstop confirmations = %d after the rerun, want %d (no second confirmation for the same input)", after, before)
	}

	// A different limit is a different input: the record follows it.
	changed, _ := yaml.Marshal(map[string]any{"providers": map[string]any{"anthropic": map[string]any{"limitUsd": 110, "consoleMfaEnabled": true}}})
	p := write(t, filepath.Join(i.dir, "backstop-110.yaml"), string(changed), 0o600)
	i.git("add", "-A")
	if i.git("status", "--porcelain") != "" {
		i.git("-c", "user.name=fixture", "-c", "user.email=fixture@example.net", "commit", "-q", "-m", "operator: tidy")
	}
	code, out = i.run("", env, i.argsWith(p, "--non-interactive", "--confirm-storage", clusterName))
	requireExit(t, code, 0, out)
	third, _ := os.ReadFile(i.backstopRecord())
	if !strings.Contains(string(third), "110") {
		t.Errorf("a changed limit did not update the record:\n%s", third)
	}
}

// B3a (plan 50 section 14.3 line 423 "If the journal is missing but the bundle exists, the bundle is the source of truth"):
// with the local journal deleted after plan.install-identity, a rerun with the passphrase adopts the bundle's identity.
func TestInstallIdentity_JournalMissingBundleExists_AdoptsBundle_B3(t *testing.T) {
	i := newInst(t)
	env := merge(i.passphraseFile(), stopAfter("plan.install-identity"))
	args := i.args("--non-interactive")
	code, out := i.run("", env, args)
	requireExit(t, code, 0, out)
	uid := i.journal().InstallUID
	if uid == "" {
		t.Fatal("no install_uid after plan.install-identity")
	}
	if err := os.Remove(filepath.Join(i.repo, ".bootstrap", "journal.json")); err != nil {
		t.Fatal(err)
	}

	code, out = i.run("", env, args)
	if strings.Contains(out, "BUNDLE_EXISTS_WITHOUT_JOURNAL") {
		t.Errorf("the rerun refuses with BUNDLE_EXISTS_WITHOUT_JOURNAL; the bundle is the source of truth:\n%s", out)
	}
	requireExit(t, code, 0, out)
	if got := i.journal().InstallUID; got != uid {
		t.Errorf("install_uid after the rerun = %q, want the bundle's %q", got, uid)
	}
	if c := i.installCommits(); len(c) != 1 {
		t.Errorf("installation config commits = %v, want exactly one", c)
	}
}

// B3b (same plan text, crash window between writing the bundle files and the journal checkpoint): the journal knows no
// install_uid and no bundle, the bundle files exist, and HEAD holds no installation commit. The rerun resumes from the bundle.
func TestInstallIdentity_CrashBeforeCheckpoint_ResumesFromBundle_B3(t *testing.T) {
	i := newInst(t)
	env := merge(i.passphraseFile(), stopAfter("plan.install-identity"))
	args := i.args("--non-interactive")
	code, out := i.run("", env, args)
	requireExit(t, code, 0, out)
	uid := i.journal().InstallUID

	jp := filepath.Join(i.repo, ".bootstrap", "journal.json")
	raw, err := os.ReadFile(jp)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	delete(doc, "install_uid")
	delete(doc, "recovery_bundle")
	delete(doc, "pipelines")
	delete(doc, "confirmations")
	out2, _ := json.Marshal(doc)
	if err := os.WriteFile(jp, out2, 0o600); err != nil {
		t.Fatal(err)
	}
	i.git("reset", "-q", "--hard", "HEAD~1")
	if len(i.installCommits()) != 0 {
		t.Fatal("fixture: the installation commit is still in the history")
	}

	code, out = i.run("", env, args)
	if strings.Contains(out, "BUNDLE_EXISTS_WITHOUT_JOURNAL") {
		t.Errorf("the rerun refuses with BUNDLE_EXISTS_WITHOUT_JOURNAL:\n%s", out)
	}
	requireExit(t, code, 0, out)
	if got := i.journal().InstallUID; got != uid {
		t.Errorf("install_uid after the crash-window rerun = %q, want the bundle's %q", got, uid)
	}
	if c := i.installCommits(); len(c) != 1 {
		t.Errorf("installation config commits = %v, want exactly one", c)
	}
}

// N3 (plan 50 section 14.3 done check "trailer SC-Install-Uid: <id>", 50-AC-18 abandon): a credentials commit with the right
// subject but another installation's SC-Install-Uid does not satisfy plan.storage-credentials.
func TestStorageCredentials_CommitRecognisedByUidTrailer_N3(t *testing.T) {
	i := newInst(t)
	const stale = "00000000-0000-4000-8000-00000000abcd"
	subject := "bootstrap(" + clusterName + "): object-storage credentials"
	i.git("-c", "user.name=fixture", "-c", "user.email=fixture@example.net", "commit", "-q", "--allow-empty",
		"-m", subject+"\n\nSC-Install-Uid: "+stale+"\nSC-Run-Id: abandoned")

	code, out := i.run("", merge(i.passphraseFile(), stopAfter("plan.storage-credentials")), i.args("--non-interactive", "--confirm-storage", clusterName))
	requireExit(t, code, 0, out)
	uid := i.journal().InstallUID
	if uid == "" || uid == stale {
		t.Fatalf("install_uid = %q", uid)
	}
	mine := 0
	for _, h := range strings.Fields(i.git("log", "--format=%H", "--grep=SC-Install-Uid: "+uid)) {
		if i.git("log", "-1", "--format=%s", h) == subject {
			mine++
		}
	}
	if mine != 1 {
		t.Errorf("commits with subject %q and SC-Install-Uid %s = %d, want 1: the abandoned installation's commit must not satisfy the step", subject, uid, mine)
	}
}
