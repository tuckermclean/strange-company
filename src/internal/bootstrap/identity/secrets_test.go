package identity_test

import (
	"encoding/base64"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"strange.company/sc/internal/bootstrap/identity"
	"strange.company/sc/internal/bootstrap/redact"
)

var uuidV4Re = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// Plan 50 section 14.3 step 1 and section 10: plan.install-identity generates the UUID and every secret plan 35
// assigns to it (SC-K3S-TOKEN, SC-AGE-CLUSTER, SC-FORGEJO-ADMIN, SC-BREAK-GLASS-*, SC-PG-APP seeds, ...).
func TestGenerate_EveryInstallIdentityClass_50AC10(t *testing.T) {
	set := genSet(t)
	if !uuidV4Re.MatchString(set.InstallUID) {
		t.Errorf("InstallUID %q is not a UUIDv4", set.InstallUID)
	}
	want := generatedClasses(t)
	got := map[string][]identity.Secret{}
	for _, s := range set.Secrets {
		got[s.Class] = append(got[s.Class], s)
	}
	for id := range want {
		if len(got[id]) == 0 {
			t.Errorf("class %s (plan 35: created by plan.install-identity) has no generated secret", id)
		}
	}
	for id := range got {
		if _, ok := want[id]; !ok {
			t.Errorf("class %s was generated but plan 35 does not assign it to plan.install-identity", id)
		}
	}
	for id, c := range want {
		if id == "SC-RUNNER-ADMIN" {
			continue
		}
		for _, in := range c.Instances {
			found := false
			for _, s := range got[id] {
				found = found || s.Instance == in.Name
			}
			if !found {
				t.Errorf("class %s instance %s has no generated secret", id, in.Name)
			}
		}
	}
	// SC-RUNNER-ADMIN: only the break-glass instance exists before provision (operators join later).
	for _, s := range got["SC-RUNNER-ADMIN"] {
		if s.Instance != "breakglass" {
			t.Errorf("SC-RUNNER-ADMIN instance %q generated at install, want only breakglass", s.Instance)
		}
	}
	if len(got["SC-RUNNER-ADMIN"]) != 1 {
		t.Errorf("SC-RUNNER-ADMIN secrets %d, want 1 (breakglass)", len(got["SC-RUNNER-ADMIN"]))
	}
}

// Plan 35 register: InBundle follows inRecoveryBundle of the class (or of the instance when the class lists them).
func TestGenerate_InBundleFollowsRegister_50AC10(t *testing.T) {
	set := genSet(t)
	want := generatedClasses(t)
	for _, s := range set.Secrets {
		c, ok := want[s.Class]
		if !ok {
			continue
		}
		exp := c.InRecoveryBundle
		for _, in := range c.Instances {
			if in.Name == s.Instance && in.InRecoveryBundle != nil {
				exp = *in.InRecoveryBundle
			}
		}
		if s.InBundle != exp {
			t.Errorf("%s/%s InBundle=%v, register says %v", s.Class, s.Instance, s.InBundle, exp)
		}
	}
}

