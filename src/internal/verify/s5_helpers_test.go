package verify

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"strange.company/sc/internal/contracts"
)

// Slice S5 (green) helpers.
//
// Interface choices of S5 (plan 22 section 14.7 and the green-result contract are silent on the Go signatures):
//   - ClassifyGreen(GreenInput) (GreenClassification, error): pure over the observed document, the Runner's Integrity
//     facts, the manifest, the RED observation (full-suite id sets) and the policies. Outcomes and reasons are those of
//     green-result.schema.yaml; the first failing rule in table order decides the outcome, every failing rule adds
//     its reasons in rule order. Identity failure and unusable or inconsistent facts are *NotClassifiedError.
//   - DeriveIntegrity(IntegrityInput) (Integrity, error): real Git (rev-list, merge-base, ls-tree), the ingestion
//     violations of red..head passed in; foreign commits are the commits of red..head (RevList order) whose full
//     SHA is neither in AcceptedCommits nor in HumanCommits.
//   - DeriveIsolationFiles(IsolationFilesInput) (IsolationFacts, error): the test-, fixture- and test-infra-classified
//     files ADDED in red..head, sorted; Sha256 is hex(sha256(paths joined by "\n")).
//
// Fixtures: testdata/green/<rule>/<case>/{overlay.json,expect.json}. overlay.json is merged (JSON objects deep,
// everything else replaced, null clears) over the valid baseline greenFixture below; expect.json is the golden
// outcome. The fixtures are hand-written classification inputs (observed document + Runner facts), as plan 22
// section 22 "Unit (F06)" describes; every fact the library can derive from Git is derived in s5_derive_test.go
// from real scratch repositories.

const (
	g5Base = "0123456789abcdef0123456789abcdef01234567"
	g5Red  = "89abcdef0123456789abcdef0123456789abcdef"
	g5Head = "fedcba9876543210fedcba9876543210fedcba98"
	g5Blob = "3b18e512dba79e4c8300dd08aeb37f8e728b8dad"
)

func g5ID(name string) string { return "unit::example.com/x/pkg." + name }

type greenFixture struct {
	Identity  Identity         `json:"identity"`
	Manifest  Manifest         `json:"manifest"`
	Observed  GreenObservation `json:"observed"`
	Integrity Integrity        `json:"integrity"`
	IDSets    struct {
		BasePassing []string `json:"base_passing"`
		BaseSkipped []string `json:"base_skipped"`
		HeadPresent []string `json:"head_present"`
		HeadSkipped []string `json:"head_skipped"`
	} `json:"id_sets"`
	IsolationFiles       []string `json:"isolation_files"`
	ProtectedTestIDs     []string `json:"protected_test_ids"`
	RequiredChecks       []string `json:"required_checks"`
	ApprovedRemovedTests []string `json:"approved_removed_tests"`
}

type greenExpect struct {
	Error    string    `json:"error"`
	Outcome  string    `json:"outcome"`
	Reasons  *[]string `json:"reasons"`
	Contains []string  `json:"contains"`
	// OnlyFrom: the reasons are not empty and each is one of these.
	OnlyFrom []string `json:"only_from"`
}

func g5IsoFacts(files []string) IsolationFacts {
	f := append([]string{}, files...)
	sort.Strings(f)
	sum := sha256.Sum256([]byte(strings.Join(f, "\n")))
	return IsolationFacts{Files: f, Count: len(f), Sha256: hex.EncodeToString(sum[:])}
}

func g5Strs(l ...string) []string { return append([]string{}, l...) }

