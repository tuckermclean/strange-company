package verify

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

var acTokenRE = regexp.MustCompile(`AC-[1-9][0-9]{0,2}`)

// BuildManifest builds the test manifest of a red_valid classification (plan 22 section 14.5): the protected files
// (cumulative over generations), the tests N and M plus the earlier generation's tests still present, the AC
// mapping, the stubs, the discovery-protection snapshot and test paths, `supersedes` and the digest (sha256 of the
// canonical JSON of the document without `digest`). Any other classification is an error.
func BuildManifest(in ManifestInput) (Manifest, error) {
	if in.Classification.Outcome != outcomeRedValid {
		return Manifest{}, fmt.Errorf("no manifest for classification outcome %q: only red_valid has one", in.Classification.Outcome)
	}
	fa := in.Facts
	m := Manifest{
		SchemaVersion: "v1alpha1", Kind: "test", TaskID: fa.TaskID, Generation: fa.Generation, ACVersion: fa.ACVersion,
		CreatedAt: fa.CreatedAt, Repository: fa.Repository, BaseCommit: fa.BaseCommit,
		GenerationStartCommit: fa.GenerationStartCommit, TestCommit: fa.TestCommit, RedEvidenceDigest: fa.RedEvidenceDigest,
		PolicyRevision: in.Red.PolicyRevision,
	}
	if m.PolicyRevision == "" {
		m.PolicyRevision = in.Classification.PolicyRevision
	}
	m.Files = manifestFiles(in.Red.Scope.ChangedFiles, fa.Previous)
	tests, err := manifestTests(in.Red, in.Classification.NewTests, fa.Previous)
	if err != nil {
		return Manifest{}, err
	}
	m.Tests = tests
	m.ACMapping = manifestACMapping(in.Red.Criteria, tests)
	m.Stubs = manifestStubs(in.Red.Scope.Stubs)
	for _, st := range m.Stubs {
		if len(st.ACIDs) == 0 {
			return Manifest{}, fmt.Errorf("stub marker %q of %s names no criterion", st.Marker, st.Path)
		}
	}
	m.DiscoveryProtection = fa.DiscoveryProtection
	if m.DiscoveryProtection.WholeFiles == nil {
		m.DiscoveryProtection.WholeFiles = []WholeFileProtection{}
	}
	if m.DiscoveryProtection.SemanticKeys == nil {
		m.DiscoveryProtection.SemanticKeys = []SemanticKeyProtection{}
	}
	paths, err := manifestTestPaths(in.Red.Effective.Values)
	if err != nil {
		return Manifest{}, err
	}
	m.TestPaths = paths
	if fa.Previous != nil {
		switch fa.SupersedesReason {
		case "test_change_approved", "strengthened", "respec":
		default:
			return Manifest{}, fmt.Errorf("supersedes reason %q is not valid", fa.SupersedesReason)
		}
		m.Supersedes = &ManifestSupersedes{Generation: fa.Previous.Generation, Digest: fa.Previous.Digest, Reason: fa.SupersedesReason}
	}
	if len(m.Files) == 0 || len(m.Tests) == 0 || len(m.ACMapping) == 0 {
		return Manifest{}, fmt.Errorf("a manifest needs files, tests and an ac_mapping")
	}
	d, err := manifestDigest(m)
	if err != nil {
		return Manifest{}, err
	}
	m.Digest = d
	if err := validateManifestSchema(m); err != nil {
		return Manifest{}, fmt.Errorf("manifest does not satisfy test-manifest.schema.yaml: %w", err)
	}
	return m, nil
}

