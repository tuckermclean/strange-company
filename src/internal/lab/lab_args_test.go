package lab_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"strange.company/sc/internal/lab"
)

func fakeKeys(_ context.Context, dir string, nodes int) (lab.Keys, error) {
	k := lab.Keys{PrivateKeyPath: filepath.Join(dir, "id_ed25519"), PublicKey: "ssh-ed25519 AAAAclient SC-LAB-SSH"}
	for n := 1; n <= nodes; n++ {
		k.Hosts = append(k.Hosts, lab.HostKey{
			Private:     "-----BEGIN OPENSSH PRIVATE KEY-----\nkey" + strconv.Itoa(n) + "\n-----END OPENSSH PRIVATE KEY-----\n",
			Public:      "ssh-ed25519 AAAAhost" + strconv.Itoa(n),
			Fingerprint: "SHA256:fingerprintnode" + strconv.Itoa(n),
		})
	}
	return k, nil
}

func upOK(t *testing.T, r *rig, substrate, osName, runID string) {
	t.Helper()
	if err := lab.Up(context.Background(), r.env, lab.UpOptions{Substrate: substrate, OS: osName, RunID: runID}); err != nil {
		t.Fatalf("Up(%s,%s,%s): %v", substrate, osName, runID, err)
	}
}

func envMap(t *testing.T, r *rig, runID string) map[string]string {
	t.Helper()
	b, err := lab.EnvFile(r.env, runID)
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]string{}
	for _, l := range strings.Split(string(b), "\n") {
		if k, v, ok := strings.Cut(l, "="); ok {
			m[k] = v
		}
	}
	return m
}

func argAfter(args []string, flag, contains string) (string, bool) {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && strings.Contains(args[i+1], contains) {
			return args[i+1], true
		}
	}
	return "", false
}

// 05-AC-8: the launched VM uses KVM and never falls back to TCG.
func TestUp_VMArgsEnableKVMNoTCG_05AC8(t *testing.T) {
	r := newRig(t, debianSHA256(sha256hex(debianBytes)))
	upOK(t, r, "lab-ha", "debian-13", "t1")
	if len(r.launcher.vms) != 3 {
		t.Fatalf("launched %d VMs, want 3", len(r.launcher.vms))
	}
	for _, vm := range r.launcher.vms {
		if !slices.Contains(vm.Args, "-enable-kvm") {
			t.Errorf("%s args lack -enable-kvm: %v", vm.Name, vm.Args)
		}
		// the temp root's name may contain the test name, so only the non-path part is searched
		if strings.Contains(strings.ToLower(strings.ReplaceAll(strings.Join(vm.Args, " "), r.env.Root, "")), "tcg") {
			t.Errorf("%s args mention tcg: %v", vm.Name, vm.Args)
		}
	}
}

// 05-AC-7 (plan 05 14.6 networking): NIC0 forwards 127.0.0.1:220N to the guest SSH port.
func TestUp_NIC0HostForward_05AC7(t *testing.T) {
	r := newRig(t, debianSHA256(sha256hex(debianBytes)))
	upOK(t, r, "lab-ha", "debian-13", "t1")
	for i, vm := range r.launcher.vms {
		want := fmt.Sprintf("hostfwd=tcp:127.0.0.1:%d-:22", 2201+i)
		if _, ok := argAfter(vm.Args, "-netdev", want); !ok {
			t.Errorf("%s: no -netdev with %s in %v", vm.Name, want, vm.Args)
		}
	}
}

// 05-AC-7 (plan 05 14.6 networking): NIC1 is a socket mcast segment whose port derives from the run id.
func TestUp_NIC1MulticastPortFromRunID_05AC7(t *testing.T) {
	re := regexp.MustCompile(`socket,id=n1,mcast=230\.0\.0\.1:([0-9]+),localaddr=127\.0\.0\.1`)
	portOf := func(runID string) []int {
		r := newRig(t, debianSHA256(sha256hex(debianBytes)))
		upOK(t, r, "lab-ha", "debian-13", runID)
		var ports []int
		for _, vm := range r.launcher.vms {
			found := false
			for _, a := range vm.Args {
				if m := re.FindStringSubmatch(a); m != nil {
					p, _ := strconv.Atoi(m[1])
					ports = append(ports, p)
					found = true
				}
			}
			if !found {
				t.Fatalf("%s: no socket mcast netdev in %v", vm.Name, vm.Args)
			}
		}
		return ports
	}
	distinct := map[int]bool{}
	for _, id := range []string{"run-a", "run-b", "run-c", "run-d", "run-e"} {
		ports := portOf(id)
		for _, p := range ports {
			if p < 1024 || p > 65535 || p != ports[0] {
				t.Errorf("run %s: ports %v, want one shared port in 1024-65535", id, ports)
			}
		}
		if again := portOf(id); again[0] != ports[0] {
			t.Errorf("run %s: port not deterministic: %d then %d", id, ports[0], again[0])
		}
		distinct[ports[0]] = true
	}
	if len(distinct) < 2 {
		t.Errorf("5 run ids share the port set %v, want it derived from the id", distinct)
	}
}