// g5BaseFixture is a valid GREEN: every rule G0-G9 passes.
func g5BaseFixture(t testing.TB) greenFixture {
	t.Helper()
	var fx greenFixture
	fx.Identity = Identity{Passed: true, BaseValidationBlob: "7d1a0c3e9b2f4a6c8e0d1f3a5b7c9e1d3f5a7b9c"}

	m1, m2, m3 := g5ID("TestNewBehavior"), g5ID("TestStubbed"), g5ID("TestKeepsWorking")
	fx.Manifest = Manifest{
		SchemaVersion: "v1alpha1", Kind: "test", TaskID: "sc-9", Generation: 1, ACVersion: 1, CreatedAt: "2026-10-13T12:20:01Z",
		Repository: "o/demo", BaseCommit: g5Base, GenerationStartCommit: g5Base, TestCommit: g5Red,
		RedEvidenceDigest: "sha256:" + strings.Repeat("ab", 32),
		Files:             []ManifestFile{{Path: "pkg/behavior_test.go", BlobSha: g5Blob, Class: "test", Change: "added"}},
		Tests: []ManifestTest{
			{ID: m1, Suite: "unit", Kind: "new", ACIDs: []string{"AC-1"}, RedStatus: "failed_assertion"},
			{ID: m2, Suite: "unit", Kind: "new", ACIDs: []string{"AC-2"}, RedStatus: "stub_unimplemented"},
			{ID: m3, Suite: "unit", Kind: "new", ACIDs: []string{"AC-3"}, RedStatus: "passed"},
		},
		ACMapping: []ManifestACMapping{
			{ACID: "AC-1", Kind: "change", Tests: []string{m1}},
			{ACID: "AC-2", Kind: "change", Tests: []string{m2}},
			{ACID: "AC-3", Kind: "preservation", Tests: []string{m3}},
		},
		Stubs:               []ManifestStub{{Path: "pkg/impl.go", Symbol: "F", Marker: "SC-STUB(sc-9 AC-2)", Shape: "zero_value", Language: "go", ACIDs: []string{"AC-2"}}},
		DiscoveryProtection: DiscoveryProtection{WholeFiles: []WholeFileProtection{}, SemanticKeys: []SemanticKeyProtection{}},
		TestPaths:           ManifestTestPaths{Test: []string{"**/*_test.go"}, Fixture: []string{"**/testdata/**"}, TestInfra: []string{"internal/testutil/**"}},
	}
	d, err := manifestDigest(fx.Manifest)
	if err != nil {
		t.Fatalf("manifestDigest: %v", err)
	}
	fx.Manifest.Digest = d

	old1, old2, old3, skipped, mine := g5ID("TestOld1"), g5ID("TestOld2"), g5ID("TestOld3"), g5ID("TestSkippedAtBase"), g5ID("TestMine")
	fx.IDSets.BasePassing = g5Strs(old1, old2, old3)
	fx.IDSets.BaseSkipped = g5Strs(skipped)
	fx.IDSets.HeadPresent = g5Strs(old1, old2, old3, skipped, m1, m2, m3, mine)
	fx.IDSets.HeadSkipped = g5Strs(skipped)
	fx.IsolationFiles = g5Strs("pkg/mine_test.go")
	fx.ProtectedTestIDs = g5Strs(old1)
	fx.RequiredChecks = g5Strs("lint")
	fx.ApprovedRemovedTests = g5Strs()

	iso := g5IsoFacts(fx.IsolationFiles)
	pass := func(id string) TestObservation { return TestObservation{ID: id, Suite: "unit", StatusHead: "passed"} }
	fx.Observed = GreenObservation{
		TaskID: "sc-9", Repository: "o/demo", Phase: "green", Subject: "tests", IntentID: "vi-9b21",
		BaseSha: g5Base, HeadSha: g5Head,
		Verifier:       Verifier{Version: "0.1.0", BinarySha256: "sha256:" + strings.Repeat("5", 64)},
		ValidationBlob: fx.Identity.BaseValidationBlob,
		ToolchainImage: "git.example.org/strange-company/ci@sha256:" + strings.Repeat("6", 64),
		Runs: []RunSummary{{Tree: "head", Commit: g5Head, Suite: "unit", Adapter: "go-test-json", ExitCode: 0, DurationMs: 44000, ReportFound: true,
			Counts: Counts{Total: 8, Passed: 7, Skipped: 1}}},
		Checks: []CheckRun{{Name: "lint", ExitCode: 0, DurationMs: 20000, Required: true}},
		Tests: []TestObservation{
			pass(m1), pass(m2), pass(m3), pass(old1),
			{ID: skipped, Suite: "unit", StatusHead: "skipped"},
		},
		Isolation: &IsolationRun{RemovedFiles: iso.Count, RemovedFilesSha256: iso.Sha256,
			Tests: []ReplayTest{{ID: m1, Status: "passed"}, {ID: m2, Status: "passed"}, {ID: m3, Status: "passed"}}},
		RedReplay: &RedReplay{RedCommit: g5Red,
			Tests: []ReplayTest{{ID: m1, Status: "failed_assertion"}, {ID: m2, Status: "stub_unimplemented"}}},
		Repeats:    &GreenRepeats{Runs: 3, Inconsistent: []string{}},
		ObservedAt: "2026-10-13T12:59:30Z",
	}
	fx.Integrity = Integrity{
		Generation: 1, RedCommit: g5Red, HeadDescendsFromRed: true, ManifestDigest: fx.Manifest.Digest,
		ManifestFiles:         []IntegrityFile{{Path: "pkg/behavior_test.go", ExpectedBlob: g5Blob, ActualBlob: strp(g5Blob), Status: "intact"}},
		ProtectedTestsMissing: []string{}, DiscoveryChanges: []DiscoveryChange{}, StaticFindings: []IntegrityFinding{},
		StubsRemaining: []string{}, ForeignCommits: []string{},
	}
	return fx
}

