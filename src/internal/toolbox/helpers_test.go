package toolbox_test

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/binary"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"strange.company/sc/internal/toolbox"
)

type result struct {
	code        int
	stdout, err string
}

func run(t *testing.T, env map[string]string, args ...string) result {
	t.Helper()
	var o, e bytes.Buffer
	if env == nil {
		env = map[string]string{}
	}
	code := toolbox.Main(args, env, &o, &e)
	return result{code, o.String(), e.String()}
}

func (r result) all() string { return r.stdout + r.err }

func write(t *testing.T, path, content string, mode os.FileMode) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	return path
}

// lockWithTool renders a minimal ToolboxLock whose one tool reports the given version through versionCommand.
func lockWithTool(version string) string {
	return `apiVersion: strange.company/v1alpha1
kind: ToolboxLock
metadata:
  name: fixture
spec:
  tools:
    - name: faketool
      set: runtime
      version: "` + version + `"
      url: http://127.0.0.1:1/unused
      sha256:
        amd64: "` + strings.Repeat("0", 64) + `"
        arm64: "` + strings.Repeat("0", 64) + `"
      install: {to: /usr/local/bin/faketool}
      versionCommand: [sh, -c, "echo v1.2.3"]
      versionRegex: '^v{version}$'
`
}

// --- OpenSSH key fixtures written by hand so the tests need no ssh-keygen and no third-party module.

func sshString(b []byte) []byte {
	out := binary.BigEndian.AppendUint32(nil, uint32(len(b)))
	return append(out, b...)
}

func sshMpint(n *big.Int) []byte {
	b := n.Bytes()
	if len(b) > 0 && b[0]&0x80 != 0 {
		b = append([]byte{0}, b...)
	}
	return sshString(b)
}

func openSSHPEM(pubBlob, priv []byte) string {
	var body []byte
	body = append(body, "openssh-key-v1\x00"...)
	body = append(body, sshString([]byte("none"))...)
	body = append(body, sshString([]byte("none"))...)
	body = append(body, sshString(nil)...)
	body = binary.BigEndian.AppendUint32(body, 1)
	body = append(body, sshString(pubBlob)...)
	for i := byte(1); len(priv)%8 != 0; i++ {
		priv = append(priv, i)
	}
	body = append(body, sshString(priv)...)
	b64 := base64.StdEncoding.EncodeToString(body)
	var sb strings.Builder
	sb.WriteString("-----BEGIN OPENSSH PRIVATE KEY-----\n")
	for len(b64) > 70 {
		sb.WriteString(b64[:70] + "\n")
		b64 = b64[70:]
	}
	sb.WriteString(b64 + "\n-----END OPENSSH PRIVATE KEY-----\n")
	return sb.String()
}

// writeKey writes dir/<name> and dir/<name>.pub and returns the private path.
func writeEd25519(t *testing.T, dir, name string) string {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	blob := append(sshString([]byte("ssh-ed25519")), sshString(pub)...)
	var p []byte
	p = append(p, 1, 2, 3, 4, 1, 2, 3, 4)
	p = append(p, sshString([]byte("ssh-ed25519"))...)
	p = append(p, sshString(pub)...)
	p = append(p, sshString(priv)...)
	p = append(p, sshString([]byte("fixture"))...)
	path := write(t, filepath.Join(dir, name), openSSHPEM(blob, p), 0o600)
	write(t, path+".pub", "ssh-ed25519 "+base64.StdEncoding.EncodeToString(blob)+" fixture\n", 0o644)
	return path
}

func writeRSA(t *testing.T, dir, name string, bits int) string {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, bits)
	if err != nil {
		t.Fatal(err)
	}
	k.Precompute()
	e := big.NewInt(int64(k.E))
	blob := append(sshString([]byte("ssh-rsa")), sshMpint(e)...)
	blob = append(blob, sshMpint(k.N)...)
	var p []byte
	p = append(p, 1, 2, 3, 4, 1, 2, 3, 4)
	p = append(p, sshString([]byte("ssh-rsa"))...)
	p = append(p, sshMpint(k.N)...)
	p = append(p, sshMpint(e)...)
	p = append(p, sshMpint(k.D)...)
	p = append(p, sshMpint(k.Precomputed.Qinv)...)
	p = append(p, sshMpint(k.Primes[0])...)
	p = append(p, sshMpint(k.Primes[1])...)
	p = append(p, sshString([]byte("fixture"))...)
	path := write(t, filepath.Join(dir, name), openSSHPEM(blob, p), 0o600)
	write(t, path+".pub", "ssh-rsa "+base64.StdEncoding.EncodeToString(blob)+" fixture\n", 0o644)
	return path
}

// The hand-written fixtures must be real OpenSSH keys, or the PF-06 tests would test nothing.
func TestFixtureKeysAreValidOpenSSH(t *testing.T) {
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("ssh-keygen not available")
	}
	dir := t.TempDir()
	for _, k := range []string{writeEd25519(t, dir, "e"), writeRSA(t, dir, "r", 2048)} {
		out, err := exec.Command("ssh-keygen", "-y", "-f", k).CombinedOutput()
		if err != nil {
			t.Fatalf("ssh-keygen -y %s: %v\n%s", k, err, out)
		}
		pub, _ := os.ReadFile(k + ".pub")
		if !strings.HasPrefix(string(pub), strings.Join(strings.Fields(string(out))[:2], " ")) {
			t.Fatalf("derived public key differs from fixture .pub for %s", k)
		}
	}
}
