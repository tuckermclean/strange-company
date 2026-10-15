package lint

import (
	"bytes"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Rule IDs of `sc-lint secrets` (plan 01 section 7.1 secrets-scan).
const (
	ruleSecretPattern = "secret-pattern"
	ruleSopsStanza    = "sops-stanza"
	ruleSopsRecipient = "sops-recipients"
)

const maxScanBytes = 2 << 20

// secretPatterns is the pinned ruleset (gitleaks-equivalent subset). Matches containing EXAMPLE are the
// documentation placeholders of the vendors and pass.
var secretPatterns = []struct {
	name string
	re   *regexp.Regexp
}{
	{"aws-access-key-id", regexp.MustCompile(`\b(?:AKIA|ASIA|AGPA|AIDA|AROA|ANPA|ANVA)[0-9A-Z]{16}\b`)},
	{"aws-secret-access-key", regexp.MustCompile(`(?i)aws_?secret_?access_?key["']?\s*[:=]\s*["']?[A-Za-z0-9/+=]{40,}`)},
	{"private-key-block", regexp.MustCompile(`-----BEGIN (?:[A-Z]+ )*PRIVATE KEY(?: BLOCK)?-----`)},
	{"age-secret-key", regexp.MustCompile(`AGE-SECRET-KEY-1[0-9A-Z]{58}`)},
	{"github-token", regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9]{36,}|github_pat_[A-Za-z0-9_]{22,})`)},
	{"slack-token", regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9-]{10,}`)},
	{"anthropic-api-key", regexp.MustCompile(`\bsk-ant-[A-Za-z0-9_-]{20,}`)},
	{"openai-api-key", regexp.MustCompile(`\bsk-(?:proj-)?[A-Za-z0-9_-]{32,}`)},
	{"google-api-key", regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}\b`)},
}

// secretAllowlist holds the exact files `make secrets-scan` whitelists (plan 01 section 6.5).
var secretAllowlist = map[string]bool{"test/fixtures/age/ci-test.agekey": true}

// SecretsScan evaluates the plaintext-secret and SOPS rules of plan 01 section 7.1 on the tree under root.
func SecretsScan(root string) (Result, error) {
	var r Result
	r.ran(ruleSecretPattern)
	r.ran(ruleSopsStanza)
	r.ran(ruleSopsRecipient)
	files, err := listFiles(root)
	if err != nil {
		return r, err
	}
	rules, rulesErr := loadSopsRules(root)
	var sopsFiles []string
	for _, rel := range files {
		if (strings.HasSuffix(rel, ".sops.yaml") || strings.HasSuffix(rel, ".sops.yml")) && path.Base(rel) != ".sops.yaml" {
			sopsFiles = append(sopsFiles, rel)
		}
		if secretAllowlist[rel] {
			continue
		}
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil || len(b) > maxScanBytes || bytes.IndexByte(b, 0) >= 0 {
			continue
		}
		scanSecrets(rel, b, &r)
	}
	for _, rel := range sopsFiles {
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			return r, err
		}
		checkSops(rel, b, rules, rulesErr, &r)
	}
	return r, nil
}

var pemBodyRe = regexp.MustCompile(`^\s*[A-Za-z0-9+/=]{40,}\s*$`)

// placeholder reports whether a match is documentation filler: it contains EXAMPLE, or its tail is a run of
// at most two distinct characters (AAAAAAAA, QQQQQQQQ).
func placeholder(m string) bool {
	if strings.Contains(strings.ToUpper(m), "EXAMPLE") {
		return true
	}
	tail := m
	if len(tail) > 16 {
		tail = tail[len(tail)-16:]
	}
	seen := map[rune]bool{}
	for _, c := range tail {
		seen[c] = true
	}
	return len(seen) <= 2
}

func scanSecrets(rel string, b []byte, r *Result) {
	lines := strings.Split(string(b), "\n")
	for n, line := range lines {
		for _, p := range secretPatterns {
			m := p.re.FindString(line)
			if m == "" {
				continue
			}
			if p.name == "private-key-block" {
				// A header alone is prose or a schema example; a key has its base64 body on the next line.
				if n+1 >= len(lines) || !pemBodyRe.MatchString(lines[n+1]) {
					continue
				}
			} else if placeholder(m) {
				continue
			}
			// The value is never printed.
			r.add(ruleSecretPattern, fmt.Sprintf("%s:%d", rel, n+1), "matches %s", p.name)
		}
	}
}

type sopsRule struct {
	re   *regexp.Regexp
	age  []string
	expr string
}

// loadSopsRules reads the creation rules of .sops.yaml; a missing file gives no rules and no error.
func loadSopsRules(root string) ([]sopsRule, error) {
	v, err := readYAML(root, ".sops.yaml")
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf(".sops.yaml: %v", err)
	}
	rs, _ := at(v, "creation_rules").([]any)
	var out []sopsRule
	for i, x := range rs {
		expr := str(at(x, "path_regex"))
		re, err := regexp.Compile(expr)
		if err != nil {
			return nil, fmt.Errorf(".sops.yaml rule %d: path_regex: %v", i+1, err)
		}
		var age []string
		for _, f := range strings.FieldsFunc(str(at(x, "age")), func(c rune) bool { return c == ',' || c == ' ' || c == '\n' || c == '\t' }) {
			age = append(age, f)
		}
		out = append(out, sopsRule{re: re, age: age, expr: expr})
	}
	return out, nil
}

func checkSops(rel string, b []byte, rules []sopsRule, rulesErr error, r *Result) {
	var doc any
	if err := yaml.Unmarshal(b, &doc); err != nil {
		r.add(ruleSopsStanza, rel, "not parseable YAML: %v", err)
		return
	}
	stanza, ok := at(doc, "sops").(map[string]any)
	if !ok {
		r.add(ruleSopsStanza, rel, "no sops: stanza (the file is not SOPS-encrypted)")
		return
	}
	if str(stanza["mac"]) == "" {
		r.add(ruleSopsStanza, rel, "sops: stanza has no mac")
	}
	got := map[string]bool{}
	groups := 0
	for key, v := range stanza {
		list, _ := v.([]any)
		switch key {
		case "age":
			for _, e := range list {
				groups++
				if rc := str(at(e, "recipient")); rc != "" {
					got[rc] = true
				}
			}
		case "kms", "gcp_kms", "azure_kv", "hc_vault", "pgp":
			if len(list) > 0 {
				groups++
				r.add(ruleSopsRecipient, rel, "sops: %s recipients are not allowed; only age", key)
			}
		}
	}
	if groups == 0 {
		r.add(ruleSopsStanza, rel, "sops: stanza lists no recipient")
		return
	}
	if hasPrefixDir(rel, "secrets/offcluster") && len(got) != 1 {
		r.add(ruleSopsRecipient, rel, "off-cluster files are encrypted to the recovery recipient only; found %d recipients", len(got))
	}
	if rulesErr != nil {
		r.add(ruleSopsRecipient, ".sops.yaml", "%v", rulesErr)
		return
	}
	var rule *sopsRule
	for i := range rules {
		if rules[i].re.MatchString(path.Clean(rel)) {
			rule = &rules[i]
			break
		}
	}
	if rule == nil {
		r.add(ruleSopsRecipient, rel, "no .sops.yaml creation rule matches this path")
		return
	}
	want := map[string]bool{}
	for _, a := range rule.age {
		want[a] = true
	}
	for _, rc := range sortedKeys(got) {
		if !want[rc] {
			r.add(ruleSopsRecipient, rel, "recipient %s is not in the creation rule %s", rc, rule.expr)
		}
	}
	for _, rc := range sortedKeys(want) {
		if !got[rc] {
			r.add(ruleSopsRecipient, rel, "creation rule %s requires recipient %s", rule.expr, rc)
		}
	}
}

func sortedKeys(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
