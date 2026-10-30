package install_test

import (
	"bytes"
	"os/exec"
	"strings"
	"testing"

	"filippo.io/age"

	"strange.company/sc/internal/bootstrap/install"
)

// S8b3: the production Encrypt (install.SopsEncrypt) runs the real sops with the recipients given explicitly (the ci image
// has sops 3.13.3 and age 1.3.2). The output decrypts with the age identity; a Kubernetes Secret keeps its metadata readable
// and only data/stringData are encrypted; an off-cluster map is encrypted whole. Skipped where sops is not installed
// (the host); `make test` runs in the ci image.
func TestSopsEncrypt_RealSopsAndAge_S8b3(t *testing.T) {
	t.Parallel()
	sops, err := exec.LookPath("sops")
	if err != nil {
		t.Skip("sops is not on PATH (it is in the ci image)")
	}
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	decrypt := func(ct []byte) string {
		cmd := exec.Command(sops, "--decrypt", "--input-type", "yaml", "--output-type", "yaml", "/dev/stdin")
		cmd.Env = []string{"SOPS_AGE_KEY=" + id.String(), "PATH=/usr/bin:/bin"}
		cmd.Stdin = bytes.NewReader(ct)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("sops --decrypt: %v\n%.300s", err, out)
		}
		return string(out)
	}
	secret := []byte("apiVersion: v1\nkind: Secret\nmetadata:\n  name: sc-demo\n  namespace: flux-system\nstringData:\n  token: hunter2-plain-value\n")
	ct, err := install.SopsEncrypt(secret, []string{id.Recipient().String()})
	if err != nil {
		t.Fatalf("SopsEncrypt(secret): %v", err)
	}
	c := string(ct)
	if strings.Contains(c, "hunter2-plain-value") || !strings.Contains(c, "ENC[") {
		t.Errorf("the secret value is not encrypted:\n%s", c)
	}
	if !strings.Contains(c, "name: sc-demo") || !strings.Contains(c, "kind: Secret") {
		t.Errorf("the metadata of a Kubernetes Secret must stay readable (encrypted_regex ^(data|stringData)$):\n%s", c)
	}
	if !strings.Contains(c, "encrypted_regex") || !strings.Contains(c, id.Recipient().String()) {
		t.Errorf("the sops block lacks the encrypted_regex or the recipient:\n%s", c)
	}
	if d := decrypt(ct); !strings.Contains(d, "hunter2-plain-value") {
		t.Errorf("the output does not decrypt with the age identity:\n%s", d)
	}
	whole, err := install.SopsEncrypt([]byte("SC-TOFU-ENCRYPTION: p1-secret-passphrase\n"), []string{id.Recipient().String()})
	if err != nil {
		t.Fatalf("SopsEncrypt(map): %v", err)
	}
	if bytes.Contains(whole, []byte("p1-secret-passphrase")) || !strings.Contains(decrypt(whole), "p1-secret-passphrase") {
		t.Errorf("an off-cluster map must be encrypted whole and decrypt with the identity:\n%s", whole)
	}
}
