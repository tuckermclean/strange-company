package lint_test

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"strange.company/sc/internal/lint"
)

// Plan 52 section 9.4 (determinism): the same tree gives byte-identical stdout and report.json on every run,
// and findings of one rule come out sorted by (path, message), whatever the map iteration order was.

func runLintTwenty(t *testing.T, cmd, dir, report string) {
	t.Helper()
	var firstOut, firstRep []byte
	for i := 0; i < 20; i++ {
		var out, errb bytes.Buffer
		if code := lint.Main([]string{cmd, "--root", dir}, &out, &errb); code != 1 {
			t.Fatalf("run %d: sc-lint %s exit %d (want 1)\n%s%s", i, cmd, code, out.String(), errb.String())
		}
		rep, err := os.ReadFile(filepath.Join(dir, "out", report, "report.json"))
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			firstOut, firstRep = out.Bytes(), rep
			if n := strings.Count(out.String(), "error: "); n < 3 {
				t.Fatalf("fixture must yield at least 3 findings, got %d:\n%s", n, out.String())
			}
			checkSortedFindings(t, out.String())
			continue
		}
		if !bytes.Equal(out.Bytes(), firstOut) {
			t.Fatalf("run %d: stdout differs from run 0:\n--- run 0\n%s--- run %d\n%s", i, firstOut, i, out.Bytes())
		}
		if !bytes.Equal(rep, firstRep) {
			t.Fatalf("run %d: out/%s/report.json differs from run 0", i, report)
		}
	}
}

// checkSortedFindings requires that consecutive findings of the same rule are ordered by (path, message).
func checkSortedFindings(t *testing.T, out string) {
	t.Helper()
	type f struct{ rule, path, msg string }
	var prev *f
	for _, l := range strings.Split(out, "\n") {
		parts := strings.SplitN(l, ": ", 4)
		if len(parts) < 4 || parts[0] != "error" {
			continue
		}
		cur := &f{parts[1], parts[2], parts[3]}
		if prev != nil && prev.rule == cur.rule && (prev.path > cur.path || prev.path == cur.path && prev.msg > cur.msg) {
			t.Errorf("findings of rule %s are not sorted by (path, message):\n  %s: %s\n  %s: %s", cur.rule, prev.path, prev.msg, cur.path, cur.msg)
		}
		prev = cur
	}
}

// 52-AC-1 / 01-AC-7 (plan 52 9.4): `sc-lint locks` output is deterministic with several components in the compat table.
func TestLocksCheck_Deterministic_01AC7(t *testing.T) {
	d := locksTree(t)
	var b strings.Builder
	b.WriteString("apiVersion: strange.company/v1alpha1\nkind: CompatibilityMatrix\nmetadata: {name: compat}\nspec:\n  components:\n")
	for _, c := range []string{"zulu", "alpha", "mike", "echo", "tango", "bravo", "kilo", "delta"} {
		fmt.Fprintf(&b, "    %s: {version: \"1.0.0\", kubernetesMinors: [\"1.20\"], source: \"https://example.invalid/%s\"}\n", c, c)
	}
	write(t, d, "versions/compat.yaml", b.String())
	runLintTwenty(t, "locks", d, "locks-check")
}

// 01-AC-8 (plan 52 9.4): `sc-lint secrets` output is deterministic with several recipient groups in one stanza.
func TestSecretsScan_Deterministic_01AC8(t *testing.T) {
	d := secretsTree(t)
	for _, n := range []string{"a", "b", "c"} {
		body := sopsFile(recoveryRecipient)
		body = strings.Replace(body, "  lastmodified:", "  kms: [{arn: x}]\n  gcp_kms: [{resource_id: x}]\n  azure_kv: [{vault_url: x}]\n  hc_vault: [{vault_address: x}]\n  pgp: [{fp: x}]\n  lastmodified:", 1)
		write(t, d, "secrets/offcluster/x/"+n+".sops.yaml", body)
	}
	runLintTwenty(t, "secrets", d, "secrets-scan")
}
