package verify

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"strange.company/sc/internal/contracts"
)

// Slice S6 (judges) helpers.
//
// Interface choices of S6 (plan 22 section 14.8 and the contracts are silent on the Go signatures and on a few
// facts; each choice is stated here, in the stricter reading, and reported to the orchestrator):
//   - judge.go: ClassifyJudge(JudgeInput) (RedClassification, error) is the judge validation (SC_PHASE=red,
//     SC_SUBJECT=judge) and ClassifyJudging(JudgingInput) (GreenClassification, error) the judging (SC_PHASE=green,
//     SC_SUBJECT=judge). Both are pure over their inputs; identity failure and unusable or inconsistent facts are
//     *NotClassifiedError{verification_infra_failed}, never a classification (22-R-3).
//   - Observation.JudgeChecks / GreenObservation.JudgeChecks mirror the judge_checks array of the contracts. A check
//     is named by its artifact id (`J-1`); the judge manifest names the same check `judge::J-1` (a manifest test id
//     needs a `<suite>::` prefix, suite `judge`).
//   - The judge artifact is passed as YAML text (JudgeFile); it is validated against JudgeArtifact natively.
//     Validation outcomes: judge_valid, judge_invalid, already_satisfied, partially_satisfied (all in
//     red-result.schema.yaml). An invalid artifact (schema, class, task, kind, coverage, scope) is judge_invalid.
//   - A frozen criterion is satisfied when it has checks and all of them pass; judge_valid needs every criterion
//     unsatisfied (the answer does not exist yet), partially_satisfied some, already_satisfied all. Their reasons
//     are [judge_passes_on_base]. A characterization judge has no such rule: every check must pass at the judge
//     commit (Observed) and at base (BaseObserved), and then the validation is judge_valid with no reason.
//   - Judging outcomes: judging_pass, judging_fail, tamper_detected (judge_artifact_changed), all in
//     green-result.schema.yaml. The producer diff is judged by CheckScope (role producer, the task's exemption
//     decision), so the refactor class cannot touch test-classified paths (TDD-B9, D21).
//   - A class whose judge source is acceptance-criteria (research) has no artifact and no manifest; JUDGING checks
//     the research report (non-empty, every AC id by markdown heading).

const (
	s6Task   = "sc-9"
	s6Repo   = "o/demo"
	s6Base   = "0123456789abcdef0123456789abcdef01234567"
	s6Judge  = "89abcdef0123456789abcdef0123456789abcdef"
	s6Head   = "fedcba9876543210fedcba9876543210fedcba98"
	s6Blob   = "3b18e512dba79e4c8300dd08aeb37f8e728b8dad"
	s6Path   = "verification/sc-9/judge.yaml"
	s6Blob2  = "9f1c2b3a4d5e60718293a4b5c6d7e8f901234567"
	s6Policy = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	s6At     = "2026-10-13T12:30:00Z"
)

const s6DocJudgeYAML = `apiVersion: strange.company/v1alpha1
kind: Judge
metadata:
  task: sc-9
  exemptionClass: documentation-only
spec:
  kind: required-facts
  checks:
    - id: J-1
      acIds: [AC-1]
      requiredFacts:
        paths: ["docs/guide.md"]
        mustMatch: ["^## Retention"]
    - id: J-2
      acIds: [AC-2]
      requiredFacts:
        paths: ["docs/guide.md"]
        mustMatch: ["30 days"]
`

// s6Fixture is the overlay target of every testdata/judge case: the hand-written classification inputs (observed
// documents and Runner facts). Facts the library can derive from Git (the producer diff) are derived in the test from
// a real scratch repository out of ProducerPaths.
type s6Fixture struct {
	Identity      Identity         `json:"identity"`
	TaskClass     string           `json:"task_class"`
	Approval      string           `json:"approval"`
	Criteria      []Criterion      `json:"criteria"`
	JudgeYAML     string           `json:"judge_yaml"`
	ChangedFiles  []ChangedFile    `json:"changed_files"`
	Observed      Observation      `json:"observed"`
	BaseChecks    []JudgeCheck     `json:"base_checks"`
	BaseCommit    string           `json:"base_commit"`
	Green         GreenObservation `json:"green"`
	Manifest      *Manifest        `json:"manifest"`
	Redigest      bool             `json:"redigest"`
	Integrity     Integrity        `json:"integrity"`
	ProducerPaths []string         `json:"producer_paths"`
	Report        *string          `json:"report"`
	Required      []string         `json:"required_checks"`
}

