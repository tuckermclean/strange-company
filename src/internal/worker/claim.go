package worker

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"strange.company/sc/internal/contracts"
	"strange.company/sc/internal/worker/result"
)

// The harness claim (plan 27 section 14.9).

const (
	maxClaimBytes = 512 << 10
	claimRef      = "https://strange.company/contracts/worker-result/v1alpha1#/$defs/HarnessClaim"
)

var (
	claimOnce   sync.Once
	claimSchema *jsonschema.Schema
	claimErr    error
)

func claimValidator() (*jsonschema.Schema, error) {
	claimOnce.Do(func() {
		docs := map[string][]byte{}
		for _, n := range []string{"worker-result", "handoff", "context", "worker-input"} {
			docs[n+".schema.yaml"] = EmbeddedSchema(n)
		}
		claimSchema, claimErr = contracts.CompileSchemaBytesRef(docs, claimRef)
	})
	return claimSchema, claimErr
}

type claimDoc struct {
	raw     map[string]any
	State   string
	Summary string
}

// allowedStates are the result states each role may claim (the role registry's allowedResultStates; the Runner
// re-checks them).
func allowedClaimState(role, state string) bool {
	switch state {
	case "completed", "failed", "blocked":
		return true
	case "already_satisfied":
		return role == "tester"
	case "test_spec_conflict":
		return role == "implementer"
	}
	return false
}

// readClaim reads and validates /workspace/out/claim.json. why explains an unusable claim ("" when it is usable).
func (pl *pipeline) readClaim() (*claimDoc, string) {
	f, err := openRegular(pl.w.cfg.WorkspaceDir, "out/claim.json", os.O_RDONLY)
	if err != nil {
		if isRefusal(err) {
			pl.refuse(filepath.Join(pl.w.cfg.WorkspaceDir, "out", "claim.json"), err)
		}
		return nil, "claim.json absent"
	}
	defer f.Close()
	if st, err := f.Stat(); err != nil || st.Size() > maxClaimBytes {
		return nil, "claim.json larger than 512 KiB"
	}
	b, err := io.ReadAll(io.LimitReader(f, maxClaimBytes+1))
	if err != nil {
		return nil, "claim.json unreadable"
	}
	if len(b) > maxClaimBytes {
		return nil, "claim.json larger than 512 KiB"
	}
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, "claim.json is not JSON"
	}
	sc, err := claimValidator()
	if err != nil {
		return nil, "claim schema unavailable: " + oneLine(err.Error())
	}
	if err := sc.Validate(v); err != nil {
		return nil, "schema: " + oneLine(err.Error())
	}
	m, _ := v.(map[string]any)
	c := &claimDoc{raw: m}
	c.State, _ = m["state"].(string)
	c.Summary, _ = m["summary"].(string)
	role := pl.p.Execution.Role
	if !allowedClaimState(role, c.State) {
		return nil, fmt.Sprintf("state %q is not allowed for role %s", c.State, role)
	}
	if ro, ok := m["role_output"].(map[string]any); ok {
		if kind, _ := ro["kind"].(string); kind != roleOutputKind[role] {
			return nil, fmt.Sprintf("role_output.kind %q, want %q", kind, roleOutputKind[role])
		}
	}
	if hb, err := result.Canonical(m["handoff"]); err != nil || (pl.p.Limits.MaxHandoffBytes > 0 && len(hb) > pl.p.Limits.MaxHandoffBytes) {
		return nil, "handoff exceeds max_handoff_bytes"
	}
	return c, ""
}
