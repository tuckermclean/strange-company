package toolbox_test

import (
	"path/filepath"
	"strings"
	"testing"
)

func lockWithCommand(version, command string) string {
	return strings.Replace(lockWithTool(version), `versionCommand: [sh, -c, "echo v1.2.3"]`, `versionCommand: [sh, -c, "`+command+`"]`, 1)
}

func pf01Fixture(t *testing.T, command string) (map[string]string, string) {
	t.Helper()
	dir := t.TempDir()
	lock := lockWithCommand("9.9.9", command)
	emb := write(t, filepath.Join(dir, "emb.yaml"), lock, 0o644)
	repo := filepath.Join(dir, "repo")
	write(t, filepath.Join(repo, "toolbox", "tools.lock.yaml"), lock, 0o644)
	key := writeEd25519(t, filepath.Join(dir, "ssh"), "id_ed25519")
	envFile := write(t, filepath.Join(dir, "bootstrap.env"),
		"CLUSTER_NAME=demo\nSSH_PRIVATE_KEY_PATH=\""+key+"\"\nSSH_PUBLIC_KEY_PATH='"+key+".pub'\n", 0o600)
	return map[string]string{"SC_EMBEDDED_LOCK": emb, "SC_REPO_ROOT": repo, "SC_FIXTURE_SECRET": "fixture-secret-0123456789abcdef"}, envFile
}

// 02-AC-10 (plan 50 section 14.2 PF-01): TOOLBOX_TOOL_MISMATCH names each mismatched tool with the locked version
// and the observed version output, so a CI failure can be diagnosed from the log alone.
func TestPreflight_Pf01MismatchShowsObservedOutput_02AC10(t *testing.T) {
	env, envFile := pf01Fixture(t, "echo v1.2.3")
	r := run(t, env, "--env-file", envFile, "preflight")
	if r.code != 3 || !strings.Contains(r.all(), "PF-01 TOOLBOX_TOOL_MISMATCH") {
		t.Fatalf("exit %d, output %q", r.code, r.all())
	}
	for _, want := range []string{"faketool", "9.9.9", "v1.2.3"} {
		if !strings.Contains(r.all(), want) {
			t.Errorf("PF-01 output lacks %q: %q", want, r.all())
		}
	}
	if strings.Contains(r.all(), "fixture-secret") {
		t.Errorf("a process-environment secret reached the output: %q", r.all())
	}
}

// 02-AC-10: when the version command itself fails, its error text is shown, truncated.
func TestPreflight_Pf01MismatchShowsCommandFailureTruncated_02AC10(t *testing.T) {
	env, envFile := pf01Fixture(t, "echo broken-loader-diagnostic >&2; head -c 5000 /dev/zero | tr '\\\\0' Q; exit 7")
	r := run(t, env, "--env-file", envFile, "preflight")
	if r.code != 3 || !strings.Contains(r.all(), "PF-01 TOOLBOX_TOOL_MISMATCH") {
		t.Fatalf("exit %d, output %q", r.code, r.all())
	}
	if !strings.Contains(r.all(), "broken-loader-diagnostic") {
		t.Errorf("PF-01 output lacks the version command's output: %q", r.all())
	}
	if n := strings.Count(r.all(), "Q"); n == 0 || n > 400 {
		t.Errorf("observed output not shown truncated: %d Q characters in %q", n, r.all())
	}
}
