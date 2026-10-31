package identity_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/netip"
	"sort"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"strange.company/sc/internal/bootstrap/identity"
	"strange.company/sc/internal/bootstrap/preflight"
)

// --- SOPS render

type encCall struct {
	plain      []byte
	recipients []string
}

// fakeEnc stands in for sops: it records what it was given and returns a ciphertext that holds no plaintext.
func fakeEnc(calls *[]encCall) identity.Encryptor {
	return func(plain []byte, recipients []string) ([]byte, error) {
		*calls = append(*calls, encCall{append([]byte(nil), plain...), append([]string(nil), recipients...)})
		sum := sha256.Sum256(plain)
		return []byte("sops:\n  age: [" + strings.Join(recipients, ",") + "]\n  mac: " + hex.EncodeToString(sum[:]) + "\n"), nil
	}
}

func rcp(set *identity.Set) identity.Recipients {
	return identity.Recipients{Cluster: set.ClusterRecipient, Recovery: set.RecoveryRecipient}
}

// Plan 50 section 14.3 step 1 and plan 35 35-R-3: in-cluster SOPS files under gitops/clusters/<cluster>/ are
// encrypted to both recipients, off-cluster files under secrets/offcluster/<cluster>/ to the recovery recipient only.
func TestRenderSOPS_TwoTiersAndRecipients_50AC10(t *testing.T) {
	set := genSet(t)
	var calls []encCall
	files, err := identity.RenderSOPS(cluster, set, rcp(set), fakeEnc(&calls))
	if err != nil {
		t.Fatalf("RenderSOPS: %v", err)
	}
	byPath := map[string]identity.File{}
	for _, f := range files {
		byPath[f.Path] = f
		if !strings.HasSuffix(f.Path, ".sops.yaml") {
			t.Errorf("%s does not end in .sops.yaml", f.Path)
		}
		inCluster := strings.HasPrefix(f.Path, "gitops/clusters/"+cluster+"/")
		offCluster := strings.HasPrefix(f.Path, "secrets/offcluster/"+cluster+"/")
		if !inCluster && !offCluster {
			t.Errorf("%s is outside gitops/clusters/%s/ and secrets/offcluster/%s/", f.Path, cluster, cluster)
		}
		hdr := string(f.Data)
		hasCluster, hasRecovery := strings.Contains(hdr, set.ClusterRecipient), strings.Contains(hdr, set.RecoveryRecipient)
		switch {
		case inCluster && !(hasCluster && hasRecovery):
			t.Errorf("%s is not encrypted to both recipients", f.Path)
		case offCluster && (hasCluster || !hasRecovery):
			t.Errorf("%s must be encrypted to the recovery recipient only", f.Path)
		}
	}
	for _, p := range []string{
		"gitops/clusters/lab-one/forgejo/forgejo-admin.sops.yaml",
		"gitops/clusters/lab-one/authentik/authentik-secret-key.sops.yaml",
		"gitops/clusters/lab-one/secrets/monitoring/grafana-admin.sops.yaml",
		"secrets/offcluster/lab-one/k3s-token.sops.yaml",
		"secrets/offcluster/lab-one/tofu-encryption.sops.yaml",
		"secrets/offcluster/lab-one/break-glass-cluster.sops.yaml",
		"secrets/offcluster/lab-one/synthetic-user.sops.yaml",
	} {
		if _, ok := byPath[p]; !ok {
			t.Errorf("missing %s (plan 35 storage paths)", p)
		}
	}
	sorted := make([]string, 0, len(files))
	for _, f := range files {
		sorted = append(sorted, f.Path)
	}
	if !sort.StringsAreSorted(sorted) {
		t.Error("files are not returned in path order (a re-render must be deterministic)")
	}
}

