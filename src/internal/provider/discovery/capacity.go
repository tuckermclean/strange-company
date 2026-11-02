package discovery

// Capacity is the hardware a provider reports for a node it created (the fake provider's scenario knobs): there are no
// facts to discover, only the numbers of plan 03 section 14.3.
type Capacity struct {
	CPU       int64
	Memory    int64 // bytes
	Disk      int64 // bytes of the root disk
	DiskClass string
}

// ValidateCapacity compares one node's capacity with the effective profile in profileFile exactly as Evaluate does for a
// discovered node (0.92 tolerance, constrained lower bound, minimum, preferred) and returns the substrate_validation
// object of the ProviderOutput node (contracts/provider.schema.yaml #/$defs/node) together with its findings.
func ValidateCapacity(profileFile, nodeName string, c Capacity) (map[string]any, []Finding, error) {
	prof, err := loadProfile(profileFile)
	if err != nil {
		return nil, nil, err
	}
	e := &eval{cpu: c.CPU, memory: c.Memory, diskSize: c.Disk, diskClass: c.DiskClass}
	e.capacity(prof, nodeName)
	fs := make([]any, 0, len(e.findings))
	for _, f := range e.findings {
		fs = append(fs, map[string]any{"code": f.Code, "severity": f.Severity, "message": f.Message})
	}
	var pref any = e.meetsPref
	sv := map[string]any{"profile": prof.name, "meets_minimum": e.meetsMinimum, "meets_preferred": pref, "findings": fs}
	return sv, append([]Finding(nil), e.findings...), nil
}
