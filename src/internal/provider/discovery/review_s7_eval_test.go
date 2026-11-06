package discovery_test

import (
	"testing"

	"strange.company/sc/internal/provider/discovery"
)

// S7 review findings on the evaluation (05-AC-2, plan 05 section 14.5).

func fsEntry(e *env) map[string]any {
	return e.facts["findmnt"].(map[string]any)["filesystems"].([]any)[0].(map[string]any)
}

// 05-AC-2 (plan 05 section 14.2: "a missing command yields DISCOVERY_TOOL_MISSING (fail) rather than a guessed value"):
// a findmnt entry without size or avail is a discovery failure, never the value 0, which would read as a full disk.
// Decision: Evaluate returns no error and the node carries a DISCOVERY_TOOL_MISSING fail finding; NODE_DISK_FREE_LOW is
// not reported from the absent value.
func TestEvaluate_FindmntWithoutSizeOrAvailIsDiscoveryFailure_05AC2(t *testing.T) {
	cases := map[string]func(fs map[string]any){
		"avail-missing": func(fs map[string]any) { delete(fs, "avail") },
		"avail-null":    func(fs map[string]any) { fs["avail"] = nil },
		"size-missing":  func(fs map[string]any) { delete(fs, "size") },
		"both-missing":  func(fs map[string]any) { delete(fs, "size"); delete(fs, "avail") },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			e := baseline(t)
			mutate(fsEntry(e))
			r := e.eval(t)
			codes := findingSet(r.Findings)
			if !codes["DISCOVERY_TOOL_MISSING:fail"] {
				t.Errorf("findings %v: want DISCOVERY_TOOL_MISSING:fail for a findmnt entry without size or avail", r.Findings)
			}
			if codeSet(r.Findings)["NODE_DISK_FREE_LOW"] {
				t.Errorf("findings %v: NODE_DISK_FREE_LOW was computed from an absent value (read as 0)", r.Findings)
			}
		})
	}
}

// 05-AC-2 (plan 04 04-AC-21): a node that meets the minimum but not the preferred disk (200 GiB NVMe) gets the warning
// BELOW_PREFERRED_DISK, the code plan 04 names; NODE_BELOW_PREFERRED is not a plan code.
func TestEvaluate_BelowPreferredDiskCode_05AC2(t *testing.T) {
	e := baseline(t)
	e.setDiskSize(150 * gib)
	r := e.eval(t)
	codes := findingSet(r.Findings)
	if !codes["BELOW_PREFERRED_DISK:warn"] {
		t.Errorf("findings %v: want BELOW_PREFERRED_DISK:warn for a 150 GiB NVMe node (preferred is 200 GiB)", r.Findings)
	}
	if codeSet(r.Findings)["NODE_BELOW_PREFERRED"] {
		t.Errorf("findings %v: NODE_BELOW_PREFERRED is not a plan code", r.Findings)
	}
}

// 05-AC-2 (plan 08 section 14.2 / CD-23): 8 GiB of memory passes as constrained with the warning NODE_CONSTRAINED, the
// code plan 08 names; NODE_CONSTRAINED_CAPACITY is not a plan code.
func TestEvaluate_ConstrainedNodeCode_05AC2(t *testing.T) {
	e := baseline(t)
	e.setMem(8 * gib)
	r := e.eval(t)
	codes := findingSet(r.Findings)
	if !codes["NODE_CONSTRAINED:warn"] {
		t.Errorf("findings %v: want NODE_CONSTRAINED:warn for an 8 GiB node", r.Findings)
	}
	if codeSet(r.Findings)["NODE_CONSTRAINED_CAPACITY"] {
		t.Errorf("findings %v: NODE_CONSTRAINED_CAPACITY is not a plan code", r.Findings)
	}
}

// 05-AC-2 (plan 05 section 14.5, size class): a node whose disk class cannot be discovered (DISCOVERY of a virtio disk,
// reported by DISK_CLASS_UNVERIFIED) is not a different capacity class: next to a standard node it must not produce
// EXISTING_MIXED_SIZE_CLASS.
func TestEvaluate_UnknownDiskClassIsNotAMixedSizeClass_05AC2(t *testing.T) {
	a := loadInput(t, "debian13-baremetal-nvme")
	b := loadInput(t, "debian13-virtio-unverified")
	b.Node.Name = "node-02"
	rs := evalAll(t, []discovery.Input{a, b}, ctxFor(t))
	if !findingSet(rs[1].Findings)["DISK_CLASS_UNVERIFIED:fail"] {
		t.Fatalf("node-02 findings %v: the fixture must be an unverified virtio disk", rs[1].Findings)
	}
	for i, r := range rs {
		if codeSet(r.Findings)["EXISTING_MIXED_SIZE_CLASS"] {
			t.Errorf("node-0%d findings %v: EXISTING_MIXED_SIZE_CLASS for a node whose only problem is an unknown disk class", i+1, r.Findings)
		}
	}
}
