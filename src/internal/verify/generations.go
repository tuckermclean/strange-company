package verify

// TestFile is one test-, fixture- or test-infra-classified file of the red range: its content at the generation
// start (nil for a new file) and at the red commit (nil for a deleted file). Language is a policy language name.
type TestFile struct {
	Path     string
	Language string
	Base     []byte
	Head     []byte
}

// Repeats are the repeat-run facts of the head run (plan 22 section 14.1 step 4): Runs is the number of times the
// new and declared tests ran (one of them on a marker-stripped copy), Inconsistent the test ids whose status
// changed between plain runs, MarkerSensitive the test ids whose status changed on the marker-stripped run.
type Repeats struct {
	Runs            int      `json:"runs"`
	Inconsistent    []string `json:"inconsistent"`
	MarkerSensitive []string `json:"marker_sensitive"`
}

// GenerationInput is RedInput plus the facts of rules R1b, R11b and R12-R14. Opening is "" for a first
// generation, "strengthened" or "test_change_approved"; ApprovedACIDs are the conflict's approved criteria.
type GenerationInput struct {
	RedInput
	Opening       string
	ApprovedACIDs []string
	Previous      *Manifest
	TestFiles     []TestFile
	Repeats       *Repeats
}

// ManifestFile mirrors test-manifest files items.
type ManifestFile struct {
	Path    string `json:"path"`
	BlobSha string `json:"blob_sha"`
	Class   string `json:"class"`
	Change  string `json:"change"`
}

// ManifestTest mirrors test-manifest tests items.
type ManifestTest struct {
	ID        string   `json:"id"`
	Suite     string   `json:"suite"`
	Kind      string   `json:"kind"`
	ACIDs     []string `json:"ac_ids"`
	RedStatus string   `json:"red_status"`
}

// ManifestACMapping mirrors test-manifest ac_mapping items.
type ManifestACMapping struct {
	ACID  string   `json:"ac_id"`
	Kind  string   `json:"kind"`
	Tests []string `json:"tests"`
}

// ManifestStub mirrors test-manifest stubs items.
type ManifestStub struct {
	Path     string   `json:"path"`
	Symbol   string   `json:"symbol"`
	Marker   string   `json:"marker"`
	Shape    string   `json:"shape"`
	Language string   `json:"language,omitempty"`
	ACIDs    []string `json:"ac_ids"`
}

// WholeFileProtection mirrors discovery_protection.whole_files items; a nil BlobSha means the file is absent.
type WholeFileProtection struct {
	Path    string  `json:"path"`
	BlobSha *string `json:"blob_sha"`
}

// SemanticKeyProtection mirrors discovery_protection.semantic_keys items; a nil ValueSha256 means the key is absent.
type SemanticKeyProtection struct {
	Path        string  `json:"path"`
	Key         string  `json:"key"`
	ValueSha256 *string `json:"value_sha256"`
}

// DiscoveryProtection mirrors test-manifest discovery_protection.
type DiscoveryProtection struct {
	WholeFiles   []WholeFileProtection   `json:"whole_files"`
	SemanticKeys []SemanticKeyProtection `json:"semantic_keys"`
}

// ManifestTestPaths mirrors test-manifest test_paths.
type ManifestTestPaths struct {
	Test      []string `json:"test"`
	Fixture   []string `json:"fixture"`
	TestInfra []string `json:"test_infra"`
}

// ManifestSupersedes mirrors test-manifest supersedes.
type ManifestSupersedes struct {
	Generation int    `json:"generation"`
	Digest     string `json:"digest"`
	Reason     string `json:"reason"`
}

// Manifest mirrors contracts/test-manifest.schema.yaml.
type Manifest struct {
	SchemaVersion         string              `json:"schema_version"`
	Kind                  string              `json:"kind"`
	TaskID                string              `json:"task_id"`
	Generation            int                 `json:"generation"`
	ACVersion             int                 `json:"ac_version"`
	CreatedAt             string              `json:"created_at"`
	Repository            string              `json:"repository"`
	BaseCommit            string              `json:"base_commit"`
	GenerationStartCommit string              `json:"generation_start_commit"`
	TestCommit            string              `json:"test_commit"`
	RedEvidenceDigest     string              `json:"red_evidence_digest"`
	PolicyRevision        string              `json:"policy_revision,omitempty"`
	Files                 []ManifestFile      `json:"files"`
	Tests                 []ManifestTest      `json:"tests"`
	ACMapping             []ManifestACMapping `json:"ac_mapping"`
	Stubs                 []ManifestStub      `json:"stubs"`
	DiscoveryProtection   DiscoveryProtection `json:"discovery_protection"`
	TestPaths             ManifestTestPaths   `json:"test_paths"`
	Supersedes            *ManifestSupersedes `json:"supersedes,omitempty"`
	Digest                string              `json:"digest"`
}

// ManifestFacts are the Runner-derived facts BuildManifest needs besides the classified RED: identities, the
// discovery-protection snapshot read from Git at the red commit, the previous generation's manifest and, with it,
// the reason of the supersession.
type ManifestFacts struct {
	TaskID                string
	Repository            string
	Generation            int
	ACVersion             int
	BaseCommit            string
	GenerationStartCommit string
	TestCommit            string
	RedEvidenceDigest     string
	CreatedAt             string
	DiscoveryProtection   DiscoveryProtection
	Previous              *Manifest
	SupersedesReason      string
}

// ManifestInput is the classified RED (its input and classification) and the Git facts.
type ManifestInput struct {
	Red            RedInput
	Classification RedClassification
	Facts          ManifestFacts
}
