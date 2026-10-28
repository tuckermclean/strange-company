package install

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"strange.company/sc/internal/bootstrap/cli"
	"strange.company/sc/internal/bootstrap/commit"
	"strange.company/sc/internal/bootstrap/engine"
	"strange.company/sc/internal/bootstrap/identity"
	"strange.company/sc/internal/provider"
)

// This file holds plan.compute-plan and plan.confirm (plan 50 section 14.3 steps 10 and 11, plan 03 sections 14.1 and
// 14.7), the provision phase (plan 50 section 14.4, plan 03 section 14.8) and the stubbed phases after it
// (sections 14.5 and 14.6), which later nodes replace.

const (
	stepComputePlan = "plan.compute-plan"
	stepPlanConfirm = "plan.confirm"

	// maxNodeSlots is the number of SC_NODE_IP_<n> keys of cluster-vars.
	maxNodeSlots = 5
)

// stubPhases are the phases after provision in the order of plan 50 sections 14.5 and 14.6. They are placeholders
// that exist only in test mode: no provider but the fake one reaches them.
var stubPhases = []string{"seed", "forge-init", "flux-bootstrap", "sso-init", "ci-init", "verify", "control-transfer"}

func (s *sess) expectedNodes() int {
	n, err := strconv.Atoi(str(dig(s.doc, "spec", "provider", "expectedNodeCount")))
	if err != nil || n < 1 {
		return 1
	}
	return n
}

func (s *sess) desired() provider.Desired {
	return provider.Desired{Cluster: s.cluster, NodeCount: s.expectedNodes()}
}

// providerOrFail is the provider of the install: only the fake provider exists in this build; the adapters of plans 04
// and 05 arrive with their own nodes.
func (s *sess) providerOrFail() (provider.Provider, error) {
	if s.prov == nil {
		return nil, fail("PROVIDER_NOT_IMPLEMENTED", 1, procPreflight,
			"the provider %q has no adapter in this build; only the fake provider (SC_TEST_MODE=1) can compute and apply a plan",
			str(dig(s.doc, "spec", "provider", "name")))
	}
	return s.prov, nil
}

// wrapProviderErr gives a provider failure its stable code.
func wrapProviderErr(err error) error {
	var pe *provider.Error
	if errors.As(err, &pe) {
		return &engine.Error{Code: pe.Code, Message: pe.Message, Retryable: pe.Retryable()}
	}
	return err
}

// planKind is the confirmation kind of the plan: a provider that creates nodes is confirmed as a paid footprint, one
// that only takes over nodes as the existing-node list.
func (s *sess) planKind() string {
	if s.prov != nil && s.prov.Capabilities().CreatesNodes {
		return "paid-footprint"
	}
	return "existing-nodes"
}

// d8 is the short plan digest the operator types (plan 03 section 14.7).
func d8(digest string) string {
	d := strings.TrimPrefix(digest, "sha256:")
	if len(d) > 8 {
		d = d[:8]
	}
	return d
}

func (s *sess) planFootprint(p *provider.ProviderPlan) string {
	caps := s.prov.Capabilities()
	var sb strings.Builder
	title := "PAID FOOTPRINT"
	if !caps.Paid {
		title = "FOOTPRINT (0 paid resources)"
	}
	fmt.Fprintf(&sb, "%s - cluster %s (%s)\n", title, s.cluster, caps.Name)
	for _, n := range p.Nodes {
		verb := "TAKE OVER"
		if caps.CreatesNodes {
			verb = "CREATE"
		}
		fmt.Fprintf(&sb, "  %s  %s  %s\n", verb, n.Name, n.InternalIP)
	}
	if p.MonthlyCost != "" {
		fmt.Fprintf(&sb, "  Monthly total: %s\n", p.MonthlyCost)
	}
	fmt.Fprintf(&sb, "Plan digest: %s\n", p.Digest)
	if caps.CreatesNodes {
		fmt.Fprintf(&sb, "To proceed type exactly:  create %s %s\n", s.cluster, d8(p.Digest))
	} else {
		fmt.Fprintf(&sb, "To proceed type exactly:  %s\n", s.cluster)
	}
	return sb.String()
}

// stateAtLeastProvisioned reports whether provision has succeeded (a later phase may have consumed its output).
func stateAtLeastProvisioned(state string) bool {
	switch state {
	case "", "new", "planned":
		return false
	}
	return true
}

// --- plan.compute-plan

