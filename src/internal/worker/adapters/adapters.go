// Package adapters holds the harness adapters of sc-worker (plan 28 section 8.1).
package adapters

// File is a config file an adapter asks sc-worker to place under $HOME.
type File struct {
	Path    string `json:"path"`
	Mode    uint32 `json:"mode"`
	Content string `json:"content"`
}

// Prepared is the output of Prepare: the argv, the complete child environment (token values only as
// @file:<name> references) and the config files.
type Prepared struct {
	Argv  []string          `json:"argv"`
	Env   map[string]string `json:"env"`
	Files []File            `json:"files"`
}

// FakeKnobs are the fake harness knobs of plan 28 section 8.2 (28-R-5), all comparable so that "all off" is
// FakeKnobs{}.
type FakeKnobs struct {
	SkipPrechecks       bool
	SkipReport          bool
	RawResult           bool
	DisableCancel       bool
	SkipFold            bool
	PushExtraRefs       bool
	ParentOverride      string
	ClaimCommitOverride string
}

// SC-STUB(F07 28-AC-5)
func Prepare(packetJSON []byte) (Prepared, error) { return Prepared{}, nil }

// SC-STUB(F07 28-AC-6)
func DeriveFakeKnobs(packetJSON []byte) (FakeKnobs, error) { return FakeKnobs{}, nil }

// SC-STUB(F07 28-AC-6)
func PrechecksEnabled(packetJSON []byte) (bool, error) { return false, nil }
