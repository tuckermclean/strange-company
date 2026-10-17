package toolbox_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// F03 S3 carry-overs from the S1 review (plan 02 sections 14.3, 14.4; plan 50 section 9.2).

// S1-N1: with no SSH_PRIVATE_KEY_PATH the PF-06 line names the default as the operator writes it, ~/.ssh/id_ed25519,
// not the expanded container home (HOME=/run/sc/home inside the toolbox).
func TestPF06_DefaultKeyIsShownUnexpanded_Plan50S9_2_S1N1(t *testing.T) {
	f := newPreflight(t, false)
	home := filepath.Join(f.dir, "home")
	writeEd25519(t, filepath.Join(home, ".ssh"), "id_ed25519")
	envf := write(t, filepath.Join(f.dir, "h.env"), "CLUSTER_NAME=demo\n", 0o600)
	r := run(t, pfEnv(f, map[string]string{"HOME": home}), "--env-file", envf, "preflight")
	if r.code != 0 {
		t.Fatalf("exit %d, want 0: %s", r.code, r.all())
	}
	if !strings.Contains(r.all(), "~/.ssh/id_ed25519") {
		t.Fatalf("PF-06 output must show ~/.ssh/id_ed25519: %q", r.all())
	}
	if strings.Contains(r.all(), home) {
		t.Fatalf("PF-06 output must not show the expanded home %s: %q", home, r.all())
	}
}

// S1-N1, failure side: the message of a missing default key shows the unexpanded default too.
func TestPF06_MissingDefaultKeyIsShownUnexpanded_Plan50S9_2_S1N1(t *testing.T) {
	f := newPreflight(t, false)
	home := filepath.Join(f.dir, "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	envf := write(t, filepath.Join(f.dir, "h.env"), "CLUSTER_NAME=demo\n", 0o600)
	r := run(t, pfEnv(f, map[string]string{"HOME": home}), "--env-file", envf, "preflight")
	if r.code != 3 || !strings.Contains(r.all(), "PF-06") {
		t.Fatalf("exit %d, want 3 with PF-06: %s", r.code, r.all())
	}
	if !strings.Contains(r.all(), "~/.ssh/id_ed25519") || strings.Contains(r.all(), home) {
		t.Fatalf("PF-06 failure must show ~/.ssh/id_ed25519, not the expanded home: %q", r.all())
	}
}

// S1-N1, mounted side: when the wrapper mounted the key and the env file names no path, PF-06 says so.
func TestPF06_MountedKeyWithoutEnvPathIsShownAsMounted_Plan02S14_3_S1N1(t *testing.T) {
	f := newPreflight(t, false)
	in := mountedKeys(t, f)
	envf := write(t, filepath.Join(f.dir, "m.env"), "CLUSTER_NAME=demo\n", 0o600)
	r := run(t, pfEnv(f, map[string]string{"SC_INPUT_DIR": in}), "--env-file", envf, "preflight")
	if r.code != 0 {
		t.Fatalf("exit %d, want 0 (mounted key is valid): %s", r.code, r.all())
	}
	want := filepath.Join(in, "ssh", "id_key") + " (mounted)"
	if !strings.Contains(r.all(), want) {
		t.Fatalf("PF-06 output must show %q: %q", want, r.all())
	}
	if strings.Contains(r.all(), "emptyhome") {
		t.Fatalf("PF-06 output must not show the expanded container home: %q", r.all())
	}
}

// S1-N2: the mounted key is checked before the HOME-empty branch: HOME unset, no path in the env file, key mounted.
func TestPF06_MountedKeyWorksWithHomeUnset_Plan02S14_3_S1N2(t *testing.T) {
	f := newPreflight(t, false)
	in := mountedKeys(t, f)
	envf := write(t, filepath.Join(f.dir, "m.env"), "CLUSTER_NAME=demo\n", 0o600)
	r := run(t, pfEnv(f, map[string]string{"SC_INPUT_DIR": in, "HOME": ""}), "--env-file", envf, "preflight")
	if r.code != 0 || strings.Contains(r.all(), "SSH_KEY_UNSET") {
		t.Fatalf("exit %d, want 0: a mounted key needs no HOME: %s", r.code, r.all())
	}
}
