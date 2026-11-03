// Package existing is the existing-nodes provider of plan 05: it accepts machines that already exist and are reachable
// over SSH, discovers their facts (src/internal/provider/discovery), validates them against the effective profile and
// emits the same normalized ProviderOutput as a paid provider. It creates nothing and spends nothing.
package existing

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"go.yaml.in/yaml/v3"

	"strange.company/sc/internal/provider"
	"strange.company/sc/internal/provider/discovery"
)

// ImplementationVersion is written to provider.implementation_version of the output.
const ImplementationVersion = "0.1.0"

// Transport reaches a node: the SSH implementation in production, a directory of recorded output in test mode.
type Transport interface {
	// HostKeys returns the ssh-keyscan lines of the node.
	HostKeys(ctx context.Context, n discovery.Node) ([]string, error)
	// Facts runs the discovery script on the node and returns its JSON output. hostKeys are the keys captured by
	// HostKeys, which the connection is pinned to.
	Facts(ctx context.Context, n discovery.Node, hostKeys []string) ([]byte, error)
}

// Config configures the provider.
type Config struct {
	Nodes []discovery.Node
	// ProfileFile is the effective profile, <repo>/infra/profiles/<profile>.yaml.
	ProfileFile string
	// InstallUID returns this installation's uid (known once the engine runs).
	InstallUID func() string
	Transport  Transport
}

// Provider is the existing-nodes provider.
type Provider struct {
	cfg Config

	mu      sync.Mutex
	inputs  []discovery.Input
	results []discovery.Result
}

var _ provider.Provider = (*Provider)(nil)

// New returns the provider for the configured nodes.
func New(cfg Config) *Provider { return &Provider{cfg: cfg} }

// Capabilities: nothing is created and nothing costs money.
func (p *Provider) Capabilities() provider.Capabilities {
	return provider.Capabilities{Name: "existing"}
}

// Validate checks the node list against the expected node count.
func (p *Provider) Validate(cfg provider.Config) error {
	if len(p.cfg.Nodes) == 0 {
		return provider.NewError("EXISTING_NODES_EMPTY", "EXISTING_NODES lists no node", false)
	}
	if cfg.ExpectedNodeCount != len(p.cfg.Nodes) {
		return provider.NewError("NODE_COUNT_MISMATCH",
			fmt.Sprintf("EXISTING_NODES lists %d nodes but EXPECTED_NODE_COUNT is %d", len(p.cfg.Nodes), cfg.ExpectedNodeCount), false)
	}
	return nil
}

// Preflight has no online check of its own: reachability is part of discovery, which Plan runs.
func (p *Provider) Preflight(context.Context, provider.Config) ([]provider.Finding, error) {
	return nil, nil
}

// failure turns the fail findings of the results into one provider error (exit 3 in the pipeline).
func failure(results []discovery.Result) error {
	var codes, lines []string
	for _, r := range results {
		for _, f := range r.Findings {
			if f.Severity != "fail" {
				continue
			}
			codes = append(codes, f.Code)
			lines = append(lines, f.Code+": "+f.Message)
		}
	}
	if len(codes) == 0 {
		return nil
	}
	return provider.NewError(codes[0], strings.Join(lines, "\n"), false)
}

// discover captures the host keys of every node, checks the attested ones before any login, runs the discovery script
// and evaluates the facts. The nodes are contacted concurrently.
func (p *Provider) discover(ctx context.Context) ([]discovery.Input, []discovery.Result, error) {
	nodes := p.cfg.Nodes
	inputs := make([]discovery.Input, len(nodes))
	errs := make([]error, len(nodes))
	var wg sync.WaitGroup
	for i := range nodes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			inputs[i], errs[i] = p.contact(ctx, nodes[i])
		}(i)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return nil, nil, err
		}
	}
	uid := ""
	if p.cfg.InstallUID != nil {
		uid = p.cfg.InstallUID()
	}
	results, err := discovery.Evaluate(inputs, discovery.Context{ProfileFile: p.cfg.ProfileFile, InstallUID: uid})
	if err != nil {
		return nil, nil, provider.NewError("DISCOVERY_FAILED", err.Error(), false)
	}
	return inputs, results, nil
}

