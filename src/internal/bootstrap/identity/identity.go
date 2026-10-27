// Package identity is plan.install-identity of plan 50 section 14.3: secret generation per plan 35 class, the
// passphrase policy and handling, the age-encrypted recovery bundle and its manifest, the SOPS and cluster-vars
// render, and the plan.bundle-confirm step.
package identity

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"filippo.io/age"
	"go.yaml.in/yaml/v3"
	"golang.org/x/crypto/ssh"

	"strange.company/sc/internal/bootstrap/redact"
)

// Error is a refusal with a stable code.
type Error struct {
	Code    string
	Message string
}

// Error implements error.
func (e *Error) Error() string { return e.Code + ": " + e.Message }

func fail(code, format string, a ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, a...)}
}

// Class is one row of contracts/secret-classes.yaml.
type Class struct {
	ID               string
	Actor            string
	Format           string
	InRecoveryBundle bool
	Instances        []Instance
	Storage          []Storage
}

// Instance is a registered instance of a class. InRecoveryBundle is the effective value: the instance's own when the
// register sets one, the class's otherwise.
type Instance struct {
	Name             string
	InRecoveryBundle bool
}

// Storage is one storage row of a class (the kinds this package renders: sops-git, offcluster-sops, k8s-secret).
type Storage struct {
	Kind      string
	Path      string
	Namespace string
	Name      string
	Keys      []string
}

// Register is the parsed secret-class register.
type Register struct {
	Classes []Class
}

