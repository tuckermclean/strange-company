package toolbox_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type selfcheckDoc struct {
	Tools []struct {
		Tool     string `json:"tool"`
		Expected string `json:"expected"`
		Actual   string `json:"actual"`
		OK       bool   `json:"ok"`
	} `json:"tools"`
}

// 02-AC-12 (Go side): `version` exits 0 and needs no SC_REPO_ROOT.
func TestVersion_ExitsZeroWithoutRepoRoot_02AC12(t *testing.T) {
	r := run(t, nil, "version")
	if r.code != 0 || !strings.Contains(r.all(), "sc-bootstrap") {
		t.Fatalf("exit %d, output %q", r.code, r.all())
	}
}

func TestUnknownCommand_IsUsageError(t *testing.T) {
	if r := run(t, nil, "no-such-command"); r.code != 2 {
		t.Fatalf("exit %d, want 2 (plan 50 section 7.2 usage)", r.code)
	}
}

// 02-AC-1 (selfcheck rows): every tool reports {tool, expected, actual, ok} from versionCommand/versionRegex.
func TestSelfcheck_JSONRows_02AC1(t *testing.T) {
	dir := t.TempDir()
	emb := write(t, filepath.Join(dir, "emb.yaml"), lockWithTool("1.2.3"), 0o644)
	r := run(t, map[string]string{"SC_EMBEDDED_LOCK": emb}, "toolbox", "selfcheck", "--json", "--embedded-only")
	if r.code != 0 {
		t.Fatalf("exit %d: %s", r.code, r.all())
	}
	var doc selfcheckDoc
	if err := json.Unmarshal([]byte(r.stdout), &doc); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, r.stdout)
	}
	if len(doc.Tools) != 1 || doc.Tools[0].Tool != "faketool" || doc.Tools[0].Expected != "1.2.3" || !doc.Tools[0].OK {
		t.Fatalf("rows = %+v", doc.Tools)
	}
	if doc.Tools[0].Actual == "" {
		t.Fatalf("actual is empty")
	}
}

func TestSelfcheck_VersionMismatchIsNotOK_02AC1(t *testing.T) {
	dir := t.TempDir()
	emb := write(t, filepath.Join(dir, "emb.yaml"), lockWithTool("9.9.9"), 0o644) // tool prints v1.2.3
	r := run(t, map[string]string{"SC_EMBEDDED_LOCK": emb}, "toolbox", "selfcheck", "--json", "--embedded-only")
	var doc selfcheckDoc
	if err := json.Unmarshal([]byte(r.stdout), &doc); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, r.stdout)
	}
	if r.code == 0 || len(doc.Tools) != 1 || doc.Tools[0].OK {
		t.Fatalf("exit %d rows %+v, want non-zero and ok=false", r.code, doc.Tools)
	}
}

// 02-AC-10: embedded lock and the checkout's lock must be sha256-equal, else TOOLBOX_STALE.
func TestSelfcheck_StaleLockReportsToolboxStale_02AC10(t *testing.T) {
	dir := t.TempDir()
	emb := write(t, filepath.Join(dir, "emb.yaml"), lockWithTool("1.2.3"), 0o644)
	repo := filepath.Join(dir, "repo")
	write(t, filepath.Join(repo, "toolbox", "tools.lock.yaml"), lockWithTool("1.2.4"), 0o644)
	r := run(t, map[string]string{"SC_EMBEDDED_LOCK": emb, "SC_REPO_ROOT": repo}, "toolbox", "selfcheck", "--json")
	if r.code == 0 || !strings.Contains(r.all(), "TOOLBOX_STALE") {
		t.Fatalf("exit %d, output %q", r.code, r.all())
	}
}

