package install

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"strange.company/sc/internal/bootstrap/cli"
	"strange.company/sc/internal/bootstrap/engine"
	"strange.company/sc/internal/provider"
)

// This file holds the compute part of the recover pipeline (plan 50 sections 7.1 and 14.9, plan 03 section 20.1,
// DR-12): recover.fence moves the compute nodes whose provider API does not answer out of managed state, then
// plan.compute-plan and plan.confirm plan and confirm replacements under new names in the configured region. The
// restore steps of plan 45 are not implemented here: the pipeline ends after plan.confirm.

const (
	stepFenceNodes = "recover.fence"

	// nodeProbeTimeout is how long one node's provider API gets to answer before the node counts as unreachable.
	nodeProbeTimeout = 2 * time.Minute

	procFenceNodes = "procedures/provider-outage.md#steps"
)

// fencedNode is one entry of the journal's facts.fenced_nodes (contracts/bootstrap-journal.schema.yaml).
type fencedNode struct {
	Address    string `json:"address"`
	ProviderID string `json:"provider_id"`
	FencedAt   string `json:"fenced_at"`
}

func (s *sess) recoverer() (provider.Recoverer, error) {
	prov, err := s.providerOrFail()
	if err != nil {
		return nil, err
	}
	r, ok := prov.(provider.Recoverer)
	if !ok {
		return nil, fail("RECOVER_UNSUPPORTED", 1, procFenceNodes, "the provider %q cannot fence nodes or plan replacements in this build", prov.Capabilities().Name)
	}
	return r, nil
}

// fencedAddresses are the plan addresses of facts.fenced_nodes.
func (s *sess) fencedAddresses() []string {
	out := []string{}
	for _, f := range s.fencedFacts() {
		out = append(out, f.Address)
	}
	return out
}

func (s *sess) fencedFacts() []fencedNode {
	var fs []fencedNode
	if raw := s.eng.Fact("fenced_nodes"); len(raw) > 0 {
		_ = json.Unmarshal(raw, &fs)
	}
	return fs
}

// unreachable probes every node of the state, each with its own timeout, and returns those that do not answer.
func (s *sess) unreachable(ctx context.Context, r provider.Recoverer) ([]provider.StateNode, []fencedNode, error) {
	nodes, err := r.StateNodes(ctx)
	if err != nil {
		return nil, nil, wrapProviderErr(err)
	}
	var bad []provider.StateNode
	badAddr := map[string]bool{}
	live := map[string]bool{}
	for _, n := range nodes {
		pctx, cancel := context.WithTimeout(ctx, nodeProbeTimeout)
		perr := r.Probe(pctx, n)
		cancel()
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		live[n.Address] = true
		if perr != nil {
			if !provider.IsUnavailable(perr) && !errors.Is(perr, context.DeadlineExceeded) {
				return nil, nil, wrapProviderErr(perr)
			}
			badAddr[n.Address] = true
			s.eng.Log("warn", "node-unreachable", "the provider API does not answer for a node", map[string]any{"node": n.Name, "error": perr.Error()})
			bad = append(bad, n)
		}
	}
	// A fenced fact whose node is still managed and answers again is stale (a Fence that failed after the checkpoint).
	var stale []fencedNode
	for _, f := range s.fencedFacts() {
		if live[f.Address] && !badAddr[f.Address] {
			stale = append(stale, f)
		}
	}
	return bad, stale, nil
}

// dropStale removes the stale entries from facts.fenced_nodes so the facts never list a live managed node.
func (s *sess) dropStale(ctx context.Context, stale []fencedNode) error {
	if len(stale) == 0 {
		return nil
	}
	gone := map[string]bool{}
	for _, f := range stale {
		gone[f.Address] = true
	}
	kept := []fencedNode{}
	for _, f := range s.fencedFacts() {
		if !gone[f.Address] {
			kept = append(kept, f)
		}
	}
	if err := s.eng.SetFact("fenced_nodes", kept); err != nil {
		return err
	}
	return s.eng.Checkpoint(ctx)
}

func (s *sess) stepFenceNodes() engine.Unit {
	return &unit{name: stepFenceNodes, timeout: 30 * time.Minute, inputs: map[string]any{"step": stepFenceNodes},
		check: func(ctx context.Context) (engine.Check, error) {
			r, err := s.recoverer()
			if err != nil {
				return engine.Check{}, err
			}
			bad, stale, err := s.unreachable(ctx, r)
			if err != nil {
				if ctx.Err() != nil {
					return engine.Check{}, err
				}
				// The run repeats the probe and returns the error as the step's own (with its code).
				return notDone("the nodes cannot be probed: " + err.Error()), nil
			}
			if len(stale) > 0 {
				return notDone(fmt.Sprintf("%d fenced fact(s) name a node that is managed and answers", len(stale))), nil
			}
			if len(bad) == 0 {
				return done("every node of the state answers"), nil
			}
			return notDone(fmt.Sprintf("%d node(s) do not answer", len(bad))), nil
		},
		run: s.runFenceNodes}
}