// s6Expect is the golden expectation of a case. Function is validate (ClassifyJudge) or judging (ClassifyJudging).
type s6Expect struct {
	Function string    `json:"function"`
	Error    string    `json:"error"`
	Outcome  string    `json:"outcome"`
	Reasons  *[]string `json:"reasons"`
	Contains []string  `json:"contains"`
	// NonEmpty: the reasons are not empty (and, by the schema check, each is a reason code of the contract).
	NonEmpty  bool        `json:"non_empty"`
	ACResults *[]ACResult `json:"ac_results"`
}

func s6Checks(pairs ...string) []JudgeCheck {
	var out []JudgeCheck
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, JudgeCheck{ID: pairs[i], Status: pairs[i+1]})
	}
	return out
}

func s6Manifest(t testing.TB) Manifest {
	t.Helper()
	m := Manifest{
		SchemaVersion: "v1alpha1", Kind: "judge", TaskID: s6Task, Generation: 1, ACVersion: 1, CreatedAt: "2026-10-13T12:20:01Z",
		Repository: s6Repo, BaseCommit: s6Base, GenerationStartCommit: s6Base, TestCommit: s6Judge,
		RedEvidenceDigest: "sha256:" + strings.Repeat("ab", 32),
		Files:             []ManifestFile{{Path: s6Path, BlobSha: s6Blob, Class: "judge", Change: "added"}},
		Tests: []ManifestTest{
			{ID: "judge::J-1", Suite: "judge", Kind: "new", ACIDs: []string{"AC-1"}, RedStatus: "failed_assertion"},
			{ID: "judge::J-2", Suite: "judge", Kind: "new", ACIDs: []string{"AC-2"}, RedStatus: "failed_assertion"},
		},
		ACMapping: []ManifestACMapping{
			{ACID: "AC-1", Kind: "change", Tests: []string{"judge::J-1"}},
			{ACID: "AC-2", Kind: "change", Tests: []string{"judge::J-2"}},
		},
		Stubs:               []ManifestStub{},
		DiscoveryProtection: DiscoveryProtection{WholeFiles: []WholeFileProtection{}, SemanticKeys: []SemanticKeyProtection{}},
		TestPaths:           ManifestTestPaths{Test: []string{"**/*_test.go"}, Fixture: []string{"**/testdata/**"}, TestInfra: []string{"internal/testutil/**"}},
	}
	d, err := manifestDigest(m)
	if err != nil {
		t.Fatalf("manifestDigest: %v", err)
	}
	m.Digest = d
	return m
}