func strp(s string) *string { return &s }

// g5InputOf builds the library input: id sets encoded, the isolation derivation and the RED baseline attached.
func g5InputOf(t *testing.T, fx greenFixture) GreenInput {
	t.Helper()
	plat, eff := s3Effective(t)
	obs := fx.Observed
	obs.TestIDSets = &GreenTestIDSets{HeadPresent: encodeIDSet(t, fx.IDSets.HeadPresent), HeadSkipped: encodeIDSet(t, fx.IDSets.HeadSkipped)}
	redHead := append(append([]string{}, fx.IDSets.BasePassing...), fx.IDSets.BaseSkipped...)
	for _, mt := range fx.Manifest.Tests {
		redHead = append(redHead, mt.ID)
	}
	baseline := Observation{
		TaskID: "sc-9", Repository: "o/demo", Phase: "red", Subject: "tests", IntentID: "vi-1", BaseSha: g5Base, HeadSha: g5Red,
		Verifier: obs.Verifier, ValidationBlob: obs.ValidationBlob, ToolchainImage: obs.ToolchainImage,
		Runs: []RunSummary{}, Tests: []TestObservation{}, ObservedAt: "2026-10-13T12:20:00Z",
		TestIDSets: &TestIDSets{
			BasePassing: encodeIDSet(t, fx.IDSets.BasePassing), BaseSkipped: encodeIDSet(t, fx.IDSets.BaseSkipped),
			HeadPresent: encodeIDSet(t, redHead),
		},
	}
	return GreenInput{
		Observed: obs, Integrity: fx.Integrity, Manifest: fx.Manifest, Baseline: baseline, Identity: fx.Identity,
		Isolation: g5IsoFacts(fx.IsolationFiles), ProtectedTestIDs: fx.ProtectedTestIDs, RequiredChecks: fx.RequiredChecks,
		ApprovedRemovedTests: fx.ApprovedRemovedTests, Platform: plat, Effective: eff,
		ComputedAt: "2026-10-13T13:00:00Z", PolicyRevision: "sha256:" + strings.Repeat("a", 64),
	}
}

// g5Merge overlays src over dst: objects merge recursively, any other value (including null) replaces.
func g5Merge(dst, src any) any {
	sm, sok := src.(map[string]any)
	dm, dok := dst.(map[string]any)
	if !sok || !dok {
		return src
	}
	for k, v := range sm {
		dm[k] = g5Merge(dm[k], v)
	}
	return dm
}

func g5Load(t *testing.T, rule, name string) (greenFixture, greenExpect) {
	t.Helper()
	dir := filepath.Join("testdata", "green", rule, name)
	b, err := json.Marshal(g5BaseFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	var merged any
	if err := json.Unmarshal(b, &merged); err != nil {
		t.Fatal(err)
	}
	var overlay any
	readJSONFile(t, &overlay, dir, "overlay.json")
	b, err = json.Marshal(g5Merge(merged, overlay))
	if err != nil {
		t.Fatal(err)
	}
	var fx greenFixture
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&fx); err != nil {
		t.Fatalf("%s: overlay does not fit the fixture: %v", dir, err)
	}
	var exp greenExpect
	readJSONFile(t, &exp, dir, "expect.json")
	return fx, exp
}

var (
	g5SchemaOnce sync.Once
	g5Schema     interface{ Validate(any) error }
	g5SchemaErr  error
)