func TestSelfcheck_IdenticalLocksAreNotStale_02AC10(t *testing.T) {
	dir := t.TempDir()
	emb := write(t, filepath.Join(dir, "emb.yaml"), lockWithTool("1.2.3"), 0o644)
	repo := filepath.Join(dir, "repo")
	write(t, filepath.Join(repo, "toolbox", "tools.lock.yaml"), lockWithTool("1.2.3"), 0o644)
	r := run(t, map[string]string{"SC_EMBEDDED_LOCK": emb, "SC_REPO_ROOT": repo}, "toolbox", "selfcheck", "--json")
	var doc selfcheckDoc
	if err := json.Unmarshal([]byte(r.stdout), &doc); err != nil || len(doc.Tools) != 1 {
		t.Fatalf("stdout is not selfcheck JSON: %v\n%s", err, r.stdout)
	}
	if r.code != 0 || strings.Contains(r.all(), "TOOLBOX_STALE") {
		t.Fatalf("exit %d, output %q", r.code, r.all())
	}
}

// preflightFixture builds a consistent environment: matching locks, a good ed25519 key and an env file.
type preflightFixture struct {
	env     map[string]string
	envFile string
	dir     string
}

func newPreflight(t *testing.T, stale bool) preflightFixture {
	t.Helper()
	dir := t.TempDir()
	emb := write(t, filepath.Join(dir, "emb.yaml"), lockWithTool("1.2.3"), 0o644)
	repoLock := lockWithTool("1.2.3")
	if stale {
		repoLock = lockWithTool("1.2.4")
	}
	repo := filepath.Join(dir, "repo")
	write(t, filepath.Join(repo, "toolbox", "tools.lock.yaml"), repoLock, 0o644)
	key := writeEd25519(t, filepath.Join(dir, "ssh"), "id_ed25519")
	envFile := write(t, filepath.Join(dir, "bootstrap.env"),
		"# operator settings\nCLUSTER_NAME=demo\nSSH_PRIVATE_KEY_PATH=\""+key+"\"\nSSH_PUBLIC_KEY_PATH='"+key+".pub'\n", 0o600)
	return preflightFixture{
		env:     map[string]string{"SC_EMBEDDED_LOCK": emb, "SC_REPO_ROOT": repo},
		envFile: envFile, dir: dir,
	}
}

func TestPreflight_PassesWithGoodToolboxAndKey_02AC10(t *testing.T) {
	f := newPreflight(t, false)
	r := run(t, f.env, "--env-file", f.envFile, "preflight")
	if r.code != 0 {
		t.Fatalf("exit %d: %s", r.code, r.all())
	}
	if !strings.Contains(r.all(), "PF-01") || !strings.Contains(r.all(), "PF-06") {
		t.Fatalf("output must list PF-01 and PF-06: %q", r.all())
	}
}

// 02-AC-10: a stale toolbox makes preflight exit 3 with `PF-01 TOOLBOX_STALE`.
func TestPreflight_StaleToolboxExits3_02AC10(t *testing.T) {
	f := newPreflight(t, true)
	r := run(t, f.env, "--env-file", f.envFile, "preflight")
	if r.code != 3 || !strings.Contains(r.all(), "PF-01 TOOLBOX_STALE") {
		t.Fatalf("exit %d, output %q", r.code, r.all())
	}
}

// 02-AC-11: the env file is not shell-evaluated; the literal path is used, PF-06 fails, nothing is created.
func TestPreflight_EnvFileValueIsNotEvaluated_02AC11(t *testing.T) {
	f := newPreflight(t, false)
	pwned := filepath.Join(f.dir, "pwned")
	bad := write(t, filepath.Join(f.dir, "evil.env"),
		"SSH_PRIVATE_KEY_PATH=$(touch "+pwned+")\n", 0o600)
	r := run(t, f.env, "--env-file", bad, "preflight")
	if r.code != 3 || !strings.Contains(r.all(), "PF-06") {
		t.Fatalf("exit %d, output %q", r.code, r.all())
	}
	if _, err := os.Stat(pwned); err == nil {
		t.Fatalf("%s exists: the env file value was evaluated", pwned)
	}
	if !strings.Contains(r.all(), "$(touch") {
		t.Fatalf("PF-06 output should show the literal path used: %q", r.all())
	}
}