// The plaintext handed to the encryptor is a Kubernetes Secret manifest for in-cluster files and holds the value;
// the returned files hold no plaintext secret value (35-R-3: ciphertext only in Git).
func TestRenderSOPS_PlaintextOnlyInsideEncryptor_50AC10(t *testing.T) {
	set := genSet(t)
	var calls []encCall
	files, err := identity.RenderSOPS(cluster, set, rcp(set), fakeEnc(&calls))
	if err != nil {
		t.Fatalf("RenderSOPS: %v", err)
	}
	if len(calls) != len(files) || len(files) == 0 {
		t.Fatalf("encryptor calls %d, files %d, want one call per file", len(calls), len(files))
	}
	admin := set.Find("SC-FORGEJO-ADMIN", "")
	var sawAdmin bool
	for _, c := range calls {
		var doc map[string]any
		if err := yaml.Unmarshal(c.plain, &doc); err != nil {
			t.Fatalf("plaintext is not YAML: %v", err)
		}
		if bytes.Contains(c.plain, []byte(goodPass)) {
			t.Error("the passphrase reached a SOPS plaintext")
		}
		if doc["kind"] == "Secret" && admin != nil && strings.Contains(string(c.plain), strings.TrimSpace(string(admin.Value))) {
			sawAdmin = true
		}
	}
	if !sawAdmin {
		t.Error("no Secret manifest carrying the SC-FORGEJO-ADMIN value was encrypted")
	}
	for _, f := range files {
		for _, s := range set.Secrets {
			if v := bytes.TrimSpace(s.Value); len(v) > 0 && bytes.Contains(f.Data, v) {
				t.Errorf("%s contains the plaintext of %s/%s", f.Path, s.Class, s.Instance)
			}
		}
	}
}

// A broken encryptor that returns its input must not leak: the render refuses (and returns no files).
func TestRenderSOPS_RefusesPlaintextOutputAndEncryptorErrors_50AC10(t *testing.T) {
	set := genSet(t)
	files, err := identity.RenderSOPS(cluster, set, rcp(set), func(plain []byte, _ []string) ([]byte, error) { return plain, nil })
	if err == nil || len(files) != 0 {
		t.Errorf("an encryptor returning plaintext was accepted (%d files, %v)", len(files), err)
	}
	boom := errors.New("sops exited 1")
	files, err = identity.RenderSOPS(cluster, set, rcp(set), func([]byte, []string) ([]byte, error) { return nil, boom })
	if err == nil || len(files) != 0 || !errors.Is(err, boom) {
		t.Errorf("an encryptor error was not returned as is (%d files, %v)", len(files), err)
	}
}

type sopsRule struct {
	PathRegex string `yaml:"path_regex"`
	Age       string `yaml:"age"`
}

func parseRules(t testing.TB, b []byte) []sopsRule {
	t.Helper()
	var doc struct {
		Rules []sopsRule `yaml:"creation_rules"`
	}
	if err := yaml.Unmarshal(b, &doc); err != nil {
		t.Fatalf(".sops.yaml is not YAML: %v\n%s", err, b)
	}
	return doc.Rules
}

// Plan 50 section 14.3: add a .sops.yaml creation rule for gitops/clusters/<cluster>/.*\.sops\.yaml$ with both
// recipients; existing rules survive; repeating is idempotent.
func TestSOPSRules_AddsClusterRuleIdempotently_50AC10(t *testing.T) {
	set := genSet(t)
	existing := []byte("creation_rules:\n  - path_regex: 'gitops/clusters/other/.*\\.sops\\.yaml$'\n    age: age1aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n")
	out, err := identity.SOPSRules(cluster, rcp(set), existing)
	if err != nil {
		t.Fatalf("SOPSRules: %v", err)
	}
	rules := parseRules(t, out)
	var mine []sopsRule
	for _, r := range rules {
		if strings.Contains(r.PathRegex, "gitops/clusters/"+cluster+"/") {
			mine = append(mine, r)
		}
	}
	if len(mine) != 1 {
		t.Fatalf("%d rules for the cluster, want 1: %s", len(mine), out)
	}
	if mine[0].PathRegex != `gitops/clusters/lab-one/.*\.sops\.yaml$` {
		t.Errorf("path_regex %q", mine[0].PathRegex)
	}
	got := strings.Split(mine[0].Age, ",")
	sort.Strings(got)
	want := []string{set.ClusterRecipient, set.RecoveryRecipient}
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("rule recipients %v, want both %v", got, want)
	}
	if !strings.Contains(string(out), "gitops/clusters/other/") {
		t.Error("the existing rule for another cluster was dropped")
	}
	again, err := identity.SOPSRules(cluster, rcp(set), out)
	if err != nil || !bytes.Equal(again, out) {
		t.Errorf("a second application changed .sops.yaml (%v)", err)
	}
	fresh, err := identity.SOPSRules(cluster, rcp(set), nil)
	if err != nil || len(parseRules(t, fresh)) == 0 {
		t.Errorf("no .sops.yaml yet: %v %q", err, fresh)
	}
}

