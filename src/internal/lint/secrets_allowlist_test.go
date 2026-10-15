package lint_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"strange.company/sc/internal/lint"
)

// Secret-looking strings are assembled at run time so this file holds no scannable secret.

const allowlistRel = "test/fixtures/secrets-scan/allowlist.yaml"

func ageKey(body string) string { return "AGE-SECRET-" + "KEY-1" + body }
func antKey(body string) string { return "sk-" + "ant-" + body }

func hasFindingAt(r lint.Result, prefix string) bool {
	for _, f := range r.Findings {
		if strings.HasPrefix(f.Path, prefix) {
			return true
		}
	}
	return false
}

func allowlistYAML(path, rule, value string) string {
	return "apiVersion: strange.company/v1alpha1\nkind: SecretsScanAllowlist\nentries:\n" +
		"  - path: " + path + "\n    rule: " + rule + "\n    value: " + value + "\n    reason: test\n"
}

// 01-AC-8: a placeholder earlier on a line must not hide a real key later on the same line.
func TestSecretsScan_MultipleMatchesPerLine_01AC8(t *testing.T) {
	real := "AKIA" + "QYLPMN5HHHFPZAM2"
	d := secretsTree(t)
	write(t, d, "config/keys.yaml", "keys: [AKIAIOSFODNN7"+"EXAMPLE, "+real+"]\n")
	if r := mustSecrets(t, d); !hasRule(r, ruleSecretPattern) {
		t.Errorf("want a secret-pattern finding for the real key after a placeholder, got %v", r.Findings)
	}
}

// 01-AC-8: allowlist entries bind path + rule + exact value (plan 35 section 14.8.1).
func TestSecretsScan_AllowlistSemantics_01AC8(t *testing.T) {
	val := ageKey(strings.Repeat("Q7ZX4M2P9RT5HW8K", 4)[:58])
	other := ageKey(strings.Repeat("Z7ZX4M2P9RT5HW8K", 4)[:58])
	const p = "docs/example.md"
	line := func(v string) string { return "key: " + v + "\n" }
	build := func(t *testing.T, entryPath, entryRule, entryVal string, files map[string]string) string {
		d := secretsTree(t)
		write(t, d, allowlistRel, allowlistYAML(entryPath, entryRule, entryVal))
		for k, v := range files {
			write(t, d, k, v)
		}
		return d
	}
	t.Run("a allowlisted value in the allowlisted path passes", func(t *testing.T) {
		d := build(t, p, "age-secret-key", val, map[string]string{p: line(val)})
		if f := errorsOf(mustSecrets(t, d)); len(f) != 0 {
			t.Errorf("want no findings, got %v", f)
		}
	})
	t.Run("b same value in another path is reported", func(t *testing.T) {
		d := build(t, p, "age-secret-key", val, map[string]string{p: line(val), "docs/other.md": line(val)})
		r := mustSecrets(t, d)
		if !hasFindingAt(r, "docs/other.md") {
			t.Errorf("want a finding in docs/other.md, got %v", r.Findings)
		}
		if hasFindingAt(r, p+":") {
			t.Errorf("allowlisted path must not be reported: %v", r.Findings)
		}
	})
	t.Run("c different value of the rule in the path is reported", func(t *testing.T) {
		d := build(t, p, "age-secret-key", val, map[string]string{p: line(val) + line(other)})
		if r := mustSecrets(t, d); !hasFindingAt(r, p+":2") {
			t.Errorf("want a finding at %s:2, got %v", p, r.Findings)
		}
	})
	t.Run("c2 different repeated-character value is reported", func(t *testing.T) {
		z := ageKey(strings.Repeat("Z", 58))
		d := build(t, p, "age-secret-key", val, map[string]string{p: line(z)})
		if r := mustSecrets(t, d); !hasFindingAt(r, p+":1") {
			t.Errorf("want a finding, got %v", r.Findings)
		}
	})
	t.Run("d unlisted one-repeated-character keys are reported", func(t *testing.T) {
		cases := map[string]string{
			"age":       ageKey(strings.Repeat("Q", 58)),
			"anthropic": antKey(strings.Repeat("A", 24)),
			"openai":    "sk-" + strings.Repeat("B", 40),
		}
		for name, v := range cases {
			d := secretsTree(t)
			write(t, d, "docs/x.md", line(v))
			if r := mustSecrets(t, d); !hasFindingAt(r, "docs/x.md") {
				t.Errorf("%s: one-repeated-character key must be reported, got %v", name, r.Findings)
			}
		}
	})
	t.Run("e stale entry is a secret-pattern finding", func(t *testing.T) {
		// A one-repeated-character value keeps the allowlist file itself clean of other findings.
		stale := ageKey(strings.Repeat("Q", 58))
		d := build(t, p, "age-secret-key", stale, map[string]string{p: "nothing here\n"})
		if r := mustSecrets(t, d); !hasRule(r, ruleSecretPattern) {
			t.Errorf("want a secret-pattern finding for the stale entry, got %v", r.Findings)
		}
	})
}

// 01-AC-8: legacy encrypted PEM (Proc-Type/DEK-Info headers before the body) is a private key.
func TestSecretsScan_LegacyEncryptedPEM_01AC8(t *testing.T) {
	hdr := "-----BEGIN RSA " + "PRIVATE KEY-----\n"
	body := strings.Repeat("MIIEvQIBADANBgkqhkiG9w0B", 3)
	pem := hdr + "Proc-Type: 4,ENCRYPTED\nDEK-Info: AES-128-CBC,0123456789ABCDEF0123456789ABCDEF\n\n" + body + "\n" + body + "\n-----END RSA " + "PRIVATE KEY-----\n"
	d := secretsTree(t)
	write(t, d, "config/legacy.pem", pem)
	if r := mustSecrets(t, d); !hasRule(r, ruleSecretPattern) {
		t.Errorf("want a secret-pattern finding for the encrypted legacy PEM, got %v", r.Findings)
	}
}

// 01-AC-8: the committed allowlist has exactly the two approved entries.
func TestRealTree_SecretsAllowlistExact_01AC8(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(repoRoot(t), allowlistRel))
	if err != nil {
		t.Fatalf("allowlist: %v", err)
	}
	var doc struct {
		Entries []struct{ Path, Rule, Value string }
	}
	if err := yaml.Unmarshal(b, &doc); err != nil {
		t.Fatalf("allowlist YAML: %v", err)
	}
	want := []struct{ Path, Rule, Value string }{
		{"contracts/recovery-bundle.schema.yaml", "age-secret-key", ageKey(strings.Repeat("Q", 58))},
		{"plans/40-opentelemetry.md", "anthropic-api-key", antKey(strings.Repeat("A", 24))},
	}
	if len(doc.Entries) != len(want) {
		t.Fatalf("allowlist has %d entries, want %d", len(doc.Entries), len(want))
	}
	for i, w := range want {
		if doc.Entries[i] != w {
			t.Errorf("entry %d = %+v, want %+v", i, doc.Entries[i], w)
		}
	}
}
