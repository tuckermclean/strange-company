//go:build integration

package tofustate_test

// Tier-E tests of S7 (plan 52 D2): real `tofu` 1.13.1 and real SeaweedFS, no provider plugin. They exercise the fake compute
// module of CD-70(d) (`infra/tofu/compute/fake`, only the built-in terraform_data resource, refused outside SC_TEST_MODE=1)
// through the rendered S3 backend and the rendered TF_ENCRYPTION, for 06-AC-3 (lock contention with a real holder), 06-AC-6
// (rotation with the stable p<N> key provider names) and 06-AC-8 (restore drill). `make test-integration` runs them with
//
//	cd src && go test -count=1 -tags integration -run Integration ./internal/tofustate/...
//
// The module's inputs are TF_VAR_cluster, TF_VAR_nodes (node name to internal IP) and TF_VAR_sc_test_mode (SC_TEST_MODE).

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"strange.company/sc/internal/clock"
	"strange.company/sc/internal/s3client"
	"strange.company/sc/internal/tofustate"
)

const (
	nodesOne = `{"node-01":"10.0.0.11"}`
	nodesTwo = `{"node-01":"10.0.0.11","node-02":"10.0.0.12"}`
)

func repoRootIT(t *testing.T) string { return filepath.Dir(filepath.Dir(repoOut(t))) }

