package toolbox_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/binary"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Review round 1 of F03 S1 (GREEN 2438eac). Each test cites the plan section it encodes.

// --- 1. plan 02 section 14.9: fetch keeps fetch-pinned's {version}/{arch} expansion of install.extract.
// (fetch-pinned expands only install.extract, url and the sha256 arch key; install.to is not expanded.)

func quotedExtractLock(base, urlPath, sha, extract string) string {
	return `apiVersion: strange.company/v1alpha1
kind: ToolboxLock
metadata:
  name: fixture
spec:
  tools:
    - name: mytool
      set: runtime
      version: "1.0.0"
      url: ` + base + urlPath + `
      sha256:
        amd64: "` + sha + `"
        arm64: "` + sha + `"
      install: {extract: "` + extract + `", to: /usr/local/bin/mytool}
      versionCommand: [mytool, --version]
      versionRegex: '^{version}$'
`
}

func TestFetch_ExpandsVersionAndArchInExtractMember_Plan02S14_9(t *testing.T) {
	for _, arch := range []string{"amd64", "arm64"} {
		t.Run(arch, func(t *testing.T) {
			arc := tarGz(t, "pkg-1.0.0-linux-"+arch+"/mytool", "#!/bin/sh\necho 1.0.0\n")
			s := serve(t, map[string][]byte{"/a.tgz": arc}, nil)
			dir := t.TempDir()
			lock := write(t, filepath.Join(dir, "lock.yaml"),
				quotedExtractLock(s.srv.URL, "/a.tgz", sum(arc), "pkg-{version}-linux-{arch}/mytool"), 0o644)
			dest := filepath.Join(dir, "dest")
			r := run(t, fastRetry(), "toolbox", "fetch", "--lock", lock, "--arch", arch, "--dest", dest)
			if r.code != 0 || !hasInstalled(t, dest, "mytool") {
				t.Fatalf("exit %d, installed %v, out %q", r.code, installed(t, dest), r.all())
			}
		})
	}
}

// --- 2/3/5/6/7: PF-06 (plan 50 section 9.2 and 14.2; plan 02 sections 14.2 step 3, 14.3, 14.4).

func pfEnv(f preflightFixture, extra map[string]string) map[string]string {
	e := map[string]string{"HOME": filepath.Join(f.dir, "emptyhome")}
	for k, v := range f.env {
		e[k] = v
	}
	for k, v := range extra {
		e[k] = v
	}
	return e
}

// Plan 50 section 9.2: SSH_PUBLIC_KEY_PATH defaults to <private>.pub, so PF-06 ("equal to SSH_PUBLIC_KEY_PATH")
// compares against it when unset.
func TestPF06_DefaultPubKeyMismatchFails_Plan50S9_2(t *testing.T) {
	f := newPreflight(t, false)
	a := writeEd25519(t, filepath.Join(f.dir, "a"), "id")
	b := writeEd25519(t, filepath.Join(f.dir, "b"), "id")
	data, _ := os.ReadFile(b + ".pub")
	write(t, a+".pub", string(data), 0o644) // default .pub of a holds b's public key
	envf := write(t, filepath.Join(f.dir, "d.env"), "SSH_PRIVATE_KEY_PATH="+a+"\n", 0o600)
	r := run(t, pfEnv(f, nil), "--env-file", envf, "preflight")
	if r.code != 3 || !strings.Contains(r.all(), "PF-06") {
		t.Fatalf("exit %d, output %q", r.code, r.all())
	}
}

// Same section: an absent default .pub cannot be "equal", so PF-06 fails rather than passing silently.
func TestPF06_AbsentDefaultPubKeyFails_Plan50S14_2(t *testing.T) {
	f := newPreflight(t, false)
	a := writeEd25519(t, filepath.Join(f.dir, "a"), "id")
	if err := os.Remove(a + ".pub"); err != nil {
		t.Fatal(err)
	}
	envf := write(t, filepath.Join(f.dir, "d.env"), "SSH_PRIVATE_KEY_PATH="+a+"\n", 0o600)
	r := run(t, pfEnv(f, nil), "--env-file", envf, "preflight")
	if r.code != 3 || !strings.Contains(r.all(), "PF-06") {
		t.Fatalf("exit %d, output %q", r.code, r.all())
	}
}

