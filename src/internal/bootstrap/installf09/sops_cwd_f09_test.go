package installf09_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"

	"strange.company/sc/internal/bootstrap/install"
)

// 05-AC-6 (lab smoke through provision): install.SopsEncrypt is given its recipients explicitly, so its result must not
// depend on a .sops.yaml in the working directory or its parents. sc-bootstrap writes the repository .sops.yaml (cluster
// creation rules only) just before rendering; sops 3.13 still loads it from the working directory and fails with "no
// matching creation rules found" when no rule matches /dev/stdin. Serial: it changes the process working directory.
func TestSopsEncrypt_IgnoresWorkingDirSopsYaml_F09_05AC6(t *testing.T) {
	sops, err := exec.LookPath("sops")
	if err != nil {
		t.Skip("sops is not on PATH (it is in the ci image)")
	}
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	other, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	rules := "creation_rules:\n  - path_regex: gitops/clusters/x/.*\\.sops\\.yaml$\n    age: " + other.Recipient().String() + "\n"
	if err := os.WriteFile(filepath.Join(dir, ".sops.yaml"), []byte(rules), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	decrypt := func(ct []byte) string {
		cmd := exec.Command(sops, "--decrypt", "--input-type", "yaml", "--output-type", "yaml", "/dev/stdin")
		cmd.Dir = t.TempDir()
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
		t.Fatalf("SopsEncrypt(secret) with a non-matching .sops.yaml in the working directory: %v", err)
	}
	if d := decrypt(ct); !strings.Contains(d, "hunter2-plain-value") {
		t.Errorf("the secret does not decrypt with the age identity:\n%s", d)
	}
	whole, err := install.SopsEncrypt([]byte("SC-TOFU-ENCRYPTION: p1-secret-passphrase\n"), []string{id.Recipient().String()})
	if err != nil {
		t.Fatalf("SopsEncrypt(map) with a non-matching .sops.yaml in the working directory: %v", err)
	}
	if !strings.Contains(decrypt(whole), "p1-secret-passphrase") {
		t.Errorf("the off-cluster map does not decrypt with the age identity:\n%s", whole)
	}
}
