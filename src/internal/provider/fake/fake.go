// Package fake is the OpenTofu-free fake provider (plan 03 section 22, BOOT-11): it simulates creation for the
// bootstrap tests and counts creates. It is refused outside SC_TEST_MODE=1.
package fake

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"go.yaml.in/yaml/v3"

	"strange.company/sc/internal/provider"
	"strange.company/sc/internal/provider/guard"
)

const counterFile = "creates"

// Provider is the fake provider bound to its state directory.
type Provider struct {
	dir string
	mu  sync.Mutex
	// profileFile is the effective profile node hardware is validated against and catalogueFile the price catalogue of a
	// paid scenario (Configure); both are only read by OutputDocument.
	profileFile, catalogueFile string
}

// Configure names the effective profile (infra/profiles/<profile>.yaml) and the price catalogue
// (infra/catalog/prices/fake.yaml) of the installation.
func (p *Provider) Configure(profileFile, catalogueFile string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.profileFile, p.catalogueFile = profileFile, catalogueFile
}

var _ provider.Provider = (*Provider)(nil)

// New returns the fake provider with state in dir; without SC_TEST_MODE=1 in env it fails with TEST_MODE_REQUIRED.
func New(env map[string]string, dir string) (*Provider, error) {
	if env["SC_TEST_MODE"] != "1" {
		return nil, errors.New("TEST_MODE_REQUIRED: the fake provider only runs with SC_TEST_MODE=1")
	}
	if dir == "" {
		return nil, errors.New("fake provider: no state directory")
	}
	return &Provider{dir: dir}, nil
}

// CreateCount reads the create counter in dir (the file "creates"); a missing file is zero.
func CreateCount(dir string) (int, error) {
	b, err := os.ReadFile(filepath.Join(dir, counterFile))
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || n < 0 {
		return 0, fmt.Errorf("fake provider: %s does not hold a count", filepath.Join(dir, counterFile))
	}
	return n, nil
}

func (p *Provider) bump(n int) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	cur, err := CreateCount(p.dir)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(p.dir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(p.dir, counterFile), []byte(strconv.Itoa(cur+n)+"\n"), 0o600)
}

// defaultRegion is the region of a node the scenario names no region for.
const defaultRegion = "fake-1"

// ipFor is the internal IP of node-NN: 10.99.0.<10+NN>.
func ipFor(name string) string {
	n, _ := nodeNumber(name)
	return fmt.Sprintf("10.99.0.%d", 10+n)
}

// nodeNumber parses the NN of node-NN.
func nodeNumber(name string) (int, bool) {
	n, err := strconv.Atoi(strings.TrimPrefix(name, "node-"))
	return n, err == nil && strings.HasPrefix(name, "node-") && n > 0
}

func nodeName(i int) string { return fmt.Sprintf("node-%02d", i) }

func nodes(n int) []provider.Node {
	out := make([]provider.Node, 0, n)
	for i := 1; i <= n; i++ {
		out = append(out, provider.Node{Name: nodeName(i), InternalIP: ipFor(nodeName(i))})
	}
	return out
}

// stateFile is the fake's managed state: the nodes it holds (with their regions) and the names it retired.
const stateFile = "state.json"

type stateNode struct {
	Name   string `json:"name"`
	Region string `json:"region"`
}

type state struct {
	Nodes []stateNode `json:"nodes"`
	// Retired are the names of fenced nodes; a name is never used again (plan 03 section 8).
	Retired []string `json:"retired,omitempty"`
}