// --- cluster-vars render

func renderVars(t testing.TB, in identity.ClusterVarsInput) (map[string]string, error) {
	t.Helper()
	b, err := identity.RenderClusterVars(in)
	if err != nil {
		return nil, err
	}
	var doc struct {
		APIVersion string `yaml:"apiVersion"`
		Kind       string `yaml:"kind"`
		Metadata   struct {
			Name      string `yaml:"name"`
			Namespace string `yaml:"namespace"`
		} `yaml:"metadata"`
		Data map[string]string `yaml:"data"`
	}
	if err := yaml.Unmarshal(b, &doc); err != nil {
		t.Fatalf("cluster-vars is not a ConfigMap of strings: %v\n%s", err, b)
	}
	if doc.APIVersion != "v1" || doc.Kind != "ConfigMap" || doc.Metadata.Name != "cluster-vars" || doc.Metadata.Namespace != "flux-system" {
		t.Errorf("cluster-vars is %s %s %s/%s, want v1 ConfigMap flux-system/cluster-vars", doc.APIVersion, doc.Kind, doc.Metadata.Namespace, doc.Metadata.Name)
	}
	if doc.Data == nil {
		t.Fatalf("cluster-vars has no data:\n%s", b)
	}
	return doc.Data, nil
}

func baseVars() identity.ClusterVarsInput {
	return identity.ClusterVarsInput{
		Vars:        map[string]string{"SC_CLUSTER_NAME": cluster, "BASE_DOMAIN": "example.test", "INGRESS_MODE": "tunnel"},
		ServiceCIDR: "10.43.0.0/16", PodCIDR: "10.42.0.0/16",
		S3Endpoint:   "https://s3.example.test",
		Resolve:      func(string) ([]netip.Addr, error) { return []netip.Addr{netip.MustParseAddr("198.51.100.7")}, nil },
		DNSUpstreams: []string{"192.0.2.53"},
	}
}

// 50-AC-34 (CD-37, PF-30): SC_TRAEFIK_CLUSTER_IP is the 20th address of the service CIDR.
func TestRenderClusterVars_TraefikClusterIP_50AC34(t *testing.T) {
	for cidr, want := range map[string]string{"10.43.0.0/16": "10.43.0.20", "10.96.0.0/12": "10.96.0.20"} {
		in := baseVars()
		in.ServiceCIDR = cidr
		d, err := renderVars(t, in)
		if err != nil {
			t.Errorf("%s: %v", cidr, err)
			continue
		}
		if d["SC_TRAEFIK_CLUSTER_IP"] != want {
			t.Errorf("SERVICE_CIDR=%s: SC_TRAEFIK_CLUSTER_IP=%q, want %s", cidr, d["SC_TRAEFIK_CLUSTER_IP"], want)
		}
		if d["SC_SERVICE_CIDR"] != cidr || d["SC_POD_CIDR"] != "10.42.0.0/16" {
			t.Errorf("SC_SERVICE_CIDR=%q SC_POD_CIDR=%q", d["SC_SERVICE_CIDR"], d["SC_POD_CIDR"])
		}
		for k, v := range baseVars().Vars {
			if d[k] != v {
				t.Errorf("%s=%q, want the supplied %q", k, d[k], v)
			}
		}
	}
}

// 50-AC-34: the render reuses the preflight derivation, so the refusals carry the preflight codes.
func TestRenderClusterVars_ServiceCIDRRefusals_50AC34(t *testing.T) {
	var pe *preflight.Error
	in := baseVars()
	in.ServiceCIDR = "10.43.0.0/27"
	if _, err := renderVars(t, in); !errors.As(err, &pe) || pe.Code != "SERVICE_CIDR_TOO_SMALL" {
		t.Errorf("a /27 service CIDR: %v, want preflight SERVICE_CIDR_TOO_SMALL", err)
	}
	in = baseVars()
	in.PinnedIPs = []netip.Addr{netip.MustParseAddr("10.43.0.20")}
	pe = nil
	if _, err := renderVars(t, in); !errors.As(err, &pe) || pe.Code != "SERVICE_CIDR_IP_COLLISION" {
		t.Errorf("a pinned ClusterIP equal to the Traefik IP: %v, want preflight SERVICE_CIDR_IP_COLLISION", err)
	}
}