func copyTF(t *testing.T, src, dst string) {
	t.Helper()
	files, _ := filepath.Glob(filepath.Join(src, "*.tf"))
	if len(files) == 0 {
		t.Fatalf("%s has no .tf files: the fake compute module of CD-70(d) does not exist", src)
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dst, filepath.Base(f)), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// fakeEnv is the process environment of a tofu run on the fake compute module.
func (e *itEnv) fakeEnv(t *testing.T, enc tofustate.Encryption, nodes, testMode string) []string {
	t.Helper()
	tfenc, err := tofustate.EncryptionEnv(enc)
	if err != nil {
		t.Fatalf("EncryptionEnv: %v", err)
	}
	extra := []string{tfenc, "TF_VAR_cluster=lab-one", "TF_VAR_nodes=" + nodes}
	if testMode != "" {
		extra = append(extra, "TF_VAR_sc_test_mode="+testMode)
	}
	return e.env(t, extra...)
}

// fakeDir copies infra/tofu/compute/fake into a fresh work dir with the rendered backend for the module key and inits it.
func (e *itEnv) fakeDir(t *testing.T, module string, env []string) string {
	t.Helper()
	dir := t.TempDir()
	copyTF(t, filepath.Join(repoRootIT(t), "infra", "tofu", "compute", "fake"), dir)
	be, err := tofustate.RenderBackendS3(e.backend(tofustate.StateKey("lab-one", module)))
	if err != nil {
		t.Fatalf("RenderBackendS3: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "_backend.tf"), []byte(be), 0o600); err != nil {
		t.Fatal(err)
	}
	out, code := e.run(t, dir, env, "init", "-input=false", "-no-color")
	mustRun(t, out, code, "tofu init of the fake compute module")
	return dir
}

func (e *itEnv) apply(t *testing.T, dir string, env []string, extra ...string) {
	t.Helper()
	args := append([]string{"apply", "-auto-approve", "-input=false", "-no-color", "-lock-timeout=60s"}, extra...)
	out, code := e.run(t, dir, env, args...)
	mustRun(t, out, code, "tofu apply")
}

func (e *itEnv) pull(t *testing.T, dir string, env []string) (string, int) {
	t.Helper()
	return e.run(t, dir, env, "state", "pull", "-no-color")
}

// CD-70(d): the fake compute module needs no provider plugin, records the nodes in an encrypted S3-backed state, and refuses
// to run outside SC_TEST_MODE=1.
func TestIntegration_FakeComputeModule_CD70d(t *testing.T) {
	e := newIT(t)
	enc := tofustate.Encryption{Current: passphrase(3), Generation: 1}

	refused := e.fakeDir(t, "compute", e.fakeEnv(t, enc, nodesOne, "1"))
	for _, mode := range []string{"", "0"} {
		out, code := e.run(t, refused, e.fakeEnv(t, enc, nodesOne, mode), "plan", "-input=false", "-no-color")
		if code == 0 {
			t.Errorf("tofu plan succeeded with SC_TEST_MODE=%q:\n%s", mode, out)
		}
		if !strings.Contains(out, "SC_TEST_MODE") {
			t.Errorf("the refusal with SC_TEST_MODE=%q does not name SC_TEST_MODE:\n%s", mode, out)
		}
	}

	env := e.fakeEnv(t, enc, nodesTwo, "1")
	e.apply(t, refused, env)
	out, code := e.run(t, refused, env, "state", "list")
	mustRun(t, out, code, "tofu state list")
	for _, want := range []string{`terraform_data.node["node-01"]`, `terraform_data.node["node-02"]`} {
		if !strings.Contains(out, want) {
			t.Errorf("state list lacks %s:\n%s", want, out)
		}
	}
	o, err := e.bucket.Get(context.Background(), tofustate.StateKey("lab-one", "compute"))
	if err != nil {
		t.Fatalf("raw state object: %v", err)
	}
	if _, err := tofustate.ParseEnvelope(o.Data); err != nil {
		t.Errorf("the compute state is not an encryption envelope: %v", err)
	}
	if bytes.Contains(o.Data, []byte("10.0.0.11")) {
		t.Error("a node address is readable in the stored state: it is not encrypted")
	}
	if entries, _ := os.ReadDir(filepath.Join(refused, ".terraform", "providers")); len(entries) != 0 {
		t.Errorf(".terraform/providers has %d entries: the module must need no provider plugin", len(entries))
	}
	if out, code := e.run(t, refused, env, "plan", "-detailed-exitcode", "-input=false", "-no-color"); code != 0 {
		t.Errorf("a second plan shows changes (exit %d):\n%s", code, out)
	}
}

func (e *itEnv) runSplit(t *testing.T, dir string, env []string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	cmd := exec.Command(e.tofu, append([]string{"-chdir=" + dir}, args...)...)
	cmd.Env = env
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	err := cmd.Run()
	if err == nil {
		return so.String(), se.String(), 0
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return so.String(), se.String(), ee.ExitCode()
	}
	t.Fatalf("running tofu %v: %v", args, err)
	return "", "", -1
}

// 06-AC-3: while process A holds the compute state lock (a real `tofu apply` blocked in a local-exec provisioner),
// `tofu plan -lock-timeout=0s` in the compute work dir exits non-zero and its stderr contains "Error acquiring the state
// lock"; the lock is readable through the library; once A is done the plan runs.
func TestIntegration_LockContentionWithRealHolder_06AC3(t *testing.T) {
	e := newIT(t)
	ctx := context.Background()
	enc := tofustate.Encryption{Current: passphrase(3), Generation: 1}
	env := e.fakeEnv(t, enc, nodesOne, "1")
	work := e.fakeDir(t, "compute", env)
	e.apply(t, work, env)

	hold := e.fakeDir(t, "compute", env)
	flags := t.TempDir()
	ready, release := filepath.Join(flags, "ready"), filepath.Join(flags, "release")
	script := "resource \"terraform_data\" \"hold\" {\n  provisioner \"local-exec\" {\n    command = \"touch '" + ready + "'; while [ ! -e '" + release + "' ]; do sleep 0.1; done\"\n  }\n}\n"
	if err := os.WriteFile(filepath.Join(hold, "hold.tf"), []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	a := exec.Command(e.tofu, "-chdir="+hold, "apply", "-auto-approve", "-input=false", "-no-color")
	a.Env = env
	var aout bytes.Buffer
	a.Stdout, a.Stderr = &aout, &aout
	if err := a.Start(); err != nil {
		t.Fatalf("starting process A: %v", err)
	}
	released := false
	t.Cleanup(func() {
		if !released {
			_ = os.WriteFile(release, nil, 0o600)
		}
		_ = a.Wait()
	})
	for n := 0; ; n++ {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if n > 1200 {
			t.Fatalf("process A never reached the provisioner:\n%s", aout.String())
		}
		if err := (clock.Real{}).Sleep(ctx, 50*time.Millisecond); err != nil {
			t.Fatal(err)
		}
	}

	st, err := tofustate.ReadLock(ctx, e.bucket, "lab-one", "compute")
	if err != nil || !st.Present || st.Info.ID == "" {
		t.Errorf("ReadLock = %+v, %v; want process A's lock", st, err)
	}
	_, stderr, code := e.runSplit(t, work, env, "plan", "-lock-timeout=0s", "-input=false", "-no-color")
	if code == 0 {
		t.Fatalf("tofu plan succeeded while process A holds the state lock")
	}
	if !strings.Contains(stderr, "Error acquiring the state lock") {
		t.Errorf("stderr lacks \"Error acquiring the state lock\":\n%s", stderr)
	}
	if !tofustate.IsLockError(stderr) || tofustate.LockIDFromStderr(stderr) != st.Info.ID {
		t.Errorf("the library does not recognize the lock error or its id (%q, want %q)", tofustate.LockIDFromStderr(stderr), st.Info.ID)
	}

	released = true
	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := a.Wait(); err != nil {
		t.Fatalf("process A failed: %v\n%s", err, aout.String())
	}
	if out, code := e.run(t, work, env, "plan", "-lock-timeout=0s", "-input=false", "-no-color"); code != 0 {
		t.Errorf("tofu plan after process A finished exited %d:\n%s", code, out)
	}
}

// 06-AC-6: rotation with the stable p<N> names. Before the refresh a state written under P1 opens with the P2-plus-fallback
// encryption but not with P2 alone; the refresh-only apply rewrites it under P2; afterwards P2 alone opens every module and
// P1 alone opens none.
func TestIntegration_RotationWithStableProviderNames_06AC6(t *testing.T) {
	e := newIT(t)
	ctx := context.Background()
	p1, p2 := passphrase(3), passphrase(11)
	enc1 := tofustate.Encryption{Current: p1, Generation: 1}
	enc2 := tofustate.Encryption{Current: p2, Generation: 2}
	rot := tofustate.Encryption{Current: p2, Generation: 2, Previous: p1}

	type mod struct {
		name string
		dir  string
	}
	var mods []mod
	for _, name := range []string{"compute", "storage"} {
		env := e.fakeEnv(t, enc1, nodesOne, "1")
		dir := e.fakeDir(t, name, env)
		e.apply(t, dir, env)
		mods = append(mods, mod{name, dir})
	}
	for _, m := range mods {
		key := tofustate.StateKey("lab-one", m.name)
		before, err := e.bucket.Get(ctx, key)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(before.Data), "key_provider.pbkdf2.p1") {
			t.Errorf("%s: the state written under generation 1 does not name the key provider p1:\n%.300s", m.name, before.Data)
		}
		if out, code := e.pull(t, m.dir, e.fakeEnv(t, enc2, nodesOne, "1")); code == 0 {
			t.Errorf("%s: state pull with only P2 succeeded before the rotation:\n%.300s", m.name, out)
		}
		if out, code := e.pull(t, m.dir, e.fakeEnv(t, rot, nodesOne, "1")); code != 0 {
			t.Errorf("%s: state pull with P2 and the fallback P1 failed (exit %d):\n%s", m.name, code, out)
		}
		versions, _ := tofustate.ListVersions(ctx, e.bucket, "lab-one", m.name, 0)

		out, code := e.run(t, m.dir, e.fakeEnv(t, rot, nodesOne, "1"), "apply", "-refresh-only", "-auto-approve", "-input=false", "-no-color", "-lock-timeout=60s")
		mustRun(t, out, code, m.name+": tofu apply -refresh-only with the rotation encryption")

		after, err := e.bucket.Get(ctx, key)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Equal(before.Data, after.Data) {
			t.Errorf("%s: the refresh-only apply did not rewrite the state object", m.name)
		}
		if got, _ := tofustate.ListVersions(ctx, e.bucket, "lab-one", m.name, 0); len(got) != len(versions)+1 {
			t.Errorf("%s: version count %d -> %d, want +1", m.name, len(versions), len(got))
		}
		if !strings.Contains(string(after.Data), "key_provider.pbkdf2.p2") || strings.Contains(string(after.Data), "key_provider.pbkdf2.p1") {
			t.Errorf("%s: the rewritten state does not name only the key provider p2:\n%.300s", m.name, after.Data)
		}
		if out, code := e.pull(t, m.dir, e.fakeEnv(t, enc2, nodesOne, "1")); code != 0 {
			t.Errorf("%s: state pull with only P2 failed after the rotation (exit %d):\n%s", m.name, code, out)
		}
		if out, code := e.pull(t, m.dir, e.fakeEnv(t, enc1, nodesOne, "1")); code == 0 {
			t.Errorf("%s: state pull with only P1 succeeded after the rotation:\n%.300s", m.name, out)
		}
	}
}

// 06-AC-8: the restore drill on the fake compute module. A garbage upload is a new version; the newest valid noncurrent version
// is restored as one more version (history +1); tofu plan then shows no changes.
func TestIntegration_RestoreDrillOnFakeCompute_06AC8(t *testing.T) {
	e := newIT(t)
	ctx := context.Background()
	enc := tofustate.Encryption{Current: passphrase(3), Generation: 1}
	env := e.fakeEnv(t, enc, nodesOne, "1")
	dir := e.fakeDir(t, "compute", env)
	e.apply(t, dir, env)
	env2 := e.fakeEnv(t, enc, nodesTwo, "1")
	e.apply(t, dir, env2)

	key := tofustate.StateKey("lab-one", "compute")
	if _, err := e.bucket.PutObject(ctx, key, []byte("GARBAGE"), s3client.PutOptions{}); err != nil {
		t.Fatalf("uploading garbage: %v", err)
	}
	if out, code := e.run(t, dir, env2, "plan", "-input=false", "-no-color"); code == 0 {
		t.Fatalf("tofu plan succeeded on a garbage state:\n%s", out)
	}
	vs, err := tofustate.ListVersions(ctx, e.bucket, "lab-one", "compute", 0)
	if err != nil || len(vs) < 3 || !vs[0].Current {
		t.Fatalf("ListVersions = %+v, %v; want at least 3 versions, the garbage current", vs, err)
	}
	validate := func(raw []byte) error { _, err := tofustate.ParseEnvelope(raw); return err }
	sel, err := tofustate.SelectRestoreVersion(vs, func(v tofustate.Version) error {
		o, err := e.bucket.GetVersion(ctx, key, v.ID)
		if err != nil {
			return err
		}
		return validate(o.Data)
	})
	if err != nil {
		t.Fatalf("SelectRestoreVersion: %v", err)
	}
	res, err := tofustate.Restore(ctx, e.bucket, "lab-one", "compute", sel.ID, validate)
	if err != nil || res.NoOp || res.NewVersionID == "" {
		t.Fatalf("Restore = %+v, %v", res, err)
	}
	after, _ := tofustate.ListVersions(ctx, e.bucket, "lab-one", "compute", 0)
	if len(after) != len(vs)+1 {
		t.Errorf("version count %d -> %d, want +1", len(vs), len(after))
	}
	out, code := e.run(t, dir, env2, "plan", "-detailed-exitcode", "-input=false", "-no-color")
	if code != 0 {
		t.Errorf("tofu plan after the restore exited %d, want 0 (no changes):\n%s", code, out)
	}
	var doc map[string]any
	pulled, code := e.pull(t, dir, env2)
	if code != 0 || json.Unmarshal([]byte(pulled), &doc) != nil || doc["resources"] == nil {
		t.Errorf("state pull after the restore: exit %d, not a plaintext tfstate:\n%.300s", code, pulled)
	}
}
