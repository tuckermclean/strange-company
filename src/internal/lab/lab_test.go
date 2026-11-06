package lab_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"strange.company/sc/internal/lab"
)

const (
	gib       = uint64(1) << 30
	debianURL = "https://example.invalid/debian-13-genericcloud-amd64.qcow2"
	ubuntuURL = "https://example.invalid/ubuntu-24.04-server-cloudimg-amd64.img"
)

var (
	debianBytes = []byte("debian image bytes")
	ubuntuBytes = []byte("ubuntu image bytes")
)

type fakeFetcher struct {
	data  map[string][]byte
	calls []string
}

func (f *fakeFetcher) Fetch(_ context.Context, url, dst string) error {
	f.calls = append(f.calls, url)
	b, ok := f.data[url]
	if !ok {
		return errors.New("unexpected url " + url)
	}
	return os.WriteFile(dst, b, 0o644)
}

type fakeLauncher struct{ vms []lab.VM }

func (l *fakeLauncher) Launch(_ context.Context, vm lab.VM) error {
	l.vms = append(l.vms, vm)
	return nil
}

func sha256hex(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }
func sha512hex(b []byte) string { s := sha512.Sum512(b); return hex.EncodeToString(s[:]) }

const labYAML = `apiVersion: strange.company/v1alpha1
kind: LabSpec
spec:
  internalNetwork: 10.77.0.0/24
  sshPortBase: 2200
  substrates:
    lab-small: {nodes: 1, vcpu: 2, memory: 4Gi, disk: 20Gi, diskDevice: virtio-blk}
    lab-ha: {nodes: 3, vcpu: 2, memory: 4Gi, disk: 20Gi, diskDevice: scsi-hd-ssd}
    lab-seed: {nodes: 1, vcpu: 4, memory: 8Gi, disk: 40Gi, diskDevice: virtio-blk}
`

type rig struct {
	env      lab.Env
	fetcher  *fakeFetcher
	launcher *fakeLauncher
	stdout   *bytes.Buffer
	stderr   *bytes.Buffer
}