func (s *sess) runFenceNodes(ctx context.Context) ([]engine.OutputRef, error) {
	r, err := s.recoverer()
	if err != nil {
		return nil, err
	}
	bad, stale, err := s.unreachable(ctx, r)
	if err != nil {
		return nil, err
	}
	if err := s.dropStale(ctx, stale); err != nil {
		return nil, err
	}
	if len(bad) == 0 {
		return nil, nil
	}
	sort.Slice(bad, func(i, j int) bool { return bad[i].Address < bad[j].Address })
	count := strconv.Itoa(len(bad))
	s.printf("UNREACHABLE NODES - cluster %s: the provider API does not answer for %s node(s); they will be moved out of managed state\n", s.cluster, count)
	var addrs []string
	for _, n := range bad {
		s.printf("  FENCE  %s  %s  (%s)\n", n.Name, n.Address, n.ProviderID)
		addrs = append(addrs, n.Address)
	}
	s.printf("Fencing does not delete anything at the provider; the nodes keep running until you remove them there.\n")
	want := s.cluster + " " + count
	given := s.inv.Confirm["fence-nodes"]
	if given == "" {
		given = s.env["SC_CONFIRM_FENCE_NODES"]
	}
	method := "flag"
	if given == "" {
		if s.inv.NonInteractive {
			return nil, fail("FENCE_CONFIRMATION_MISSING", 5, procFenceNodes,
				"confirm fencing with SC_CONFIRM_FENCE_NODES=\"%s\" (the typed confirmation is: fence-nodes %s)", want, want)
		}
		ok := false
		for attempt := 1; attempt <= 3 && !ok; attempt++ {
			line, err := s.readLine(ctx, fmt.Sprintf("Type 'fence-nodes %s' to proceed (attempt %d of 3): ", want, attempt), false)
			if err != nil {
				return nil, err
			}
			ok = line == "fence-nodes "+want
		}
		if !ok {
			return nil, fail("FENCE_CONFIRMATION_MISMATCH", 5, procFenceNodes, "the typed confirmation does not match 'fence-nodes %s'", want)
		}
		method = "interactive"
	} else if given != want {
		return nil, fail("FENCE_CONFIRMATION_MISMATCH", 5, procFenceNodes,
			"the confirmed text %q is not %q; nothing was fenced", given, want)
	}
	sum := sha256.Sum256([]byte("fence-nodes\n" + s.cluster + "\n" + strings.Join(addrs, "\n")))
	subject := "sha256:" + hex.EncodeToString(sum[:])
	if !s.eng.Confirmed("fence-nodes", subject) {
		s.eng.RecordConfirmation(cli.Confirmation{Kind: "fence-nodes", SubjectDigest: subject, Method: method})
	}
	// The intent is recorded before the state is touched: a crash in between repeats the fencing, which finds the node
	// unreachable again and does not list it twice.
	facts := s.fencedFacts()
	at := s.clk.Now().UTC().Format(time.RFC3339)
	for _, n := range bad {
		known := false
		for _, f := range facts {
			known = known || f.Address == n.Address
		}
		if !known {
			facts = append(facts, fencedNode{Address: n.Address, ProviderID: n.ProviderID, FencedAt: at})
		}
	}
	if err := s.eng.SetFact("fenced_nodes", facts); err != nil {
		return nil, err
	}
	if err := s.eng.Checkpoint(ctx); err != nil {
		return nil, err
	}
	for _, n := range bad {
		if err := r.Fence(ctx, n); err != nil {
			return nil, wrapProviderErr(err)
		}
		s.printf("Fenced %s (%s).\n", n.Name, n.Address)
	}
	return nil, nil
}

// recoverPipeline is the compute part of the recover pipeline: preflight, then recover.fence, plan.compute-plan
// and plan.confirm. --scope state-only fences and plans no compute (plan 50 section 14.9).
func (s *sess) recoverPipeline() engine.Pipeline {
	pre := &phase{unit: unit{name: "preflight", timeout: 10 * time.Minute, inputs: map[string]string{"step": "preflight"}, run: s.runPreflight}}
	phases := []engine.Phase{pre}
	if s.inv.Flags["scope"] != "state-only" {
		steps := []engine.Unit{s.stepFenceNodes(), s.stepComputePlan(), s.stepPlanConfirm()}
		stop := ""
		if s.env["SC_TEST_MODE"] == "1" {
			stop = s.env["SC_TEST_STOP_AFTER"]
		}
		for i, st := range steps {
			if st.Name() == stop {
				steps = steps[:i+1]
				break
			}
		}
		phases = append(phases, &phase{unit: unit{name: "plan", timeout: 6 * time.Hour,
			inputs: map[string]any{"cluster": s.cluster, "recover": true}}, steps: steps})
	}
	return engine.Pipeline{Kind: "recover", Phases: phases}
}
