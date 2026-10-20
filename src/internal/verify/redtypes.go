// Package verify is the verifier library of plan 22 (F06): adapters, red classification, manifests, ingestion and
// green classification. Every function is pure over its inputs, except the Git derivation helper (later slice).
package verify

import "strange.company/sc/internal/policy"

// Adapter is one entry of spec.adapters in the platform verification policy (plan 22 section 9.1).
type Adapter struct {
	Name                 string
	Format               string
	IDTemplate           string
	ErrorTypes           []string
	AssertionTypes       []string
	BuildFailurePatterns []string
	TimeoutPatterns      []string
	// PanicMessage is spec.stubPolicy.panicMessage of the policy the adapter was loaded from; a failure message
	// containing it is a stub_unimplemented result. Empty: no result is classified as a stub.
	PanicMessage string
}

// TestResult is one test of a normalized report (plan 22 section 14.1 step 5). ID is `<suite>::<id>`.
type TestResult struct {
	ID          string
	File        string
	Status      string
	FailureType string
	Message     string
}

// Report is a normalized suite report. A missing or unparsable report is an error, never an empty Report.
type Report struct {
	Tests       []TestResult
	BuildFailed bool
	TimedOut    bool
}

// StubPolicy is spec.stubPolicy of the platform policy.
type StubPolicy struct {
	Marker               string
	PanicMessage         string
	MaxAddedLinesPerStub int
	MaxStubsPerTask      int
	AllowPanicStubsFor   []string
}

// StubFile is one file of the red range under stubPaths: the content at the generation start (nil for a new file)
// and at the red commit. Language is a policy language name (go, rust, ...).
type StubFile struct {
	Path     string
	Language string
	Base     []byte
	Head     []byte
}

// StubFact is a stub the library derived from the source (the Scope.stubs entry of red-result).
type StubFact struct {
	Path       string `json:"path"`
	Symbol     string `json:"symbol"`
	Marker     string `json:"marker"`
	Shape      string `json:"shape"`
	WellFormed bool   `json:"well_formed"`
}

// Identity carries the Runner's pipeline identity verdict (plan 20 I1-I7) and the base revision's validation blob.
type Identity struct {
	Passed             bool   `json:"passed"`
	BaseValidationBlob string `json:"base_validation_blob"`
}

// Criterion is one frozen acceptance criterion.
type Criterion struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
}

// NotClassifiedError reports a RED that cannot be classified (rule R0, verification_infra_failed).
type NotClassifiedError struct {
	Code string
}

// RedInput is everything ClassifyRed needs (plan 22 section 14.3). Declared maps an AC id to the tester's test
// names (exact TestIds or `::`-suffixes). SuiteLanguages maps a suite name to its language. HeadScope maps a suite to
// the Go package import paths the head run was limited to; a suite not listed ran unscoped.
type RedInput struct {
	Observed       Observation
	Scope          Scope
	Identity       Identity
	Declared       map[string][]string
	Criteria       []Criterion
	SuiteLanguages map[string]string
	HeadScope      map[string][]string
	Platform       policy.Platform
	Effective      policy.Effective
	ComputedAt     string
	PolicyRevision string
}

// TestIDSet mirrors $defs/TestIdSet.
type TestIDSet struct {
	Count  int    `json:"count"`
	Sha256 string `json:"sha256"`
	Data   string `json:"data"`
}

// TestIDSets mirrors Observation.test_id_sets.
type TestIDSets struct {
	BasePassing *TestIDSet `json:"base_passing,omitempty"`
	BaseSkipped *TestIDSet `json:"base_skipped,omitempty"`
	HeadPresent *TestIDSet `json:"head_present,omitempty"`
}

// Counts mirrors RunSummary.counts.
type Counts struct {
	Total             int `json:"total"`
	Passed            int `json:"passed"`
	FailedAssertion   int `json:"failed_assertion"`
	FailedError       int `json:"failed_error"`
	StubUnimplemented int `json:"stub_unimplemented"`
	Skipped           int `json:"skipped"`
}

