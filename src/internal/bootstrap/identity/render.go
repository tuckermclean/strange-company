package identity

import (
	"bytes"
	"encoding/pem"
	"fmt"
	"net/netip"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"

	"strange.company/sc/internal/bootstrap/preflight"
)

// File is a repository file produced by a render.
type File struct {
	Path string
	Data []byte
}

// Recipients are the two public age recipients.
type Recipients struct {
	Cluster  string
	Recovery string
}

// Encryptor encrypts a plaintext YAML document to the recipients (production: sops).
type Encryptor func(plain []byte, recipients []string) ([]byte, error)

// nsFor resolves the <ns> placeholder of a storage path for the instances plan 35 spreads over namespaces.
func nsFor(class, instance string) string {
	switch class {
	case "SC-OIDC-CLIENTS":
		return map[string]string{"forgejo": "forgejo", "grafana": "monitoring", "hermes-dashboard": "strange-company"}[instance]
	case "SC-PG-APP":
		switch {
		case strings.HasPrefix(instance, "forgejo-"):
			return "forgejo"
		case strings.HasPrefix(instance, "authentik-"):
			return "authentik"
		case strings.HasPrefix(instance, "woodpecker-"):
			return "woodpecker"
		case instance == "sc-pg-grafana-ro":
			return "monitoring"
		case strings.HasPrefix(instance, "sc-pg-"):
			return "strange-company"
		}
	}
	return ""
}

// resolvePath fills the placeholders of a register path. When the path cannot be rendered, reason says why.
func resolvePath(cluster string, s Secret, p string) (resolved, reason string) {
	p = strings.ReplaceAll(p, "{cluster}", cluster)
	p = strings.ReplaceAll(p, "<instance>", s.Instance)
	if strings.Contains(p, "<ns>") {
		if ns := nsFor(s.Class, s.Instance); ns != "" {
			p = strings.ReplaceAll(p, "<ns>", ns)
		}
	}
	if strings.Contains(p, "<cluster>") && s.Instance != "" {
		p = strings.ReplaceAll(p, "<cluster>", s.Instance)
	}
	if strings.ContainsAny(p, "<>") {
		return "", "the storage path has an open placeholder (its namespace or instance is not known before provision)"
	}
	if strings.Contains(p, "//") {
		return "", "the storage path has an empty segment"
	}
	for _, seg := range strings.Split(p, "/") {
		if strings.HasPrefix(seg, "-") || seg == "" {
			return "", "the storage path has a segment that is empty or starts with a hyphen (an instance name is missing)"
		}
	}
	if !strings.HasSuffix(p, ".sops.yaml") {
		return "", "the storage path is not a .sops.yaml file"
	}
	if !strings.HasPrefix(p, "gitops/clusters/"+cluster+"/") && !strings.HasPrefix(p, "secrets/offcluster/"+cluster+"/") {
		return "", "the storage path is outside gitops/clusters/" + cluster + " and secrets/offcluster/" + cluster + ", the directories install-identity may write"
	}
	return p, ""
}