// Plan 02 section 14.3/14.4: inside the container the key is the mount $SC_INPUT_DIR/ssh/id_key(.pub); the
// env-file host path does not exist there. Output still names the literal env-file path.
func mountedKeys(t *testing.T, f preflightFixture) string {
	t.Helper()
	in := filepath.Join(f.dir, "input")
	k := writeEd25519(t, filepath.Join(f.dir, "mk"), "k")
	priv, _ := os.ReadFile(k)
	pub, _ := os.ReadFile(k + ".pub")
	write(t, filepath.Join(in, "ssh", "id_key"), string(priv), 0o600)
	write(t, filepath.Join(in, "ssh", "id_key.pub"), string(pub), 0o644)
	return in
}

func TestPF06_UsesMountedKeyInsideContainer_Plan02S14_4(t *testing.T) {
	f := newPreflight(t, false)
	in := mountedKeys(t, f)
	hostPath := "/home/operator-on-host/.ssh/id_ed25519"
	envf := write(t, filepath.Join(f.dir, "c.env"), "SSH_PRIVATE_KEY_PATH="+hostPath+"\nSSH_PUBLIC_KEY_PATH="+hostPath+".pub\n", 0o600)
	r := run(t, pfEnv(f, map[string]string{"SC_INPUT_DIR": in}), "--env-file", envf, "preflight")
	if r.code != 0 {
		t.Fatalf("exit %d, want 0 (mounted key is valid): %s", r.code, r.all())
	}
	if !strings.Contains(r.all(), hostPath) {
		t.Fatalf("output must name the env-file path %s: %q", hostPath, r.all())
	}
}

func TestPF06_MountedPubKeyMismatchFails_Plan02S14_4(t *testing.T) {
	f := newPreflight(t, false)
	in := mountedKeys(t, f)
	other := writeEd25519(t, filepath.Join(f.dir, "other"), "o")
	pub, _ := os.ReadFile(other + ".pub")
	write(t, filepath.Join(in, "ssh", "id_key.pub"), string(pub), 0o644)
	envf := write(t, filepath.Join(f.dir, "c.env"), "SSH_PRIVATE_KEY_PATH=/nonexistent/host/key\n", 0o600)
	r := run(t, pfEnv(f, map[string]string{"SC_INPUT_DIR": in}), "--env-file", envf, "preflight")
	if r.code != 3 || !strings.Contains(r.all(), "PF-06") {
		t.Fatalf("exit %d, output %q", r.code, r.all())
	}
}

// 02-AC-11 still holds with an input dir that has no mounted key: literal path shown, PF-06 fails, X not created.
func TestPF06_NoMountedKeyKeepsLiteralPathSemantics_02AC11(t *testing.T) {
	f := newPreflight(t, false)
	in := filepath.Join(f.dir, "emptyinput")
	if err := os.MkdirAll(in, 0o755); err != nil {
		t.Fatal(err)
	}
	x := filepath.Join(f.dir, "X")
	envf := write(t, filepath.Join(f.dir, "e.env"), "SSH_PRIVATE_KEY_PATH=$(touch "+x+")\n", 0o600)
	r := run(t, pfEnv(f, map[string]string{"SC_INPUT_DIR": in}), "--env-file", envf, "preflight")
	if r.code != 3 || !strings.Contains(r.all(), "PF-06") || !strings.Contains(r.all(), "$(touch") {
		t.Fatalf("exit %d, output %q", r.code, r.all())
	}
	if _, err := os.Stat(x); err == nil {
		t.Fatalf("%s was created: the env file value was evaluated", x)
	}
}

// --- 4. plan 02 section 14.9: selfcheck keeps fetch-pinned's TOOLBOX_VERSION_MISMATCH:<tool> on stderr.
func TestSelfcheck_PrintsVersionMismatchCodePerBadRow_Plan02S14_9(t *testing.T) {
	dir := t.TempDir()
	emb := write(t, filepath.Join(dir, "emb.yaml"), lockWithTool("9.9.9"), 0o644)
	for _, args := range [][]string{{"toolbox", "selfcheck", "--embedded-only"}, {"toolbox", "selfcheck", "--json", "--embedded-only"}} {
		r := run(t, map[string]string{"SC_EMBEDDED_LOCK": emb}, args...)
		if r.code == 0 || !strings.Contains(r.err, "TOOLBOX_VERSION_MISMATCH:faketool") {
			t.Fatalf("%v: exit %d, stderr %q", args, r.code, r.err)
		}
	}
}

func TestSelfcheck_NoVersionMismatchCodeWhenOK_Plan02S14_9(t *testing.T) {
	dir := t.TempDir()
	emb := write(t, filepath.Join(dir, "emb.yaml"), lockWithTool("1.2.3"), 0o644)
	r := run(t, map[string]string{"SC_EMBEDDED_LOCK": emb}, "toolbox", "selfcheck", "--embedded-only")
	if r.code != 0 || strings.Contains(r.all(), "TOOLBOX_VERSION_MISMATCH") {
		t.Fatalf("exit %d, output %q", r.code, r.all())
	}
}