// RunSummary mirrors $defs/RunSummary.
type RunSummary struct {
	Tree        string `json:"tree"`
	Commit      string `json:"commit"`
	Suite       string `json:"suite"`
	Adapter     string `json:"adapter"`
	ArgvSha256  string `json:"argv_sha256,omitempty"`
	ExitCode    int    `json:"exit_code"`
	DurationMs  int    `json:"duration_ms"`
	TimedOut    bool   `json:"timed_out"`
	BuildFailed bool   `json:"build_failed"`
	ReportFound bool   `json:"report_found"`
	Counts      Counts `json:"counts"`
	OutputTail  string `json:"output_tail,omitempty"`
}

// TestObservation mirrors $defs/TestObservation.
type TestObservation struct {
	ID          string `json:"id"`
	Suite       string `json:"suite"`
	File        string `json:"file,omitempty"`
	StatusBase  string `json:"status_base,omitempty"`
	StatusHead  string `json:"status_head"`
	FailureType string `json:"failure_type,omitempty"`
	Message     string `json:"message,omitempty"`
	DurationMs  int    `json:"duration_ms,omitempty"`
}

// Verifier mirrors Observation.verifier.
type Verifier struct {
	Version      string `json:"version"`
	BinarySha256 string `json:"binary_sha256"`
}

// Observation mirrors $defs/Observation (judge fields are added by the judge slice).
type Observation struct {
	TaskID         string            `json:"task_id"`
	Repository     string            `json:"repository"`
	Phase          string            `json:"phase"`
	Subject        string            `json:"subject"`
	IntentID       string            `json:"intent_id"`
	BaseSha        string            `json:"base_sha"`
	HeadSha        string            `json:"head_sha"`
	Verifier       Verifier          `json:"verifier"`
	ValidationBlob string            `json:"validation_blob"`
	ToolchainImage string            `json:"toolchain_image"`
	Runs           []RunSummary      `json:"runs"`
	Tests          []TestObservation `json:"tests"`
	TestsTruncated bool              `json:"tests_truncated"`
	TestIDSets     *TestIDSets       `json:"test_id_sets,omitempty"`
	ObservedAt     string            `json:"observed_at"`
}

// ChangedFile mirrors Scope.changed_files items.
type ChangedFile struct {
	Path    string `json:"path"`
	Change  string `json:"change"`
	Class   string `json:"class"`
	BlobSha string `json:"blob_sha,omitempty"`
}

// StaticFinding mirrors Scope.static_findings items.
type StaticFinding struct {
	Code   string `json:"code"`
	Path   string `json:"path"`
	Line   int    `json:"line,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// Scope mirrors $defs/Scope.
type Scope struct {
	Generation            int             `json:"generation"`
	GenerationStartCommit string          `json:"generation_start_commit"`
	ChangedFiles          []ChangedFile   `json:"changed_files"`
	Stubs                 []StubFact      `json:"stubs"`
	StaticFindings        []StaticFinding `json:"static_findings"`
}

// ACResult mirrors RedClassification.ac_results items.
type ACResult struct {
	ACID    string   `json:"ac_id"`
	Kind    string   `json:"kind"`
	Tests   []string `json:"tests"`
	Outcome string   `json:"outcome"`
}

// RedClassification mirrors $defs/RedClassification.
type RedClassification struct {
	Outcome            string     `json:"outcome"`
	Reasons            []string   `json:"reasons"`
	ACResults          []ACResult `json:"ac_results"`
	NewTests           []string   `json:"new_tests"`
	Warnings           []string   `json:"warnings,omitempty"`
	ComputedBy         string     `json:"computed_by"`
	ComputedAt         string     `json:"computed_at"`
	PolicyRevision     string     `json:"policy_revision"`
	VerifierLibVersion string     `json:"verifier_lib_version"`
}