func (p *Provider) contact(ctx context.Context, n discovery.Node) (discovery.Input, error) {
	keys, err := p.cfg.Transport.HostKeys(ctx, n)
	if err != nil {
		return discovery.Input{}, provider.NewError("EXISTING_NODE_UNREACHABLE", fmt.Sprintf("%s (%s:%d): %v", n.Name, n.Host, n.Port, err), true)
	}
	if n.HostKeyFingerprint != "" {
		fps := discovery.Fingerprints(keys)
		var narrowed []string
		for _, l := range keys {
			for _, fp := range discovery.Fingerprints([]string{l}) {
				if fp == n.HostKeyFingerprint {
					narrowed = append(narrowed, l)
					break
				}
			}
		}
		if len(narrowed) == 0 {
			return discovery.Input{}, provider.NewError("HOST_KEY_MISMATCH",
				fmt.Sprintf("%s (%s:%d): none of its host keys has the attested fingerprint %s (found: %s); nothing was contacted", n.Name, n.Host, n.Port, n.HostKeyFingerprint, strings.Join(fps, ", ")), false)
		}
		keys = narrowed
	}
	facts, err := p.cfg.Transport.Facts(ctx, n, keys)
	if err != nil {
		if strings.Contains(err.Error(), "Host key verification failed") {
			return discovery.Input{}, provider.NewError("HOST_KEY_MISMATCH", fmt.Sprintf("%s (%s:%d): the host key changed since it was captured: %v", n.Name, n.Host, n.Port, err), false)
		}
		return discovery.Input{}, provider.NewError("EXISTING_NODE_UNREACHABLE", fmt.Sprintf("%s (%s:%d): %v", n.Name, n.Host, n.Port, err), true)
	}
	return discovery.Input{Node: n, Facts: facts, HostKeys: keys}, nil
}

// Plan discovers and validates the nodes and returns the no-op plan whose digest covers the node list, the host-key
// fingerprints and the discovered facts used for validation. It changes nothing on the nodes.
func (p *Provider) Plan(ctx context.Context, d provider.Desired) (*provider.ProviderPlan, error) {
	inputs, results, err := p.discover(ctx)
	if err != nil {
		return nil, err
	}
	if err := failure(results); err != nil {
		return nil, err
	}
	p.mu.Lock()
	p.inputs, p.results = inputs, results
	p.mu.Unlock()
	digest, err := planDigest(results)
	if err != nil {
		return nil, provider.NewError("PLAN_DIGEST_FAILED", err.Error(), false)
	}
	return &provider.ProviderPlan{Digest: digest, Nodes: p.nodes(results)}, nil
}

