// Package fake is the OpenTofu-free fake provider (plan 03 section 22, BOOT-11): it simulates creation for the
// bootstrap tests and counts creates. It is refused outside SC_TEST_MODE=1.
package fake

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"go.yaml.in/yaml/v3"

	"strange.company/sc/internal/provider"
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

// Plan lists the nodes to create; it creates nothing.
func (p *Provider) Plan(_ context.Context, d provider.Desired) (*provider.ProviderPlan, error) {
	ns := nodes(d.NodeCount)
	h := sha256.Sum256([]byte(fmt.Sprintf("%s/%d", d.Cluster, d.NodeCount)))
	return &provider.ProviderPlan{Digest: "sha256:" + hex.EncodeToString(h[:]), Nodes: ns}, nil
}

// Apply "creates" every node of the plan and counts them.
func (p *Provider) Apply(_ context.Context, plan *provider.ProviderPlan) error {
	if plan == nil {
		return provider.NewError("PLAN_MISSING", "no plan to apply", false)
	}
	return p.bump(len(plan.Nodes))
}

// Inventory returns the nodes the counter says exist.
func (p *Provider) Inventory(context.Context) (*provider.ProviderOutput, error) {
	n, err := CreateCount(p.dir)
	if err != nil {
		return nil, err
	}
	return &provider.ProviderOutput{Nodes: nodes(n)}, nil
}

// OrphanScan finds nothing.
func (p *Provider) OrphanScan(context.Context) ([]string, error) { return nil, nil }

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
	var sc struct {
		Discovery struct {
			Resolvers []string `yaml:"resolvers"`
		} `yaml:"discovery"`
	}
	if err := yaml.Unmarshal(b, &sc); err != nil {
		return nil, provider.NewError("SCENARIO_INVALID", fmt.Sprintf("%s is not a fake provider scenario: %v", filepath.Join(p.dir, scenarioFile), err), false)
	}
	if len(sc.Discovery.Resolvers) == 0 {
		return &provider.Discovery{Resolvers: []string{DefaultResolver}}, nil
	}
	return &provider.Discovery{Resolvers: sc.Discovery.Resolvers}, nil
}
