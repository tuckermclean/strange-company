package crash_test

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// 50-AC-19 (BOOT-7): new machine. The machine dies (SIGKILL) after forge-init; the operator clones the Git repository
// at the install's parent commit on a new machine (a fresh workspace without .bootstrap/), provides bootstrap.env and
// the recovery bundle copies (the blackstart directory), and runs ./bootstrap again against the same S3. The remote
// journal resumes the pipeline.
//
// What this build can and cannot show of the acceptance criterion, and what these tests fix:
//   - there is no Forgejo behind the stubbed forge-init: no pushed_sha fact, no token list, no Forgejo main to
//     fast-forward from. Plan 50 section 20 gives the rule for this case: "before forge-init, the install commits exist
//     only on the lost machine; sc-bootstrap detects that gitops/clusters/<cluster>/ is missing from HEAD while the
//     remote journal shows plan succeeded, and re-renders the files from the bundle". The observable "no divergent
//     commit" is then a linear history above the parent commit with exactly the commits of the lost machine's history
//     (same subjects, one installation config commit), and "no new forge-init token minted" is that the forge-init
//     phase record of the remote journal is untouched (same attempt count, start and finish);
//   - the fake provider's state and the stub cluster (.bootstrap/fake-provider, .bootstrap/stub-cluster) stand for the
//     cloud and the cluster, which outlive the machine: the new machine is given a copy;
//   - the dead machine's run lock object is still in the store. A new machine has no local journal and so no install_uid
//     to prove the holder is its own dead run, and in reality another host name: plan 50 section 17.4 has the operator
//     break the lock with `./bootstrap unlock` (typed confirmation), a command this build does not implement yet. The
//     tests delete the lock object, which is all that command does, before the new machine runs;
//   - the second case is the other new-machine case of the plan: seed complete, forge-init not started.

func copyTree(t testing.TB, from, to string) {
	t.Helper()
	err := filepath.WalkDir(from, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(from, p)
		dst := filepath.Join(to, rel)
		if d.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, b, 0o644)
	})
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

// newMachine is a fresh workspace for the same installation: a clone of the repository reset to the parent commit
// parent, the same bootstrap.env, the same blackstart directory (the operator provides the bundle copies) and the same S3.
func (a *scenario) newMachine(parent string) *scenario {
	a.t.Helper()
	b := &scenario{t: a.t, w: a.w, dir: a.t.TempDir(), blackstart: a.blackstart, key: a.key, prefix: a.prefix, vars: a.vars, envFile: a.envFile, s3side: a.s3side}
	b.repo = filepath.Join(b.dir, "repo")
	b.logDir = filepath.Join(b.dir, "log")
	if err := os.MkdirAll(filepath.Join(b.dir, "home"), 0o755); err != nil {
		a.t.Fatal(err)
	}
	gitIn(a.t, a.dir, "clone", "-q", a.repo, b.repo)
	gitIn(a.t, b.repo, "reset", "-q", "--hard", parent)
	b.env = map[string]string{}
	for k, v := range a.env {
		b.env[k] = v
	}
	b.env["SC_REPO_ROOT"], b.env["HOME"], b.env["CRASH_TEST_LOG_DIR"] = b.repo, filepath.Join(b.dir, "home"), b.logDir
	for _, d := range []string{"fake-provider", "stub-cluster"} {
		copyTree(a.t, filepath.Join(a.repo, ".bootstrap", d), filepath.Join(b.repo, ".bootstrap", d))
	}
	return b
}

func phaseRecord(j map[string]any, name string) map[string]any {
	pipes, _ := j["pipelines"].([]any)
	for _, p := range pipes {
		phases, _ := p.(map[string]any)["phases"].([]any)
		for _, ph := range phases {
			if m, _ := ph.(map[string]any); m["name"] == name {
				return m
			}
		}
	}
	return nil
}

func TestNewMachine_FreshWorkspaceResumesFromTheRemoteJournal_NoDivergence_50AC19(t *testing.T) {
	t.Parallel()
	for _, point := range []string{"forge-init:after", "forge-init:before"} {
		t.Run(strings.ReplaceAll(point, ":", "_"), func(t *testing.T) {
			t.Parallel()
			ref := theReference(t)
			a := newScenario(t)
			env := a.unattendedEnv()
			args := a.runArgs("--non-interactive")
			crashed := a.run("", merge(env, map[string]string{"SC_TEST_CRASH_AT": point}), args)
			if !crashed.killedByKill() {
				t.Fatalf("SC_TEST_CRASH_AT=%s: the engine ended with exit %d (signal %v), want death by SIGKILL:\n%s", point, crashed.exit, crashed.signal, tail(crashed.out, 2000))
			}
			parent := a.rootCommit()
			aSubjects := commitSubjects(t, a.repo)
			sort.Strings(aSubjects)
			if countSubject(aSubjects, a.installSubject()) != 1 {
				t.Fatalf("fixture: the lost machine's history has no single installation config commit:\n%s", strings.Join(aSubjects, "\n"))
			}
			aForge := phaseRecord(a.journal(), "forge-init")

			b := a.newMachine(parent)
			if _, held := a.lockObject(); !held {
				t.Fatal("fixture: the killed run left no lock object")
			}
			if err := a.client(true).Bucket(a.bucket("tofu-state")).Delete(context.Background(), a.lockKey(), ""); err != nil {
				t.Fatalf("breaking the dead machine's lock: %v", err)
			}
			if got := b.git("rev-parse", "HEAD"); got != parent {
				t.Fatalf("fixture: the new workspace is at %s, want the parent commit %s", got, parent)
			}
			if _, ok := b.journalBytes(); ok {
				t.Fatal("fixture: the new workspace has a local journal")
			}
			res := b.run("", env, b.runArgs("--non-interactive"))
			b.requireExit(res, 0, "50-AC-19: the rerun on the new machine")
			b.statusValidates("on the new machine")

			bSubjects := commitSubjects(t, b.repo)
			sort.Strings(bSubjects)
			if !reflect.DeepEqual(bSubjects, aSubjects) {
				t.Errorf("50-AC-19: the new machine's history has the subjects\n%s\nwant the lost machine's\n%s", strings.Join(bSubjects, "\n"), strings.Join(aSubjects, "\n"))
			}
			if merges := b.git("rev-list", "--merges", "HEAD"); merges != "" {
				t.Errorf("50-AC-19: the new machine's history has merge commits (a divergence was merged): %s", merges)
			}
			if root := b.rootCommit(); root != parent {
				t.Errorf("50-AC-19: the new machine's history is rooted at %s, want the parent commit %s", root, parent)
			}
			if got := b.git("symbolic-ref", "--short", "HEAD"); got != "main" {
				t.Errorf("50-AC-19: the new machine is on branch %s, want main", got)
			}
			if st := b.git("status", "--porcelain"); st != "" {
				t.Errorf("50-AC-19: the new machine's work tree is not clean after the run:\n%s", st)
			}
			if !reflect.DeepEqual(projection(b.journal()), ref.proj) {
				t.Errorf("50-AC-19: the journal on the new machine differs from the crash-free reference: %v", projection(b.journal()))
			}
			b.assertConverged("on the new machine")
			if point == "forge-init:after" {
				bForge := phaseRecord(b.journal(), "forge-init")
				for _, k := range []string{"attempt", "started_at", "finished_at"} {
					if aForge[k] != bForge[k] {
						t.Errorf("50-AC-19: forge-init %s is %v on the new machine, was %v on the lost one: the phase ran again (a second token would be minted)", k, bForge[k], aForge[k])
					}
				}
			}
		})
	}
}
