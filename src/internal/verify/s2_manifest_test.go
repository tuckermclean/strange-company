package verify

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"strange.company/sc/internal/contracts"
)

// BuildManifest (plan 22 section 14.5, 22-AC-9). The sc-184 fixture is testdata/manifest/sc184/input.json: the
// observed RED, scope, declared mapping, criteria and effective policy of the task. The Git facts of the manifest
// that no rule derives (identities, commits, the discovery-protection snapshot read at the red commit) are taken
// from examples[0] of contracts/test-manifest.schema.yaml, which is also the expected document.
//
// Interface choices (generations.go): ManifestInput{Red, Classification, Facts}; Classification must be red_valid
// (anything else is an error); test_paths come from the effective /spec/testPaths, /spec/fixturePaths and
// /spec/testInfraPaths values; tests are ordered by the criteria that declare them (criteria order, then declaration
// order), files and stubs keep the scope's order.

func manifestSchemaExample(t *testing.T) map[string]any {
	t.Helper()
	doc := readYAMLMap(t, filepath.Join(repoRoot(t), "contracts", "test-manifest.schema.yaml"))
	exs, ok := doc["examples"].([]any)
	if !ok || len(exs) == 0 {
		t.Fatal("test-manifest.schema.yaml has no examples")
	}
	m, ok := exs[0].(map[string]any)
	if !ok || m["task_id"] != "sc-184" {
		t.Fatalf("examples[0] is not the sc-184 manifest: %v", exs[0])
	}
	return m
}

