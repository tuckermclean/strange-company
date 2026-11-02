package existing_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"strange.company/sc/internal/clock"
	"strange.company/sc/internal/provider"
	"strange.company/sc/internal/provider/discovery"
	"strange.company/sc/internal/provider/existing"
)

// S7 review findings on the existing-nodes provider: the plan digest (plan 05 sections 8, 14.1 step 4 and 17), SSH host
// safety and the SSH retry count (sections 9, 14.2 and 16). Criteria 05-AC-2 (discovered facts) and 05-AC-4 (digest).

const (
	fixtureDir = "../../../../test/fixtures/existing/discovery/debian13-baremetal-nvme"
	profile    = "../../../../infra/profiles/self-hosting-minimum.yaml"
	uidA       = "6f1c2d3e-4b5a-4c6d-8e7f-0a1b2c3d4e5f"
	uidB       = "0a0b0c0d-1111-4222-8333-444455556666"
)

// factsTransport serves one node's facts, edited by the test, and the fixture's host keys.
type factsTransport struct {
	keys  []string
	facts map[string]any
}

func (f factsTransport) HostKeys(context.Context, discovery.Node) ([]string, error) {
	return f.keys, nil
}
func (f factsTransport) Facts(context.Context, discovery.Node, []string) ([]byte, error) {
	return json.Marshal(f.facts)
}

func fixture(t *testing.T) (discovery.Node, factsTransport) {
	t.Helper()
	var in struct {
		Node discovery.Node `json:"node"`
	}
	b, err := os.ReadFile(filepath.Join(fixtureDir, "input.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &in); err != nil {
		t.Fatal(err)
	}
	hk, err := os.ReadFile(filepath.Join(fixtureDir, "hostkeys.txt"))
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, l := range strings.Split(string(hk), "\n") {
		if strings.TrimSpace(l) != "" {
			keys = append(keys, l)
		}
	}
	fb, err := os.ReadFile(filepath.Join(fixtureDir, "facts.json"))
	if err != nil {
		t.Fatal(err)
	}
	var facts map[string]any
	if err := json.Unmarshal(fb, &facts); err != nil {
		t.Fatal(err)
	}
	return in.Node, factsTransport{keys: keys, facts: facts}
}

// digest plans the fixture node with its facts edited by mutate and returns the plan digest. uid is the installation's
// uid, which the node's install marker may equal.
func digest(t *testing.T, uid string, mutate func(facts map[string]any)) string {
	t.Helper()
	node, tr := fixture(t)
	if mutate != nil {
		mutate(tr.facts)
	}
	p := existing.New(existing.Config{Nodes: []discovery.Node{node}, ProfileFile: profile, InstallUID: func() string { return uid }, Transport: tr})
	plan, err := p.Plan(context.Background(), provider.Desired{Cluster: "lab-one", NodeCount: 1})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.Digest == "" {
		t.Fatal("empty plan digest")
	}
	return plan.Digest
}

// 05-AC-4 (plan 05 section 14.1 step 4: "a different machine answering on that address" is PLAN_DIGEST_MISMATCH): the digest
// changes with the machine id, the install marker, the platforms found and the kernel release (plan 05 section 17 lists
// only free space, swap and time-sync status as volatile).
func TestPlanDigest_ChangesWithIdentityFacts_05AC4(t *testing.T) {
	base := digest(t, uidA, nil)
	if again := digest(t, uidA, nil); again != base {
		t.Fatalf("the digest is not stable over unchanged nodes: %s vs %s", base, again)
	}
	cases := []struct {
		name   string
		uid    string
		mutate func(f map[string]any)
	}{
		{"machine_id", uidA, func(f map[string]any) { f["machine_id"] = "ffffffffffffffffffffffffffffffff" }},
		{"install_uid", uidB, func(f map[string]any) { f["install_uid"] = uidB }},
		{"install_uid-marker-appears", uidA, func(f map[string]any) { f["install_uid"] = uidA }},
		{"platforms", uidA, func(f map[string]any) { f["platforms"] = []any{"containerd"} }},
		{"kernel", uidA, func(f map[string]any) { f["kernel"] = "6.12.74+deb13-amd64" }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := digest(t, c.uid, c.mutate); got == base {
				t.Errorf("the digest did not change with %s (a different machine could answer on the same address)", c.name)
			}
		})
	}
}