// --- 5. passphrase-protected key + ssh-agent (plan 50 section 9.2 "yes unless agent", 14.2 PF-06).

// writeEncryptedKey writes an openssh-key-v1 file with cipher aes256-ctr / kdf bcrypt. The private section is
// opaque bytes: only the unencrypted public blob can be read without the passphrase.
func writeEncryptedKey(t *testing.T, dir, name string, blob []byte, typ string) string {
	t.Helper()
	var body []byte
	body = append(body, "openssh-key-v1\x00"...)
	body = append(body, sshString([]byte("aes256-ctr"))...)
	body = append(body, sshString([]byte("bcrypt"))...)
	opts := append(sshString(make([]byte, 16)), 0, 0, 0, 16)
	body = append(body, sshString(opts)...)
	body = binary.BigEndian.AppendUint32(body, 1)
	body = append(body, sshString(blob)...)
	opaque := make([]byte, 128)
	_, _ = rand.Read(opaque)
	body = append(body, sshString(opaque)...)
	b64 := base64.StdEncoding.EncodeToString(body)
	var sb strings.Builder
	sb.WriteString("-----BEGIN OPENSSH PRIVATE KEY-----\n")
	for len(b64) > 70 {
		sb.WriteString(b64[:70] + "\n")
		b64 = b64[70:]
	}
	sb.WriteString(b64 + "\n-----END OPENSSH PRIVATE KEY-----\n")
	path := write(t, filepath.Join(dir, name), sb.String(), 0o600)
	write(t, path+".pub", typ+" "+base64.StdEncoding.EncodeToString(blob)+" fixture\n", 0o644)
	return path
}

func edBlob(t *testing.T) []byte {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return append(sshString([]byte("ssh-ed25519")), sshString(pub)...)
}

func agentSocket(t *testing.T) string {
	t.Helper()
	in, err := os.MkdirTemp("", "scin")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(in) })
	l, err := net.Listen("unix", filepath.Join(in, "ssh-agent.sock"))
	if err != nil {
		t.Skipf("cannot create a unix socket: %v", err)
	}
	t.Cleanup(func() { l.Close() })
	return in
}

func TestPF06_AgentAcceptsPassphraseProtectedKey_Plan50S14_2(t *testing.T) {
	f := newPreflight(t, false)
	in := agentSocket(t)
	key := writeEncryptedKey(t, filepath.Join(f.dir, "enc"), "id", edBlob(t), "ssh-ed25519")
	envf := write(t, filepath.Join(f.dir, "a.env"), "SSH_PRIVATE_KEY_PATH="+key+"\nSSH_USE_AGENT=true\n", 0o600)
	r := run(t, pfEnv(f, map[string]string{"SC_INPUT_DIR": in}), "--env-file", envf, "preflight")
	if r.code != 0 {
		t.Fatalf("exit %d, want 0 (type, size and public key come from the unencrypted blob): %s", r.code, r.all())
	}
}

func TestPF06_EncryptedWeakRSAStillFails_Plan50S14_2(t *testing.T) {
	f := newPreflight(t, false)
	in := agentSocket(t)
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	blob := append(sshString([]byte("ssh-rsa")), sshMpint(big.NewInt(int64(k.E)))...)
	blob = append(blob, sshMpint(k.N)...)
	key := writeEncryptedKey(t, filepath.Join(f.dir, "encrsa"), "id", blob, "ssh-rsa")
	envf := write(t, filepath.Join(f.dir, "a.env"), "SSH_PRIVATE_KEY_PATH="+key+"\nSSH_USE_AGENT=true\n", 0o600)
	r := run(t, pfEnv(f, map[string]string{"SC_INPUT_DIR": in}), "--env-file", envf, "preflight")
	if r.code != 3 || !strings.Contains(r.all(), "PF-06") {
		t.Fatalf("exit %d, output %q", r.code, r.all())
	}
}

func TestPF06_AgentWithoutKeyPathNeedsOnlyTheSocket_Plan50S9_2(t *testing.T) {
	f := newPreflight(t, false)
	in := agentSocket(t)
	envf := write(t, filepath.Join(f.dir, "a.env"), "SSH_USE_AGENT=true\n", 0o600)
	r := run(t, pfEnv(f, map[string]string{"SC_INPUT_DIR": in}), "--env-file", envf, "preflight")
	if r.code != 0 || strings.Contains(r.all(), "SSH_KEY_UNSET") {
		t.Fatalf("exit %d, output %q", r.code, r.all())
	}
}

