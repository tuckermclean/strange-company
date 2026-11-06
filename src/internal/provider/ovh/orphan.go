package ovh

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
)

// Orphan codes (plan 04 section 14.4).
const (
	CodeOrphanPaidResource = "ORPHAN_PAID_RESOURCE"
	CodeOrphanPendingOrder = "ORPHAN_PENDING_ORDER"
)

// Orphan is one resource of this install that is absent from state.
type Orphan struct {
	// Code is ORPHAN_PAID_RESOURCE or ORPHAN_PENDING_ORDER.
	Code string
	// ProviderID is the VPS service name (empty for a pending order without a service).
	ProviderID string
	// OrderID is the OVH order id, when the orphan was found through an order.
	OrderID string
}

// ScanConfig configures the orphan scan (plan 04 section 14.4).
type ScanConfig struct {
	Cluster string
	// InstallCreatedAt is the RFC 3339 instant passed as date.from to GET /me/order.
	InstallCreatedAt string
	// PlanCodes are the VPS plan codes of the catalogue.
	PlanCodes []string
	// StateIDs are the provider ids (service names) present in state.
	StateIDs []string
}

// Scanner is the read-only OVH orphan scan.
type Scanner struct {
	c   *Client
	cfg ScanConfig
}

// NewScanner returns a scanner that reads through c.
func NewScanner(c *Client, cfg ScanConfig) *Scanner { return &Scanner{c: c, cfg: cfg} }

// Scan returns delivered orphans, pending orders and delivered unnamed orders; it only issues GET requests.
func (s *Scanner) Scan(ctx context.Context) ([]Orphan, error) {
	if s.cfg.Cluster == "" || s.cfg.InstallCreatedAt == "" {
		return nil, errors.New("ovh: orphan scan: the cluster and the install created_at are required")
	}
	if len(s.cfg.PlanCodes) == 0 {
		return nil, errors.New("ovh: orphan scan: the catalogue plan codes are required (an empty set would classify nothing)")
	}
	inState := set(s.cfg.StateIDs)
	plans := set(s.cfg.PlanCodes)
	named := regexp.MustCompile(`^sc-` + regexp.QuoteMeta(s.cfg.Cluster) + `-node-[0-9]{2}$`)

	var out []Orphan
	idx := map[string]int{} // service name -> index in out
	add := func(o Orphan) {
		if o.ProviderID != "" {
			if i, ok := idx[o.ProviderID]; ok {
				if out[i].OrderID == "" {
					out[i].OrderID = o.OrderID
				}
				return
			}
			idx[o.ProviderID] = len(out)
		}
		out = append(out, o)
	}

	var services []string
	if err := s.c.Get(ctx, "/vps", &services); err != nil {
		return nil, err
	}
	for _, sn := range services {
		if inState[sn] {
			continue
		}
		var v struct {
			DisplayName string `json:"displayName"`
		}
		if err := s.c.Get(ctx, "/vps/"+sn, &v); err != nil {
			return nil, err
		}
		if named.MatchString(v.DisplayName) {
			add(Orphan{Code: CodeOrphanPaidResource, ProviderID: sn})
		}
	}

	var orders []json.Number
	if err := s.c.Get(ctx, "/me/order?date.from="+url.QueryEscape(s.cfg.InstallCreatedAt), &orders); err != nil {
		return nil, err
	}
	for _, n := range orders {
		id := n.String()
		var status string
		if err := s.c.Get(ctx, "/me/order/"+id+"/status", &status); err != nil {
			return nil, err
		}
		if status == "cancelled" {
			continue
		}
		var details []json.Number
		if err := s.c.Get(ctx, "/me/order/"+id+"/details", &details); err != nil {
			return nil, err
		}
		for _, d := range details {
			base := "/me/order/" + id + "/details/" + d.String()
			var det struct {
				Domain string `json:"domain"`
			}
			if err := s.c.Get(ctx, base, &det); err != nil {
				return nil, err
			}
			var ext struct {
				Order struct {
					Plan *struct {
						Code *string `json:"code"`
					} `json:"plan"`
				} `json:"order"`
			}
			if err := s.c.Get(ctx, base+"/extension", &ext); err != nil {
				return nil, err
			}
			// A missing plan code cannot be shown to be foreign: fail closed and report it.
			if p := ext.Order.Plan; p != nil && p.Code != nil && !plans[*p.Code] {
				continue
			}
			if det.Domain != "" && inState[det.Domain] {
				continue
			}
			switch {
			case status == "delivered":
				add(Orphan{Code: CodeOrphanPaidResource, ProviderID: det.Domain, OrderID: id})
			default:
				add(Orphan{Code: CodeOrphanPendingOrder, ProviderID: det.Domain, OrderID: id})
			}
		}
	}
	return out, nil
}

func set(l []string) map[string]bool {
	m := make(map[string]bool, len(l))
	for _, s := range l {
		m[s] = true
	}
	return m
}
