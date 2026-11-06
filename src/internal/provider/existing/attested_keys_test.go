package existing_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"strange.company/sc/internal/provider"
	"strange.company/sc/internal/provider/discovery"
	"strange.company/sc/internal/provider/existing"
)

// recordingTransport serves the given keys and records the keys it is asked to pin the connection to.
type recordingTransport struct {
	factsTransport
	mu     sync.Mutex
	pinned [][]string
}

func (r *recordingTransport) Facts(ctx context.Context, n discovery.Node, keys []string) ([]byte, error) {
	r.mu.Lock()
	r.pinned = append(r.pinned, append([]string(nil), keys...))
	r.mu.Unlock()
	return r.factsTransport.Facts(ctx, n, keys)
}

// 05-AC-4 (plan 05 section 14.4, 05-R-4): with an operator attestation (EXISTING_NODE_OVERRIDES host_key), only scanned
// keys whose SHA256 fingerprint equals it are trusted: the keys the connection is pinned to (the temporary known_hosts)
// and the node's host_key_fingerprints hold the attested key only, not the ecdsa key that was scanned beside it.
func TestHostKeyAttestation_NarrowsTheTrustedKeySet_05AC4(t *testing.T) {
	node, tr := fixture(t)
	var ed, ecdsa string
	for _, k := range tr.keys {
		switch {
		case strings.Contains(k, " ssh-ed25519 "):
			ed = k
		case strings.Contains(k, " ecdsa-sha2-"):
			ecdsa = k
		}
	}
	if ed == "" || ecdsa == "" {
		t.Fatal("fixture lacks an ed25519 or ecdsa key")
	}
	attested := discovery.Fingerprints([]string{ed})[0]
	node.HostKeyFingerprint = attested
	rec := &recordingTransport{factsTransport: factsTransport{keys: []string{ed, ecdsa}, facts: tr.facts}}

	p := existing.New(existing.Config{Nodes: []discovery.Node{node}, ProfileFile: profile, InstallUID: func() string { return uidA }, Transport: rec})
	if _, err := p.Plan(context.Background(), provider.Desired{Cluster: "lab-one", NodeCount: 1}); err != nil {
		t.Fatalf("Plan: %v", err)
	}

	if len(rec.pinned) != 1 {
		t.Fatalf("Facts was called %d times, want 1", len(rec.pinned))
	}
	if got := rec.pinned[0]; len(got) != 1 || got[0] != ed {
		t.Errorf("Facts was given %d keys %v, want only the attested ed25519 key", len(got), got)
	}
	doc, err := p.OutputDocument("lab-one", time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC), "0", "USD")
	if err != nil {
		t.Fatalf("OutputDocument: %v", err)
	}
	if s := string(doc); strings.Contains(s, discovery.Fingerprints([]string{ecdsa})[0]) {
		t.Errorf("the output still lists the unattested ecdsa fingerprint:\n%s", s)
	}
	if kh := string(p.KnownHosts()); strings.Contains(kh, "ecdsa-sha2-") {
		t.Errorf("KnownHosts still holds the unattested ecdsa key:\n%s", kh)
	}
}