// loadState reads <dir>/state.json. Without it the state is what the create counter says: node-01..node-N in the
// default region.
func (p *Provider) loadState() (state, error) {
	b, err := os.ReadFile(filepath.Join(p.dir, stateFile))
	if errors.Is(err, os.ErrNotExist) {
		n, err := CreateCount(p.dir)
		if err != nil {
			return state{}, err
		}
		st := state{}
		for _, x := range nodes(n) {
			st.Nodes = append(st.Nodes, stateNode{Name: x.Name, Region: defaultRegion})
		}
		return st, nil
	}
	if err != nil {
		return state{}, err
	}
	var st state
	if err := json.Unmarshal(b, &st); err != nil {
		return state{}, fmt.Errorf("fake provider: %s is not a state file: %w", filepath.Join(p.dir, stateFile), err)
	}
	return st, nil
}

func (p *Provider) saveState(st state) error {
	if err := os.MkdirAll(p.dir, 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(p.dir, stateFile), append(b, '\n'), 0o600)
}

func (st state) has(name string) bool {
	for _, n := range st.Nodes {
		if n.Name == name {
			return true
		}
	}
	return false
}

func (st state) retired(name string) bool {
	for _, n := range st.Retired {
		if n == name {
			return true
		}
	}
	return false
}

// configuredRegion is the region new nodes are created in.
func (sc scenarioDoc) configuredRegion() string {
	if sc.Regions.Configured != "" {
		return sc.Regions.Configured
	}
	return defaultRegion
}

func (sc scenarioDoc) unavailable(region string) bool {
	for _, r := range sc.Regions.Unavailable {
		if r == region {
			return true
		}
	}
	return false
}

// Capabilities: the fake creates nodes; it costs nothing unless the scenario says paid: true (03-AC-11). A scenario that
// cannot be read leaves the fake unpaid; Plan reports the same scenario as SCENARIO_INVALID.
func (p *Provider) Capabilities() provider.Capabilities {
	sc, _ := p.scenario()
	return provider.Capabilities{Name: "fake", CreatesNodes: true, Paid: sc.Paid}
}

// Validate checks the node count.
func (p *Provider) Validate(cfg provider.Config) error {
	if cfg.ExpectedNodeCount < 1 || cfg.ExpectedNodeCount > 9 {
		return provider.NewError("NODE_COUNT_INVALID", "the node count must be 1..9", false)
	}
	return nil
}

// Preflight has nothing to check against.
func (p *Provider) Preflight(context.Context, provider.Config) ([]provider.Finding, error) {
	return []provider.Finding{{ID: "PF-07", Status: "pass", Message: "the fake provider needs no credentials"}}, nil
}

// Plan builds the synthetic plan JSON (PlanJSON plus the scenario's plan.extraResources), judges it with the fake
// guard (infra/tofu/compute/fake/guard.yaml; a failing verdict is an error carrying its GUARD_* code) and returns the
// nodes to create with the guard digest of the plan. It creates nothing.
func (p *Provider) Plan(ctx context.Context, d provider.Desired) (*provider.ProviderPlan, error) {
	sc, err := p.scenario()
	if err != nil {
		return nil, err
	}
	region := sc.configuredRegion()
	var ps []plannedNode
	for _, n := range nodes(d.NodeCount) {
		ps = append(ps, plannedNode{node: n, region: region, action: "create"})
	}
	return p.judge(ctx, sc, d, ps, nil)
}

// plannedNode is one fake_node of the synthetic plan.
type plannedNode struct {
	node   provider.Node
	region string
	action string // create or no-op
}