func get(m map[string]any, path ...string) any {
	var cur any = m
	for _, k := range path {
		mm, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = mm[k]
	}
	return cur
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func (p *Provider) nodes(results []discovery.Result) []provider.Node {
	out := make([]provider.Node, 0, len(results))
	for _, r := range results {
		n := r.Node
		var fps []string
		for _, f := range asList(get(n, "ssh", "host_key_fingerprints")) {
			fps = append(fps, str(f))
		}
		sort.Strings(fps)
		port, _ := get(n, "ssh", "port").(int)
		mem, _ := get(n, "hardware", "memory_bytes").(int64)
		cpus, _ := get(n, "hardware", "cpu_count").(int64)
		class := ""
		if ds := asList(get(n, "hardware", "disks")); len(ds) > 0 {
			class = str(get(ds[0].(map[string]any), "class"))
		}
		out = append(out, provider.Node{
			Name: str(n["name"]), InternalIP: str(get(n, "addresses", "internal_ip")),
			SSHHost: str(get(n, "ssh", "host")), SSHPort: port, SSHUser: str(get(n, "ssh", "user")), HostKeys: fps,
			Summary: fmt.Sprintf("%s, %d CPUs, %.1f GiB memory, %s disk", str(get(n, "os", "pretty_name")), cpus, float64(mem)/(1<<30), class),
		})
	}
	return out
}

func asList(v any) []any {
	l, _ := v.([]any)
	return l
}

// planDigest is the sha256 of a canonical document of everything that identifies the machines: their configuration,
// host-key fingerprints, the machine id, the install marker, the platforms found, the kernel release and the other
// discovered facts. Only the facts that legitimately change between runs (free space, swap and time-sync status) are
// left out and re-checked at provision (plan 05 section 17).
func planDigest(results []discovery.Result) (string, error) {
	type entry struct {
		Name       string   `json:"name"`
		Host       string   `json:"host"`
		Port       any      `json:"port"`
		User       string   `json:"user"`
		InternalIP string   `json:"internal_ip"`
		External   any      `json:"external_ip"`
		PublicV4   any      `json:"public_ipv4"`
		PublicV6   any      `json:"public_ipv6"`
		HostKeys   []string `json:"host_keys"`
		CPUs       any      `json:"cpu_count"`
		Memory     any      `json:"memory_bytes"`
		Disks      any      `json:"disks"`
		Virt       any      `json:"virtualization"`
		Arch       any      `json:"architecture"`
		OSID       any      `json:"os_id"`
		OSVersion  any      `json:"os_version_id"`
		Roles      any      `json:"roles"`
		Kernel     any      `json:"kernel"`
		MachineID  string   `json:"machine_id"`
		InstallUID string   `json:"install_uid"`
		Platforms  []string `json:"platforms"`
	}
	var es []entry
	for _, r := range results {
		n := r.Node
		var fps []string
		for _, f := range asList(get(n, "ssh", "host_key_fingerprints")) {
			fps = append(fps, str(f))
		}
		sort.Strings(fps)
		es = append(es, entry{
			Name: str(n["name"]), Host: str(get(n, "ssh", "host")), Port: get(n, "ssh", "port"), User: str(get(n, "ssh", "user")),
			InternalIP: str(get(n, "addresses", "internal_ip")), External: get(n, "addresses", "external_ip"),
			PublicV4: get(n, "addresses", "public_ipv4"), PublicV6: get(n, "addresses", "public_ipv6"),
			HostKeys: fps, CPUs: get(n, "hardware", "cpu_count"), Memory: get(n, "hardware", "memory_bytes"),
			Disks: get(n, "hardware", "disks"), Virt: get(n, "hardware", "virtualization"), Arch: get(n, "hardware", "architecture"),
			OSID: get(n, "os", "id"), OSVersion: get(n, "os", "version_id"), Roles: n["roles"],
			Kernel: get(n, "os", "kernel"), MachineID: r.Identity.MachineID, InstallUID: r.Identity.InstallUID,
			Platforms: append([]string{}, r.Identity.Platforms...),
		})
	}
	sort.Slice(es, func(i, j int) bool { return es[i].Name < es[j].Name })
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(es); err != nil {
		return "", err
	}
	sum := sha256.Sum256(buf.Bytes())
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// Apply is a no-op: there is nothing to create.
func (p *Provider) Apply(context.Context, *provider.ProviderPlan) error { return nil }

// Inventory is the normalized output of the latest plan; without one the nodes are discovered now.
func (p *Provider) Inventory(ctx context.Context) (*provider.ProviderOutput, error) {
	p.mu.Lock()
	results := p.results
	p.mu.Unlock()
	if results == nil {
		_, rs, err := p.discover(ctx)
		if err != nil {
			return nil, err
		}
		if err := failure(rs); err != nil {
			return nil, err
		}
		p.mu.Lock()
		p.results = rs
		p.mu.Unlock()
		results = rs
	}
	return &provider.ProviderOutput{Nodes: p.nodes(results)}, nil
}

// OrphanScan: existing never creates anything, so nothing can be orphaned.
func (p *Provider) OrphanScan(context.Context) ([]string, error) { return []string{}, nil }

// Expand plans with the current node list (the operator added the new entries to EXISTING_NODES).
func (p *Provider) Expand(ctx context.Context, target int) (*provider.ProviderPlan, error) {
	return p.Plan(ctx, provider.Desired{NodeCount: target})
}

// DestroyCompute is not supported: destroying existing nodes is a decommission (plan 49).
func (p *Provider) DestroyCompute(context.Context) (*provider.ProviderPlan, error) {
	return nil, provider.NewError("NOT_SUPPORTED", "existing nodes are decommissioned, not destroyed (plan 49)", false)
}

// OutputDocument renders the ProviderOutput document (contracts/provider.schema.yaml) of the latest plan: capabilities
// of the existing provider, a zero cost estimate, one entry per node.
func (p *Provider) OutputDocument(cluster string, now time.Time, maxMonthlyCost, currency string) ([]byte, error) {
	p.mu.Lock()
	results := p.results
	p.mu.Unlock()
	if results == nil {
		return nil, provider.NewError("PLAN_MISSING", "no plan was computed", false)
	}
	attested := true
	nodes := make([]any, 0, len(results))
	byName := append([]discovery.Result(nil), results...)
	sort.Slice(byName, func(i, j int) bool { return str(byName[i].Node["name"]) < str(byName[j].Node["name"]) })
	for _, r := range byName {
		nodes = append(nodes, r.Node)
	}
	for _, n := range p.cfg.Nodes {
		attested = attested && n.HostKeyFingerprint != ""
	}
	source := "tofu"
	if attested {
		source = "operator-supplied"
		if p.labProfile() {
			// The lab generates the host keys and injects them through cloud-init (plan 05 section 14.6).
			source = "cloud-init"
		}
	}
	if currency == "" {
		currency = "EUR"
	}
	doc := map[string]any{
		"schema_version": "v1alpha1", "record_type": "provider_output", "cluster": cluster,
		"generated_at": now.UTC().Format(time.RFC3339),
		"provider": map[string]any{"name": "existing", "implementation_version": ImplementationVersion, "product_line": "none",
			"capabilities": map[string]any{"provisions_compute": false, "paid": false, "reversible_within_hours": true,
				"supports_expand": true, "supports_destroy_compute": false, "host_key_source": source}},
		"region": map[string]any{"provider_region": "n/a"},
		"nodes":  nodes,
		"cost_estimate": map[string]any{"currency": currency, "monthly_total": "0", "committed_minimum": "0",
			"max_monthly_cost": maxMonthlyCost, "within_limit": true, "complete": true, "catalog_version": "none", "items": []any{}},
		"tofu": nil, "warnings": []any{},
	}
	return json.MarshalIndent(doc, "", "  ")
}

// labProfile reports whether the effective profile carries the label strange.company/lab: "true".
func (p *Provider) labProfile() bool {
	if p.cfg.ProfileFile == "" {
		return false
	}
	raw, err := os.ReadFile(p.cfg.ProfileFile)
	if err != nil {
		return false
	}
	var doc struct {
		Metadata struct {
			Labels map[string]string `yaml:"labels"`
		} `yaml:"metadata"`
	}
	if yaml.Unmarshal(raw, &doc) != nil {
		return false
	}
	return doc.Metadata.Labels["strange.company/lab"] == "true"
}

// KnownHosts renders the pinned host keys of the latest plan in known_hosts format ([host]:port keytype key).
func (p *Provider) KnownHosts() []byte {
	p.mu.Lock()
	inputs := p.inputs
	p.mu.Unlock()
	var b strings.Builder
	for _, in := range inputs {
		for _, l := range in.HostKeys {
			f := strings.Fields(l)
			if len(f) < 3 || strings.HasPrefix(l, "#") {
				continue
			}
			host := in.Node.Host
			if in.Node.Port != 0 && in.Node.Port != 22 {
				host = fmt.Sprintf("[%s]:%d", in.Node.Host, in.Node.Port)
			}
			fmt.Fprintf(&b, "%s %s %s\n", host, f[1], f[2])
		}
	}
	return []byte(b.String())
}