func TestPF06_AgentSocketAbsentFails_Plan50S14_2(t *testing.T) {
	f := newPreflight(t, false)
	in := filepath.Join(f.dir, "noagent")
	if err := os.MkdirAll(in, 0o755); err != nil {
		t.Fatal(err)
	}
	envf := write(t, filepath.Join(f.dir, "a.env"), "SSH_USE_AGENT=true\n", 0o600)
	r := run(t, pfEnv(f, map[string]string{"SC_INPUT_DIR": in}), "--env-file", envf, "preflight")
	if r.code != 3 || !strings.Contains(r.all(), "PF-06") {
		t.Fatalf("exit %d, output %q", r.code, r.all())
	}
}

func TestPF06_AgentPathThatIsNotASocketFails_Plan50S14_2(t *testing.T) {
	f := newPreflight(t, false)
	in := filepath.Join(f.dir, "fakeagent")
	write(t, filepath.Join(in, "ssh-agent.sock"), "not a socket", 0o600)
	envf := write(t, filepath.Join(f.dir, "a.env"), "SSH_USE_AGENT=true\n", 0o600)
	r := run(t, pfEnv(f, map[string]string{"SC_INPUT_DIR": in}), "--env-file", envf, "preflight")
	if r.code != 3 || !strings.Contains(r.all(), "PF-06") {
		t.Fatalf("exit %d, output %q", r.code, r.all())
	}
}

// --- 6. plan 50 section 14.2 PF-06 "public key derivable": derived from the seed, not read from the key file.
func TestPF06_Ed25519PublicHalfNotMatchingSeedIsRejected_Plan50S14_2(t *testing.T) {
	f := newPreflight(t, false)
	_, privA, _ := ed25519.GenerateKey(rand.Reader)
	pubB, _, _ := ed25519.GenerateKey(rand.Reader)
	forged := append(append([]byte{}, privA.Seed()...), pubB...) // seed of A, public half of B
	blob := append(sshString([]byte("ssh-ed25519")), sshString(pubB)...)
	var p []byte
	p = append(p, 1, 2, 3, 4, 1, 2, 3, 4)
	p = append(p, sshString([]byte("ssh-ed25519"))...)
	p = append(p, sshString(pubB)...)
	p = append(p, sshString(forged)...)
	p = append(p, sshString([]byte("fixture"))...)
	key := write(t, filepath.Join(f.dir, "forged", "id"), openSSHPEM(blob, p), 0o600)
	write(t, key+".pub", "ssh-ed25519 "+base64.StdEncoding.EncodeToString(blob)+" fixture\n", 0o644)
	envf := write(t, filepath.Join(f.dir, "f.env"), "SSH_PRIVATE_KEY_PATH="+key+"\nSSH_PUBLIC_KEY_PATH="+key+".pub\n", 0o600)
	r := run(t, pfEnv(f, nil), "--env-file", envf, "preflight")
	if r.code != 3 || !strings.Contains(r.all(), "PF-06") {
		t.Fatalf("exit %d, output %q", r.code, r.all())
	}
}

// --- 7. env-file grammar (plan 02 section 14.2 step 3: ^[A-Z][A-Z0-9_]*= ; plan 50 section 9.1).
// Lines outside the grammar assign nothing, so PF-06 must not see the key path.
func TestEnvFile_LinesOutsideGrammarAssignNothing_Plan02S14_2(t *testing.T) {
	for name, line := range map[string]string{
		"spaces around =": "SSH_PRIVATE_KEY_PATH = %s",
		"lowercase key":   "ssh_private_key_path=%s",
	} {
		t.Run(name, func(t *testing.T) {
			f := newPreflight(t, false)
			key := writeEd25519(t, filepath.Join(f.dir, "g"), "id")
			envf := write(t, filepath.Join(f.dir, "g.env"), strings.Replace(line, "%s", key, 1)+"\n", 0o600)
			r := run(t, pfEnv(f, nil), "--env-file", envf, "preflight")
			if r.code == 0 || strings.Contains(r.all(), "ssh-ed25519") {
				t.Fatalf("the out-of-grammar line was used: exit %d, output %q", r.code, r.all())
			}
		})
	}
}

// Plan 50 section 7.2: an unreadable/invalid config is exit 2.
func TestEnvS3_UnreadableEnvFileExits2_Plan50S7(t *testing.T) {
	dir := t.TempDir()
	r := run(t, map[string]string{"SC_RUN_DIR": filepath.Join(dir, "run")}, "--env-file", filepath.Join(dir, "absent.env"), "env", "s3")
	if r.code != 2 {
		t.Fatalf("exit %d, want 2: %s", r.code, r.all())
	}
}