// 02-AC-11 / PF-06: the private key must be ed25519 or RSA >= 3072.
func TestPreflight_WeakRSAKeyFailsPF06_02AC11(t *testing.T) {
	f := newPreflight(t, false)
	key := writeRSA(t, filepath.Join(f.dir, "weak"), "id_rsa", 2048)
	envf := write(t, filepath.Join(f.dir, "weak.env"), "SSH_PRIVATE_KEY_PATH="+key+"\nSSH_PUBLIC_KEY_PATH="+key+".pub\n", 0o600)
	r := run(t, f.env, "--env-file", envf, "preflight")
	if r.code != 3 || !strings.Contains(r.all(), "PF-06") {
		t.Fatalf("exit %d, output %q", r.code, r.all())
	}
}

func TestPreflight_RSA3072Passes_02AC11(t *testing.T) {
	f := newPreflight(t, false)
	key := writeRSA(t, filepath.Join(f.dir, "strong"), "id_rsa", 3072)
	envf := write(t, filepath.Join(f.dir, "strong.env"), "SSH_PRIVATE_KEY_PATH="+key+"\nSSH_PUBLIC_KEY_PATH="+key+".pub\n", 0o600)
	if r := run(t, f.env, "--env-file", envf, "preflight"); r.code != 0 || !strings.Contains(r.all(), "PF-06") {
		t.Fatalf("exit %d: %s", r.code, r.all())
	}
}

// PF-06: the public key derived from the private key must equal SSH_PUBLIC_KEY_PATH.
func TestPreflight_PublicKeyMismatchFailsPF06_02AC11(t *testing.T) {
	f := newPreflight(t, false)
	a := writeEd25519(t, filepath.Join(f.dir, "a"), "id")
	b := writeEd25519(t, filepath.Join(f.dir, "b"), "id")
	envf := write(t, filepath.Join(f.dir, "mm.env"), "SSH_PRIVATE_KEY_PATH="+a+"\nSSH_PUBLIC_KEY_PATH="+b+".pub\n", 0o600)
	r := run(t, f.env, "--env-file", envf, "preflight")
	if r.code != 3 || !strings.Contains(r.all(), "PF-06") {
		t.Fatalf("exit %d, output %q", r.code, r.all())
	}
}

func TestPreflight_MissingKeyFailsPF06_02AC11(t *testing.T) {
	f := newPreflight(t, false)
	envf := write(t, filepath.Join(f.dir, "none.env"), "SSH_PRIVATE_KEY_PATH="+filepath.Join(f.dir, "absent")+"\n", 0o600)
	r := run(t, f.env, "--env-file", envf, "preflight")
	if r.code != 3 || !strings.Contains(r.all(), "PF-06") {
		t.Fatalf("exit %d, output %q", r.code, r.all())
	}
}