// validateManifestSchema checks a manifest against contracts/test-manifest.schema.yaml natively (the library does no
// file access, and the schema is protected from change here): required fields, enums and bounds here, the string
// limits through the schema's own constraints (manifestLimits). The error lists every violation in document order,
// so the same input always gives the same error. The BuildManifest tests also validate the output against the
// schema file itself.
func validateManifestSchema(m Manifest) error {
	var bad []string
	req := func(ok bool, what string) {
		if !ok {
			bad = append(bad, what)
		}
	}
	req(m.SchemaVersion == "v1alpha1", "schema_version")
	req(m.Kind == "test" || m.Kind == "judge", "kind")
	req(withinLimit(limTaskID, m.TaskID), "task_id")
	req(m.Generation >= 1, "generation")
	req(m.ACVersion >= 1, "ac_version")
	req(withinLimit(limCreatedAt, m.CreatedAt), "created_at")
	req(withinLimit(limRepository, m.Repository), "repository")
	req(withinLimit(limGitSha, m.BaseCommit), "base_commit")
	req(withinLimit(limGitSha, m.GenerationStartCommit), "generation_start_commit")
	req(withinLimit(limGitSha, m.TestCommit), "test_commit")
	req(withinLimit(limSha256, m.RedEvidenceDigest), "red_evidence_digest")
	req(m.PolicyRevision == "" || withinLimit(limSha256, m.PolicyRevision), "policy_revision")
	req(len(m.Files) >= 1 && len(m.Files) <= maxManifestFiles, "files (size)")
	req(len(m.Tests) >= 1 && len(m.Tests) <= maxManifestTests, "tests (size)")
	req(len(m.ACMapping) >= 1 && len(m.ACMapping) <= maxManifestACMapping, "ac_mapping (size)")
	req(len(m.Stubs) <= maxManifestStubs, "stubs (size)")
	bad = append(bad, itemViolations(m, true)...)
	dp := m.DiscoveryProtection
	req(dp.WholeFiles != nil && len(dp.WholeFiles) <= maxDiscoveryItems, "discovery_protection.whole_files")
	for i, w := range dp.WholeFiles {
		req(withinLimit(limRepoPath, w.Path), fmt.Sprintf("discovery_protection.whole_files[%d].path", i))
		req(w.BlobSha == nil || withinLimit(limObjectID, *w.BlobSha), fmt.Sprintf("discovery_protection.whole_files[%d].blob_sha", i))
	}
	req(dp.SemanticKeys != nil && len(dp.SemanticKeys) <= maxDiscoveryItems, "discovery_protection.semantic_keys")
	for i, k := range dp.SemanticKeys {
		req(withinLimit(limRepoPath, k.Path), fmt.Sprintf("discovery_protection.semantic_keys[%d].path", i))
		req(withinLimit(limSemanticKey, k.Key), fmt.Sprintf("discovery_protection.semantic_keys[%d].key", i))
		req(k.ValueSha256 == nil || withinLimit(limSha256, *k.ValueSha256), fmt.Sprintf("discovery_protection.semantic_keys[%d].value_sha256", i))
	}
	if m.Supersedes != nil {
		req(m.Supersedes.Generation >= 1, "supersedes.generation")
		req(withinLimit(limSha256, m.Supersedes.Digest), "supersedes.digest")
		r := m.Supersedes.Reason
		req(r == "test_change_approved" || r == "strengthened" || r == "respec", "supersedes.reason")
	}
	req(withinLimit(limSha256, m.Digest), "digest")
	if len(bad) > 0 {
		return fmt.Errorf("invalid %s", strings.Join(bad, ", "))
	}
	return nil
}

// manifestACMapping maps every criterion, in criteria order, to the manifest tests that carry it; a criterion with
// no test has no entry.
func manifestACMapping(criteria []Criterion, tests []ManifestTest) []ManifestACMapping {
	out := []ManifestACMapping{}
	for _, c := range criteria {
		var ids []string
		for _, t := range tests {
			for _, ac := range t.ACIDs {
				if ac == c.ID {
					ids = append(ids, t.ID)
					break
				}
			}
		}
		if len(ids) > 0 {
			out = append(out, ManifestACMapping{ACID: c.ID, Kind: c.Kind, Tests: ids})
		}
	}
	return out
}

// manifestStubs is the stubs list: each declared stub with the criteria its marker names (none when the marker names
// none, which BuildManifest refuses and the schema rejects).
func manifestStubs(stubs []StubFact) []ManifestStub {
	out := []ManifestStub{}
	for _, s := range stubs {
		acs := acTokenRE.FindAllString(s.Marker, -1)
		if acs == nil {
			acs = []string{}
		}
		out = append(out, ManifestStub{Path: s.Path, Symbol: s.Symbol, Marker: s.Marker, Shape: s.Shape, Language: languageOfPath(s.Path), ACIDs: acs})
	}
	return out
}