// 50-AC-35 (PF-28): a public endpoint on 443 writes neither SC_S3_CIDR nor SC_S3_PORT.
func TestRenderClusterVars_PublicEndpointHasNoS3Egress_50AC35(t *testing.T) {
	d, err := renderVars(t, baseVars())
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"SC_S3_CIDR", "SC_S3_PORT"} {
		if v, ok := d[k]; ok {
			t.Errorf("%s=%q present for a public endpoint on 443", k, v)
		}
	}
}

// 50-AC-35: a private endpoint with S3_PRIVATE_CIDR writes SC_S3_CIDR and SC_S3_PORT; without it the render refuses
// with S3_ENDPOINT_PLACEMENT_UNSUPPORTED.
func TestRenderClusterVars_PrivateEndpointEgress_50AC35(t *testing.T) {
	in := baseVars()
	in.S3Endpoint = "https://10.20.0.5:9000"
	in.S3PrivateCIDR = "10.20.0.0/24"
	d, err := renderVars(t, in)
	if err != nil {
		t.Fatal(err)
	}
	if d["SC_S3_CIDR"] != "10.20.0.0/24" || d["SC_S3_PORT"] != "9000" {
		t.Errorf("SC_S3_CIDR=%q SC_S3_PORT=%q, want 10.20.0.0/24 and 9000", d["SC_S3_CIDR"], d["SC_S3_PORT"])
	}
	in.S3PrivateCIDR = ""
	var pe *preflight.Error
	if _, err := renderVars(t, in); !errors.As(err, &pe) || pe.Code != "S3_ENDPOINT_PLACEMENT_UNSUPPORTED" {
		t.Errorf("private endpoint without S3_PRIVATE_CIDR: %v, want preflight S3_ENDPOINT_PLACEMENT_UNSUPPORTED", err)
	}
}

// Plan 50 section 14.3 / plan 18 section 9.2: SC_DNS_UPSTREAM_1..3 come from the resolvers; unused slots repeat
// slot 1 (the render is seeded with the toolbox resolver, 50-AC-35's discovery case uses two).
func TestRenderClusterVars_DNSUpstreamSlots_50AC35(t *testing.T) {
	cases := []struct {
		in   []string
		want [3]string
	}{
		{[]string{"192.0.2.53"}, [3]string{"192.0.2.53", "192.0.2.53", "192.0.2.53"}},
		{[]string{"192.0.2.53", "192.0.2.54"}, [3]string{"192.0.2.53", "192.0.2.54", "192.0.2.53"}},
		{[]string{"192.0.2.53", "192.0.2.54", "192.0.2.55"}, [3]string{"192.0.2.53", "192.0.2.54", "192.0.2.55"}},
		{[]string{"192.0.2.53", "192.0.2.54", "192.0.2.55", "192.0.2.56"}, [3]string{"192.0.2.53", "192.0.2.54", "192.0.2.55"}},
	}
	for _, c := range cases {
		in := baseVars()
		in.DNSUpstreams = c.in
		d, err := renderVars(t, in)
		if err != nil {
			t.Errorf("%v: %v", c.in, err)
			continue
		}
		got := [3]string{d["SC_DNS_UPSTREAM_1"], d["SC_DNS_UPSTREAM_2"], d["SC_DNS_UPSTREAM_3"]}
		if got != c.want {
			t.Errorf("resolvers %v: slots %v, want %v", c.in, got, c.want)
		}
	}
	in := baseVars()
	in.DNSUpstreams = []string{"not-an-ip"}
	if _, err := identity.RenderClusterVars(in); err == nil {
		t.Error("a resolver that is not an IP address was accepted")
	}
}

// The render is deterministic and holds no SC_NODE_IP_*: provision owns those keys (BOOT-9).
func TestRenderClusterVars_DeterministicAndLeavesNodeIPsToProvision_50AC35(t *testing.T) {
	a, errA := identity.RenderClusterVars(baseVars())
	b, errB := identity.RenderClusterVars(baseVars())
	if errA != nil || errB != nil || len(a) == 0 || !bytes.Equal(a, b) {
		t.Fatalf("two renders differ or failed: %v %v", errA, errB)
	}
	for _, n := range []string{"SC_NODE_IP_1", "SC_NODE_IP_5"} {
		if bytes.Contains(a, []byte(n)) {
			t.Errorf("the install-identity render writes %s", n)
		}
	}
}