// s6BaseFixture is a valid judge validation AND a valid judging: every rule passes. A case overlays one fact.
func s6BaseFixture(t testing.TB) s6Fixture {
	t.Helper()
	var fx s6Fixture
	fx.Identity = Identity{Passed: true, BaseValidationBlob: "7d1a0c3e9b2f4a6c8e0d1f3a5b7c9e1d3f5a7b9c"}
	fx.TaskClass, fx.Approval = "documentation-only", "auto"
	fx.Criteria = []Criterion{{ID: "AC-1", Kind: "change"}, {ID: "AC-2", Kind: "change"}}
	fx.JudgeYAML = s6DocJudgeYAML
	fx.ChangedFiles = []ChangedFile{{Path: s6Path, Change: "added", Class: "judge", BlobSha: s6Blob}}
	ver := Verifier{Version: "0.1.0", BinarySha256: "sha256:" + strings.Repeat("5", 64)}
	image := "git.example.org/strange-company/ci@sha256:" + strings.Repeat("6", 64)
	fx.Observed = Observation{
		TaskID: s6Task, Repository: s6Repo, Phase: "red", Subject: "judge", IntentID: "vi-9b21", BaseSha: s6Base, HeadSha: s6Judge,
		Verifier: ver, ValidationBlob: fx.Identity.BaseValidationBlob, ToolchainImage: image,
		Runs: []RunSummary{{Tree: "head", Commit: s6Judge, Suite: "judge", Adapter: "judge", ExitCode: 1, DurationMs: 900, ReportFound: true,
			Counts: Counts{Total: 2, FailedAssertion: 2}}},
		Tests: []TestObservation{}, ObservedAt: "2026-10-13T12:20:00Z",
		JudgeChecks: s6Checks("J-1", "fail", "J-2", "fail"),
	}
	fx.Green = GreenObservation{
		TaskID: s6Task, Repository: s6Repo, Phase: "green", Subject: "judge", IntentID: "vi-9c33", BaseSha: s6Base, HeadSha: s6Head,
		Verifier: ver, ValidationBlob: fx.Identity.BaseValidationBlob, ToolchainImage: image,
		Runs: []RunSummary{{Tree: "head", Commit: s6Head, Suite: "judge", Adapter: "judge", ExitCode: 0, DurationMs: 900, ReportFound: true,
			Counts: Counts{Total: 2, Passed: 2}}},
		Checks:      []CheckRun{{Name: "lint", ExitCode: 0, DurationMs: 20000, Required: true}},
		Tests:       []TestObservation{},
		ObservedAt:  "2026-10-13T12:59:30Z",
		JudgeChecks: s6Checks("J-1", "pass", "J-2", "pass"),
	}
	m := s6Manifest(t)
	fx.Manifest = &m
	fx.Integrity = Integrity{
		Generation: 1, RedCommit: s6Judge, HeadDescendsFromRed: true, ManifestDigest: m.Digest,
		ManifestFiles:         []IntegrityFile{{Path: s6Path, ExpectedBlob: s6Blob, ActualBlob: strp(s6Blob), Status: "intact"}},
		ProtectedTestsMissing: []string{}, DiscoveryChanges: []DiscoveryChange{}, StaticFindings: []IntegrityFinding{},
		StubsRemaining: []string{}, ForeignCommits: []string{},
	}
	fx.ProducerPaths = []string{"docs/guide.md"}
	fx.Required = []string{"lint"}
	return fx
}

// s6Load merges testdata/judge/<rule>/<case>/overlay.json over the base fixture. A file judge.yaml.fx in the case
// directory replaces judge_yaml (the `.fx` suffix keeps the document inert, as in testdata/ingest).
func s6Load(t testing.TB, rule, name string) (s6Fixture, s6Expect) {
	t.Helper()
	dir := filepath.Join("testdata", "judge", rule, name)
	b, err := json.Marshal(s6BaseFixture(t))
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
	var fx s6Fixture
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&fx); err != nil {
		t.Fatalf("%s: overlay does not fit the fixture: %v", dir, err)
	}
	if y, err := os.ReadFile(filepath.Join(dir, "judge.yaml.fx")); err == nil {
		fx.JudgeYAML = string(y)
	}
	if fx.Redigest && fx.Manifest != nil {
		d, err := manifestDigest(*fx.Manifest)
		if err != nil {
			t.Fatalf("%s: manifestDigest: %v", dir, err)
		}
		fx.Manifest.Digest = d
		fx.Integrity.ManifestDigest = d
	}
	var exp s6Expect
	readJSONFile(t, &exp, dir, "expect.json")
	if exp.Function != "validate" && exp.Function != "judging" {
		t.Fatalf("%s: expect.json function = %q, want validate or judging", dir, exp.Function)
	}
	return fx, exp
}

// s6ValidateInput builds the ClassifyJudge input of a fixture.
func s6ValidateInput(t *testing.T, fx s6Fixture) (JudgeInput, Scope) {
	t.Helper()
	plat, eff := s4Effective(t, s4PolicySpec{Name: "demo", Allowed: s4AllClasses})
	sc := Scope{Generation: 1, GenerationStartCommit: s6Base, ChangedFiles: fx.ChangedFiles, Stubs: []StubFact{}, StaticFindings: []StaticFinding{}}
	if sc.ChangedFiles == nil {
		sc.ChangedFiles = []ChangedFile{}
	}
	in := JudgeInput{
		Observed: fx.Observed, Identity: fx.Identity, TaskClass: fx.TaskClass,
		Judge:    JudgeFile{Path: s6Path, Content: []byte(fx.JudgeYAML)},
		Criteria: fx.Criteria, Scope: sc, Platform: plat, Effective: eff, ComputedAt: s6At, PolicyRevision: s6Policy,
	}
	if fx.BaseChecks != nil {
		base := fx.Observed
		base.HeadSha = s6Base
		if fx.BaseCommit != "" {
			base.HeadSha = fx.BaseCommit
		}
		base.Runs = []RunSummary{{Tree: "head", Commit: base.HeadSha, Suite: "judge", Adapter: "judge", ExitCode: 0, DurationMs: 900, ReportFound: true,
			Counts: Counts{Total: len(fx.BaseChecks), Passed: len(fx.BaseChecks)}}}
		base.JudgeChecks = fx.BaseChecks
		in.BaseObserved = &base
	}
	return in, sc
}