// manifestTestPaths is test_paths: the effective testPaths, fixturePaths and testInfraPaths. An ill-typed list is an
// error.
func manifestTestPaths(values map[string]any) (ManifestTestPaths, error) {
	var tp ManifestTestPaths
	for _, p := range []struct {
		key string
		dst *[]string
	}{{"/spec/testPaths", &tp.Test}, {"/spec/fixturePaths", &tp.Fixture}, {"/spec/testInfraPaths", &tp.TestInfra}} {
		list, err := stringList(values[p.key])
		if err != nil {
			return ManifestTestPaths{}, fmt.Errorf("effective %s: %w", p.key, err)
		}
		if list == nil {
			list = []string{}
		}
		*p.dst = list
	}
	return tp, nil
}

// manifestTests lists N ∪ M in the order of the criteria that declare each test (criteria order, then declaration
// order), the undeclared new tests after them, then the previous manifest's tests that are still present at head.
func manifestTests(red RedInput, newTests []string, prev *Manifest) ([]ManifestTest, error) {
	reported := map[string]TestObservation{}
	for _, t := range red.Observed.Tests {
		reported[t.ID] = t
	}
	if red.Observed.TestIDSets == nil {
		return nil, fmt.Errorf("observed document has no test_id_sets")
	}
	headPresent, err := decodeIDSet("head_present", red.Observed.TestIDSets.HeadPresent)
	if err != nil {
		return nil, err
	}
	// N ∪ M is derived from the observation as well as taken from the classification: a classified red_valid
	// document holds every declared test in N ∪ M (R5), and a test reported absent at base and present at head
	// is in N. The caller's slice is not the only source of a protected test.
	inNew := map[string]bool{}
	for _, id := range newTests {
		inNew[id] = true
	}
	for _, t := range red.Observed.Tests {
		if t.StatusBase == statusAbsent && t.StatusHead != statusAbsent && headPresent[t.ID] {
			inNew[t.ID] = true
		}
	}
	for _, names := range red.Declared {
		for _, name := range names {
			for id := range headPresent {
				if id == name || strings.HasSuffix(id, "::"+name) {
					inNew[id] = true
				}
			}
		}
	}
	acs := map[string][]string{}
	var order []string
	place := func(id string) {
		for _, o := range order {
			if o == id {
				return
			}
		}
		order = append(order, id)
	}
	for _, c := range red.Criteria {
		for _, name := range red.Declared[c.ID] {
			for _, id := range sortedKeys(inNew) {
				if id == name || strings.HasSuffix(id, "::"+name) {
					place(id)
					acs[id] = appendUnique(acs[id], c.ID)
				}
			}
		}
	}
	for _, id := range sortedKeys(inNew) {
		place(id)
	}
	out := []ManifestTest{}
	for _, id := range order {
		t, ok := reported[id]
		if !ok {
			return nil, fmt.Errorf("test %q is not in the observed tests", id)
		}
		switch t.StatusHead {
		case statusFailedAssertion, statusStub, statusPassed:
		default:
			return nil, fmt.Errorf("test %q has status %q at head, which a manifest cannot record", id, t.StatusHead)
		}
		ids := acs[id]
		if ids == nil {
			ids = []string{}
		}
		suite := t.Suite
		if suite == "" {
			suite = suiteOf(id)
		}
		out = append(out, ManifestTest{ID: id, Suite: suite, Kind: protectedKind(reported, id), ACIDs: ids, RedStatus: t.StatusHead})
	}
	if prev != nil {
		have := map[string]bool{}
		for _, t := range out {
			have[t.ID] = true
		}
		for _, t := range prev.Tests {
			if !have[t.ID] && headPresent[t.ID] {
				out = append(out, t)
			}
		}
	}
	return out, nil
}

// manifestDigest is sha256 over the canonical JSON (sorted keys, compact) of the manifest without `digest`.
func manifestDigest(m Manifest) (string, error) {
	raw, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var generic map[string]any
	if err := dec.Decode(&generic); err != nil {
		return "", err
	}
	delete(generic, "digest")
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false) // map keys are sorted; no HTML escaping (as the F05 Fingerprint)
	if err := enc.Encode(generic); err != nil {
		return "", err
	}
	canon := bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
	sum := sha256.Sum256(canon)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
