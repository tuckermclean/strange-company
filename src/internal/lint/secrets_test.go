package lint_test

import (
	"bytes"
	"strings"
	"testing"

	"strange.company/sc/internal/lint"
)

// Rule IDs of `sc-lint secrets` (plan 01 section 7.1 secrets-scan). The plan names none; chosen here.
const (
	ruleSecretPattern = "secret-pattern"
	ruleSopsStanza    = "sops-stanza"
	ruleSopsRecipient = "sops-recipients"
)

var allSecretRules = []string{ruleSecretPattern, ruleSopsStanza, ruleSopsRecipient}

// Public age recipients from plan 35 section 14.2.
const (
	recoveryRecipient = "age1zvkyg2lqzraa2lnjvqej32nkuu0ues2s82hzrye869xeexvn73equnujwj"
	clusterRecipient  = "age1ql3z7hjy54pw3hyww5ayyfg7zqgvc7w3j2elw8zmrj2kg5sfn9aqmcac8p"
)

const sopsCreationRules = `creation_rules:
  - path_regex: ^secrets/offcluster/x/[a-z0-9.-]+\.sops\.yaml$
    age: ` + recoveryRecipient + `
  - path_regex: ^gitops/clusters/x/.+\.sops\.yaml$
    encrypted_regex: ^(data|stringData)$
    age: >-
      ` + clusterRecipient + `,
      ` + recoveryRecipient + `
`

// sopsFile renders an encrypted-looking SOPS file with the given age recipients.
func sopsFile(recipients ...string) string {
	var b strings.Builder
	b.WriteString("apiVersion: v1\nkind: Secret\nmetadata: {name: s}\nstringData:\n  token: ENC[AES256_GCM,data:Zm9v,iv:YmFy,tag:YmF6,type:str]\nsops:\n  age:\n")
	for _, r := range recipients {
		b.WriteString("    - recipient: " + r + "\n      enc: |\n        -----BEGIN AGE ENCRYPTED FILE-----\n        YWdlLWVuY3J5cHRpb24ub3JnL3YxCg\n        -----END AGE ENCRYPTED FILE-----\n")
	}
	b.WriteString("  lastmodified: \"2026-10-14T00:00:00Z\"\n  mac: ENC[AES256_GCM,data:bWFj,iv:aXY=,tag:dGFn,type:str]\n  version: 3.9.0\n")
	return b.String()
}

func secretsTree(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	write(t, d, ".sops.yaml", sopsCreationRules)
	write(t, d, "secrets/offcluster/x/a.sops.yaml", sopsFile(recoveryRecipient))
	write(t, d, "gitops/clusters/x/vars/cluster-secrets.sops.yaml", sopsFile(clusterRecipient, recoveryRecipient))
	write(t, d, "config/plain.yaml", "name: x\nreplicas: 2\n")
	return d
}

func mustSecrets(t *testing.T, dir string) lint.Result {
	t.Helper()
	r, err := lint.SecretsScan(dir)
	if err != nil {
		t.Fatalf("SecretsScan: %v", err)
	}
	return r
}

// 01-AC-8: a correctly encrypted tree passes and every rule was evaluated.
func TestSecretsScan_ValidFixturePasses_01AC8(t *testing.T) {
	r := mustSecrets(t, secretsTree(t))
	ranRules(t, r, allSecretRules...)
	if f := errorsOf(r); len(f) != 0 {
		t.Errorf("valid fixture has findings: %v", f)
	}
}