// 05-AC-4 (plan 05 section 17): facts that legitimately change between runs (free space, swap, time-sync status) are not
// in the digest.
func TestPlanDigest_IgnoresVolatileFacts_05AC4(t *testing.T) {
	base := digest(t, uidA, nil)
	cases := []struct {
		name   string
		mutate func(f map[string]any)
	}{
		{"free-space", func(f map[string]any) {
			f["findmnt"].(map[string]any)["filesystems"].([]any)[0].(map[string]any)["avail"] = float64(700000000000)
		}},
		{"swap", func(f map[string]any) { f["swap"] = "2147483648\n" }},
		{"time-sync", func(f map[string]any) { f["ntp_synchronized"] = "no" }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := digest(t, uidA, c.mutate); got != base {
				t.Errorf("the digest changed with %s, a volatile fact (plan 05 section 17)", c.name)
			}
		})
	}
}

// fakeSSH puts fake ssh and ssh-keyscan commands first on PATH. Each appends one line with its arguments to log and fails
// with a connection error, so a test sees how often and with what arguments the transport called them.
func fakeSSH(t *testing.T) (log string) {
	t.Helper()
	dir := t.TempDir()
	log = filepath.Join(dir, "calls.log")
	body := "#!/bin/sh\necho \"$0 $*\" >> " + log + "\necho 'ssh: connect to host: Connection refused' >&2\nexit 255\n"
	for _, name := range []string{"ssh", "ssh-keyscan"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

func calls(t *testing.T, log string) []string {
	t.Helper()
	b, err := os.ReadFile(log)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

func transport(t *testing.T) existing.SSHTransport {
	return existing.SSHTransport{TempDir: t.TempDir(), Clock: clock.NewFake(time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC))}
}

// 05-AC-3 (safety; plan 05 section 9 host format): a host that starts with "-" would be read by ssh and ssh-keyscan as an
// option (-oProxyCommand=... runs a command). The transport refuses it before any ssh or ssh-keyscan invocation.
func TestSSHTransport_RefusesHostStartingWithDash_05AC3(t *testing.T) {
	for _, host := range []string{"-oProxyCommand=touch-pwned", "-v", "--"} {
		t.Run(host, func(t *testing.T) {
			log := fakeSSH(t)
			n := discovery.Node{Name: "node-01", Host: host, Port: 22, User: "debian"}
			if _, err := transport(t).HostKeys(context.Background(), n); err == nil {
				t.Errorf("HostKeys accepted the host %q", host)
			}
			if _, err := transport(t).Facts(context.Background(), n, []string{"h ssh-ed25519 AAAA"}); err == nil {
				t.Errorf("Facts accepted the host %q", host)
			}
			if c := calls(t, log); len(c) != 0 {
				t.Errorf("ssh or ssh-keyscan was invoked for the host %q: %v", host, c)
			}
		})
	}
}

// 05-AC-3 (plan 05 section 16, "3 attempts"): a connection failure is tried at most three times, and more than once
// (it is transient right after a reboot). The delay gaps are the fake clock's: nothing waits.
func TestSSHTransport_AtMostThreeAttempts_05AC3(t *testing.T) {
	n := discovery.Node{Name: "node-01", Host: "203.0.113.10", Port: 22, User: "debian"}
	t.Run("ssh-keyscan", func(t *testing.T) {
		log := fakeSSH(t)
		if _, err := transport(t).HostKeys(context.Background(), n); err == nil {
			t.Fatal("HostKeys succeeded against a failing ssh-keyscan")
		}
		if got := len(calls(t, log)); got < 2 || got > 3 {
			t.Errorf("ssh-keyscan was run %d times, want 2 or 3 (plan 05 section 16: 3 attempts)", got)
		}
	})
	t.Run("ssh", func(t *testing.T) {
		log := fakeSSH(t)
		if _, err := transport(t).Facts(context.Background(), n, []string{"203.0.113.10 ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl"}); err == nil {
			t.Fatal("Facts succeeded against a failing ssh")
		}
		if got := len(calls(t, log)); got < 2 || got > 3 {
			t.Errorf("ssh was run %d times, want 2 or 3 (plan 05 section 16: 3 attempts)", got)
		}
	})
}