// 05-AC-7 (plan 05 14.6 shapes): lab-ha disks are scsi-hd with rotation_rate=1, lab-small is virtio-blk.
func TestUp_DiskDevices_05AC7(t *testing.T) {
	r := newRig(t, debianSHA256(sha256hex(debianBytes)))
	upOK(t, r, "lab-ha", "debian-13", "t1")
	for _, vm := range r.launcher.vms {
		if _, ok := argAfter(vm.Args, "-device", "scsi-hd"); !ok {
			t.Errorf("%s: no scsi-hd device in %v", vm.Name, vm.Args)
		}
		if _, ok := argAfter(vm.Args, "-device", "rotation_rate=1"); !ok {
			t.Errorf("%s: no rotation_rate=1 in %v", vm.Name, vm.Args)
		}
	}
	r2 := newRig(t, debianSHA256(sha256hex(debianBytes)))
	upOK(t, r2, "lab-small", "debian-13", "t1")
	args := r2.launcher.vms[0].Args
	if _, ok := argAfter(args, "-device", "virtio-blk-pci"); !ok {
		t.Errorf("lab-small: no virtio-blk-pci in %v", args)
	}
	if _, ok := argAfter(args, "-device", "scsi-hd"); ok {
		t.Errorf("lab-small must not use scsi-hd: %v", args)
	}
}

// 05-AC-7 (plan 05 14.6 generated bootstrap.env): lab-small on Debian.
func TestUp_BootstrapEnvLabSmallDebian_05AC7(t *testing.T) {
	r := newRig(t, debianSHA256(sha256hex(debianBytes)))
	r.env.GenKeys = fakeKeys
	upOK(t, r, "lab-small", "debian-13", "t1")
	m := envMap(t, r, "t1")
	want := map[string]string{
		"EXISTING_NODES":          "node-01=debian@127.0.0.1:2201/10.77.0.11",
		"EXISTING_NODE_OVERRIDES": "node-01:disk_class=ssd;node-01:host_key=SHA256:fingerprintnode1",
		"PROVIDER":                "existing",
		"MAX_MONTHLY_COST":        "0",
		"PROFILE":                 "lab-small",
		"CLUSTER_NAME":            "lab-t1",
	}
	for k, v := range want {
		if m[k] != v {
			t.Errorf("%s = %q, want %q", k, m[k], v)
		}
	}
}

// 05-AC-7: Ubuntu images use the ubuntu user; lab-ha (scsi-hd) attests no disk class, only host keys.
func TestUp_BootstrapEnvLabHaUbuntu_05AC7(t *testing.T) {
	r := newRig(t, debianSHA256(sha256hex(debianBytes)))
	r.env.GenKeys = fakeKeys
	upOK(t, r, "lab-ha", "ubuntu-24.04", "t1")
	m := envMap(t, r, "t1")
	wantNodes := "node-01=ubuntu@127.0.0.1:2201/10.77.0.11,node-02=ubuntu@127.0.0.1:2202/10.77.0.12,node-03=ubuntu@127.0.0.1:2203/10.77.0.13"
	if m["EXISTING_NODES"] != wantNodes {
		t.Errorf("EXISTING_NODES = %q, want %q", m["EXISTING_NODES"], wantNodes)
	}
	if strings.Contains(m["EXISTING_NODE_OVERRIDES"], "disk_class") {
		t.Errorf("lab-ha must not attest disk_class: %q", m["EXISTING_NODE_OVERRIDES"])
	}
	if !strings.Contains(m["EXISTING_NODE_OVERRIDES"], "node-03:host_key=SHA256:fingerprintnode3") {
		t.Errorf("overrides lack node-03 host key: %q", m["EXISTING_NODE_OVERRIDES"])
	}
	if m["PROFILE"] != "lab-ha" || m["EXPECTED_NODE_COUNT"] != "3" {
		t.Errorf("PROFILE=%q EXPECTED_NODE_COUNT=%q", m["PROFILE"], m["EXPECTED_NODE_COUNT"])
	}
}

// 05-AC-7: Down is idempotent and a run that never existed is not an error.
func TestDown_Idempotent_05AC7(t *testing.T) {
	r := newRig(t, debianSHA256(sha256hex(debianBytes)))
	if err := lab.Down(r.env, "never-ran"); err != nil {
		t.Fatalf("Down of an unknown run: %v", err)
	}
	upOK(t, r, "lab-small", "debian-13", "t1")
	for i := 0; i < 2; i++ {
		if err := lab.Down(r.env, "t1"); err != nil {
			t.Fatalf("Down #%d: %v", i+1, err)
		}
	}
	for _, d := range []string{filepath.Join(r.env.RunBase, "t1"), filepath.Join(r.env.RuntimeDir, "t1")} {
		if _, err := os.Stat(d); !os.IsNotExist(err) {
			t.Errorf("%s still exists after Down (err %v)", d, err)
		}
	}
}
