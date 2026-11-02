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

func nodes(n int) []provider.Node {
	out := make([]provider.Node, 0, n)
	for i := 1; i <= n; i++ {
		out = append(out, provider.Node{Name: fmt.Sprintf("node-%02d", i), InternalIP: fmt.Sprintf("10.99.0.%d", 10+i)})
	}
	return out
}

// Capabilities: the fake creates nodes but costs nothing.
func (p *Provider) Capabilities() provider.Capabilities {
	return provider.Capabilities{Name: "fake", CreatesNodes: true}
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
func (p *Provider) Plan(_ context.Context, d provider.Desired) (*provider.ProviderPlan, error) {
	sc, err := p.scenario()
	if err != nil {
		return nil, err
	}
	g, err := guard.ParseGuard([]byte(guardYAML))
	if err != nil {
		return nil, provider.NewError("GUARD_INVALID", err.Error(), false)
	}
	pj := planJSON(d.Cluster, d.NodeCount, sc.Plan.ExtraResources)
	if v := g.Evaluate(pj, guard.Expect{ExpectedNodeCount: d.NodeCount}); !v.Pass {
		return nil, provider.NewError(v.Code, fmt.Sprintf("%s (%s)", v.Message, v.Address), false)
	}
	dg, err := g.Digest(pj)
	if err != nil {
		return nil, provider.NewError("GUARD_DIGEST_FAILED", err.Error(), false)
	}
	return &provider.ProviderPlan{Digest: "sha256:" + dg, Nodes: nodes(d.NodeCount)}, nil
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
	if sc.Orphan.CrashAfterOrder {
		if err := os.WriteFile(filepath.Join(p.dir, orderFile), []byte(strconv.Itoa(len(plan.Nodes))+"\n"), 0o600); err != nil {
			return err
		}
		return provider.NewError("FAKE_CRASH_AFTER_ORDER", "the fake run died after the order was accepted", false)
	}
	return nil
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

// Inventory returns the nodes the counter says exist, without those of an unrecorded order.
func (p *Provider) Inventory(context.Context) (*provider.ProviderOutput, error) {
	n, err := CreateCount(p.dir)
	if err != nil {
		return nil, err
	}
	o, err := p.unrecordedOrder()
	if err != nil {
		return nil, err
	}
	if o > n {
		o = n
	}
	return &provider.ProviderOutput{Nodes: nodes(n - o)}, nil
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
	return sc, nil
}

// fakeProviderSource is the provider the synthetic plan names. The fake module (infra/tofu/compute/fake) really uses
// the built-in terraform_data, which no guard admits; the synthetic plan therefore names this registry-style source,
// which infra/tofu/compute/fake/guard.yaml allows.
const fakeProviderSource = "registry.opentofu.org/strange-company/fake"

// PlanJSON is the synthetic OpenTofu plan JSON (show -json) the fake feeds through the guard for cluster and nodeCount,
// without scenario additions.
func PlanJSON(cluster string, nodeCount int) []byte { return planJSON(cluster, nodeCount, nil) }

func planJSON(cluster string, nodeCount int, extras []extraResource) []byte {
	changes := []any{}
	for _, n := range nodes(nodeCount) {
		changes = append(changes, map[string]any{
			"address": fmt.Sprintf("fake_node.node[%q]", n.Name), "mode": "managed", "type": "fake_node", "name": "node",
			"index": n.Name, "provider_name": fakeProviderSource,
			"change": map[string]any{
				"actions": []string{"create"}, "before": nil,
				"after":           map[string]any{"cluster": cluster, "name": n.Name, "ip": n.InternalIP},
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