// newRig builds a temp repository root with a lock whose debian-13 entry carries debianSum (sha256) and an
// injected environment with KVM present, plenty of disk and memory.
func newRig(t *testing.T, debianLock string) *rig {
	t.Helper()
	root := t.TempDir()
	lock := "apiVersion: strange.company/v1alpha1\nkind: LabImageLock\nspec:\n  images:\n" +
		"    debian-13:\n      serial: \"20250101-1\"\n      amd64:\n        url: " + debianURL + "\n" + debianLock +
		"    ubuntu-24.04:\n      serial: \"20250101\"\n      amd64:\n        url: " + ubuntuURL + "\n        sha256: " + sha256hex(ubuntuBytes) + "\n"
	for rel, content := range map[string]string{"versions/lab-images.lock.yaml": lock, "infra/lab/lab.yaml": labYAML} {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	kvm := filepath.Join(root, "kvm")
	if err := os.WriteFile(kvm, nil, 0o666); err != nil {
		t.Fatal(err)
	}
	r := &rig{
		fetcher:  &fakeFetcher{data: map[string][]byte{debianURL: debianBytes, ubuntuURL: ubuntuBytes}},
		launcher: &fakeLauncher{},
		stdout:   &bytes.Buffer{},
		stderr:   &bytes.Buffer{},
	}
	r.env = lab.Env{
		Root:       root,
		CacheDir:   filepath.Join(root, ".cache", "lab-images"),
		RunBase:    filepath.Join(root, "tmp-sc-lab"),
		RuntimeDir: filepath.Join(root, "run-sc-lab"),
		KVMPath:    kvm,
		Fetcher:    r.fetcher,
		Launcher:   r.launcher,
		WaitSSH:    func(context.Context, int) error { return nil },
		FreeBytes:  func(string) (uint64, error) { return 500 * gib, nil },
		MemBytes:   func() (uint64, error) { return 64 * gib, nil },
		Stdout:     r.stdout,
		Stderr:     r.stderr,
	}
	return r
}

func debianSHA256(sum string) string { return "        sha256: " + sum + "\n" }

func upArgs(substrate, osName string) []string {
	return []string{"up", "--substrate", substrate, "--os", osName, "--run-id", "t1"}
}

func wantCode(t *testing.T, err error, code string) {
	t.Helper()
	var le *lab.Error
	if !errors.As(err, &le) {
		t.Fatalf("want *lab.Error %s, got %v", code, err)
	}
	if le.Code != code {
		t.Fatalf("code = %q, want %q", le.Code, code)
	}
}

// 05-AC-7: a wrong checksum fails the fetched image with LAB_IMAGE_CHECKSUM_MISMATCH and starts no VM.
func TestUp_WrongChecksumFetched_NoVM_05AC7(t *testing.T) {
	r := newRig(t, debianSHA256(strings.Repeat("0", 64)))
	err := lab.Up(context.Background(), r.env, lab.UpOptions{Substrate: "lab-small", OS: "debian-13", RunID: "t1"})
	wantCode(t, err, "LAB_IMAGE_CHECKSUM_MISMATCH")
	if n := len(r.launcher.vms); n != 0 {
		t.Errorf("launcher called %d times, want 0", n)
	}
}

// 05-AC-7: the cli path exits non-zero and names the code on stderr.
func TestRun_WrongChecksum_ExitsNonZeroNoVM_05AC7(t *testing.T) {
	r := newRig(t, debianSHA256(strings.Repeat("0", 64)))
	code := lab.Run(context.Background(), upArgs("lab-small", "debian-13"), r.env)
	if code == 0 {
		t.Error("exit 0, want non-zero")
	}
	if !strings.Contains(r.stderr.String(), "LAB_IMAGE_CHECKSUM_MISMATCH") {
		t.Errorf("stderr lacks LAB_IMAGE_CHECKSUM_MISMATCH: %q", r.stderr.String())
	}
	if len(r.launcher.vms) != 0 {
		t.Errorf("launcher called %d times, want 0", len(r.launcher.vms))
	}
}

// 05-AC-7: an image already in the cache is verified too: after a good run filled the cache, a lock entry with a
// wrong checksum fails with LAB_IMAGE_CHECKSUM_MISMATCH and starts no further VM.
func TestUp_WrongChecksumCached_NoVM_05AC7(t *testing.T) {
	r := newRig(t, debianSHA256(sha256hex(debianBytes)))
	opts := lab.UpOptions{Substrate: "lab-small", OS: "debian-13", RunID: "t1"}
	if err := lab.Up(context.Background(), r.env, opts); err != nil {
		t.Fatalf("first Up: %v", err)
	}
	launched := len(r.launcher.vms)
	lockPath := filepath.Join(r.env.Root, "versions", "lab-images.lock.yaml")
	b, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	bad := strings.Replace(string(b), sha256hex(debianBytes), strings.Repeat("0", 64), 1)
	if err := os.WriteFile(lockPath, []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}
	wantCode(t, lab.Up(context.Background(), r.env, opts), "LAB_IMAGE_CHECKSUM_MISMATCH")
	if len(r.launcher.vms) != launched {
		t.Errorf("launcher called %d more times after the mismatch, want 0", len(r.launcher.vms)-launched)
	}
}

// 05-AC-7: a sha512 lock entry (Debian upstream publishes sha512) is verified as well.
func TestUp_WrongSHA512_NoVM_05AC7(t *testing.T) {
	r := newRig(t, "        sha512: "+strings.Repeat("a", 128)+"\n")
	err := lab.Up(context.Background(), r.env, lab.UpOptions{Substrate: "lab-small", OS: "debian-13", RunID: "t1"})
	wantCode(t, err, "LAB_IMAGE_CHECKSUM_MISMATCH")
	if len(r.launcher.vms) != 0 {
		t.Errorf("launcher called %d times, want 0", len(r.launcher.vms))
	}
}

// 05-AC-7 (control): right sha256 and right sha512 checksums verify and the VM is launched.
func TestUp_RightChecksum_LaunchesVM_05AC7(t *testing.T) {
	for name, lock := range map[string]string{
		"sha256": debianSHA256(sha256hex(debianBytes)),
		"sha512": "        sha512: " + sha512hex(debianBytes) + "\n",
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t, lock)
			if err := lab.Up(context.Background(), r.env, lab.UpOptions{Substrate: "lab-small", OS: "debian-13", RunID: "t1"}); err != nil {
				t.Fatalf("Up: %v", err)
			}
			if len(r.launcher.vms) != 1 {
				t.Errorf("launched %d VMs, want 1 (lab-small has node-01)", len(r.launcher.vms))
			}
		})
	}
}