// s6JudgingInput builds the ClassifyJudging input of a fixture; the producer diff comes from real Git.
func s6JudgingInput(t *testing.T, fx s6Fixture) JudgingInput {
	t.Helper()
	plat, eff := s4Effective(t, s4PolicySpec{Name: "demo", Allowed: s4AllClasses})
	in := JudgingInput{
		Observed: fx.Green, Identity: fx.Identity, Manifest: fx.Manifest, Integrity: fx.Integrity,
		Exemption: s4Producer(fx.TaskClass, fx.Approval), Criteria: fx.Criteria, RequiredChecks: fx.Required,
		Platform: plat, Effective: eff, ComputedAt: s6At, PolicyRevision: s6Policy,
	}
	in.Diff = []DiffEntry{}
	if len(fx.ProducerPaths) > 0 {
		in.Diff = s4Diff(t, nil, s4Add(fx.ProducerPaths...))
	}
	if fx.Report != nil {
		in.Report = &ResearchReport{Path: "reports/sc-9.md", Content: []byte(*fx.Report)}
	}
	return in
}

var (
	s6SchemaOnce sync.Once
	s6Schemas    map[string]interface{ Validate(any) error }
	s6SchemaErr  error
)

func s6Schema(t testing.TB, name string) interface{ Validate(any) error } {
	t.Helper()
	s6SchemaOnce.Do(func() {
		all, err := contracts.CompileSchemas(filepath.Join(repoRoot(t), "contracts"))
		if err != nil {
			s6SchemaErr = err
			return
		}
		s6Schemas = map[string]interface{ Validate(any) error }{}
		for _, n := range []string{"red-result", "green-result"} {
			if s := all[n]; s != nil {
				s6Schemas[n] = s
			}
		}
	})
	if s6SchemaErr != nil {
		t.Fatal(s6SchemaErr)
	}
	if s6Schemas[name] == nil {
		t.Fatalf("no %s schema", name)
	}
	return s6Schemas[name]
}