// judge builds the synthetic plan JSON for ps (plus the scenario's extra resources), judges it with the fake guard
// (in recover mode when fenced is not nil) and returns the nodes to create with the guard digest of the plan.
func (p *Provider) judge(_ context.Context, sc scenarioDoc, d provider.Desired, ps []plannedNode, fenced []string) (*provider.ProviderPlan, error) {
	g, err := guard.ParseGuard([]byte(guardYAML))
	if err != nil {
		return nil, provider.NewError("GUARD_INVALID", err.Error(), false)
	}
	pj := planJSON(d.Cluster, ps, sc.Plan.ExtraResources)
	e := guard.Expect{ExpectedNodeCount: d.NodeCount, Region: guard.Attr{Type: "fake_node", Path: "region", Want: sc.configuredRegion()}}
	var v guard.Verdict
	if fenced != nil {
		v = g.EvaluateRecover(pj, e, guard.RecoverMode{Fenced: fenced})
	} else {
		v = g.Evaluate(pj, e)
	}
	if !v.Pass {
		return nil, provider.NewError(v.Code, fmt.Sprintf("%s (%s)", v.Message, v.Address), false)
	}
	dg, err := g.Digest(pj)
	if err != nil {
		return nil, provider.NewError("GUARD_DIGEST_FAILED", err.Error(), false)
	}
	var create []provider.Node
	for _, n := range ps {
		if n.action == "create" {
			create = append(create, n.node)
		}
	}
	return &provider.ProviderPlan{Digest: "sha256:" + dg, Nodes: create, MachineClass: sc.MachineClass}, nil
}

var _ provider.Recoverer = (*Provider)(nil)

// StateNodes lists the nodes of the fake's state, reachable or not.
func (p *Provider) StateNodes(context.Context) ([]provider.StateNode, error) {
	st, err := p.loadState()
	if err != nil {
		return nil, err
	}
	var out []provider.StateNode
	for _, n := range st.Nodes {
		out = append(out, provider.StateNode{Name: n.Name, ProviderID: "fake:" + n.Name, Address: nodeAddress(n.Name)})
	}
	return out, nil
}

func nodeAddress(name string) string { return fmt.Sprintf("fake_node.node[%q]", name) }

// Probe asks the fake API for the node: one in an unavailable region answers `unavailable`.
func (p *Provider) Probe(_ context.Context, n provider.StateNode) error {
	sc, err := p.scenario()
	if err != nil {
		return err
	}
	st, err := p.loadState()
	if err != nil {
		return err
	}
	for _, x := range st.Nodes {
		if x.Name == n.Name && sc.unavailable(x.Region) {
			return provider.NewError(provider.CodeNodeUnavailable, fmt.Sprintf("the fake API reports node %s (region %s) unavailable", x.Name, x.Region), false)
		}
	}
	return nil
}