// canonicalJSON is the canonical form of the manifest digest: object keys sorted, no insignificant whitespace.
// encoding/json of a decoded generic value sorts the keys.
func canonicalJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var generic any
	if err := json.Unmarshal(b, &generic); err != nil {
		t.Fatal(err)
	}
	out, err := json.Marshal(generic)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func asGeneric(t *testing.T, v any) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(canonicalJSON(t, v), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func sha256Of(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func wholeFilesOf(t *testing.T, ex map[string]any) DiscoveryProtection {
	t.Helper()
	var dp DiscoveryProtection
	b, err := json.Marshal(ex["discovery_protection"])
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &dp); err != nil {
		t.Fatal(err)
	}
	return dp
}

func sc184Input(t *testing.T, mut ...func(*redFixture)) ManifestInput {
	t.Helper()
	var fx redFixture
	readJSONFile(t, &fx, "testdata", "manifest", "sc184", "input.json")
	for _, m := range mut {
		m(&fx)
	}
	in := redInputOf(t, fx)
	in.PolicyRevision = "sha256:" + strings.Repeat("a", 64)
	ex := asGeneric(t, manifestSchemaExample(t)) // JSON numbers, whatever the YAML parser yields
	str := func(k string) string { return ex[k].(string) }
	gen := int(ex["generation"].(float64))
	acv := int(ex["ac_version"].(float64))
	var ids []string
	for _, tt := range in.Observed.Tests {
		ids = append(ids, tt.ID)
	}
	return ManifestInput{
		Red: in,
		Classification: RedClassification{
			Outcome: "red_valid", Reasons: []string{}, NewTests: ids,
			ACResults: []ACResult{
				{ACID: "AC-1", Kind: "change", Tests: ids[0:1], Outcome: "failing_assertion"},
				{ACID: "AC-2", Kind: "change", Tests: ids[1:2], Outcome: "failing_assertion"},
				{ACID: "AC-3", Kind: "preservation", Tests: ids[2:3], Outcome: "passing"},
			},
			ComputedBy: "runner", ComputedAt: in.ComputedAt, PolicyRevision: in.PolicyRevision, VerifierLibVersion: "0.1.0",
		},
		Facts: ManifestFacts{
			TaskID: str("task_id"), Repository: str("repository"), Generation: gen, ACVersion: acv,
			BaseCommit: str("base_commit"), GenerationStartCommit: str("generation_start_commit"), TestCommit: str("test_commit"),
			RedEvidenceDigest: str("red_evidence_digest"), CreatedAt: str("created_at"),
			DiscoveryProtection: wholeFilesOf(t, ex),
		},
	}
}

func validateManifest(t *testing.T, doc any) error {
	t.Helper()
	schemas, err := contracts.CompileSchemas(filepath.Join(repoRoot(t), "contracts"))
	if err != nil {
		t.Fatal(err)
	}
	s := schemas["test-manifest"]
	if s == nil {
		t.Fatal("no test-manifest schema")
	}
	return s.Validate(asGeneric(t, doc))
}

// 22-AC-9: BuildManifest on the sc-184 fixture produces the document of test-manifest.schema.yaml examples[0], and
// the manifest validates. The digest is the sha256 of the canonical JSON of the document without `digest`
// (schema description of `digest`, plan 22 section 14.5).
//
// AUDIT: examples[0].digest is the placeholder "sha256:efef...ef", which is not the digest of examples[0], so
// "digest equal" cannot hold literally. Every other field is compared with the example, and the digest is
// compared with the digest computed independently here from the example without its digest.
func TestBuildManifest_Sc184MatchesSchemaExample_22AC9(t *testing.T) {
	ex := manifestSchemaExample(t)
	got, err := BuildManifest(sc184Input(t))
	if err != nil {
		t.Fatalf("BuildManifest: %v", err)
	}
	gotDoc := asGeneric(t, got)
	wantDoc := asGeneric(t, ex)
	wantDigest := sha256Of(canonicalJSON(t, withoutDigest(wantDoc)))
	if gotDoc["digest"] != wantDigest {
		t.Errorf("digest = %v, want %s (sha256 of the canonical JSON without digest)", gotDoc["digest"], wantDigest)
	}
	delete(gotDoc, "digest")
	delete(wantDoc, "digest")
	if g, w := string(canonicalJSON(t, gotDoc)), string(canonicalJSON(t, wantDoc)); g != w {
		t.Errorf("manifest differs from test-manifest.schema.yaml examples[0]\n got: %s\nwant: %s", g, w)
	}
	if verr := validateManifest(t, got); verr != nil {
		t.Errorf("the manifest does not validate against test-manifest.schema.yaml: %v", verr)
	}
}

func withoutDigest(m map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range m {
		if k != "digest" {
			out[k] = v
		}
	}
	return out
}

// 22-AC-9: the digest follows the document. A manifest built from a different observation (the base RED of
// another commit) has a different digest, and every digest equals the sha256 of its own canonical JSON.
func TestBuildManifest_DigestCoversTheDocument_22AC9(t *testing.T) {
	in := sc184Input(t)
	a, err := BuildManifest(in)
	if err != nil {
		t.Fatalf("BuildManifest: %v", err)
	}
	in.Facts.TestCommit = strings.Repeat("7", 40)
	b, err := BuildManifest(in)
	if err != nil {
		t.Fatalf("BuildManifest: %v", err)
	}
	for _, m := range []Manifest{a, b} {
		if want := sha256Of(canonicalJSON(t, withoutDigest(asGeneric(t, m)))); m.Digest != want {
			t.Errorf("digest = %q, want %q", m.Digest, want)
		}
	}
	if a.Digest == b.Digest {
		t.Errorf("manifests of different test commits share the digest %q", a.Digest)
	}
	if b.TestCommit != strings.Repeat("7", 40) {
		t.Errorf("test_commit = %q, want the input's", b.TestCommit)
	}
}

// 22-R-3 / plan 22 section 14.5: a manifest exists only on red_valid; any other classification is an error.
func TestBuildManifest_OnlyForRedValid_22AC9(t *testing.T) {
	for _, outcome := range []string{"red_invalid", "already_satisfied", "partially_satisfied", "inconclusive", "baseline_failing", ""} {
		t.Run("outcome_"+outcome, func(t *testing.T) {
			in := sc184Input(t)
			in.Classification.Outcome = outcome
			if m, err := BuildManifest(in); err == nil {
				t.Errorf("BuildManifest built a manifest (digest %q) from outcome %q", m.Digest, outcome)
			}
		})
	}
}

// 22-AC-9 (section 14.5, cumulative generations): generation n's manifest includes generation n-1's files (blobs
// updated) and tests that are still present, and `supersedes` points at the previous manifest.
func TestBuildManifest_CumulativeGeneration_22AC9(t *testing.T) {
	older := gid("TestOlder")
	in := sc184Input(t, func(fx *redFixture) {
		fx.IDSets.HeadPresent = append(fx.IDSets.HeadPresent, older)
		fx.Observed.Tests = append(fx.Observed.Tests, TestObservation{ID: older, Suite: "unit", File: "internal/httpx/older_test.go", StatusBase: "absent", StatusHead: "failed_assertion"})
		fx.Declared["AC-1"] = append(fx.Declared["AC-1"], older)
		fx.Scope.ChangedFiles = append(fx.Scope.ChangedFiles, goChange("internal/httpx/older_test.go", "modified", "test"))
	})
	prev := prevManifest(
		[]ManifestFile{
			{Path: "internal/httpx/redirect_test.go", BlobSha: strings.Repeat("1", 40), Class: "test", Change: "added"},
			{Path: "internal/httpx/older_test.go", BlobSha: strings.Repeat("2", 40), Class: "test", Change: "added"},
		},
		[]ManifestTest{
			{ID: gid("TestRedirect307PreservesMethod"), Suite: "unit", Kind: "new", ACIDs: []string{"AC-1"}, RedStatus: "failed_assertion"},
			{ID: gid("TestOlder"), Suite: "unit", Kind: "new", ACIDs: []string{"AC-1"}, RedStatus: "failed_assertion"},
		})
	prev.Digest = "sha256:" + strings.Repeat("d", 64)
	in.Facts.Generation, in.Facts.Previous, in.Facts.SupersedesReason = 2, prev, "strengthened"
	in.Classification.ACResults[0].Tests = append(in.Classification.ACResults[0].Tests, older)
	got, err := BuildManifest(in)
	if err != nil {
		t.Fatalf("BuildManifest: %v", err)
	}
	if got.Supersedes == nil || got.Supersedes.Generation != 1 || got.Supersedes.Digest != prev.Digest || got.Supersedes.Reason != "strengthened" {
		t.Errorf("supersedes = %+v, want generation 1, digest %s, reason strengthened", got.Supersedes, prev.Digest)
	}
	var paths []string
	blobs := map[string]string{}
	for _, f := range got.Files {
		paths = append(paths, f.Path)
		blobs[f.Path] = f.BlobSha
	}
	sort.Strings(paths)
	want := []string{"internal/httpx/older_test.go", "internal/httpx/redirect_test.go", "internal/httpx/testdata/body.txt"}
	if strings.Join(paths, ",") != strings.Join(want, ",") {
		t.Errorf("files = %v, want %v (generation 1's files plus this generation's)", paths, want)
	}
	if blobs["internal/httpx/redirect_test.go"] != "3b18e512dba79e4c8300dd08aeb37f8e728b8dad" {
		t.Errorf("redirect_test.go blob = %q, want the updated blob of this generation", blobs["internal/httpx/redirect_test.go"])
	}
	var ids []string
	for _, tt := range got.Tests {
		ids = append(ids, tt.ID)
	}
	if !contains(ids, gid("TestOlder")) || !contains(ids, gid("TestRedirect307PreservesMethod")) || len(ids) != 4 {
		t.Errorf("tests = %v, want the three of sc-184 plus generation 1's TestOlder", ids)
	}
	if got.Generation != 2 {
		t.Errorf("generation = %d, want 2", got.Generation)
	}
	if verr := validateManifest(t, got); verr != nil {
		t.Errorf("the manifest does not validate: %v", verr)
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
