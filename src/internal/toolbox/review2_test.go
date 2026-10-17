package toolbox_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"strange.company/sc/internal/toolbox"
)

// Review round 2 of F03 S1 (head 1c158b6).

// --- 1. plan 02 sections 7.2/14.5: the container paths are /run/sc-input and /run/sc, not variables.
// Seam: toolbox.ProductionEnv is what cmd/sc-bootstrap/main.go must pass to toolbox.Run; toolbox.Main keeps
// its current behavior (no defaults added), so existing tests do not depend on the host's /run.

func TestProductionEnv_DefaultsContainerDirs_Plan02S7_2(t *testing.T) {
	got := toolbox.ProductionEnv(map[string]string{"HOME": "/h"})
	if got["SC_INPUT_DIR"] != "/run/sc-input" || got["SC_RUN_DIR"] != "/run/sc" || got["HOME"] != "/h" {
		t.Fatalf("ProductionEnv = %v, want SC_INPUT_DIR=/run/sc-input SC_RUN_DIR=/run/sc and HOME kept", got)
	}
}

func TestProductionEnv_KeepsExplicitDirs_Plan02S7_2(t *testing.T) {
	got := toolbox.ProductionEnv(map[string]string{"SC_INPUT_DIR": "/x/in", "SC_RUN_DIR": "/x/run"})
	if got["SC_INPUT_DIR"] != "/x/in" || got["SC_RUN_DIR"] != "/x/run" {
		t.Fatalf("explicit values must win: %v", got)
	}
}

func TestMainBinary_UsesProductionEnv_Plan02S7_2(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "cmd", "sc-bootstrap", "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), "toolbox.ProductionEnv(") {
		t.Fatal("cmd/sc-bootstrap/main.go must pass its environment through toolbox.ProductionEnv")
	}
}

func TestMain_DoesNotAddContainerDefaults_Plan02S7_2(t *testing.T) {
	// Main with SC_INPUT_DIR unset must ignore a mounted key at the default dir: a nonexistent host key fails.
	f := newPreflight(t, false)
	envf := write(t, filepath.Join(f.dir, "n.env"), "SSH_PRIVATE_KEY_PATH=/nonexistent/key\n", 0o600)
	r := run(t, pfEnv(f, nil), "--env-file", envf, "preflight")
	if r.code != 3 {
		t.Fatalf("exit %d: %s", r.code, r.all())
	}
}

// --- 2. plan 50 section 9.2: SSH_PRIVATE_KEY_PATH defaults to ~/.ssh/id_ed25519.

func TestPF06_DefaultsToHomeSSHKey_Plan50S9_2(t *testing.T) {
	f := newPreflight(t, false)
	home := filepath.Join(f.dir, "home")
	writeEd25519(t, filepath.Join(home, ".ssh"), "id_ed25519")
	envf := write(t, filepath.Join(f.dir, "h.env"), "CLUSTER_NAME=demo\n", 0o600)
	r := run(t, pfEnv(f, map[string]string{"HOME": home}), "--env-file", envf, "preflight")
	if r.code != 0 || strings.Contains(r.all(), "SSH_KEY_UNSET") {
		t.Fatalf("exit %d, want 0 with the default ~/.ssh/id_ed25519: %s", r.code, r.all())
	}
}

// --- 3. plan 50 section 14.2: the agent socket must be usable, a stale socket file is not.

func TestPF06_StaleAgentSocketFails_Plan50S14_2(t *testing.T) {
	f := newPreflight(t, false)
	in, err := os.MkdirTemp("", "scin")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(in) })
	l, err := net.Listen("unix", filepath.Join(in, "ssh-agent.sock"))
	if err != nil {
		t.Skipf("cannot create a unix socket: %v", err)
	}
	l.(*net.UnixListener).SetUnlinkOnClose(false)
	l.Close() // the socket file stays, nothing listens
	envf := write(t, filepath.Join(f.dir, "s.env"), "SSH_USE_AGENT=true\n", 0o600)
	r := run(t, pfEnv(f, map[string]string{"SC_INPUT_DIR": in}), "--env-file", envf, "preflight")
	if r.code != 3 || !strings.Contains(r.all(), "SSH_AGENT_UNAVAILABLE") {
		t.Fatalf("exit %d, want 3 SSH_AGENT_UNAVAILABLE for a stale socket: %s", r.code, r.all())
	}
}

// --- 4. plan 02 section 16: fetch waits 5s then 15s; no env knob changes that.

func TestFetch_RetryWaitsAre5sThen15s_Plan02S16(t *testing.T) {
	body := []byte("payload")
	sum := sha256.Sum256(body)
	s := serve(t, map[string][]byte{"/t": body}, map[string]int{"/t": 2})
	dir := t.TempDir()
	lock := write(t, filepath.Join(dir, "lock.yaml"), fetchLock(s.srv.URL, tool{"flaky", "runtime", "/t", hex.EncodeToString(sum[:]), ""}), 0o644)
	var waits []time.Duration
	var o, e bytes.Buffer
	code := toolbox.Run([]string{"toolbox", "fetch", "--lock", lock, "--arch", "amd64", "--dest", filepath.Join(dir, "dest")},
		fastRetry(), &o, &e, func(d time.Duration) { waits = append(waits, d) })
	if code != 0 {
		t.Fatalf("exit %d: %s%s", code, o.String(), e.String())
	}
	if len(waits) != 2 || waits[0] != 5*time.Second || waits[1] != 15*time.Second {
		t.Fatalf("waits = %v, want [5s 15s] regardless of SC_TOOLBOX_RETRY_DELAY", waits)
	}
}