// LoadRegister parses contracts/secret-classes.yaml.
func LoadRegister(data []byte) (*Register, error) {
	var doc struct {
		Spec struct {
			Classes []struct {
				ID               string `yaml:"id"`
				Format           string `yaml:"format"`
				InRecoveryBundle bool   `yaml:"inRecoveryBundle"`
				Creator          struct {
					Actor string `yaml:"actor"`
				} `yaml:"creator"`
				Instances []struct {
					Name             string `yaml:"name"`
					InRecoveryBundle *bool  `yaml:"inRecoveryBundle"`
				} `yaml:"instances"`
				Storage []struct {
					Kind      string   `yaml:"kind"`
					Path      string   `yaml:"path"`
					Namespace string   `yaml:"namespace"`
					Name      string   `yaml:"name"`
					Keys      []string `yaml:"keys"`
				} `yaml:"storage"`
			} `yaml:"classes"`
		} `yaml:"spec"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fail("REGISTER_INVALID", "the secret-class register is not YAML: %v", err)
	}
	if len(doc.Spec.Classes) == 0 {
		return nil, fail("REGISTER_INVALID", "the secret-class register has no classes")
	}
	reg := &Register{}
	for _, c := range doc.Spec.Classes {
		cl := Class{ID: c.ID, Actor: c.Creator.Actor, Format: c.Format, InRecoveryBundle: c.InRecoveryBundle}
		for _, i := range c.Instances {
			in := Instance{Name: i.Name, InRecoveryBundle: c.InRecoveryBundle}
			if i.InRecoveryBundle != nil {
				in.InRecoveryBundle = *i.InRecoveryBundle
			}
			cl.Instances = append(cl.Instances, in)
		}
		for _, s := range c.Storage {
			cl.Storage = append(cl.Storage, Storage{Kind: s.Kind, Path: s.Path, Namespace: s.Namespace, Name: s.Name, Keys: s.Keys})
		}
		reg.Classes = append(reg.Classes, cl)
	}
	return reg, nil
}

// generatedByInstall reports whether plan 35 assigns the class to plan.install-identity: its creator is
// plan.install-identity or a pre-seed, or it is the internal CA (`secrets gen internal-ca`).
func generatedByInstall(c Class) bool {
	a := strings.ToLower(c.Actor)
	return strings.Contains(a, "plan.install-identity") || strings.Contains(a, "pre-seed") || c.ID == "SC-INTERNAL-CA"
}

// Secret is one generated or supplied secret value.
type Secret struct {
	Class    string
	Instance string
	Format   string
	Value    []byte
	InBundle bool
}

// Set is everything plan.install-identity generates.
type Set struct {
	InstallUID        string
	ClusterRecipient  string
	RecoveryRecipient string
	Secrets           []Secret
	// Register is the register the set was generated from (RenderSOPS reads the storage paths from it).
	Register *Register
}

// Find returns the secret of class and instance, or nil.
func (s *Set) Find(class, instance string) *Secret {
	for i := range s.Secrets {
		if s.Secrets[i].Class == class && s.Secrets[i].Instance == instance {
			return &s.Secrets[i]
		}
	}
	return nil
}

func randBytes(n int) ([]byte, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return nil, fail("RANDOM_FAILED", "the system random source failed: %v", err)
	}
	return b, nil
}

func newUUIDv4() (string, error) {
	b, err := randBytes(16)
	if err != nil {
		return "", err
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:], nil
}

// bundleFormat maps a register format to a manifest entry format (contracts/recovery-bundle.schema.yaml).
func bundleFormat(registerFormat string) string {
	switch registerFormat {
	case "age-identity":
		return "age-identity"
	case "ssh-keypair":
		return "ssh-private-key"
	case "x509-ca", "x509-client-cert":
		return "pem"
	case "kubeconfig":
		return "kubeconfig"
	case "oauth-client", "app-secret-set", "jwt-signing-key", "cosign-keypair":
		return "json"
	default:
		return "text"
	}
}

// Generate creates the install UUID and every secret plan 35 marks as created by plan.install-identity. It reads no
// clock: the internal CA is valid from a fixed epoch for a century, so no wall time enters the tested code.
func Generate(reg *Register, cluster string) (*Set, error) {
	if reg == nil {
		return nil, fail("REGISTER_INVALID", "no secret-class register")
	}
	uid, err := newUUIDv4()
	if err != nil {
		return nil, err
	}
	set := &Set{InstallUID: uid, Register: reg}
	classes := append([]Class(nil), reg.Classes...)
	sort.SliceStable(classes, func(i, j int) bool { return classes[i].ID < classes[j].ID })
	for _, c := range classes {
		if !generatedByInstall(c) {
			continue
		}
		type target struct {
			name     string
			inBundle bool
		}
		targets := []target{{"", c.InRecoveryBundle}}
		if len(c.Instances) > 0 {
			targets = nil
			for _, in := range c.Instances {
				// Only the break-glass instance exists before provision; operators join later.
				if c.ID == "SC-RUNNER-ADMIN" && in.Name != "breakglass" {
					continue
				}
				targets = append(targets, target{in.Name, in.InRecoveryBundle})
			}
		}
		for _, t := range targets {
			v, err := generateValue(c)
			if err != nil {
				return nil, err
			}
			set.Secrets = append(set.Secrets, Secret{Class: c.ID, Instance: t.name, Format: bundleFormat(c.Format), Value: v, InBundle: t.inBundle})
		}
	}
	if s := set.Find("SC-AGE-CLUSTER", ""); s != nil {
		set.ClusterRecipient = publicKeyOf(s.Value)
	}
	if s := set.Find("SC-AGE-RECOVERY", ""); s != nil {
		set.RecoveryRecipient = publicKeyOf(s.Value)
	}
	if set.ClusterRecipient == "" || set.RecoveryRecipient == "" {
		return nil, fail("REGISTER_INVALID", "the register assigns no SC-AGE-CLUSTER or SC-AGE-RECOVERY to plan.install-identity")
	}
	return set, nil
}

// publicKeyOf reads the recipient from an age-keygen file.
func publicKeyOf(file []byte) string {
	for _, l := range strings.Split(string(file), "\n") {
		if r, ok := strings.CutPrefix(l, "# public key: "); ok {
			return strings.TrimSpace(r)
		}
	}
	return ""
}

func generateValue(c Class) ([]byte, error) {
	switch {
	case c.ID == "SC-K3S-TOKEN":
		b, err := randBytes(32)
		return []byte(hex.EncodeToString(b)), err
	case c.ID == "SC-TOFU-ENCRYPTION" || c.ID == "SC-FORGEJO-METRICS-TOKEN" || c.ID == "SC-CI-INTENTS" || c.ID == "SC-WOODPECKER-METRICS-TOKEN":
		b, err := randBytes(32)
		return []byte(base64.RawURLEncoding.EncodeToString(b)), err
	case c.ID == "SC-AUTHENTIK-SECRET-KEY":
		b, err := randBytes(50)
		return []byte(base64.StdEncoding.EncodeToString(b)), err
	case c.ID == "SC-AUTHENTIK-AUDIT-TOKEN":
		b, err := randBytes(32)
		return []byte(hex.EncodeToString(b)), err
	case c.ID == "SC-FORGEJO-APP-SECRETS":
		return jsonSecrets("secret_key", "internal_token", "lfs_jwt_secret", "oauth2_jwt_secret")
	case c.ID == "SC-OIDC-CLIENTS":
		return jsonSecrets("client_id", "client_secret")
	case c.Format == "age-identity":
		return generateAge()
	case c.Format == "x509-ca":
		return generateCA(c.ID)
	case c.Format == "ssh-keypair":
		return generateSSH()
	default:
		b, err := randBytes(32)
		return []byte(base64.RawURLEncoding.EncodeToString(b)), err
	}
}

func jsonSecrets(keys ...string) ([]byte, error) {
	var sb strings.Builder
	sb.WriteString("{")
	for i, k := range keys {
		b, err := randBytes(48)
		if err != nil {
			return nil, err
		}
		if i > 0 {
			sb.WriteString(",")
		}
		fmt.Fprintf(&sb, "%q:%q", k, base64.RawURLEncoding.EncodeToString(b))
	}
	sb.WriteString("}\n")
	return []byte(sb.String()), nil
}

// generateAge returns an age-keygen file (without the creation-time comment: Generate reads no clock).
func generateAge() ([]byte, error) {
	id, err := age.GenerateX25519Identity()
	if err != nil {
		return nil, fail("AGE_KEYGEN_FAILED", "age key generation failed: %v", err)
	}
	return []byte("# public key: " + id.Recipient().String() + "\n" + id.String() + "\n"), nil
}

// generateCA returns an ECDSA P-256 root certificate and its key as PEM (certificate first).
func generateCA(class string) ([]byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fail("CA_KEYGEN_FAILED", "CA key generation failed: %v", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		return nil, fail("RANDOM_FAILED", "the system random source failed: %v", err)
	}
	tpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "Strange Company internal CA", Organization: []string{"Strange Company"}},
		NotBefore:             time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC),
		NotAfter:              time.Date(2120, 1, 1, 0, 0, 0, 0, time.UTC),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		return nil, fail("CA_KEYGEN_FAILED", "%s certificate creation failed: %v", class, err)
	}
	kb, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, fail("CA_KEYGEN_FAILED", "CA key encoding failed: %v", err)
	}
	return append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb})...), nil
}

// generateSSH returns an ed25519 private key in OpenSSH format.
func generateSSH() ([]byte, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fail("SSH_KEYGEN_FAILED", "SSH key generation failed: %v", err)
	}
	blk, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		return nil, fail("SSH_KEYGEN_FAILED", "SSH key encoding failed: %v", err)
	}
	return pem.EncodeToMemory(blk), nil
}

// CheckPassphrase applies the passphrase policy of plan 50 section 14.3: at least 20 characters or at least 5
// space-separated words, no leading or trailing whitespace, and not containing the cluster name. The error never
// repeats the passphrase.
func CheckPassphrase(pass, cluster string) error {
	if pass == "" {
		return fail("PASSPHRASE_TOO_SHORT", "the recovery passphrase is empty")
	}
	first, _ := utf8.DecodeRuneInString(pass)
	last, _ := utf8.DecodeLastRuneInString(pass)
	if unicode.IsSpace(first) || unicode.IsSpace(last) {
		return fail("PASSPHRASE_WHITESPACE", "the recovery passphrase has leading or trailing whitespace")
	}
	if cluster != "" && strings.Contains(strings.ToLower(pass), strings.ToLower(cluster)) {
		return fail("PASSPHRASE_CONTAINS_CLUSTER", "the recovery passphrase must not contain the cluster name")
	}
	if utf8.RuneCountInString(pass) < 20 && len(strings.Fields(pass)) < 5 {
		return fail("PASSPHRASE_TOO_SHORT", "the recovery passphrase needs at least 20 characters or 5 words")
	}
	return nil
}

// ReadPassphraseFile reads SC_RECOVERY_PASSPHRASE_FILE; the trailing line break is not part of the passphrase.
func ReadPassphraseFile(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fail("PASSPHRASE_FILE_UNREADABLE", "the passphrase file cannot be read: %v", err)
	}
	p := strings.TrimRight(string(b), "\r\n")
	if p == "" {
		return "", fail("PASSPHRASE_FILE_UNREADABLE", "the passphrase file %s is empty", path)
	}
	return p, nil
}

// NewPassphrase prompts twice, requires both entries to match and applies the policy.
func NewPassphrase(prompt func() (string, error), cluster string) (string, error) {
	a, err := prompt()
	if err != nil {
		return "", err
	}
	b, err := prompt()
	if err != nil {
		return "", err
	}
	if a != b {
		return "", fail("PASSPHRASE_MISMATCH", "the two passphrase entries differ")
	}
	if err := CheckPassphrase(a, cluster); err != nil {
		return "", err
	}
	return a, nil
}

// RegisterSecrets registers every secret value and the passphrase with the redactor. A multi-line value is also
// registered line by line, so a log line that carries part of a key file is still redacted.
func RegisterSecrets(red *redact.Redactor, set *Set, passphrase string) {
	if passphrase != "" {
		red.Register("SC-RECOVERY-PASSPHRASE", passphrase)
	}
	if set == nil {
		return
	}
	for _, s := range set.Secrets {
		v := string(s.Value)
		red.Register(s.Class, v)
		if t := strings.TrimSpace(v); t != v {
			red.Register(s.Class, t)
		}
		for _, l := range strings.Split(v, "\n") {
			l = strings.TrimSpace(l)
			if len(l) >= 12 && !strings.HasPrefix(l, "-----") && !strings.HasPrefix(l, "#") {
				red.Register(s.Class, l)
			}
		}
	}
}