func (s *sess) stepComputePlan() engine.Unit {
	return &unit{name: stepComputePlan, timeout: 30 * time.Minute, inputs: map[string]any{"step": stepComputePlan},
		// Never cached: the plan is recomputed on every run (plan 50 section 14.3 step 10).
		run: func(ctx context.Context) ([]engine.OutputRef, error) {
			prov, err := s.providerOrFail()
			if err != nil {
				return nil, err
			}
			if err := prov.Validate(provider.Config{Cluster: s.cluster, ExpectedNodeCount: s.expectedNodes()}); err != nil {
				return nil, &engine.Error{Code: "PROVIDER_CONFIG_INVALID", Message: err.Error(), Exit: 2}
			}
			plan, err := prov.Plan(ctx, s.desired())
			if err != nil {
				return nil, wrapProviderErr(err)
			}
			s.plan = plan
			return nil, nil
		}}
}

// --- plan.confirm

func (s *sess) planDigest() string {
	if s.plan == nil {
		return ""
	}
	return s.plan.Digest
}

func (s *sess) stepPlanConfirm() engine.Unit {
	return &unit{name: stepPlanConfirm, timeout: 30 * time.Minute, inputsFn: func() any { return map[string]any{"plan": s.planDigest()} },
		check: func(context.Context) (engine.Check, error) {
			if stateAtLeastProvisioned(s.eng.InstallationState()) {
				return done("provision consumed the confirmation of the plan"), nil
			}
			if s.plan != nil && s.eng.Confirmed(s.planKind(), s.plan.Digest) {
				return done("the plan is confirmed"), nil
			}
			return notDone("the plan is not confirmed"), nil
		},
		run: func(ctx context.Context) ([]engine.OutputRef, error) {
			if s.plan == nil {
				return nil, fail("PLAN_MISSING", 1, procResume, "no plan was computed in this run")
			}
			s.printf("%s", s.planFootprint(s.plan))
			short := d8(s.plan.Digest)
			given := s.inv.Confirm["plan"]
			if given == "" {
				given = s.env["SC_CONFIRM_PLAN"]
			}
			method := "flag"
			if given == "" {
				if s.inv.NonInteractive {
					return nil, fail("PLAN_CONFIRMATION_MISSING", 5, procResume,
						"confirm the plan with --confirm-plan %s or SC_CONFIRM_PLAN=%s", short, short)
				}
				want := s.cluster
				if s.prov.Capabilities().CreatesNodes {
					want = "create " + s.cluster + " " + short
				}
				ok := false
				for attempt := 1; attempt <= 3 && !ok; attempt++ {
					line, err := s.readLine(ctx, fmt.Sprintf("Type '%s' to proceed (attempt %d of 3): ", want, attempt), false)
					if err != nil {
						return nil, err
					}
					ok = line == want
				}
				if !ok {
					return nil, fail("PLAN_CONFIRMATION_MISMATCH", 5, procResume, "the typed confirmation does not match '%s'", want)
				}
				method = "interactive"
			} else if given != short {
				return nil, fail("PLAN_CONFIRMATION_MISMATCH", 5, procResume,
					"the confirmed plan digest %q is not %s; the plan stays unconfirmed", given, short)
			}
			s.eng.RecordConfirmation(cli.Confirmation{Kind: s.planKind(), SubjectDigest: s.plan.Digest, Method: method})
			return nil, nil
		}}
}

// --- provision

const nodeInventorySubjectFmt = "bootstrap(%s): node inventory"

func (s *sess) nodeInventorySubject() string { return fmt.Sprintf(nodeInventorySubjectFmt, s.cluster) }

func (s *sess) clusterDir() string { return "gitops/clusters/" + s.cluster + "/" }

func (s *sess) stepProvision() engine.Phase {
	return &phase{unit: unit{name: "provision", timeout: 60 * time.Minute, inputsFn: func() any { return map[string]any{"plan": s.planDigest()} },
		check: func(ctx context.Context) (engine.Check, error) {
			prov, err := s.providerOrFail()
			if err != nil {
				return engine.Check{}, err
			}
			out, err := prov.Inventory(ctx)
			if err != nil {
				return engine.Check{}, wrapProviderErr(err)
			}
			if len(out.Nodes) != s.expectedNodes() {
				return notDone(fmt.Sprintf("the inventory lists %d nodes, %d are expected", len(out.Nodes), s.expectedNodes())), nil
			}
			if !s.commitForInstall(ctx, s.nodeInventorySubject()) {
				return notDone("the node inventory is not committed"), nil
			}
			return done("the inventory has the expected nodes and the node inventory is committed"), nil
		},
		run: s.runProvision}}
}

