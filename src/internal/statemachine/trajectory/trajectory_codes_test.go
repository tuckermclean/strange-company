package trajectory

// Codes moved here from the declared stub file: a RED commit may not add a const
// declaration outside test paths (plan 52 section 8.3a); the GREEN commit restores them.

// Error codes of Parse and Load.
const (
	CodeYAML           = "TRAJ_YAML"
	CodeUnknownField   = "TRAJ_UNKNOWN_FIELD"
	CodeMissingField   = "TRAJ_MISSING_FIELD"
	CodeLengthMismatch = "TRAJ_LENGTH_MISMATCH"
	CodeNameMismatch   = "TRAJ_NAME_MISMATCH"
	CodeBadValue       = "TRAJ_BAD_VALUE"
	CodeEmpty          = "TRAJ_EMPTY"
)
