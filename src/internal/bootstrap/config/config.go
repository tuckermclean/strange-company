// Package config normalizes bootstrap.env into a BootstrapConfig (plan 50 section 9).
package config

import "strange.company/sc/internal/clock"

// Row is one row of the plan 50 section 9.2 variable table. Vars lists the variable names of the row in table
// order; names with a placeholder (S3_CRED_<INSTANCE>_...) keep the placeholder.
type Row struct {
	Vars        []string
	Required    string
	Default     string
	Field       string
	SecretClass string
}

// Options are the inputs of Normalize that do not come from bootstrap.env.
type Options struct {
	Clock      clock.Clock
	InstallUID string
	Version    string
	// Profile is the --profile flag; it overrides PROFILE.
	Profile string
}

// Result is a normalized BootstrapConfig: the YAML document and its parsed form.
type Result struct {
	YAML []byte
	Doc  map[string]any
}

// Error is a configuration refusal; ExitCode is the plan 50 section 7.2 code.
type Error struct {
	Code     string
	ExitCode int
	Message  string
}

// SC-STUB(F08 50-AC-1)
func (e *Error) Error() string { return "" }

// SC-STUB(F08 50-AC-1 50-AC-12)
func Rows() []Row { return nil }

// SC-STUB(F08 50-AC-1)
func ParseEnv(data []byte) (map[string]string, error) { return nil, nil }

// SC-STUB(F08 50-AC-1 50-AC-3 50-AC-7)
func Normalize(env map[string]string, opt Options) (*Result, error) { return nil, nil }

// SC-STUB(F08 50-AC-12)
func ExampleEnv() []byte { return nil }

// SC-STUB(F08 50-AC-12)
func CheckExample(example []byte) []string { return nil }
