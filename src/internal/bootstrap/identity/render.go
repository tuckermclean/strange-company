package identity

import (
	"bytes"
	"encoding/pem"
	"fmt"
	"net/netip"
	"sort"
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

// resolvePath fills the placeholders of a register path; ok is false when one stays open or the path is outside the
// two directories a render may write.
func resolvePath(cluster string, s Secret, p string) (string, bool) {
	p = strings.ReplaceAll(p, "{cluster}", cluster)
	p = strings.ReplaceAll(p, "<instance>", s.Instance)
	if strings.Contains(p, "<ns>") {
		p = strings.ReplaceAll(p, "<ns>", nsFor(s.Class, s.Instance))
	}
	if strings.Contains(p, "<cluster>") {
		p = strings.ReplaceAll(p, "<cluster>", s.Instance)
	}
	if strings.ContainsAny(p, "<>") || strings.Contains(p, "//") || !strings.HasSuffix(p, ".sops.yaml") {
		return "", false
	}
	if !strings.HasPrefix(p, "gitops/clusters/"+cluster+"/") && !strings.HasPrefix(p, "secrets/offcluster/"+cluster+"/") {
		return "", false
	}
	return p, true
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
	for _, s := range secrets {
		c := classes[s.Class]
		var k8sName, k8sNS string
		for _, st := range c.Storage {
			if st.Kind == "k8s-secret" && st.Name != "" {
				k8sName = strings.ReplaceAll(st.Name, "<cluster>", s.Instance)
				k8sNS = st.Namespace
				if k8sNS == "" {
					k8sNS = nsFor(s.Class, s.Instance)
				}
				break
			}
		}
		for _, st := range c.Storage {
			if st.Kind != "sops-git" && st.Kind != "offcluster-sops" {
				continue
			}
			p, ok := resolvePath(cluster, s, st.Path)
			if !ok {
				continue
			}
			d := docs[p]
			if d == nil {
				d = &fileDoc{path: p, offcluster: strings.HasPrefix(p, "secrets/offcluster/"), keys: map[string]string{}}
				docs[p] = d
			}
			if d.name == "" {
				d.name, d.ns = k8sName, k8sNS
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
	var files []File
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
		files = append(files, File{Path: p, Data: out})
	}
	return files, nil
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
		{"path_regex": `gitops/clusters/` + cluster + `/.*\.sops\.yaml$`, "age": rcp.Cluster + "," + rcp.Recovery},
		{"path_regex": `secrets/offcluster/` + cluster + `/.*\.sops\.yaml$`, "age": rcp.Recovery},
	}
	for _, w := range want {
		replaced := false
		for i, r := range rules {
			if m, ok := r.(map[string]any); ok && m["path_regex"] == w["path_regex"] {
				rules[i], replaced = w, true
			}
		}
		if !replaced {
			rules = append(rules, w)
		}
	}
	doc["creation_rules"] = rules
	return marshal(doc)
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

// Deferred is a generated secret that RenderSOPS did not render, with the reason.
// SC-STUB(F08 50-AC-10)
type Deferred struct {
	Class    string
	Instance string
	Path     string
	Reason   string
}

// RenderReport is RenderSOPS's files plus every register secret it left out.
// SC-STUB(F08 50-AC-10)
type RenderReport struct {
	Files    []File
	Deferred []Deferred
}

// RenderSOPSReport renders like RenderSOPS and also reports every secret with a sops-git or offcluster-sops row
// that it did not render.
// SC-STUB(F08 50-AC-10)
func RenderSOPSReport(cluster string, set *Set, rcp Recipients, enc Encryptor) (*RenderReport, error) {
	return nil, nil
}
