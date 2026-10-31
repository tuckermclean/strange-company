package tofustate_test

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"strange.company/sc/internal/tofustate"
)

func golden(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// fixture passphrases: 43 characters of base64url, as plan 50 generates (32 random bytes), built at runtime.
func passphrase(seed byte) string {
	b := make([]byte, 32)
	for i := range b {
		b[i] = seed + byte(i*7)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// 06-AC-4 (render): the s3 backend matches the section 7.3 block, byte for byte (golden).
func TestRenderBackendS3_Golden_06AC4(t *testing.T) {
	got, err := tofustate.RenderBackendS3(tofustate.Backend{
		Bucket: "lab-one-tofu-state", Key: "lab-one/compute/terraform.tfstate", Region: "us-east-1",
		Endpoint: "http://127.0.0.1:18333", PathStyle: true, SkipS3Checksum: true,
	})
	if err != nil {
		t.Fatalf("RenderBackendS3: %v", err)
	}
	if want := golden(t, "backend-s3.tf.golden"); got != want {
		t.Errorf("rendered backend differs from testdata/backend-s3.tf.golden:\n--- got\n%s--- want\n%s", got, want)
	}
	got, err = tofustate.RenderBackendS3(tofustate.Backend{
		Bucket: "lab-one-tofu-state", Key: "lab-one/storage/terraform.tfstate", Region: "gra",
		Endpoint: "https://s3.gra.io.cloud.ovh.net",
	})
	if err != nil {
		t.Fatalf("RenderBackendS3 (virtual-hosted): %v", err)
	}
	if want := golden(t, "backend-s3-virtual.tf.golden"); got != want {
		t.Errorf("rendered backend differs from testdata/backend-s3-virtual.tf.golden:\n--- got\n%s--- want\n%s", got, want)
	}
}

func TestRenderBackendLocal_Golden_06AC4(t *testing.T) {
	got, err := tofustate.RenderBackendLocal(".bootstrap/work/storage.local.tfstate")
	if err != nil {
		t.Fatalf("RenderBackendLocal: %v", err)
	}
	if want := golden(t, "backend-local.tf.golden"); got != want {
		t.Errorf("rendered local backend differs from the golden:\n--- got\n%s--- want\n%s", got, want)
	}
}

// The rendered values go into HCL strings: anything that could break out of them is refused.
func TestRenderBackend_RejectsUnsafeValues_06AC4(t *testing.T) {
	ok := tofustate.Backend{Bucket: "b-tofu-state", Key: "c/compute/terraform.tfstate", Region: "r", Endpoint: "http://127.0.0.1:1"}
	if _, err := tofustate.RenderBackendS3(ok); err != nil {
		t.Fatalf("a valid backend is refused: %v", err)
	}
	mut := map[string]func(b *tofustate.Backend){
		"quote in bucket":    func(b *tofustate.Backend) { b.Bucket = `x" { evil = 1 } "` },
		"newline in key":     func(b *tofustate.Backend) { b.Key = "a\nb" },
		"interpolation":      func(b *tofustate.Backend) { b.Key = "${file(\"/etc/passwd\")}" },
		"template directive": func(b *tofustate.Backend) { b.Region = "%{ if true }x%{ endif }" },
		"empty bucket":       func(b *tofustate.Backend) { b.Bucket = "" },
		"empty key":          func(b *tofustate.Backend) { b.Key = "" },
		"empty endpoint":     func(b *tofustate.Backend) { b.Endpoint = "" },
		"non-http endpoint":  func(b *tofustate.Backend) { b.Endpoint = "file:///etc" },
		"backslash":          func(b *tofustate.Backend) { b.Endpoint = `http://h\"x` },
	}
	for name, f := range mut {
		t.Run(name, func(t *testing.T) {
			b := ok
			f(&b)
			if out, err := tofustate.RenderBackendS3(b); err == nil {
				t.Errorf("RenderBackendS3 accepted it and produced:\n%s", out)
			}
		})
	}
	if out, err := tofustate.RenderBackendLocal("a\"b"); err == nil {
		t.Errorf("RenderBackendLocal accepted a quote in the path:\n%s", out)
	}
}

// 06-AC-4 (render): state and plan encryption are enforced, the passphrase sits only in the env value.
func TestRenderEncryption_Golden_06AC4(t *testing.T) {
	p1 := passphrase(3)
	got, err := tofustate.RenderEncryption(tofustate.Encryption{Current: p1})
	if err != nil {
		t.Fatalf("RenderEncryption: %v", err)
	}
	want := strings.ReplaceAll(golden(t, "tf-encryption.golden"), "{{CURRENT}}", p1)
	if got != want {
		t.Errorf("rendered encryption differs from testdata/tf-encryption.golden:\n--- got\n%s--- want\n%s", got, want)
	}
	for _, block := range []string{"state {", "plan {"} {
		i := strings.Index(got, block)
		if i < 0 || !strings.Contains(got[i:], "enforced = true") {
			t.Errorf("%s is missing or not enforced", block)
		}
	}
}

func TestEncryptionEnv_06AC4(t *testing.T) {
	p1 := passphrase(3)
	env, err := tofustate.EncryptionEnv(tofustate.Encryption{Current: p1})
	if err != nil {
		t.Fatalf("EncryptionEnv: %v", err)
	}
	hcl, _ := tofustate.RenderEncryption(tofustate.Encryption{Current: p1})
	if hcl == "" || env != "TF_ENCRYPTION="+hcl {
		t.Errorf("EncryptionEnv = %q, want TF_ENCRYPTION= followed by the rendered HCL", env)
	}
}

func TestRenderEncryption_RejectsWeakOrUnsafePassphrases_06AC4(t *testing.T) {
	good := passphrase(3)
	cases := map[string]tofustate.Encryption{
		"empty":              {},
		"short":              {Current: "tooshort"},
		"15 characters":      {Current: strings.Repeat("a", 15)},
		"quote":              {Current: strings.Repeat("a", 20) + `"`},
		"newline":            {Current: strings.Repeat("a", 20) + "\n"},
		"interpolation":      {Current: strings.Repeat("a", 20) + "${x}"},
		"template directive": {Current: strings.Repeat("a", 20) + "%{x}"},
		"backslash":          {Current: strings.Repeat("a", 20) + `\`},
	}
	for name, e := range cases {
		t.Run(name, func(t *testing.T) {
			if out, err := tofustate.RenderEncryption(e); err == nil {
				t.Errorf("RenderEncryption accepted it:\n%s", out)
			}
			if _, err := tofustate.EncryptionEnv(e); err == nil {
				t.Error("EncryptionEnv accepted it")
			}
		})
	}
	if _, err := tofustate.RenderEncryption(tofustate.Encryption{Current: good}); err != nil {
		t.Errorf("a 43-character base64url passphrase is refused: %v", err)
	}
}

// 06-R / section 7.3: the backend files never carry the passphrase.
func TestBackendRenderHasNoSecretMaterial_06AC4(t *testing.T) {
	out, err := tofustate.RenderBackendS3(tofustate.Backend{Bucket: "b-tofu-state", Key: "c/compute/terraform.tfstate", Region: "r", Endpoint: "http://127.0.0.1:1"})
	if err != nil || out == "" {
		t.Fatalf("RenderBackendS3 = %q, %v", out, err)
	}
	for _, bad := range []string{"passphrase", "encryption", "access_key", "secret_key", "TF_ENCRYPTION"} {
		if strings.Contains(out, bad) {
			t.Errorf("the rendered backend contains %q", bad)
		}
	}
}