// g5Validate validates a classified green-result document against contracts/green-result.schema.yaml.
func g5Validate(t testing.TB, obs GreenObservation, integ Integrity, cl GreenClassification) error {
	t.Helper()
	g5SchemaOnce.Do(func() {
		schemas, err := contracts.CompileSchemas(filepath.Join(repoRoot(t), "contracts"))
		if err != nil {
			g5SchemaErr = err
			return
		}
		if s := schemas["green-result"]; s != nil {
			g5Schema = s
		} else {
			g5SchemaErr = errors.New("no green-result schema")
		}
	})
	if g5SchemaErr != nil {
		t.Fatal(g5SchemaErr)
	}
	doc := map[string]any{
		"schema_version": "v1alpha1", "document": "green-result", "stage": "classified",
		"observed": obs, "integrity": integ, "classification": cl,
		"pipeline": map[string]any{"number": 1, "intent_id": obs.IntentID, "created_by": "sc-runner",
			"config_revision": obs.BaseSha, "config_sha256": strings.Repeat("7", 64), "evidence_sha256": strings.Repeat("8", 64)},
	}
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	return g5Schema.Validate(v)
}

// g5Check compares a classification with a golden expectation and, for a classification, validates the classified
// green-result document it belongs to against its schema. in must be a schema-valid input (see g5Valid).
func g5Check(t *testing.T, in GreenInput, got GreenClassification, err error, exp greenExpect) {
	t.Helper()
	if exp.Error != "" {
		var nc *NotClassifiedError
		if !errors.As(err, &nc) {
			t.Fatalf("err = %v (classification %s), want a *NotClassifiedError", err, jsonOf(got))
		}
		if nc.Code != exp.Error {
			t.Errorf("error code = %q, want %q", nc.Code, exp.Error)
		}
		return
	}
	if err != nil {
		t.Fatalf("ClassifyGreen: %v", err)
	}
	if got.Outcome != exp.Outcome {
		t.Errorf("outcome = %q, want %q (reasons %v)", got.Outcome, exp.Outcome, got.Reasons)
	}
	if exp.Reasons != nil && jsonOf(got.Reasons) != jsonOf(*exp.Reasons) {
		t.Errorf("reasons = %s, want %s", jsonOf(got.Reasons), jsonOf(*exp.Reasons))
	}
	for _, c := range exp.Contains {
		if !g5Has(got.Reasons, c) {
			t.Errorf("reasons %v lack %q", got.Reasons, c)
		}
	}
	if exp.OnlyFrom != nil {
		if len(got.Reasons) == 0 {
			t.Errorf("reasons are empty, want a non-empty subset of %v", exp.OnlyFrom)
		}
		for _, r := range got.Reasons {
			if !g5Has(exp.OnlyFrom, r) {
				t.Errorf("reason %q is not one of %v", r, exp.OnlyFrom)
			}
		}
	}
	if got.Reasons == nil {
		t.Errorf("reasons is nil, want a (possibly empty) list")
	}
	if got.ComputedBy != "runner" || got.ComputedAt != in.ComputedAt || got.PolicyRevision != in.PolicyRevision || got.VerifierLibVersion == "" {
		t.Errorf("provenance fields = %+v", got)
	}
	if verr := g5Validate(t, in.Observed, in.Integrity, got); verr != nil {
		t.Errorf("classified green-result does not validate against green-result.schema.yaml: %v", verr)
	}
}

func g5Has(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

// g5CaseDirs lists the case directories of one rule directory (named explicitly by the calling test).
func g5CaseDirs(t *testing.T, rule string) []string {
	t.Helper()
	ents, err := os.ReadDir(filepath.Join("testdata", "green", rule))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range ents {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	if len(out) == 0 {
		t.Fatalf("testdata/green/%s holds no case", rule)
	}
	return out
}

// g5RunRule classifies every case of the named rule directories and compares with the golden expectation.
func g5RunRule(t *testing.T, rules ...string) {
	t.Helper()
	for _, rule := range rules {
		for _, name := range g5CaseDirs(t, rule) {
			t.Run(rule+"/"+name, func(t *testing.T) {
				fx, exp := g5Load(t, rule, name)
				in := g5InputOf(t, fx)
				got, err := ClassifyGreen(in)
				g5Check(t, in, got, err, exp)
			})
		}
	}
}