// Plan 35: the formats are fixed by the register text: SC-K3S-TOKEN is 32 random bytes in hex, SC-TOFU-ENCRYPTION
// 32 random bytes in base64url; the age classes are age-keygen files; no secret is empty or trivially short; no two
// secrets share a value.
func TestGenerate_ValueFormats_50AC10(t *testing.T) {
	set := genSet(t)
	if k := set.Find("SC-K3S-TOKEN", ""); k == nil {
		t.Fatal("no SC-K3S-TOKEN")
	} else if b, err := hex.DecodeString(strings.TrimSpace(string(k.Value))); err != nil || len(b) != 32 {
		t.Errorf("SC-K3S-TOKEN is not 32 bytes of hex: %v", err)
	}
	if k := set.Find("SC-TOFU-ENCRYPTION", ""); k == nil {
		t.Fatal("no SC-TOFU-ENCRYPTION")
	} else if b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(strings.TrimSpace(string(k.Value)), "=")); err != nil || len(b) != 32 {
		t.Errorf("SC-TOFU-ENCRYPTION is not 32 bytes of base64url: %v", err)
	}
	for _, c := range []string{"SC-AGE-CLUSTER", "SC-AGE-RECOVERY", "SC-AGE-BACKUP"} {
		s := set.Find(c, "")
		if s == nil {
			t.Errorf("no %s", c)
			continue
		}
		v := string(s.Value)
		if !strings.Contains(v, "AGE-SECRET-KEY-1") || !strings.Contains(v, "# public key: age1") {
			t.Errorf("%s value is not an age-keygen file", c)
		}
	}
	if !recipientRe.MatchString(set.ClusterRecipient) || !recipientRe.MatchString(set.RecoveryRecipient) {
		t.Errorf("recipients %q / %q are not age public keys", set.ClusterRecipient, set.RecoveryRecipient)
	}
	if set.ClusterRecipient == set.RecoveryRecipient {
		t.Error("cluster and recovery recipients are equal")
	}
	if s := set.Find("SC-AGE-CLUSTER", ""); s != nil && !strings.Contains(string(s.Value), "# public key: "+set.ClusterRecipient) {
		t.Error("SC-AGE-CLUSTER file does not carry the cluster recipient")
	}
	if s := set.Find("SC-AGE-RECOVERY", ""); s != nil && !strings.Contains(string(s.Value), "# public key: "+set.RecoveryRecipient) {
		t.Error("SC-AGE-RECOVERY file does not carry the recovery recipient")
	}
	seen := map[string]string{}
	for _, s := range set.Secrets {
		id := s.Class + "/" + s.Instance
		if len(s.Value) < 16 {
			t.Errorf("%s value is %d bytes, want at least 16", id, len(s.Value))
		}
		if prev, dup := seen[string(s.Value)]; dup {
			t.Errorf("%s and %s share a value", id, prev)
		}
		seen[string(s.Value)] = id
	}
}

// Each run generates new material: two Generate calls share no secret value and no install UID.
func TestGenerate_RunsDiffer_50AC10(t *testing.T) {
	a, b := genSet(t), genSet(t)
	if a.InstallUID == b.InstallUID {
		t.Error("two runs produced the same install UID")
	}
	vals := map[string]bool{}
	for _, s := range a.Secrets {
		vals[string(s.Value)] = true
	}
	for _, s := range b.Secrets {
		if vals[string(s.Value)] {
			t.Errorf("%s/%s has the same value in two runs", s.Class, s.Instance)
		}
	}
}

// Plan 50 section 14.3: passphrase policy: at least 20 characters or at least 5 space-separated words, no leading
// or trailing whitespace, must not contain CLUSTER_NAME.
func TestCheckPassphrase_Policy_50AC10(t *testing.T) {
	ok := []string{
		"abcdefghijklmnopqrst",                 // 20 characters
		"one two three four five",              // 5 words
		"a b c d e",                            // 5 words, short
		"tiny-but-twenty-chars-xx",             // long single token
		"Zx9$kQ2#mL8@vR4!nT6%",                 // 20 characters of symbols
		"correct horse battery staple lab one", // words
	}
	for _, p := range ok {
		if err := identity.CheckPassphrase(p, cluster); err != nil {
			t.Errorf("CheckPassphrase(%q) = %v, want accepted", p, err)
		}
	}
	bad := map[string]string{
		"too short":                         "short pass",
		"four words only":                   "one two three four",
		"nineteen characters":               "abcdefghijklmnopqrs",
		"empty":                             "",
		"leading space":                     " abcdefghijklmnopqrstuvwx",
		"trailing space":                    "abcdefghijklmnopqrstuvwx ",
		"trailing newline":                  "abcdefghijklmnopqrstuvwx\n",
		"contains the cluster name":         "my secret for lab-one is long",
		"contains the cluster name in word": "x y z lab-one w",
	}
	for name, p := range bad {
		err := identity.CheckPassphrase(p, cluster)
		if err == nil {
			t.Errorf("%s: CheckPassphrase(%q) accepted", name, p)
			continue
		}
		var ie *identity.Error
		if !errors.As(err, &ie) || !strings.HasPrefix(ie.Code, "PASSPHRASE_") {
			t.Errorf("%s: error %v is not an identity.Error with a PASSPHRASE_ code", name, err)
		}
		if strings.Contains(err.Error(), p) && p != "" {
			t.Errorf("%s: the error message repeats the passphrase", name)
		}
	}
}

