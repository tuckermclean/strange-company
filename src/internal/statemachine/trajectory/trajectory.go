// Package trajectory loads the trajectory files test/e2e/tdd/<test>.yaml (plan 21 section 22.3). The files are
// plain data shared by the library runner (F05, make test-tdd-library) and the live runner (C10); this package
// has no dependency on the state machine, it only parses and validates the file format.
package trajectory

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

// Error is a malformed trajectory file or directory.
type Error struct{ Code, File, Message string }

// Error renders "<code> <file>: <message>".
//
// SC-STUB(F05 21-AC-20)
func (e *Error) Error() string { return "" }

// Dispatch is the dispatch being made by a dispatch event (the fingerprint inputs of plan 29 section 16.4).
type Dispatch struct {
	Harness           string   `yaml:"harness"`
	Model             string   `yaml:"model"`
	Profile           string   `yaml:"profile"`
	StartCommit       string   `yaml:"start_commit"`
	ACVersion         int      `yaml:"ac_version"`
	Notes             string   `yaml:"notes"`
	ContextRefs       []string `yaml:"context_refs"`
	FailureSignatures []string `yaml:"failure_signatures"`
}

// Event is one input event of a trajectory.
type Event struct {
	Event            string         `yaml:"event"`
	Actor            string         `yaml:"actor"`
	Role             string         `yaml:"role"`
	At               string         `yaml:"at"`
	Dispatch         *Dispatch      `yaml:"dispatch"`
	Facts            map[string]any `yaml:"facts"`
	FailureCode      string         `yaml:"failure_code"`
	FailureSignature string         `yaml:"failure_signature"`
	Reason           string         `yaml:"reason"`
	Pipeline         int            `yaml:"pipeline"`
	Live             map[string]any `yaml:"live"`
}

// Trajectory is one test/e2e/tdd file.
type Trajectory struct {
	Name                 string         `yaml:"trajectory"`
	Title                string         `yaml:"title"`
	Refs                 []string       `yaml:"refs"`
	Mode                 string         `yaml:"mode"`
	Task                 map[string]any `yaml:"task"`
	Events               []Event        `yaml:"events"`
	ExpectedStates       []string       `yaml:"expected_states"`
	ExpectedTransitions  []string       `yaml:"expected_transitions"`
	ExpectedReasons      []string       `yaml:"expected_reasons"`
	ExpectedFailedGuards [][]string     `yaml:"expected_failed_guards"`
	ExpectedAttempts     map[string]int `yaml:"expected_attempts"`
	Live                 map[string]any `yaml:"live"`
	File                 string         `yaml:"-"`
}

// Parse parses and validates one trajectory file; name is the file name (its stem must equal the trajectory field).
// A malformed file is a *Error with the code of the first rule it breaks: invalid YAML or a duplicate key
// (CodeYAML), an unknown key anywhere (CodeUnknownField), a missing trajectory, events, expected_states,
// expected_transitions or expected_reasons, or an event without event, actor or at (CodeMissingField), a name that
// is not the file stem (CodeNameMismatch), a mode other than test-first and verification-first (the default is
// test-first), an at that is not RFC 3339, or a fact that is neither a boolean nor a string (CodeBadValue), and
// expected_* lists whose length differs from the events (CodeLengthMismatch). The result of an error is the zero
// Trajectory.
//
// SC-STUB(F05 21-AC-20)
func Parse(name string, b []byte) (Trajectory, error) { return Trajectory{}, nil }

// Load parses every *.yaml file of dir in file-name order. A directory without trajectory files is a CodeEmpty
// error, a directory that cannot be read an error, and the first malformed file ends the load with its error.
//
// SC-STUB(F05 21-AC-20)
func Load(dir string) ([]Trajectory, error) { return nil, nil }
