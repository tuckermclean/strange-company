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
