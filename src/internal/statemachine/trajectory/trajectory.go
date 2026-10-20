// Package trajectory loads the trajectory files test/e2e/tdd/<test>.yaml (plan 21 section 22.3). The files are
// plain data shared by the library runner (F05, make test-tdd-library) and the live runner (C10); this package
// has no dependency on the state machine, it only parses and validates the file format.
package trajectory

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

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
func (e *Error) Error() string { return fmt.Sprintf("%s %s: %s", e.Code, e.File, e.Message) }

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
func Parse(name string, b []byte) (Trajectory, error) {
	fail := func(code, format string, a ...any) (Trajectory, error) {
		return Trajectory{}, &Error{Code: code, File: name, Message: fmt.Sprintf(format, a...)}
	}
	// Pass 1: plain YAML. Syntax errors and duplicate keys are rejected here.
	var generic any
	if err := yaml.NewDecoder(bytes.NewReader(b)).Decode(&generic); err != nil {
		return fail(CodeYAML, "%v", err)
	}
	if _, ok := generic.(map[string]any); !ok {
		return fail(CodeYAML, "the document must be a mapping")
	}
	// Pass 2: the typed form; an unknown key anywhere is an error.
	var tr Trajectory
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(&tr); err != nil {
		code := CodeBadValue
		var te *yaml.TypeError
		if errors.As(err, &te) {
			for _, m := range te.Errors {
				if strings.Contains(m, "not found in type") {
					code = CodeUnknownField
					break
				}
			}
		}
		return fail(code, "%v", err)
	}
	if tr.Name == "" {
		return fail(CodeMissingField, "trajectory is missing")
	}
	if len(tr.Events) == 0 {
		return fail(CodeMissingField, "events is missing or empty")
	}
	for field, n := range map[string]int{"expected_states": len(tr.ExpectedStates), "expected_transitions": len(tr.ExpectedTransitions), "expected_reasons": len(tr.ExpectedReasons)} {
		if n == 0 {
			return fail(CodeMissingField, "%s is missing or empty", field)
		}
	}
	for i, e := range tr.Events {
		for field, v := range map[string]string{"event": e.Event, "actor": e.Actor, "at": e.At} {
			if v == "" {
				return fail(CodeMissingField, "events[%d].%s is missing", i, field)
			}
		}
	}
	if stem := strings.TrimSuffix(filepath.Base(name), filepath.Ext(name)); stem != tr.Name {
		return fail(CodeNameMismatch, "trajectory %q is not the file stem %q", tr.Name, stem)
	}
	switch tr.Mode {
	case "":
		tr.Mode = "test-first"
	case "test-first", "verification-first":
	default:
		return fail(CodeBadValue, "mode %q is neither test-first nor verification-first", tr.Mode)
	}
	for i, e := range tr.Events {
		if _, err := time.Parse(time.RFC3339, e.At); err != nil {
			return fail(CodeBadValue, "events[%d].at %q is not RFC 3339", i, e.At)
		}
		keys := make([]string, 0, len(e.Facts))
		for k := range e.Facts {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			switch e.Facts[k].(type) {
			case bool, string:
			default:
				return fail(CodeBadValue, "events[%d].facts.%s must be a boolean or a string", i, k)
			}
		}
	}
	n := len(tr.Events)
	if len(tr.ExpectedStates) != n || len(tr.ExpectedTransitions) != n || len(tr.ExpectedReasons) != n ||
		(tr.ExpectedFailedGuards != nil && len(tr.ExpectedFailedGuards) != n) {
		return fail(CodeLengthMismatch, "the expected_* lists must have one entry per event (%d)", n)
	}
	tr.File = name
	return tr, nil
}

// Load parses every *.yaml file of dir in file-name order. A directory without trajectory files is a CodeEmpty
// error, a directory that cannot be read an error, and the first malformed file ends the load with its error.
func Load(dir string) ([]Trajectory, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []Trajectory
	for _, e := range entries { // ReadDir sorts by file name
		if e.IsDir() || filepath.Ext(e.Name()) != ".yaml" {
			continue
		}
		p := filepath.Join(dir, e.Name())
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		tr, err := Parse(p, b)
		if err != nil {
			return nil, err
		}
		out = append(out, tr)
	}
	if len(out) == 0 {
		return nil, &Error{Code: CodeEmpty, File: dir, Message: "no trajectory files (*.yaml)"}
	}
	return out, nil
}
