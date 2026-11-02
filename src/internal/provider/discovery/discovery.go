// Package discovery is the shared node discovery library of plan 05 section 14.2: the fixed read-only POSIX script that
// a node runs over SSH, and the pure functions that turn its JSON output and the ssh-keyscan lines into a normalized
// ProviderOutput node (plan 05 section 7.2) plus the validation findings of section 14.5.
package discovery

// Node is one EXISTING_NODES entry with its EXISTING_NODE_OVERRIDES.
// SC-STUB(F09 05-AC-2)
type Node struct {
	Name               string `json:"name"`
	Host               string `json:"host"`
	Port               int    `json:"port"`
	User               string `json:"user"`
	InternalIP         string `json:"internal_ip,omitempty"`
	DiskClassOverride  string `json:"disk_class_override,omitempty"`
	HostKeyFingerprint string `json:"host_key_fingerprint,omitempty"`
}

// Input is what is known about one node: its configuration, the JSON the script printed and the ssh-keyscan output.
// SC-STUB(F09 05-AC-2)
type Input struct {
	Node     Node
	Facts    []byte
	HostKeys []string
}

// Context is what the evaluation needs besides the nodes.
// SC-STUB(F09 05-AC-2)
type Context struct {
	// ProfileFile is the effective cluster profile (infra/profiles/<profile>.yaml).
	ProfileFile string
	// InstallUID is this installation's uid, compared with /etc/strange-company/install-uid.
	InstallUID string
}

// Finding is one validation result: Code is the plan 05 section 14.5 code without any suffix (the port or command goes in
// Message), Severity is info, warn or fail.
// SC-STUB(F09 05-AC-2)
type Finding struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
}

// Result is the evaluation of one input. Node is the ProviderOutput node (contracts/provider.schema.yaml #/$defs/node)
// as a JSON-compatible value; Findings are the same findings as Node.substrate_validation.findings, including those that
// involve other nodes (EXISTING_MIXED_SIZE_CLASS, EXISTING_DUPLICATE_NODE, internal IP uniqueness).
// SC-STUB(F09 05-AC-2)
type Result struct {
	Node     map[string]any
	Findings []Finding
}

// Script returns the embedded discovery script (POSIX sh, read from stdin by `sh -s`): it prints one JSON document
// (test/fixtures/existing/discovery/README.md) and changes nothing on the node.
// SC-STUB(F09 05-AC-2 05-AC-3)
func Script() []byte { return nil }

// Evaluate normalizes and validates the nodes. Results are in the order of in, and node roles follow name order. Every
// failed check is a Finding of severity fail (also DISCOVERY_TOOL_MISSING and HOST_KEY_MISMATCH); the error is reserved
// for input that cannot be decoded at all.
// SC-STUB(F09 05-AC-2)
func Evaluate(in []Input, c Context) ([]Result, error) { return nil, nil }
