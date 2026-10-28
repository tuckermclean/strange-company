package commit_test

import (
	"context"
	"testing"

	"strange.company/sc/internal/bootstrap/commit"
)

// S6b / S6a review NB2 (plan 50 section 14.3 done check: "HEAD contains a commit with trailer SC-Install-Uid" for the
// installation config commit): every generated commit of an installation carries the trailer, so FindInstall must
// judge by the `installation config` subject, not return the newest commit with the uid. After provision there are
// three such commits; the install-identity output reference must stay the installation config commit.

func createWithSubject(t *testing.T, repo, subject, rel string) string {
	t.Helper()
	write(t, repo, rel, []byte(subject+"\n"))
	r := req(repo)
	r.Subject = subject
	sha, err := commit.Create(context.Background(), r)
	if err != nil {
		t.Fatalf("Create %q: %v", subject, err)
	}
	return sha
}

func TestFindInstall_ReturnsTheInstallationConfigCommit_NB2(t *testing.T) {
	repo := newRepo(t)
	install := createWithSubject(t, repo, "bootstrap(lab-one): installation config", "gitops/clusters/lab-one/bootstrap-config.yaml")
	createWithSubject(t, repo, "bootstrap(lab-one): object-storage credentials", "gitops/clusters/lab-one/vars/creds.yaml")
	createWithSubject(t, repo, "bootstrap(lab-one): node inventory", "gitops/clusters/lab-one/inventory.yaml")

	got, found, err := commit.FindInstall(context.Background(), repo, installUID)
	if err != nil || !found {
		t.Fatalf("FindInstall = %q %v %v, want the installation config commit", got, found, err)
	}
	if got != install {
		t.Errorf("FindInstall = %s (%s), want the installation config commit %s", got, git(t, repo, "log", "-1", "--format=%s", got), install)
	}
}

func TestFindInstall_LaterCommitsAloneAreNotTheInstallCommit_NB2(t *testing.T) {
	repo := newRepo(t)
	createWithSubject(t, repo, "bootstrap(lab-one): object-storage credentials", "gitops/clusters/lab-one/vars/creds.yaml")
	createWithSubject(t, repo, "bootstrap(lab-one): node inventory", "gitops/clusters/lab-one/inventory.yaml")
	if got, found, err := commit.FindInstall(context.Background(), repo, installUID); err != nil || found {
		t.Errorf("FindInstall = %q %v %v; commits with the uid trailer but another subject must not satisfy plan.install-identity", got, found, err)
	}
}