// Plan 02 section 14.6 (s3 scope, D2): config and credentials files under the run dir, export lines only,
// no default profile, no secret value on stdout/stderr.
func TestEnvS3_WritesProfileFilesAndNeverPrintsSecrets_02R3(t *testing.T) {
	dir := t.TempDir()
	run_ := filepath.Join(dir, "run")
	envf := write(t, filepath.Join(dir, "bootstrap.env"),
		"S3_ENDPOINT=https://s3.example.test\nS3_REGION=eu-test-1\nS3_FORCE_PATH_STYLE=true\n"+
			"S3_ACCESS_KEY_ID=AKIACANARY0001\nS3_SECRET_ACCESS_KEY=canary-secret-value-7788\n", 0o600)
	r := run(t, map[string]string{"SC_RUN_DIR": run_}, "--env-file", envf, "env", "s3")
	if r.code != 0 {
		t.Fatalf("exit %d: %s", r.code, r.all())
	}
	if !strings.Contains(r.stdout, "export ") || !strings.Contains(r.stdout, "AWS_CONFIG_FILE=") ||
		!strings.Contains(r.stdout, "AWS_SHARED_CREDENTIALS_FILE=") {
		t.Fatalf("stdout lacks the export lines: %q", r.stdout)
	}
	if strings.Contains(r.all(), "canary-secret-value-7788") {
		t.Fatalf("secret value printed: %q", r.all())
	}
	for _, bad := range []string{"AWS_PROFILE", "AWS_DEFAULT_PROFILE", "AWS_SECRET_ACCESS_KEY"} {
		if strings.Contains(r.stdout, bad) {
			t.Fatalf("stdout exports %s: %q", bad, r.stdout)
		}
	}
	cfg, err := os.ReadFile(filepath.Join(run_, "aws", "config"))
	if err != nil {
		t.Fatalf("config not written: %v", err)
	}
	cred, err := os.ReadFile(filepath.Join(run_, "aws", "credentials"))
	if err != nil {
		t.Fatalf("credentials not written: %v", err)
	}
	if !strings.Contains(string(cfg), "sc-admin") || !strings.Contains(string(cfg), "https://s3.example.test") ||
		!strings.Contains(string(cfg), "eu-test-1") {
		t.Fatalf("config lacks profile/endpoint/region:\n%s", cfg)
	}
	if !strings.Contains(string(cred), "[sc-admin]") || !strings.Contains(string(cred), "canary-secret-value-7788") {
		t.Fatalf("credentials lack the sc-admin profile:\n%s", cred)
	}
	for _, f := range []string{string(cfg), string(cred)} {
		if strings.Contains(f, "[default]") || strings.Contains(f, "[profile default]") {
			t.Fatalf("a default profile was written:\n%s", f)
		}
	}
	fi, err := os.Stat(filepath.Join(run_, "aws", "credentials"))
	if err != nil || fi.Mode().Perm()&0o077 != 0 {
		t.Fatalf("credentials must not be group/world accessible: %v %v", fi, err)
	}
}

// Plan 02 sections 14.4/14.5 steps 1-2: nss_wrapper files for sc-operator, work dirs, SSH key copy at 0600.
func TestInit_WritesNssFilesDirsAndCopiesKey_02R3(t *testing.T) {
	dir := t.TempDir()
	runDir, in := filepath.Join(dir, "run"), filepath.Join(dir, "input")
	keyBody := "fixture-private-key-bytes\n"
	write(t, filepath.Join(in, "ssh", "id_key"), keyBody, 0o644)
	r := run(t, map[string]string{"SC_RUN_DIR": runDir, "SC_INPUT_DIR": in}, "toolbox", "init")
	if r.code != 0 {
		t.Fatalf("exit %d: %s", r.code, r.all())
	}
	for _, d := range []string{"home", "ssh", "aws", "tmp"} {
		if fi, err := os.Stat(filepath.Join(runDir, d)); err != nil || !fi.IsDir() {
			t.Fatalf("%s not created: %v", d, err)
		}
	}
	pw, err := os.ReadFile(filepath.Join(runDir, "passwd"))
	if err != nil {
		t.Fatalf("passwd not written: %v", err)
	}
	uid := os.Getuid()
	if !strings.Contains(string(pw), "sc-operator:") || !strings.Contains(string(pw), ":"+itoa(uid)+":") ||
		!strings.Contains(string(pw), filepath.Join(runDir, "home")) {
		t.Fatalf("passwd lacks sc-operator/uid/home:\n%s", pw)
	}
	if grp, err := os.ReadFile(filepath.Join(runDir, "group")); err != nil || !strings.Contains(string(grp), ":"+itoa(os.Getgid())+":") {
		t.Fatalf("group file missing the gid: %v %q", err, grp)
	}
	dst := filepath.Join(runDir, "ssh", "id_key")
	got, err := os.ReadFile(dst)
	if err != nil || string(got) != keyBody {
		t.Fatalf("key not copied: %v %q", err, got)
	}
	if fi, _ := os.Stat(dst); fi.Mode().Perm() != 0o600 {
		t.Fatalf("copied key mode = %v, want 0600", fi.Mode().Perm())
	}
}

func itoa(i int) string {
	b, _ := json.Marshal(i)
	return string(b)
}