func (s *sess) runProvision(ctx context.Context) ([]engine.OutputRef, error) {
	prov, err := s.providerOrFail()
	if err != nil {
		return nil, err
	}
	orphans, err := prov.OrphanScan(ctx)
	if err != nil {
		return nil, wrapProviderErr(err)
	}
	if len(orphans) > 0 {
		return nil, fail("ORPHAN_PAID_RESOURCE", 4, "procedures/bootstrap-resume.md#provision",
			"resources of this install exist that are not in state: %s", strings.Join(orphans, ", "))
	}
	// The plan is recomputed and must be the one that was confirmed (plan 03 section 14.8 step 2).
	plan, err := prov.Plan(ctx, s.desired())
	if err != nil {
		return nil, wrapProviderErr(err)
	}
	if s.plan != nil && plan.Digest != s.plan.Digest {
		return nil, fail("PLAN_DIGEST_MISMATCH", 4, "procedures/bootstrap-resume.md#provision",
			"the plan digest changed from %s to %s after the confirmation", s.plan.Digest, plan.Digest)
	}
	if !s.eng.Confirmed(s.planKind(), plan.Digest) {
		return nil, fail("PLAN_NOT_CONFIRMED", 5, procResume, "the plan %s was never confirmed", plan.Digest)
	}
	inv, err := prov.Inventory(ctx)
	if err != nil {
		return nil, wrapProviderErr(err)
	}
	// A crash after the apply and before the commit leaves the nodes in place: a compute re-plan has nothing left to
	// do, so the apply is not repeated.
	if len(inv.Nodes) < s.expectedNodes() {
		if err := prov.Apply(ctx, plan); err != nil {
			return nil, wrapProviderErr(err)
		}
		if inv, err = prov.Inventory(ctx); err != nil {
			return nil, wrapProviderErr(err)
		}
	}
	if orphans, err = prov.OrphanScan(ctx); err != nil || len(orphans) > 0 || len(inv.Nodes) != s.expectedNodes() {
		return nil, fail("POST_APPLY_AUDIT_FAILED", 1, "procedures/provider-outage.md#unexpected-resources",
			"after the apply the inventory lists %d nodes (%d expected) and %d resources outside the plan", len(inv.Nodes), s.expectedNodes(), len(orphans))
	}
	var resolvers []string
	if d, ok := prov.(provider.Discoverer); ok {
		disc, err := d.Discover(ctx)
		if err != nil {
			return nil, wrapProviderErr(err)
		}
		resolvers = disc.Resolvers
	}
	sha, err := s.commitNodeInventory(ctx, inv, resolvers)
	if err != nil {
		return nil, err
	}
	return []engine.OutputRef{{Kind: "git-commit", Ref: sha}}, nil
}

// nodeVars are the cluster-vars keys provision owns: SC_NODE_IP_1..5 (slot 1 is node-01's internal IP, unused slots
// repeat it) and, from discovery, SC_DNS_UPSTREAM_1..3 (one resolver fills every slot, two fill the third with the first).
func nodeVars(nodes []provider.Node, resolvers []string) (map[string]string, error) {
	set := map[string]string{}
	for i := 0; i < maxNodeSlots; i++ {
		ip := nodes[0].InternalIP
		if i < len(nodes) {
			ip = nodes[i].InternalIP
		}
		if _, err := netip.ParseAddr(ip); err != nil {
			return nil, fail("NODE_IP_INVALID", 1, procResume, "node %s has the internal IP %q, which is not an IP address", nodes[min(i, len(nodes)-1)].Name, ip)
		}
		set["SC_NODE_IP_"+strconv.Itoa(i+1)] = ip
	}
	for _, r := range resolvers {
		if _, err := netip.ParseAddr(r); err != nil {
			return nil, fail("DNS_UPSTREAM_INVALID", 1, procResume, "the discovered resolver %q is not an IP address", r)
		}
	}
	if len(resolvers) > 0 {
		for i := 0; i < 3; i++ {
			r := resolvers[0]
			if i < len(resolvers) {
				r = resolvers[i]
			}
			set["SC_DNS_UPSTREAM_"+strconv.Itoa(i+1)] = r
		}
	}
	return set, nil
}

