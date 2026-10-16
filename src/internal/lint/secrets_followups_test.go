package lint_test

import (
	"strings"
	"testing"
)

// S3 review follow-ups F1-F4 (plan 35 section 14.8.1, plan 01 section 9), criterion 01-AC-8.
// Secret-looking strings are assembled at run time so this file holds no scannable secret.

// 01-AC-8 (F1): the allowlist file is scanned; only its entries' exact values are exempt there.
func TestSecretsScan_AllowlistFileIsScanned_01AC8(t *testing.T) {
	val := ageKey(strings.Repeat("Q", 58))
	const p = "docs/example.md"
	realKey := "AKIA" + "QYLPMN5HHHFPZAM2"
	d := secretsTree(t)
	write(t, d, p, "key: "+val+"\n")
	write(t, d, allowlistRel, "# note: "+realKey+"\n"+allowlistYAML(p, "age-secret-key", val))
	r := mustSecrets(t, d)
	var inAllowlist int
	for _, f := range r.Findings {
		if strings.HasPrefix(f.Path, allowlistRel) {
			inAllowlist++
			if !strings.HasPrefix(f.Path, allowlistRel+":1") {
				t.Errorf("only the comment on line 1 may be reported in the allowlist file, got %v", f)
			}
		}
	}
	if inAllowlist != 1 {
		t.Errorf("want exactly one finding in %s (the real key in the comment; the entry's own value is exempt), got %v", allowlistRel, r.Findings)
	}
}

// 01-AC-8 (F2): the trailing-EXAMPLE placeholder applies to the AWS patterns only.
func TestSecretsScan_ExamplePlaceholderOnlyForAWS_01AC8(t *testing.T) {
	rnd := "k3Jd9QpZ0xVb7LmN2sTq8RwYc5HgF1uAe4oIz6Xn"[:40]
	cases := []struct {
		name, line string
		reported   bool
	}{
		{"aws access key id placeholder passes", "id: AKIAIOSFODNN7" + "EXAMPLE", false},
		{"anthropic key ending in EXAMPLE is reported", "key: sk-" + "ant-" + rnd + "EXAMPLE", true},
		{"openai key ending in EXAMPLE is reported", "key: sk-" + rnd + "EXAMPLE", true},
		{"github token ending in EXAMPLE is reported", "token: gh" + "p_" + rnd[:36] + "EXAMPLE", true},
		{"slack token ending in EXAMPLE is reported", "token: xox" + "b-" + rnd[:12] + "EXAMPLE", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := secretsTree(t)
			write(t, d, "config/k.yaml", c.line+"\n")
			r := mustSecrets(t, d)
			if got := hasFindingAt(r, "config/k.yaml:1"); got != c.reported {
				t.Errorf("reported=%v, want %v (findings %v)", got, c.reported, r.Findings)
			}
		})
	}
}

// 01-AC-8 (F3): BEGIN header, a blank line, then base64 body lines is a private key.
func TestSecretsScan_PEMBlankLineBeforeBody_01AC8(t *testing.T) {
	body := strings.Repeat("MIIEvQIBADANBgkqhkiG9w0B", 3)
	pem := "-----BEGIN " + "PRIVATE KEY-----\n\n" + body + "\n" + body + "\n-----END " + "PRIVATE KEY-----\n"
	d := secretsTree(t)
	write(t, d, "config/blank.pem", pem)
	r := mustSecrets(t, d)
	if !hasFindingAt(r, "config/blank.pem:1") {
		t.Errorf("want a secret-pattern finding at config/blank.pem:1, got %v", r.Findings)
	}
	// A header alone stays prose.
	d = secretsTree(t)
	write(t, d, "docs/prose.md", "A key starts with -----BEGIN "+"PRIVATE KEY-----\n\nand more text follows.\n")
	if r := mustSecrets(t, d); hasFindingAt(r, "docs/prose.md") {
		t.Errorf("a header followed by prose must not be reported, got %v", r.Findings)
	}
}

// 01-AC-8 (F4): the stale-entry finding names the entry's path once.
func TestSecretsScan_StaleEntryNamesPathOnce_01AC8(t *testing.T) {
	stale := ageKey(strings.Repeat("Q", 58))
	const p = "docs/gone.md"
	d := secretsTree(t)
	write(t, d, allowlistRel, allowlistYAML(p, "age-secret-key", stale))
	write(t, d, p, "nothing here\n")
	r := mustSecrets(t, d)
	n := 0
	for _, f := range r.Findings {
		if strings.Contains(f.Message, "stale") {
			n++
			if c := strings.Count(f.Message, p); c != 1 {
				t.Errorf("stale-entry message names %s %d times, want once: %q", p, c, f.Message)
			}
		}
	}
	if n != 1 {
		t.Errorf("want exactly one stale-entry finding, got %v", r.Findings)
	}
}
