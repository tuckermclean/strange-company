package lab_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"strange.company/sc/internal/clock"
	"strange.company/sc/internal/lab"
)

// clusterName is CLUSTER_NAME's pattern in src/internal/bootstrap/config (reClusterName).
var clusterName = regexp.MustCompile(`^[a-z][a-z0-9-]{1,30}[a-z0-9]$`)

// 05-AC-7: the run id becomes CLUSTER_NAME=lab-<id>, so ids that cannot make a valid name are usage errors
// before anything starts (no run directory, no fetch, no VM).
func TestUp_RunIDMustMakeValidClusterName_05AC7(t *testing.T) {
	invalid := map[string]string{
		"too-long-29": strings.Repeat("a", 29),
		"too-long-40": strings.Repeat("a", 40),
		"lead-dash":   "-abc",
		"trail-dash":  "abc-",
		"only-dash":   "-",
		"upper":       "Abc",
		"underscore":  "a_b",
		"empty":       "",
	}
	for name, id := range invalid {
		t.Run(name, func(t *testing.T) {
			r := newRig(t, debianSHA256(sha256hex(debianBytes)))
			err := lab.Up(context.Background(), r.env, lab.UpOptions{Substrate: "lab-small", OS: "debian-13", RunID: id})
			wantCode(t, err, "LAB_USAGE")
			if len(r.fetcher.calls) != 0 || len(r.launcher.vms) != 0 {
				t.Errorf("fetched %d, launched %d, want nothing started", len(r.fetcher.calls), len(r.launcher.vms))
			}
			if _, err := os.Stat(r.env.RunBase); !os.IsNotExist(err) {
				t.Errorf("run base %s exists (err %v), want nothing created", r.env.RunBase, err)
			}
			if code := lab.Run(context.Background(), []string{"up", "--substrate", "lab-small", "--os", "debian-13", "--run-id", id}, r.env); code != 2 {
				t.Errorf("Run exit %d, want 2", code)
			}
			wantCode(t, lab.Down(r.env, id), "LAB_USAGE")
			_, err = lab.EnvFile(r.env, id)
			wantCode(t, err, "LAB_USAGE")
		})
	}
}

// 05-AC-7: every accepted run id yields a CLUSTER_NAME the config accepts.
func TestUp_ValidRunIDsMakeValidClusterName_05AC7(t *testing.T) {
	for _, id := range []string{"a", "t1", "ab-cd", "1a", strings.Repeat("b", 20)} {
		r := newRig(t, debianSHA256(sha256hex(debianBytes)))
		upOK(t, r, "lab-small", "debian-13", id)
		if got := envMap(t, r, id)["CLUSTER_NAME"]; got != "lab-"+id || !clusterName.MatchString(got) {
			t.Errorf("run id %q: CLUSTER_NAME %q, want lab-%s matching %s", id, got, id, clusterName)
		}
	}
}

// 05-AC-7: a lock entry with neither sha256 nor sha512 cannot verify anything, so it is an error and no VM starts.
func TestUp_LockEntryWithoutChecksum_NoVM_05AC7(t *testing.T) {
	r := newRig(t, "")
	err := lab.Up(context.Background(), r.env, lab.UpOptions{Substrate: "lab-small", OS: "debian-13", RunID: "t1"})
	var le *lab.Error
	if !errors.As(err, &le) || (le.Code != "LAB_IMAGE_CHECKSUM_MISMATCH" && le.Code != "LAB_CONFIG") {
		t.Fatalf("want *lab.Error LAB_IMAGE_CHECKSUM_MISMATCH or LAB_CONFIG, got %v", err)
	}
	if len(r.launcher.vms) != 0 {
		t.Errorf("launcher called %d times, want 0", len(r.launcher.vms))
	}
}