// Fence removes the node from the fake's state and retires its name.
func (p *Provider) Fence(_ context.Context, n provider.StateNode) error {
	sc, err := p.scenario()
	if err != nil {
		return err
	}
	if sc.Fence.Fail {
		return provider.NewError("FENCE_FAILED", fmt.Sprintf("the fake provider failed to fence node %s (scenario fence.fail)", n.Name), true)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	st, err := p.loadState()
	if err != nil {
		return err
	}
	kept := st.Nodes[:0:0]
	for _, x := range st.Nodes {
		if x.Name != n.Name {
			kept = append(kept, x)
		}
	}
	st.Nodes = kept
	if !st.retired(n.Name) {
		st.Retired = append(st.Retired, n.Name)
	}
	return p.saveState(st)
}

// PlanRecover plans d.NodeCount nodes in the configured region: the nodes still in state stay (no-op), the missing
// ones are created under names that are neither in state nor retired. fenced are the plan addresses of the fenced
// nodes (the recover guard excuses them from the region rule).
func (p *Provider) PlanRecover(ctx context.Context, d provider.Desired, fenced []string) (*provider.ProviderPlan, error) {
	sc, err := p.scenario()
	if err != nil {
		return nil, err
	}
	st, err := p.loadState()
	if err != nil {
		return nil, err
	}
	var ps []plannedNode
	for _, n := range st.Nodes {
		ps = append(ps, plannedNode{node: provider.Node{Name: n.Name, InternalIP: ipFor(n.Name)}, region: n.Region, action: "no-op"})
	}
	region := sc.configuredRegion()
	for i, have := 1, len(ps); have < d.NodeCount; i++ {
		name := nodeName(i)
		if st.has(name) || st.retired(name) {
			continue
		}
		ps = append(ps, plannedNode{node: provider.Node{Name: name, InternalIP: ipFor(name)}, region: region, action: "create"})
		have++
	}
	if fenced == nil {
		fenced = []string{}
	}
	plan, err := p.judge(ctx, sc, d, ps, fenced)
	if err != nil {
		return nil, err
	}
	plan.Region = region
	return plan, nil
}

// Apply "creates" every node of the plan and counts them. With the scenario orphan.crashAfterOrder the order is
// counted and recorded, then Apply fails before any node counts as recorded (the in-process stand-in for a crash).
func (p *Provider) Apply(_ context.Context, plan *provider.ProviderPlan) error {
	if plan == nil {
		return provider.NewError("PLAN_MISSING", "no plan to apply", false)
	}
	sc, err := p.scenario()
	if err != nil {
		return err
	}
	if err := p.bump(len(plan.Nodes)); err != nil {
		return err
	}
	if err := p.record(plan.Nodes, sc.configuredRegion()); err != nil {
		return err
	}
	if sc.Orphan.CrashAfterOrder {
		if err := os.WriteFile(filepath.Join(p.dir, orderFile), []byte(strconv.Itoa(len(plan.Nodes))+"\n"), 0o600); err != nil {
			return err
		}
		return provider.NewError("FAKE_CRASH_AFTER_ORDER", "the fake run died after the order was accepted", false)
	}
	return nil
}

// record adds the nodes of an applied plan to the state (a name already in it stays as it is).
func (p *Provider) record(ns []provider.Node, region string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	st, err := p.loadStateBefore(len(ns))
	if err != nil {
		return err
	}
	for _, n := range ns {
		if !st.has(n.Name) {
			st.Nodes = append(st.Nodes, stateNode{Name: n.Name, Region: region})
		}
	}
	return p.saveState(st)
}

// loadStateBefore is loadState for the moment after the counter was raised by added creates: a state synthesized from
// the counter is the state before them.
func (p *Provider) loadStateBefore(added int) (state, error) {
	if _, err := os.Stat(filepath.Join(p.dir, stateFile)); err == nil {
		return p.loadState()
	}
	cur, err := CreateCount(p.dir)
	if err != nil {
		return state{}, err
	}
	st := state{}
	for _, x := range nodes(max(cur-added, 0)) {
		st.Nodes = append(st.Nodes, stateNode{Name: x.Name, Region: defaultRegion})
	}
	return st, nil
}

// orderFile holds the size of an order that was accepted but never recorded.
const orderFile = "order"

func (p *Provider) unrecordedOrder() (int, error) {
	b, err := os.ReadFile(filepath.Join(p.dir, orderFile))
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || n < 0 {
		return 0, fmt.Errorf("fake provider: %s does not hold an order size", filepath.Join(p.dir, orderFile))
	}
	return n, nil
}

// Inventory returns the nodes of the state, without those of an unrecorded order. A node in an unavailable region
// makes it fail with NODE_UNAVAILABLE (the fake API says `unavailable`).
func (p *Provider) Inventory(context.Context) (*provider.ProviderOutput, error) {
	ns, err := p.inventoryNodes()
	if err != nil {
		return nil, err
	}
	sc, err := p.scenario()
	if err != nil {
		return nil, err
	}
	out := make([]provider.Node, 0, len(ns))
	for _, n := range ns {
		if sc.unavailable(n.Region) {
			return nil, provider.NewError("NODE_UNAVAILABLE", fmt.Sprintf("the fake API reports node %s (region %s) unavailable", n.Name, n.Region), false)
		}
		out = append(out, provider.Node{Name: n.Name, InternalIP: ipFor(n.Name)})
	}
	return &provider.ProviderOutput{Nodes: out}, nil
}

// inventoryNodes are the state nodes minus the trailing ones of an unrecorded order.
func (p *Provider) inventoryNodes() ([]stateNode, error) {
	st, err := p.loadState()
	if err != nil {
		return nil, err
	}
	o, err := p.unrecordedOrder()
	if err != nil {
		return nil, err
	}
	return st.Nodes[:len(st.Nodes)-min(o, len(st.Nodes))], nil
}

// OrphanScan lists the order that was accepted but never recorded, if any.
func (p *Provider) OrphanScan(context.Context) ([]string, error) {
	o, err := p.unrecordedOrder()
	if err != nil || o == 0 {
		return nil, err
	}
	return []string{fmt.Sprintf("fake order of %d node(s)", o)}, nil
}

// Expand plans with a higher node count.
func (p *Provider) Expand(ctx context.Context, target int) (*provider.ProviderPlan, error) {
	return p.Plan(ctx, provider.Desired{NodeCount: target})
}

// DestroyCompute is a plan-only operation; the fake plans an empty node list.
func (p *Provider) DestroyCompute(ctx context.Context) (*provider.ProviderPlan, error) {
	return p.Plan(ctx, provider.Desired{NodeCount: 0})
}

// DefaultResolver is the resolver the fake node reports when no scenario names one.
const DefaultResolver = "10.99.0.1"

const scenarioFile = "scenario.yaml"

var _ provider.Discoverer = (*Provider)(nil)

// Discover reports the resolvers of the fake node: discovery.resolvers of <dir>/scenario.yaml (plan 03 section 22,
// scenarios from fixtures), else DefaultResolver.
func (p *Provider) Discover(context.Context) (*provider.Discovery, error) {
	b, err := os.ReadFile(filepath.Join(p.dir, scenarioFile))
	if errors.Is(err, os.ErrNotExist) {
		return &provider.Discovery{Resolvers: []string{DefaultResolver}}, nil
	}
	if err != nil {
		return nil, err
	}
	sc, err := parseScenario(p.dir, b)
	if err != nil {
		return nil, err
	}
	if len(sc.Discovery.Resolvers) == 0 {
		return &provider.Discovery{Resolvers: []string{DefaultResolver}}, nil
	}
	return &provider.Discovery{Resolvers: sc.Discovery.Resolvers}, nil
}

type extraResource struct {
	Type    string   `yaml:"type"`
	Name    string   `yaml:"name"`
	Actions []string `yaml:"actions"`
}

type scenarioDoc struct {
	APIVersion string `yaml:"apiVersion"`
	Kind       string `yaml:"kind"`
	Discovery  struct {
		Resolvers []string `yaml:"resolvers"`
	} `yaml:"discovery"`
	Plan struct {
		ExtraResources []extraResource `yaml:"extraResources"`
	} `yaml:"plan"`
	Orphan struct {
		CrashAfterOrder bool `yaml:"crashAfterOrder"`
	} `yaml:"orphan"`
	// Capacity is the hardware every fake node reports; an omitted field takes the preferred shape of the supported
	// profile (4 CPUs, 12 Gi, 200 Gi nvme).
	Capacity struct {
		CPU         int64  `yaml:"cpu"`
		MemoryBytes int64  `yaml:"memoryBytes"`
		DiskBytes   int64  `yaml:"diskBytes"`
		DiskClass   string `yaml:"diskClass"`
	} `yaml:"capacity"`
	// Regions: nodes are created in Configured (default fake-1); a node recorded in one of Unavailable fails Inventory
	// (plan 03 section 20.1).
	Regions struct {
		Configured  string   `yaml:"configured"`
		Unavailable []string `yaml:"unavailable"`
	} `yaml:"regions"`
	// Fence.Fail makes Fence return an error and leave the state untouched.
	Fence struct {
		Fail bool `yaml:"fail"`
	} `yaml:"fence"`
	// Paid makes Capabilities().Paid true; MachineClass is the catalogue productId such a fake orders.
	Paid         bool   `yaml:"paid"`
	MachineClass string `yaml:"machineClass"`
}

// scenario reads <dir>/scenario.yaml; a missing file is the empty scenario.
func (p *Provider) scenario() (scenarioDoc, error) {
	var sc scenarioDoc
	b, err := os.ReadFile(filepath.Join(p.dir, scenarioFile))
	if errors.Is(err, os.ErrNotExist) {
		return sc, nil
	}
	if err != nil {
		return sc, err
	}
	return parseScenario(p.dir, b)
}

// parseScenario decodes the one scenario document strictly: an unknown key is SCENARIO_INVALID.
func parseScenario(dir string, b []byte) (scenarioDoc, error) {
	var sc scenarioDoc
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(&sc); err != nil && !errors.Is(err, io.EOF) {
		return scenarioDoc{}, provider.NewError("SCENARIO_INVALID", fmt.Sprintf("%s is not a fake provider scenario: %v", filepath.Join(dir, scenarioFile), err), false)
	}
	invalid := func(why string) (scenarioDoc, error) {
		return scenarioDoc{}, provider.NewError("SCENARIO_INVALID", fmt.Sprintf("%s is not a fake provider scenario: %s", filepath.Join(dir, scenarioFile), why), false)
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return invalid("it must hold exactly one YAML document")
	}
	if sc.Kind != "" && sc.Kind != "FakeProviderScenario" {
		return invalid("kind must be FakeProviderScenario")
	}
	if sc.APIVersion != "" && sc.APIVersion != "strange.company/v1alpha1" {
		return invalid("apiVersion must be strange.company/v1alpha1")
	}
	return sc, nil
}

// fakeProviderSource is the provider the synthetic plan names. The fake module (infra/tofu/compute/fake) really uses
// the built-in terraform_data, which no guard admits; the synthetic plan therefore names this registry-style source,
// which infra/tofu/compute/fake/guard.yaml allows.
const fakeProviderSource = "registry.opentofu.org/strange-company/fake"

// PlanJSON is the synthetic OpenTofu plan JSON (show -json) the fake feeds through the guard for cluster and nodeCount,
// without scenario additions: every node is created in the default region.
func PlanJSON(cluster string, nodeCount int) []byte {
	var ps []plannedNode
	for _, n := range nodes(nodeCount) {
		ps = append(ps, plannedNode{node: n, region: defaultRegion, action: "create"})
	}
	return planJSON(cluster, ps, nil)
}

func planJSON(cluster string, ps []plannedNode, extras []extraResource) []byte {
	changes := []any{}
	for _, n := range ps {
		changes = append(changes, map[string]any{
			"address": nodeAddress(n.node.Name), "mode": "managed", "type": "fake_node", "name": "node",
			"index": n.node.Name, "provider_name": fakeProviderSource,
			"change": map[string]any{
				"actions": []string{n.action}, "before": nil,
				"after":           map[string]any{"cluster": cluster, "name": n.node.Name, "ip": n.node.InternalIP, "region": n.region},
				"after_unknown":   map[string]any{"id": true},
				"after_sensitive": map[string]any{},
			},
		})
	}
	for _, x := range extras {
		changes = append(changes, map[string]any{
			"address": x.Type + "." + x.Name, "mode": "managed", "type": x.Type, "name": x.Name,
			"provider_name": fakeProviderSource,
			"change": map[string]any{
				"actions": x.Actions, "before": nil, "after": map[string]any{"name": x.Name},
				"after_unknown": map[string]any{}, "after_sensitive": map[string]any{},
			},
		})
	}
	b, err := json.Marshal(map[string]any{
		"format_version":    "1.2",
		"terraform_version": "1.13.1",
		"resource_changes":  changes,
		"configuration": map[string]any{
			"provider_config": map[string]any{"fake": map[string]any{"name": "fake", "full_name": fakeProviderSource}},
		},
	})
	if err != nil {
		panic(err) // unreachable: only strings and maps of strings
	}
	return b
}
