package ovh

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/netip"
	"strings"
)

// HardwareSpec is the catalogue hardware of the machine class, used before the SSH discovery has run.
type HardwareSpec struct {
	CPUCount    int
	MemoryBytes int64
	DiskBytes   int64
	DiskClass   string
}

// NodeInput is the raw API material of one node.
type NodeInput struct {
	// Name is node-NN from the resource key.
	Name string
	// VPS is the body of GET /vps/{serviceName}; IPs the body of GET /vps/{serviceName}/ips.
	VPS      []byte
	IPs      []byte
	Hardware HardwareSpec
}

// NormalizeInput is everything Normalize needs; GeneratedAt is injected (no wall clock).
type NormalizeInput struct {
	Cluster               string
	GeneratedAt           string
	ImplementationVersion string
	ProductLine           string
	Region                string
	MachineClass          string
	PlanCode              string
	Currency              string
	MonthlyTotal          string
	MaxMonthlyCost        string
	CatalogVersion        string
	StateSerial           int
	StateLineage          string
	Nodes                 []NodeInput
}

type obj = map[string]any

// nodeState maps vps.VpsStateEnum to the ProviderOutput node state.
func nodeState(s string) string {
	switch s {
	case "running":
		return "running"
	case "stopped":
		return "stopped"
	case "installing", "reinstalling", "starting", "upgrading", "migrating":
		return "provisioning"
	}
	return "unknown"
}

func money(r *big.Rat) string {
	s := r.FloatString(4)
	s = strings.TrimRight(s, "0")
	return strings.TrimSuffix(s, ".")
}

// Normalize maps the OVH API responses to a ProviderOutput JSON document (plan 04 section 7.2).
func Normalize(in NormalizeInput) ([]byte, error) {
	if len(in.Nodes) == 0 {
		return nil, errors.New("ovh: normalize: no nodes")
	}
	total, ok := new(big.Rat).SetString(in.MonthlyTotal)
	if !ok {
		return nil, fmt.Errorf("ovh: normalize: monthly total %q is not a decimal", in.MonthlyTotal)
	}
	maxCost, ok := new(big.Rat).SetString(in.MaxMonthlyCost)
	if !ok {
		return nil, fmt.Errorf("ovh: normalize: max monthly cost %q is not a decimal", in.MaxMonthlyCost)
	}
	count := int64(len(in.Nodes))
	unit := new(big.Rat).Quo(total, big.NewRat(count, 1))

	nodes := make([]obj, 0, len(in.Nodes))
	for _, n := range in.Nodes {
		var v struct {
			Name  string `json:"name"`
			State string `json:"state"`
		}
		if err := json.Unmarshal(n.VPS, &v); err != nil {
			return nil, fmt.Errorf("ovh: normalize %s: GET /vps response: %w", n.Name, err)
		}
		if v.Name == "" {
			return nil, fmt.Errorf("ovh: normalize %s: GET /vps response has no name", n.Name)
		}
		var ips []string
		if err := json.Unmarshal(n.IPs, &ips); err != nil {
			return nil, fmt.Errorf("ovh: normalize %s: ips response: %w", n.Name, err)
		}
		var v4, v6 string
		for _, s := range ips {
			a, err := netip.ParseAddr(s)
			if err != nil {
				return nil, fmt.Errorf("ovh: normalize %s: bad ip %q", n.Name, s)
			}
			switch {
			case a.Is4() && v4 == "":
				v4 = s
			case a.Is6() && !a.Is4In6() && v6 == "":
				v6 = s
			}
		}
		if v4 == "" {
			return nil, fmt.Errorf("ovh: normalize %s: no public IPv4 address", n.Name)
		}
		addr := obj{"public_ipv4": v4, "public_ipv6": nil, "internal_ip": v4, "external_ip": nil}
		if v6 != "" {
			addr["public_ipv6"] = v6
		}
		nodes = append(nodes, obj{
			"name":          n.Name,
			"provider_id":   v.Name,
			"machine_class": in.MachineClass,
			"roles":         []string{"server"},
			"addresses":     addr,
			"ssh":           obj{"host": v4, "port": 22, "user": "debian", "host_key_fingerprints": []string{}},
			"hardware": obj{
				"cpu_count":    n.Hardware.CPUCount,
				"memory_bytes": n.Hardware.MemoryBytes,
				"disks": []obj{{"name": "root", "size_bytes": n.Hardware.DiskBytes, "class": n.Hardware.DiskClass,
					"class_source": "provider-catalog", "role": "root"}},
				"virtualization": "unknown",
				"architecture":   "amd64",
			},
			"os":       obj{"id": "debian", "version_id": "13", "kernel": "unknown"},
			"location": obj{"region": in.Region, "failure_domain": in.Region},
			"state":    nodeState(v.State),
			"labels": obj{
				"strange.company/install":          in.Cluster,
				"strange.company/provider-product": in.PlanCode,
			},
			// Hardware and OS are catalogue values until the SSH discovery runs (plan 04 section 7.2).
			"substrate_validation": obj{"profile": "pending-discovery", "meets_minimum": false, "meets_preferred": nil, "findings": []obj{}},
		})
	}

	doc := obj{
		"schema_version": "v1alpha1",
		"record_type":    "provider_output",
		"cluster":        in.Cluster,
		"generated_at":   in.GeneratedAt,
		"provider": obj{
			"name":                   "ovh",
			"implementation_version": in.ImplementationVersion,
			"product_line":           in.ProductLine,
			"capabilities": obj{
				"provisions_compute": true, "paid": true, "reversible_within_hours": in.ProductLine != "vps",
				"supports_expand": true, "supports_destroy_compute": true, "host_key_source": "tofu",
			},
		},
		"region": obj{"provider_region": in.Region},
		"nodes":  nodes,
		"cost_estimate": obj{
			"currency": in.Currency, "monthly_total": money(total), "committed_minimum": money(total),
			"max_monthly_cost": money(maxCost), "within_limit": total.Cmp(maxCost) <= 0, "complete": true,
			"catalog_version": in.CatalogVersion,
			"items": []obj{{"resource_type": "ovh_vps", "product_id": in.MachineClass, "count": count,
				"unit_monthly": money(unit), "setup_fee": "0", "commitment_period": "P1M", "price_source": "catalog"}},
		},
		"tofu": obj{
			"root_module": "infra/tofu/compute/ovh-" + in.ProductLine, "backend_key": in.Cluster + "/compute/terraform.tfstate",
			"state_serial": in.StateSerial, "state_lineage": in.StateLineage,
		},
		"warnings": []obj{},
	}
	return json.MarshalIndent(doc, "", "  ")
}