// 01-AC-8: the three fixture violations each fail with their rule ID, in the findings and via sc-lint secrets.
func TestSecretsScan_Negatives_01AC8(t *testing.T) {
	// Assembled at run time so this source file holds no scannable secret.
	awsKey := "AKIA" + "QYLPMN5HHHFPZAM2"
	awsSecret := "wJalrXUtnFEMI/K7MDENG/" + "bPxRfiCYzK3Q9bN7tVa"
	cases := []struct {
		name, rule string
		mutate     func(t *testing.T, d string)
	}{
		{"plaintext AWS-style key in a YAML file", ruleSecretPattern, func(t *testing.T, d string) {
			write(t, d, "config/creds.yaml", "s3:\n  aws_access_key_id: "+awsKey+"\n  aws_secret_access_key: \""+awsSecret+"\"\n")
		}},
		{"sops.yaml without sops stanza", ruleSopsStanza, func(t *testing.T, d string) {
			write(t, d, "gitops/clusters/x/vars/plain.sops.yaml", "apiVersion: v1\nkind: Secret\nmetadata: {name: s}\nstringData:\n  token: hunter2\n")
		}},
		{"offcluster file encrypted to the cluster recipient", ruleSopsRecipient, func(t *testing.T, d string) {
			write(t, d, "secrets/offcluster/x/a.sops.yaml", sopsFile(clusterRecipient, recoveryRecipient))
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := secretsTree(t)
			c.mutate(t, d)
			r := mustSecrets(t, d)
			if !hasRule(r, c.rule) {
				t.Errorf("want rule %s, got findings %v", c.rule, r.Findings)
			}
			var out, errb bytes.Buffer
			code := lint.Main([]string{"secrets", "--root", d}, &out, &errb)
			if code != 1 || !strings.Contains(out.String(), c.rule) {
				t.Errorf("sc-lint secrets: exit %d (want 1), output must name %s:\n%s%s", code, c.rule, out.String(), errb.String())
			}
		})
	}
}

// 01-AC-8: `make secrets-scan` exits 0 on the real tree.
func TestRealTree_MakeSecretsScan_01AC8(t *testing.T) {
	runMake(t, "secrets-scan")
}

func TestRealTree_SecretsScanPasses_01AC8(t *testing.T) {
	r := mustSecrets(t, repoRoot(t))
	ranRules(t, r, allSecretRules...)
	for _, f := range errorsOf(r) {
		t.Errorf("real tree: %s", f)
	}
}

// 01-AC-8: a PEM private key escaped onto one line (JSON value with literal backslash-n) is a finding; a
// header-only mention (no body), as in the contract examples, is not.
func TestSecretsScan_PEMOneLine_01AC8(t *testing.T) {
	hdr := "-----BEGIN " + "PRIVATE KEY-----"
	body := strings.Repeat("MIIEvQIBADANBgkqhkiG9w0B", 3)
	cases := []struct {
		name, content string
		want          bool
	}{
		{"escaped on one line", "{\"private_key\":\"" + hdr + "\\n" + body + "\\n-----END " + "PRIVATE KEY-----\\n\"}\n", true},
		{"header only", "example: \"" + hdr + "\"\nnote: the worker input carries a key\n", false},
		{"header only inside JSON text", "{\"hint\":\"" + hdr + "\\n...\"}\n", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := secretsTree(t)
			write(t, d, "config/key.json", c.content)
			r := mustSecrets(t, d)
			if got := hasRule(r, ruleSecretPattern); got != c.want {
				t.Errorf("secret-pattern finding = %v, want %v; findings %v", got, c.want, r.Findings)
			}
		})
	}
}

// 01-AC-8: EXAMPLE in the middle of a key, or an all-Z filler, is not a placeholder; only a key that ends
// in EXAMPLE (the vendor documentation keys) is.
func TestSecretsScan_Placeholders_01AC8(t *testing.T) {
	cases := []struct {
		name, key string
		want      bool
	}{
		{"EXAMPLE in the middle", "AKIA" + "QYLPMN" + "EXAMPLE" + "7ZZ", true},
		{"all Z filler", "AKIA" + strings.Repeat("Z", 16), true},
		{"AWS documentation key", "AKIA" + "IOSFODNN7" + "EXAMPLE", false},
		{"key ending in EXAMPLE", "AKIA" + "QYLPMN5HH" + "EXAMPLE", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := secretsTree(t)
			write(t, d, "config/creds.yaml", "aws_access_key_id: "+c.key+"\n")
			r := mustSecrets(t, d)
			if got := hasRule(r, ruleSecretPattern); got != c.want {
				t.Errorf("secret-pattern finding for %q = %v, want %v; findings %v", c.name, got, c.want, r.Findings)
			}
		})
	}
}