// 05-AC-8: /dev/kvm missing or not openable gives LAB_NO_KVM, a non-zero exit, no launch, no TCG fallback.
func TestRun_NoKVM_FailsNoLaunch_05AC8(t *testing.T) {
	for name, mutate := range map[string]func(e *lab.Env){
		"missing":      func(e *lab.Env) { e.KVMPath = filepath.Join(e.Root, "absent-kvm") },
		"not-openable": func(e *lab.Env) { e.KVMPath = filepath.Join(e.Root, "versions", "lab-images.lock.yaml", "kvm") },
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t, debianSHA256(sha256hex(debianBytes)))
			mutate(&r.env)
			code := lab.Run(context.Background(), upArgs("lab-small", "debian-13"), r.env)
			if code == 0 {
				t.Error("exit 0, want non-zero")
			}
			if !strings.Contains(r.stderr.String(), "LAB_NO_KVM") {
				t.Errorf("stderr lacks LAB_NO_KVM: %q", r.stderr.String())
			}
			if len(r.launcher.vms) != 0 {
				t.Errorf("launcher called %d times, want 0 (no TCG fallback)", len(r.launcher.vms))
			}
			if strings.Contains(r.stdout.String(), "kvm ok") {
				t.Errorf("stdout claims kvm ok: %q", r.stdout.String())
			}
		})
	}
}

// 05-AC-8: Up returns the LAB_NO_KVM error type.
func TestUp_NoKVM_Code_05AC8(t *testing.T) {
	r := newRig(t, debianSHA256(sha256hex(debianBytes)))
	r.env.KVMPath = filepath.Join(r.env.Root, "absent-kvm")
	wantCode(t, lab.Up(context.Background(), r.env, lab.UpOptions{Substrate: "lab-small", OS: "debian-13", RunID: "t1"}), "LAB_NO_KVM")
}

// 05-AC-8: when /dev/kvm opens, the log line `sc-lab: kvm ok` is printed (the CI log of 05-AC-6 greps it).
func TestRun_KVMOK_LogsLine_05AC8(t *testing.T) {
	r := newRig(t, debianSHA256(sha256hex(debianBytes)))
	code := lab.Run(context.Background(), upArgs("lab-small", "debian-13"), r.env)
	if code != 0 {
		t.Errorf("exit %d, want 0; stderr: %s", code, r.stderr.String())
	}
	all := r.stdout.String() + r.stderr.String()
	if !strings.Contains(all, "sc-lab: kvm ok") {
		t.Errorf("output lacks %q:\n%s", "sc-lab: kvm ok", all)
	}
}

// 05-AC-8 / plan 05 14.6 lab-seed runner check: below 60 GiB free or 8 GiB memory fails before any VM starts.
func TestUp_LabSeedRunnerCheck_05AC8(t *testing.T) {
	cases := []struct {
		name string
		free uint64
		mem  uint64
		code string // "" means must not fail with a runner error
	}{
		{"disk-low", 60*gib - 1, 64 * gib, "LAB_RUNNER_DISK_LOW"},
		{"memory-low", 500 * gib, 8*gib - 1, "LAB_RUNNER_MEMORY_LOW"},
		{"exact-minimums", 60 * gib, 8 * gib, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t, debianSHA256(sha256hex(debianBytes)))
			r.env.FreeBytes = func(string) (uint64, error) { return c.free, nil }
			r.env.MemBytes = func() (uint64, error) { return c.mem, nil }
			err := lab.Up(context.Background(), r.env, lab.UpOptions{Substrate: "lab-seed", OS: "debian-13", RunID: "t1"})
			if c.code == "" {
				if err != nil {
					t.Fatalf("Up: %v", err)
				}
				if len(r.launcher.vms) != 1 {
					t.Errorf("launched %d VMs, want 1", len(r.launcher.vms))
				}
				return
			}
			wantCode(t, err, c.code)
			if len(r.launcher.vms) != 0 {
				t.Errorf("launcher called %d times, want 0", len(r.launcher.vms))
			}
		})
	}
}

// Plan 05 14.6: the runner check is for lab-seed only; lab-small does not need 60 GiB or 8 GiB.
func TestUp_LabSmallSkipsRunnerCheck_05AC8(t *testing.T) {
	r := newRig(t, debianSHA256(sha256hex(debianBytes)))
	r.env.FreeBytes = func(string) (uint64, error) { return 1 * gib, nil }
	r.env.MemBytes = func() (uint64, error) { return 4 * gib, nil }
	if err := lab.Up(context.Background(), r.env, lab.UpOptions{Substrate: "lab-small", OS: "debian-13", RunID: "t1"}); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if len(r.launcher.vms) != 1 {
		t.Errorf("launched %d VMs, want 1", len(r.launcher.vms))
	}
}

// Plan 05 14.6: lab-ha starts node-01..03.
func TestUp_LabHaLaunchesThreeVMs_05AC7(t *testing.T) {
	r := newRig(t, debianSHA256(sha256hex(debianBytes)))
	if err := lab.Up(context.Background(), r.env, lab.UpOptions{Substrate: "lab-ha", OS: "ubuntu-24.04", RunID: "t1"}); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if len(r.launcher.vms) != 3 {
		t.Errorf("launched %d VMs, want 3", len(r.launcher.vms))
	}
}