// Plan 50 section 14.3: the new passphrase is prompted twice; the entries must match and satisfy the policy.
func TestNewPassphrase_PromptsTwiceAndMustMatch_50AC10(t *testing.T) {
	seq := func(vals ...string) (func() (string, error), *int) {
		n := 0
		return func() (string, error) {
			v := vals[n%len(vals)]
			n++
			return v, nil
		}, &n
	}
	p, n := seq(goodPass, goodPass)
	got, err := identity.NewPassphrase(p, cluster)
	if err != nil || got != goodPass {
		t.Fatalf("NewPassphrase = %q, %v; want the entered passphrase", got, err)
	}
	if *n != 2 {
		t.Errorf("prompted %d times, want 2", *n)
	}
	p, n = seq(goodPass, goodPass+" x")
	if got, err := identity.NewPassphrase(p, cluster); err == nil || got != "" {
		t.Errorf("mismatched entries accepted: %q, %v", got, err)
	}
	if *n != 2 {
		t.Errorf("mismatch: prompted %d times, want 2", *n)
	}
	p, _ = seq("short", "short")
	if got, err := identity.NewPassphrase(p, cluster); err == nil || got != "" {
		t.Errorf("weak passphrase accepted: %q, %v", got, err)
	}
}

// Plan 50 section 10.2 rule 4 and section 9.2: SC_RECOVERY_PASSPHRASE_FILE is accepted for automation; the file's
// trailing newline is not part of the passphrase.
func TestReadPassphraseFile_50AC10(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	if got, err := identity.ReadPassphraseFile(write("a", goodPass+"\n")); err != nil || got != goodPass {
		t.Errorf("trailing newline: %q, %v; want %q", got, err, goodPass)
	}
	if got, err := identity.ReadPassphraseFile(write("b", goodPass)); err != nil || got != goodPass {
		t.Errorf("no newline: %q, %v; want %q", got, err, goodPass)
	}
	if got, err := identity.ReadPassphraseFile(write("c", goodPass+"\r\n")); err != nil || got != goodPass {
		t.Errorf("CRLF: %q, %v; want %q", got, err, goodPass)
	}
	for name, p := range map[string]string{"missing": filepath.Join(dir, "nope"), "empty": write("d", ""), "newline only": write("e", "\n")} {
		got, err := identity.ReadPassphraseFile(p)
		if err == nil {
			t.Errorf("%s: accepted (%q)", name, got)
			continue
		}
		var ie *identity.Error
		if !errors.As(err, &ie) || ie.Code != "PASSPHRASE_FILE_UNREADABLE" {
			t.Errorf("%s: error %v, want identity.Error PASSPHRASE_FILE_UNREADABLE", name, err)
		}
		if strings.Contains(err.Error(), goodPass) {
			t.Errorf("%s: the error repeats a passphrase", name)
		}
	}
}

// Plan 50 section 10.2 rule 3: every secret value and the passphrase are registered with the redactor before first
// use; a redacted value appears as «redacted:SC-CLASS».
func TestRegisterSecrets_RedactorCoversSecretsAndPassphrase_50AC10(t *testing.T) {
	set := genSet(t)
	red := redact.New()
	identity.RegisterSecrets(red, set, goodPass)
	text := "passphrase=" + goodPass + "\n"
	for _, s := range set.Secrets {
		text += "value " + string(s.Value) + "\n"
	}
	out := red.String(text)
	if strings.Contains(out, goodPass) {
		t.Error("the passphrase survives the redactor")
	}
	if !strings.Contains(out, "«redacted:SC-RECOVERY-PASSPHRASE»") {
		t.Errorf("the passphrase is not marked «redacted:SC-RECOVERY-PASSPHRASE»: %q", out[:min(len(out), 80)])
	}
	for _, s := range set.Secrets {
		v := strings.TrimSpace(string(s.Value))
		if v != "" && strings.Contains(out, v) {
			t.Errorf("%s/%s survives the redactor", s.Class, s.Instance)
		}
	}
	if !strings.Contains(out, "«redacted:SC-K3S-TOKEN»") {
		t.Error("SC-K3S-TOKEN is not marked «redacted:SC-K3S-TOKEN»")
	}
}