func s6Roundtrip(t testing.TB, doc any) any {
	t.Helper()
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

var s6Pipeline = func(intent, base string) map[string]any {
	return map[string]any{"number": 1, "intent_id": intent, "created_by": "sc-runner",
		"config_revision": base, "config_sha256": strings.Repeat("7", 64), "evidence_sha256": strings.Repeat("8", 64)}
}

func s6Has(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

func s6WantErr(t *testing.T, err error, got any, exp s6Expect) bool {
	t.Helper()
	if exp.Error == "" {
		return false
	}
	var nc *NotClassifiedError
	if !errors.As(err, &nc) {
		t.Fatalf("err = %v (classification %s), want a *NotClassifiedError", err, jsonOf(got))
	}
	if nc.Code != exp.Error {
		t.Errorf("error code = %q, want %q", nc.Code, exp.Error)
	}
	return true
}

func s6CheckReasons(t *testing.T, reasons []string, outcome string, exp s6Expect) {
	t.Helper()
	if outcome != exp.Outcome {
		t.Errorf("outcome = %q, want %q (reasons %v)", outcome, exp.Outcome, reasons)
	}
	if exp.Reasons != nil && jsonOf(reasons) != jsonOf(*exp.Reasons) {
		t.Errorf("reasons = %s, want %s", jsonOf(reasons), jsonOf(*exp.Reasons))
	}
	for _, c := range exp.Contains {
		if !s6Has(reasons, c) {
			t.Errorf("reasons %v lack %q", reasons, c)
		}
	}
	if exp.NonEmpty && len(reasons) == 0 {
		t.Errorf("reasons are empty, want at least one")
	}
	if reasons == nil {
		t.Errorf("reasons is nil, want a (possibly empty) list")
	}
}

// s6CheckValidate compares a judge validation with the golden expectation and validates the classified red-result
// document against its schema.
func s6CheckValidate(t *testing.T, in JudgeInput, sc Scope, got RedClassification, err error, exp s6Expect) {
	t.Helper()
	if s6WantErr(t, err, got, exp) {
		return
	}
	if err != nil {
		t.Fatalf("ClassifyJudge: %v", err)
	}
	s6CheckReasons(t, got.Reasons, got.Outcome, exp)
	if exp.ACResults != nil && jsonOf(got.ACResults) != jsonOf(*exp.ACResults) {
		t.Errorf("ac_results = %s, want %s", jsonOf(got.ACResults), jsonOf(*exp.ACResults))
	}
	if got.NewTests == nil || got.ACResults == nil {
		t.Errorf("new_tests/ac_results are nil: %+v", got)
	}
	if got.ComputedBy != "runner" || got.ComputedAt != in.ComputedAt || got.PolicyRevision != in.PolicyRevision || got.VerifierLibVersion == "" {
		t.Errorf("provenance fields = %+v", got)
	}
	doc := map[string]any{
		"schema_version": "v1alpha1", "document": "red-result", "stage": "classified",
		"observed": in.Observed, "scope": sc, "classification": got, "pipeline": s6Pipeline(in.Observed.IntentID, in.Observed.BaseSha),
	}
	if verr := s6Schema(t, "red-result").Validate(s6Roundtrip(t, doc)); verr != nil {
		t.Errorf("classified red-result does not validate against red-result.schema.yaml: %v", verr)
	}
}

// s6CheckJudging compares a judging with the golden expectation and validates the classified green-result document.
func s6CheckJudging(t *testing.T, in JudgingInput, got GreenClassification, err error, exp s6Expect) {
	t.Helper()
	if s6WantErr(t, err, got, exp) {
		return
	}
	if err != nil {
		t.Fatalf("ClassifyJudging: %v", err)
	}
	s6CheckReasons(t, got.Reasons, got.Outcome, exp)
	if got.ComputedBy != "runner" || got.ComputedAt != in.ComputedAt || got.PolicyRevision != in.PolicyRevision || got.VerifierLibVersion == "" {
		t.Errorf("provenance fields = %+v", got)
	}
	integ := in.Integrity
	if integ.ManifestDigest == "" {
		// A judge whose source is the acceptance criteria has no manifest and no integrity facts; the document is
		// checked with the integrity block of the valid baseline, so that the classification itself is validated.
		integ = s6BaseFixture(t).Integrity
	}
	doc := map[string]any{
		"schema_version": "v1alpha1", "document": "green-result", "stage": "classified",
		"observed": in.Observed, "integrity": integ, "classification": got, "pipeline": s6Pipeline(in.Observed.IntentID, in.Observed.BaseSha),
	}
	if verr := s6Schema(t, "green-result").Validate(s6Roundtrip(t, doc)); verr != nil {
		t.Errorf("classified green-result does not validate against green-result.schema.yaml: %v", verr)
	}
}

// s6CaseDirs lists the case directories of one judge rule directory (named explicitly by the calling test).
func s6CaseDirs(t testing.TB, rule string) []string {
	t.Helper()
	ents, err := os.ReadDir(filepath.Join("testdata", "judge", rule))
	if err != nil {
		t.Fatalf("judge fixture directory %s: %v", rule, err)
	}
	var out []string
	for _, e := range ents {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	if len(out) == 0 {
		t.Fatalf("testdata/judge/%s holds no case", rule)
	}
	return out
}

// s6Run classifies one case with the function its expect.json names and checks the result.
func s6Run(t *testing.T, rule, name string) {
	t.Helper()
	fx, exp := s6Load(t, rule, name)
	switch exp.Function {
	case "validate":
		in, sc := s6ValidateInput(t, fx)
		got, err := ClassifyJudge(in)
		s6CheckValidate(t, in, sc, got, err, exp)
	case "judging":
		in := s6JudgingInput(t, fx)
		got, err := ClassifyJudging(in)
		s6CheckJudging(t, in, got, err, exp)
	}
}

// s6RunRules runs every case of the named rule directories.
func s6RunRules(t *testing.T, rules ...string) {
	t.Helper()
	for _, rule := range rules {
		for _, name := range s6CaseDirs(t, rule) {
			t.Run(rule+"/"+name, func(t *testing.T) { s6Run(t, rule, name) })
		}
	}
}
