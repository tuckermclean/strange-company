package fake

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"strange.company/sc/internal/provider"
	"strange.company/sc/internal/provider/discovery"
	"strange.company/sc/internal/provider/price"
)

// ImplementationVersion is the sc-bootstrap version the fake provider's ProviderOutput names.
const ImplementationVersion = "v0.1.0"

// Defaults of the node hardware the fake reports: the preferred shape of the supported profile.
const (
	defaultCPU      = 4
	defaultMemory   = 12 << 30
	defaultDisk     = 200 << 30
	defaultDiskKind = "nvme"
)

func (p *Provider) capacity(sc scenarioDoc) discovery.Capacity {
	c := discovery.Capacity{CPU: sc.Capacity.CPU, Memory: sc.Capacity.MemoryBytes, Disk: sc.Capacity.DiskBytes, DiskClass: sc.Capacity.DiskClass}
	if c.CPU == 0 {
		c.CPU = defaultCPU
	}
	if c.Memory == 0 {
		c.Memory = defaultMemory
	}
	if c.Disk == 0 {
		c.Disk = defaultDisk
	}
	if c.DiskClass == "" {
		c.DiskClass = defaultDiskKind
	}
	return c
}

// OutputDocument renders the ProviderOutput document (contracts/provider.schema.yaml) of the nodes the fake lists: each
// node's reported hardware is validated against the effective profile (plan 03 section 14.3; a node below the lower
// bound is NODE_BELOW_MINIMUM) and the cost estimate is priced from the catalogue of a paid scenario (section 14.6) or is
// zero.
func (p *Provider) OutputDocument(cluster string, now time.Time, maxMonthlyCost, currency string) ([]byte, error) {
	sc, err := p.scenario()
	if err != nil {
		return nil, err
	}
	out, err := p.Inventory(context.Background())
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	profileFile, catalogueFile := p.profileFile, p.catalogueFile
	p.mu.Unlock()
	if profileFile == "" {
		return nil, provider.NewError("PROFILE_MISSING", "the fake provider was given no effective profile", false)
	}
	if currency == "" {
		currency = "EUR"
	}
	machineClass := sc.MachineClass
	if machineClass == "" {
		machineClass = "fake"
	}
	nodes := make([]any, 0, len(out.Nodes))
	for i, n := range out.Nodes {
		c := p.capacity(sc)
		sv, findings, err := discovery.ValidateCapacity(profileFile, n.Name, c)
		if err != nil {
			return nil, provider.NewError("PROFILE_INVALID", err.Error(), false)
		}
		for _, f := range findings {
			if f.Severity == "fail" {
				return nil, provider.NewError(f.Code, f.Message, false)
			}
		}
		role := "agent"
		if i == 0 {
			role = "server"
		}
		nodes = append(nodes, map[string]any{
			"name": n.Name, "provider_id": "fake:" + n.Name, "machine_class": machineClass, "roles": []string{role},
			"addresses": map[string]any{"public_ipv4": nil, "public_ipv6": nil, "internal_ip": n.InternalIP, "external_ip": nil},
			"ssh":       map[string]any{"host": n.InternalIP, "port": 22, "user": "root", "admin_user": nil, "host_key_fingerprints": []string{}},
			"hardware": map[string]any{"cpu_count": c.CPU, "memory_bytes": c.Memory, "virtualization": "kvm", "architecture": "amd64",
				"disks": []any{map[string]any{"name": "sda", "size_bytes": c.Disk, "class": c.DiskClass, "class_source": "provider-catalog", "role": "root"}}},
			"os":                   map[string]any{"id": "debian", "version_id": "13", "pretty_name": "Debian GNU/Linux 13 (fake)", "kernel": "6.12.0"},
			"location":             map[string]any{"region": "fake-1", "failure_domain": "fake-1"},
			"state":                "running",
			"substrate_validation": sv,
		})
	}
	if len(nodes) == 0 {
		return nil, provider.NewError("INVENTORY_EMPTY", "the fake provider lists no nodes", false)
	}
	est := price.Zero(currency)
	if sc.Paid {
		cat, err := price.Load(catalogueFile)
		if err != nil {
			return nil, provider.NewError("COST_INCOMPLETE", err.Error(), false)
		}
		est = cat.Compute(sc.MachineClass, len(out.Nodes))
	}
	items := make([]any, 0, len(est.Items))
	for _, it := range est.Items {
		items = append(items, map[string]any{"resource_type": it.ResourceType, "product_id": it.ProductID, "count": it.Count,
			"unit_monthly": it.UnitMonthly, "setup_fee": it.SetupFee, "commitment_period": it.CommitmentPeriod, "price_source": it.PriceSource})
	}
	doc := map[string]any{
		"schema_version": "v1alpha1", "record_type": "provider_output", "cluster": cluster,
		"generated_at": now.UTC().Format(time.RFC3339),
		"provider": map[string]any{"name": "fake", "implementation_version": ImplementationVersion, "product_line": "fake",
			"capabilities": map[string]any{"provisions_compute": true, "paid": sc.Paid, "reversible_within_hours": !sc.Paid,
				"supports_expand": true, "supports_destroy_compute": true, "host_key_source": "tofu"}},
		"region": map[string]any{"provider_region": "fake-1"},
		"nodes":  nodes,
		"cost_estimate": map[string]any{"currency": est.Currency, "monthly_total": est.MonthlyTotal, "committed_minimum": est.CommittedMinimum,
			"max_monthly_cost": maxMonthlyCost, "within_limit": est.Complete && price.WithinLimit(est.MonthlyTotal, maxMonthlyCost),
			"complete": est.Complete, "catalog_version": est.CatalogVersion, "items": items},
		"tofu": nil, "warnings": []any{},
	}
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("fake provider output: %w", err)
	}
	return b, nil
}
