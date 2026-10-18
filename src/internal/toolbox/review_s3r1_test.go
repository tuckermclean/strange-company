package toolbox_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// F03 S3 review round 1: env-file parser parity with the wrapper (plan 02 section 14.2 step 3: one level of
// matching quotes; plan 50 section 9.1: an empty value is unset, the last assignment wins; a CRLF line ending loses
// only its trailing CR) and PF-06 path display (S1-N1 remainder). Observed through `env s3` and `preflight`.

func s3Run(t *testing.T, envText string) (result, string) {
	t.Helper()
	dir := t.TempDir()
	envf := write(t, filepath.Join(dir, "b.env"), envText, 0o600)
	runDir := filepath.Join(dir, "run")
	r := run(t, map[string]string{"SC_RUN_DIR": runDir}, "--env-file", envf, "env", "s3")
	return r, runDir
}

func readAws(t *testing.T, runDir, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(runDir, "aws", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

const s3Base = "S3_ACCESS_KEY_ID=AKID\nS3_SECRET_ACCESS_KEY=SECRET\n"

// 02-AC-11 / plan 50 section 9.1: KEY=/first then a later empty KEY= leaves the key unset (the wrapper agrees).
func TestEnvFile_LaterEmptyAssignmentUnsets_Plan50S9_1_02AC11(t *testing.T) {
	r, _ := s3Run(t, "S3_ENDPOINT=http://first.invalid\nS3_ENDPOINT=\n"+s3Base)
	if r.code != 2 || !strings.Contains(r.err, "S3_ENDPOINT") {
		t.Fatalf("exit %d, want 2 with S3_ENDPOINT missing (later empty assignment unsets): %q", r.code, r.all())
	}
}

func TestEnvFile_LaterEmptyAssignmentUnsetsOptional_Plan50S9_1_02AC11(t *testing.T) {
	r, d := s3Run(t, "S3_ENDPOINT=http://e.invalid\nS3_REGION=eu-west-1\nS3_REGION=\n"+s3Base)
	if r.code != 0 {
		t.Fatalf("exit %d: %q", r.code, r.all())
	}
	if cfg := readAws(t, d, "config"); strings.Contains(cfg, "region") {
		t.Fatalf("S3_REGION was unset by the later empty assignment, config has a region: %q", cfg)
	}
}

// Plan 02 section 14.2 step 3: one level of matching quotes only.
func TestEnvFile_StripsOneQuoteLevelOnly_Plan02S14_2(t *testing.T) {
	r, d := s3Run(t, "S3_ENDPOINT=\"'http://x.invalid'\"\n"+s3Base)
	if r.code != 0 {
		t.Fatalf("exit %d: %q", r.code, r.all())
	}
	if cfg := readAws(t, d, "config"); !strings.Contains(cfg, "endpoint_url = 'http://x.invalid'\n") {
		t.Fatalf("want the inner quotes kept: %q", cfg)
	}
}

// Only a trailing CR (CRLF line ending) is stripped; an embedded CR stays in the value.
func TestEnvFile_KeepsEmbeddedCRStripsTrailingCR_Plan02S14_2(t *testing.T) {
	r, d := s3Run(t, "S3_ENDPOINT=http://x.invalid\r\nS3_ACCESS_KEY_ID=AK\rID\r\nS3_SECRET_ACCESS_KEY=SECRET\r\n")
	if r.code != 0 {
		t.Fatalf("exit %d: %q", r.code, r.all())
	}
	if cfg := readAws(t, d, "config"); !strings.Contains(cfg, "endpoint_url = http://x.invalid\n") {
		t.Fatalf("trailing CR must be stripped: %q", cfg)
	}
	if cred := readAws(t, d, "credentials"); !strings.Contains(cred, "aws_access_key_id = AK\rID\n") {
		t.Fatalf("embedded CR must be kept: %q", cred)
	}
}

// S1-N1 remainder: an explicit ~/ path is shown as written, never the expanded container home.
func TestPF06_ExplicitTildePathShownAsWritten_Pass_S1N1(t *testing.T) {
	f := newPreflight(t, false)
	home := filepath.Join(f.dir, "home")
	writeEd25519(t, filepath.Join(home, "keys"), "k")
	envf := write(t, filepath.Join(f.dir, "t.env"), "SSH_PRIVATE_KEY_PATH=~/keys/k\nSSH_PUBLIC_KEY_PATH=~/keys/k.pub\n", 0o600)
	r := run(t, pfEnv(f, map[string]string{"HOME": home}), "--env-file", envf, "preflight")
	if r.code != 0 {
		t.Fatalf("exit %d, want 0: %s", r.code, r.all())
	}
	if !strings.Contains(r.all(), "~/keys/k") || strings.Contains(r.all(), home) {
		t.Fatalf("PF-06 must show ~/keys/k, not the expanded home: %q", r.all())
	}
}

func TestPF06_ExplicitTildePathShownAsWritten_Fail_S1N1(t *testing.T) {
	f := newPreflight(t, false)
	home := filepath.Join(f.dir, "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	envf := write(t, filepath.Join(f.dir, "t.env"), "SSH_PRIVATE_KEY_PATH=~/keys/k\n", 0o600)
	r := run(t, pfEnv(f, map[string]string{"HOME": home}), "--env-file", envf, "preflight")
	if r.code != 3 || !strings.Contains(r.all(), "PF-06") {
		t.Fatalf("exit %d, want 3 with PF-06: %s", r.code, r.all())
	}
	if !strings.Contains(r.all(), "~/keys/k") || strings.Contains(r.all(), home) {
		t.Fatalf("PF-06 failure must show ~/keys/k, not the expanded home: %q", r.all())
	}
}

func TestPF06_ExplicitTildePathShownAsWritten_Mounted_S1N1(t *testing.T) {
	f := newPreflight(t, false)
	in := mountedKeys(t, f)
	home := filepath.Join(f.dir, "home")
	envf := write(t, filepath.Join(f.dir, "t.env"), "SSH_PRIVATE_KEY_PATH=~/keys/k\n", 0o600)
	r := run(t, pfEnv(f, map[string]string{"HOME": home, "SC_INPUT_DIR": in}), "--env-file", envf, "preflight")
	if r.code != 0 {
		t.Fatalf("exit %d, want 0: %s", r.code, r.all())
	}
	if !strings.Contains(r.all(), "~/keys/k") || strings.Contains(r.all(), home) {
		t.Fatalf("PF-06 must show ~/keys/k, not the expanded home: %q", r.all())
	}
}