var dns1123 = regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$`)

func validName(n string) bool { return len(n) <= 253 && dns1123.MatchString(n) }

// pathNamespace returns <ns> of gitops/clusters/<cluster>/secrets/<ns>/<file>.
func pathNamespace(cluster, p string) string {
	rest, ok := strings.CutPrefix(p, "gitops/clusters/"+cluster+"/secrets/")
	if !ok {
		return ""
	}
	ns, _, ok := strings.Cut(rest, "/")
	if !ok {
		return ""
	}
	return ns
}

type kv struct{ key, val string }

// keysFor names the keys one secret contributes to a file.
func keysFor(s Secret) []kv {
	v := string(s.Value)
	switch s.Class {
	case "SC-K3S-TOKEN":
		return []kv{{"token", v}}
	case "SC-TOFU-ENCRYPTION":
		return []kv{{"passphrase", v}}
	case "SC-FORGEJO-ADMIN":
		return []kv{{"username", "sc-admin"}, {"password", v}}
	case "SC-AUTHENTIK-SECRET-KEY":
		return []kv{{"secret-key", v}}
	case "SC-AGE-BACKUP":
		return []kv{{"current.agekey", v}}
	case "SC-FLUX-DEPLOY-KEY":
		return []kv{{"identity", v}}
	case "SC-FORGEJO-SSH-HOST-KEY":
		return []kv{{"ssh_host_ed25519_key", v}}
	case "SC-OBJECT-STORAGE":
		if out := objectStorageKeys(v); len(out) > 0 {
			return out
		}
	case "SC-INTERNAL-CA":
		var out []kv
		rest := []byte(v)
		for {
			var b *pem.Block
			b, rest = pem.Decode(rest)
			if b == nil {
				break
			}
			name := "tls.key"
			if b.Type == "CERTIFICATE" {
				name = "tls.crt"
			}
			out = append(out, kv{name, string(pem.EncodeToMemory(b))})
		}
		return out
	}
	if s.Instance != "" {
		return []kv{{s.Instance, v}}
	}
	return []kv{{"value", v}}
}

type fileDoc struct {
	path       string
	offcluster bool
	name, ns   string
	keys       map[string]string
	order      []string
}

// RenderSOPS renders the generated secrets as SOPS files: the in-cluster files under gitops/clusters/<cluster>/ (a
// Kubernetes Secret manifest, encrypted to both recipients) and the off-cluster files under
// secrets/offcluster/<cluster>/ (a plain YAML map, encrypted to the recovery recipient only). Several secrets that
// plan 35 stores in one file share it. The files are returned in path order; the plaintext exists only inside enc.
func RenderSOPS(cluster string, set *Set, rcp Recipients, enc Encryptor) ([]File, error) {
	rep, err := RenderSOPSReport(cluster, set, rcp, enc)
	if err != nil {
		return nil, err
	}
	return rep.Files, nil
}

// Deferred is a generated secret that RenderSOPS did not render, with the reason.
type Deferred struct {
	Class    string
	Instance string
	Path     string
	Reason   string
}

// RenderReport is RenderSOPS's files plus every register secret it left out.
type RenderReport struct {
	Files    []File
	Deferred []Deferred
}

// RenderSOPSReport renders like RenderSOPS and also reports every secret with a sops-git or offcluster-sops row
// that it did not render, so nothing is skipped silently.
func RenderSOPSReport(cluster string, set *Set, rcp Recipients, enc Encryptor) (*RenderReport, error) {
	if set == nil || set.Register == nil {
		return nil, fail("RENDER_INPUT_INVALID", "RenderSOPS needs a set generated from a register")
	}
	classes := map[string]Class{}
	for _, c := range set.Register.Classes {
		classes[c.ID] = c
	}
	secrets := append([]Secret(nil), set.Secrets...)
	sort.SliceStable(secrets, func(i, j int) bool {
		if secrets[i].Class != secrets[j].Class {
			return secrets[i].Class < secrets[j].Class
		}
		return secrets[i].Instance < secrets[j].Instance
	})
	docs := map[string]*fileDoc{}
	rep := &RenderReport{}
	for _, s := range secrets {
		c := classes[s.Class]
		var k8sName, k8sNS string
		// An instance's own storage rows replace the class rows for that instance.
		rows := c.Storage
		instRows := false
		for _, in := range c.Instances {
			if in.Name == s.Instance && len(in.Storage) > 0 {
				rows, instRows = in.Storage, true
			}
		}
		for _, st := range rows {
			if st.Kind == "k8s-secret" && st.Name != "" {
				k8sName = st.Name
				if strings.Contains(k8sName, "<cluster>") {
					if s.Instance == "" {
						k8sName = ""
					} else {
						k8sName = strings.ReplaceAll(k8sName, "<cluster>", s.Instance)
					}
				}
				k8sNS = st.Namespace
				if k8sNS == "" {
					k8sNS = nsFor(s.Class, s.Instance)
				}
				break
			}
		}
		for _, st := range rows {
			if st.Kind != "sops-git" && st.Kind != "offcluster-sops" {
				continue
			}
			p, reason := resolvePath(cluster, s, st.Path)
			if reason == "" && !strings.HasPrefix(p, "secrets/offcluster/") {
				name := k8sName
				if name == "" {
					name = strings.TrimSuffix(p[strings.LastIndex(p, "/")+1:], ".sops.yaml")
				}
				if !validName(name) {
					reason = "the Secret name " + strconv.Quote(name) + " is not a DNS-1123 subdomain"
				}
			}
			if reason != "" {
				path := p
				if path == "" {
					path = st.Path
				}
				rep.Deferred = append(rep.Deferred, Deferred{Class: s.Class, Instance: s.Instance, Path: path, Reason: reason})
				continue
			}
			d := docs[p]
			if d == nil {
				d = &fileDoc{path: p, offcluster: strings.HasPrefix(p, "secrets/offcluster/"), keys: map[string]string{}}
				docs[p] = d
			}
			if d.name == "" {
				d.name, d.ns = k8sName, k8sNS
				if instRows && k8sName == "" {
					d.name = strings.TrimSuffix(p[strings.LastIndex(p, "/")+1:], ".sops.yaml")
				}
				if ns := pathNamespace(cluster, p); ns != "" {
					d.ns = ns
				}
			}
			for _, e := range keysFor(s) {
				key := e.key
				if prev, taken := d.keys[key]; taken && prev != e.val {
					key = strings.ToLower(strings.TrimPrefix(s.Class, "SC-"))
					if s.Instance != "" {
						key += "-" + s.Instance
					}
				}
				if _, seen := d.keys[key]; !seen {
					d.order = append(d.order, key)
				}
				d.keys[key] = e.val
			}
		}
	}
	paths := make([]string, 0, len(docs))
	for p := range docs {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		d := docs[p]
		plain, err := d.plaintext()
		if err != nil {
			return nil, err
		}
		recipients := []string{rcp.Cluster, rcp.Recovery}
		if d.offcluster {
			recipients = []string{rcp.Recovery}
		}
		out, err := enc(plain, recipients)
		if err != nil {
			return nil, fmt.Errorf("encrypting %s: %w", p, err)
		}
		if len(bytes.TrimSpace(out)) == 0 {
			return nil, fail("SOPS_OUTPUT_EMPTY", "the encryptor returned nothing for %s", p)
		}
		for _, v := range d.keys {
			if t := strings.TrimSpace(v); len(t) >= 8 && bytes.Contains(out, []byte(t)) {
				return nil, fail("SOPS_PLAINTEXT_OUTPUT", "the encryptor output for %s holds a plaintext secret value", p)
			}
		}
		rep.Files = append(rep.Files, File{Path: p, Data: out})
	}
	return rep, nil
}

func (d *fileDoc) plaintext() ([]byte, error) {
	sort.Strings(d.order)
	data := &yaml.Node{Kind: yaml.MappingNode}
	for _, k := range d.order {
		data.Content = append(data.Content, scalar(k), scalar(d.keys[k]))
	}
	root := &yaml.Node{Kind: yaml.MappingNode}
	if d.offcluster {
		root = data
	} else {
		name := d.name
		if name == "" {
			name = strings.TrimSuffix(d.path[strings.LastIndex(d.path, "/")+1:], ".sops.yaml")
		}
		meta := &yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{scalar("name"), scalar(name)}}
		if d.ns != "" {
			meta.Content = append(meta.Content, scalar("namespace"), scalar(d.ns))
		}
		root.Content = []*yaml.Node{
			scalar("apiVersion"), scalar("v1"), scalar("kind"), scalar("Secret"), scalar("metadata"), meta,
			scalar("type"), scalar("Opaque"), scalar("stringData"), data,
		}
	}
	return marshal(root)
}

func scalar(s string) *yaml.Node {
	n := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: s}
	if strings.Contains(s, "\n") {
		n.Style = yaml.LiteralStyle
	}
	return n
}

func marshal(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(v); err != nil {
		return nil, fail("RENDER_FAILED", "YAML encoding failed: %v", err)
	}
	if err := enc.Close(); err != nil {
		return nil, fail("RENDER_FAILED", "YAML encoding failed: %v", err)
	}
	return buf.Bytes(), nil
}

// SOPSRules adds the creation rules of the cluster to an existing .sops.yaml: gitops/clusters/<cluster>/ to both
// recipients and secrets/offcluster/<cluster>/ to the recovery recipient only. Rules of other clusters survive and
// repeating the call changes nothing.
func SOPSRules(cluster string, rcp Recipients, existing []byte) ([]byte, error) {
	doc := map[string]any{}
	if len(bytes.TrimSpace(existing)) > 0 {
		if err := yaml.Unmarshal(existing, &doc); err != nil {
			return nil, fail("SOPS_CONFIG_INVALID", ".sops.yaml is not YAML: %v", err)
		}
		if doc == nil {
			doc = map[string]any{}
		}
	}
	var rules []any
	switch r := doc["creation_rules"].(type) {
	case nil:
	case []any:
		rules = r
	default:
		return nil, fail("SOPS_CONFIG_INVALID", ".sops.yaml creation_rules is not a list")
	}
	want := []map[string]any{
		{"path_regex": `gitops/clusters/` + cluster + `/.*\.sops\.yaml$`, "encrypted_regex": `^(data|stringData)$`, "age": rcp.Cluster + "," + rcp.Recovery},
		{"path_regex": `^secrets/offcluster/` + cluster + `/[a-z0-9.-]+\.sops\.yaml$`, "age": rcp.Recovery},
	}
	// The first matching rule wins, so a rule that is new goes before the first catch-all rule (one that matches any
	// .sops.yaml) or, without one, at the end.
	for _, w := range want {
		replaced := false
		for i, r := range rules {
			if m, ok := r.(map[string]any); ok && m["path_regex"] == w["path_regex"] {
				rules[i], replaced = w, true
			}
		}
		if replaced {
			continue
		}
		at := firstMatching(rules, cluster)
		rules = append(rules, nil)
		copy(rules[at+1:], rules[at:])
		rules[at] = w
	}
	doc["creation_rules"] = rules
	return marshal(doc)
}

// firstMatching returns the index of the first existing rule that SOPS would pick for a path of the cluster: a
// rule without path_regex, or one whose path_regex (Go regexp, as SOPS uses) matches a sample path. A rule whose
// regex does not compile is treated conservatively as matching, so the cluster rules go before it. Without such a
// rule it returns len(rules).
func firstMatching(rules []any, cluster string) int {
	samples := []string{
		"gitops/clusters/" + cluster + "/secrets/ns/x.sops.yaml",
		"gitops/clusters/" + cluster + "/x.sops.yaml",
		"secrets/offcluster/" + cluster + "/x.sops.yaml",
	}
	for i, r := range rules {
		m, ok := r.(map[string]any)
		if !ok {
			continue
		}
		p, _ := m["path_regex"].(string)
		if p == "" {
			return i
		}
		re, err := regexp.Compile(p)
		if err != nil {
			return i
		}
		for _, s := range samples {
			if re.MatchString(s) {
				return i
			}
		}
	}
	return len(rules)
}

// ClusterVarsInput is what the cluster-vars render needs.
type ClusterVarsInput struct {
	Vars          map[string]string
	ServiceCIDR   string
	PodCIDR       string
	S3Endpoint    string
	S3PrivateCIDR string
	Resolve       func(host string) ([]netip.Addr, error)
	DNSUpstreams  []string
	PinnedIPs     []netip.Addr
}

// RenderClusterVars renders vars/cluster-vars.yaml (ConfigMap flux-system/cluster-vars). The Traefik ClusterIP and
// the S3 egress variables use the preflight derivation, so the refusals carry the preflight codes. SC_NODE_IP_* is
// left to provision.
func RenderClusterVars(in ClusterVarsInput) ([]byte, error) {
	data := map[string]string{}
	for k, v := range in.Vars {
		if !strings.HasPrefix(k, "SC_NODE_IP_") {
			data[k] = v
		}
	}
	traefik, err := preflight.CheckPinnedIPs(in.ServiceCIDR, in.PinnedIPs)
	if err != nil {
		return nil, err
	}
	egress, err := preflight.EgressVars(in.S3Endpoint, in.S3PrivateCIDR, in.Resolve)
	if err != nil {
		return nil, err
	}
	if len(in.DNSUpstreams) == 0 {
		return nil, fail("DNS_UPSTREAM_MISSING", "no DNS resolver to seed SC_DNS_UPSTREAM_1")
	}
	for _, r := range in.DNSUpstreams {
		if _, perr := netip.ParseAddr(r); perr != nil {
			return nil, fail("DNS_UPSTREAM_INVALID", "the DNS resolver %q is not an IP address", r)
		}
	}
	data["SC_SERVICE_CIDR"], data["SC_POD_CIDR"], data["SC_TRAEFIK_CLUSTER_IP"] = in.ServiceCIDR, in.PodCIDR, traefik.String()
	for k, v := range egress {
		data[k] = v
	}
	for i := 0; i < 3; i++ {
		r := in.DNSUpstreams[0]
		if i < len(in.DNSUpstreams) {
			r = in.DNSUpstreams[i]
		}
		data[fmt.Sprintf("SC_DNS_UPSTREAM_%d", i+1)] = r
	}
	return clusterVarsDocument(data)
}

// clusterVarsDocument marshals the ConfigMap flux-system/cluster-vars with the keys sorted.
func clusterVarsDocument(data map[string]string) ([]byte, error) {
	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	dn := &yaml.Node{Kind: yaml.MappingNode}
	for _, k := range keys {
		dn.Content = append(dn.Content, scalar(k), scalar(data[k]))
	}
	root := &yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{
		scalar("apiVersion"), scalar("v1"), scalar("kind"), scalar("ConfigMap"),
		scalar("metadata"), {Kind: yaml.MappingNode, Content: []*yaml.Node{scalar("name"), scalar("cluster-vars"), scalar("namespace"), scalar("flux-system")}},
		scalar("data"), dn,
	}}
	return marshal(root)
}

// ClusterVarsData reads the data keys of a rendered vars/cluster-vars.yaml.
func ClusterVarsData(doc []byte) (map[string]string, error) {
	var in struct {
		Data map[string]string `yaml:"data"`
	}
	if err := yaml.Unmarshal(doc, &in); err != nil {
		return nil, fail("CLUSTER_VARS_INVALID", "cluster-vars.yaml is not a ConfigMap: %v", err)
	}
	if in.Data == nil {
		in.Data = map[string]string{}
	}
	return in.Data, nil
}

// SetClusterVars returns the document with the given data keys set and every other key kept: the way provision writes
// the keys it owns (SC_NODE_IP_1..5, SC_DNS_UPSTREAM_1..3) without re-rendering the rest (plan 50 section 14.3, BOOT-9).
func SetClusterVars(existing []byte, set map[string]string) ([]byte, error) {
	data, err := ClusterVarsData(existing)
	if err != nil {
		return nil, err
	}
	for k, v := range set {
		data[k] = v
	}
	return clusterVarsDocument(data)
}