// 05-AC-7: the image cache is keyed by more than the URL basename, so a lock bump that keeps the file name
// (new serial and checksum) fetches the new image instead of failing against the stale cached file.
func TestUp_LockBumpSameBasename_Refetches_05AC7(t *testing.T) {
	r := newRig(t, debianSHA256(sha256hex(debianBytes)))
	opts := lab.UpOptions{Substrate: "lab-small", OS: "debian-13", RunID: "t1"}
	var bases []string
	r.env.MakeOverlay = func(_ context.Context, base, _ string, _ int) error {
		bases = append(bases, base)
		return nil
	}
	if err := lab.Up(context.Background(), r.env, opts); err != nil {
		t.Fatalf("first Up: %v", err)
	}
	newBytes := []byte("debian image bytes, serial 20250201-1")
	r.fetcher.data[debianURL] = newBytes
	lockPath := filepath.Join(r.env.Root, "versions", "lab-images.lock.yaml")
	b, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	bumped := strings.Replace(strings.Replace(string(b), sha256hex(debianBytes), sha256hex(newBytes), 1), "20250101-1", "20250201-1", 1)
	if err := os.WriteFile(lockPath, []byte(bumped), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := lab.Up(context.Background(), r.env, opts); err != nil {
		t.Fatalf("Up after the lock bump: %v", err)
	}
	if got := len(r.fetcher.calls); got != 2 {
		t.Errorf("fetched %d times, want 2 (the bump must re-fetch)", got)
	}
	if len(bases) != 2 {
		t.Fatalf("overlay made %d times, want 2", len(bases))
	}
	if got, err := os.ReadFile(bases[1]); err != nil || string(got) != string(newBytes) {
		t.Errorf("second base image %s holds %q (err %v), want the bumped image", bases[1], got, err)
	}
	if bases[0] == bases[1] {
		t.Errorf("both runs used the cache path %s", bases[0])
	}
}

// 05-AC-7 (plan 05 14.6, <= 180 s per VM): each VM's SSH wait gets its own context with one deadline of at most
// 180 s, rather than a poll count.
func TestUp_WaitSSHDeadlinePerVM_05AC7(t *testing.T) {
	r := newRig(t, debianSHA256(sha256hex(debianBytes)))
	clk := clock.Real{}
	var remaining []float64
	r.env.WaitSSH = func(ctx context.Context, _ int) error {
		d, ok := ctx.Deadline()
		if !ok {
			remaining = append(remaining, -1)
			return nil
		}
		remaining = append(remaining, d.Sub(clk.Now()).Seconds())
		return nil
	}
	upOK(t, r, "lab-ha", "debian-13", "t1")
	if len(remaining) != 3 {
		t.Fatalf("WaitSSH called %d times, want 3", len(remaining))
	}
	for i, s := range remaining {
		if s < 170 || s > 180 {
			t.Errorf("VM %d: SSH wait deadline in %.1f s, want one deadline of 180 s (170-180 remaining)", i+1, s)
		}
	}
}

// 05-AC-7: the run directory holds keys-adjacent material (seed user-data, host keys), so it is 0700.
func TestUp_RunDirMode0700_05AC7(t *testing.T) {
	r := newRig(t, debianSHA256(sha256hex(debianBytes)))
	upOK(t, r, "lab-small", "debian-13", "t1")
	st, err := os.Stat(filepath.Join(r.env.RunBase, "t1"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := st.Mode().Perm(); perm != 0o700 {
		t.Errorf("run dir mode %o, want 700", perm)
	}
}

// 05-AC-7 (plan 05 14.6 `down`): a stale pid file must not make Down kill an unrelated process; only pids whose
// command is qemu or weed are killed, and a vanished pid is skipped without an error.
func TestDown_KillsOnlyQemuAndWeed_05AC7(t *testing.T) {
	r := newRig(t, debianSHA256(sha256hex(debianBytes)))
	runDir := filepath.Join(r.env.RunBase, "t1")
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, pid := range map[string]string{"node-01": "4242", "node-02": "4343", "node-03": "4545", "s3": "4444"} {
		if err := os.WriteFile(filepath.Join(runDir, name+".pid"), []byte(pid+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	r.env.ProcName = func(pid int) (string, error) {
		switch pid {
		case 4242:
			return "qemu-system-x86", nil // /proc/<pid>/comm is cut to 15 characters
		case 4343:
			return "sshd", nil // pid reused by an unrelated process
		case 4444:
			return "weed", nil
		}
		return "", errors.New("no such process")
	}
	var killed []int
	r.env.Kill = func(pid int) error { killed = append(killed, pid); return nil }
	if err := lab.Down(r.env, "t1"); err != nil {
		t.Fatalf("Down: %v", err)
	}
	sort.Ints(killed)
	if want := []int{4242, 4444}; !slices.Equal(killed, want) {
		t.Errorf("killed pids %v, want %v (only qemu and weed)", killed, want)
	}
	if _, err := os.Stat(runDir); !os.IsNotExist(err) {
		t.Errorf("%s still exists after Down (err %v)", runDir, err)
	}
}

// 05-AC-7 (plan 05 14.6 Lab S3): the s3-emulator listens on 127.0.0.1 only; VMs reach it through QEMU's host alias.
func TestS3EmulatorArgs_BindLoopbackOnly_05AC7(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "s3")
	args := lab.S3EmulatorArgs(dir)
	if !slices.Contains(args, "-s3") {
		t.Errorf("args lack -s3: %v", args)
	}
	if !slices.Contains(args, "-ip=127.0.0.1") && !slices.Contains(args, "-ip.bind=127.0.0.1") {
		t.Errorf("args bind neither -ip=127.0.0.1 nor -ip.bind=127.0.0.1: %v", args)
	}
	for _, a := range args {
		if strings.Contains(a, "0.0.0.0") || (strings.HasPrefix(a, "-ip") && !strings.HasSuffix(a, "=127.0.0.1")) {
			t.Errorf("argument %q binds beyond 127.0.0.1: %v", a, args)
		}
	}
	if !slices.Contains(args, "-dir="+dir) {
		t.Errorf("args lack -dir=%s: %v", dir, args)
	}
}
