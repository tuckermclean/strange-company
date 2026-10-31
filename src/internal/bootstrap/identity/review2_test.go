package identity_test

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"regexp"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
	"go.yaml.in/yaml/v3"

	"strange.company/sc/internal/bootstrap/identity"
	"strange.company/sc/internal/bootstrap/redact"
	"strange.company/sc/internal/clock"
)

// --- RenderSOPS (plan 35 register, section 14, 35-R-3)

var dns1123 = regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$`)

// renderedSecrets renders with the report API and returns each file with its decoded plaintext (the fake encryptor
// leaves the sha256 of the plaintext in the file, which maps a file back to its plaintext).
func renderedSecrets(t *testing.T, set *identity.Set) (*identity.RenderReport, map[string][]byte) {
	t.Helper()
	var calls []encCall
	rep, err := identity.RenderSOPSReport(cluster, set, rcp(set), fakeEnc(&calls))
	if err != nil {
		t.Fatalf("RenderSOPSReport: %v", err)
	}
	if rep == nil {
		t.Fatal("RenderSOPSReport returned no report")
	}
	byHash := map[string][]byte{}
	for _, c := range calls {
		sum := sha256.Sum256(c.plain)
		byHash[hex.EncodeToString(sum[:])] = c.plain
	}
	plain := map[string][]byte{}
	for _, f := range rep.Files {
		for h, p := range byHash {
			if strings.Contains(string(f.Data), h) {
				plain[f.Path] = p
			}
		}
	}
	return rep, plain
}

var nsInPath = regexp.MustCompile(`^gitops/clusters/[^/]+/secrets/([^/]+)/`)

// Plan 35: a Secret stored at gitops/clusters/<c>/secrets/<ns>/... is applied in <ns>; every rendered Secret carries
// that namespace, and a valid DNS-1123 name; no path holds a placeholder or a segment starting with "-".
func TestRenderSOPS_NamespaceMatchesPathAndNamesAreValid_50AC10(t *testing.T) {
	set := genSet(t)
	rep, plain := renderedSecrets(t, set)
	if len(rep.Files) == 0 {
		t.Fatal("no files rendered")
	}
	for _, f := range rep.Files {
		if strings.ContainsAny(f.Path, "<>") {
			t.Errorf("%s holds a placeholder", f.Path)
		}
		for _, seg := range strings.Split(f.Path, "/") {
			if strings.HasPrefix(seg, "-") {
				t.Errorf("%s has the path segment %q starting with -", f.Path, seg)
			}
		}
		if strings.HasPrefix(f.Path, "secrets/offcluster/") {
			continue
		}
		var doc struct {
			Kind     string `yaml:"kind"`
			Metadata struct {
				Name      string `yaml:"name"`
				Namespace string `yaml:"namespace"`
			} `yaml:"metadata"`
		}
		if err := yaml.Unmarshal(plain[f.Path], &doc); err != nil || doc.Kind != "Secret" {
			t.Errorf("%s: plaintext is not a Secret manifest (%v)", f.Path, err)
			continue
		}
		if len(doc.Metadata.Name) > 253 || !dns1123.MatchString(doc.Metadata.Name) {
			t.Errorf("%s: metadata.name %q is not a DNS-1123 subdomain", f.Path, doc.Metadata.Name)
		}
		if m := nsInPath.FindStringSubmatch(f.Path); m != nil && doc.Metadata.Namespace != m[1] {
			t.Errorf("%s: metadata.namespace %q, want %q (the path's namespace)", f.Path, doc.Metadata.Namespace, m[1])
		}
	}
}

// valueProbe is a line of the secret that a rendered plaintext must hold when the secret is rendered.
func valueProbe(v string) string {
	best := ""
	for _, l := range strings.Split(v, "\n") {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "-----") || strings.HasPrefix(l, "#") {
			continue
		}
		if len(l) > len(best) {
			best = l
		}
	}
	return best
}

// Nothing is skipped silently: SC-PG-DR-MARKER's copy with an open <ns> and SC-RUNNER-ADMIN's path outside the two
// cluster directories are reported with class and reason, and every generated secret that has a sops-git or
// offcluster-sops row is either rendered or reported.
func TestRenderSOPS_NoSilentSkips_50AC10(t *testing.T) {
	set := genSet(t)
	rep, plain := renderedSecrets(t, set)
	listed := map[string]identity.Deferred{}
	for _, d := range rep.Deferred {
		if d.Class == "" || strings.TrimSpace(d.Reason) == "" {
			t.Errorf("deferred entry %+v lacks a class or a reason", d)
		}
		listed[d.Class] = d
	}
	for _, class := range []string{"SC-PG-DR-MARKER", "SC-RUNNER-ADMIN"} {
		if _, ok := listed[class]; !ok {
			t.Errorf("%s is not in the deferred list %v", class, rep.Deferred)
		}
	}
	hasRow := map[string]bool{}
	for _, c := range loadReg(t).Classes {
		for _, st := range c.Storage {
			if st.Kind == "sops-git" || st.Kind == "offcluster-sops" {
				hasRow[c.ID] = true
			}
		}
	}
	for _, s := range set.Secrets {
		if !hasRow[s.Class] {
			continue
		}
		if _, ok := listed[s.Class]; ok {
			continue
		}
		probe := valueProbe(string(s.Value))
		found := false
		for _, p := range plain {
			if probe != "" && strings.Contains(string(p), probe) {
				found = true
			}
		}
		if !found {
			t.Errorf("%s/%s has a sops-git row but is neither rendered nor reported", s.Class, s.Instance)
		}
	}
	// The report's files are the files RenderSOPS returns.
	var calls []encCall
	files, err := identity.RenderSOPS(cluster, set, rcp(set), fakeEnc(&calls))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != len(rep.Files) {
		t.Errorf("RenderSOPS returned %d files, the report %d", len(files), len(rep.Files))
	}
}

// --- internal CA (plan 10 section 8.2 "Internal CA root" and section 13)

func checkCA(t *testing.T, name string, pemBytes []byte, wantCN string, notBefore *time.Time) {
	t.Helper()
	var cert *x509.Certificate
	var sawKey bool
	rest := pemBytes
	for {
		var b *pem.Block
		b, rest = pem.Decode(rest)
		if b == nil {
			break
		}
		if b.Type == "CERTIFICATE" && cert == nil {
			c, err := x509.ParseCertificate(b.Bytes)
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			cert = c
		} else if strings.HasSuffix(b.Type, "PRIVATE KEY") {
			sawKey = true
		}
	}
	if cert == nil || !sawKey {
		t.Fatalf("%s: no certificate and private key in the PEM (%d bytes)", name, len(pemBytes))
	}
	if pub, ok := cert.PublicKey.(*ecdsa.PublicKey); !ok || pub.Curve != elliptic.P384() {
		t.Errorf("%s: key is %T %v, want ECDSA P-384", name, cert.PublicKey, cert.PublicKey)
	}
	if !cert.IsCA || !cert.BasicConstraintsValid || cert.MaxPathLen != 0 || !cert.MaxPathLenZero {
		t.Errorf("%s: want CA:TRUE pathlen:0, got IsCA=%v valid=%v MaxPathLen=%d zero=%v", name, cert.IsCA, cert.BasicConstraintsValid, cert.MaxPathLen, cert.MaxPathLenZero)
	}
	if cert.KeyUsage != x509.KeyUsageCertSign|x509.KeyUsageCRLSign {
		t.Errorf("%s: KeyUsage %v, want exactly CertSign|CRLSign", name, cert.KeyUsage)
	}
	for _, e := range cert.Extensions {
		switch e.Id.String() {
		case "2.5.29.19", "2.5.29.15":
			if !e.Critical {
				t.Errorf("%s: extension %s is not critical", name, e.Id)
			}
		}
	}
	if cert.Subject.CommonName != wantCN {
		t.Errorf("%s: CN %q, want %q", name, cert.Subject.CommonName, wantCN)
	}
	if want := cert.NotBefore.AddDate(10, 0, 0); !cert.NotAfter.Equal(want) {
		t.Errorf("%s: validity %s to %s, want exactly 10 years", name, cert.NotBefore, cert.NotAfter)
	}
	if notBefore != nil && !cert.NotBefore.Equal(*notBefore) {
		t.Errorf("%s: NotBefore %s, want the injected clock's %s", name, cert.NotBefore, notBefore)
	}
}

// Plan 10 section 8.2: the CA's time comes from the injected clock (no wall clock).
func TestGenerateInternalCA_ProfileAndInjectedClock_50AC10(t *testing.T) {
	now := time.Date(2026, 10, 27, 9, 30, 0, 0, time.UTC)
	for _, gen := range []int{1, 3} {
		out, err := identity.GenerateInternalCA(clock.NewFake(now), cluster, gen)
		if err != nil {
			t.Fatalf("GenerateInternalCA: %v", err)
		}
		checkCA(t, "GenerateInternalCA", out, "Strange Company internal CA "+cluster+" g"+string(rune('0'+gen)), &now)
	}
}

// The CA that Generate puts into the set follows the same profile (generation 1).
func TestGenerate_InternalCAProfile_50AC10(t *testing.T) {
	s := genSet(t).Find("SC-INTERNAL-CA", "")
	if s == nil {
		t.Fatal("no SC-INTERNAL-CA in the generated set")
	}
	checkCA(t, "Generate", s.Value, "Strange Company internal CA "+cluster+" g1", nil)
}

// --- .sops.yaml (plan 35 section 14.2, plan 18)

type fullRule struct {
	PathRegex      string `yaml:"path_regex"`
	EncryptedRegex string `yaml:"encrypted_regex"`
	Age            string `yaml:"age"`
}

func fullRules(t *testing.T, b []byte) []fullRule {
	t.Helper()
	var doc struct {
		Rules []fullRule `yaml:"creation_rules"`
	}
	if err := yaml.Unmarshal(b, &doc); err != nil {
		t.Fatalf(".sops.yaml is not YAML: %v\n%s", err, b)
	}
	return doc.Rules
}

// Plan 35 section 14.2: the cluster's gitops rule encrypts only data and stringData; the off-cluster rule is anchored,
// restricted to [a-z0-9.-] names and has no encrypted_regex (every value is encrypted). (The gitops path_regex
// anchoring of plan 35 is not pinned here: RED 1's TestSOPSRules_AddsClusterRuleIdempotently_50AC10 asserts the old
// unanchored text; see the handoff.)
func TestSOPSRules_EncryptedRegexAndOffclusterAnchor_50AC10(t *testing.T) {
	set := genSet(t)
	out, err := identity.SOPSRules(cluster, rcp(set), nil)
	if err != nil {
		t.Fatal(err)
	}
	var gitops, off *fullRule
	for _, r := range fullRules(t, out) {
		r := r
		switch {
		case strings.Contains(r.PathRegex, "gitops/clusters/"+cluster+"/"):
			gitops = &r
		case strings.Contains(r.PathRegex, "secrets/offcluster/"+cluster+"/"):
			off = &r
		}
	}
	if gitops == nil || off == nil {
		t.Fatalf("rules missing:\n%s", out)
	}
	if gitops.EncryptedRegex != `^(data|stringData)$` {
		t.Errorf("gitops rule encrypted_regex %q, want ^(data|stringData)$", gitops.EncryptedRegex)
	}
	if want := `^secrets/offcluster/` + cluster + `/[a-z0-9.-]+\.sops\.yaml$`; off.PathRegex != want {
		t.Errorf("off-cluster path_regex %q, want %q", off.PathRegex, want)
	}
	if off.EncryptedRegex != "" {
		t.Errorf("off-cluster rule has encrypted_regex %q, want none", off.EncryptedRegex)
	}
}

// Plan 35 section 14.2: first matching rule wins, so new per-cluster rules go before an existing catch-all.
func TestSOPSRules_NewRulesComeBeforeExistingCatchAll_50AC10(t *testing.T) {
	set := genSet(t)
	existing := []byte("creation_rules:\n  - path_regex: 'gitops/clusters/other/.*\\.sops\\.yaml$'\n    age: age1aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n" +
		"  - path_regex: '.*\\.sops\\.yaml$'\n    age: age1bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\n")
	out, err := identity.SOPSRules(cluster, rcp(set), existing)
	if err != nil {
		t.Fatal(err)
	}
	rules := fullRules(t, out)
	catchAll, gitops, off := -1, -1, -1
	for i, r := range rules {
		switch {
		case r.PathRegex == `.*\.sops\.yaml$`:
			catchAll = i
		case strings.Contains(r.PathRegex, "gitops/clusters/"+cluster+"/"):
			gitops = i
		case strings.Contains(r.PathRegex, "secrets/offcluster/"+cluster+"/"):
			off = i
		}
	}
	if catchAll < 0 || gitops < 0 || off < 0 {
		t.Fatalf("rules lost or missing (catch-all %d, gitops %d, off-cluster %d):\n%s", catchAll, gitops, off, out)
	}
	if gitops > catchAll || off > catchAll {
		t.Errorf("the cluster rules (%d, %d) come after the catch-all (%d); the first match would win:\n%s", gitops, off, catchAll, out)
	}
	again, err := identity.SOPSRules(cluster, rcp(set), out)
	if err != nil || !bytes.Equal(again, out) {
		t.Errorf("a second application changed .sops.yaml (%v)", err)
	}
}

// --- redaction of JSON secrets (plan 50 section 10.2 rule 3)

func TestRegisterSecrets_JSONStringValuesAreRedactedAlone_50AC10(t *testing.T) {
	set := genSet(t)
	red := redact.New()
	identity.RegisterSecrets(red, set, goodPass)
	checked := 0
	for _, s := range set.Secrets {
		var m map[string]any
		if json.Unmarshal(s.Value, &m) != nil {
			continue
		}
		for k, v := range m {
			str, ok := v.(string)
			if !ok || len(str) < 16 {
				continue
			}
			checked++
			out := red.String("login failed, token=" + str + " retry\n")
			if strings.Contains(out, str) {
				t.Errorf("%s/%s: the JSON value of %q survives the redactor on its own", s.Class, s.Instance, k)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no JSON secret values in the set")
	}
}

// --- bundle-confirm fails closed without a digest (plan 50 section 14.3 step 6)

// Invented code BUNDLE_SHA_MISSING (exit 8): an uploaded bundle whose journal record has no sha256 cannot be
// verified, so it is never opened.
func TestBundleConfirm_EmptyJournalDigestFailsClosed_50AC10(t *testing.T) {
	_, sealed := sealedV1(t)
	v := newConfirmEnv(t)
	dl := 0
	r := v.runConfirm([]string{goodPass}, downloadOf(sealed, &dl), "")
	if r.res.Exit != 8 {
		t.Errorf("exit %d (%s %s), want 8", r.res.Exit, r.res.Code, r.res.Message)
	}
	if r.res.Code != "BUNDLE_SHA_MISSING" {
		t.Errorf("code %q, want BUNDLE_SHA_MISSING", r.res.Code)
	}
	if r.prompts != 0 {
		t.Errorf("prompted %d times: the bundle was opened without a digest to verify it against", r.prompts)
	}
	if got := v.bundleRecord()["status"]; got != "uploaded" {
		t.Errorf("recovery_bundle.status %v, want uploaded", got)
	}
	if n := r.provision.count(); n != 0 {
		t.Errorf("provision ran %d times, want 0", n)
	}
}

// --- Open reads at most the limit and refuses more

// A bundle whose archive is followed by more than 64 MiB of padding is not read silently truncated: BUNDLE_CORRUPT.
// The bundle is built at scrypt work factor 10 around the plaintext archive of the shared sealed bundle.
func TestOpen_PayloadLargerThanReadLimitIsCorrupt_50AC10(t *testing.T) {
	_, sealed := sealedV1(t)
	id, err := age.NewScryptIdentity(goodPass)
	if err != nil {
		t.Fatal(err)
	}
	r, err := age.Decrypt(bytes.NewReader(sealed.Data), id)
	if err != nil {
		t.Fatal(err)
	}
	archive, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	rcpt, err := age.NewScryptRecipient(goodPass)
	if err != nil {
		t.Fatal(err)
	}
	rcpt.SetWorkFactor(10)
	w, err := age.Encrypt(&buf, rcpt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(archive); err != nil {
		t.Fatal(err)
	}
	zeros := make([]byte, 1<<20)
	for i := 0; i < 65; i++ {
		if _, err := w.Write(zeros); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	o, err := identity.Open(buf.Bytes(), goodPass)
	var ie *identity.Error
	if err == nil || !errors.As(err, &ie) || ie.Code != "BUNDLE_CORRUPT" {
		t.Errorf("Open of an over-limit payload returned (%v, %v), want BUNDLE_CORRUPT", o != nil, err)
	}
}
