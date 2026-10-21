package verify

import "strange.company/sc/internal/policy"

// GreenInput is everything ClassifyGreen needs. Observed is the CI-written observation of the head, Integrity the
// facts the Runner derived from Git, Manifest the manifest of the generation under judgement, Baseline the RED
// observation whose test_id_sets hold the full-suite base_passing and base_skipped sets (G5, G5b). Isolation is the
// Runner's own derivation of the files the isolation run must remove. ProtectedTestIDs and RequiredChecks come from
// the validation.yaml of the base revision. ApprovedRemovedTests are test ids whose file an approved generation
// deleted (G5b).
type GreenInput struct {
	Observed             GreenObservation
	Integrity            Integrity
	Manifest             Manifest
	Baseline             Observation
	Identity             Identity
	Isolation            IsolationFacts
	ProtectedTestIDs     []string
	RequiredChecks       []string
	ApprovedRemovedTests []string
	Platform             policy.Platform
	Effective            policy.Effective
	ComputedAt           string
	PolicyRevision       string
}

// CheckRun mirrors green-result $defs/CheckRun.
type CheckRun struct {
	Name       string `json:"name"`
	ArgvSha256 string `json:"argv_sha256,omitempty"`
	ExitCode   int    `json:"exit_code"`
	DurationMs int    `json:"duration_ms"`
	TimedOut   bool   `json:"timed_out"`
	Required   bool   `json:"required"`
	OutputTail string `json:"output_tail,omitempty"`
}

// ReplayTest is one test of the isolation or red replay run.
type ReplayTest struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

// IsolationRun mirrors GreenObservation.isolation.
type IsolationRun struct {
	RemovedFiles       int          `json:"removed_files"`
	RemovedFilesSha256 string       `json:"removed_files_sha256"`
	Tests              []ReplayTest `json:"tests"`
}

// RedReplay mirrors GreenObservation.red_replay.
type RedReplay struct {
	RedCommit string       `json:"red_commit"`
	Tests     []ReplayTest `json:"tests"`
}

// GreenTestIDSets mirrors GreenObservation.test_id_sets.
type GreenTestIDSets struct {
	HeadPresent *TestIDSet `json:"head_present,omitempty"`
	HeadSkipped *TestIDSet `json:"head_skipped,omitempty"`
}

// GreenRepeats mirrors GreenObservation.repeats.
type GreenRepeats struct {
	Runs         int      `json:"runs"`
	Inconsistent []string `json:"inconsistent"`
}

// GreenObservation mirrors green-result $defs/GreenObservation (judge fields belong to the judge slice).
type GreenObservation struct {
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
	Checks         []CheckRun        `json:"checks"`
	Tests          []TestObservation `json:"tests"`
	TestsTruncated bool              `json:"tests_truncated"`
	TestIDSets     *GreenTestIDSets  `json:"test_id_sets,omitempty"`
	Isolation      *IsolationRun     `json:"isolation,omitempty"`
	RedReplay      *RedReplay        `json:"red_replay,omitempty"`
	Repeats        *GreenRepeats     `json:"repeats,omitempty"`
	ObservedAt     string            `json:"observed_at"`
}

// IntegrityFile mirrors Integrity.manifest_files items; a nil ActualBlob means the file is absent at the head.
type IntegrityFile struct {
	Path         string  `json:"path"`
	ExpectedBlob string  `json:"expected_blob"`
	ActualBlob   *string `json:"actual_blob"`
	Status       string  `json:"status"`
}

// DiscoveryChange mirrors Integrity.discovery_changes items.
type DiscoveryChange struct {
	Path string `json:"path"`
	Key  string `json:"key,omitempty"`
	Kind string `json:"kind"`
}

// IntegrityFinding mirrors Integrity.static_findings items.
type IntegrityFinding struct {
	Code     string `json:"code"`
	Path     string `json:"path"`
	Line     int    `json:"line,omitempty"`
	Severity string `json:"severity"`
	Detail   string `json:"detail,omitempty"`
}

// Integrity mirrors green-result $defs/Integrity.
type Integrity struct {
	Generation            int                `json:"generation"`
	RedCommit             string             `json:"red_commit"`
	HeadDescendsFromRed   bool               `json:"head_descends_from_red"`
	ManifestDigest        string             `json:"manifest_digest"`
	ManifestFiles         []IntegrityFile    `json:"manifest_files"`
	ProtectedTestsMissing []string           `json:"protected_tests_missing"`
	DiscoveryChanges      []DiscoveryChange  `json:"discovery_changes"`
	StaticFindings        []IntegrityFinding `json:"static_findings"`
	StubsRemaining        []string           `json:"stubs_remaining"`
	ForeignCommits        []string           `json:"foreign_commits"`
}

// IntegrityInput is what DeriveIntegrity needs: the repository, the head, the manifest (its TestCommit is the red
// commit), the ingestion violations of red..head (CheckIngestion), and the recorded accepted result commits and
// human_commits of the task.
type IntegrityInput struct {
	Git             GitRepo
	Head            string
	Manifest        Manifest
	Violations      []Violation
	AcceptedCommits []string
	HumanCommits    []string
}

// IsolationFilesInput names the range and the policies that classify test files.
type IsolationFilesInput struct {
	Git       GitRepo
	Red       string
	Head      string
	Platform  policy.Platform
	Effective policy.Effective
}

// IsolationFacts are the files the isolation run must remove, sorted, with their count and the sha256 (hex) of the
// paths joined by a line feed.
type IsolationFacts struct {
	Files  []string
	Count  int
	Sha256 string
}

// GreenClassification mirrors green-result $defs/GreenClassification.
type GreenClassification struct {
	Outcome            string   `json:"outcome"`
	Reasons            []string `json:"reasons"`
	Warnings           []string `json:"warnings,omitempty"`
	ComputedBy         string   `json:"computed_by"`
	ComputedAt         string   `json:"computed_at"`
	PolicyRevision     string   `json:"policy_revision"`
	VerifierLibVersion string   `json:"verifier_lib_version"`
}