// commitNodeInventory writes inventory.yaml, the node-shape fields of config/cluster.yaml and the provision-owned keys
// of vars/cluster-vars.yaml and commits them (plan 01 section 6.5).
func (s *sess) commitNodeInventory(ctx context.Context, inv *provider.ProviderOutput, resolvers []string) (string, error) {
	if len(inv.Nodes) == 0 {
		return "", fail("INVENTORY_EMPTY", 1, procResume, "the provider reports no nodes")
	}
	base := s.clusterDir()
	set, err := nodeVars(inv.Nodes, resolvers)
	if err != nil {
		return "", err
	}
	nodes := make([]map[string]any, 0, len(inv.Nodes))
	names := make([]string, 0, len(inv.Nodes))
	for _, n := range inv.Nodes {
		nodes = append(nodes, map[string]any{"name": n.Name, "internal_ip": n.InternalIP})
		names = append(names, n.Name)
	}
	invDoc, err := yaml.Marshal(map[string]any{
		"apiVersion": "strange.company/v1alpha1", "kind": "NodeInventory", "metadata": map[string]any{"name": s.cluster}, "nodes": nodes,
	})
	if err != nil {
		return "", err
	}
	if err := s.writeRepoFile(base+"inventory.yaml", invDoc); err != nil {
		return "", err
	}
	cfgDoc, err := s.clusterConfigWithNodes(names)
	if err != nil {
		return "", err
	}
	if err := s.writeRepoFile(base+"config/cluster.yaml", cfgDoc); err != nil {
		return "", err
	}
	cur, err := os.ReadFile(filepath.Join(s.repo, filepath.FromSlash(base+"vars/cluster-vars.yaml")))
	if err != nil {
		return "", fail("CLUSTER_VARS_MISSING", 1, procResume, "%svars/cluster-vars.yaml cannot be read: %v", base, err)
	}
	vars, err := identity.SetClusterVars(cur, set)
	if err != nil {
		return "", err
	}
	if err := s.writeRepoFile(base+"vars/cluster-vars.yaml", vars); err != nil {
		return "", err
	}
	return commit.Create(ctx, commit.Request{
		Repo: s.repo, Cluster: s.cluster, Kind: commit.Install, Subject: s.nodeInventorySubject(),
		BaseDomain: str(dig(s.doc, "spec", "cluster", "baseDomain")), InstallUID: s.eng.InstallUID(), RunID: s.eng.RunID(), Clock: s.clk,
		Paths: []string{base + "inventory.yaml", base + "config/cluster.yaml", base + "vars/cluster-vars.yaml"},
	})
}

// clusterConfigWithNodes sets the node-shape fields of config/cluster.yaml (provider, node count, node names) and
// keeps every other field another phase wrote there.
func (s *sess) clusterConfigWithNodes(names []string) ([]byte, error) {
	doc := map[string]any{"apiVersion": "strange.company/v1alpha1", "kind": "ClusterConfig"}
	if b, err := os.ReadFile(filepath.Join(s.repo, filepath.FromSlash(s.clusterDir()+"config/cluster.yaml"))); err == nil {
		var cur map[string]any
		if yaml.Unmarshal(b, &cur) == nil && cur != nil {
			doc = cur
		}
	}
	spec, _ := doc["spec"].(map[string]any)
	if spec == nil {
		spec = map[string]any{}
	}
	spec["provider"] = str(dig(s.doc, "spec", "provider", "name"))
	spec["nodeCount"] = len(names)
	spec["nodes"] = names
	doc["spec"] = spec
	return yaml.Marshal(doc)
}

// --- stubbed phases after provision

// stubPhase is a placeholder for a phase a later node implements. It records success and, for verify, reports the
// permanent warning of an accepted unprotected state (plan 50 section 14.3 step 5, 50-AC-32).
func (s *sess) stubPhase(name string) engine.Phase {
	u := unit{name: name, timeout: 5 * time.Minute, inputs: map[string]any{"stub": name}}
	if name == "verify" {
		u.run = s.runVerifyStub
	} else {
		u.check = func(context.Context) (engine.Check, error) { return done("a later node implements this phase"), nil }
		u.run = func(context.Context) ([]engine.OutputRef, error) { return nil, nil }
	}
	return &phase{unit: u}
}

func (s *sess) runVerifyStub(context.Context) ([]engine.OutputRef, error) {
	if s.unprotected() && s.oc.allowUnprotected {
		msg := "OBJSTORE_STATE_UNPROTECTED: the OpenTofu state and the recovery bundle share the cluster's object-storage account (accepted with S3_ALLOW_UNPROTECTED_STATE=true)"
		s.eng.Log("warn", "OBJSTORE_STATE_UNPROTECTED", msg, nil)
		s.errorf("warning: %s\n", msg)
		if err := s.eng.WriteReport("verify: warning " + msg + "\n"); err != nil {
			return nil, err
		}
	}
	return nil, nil
}
