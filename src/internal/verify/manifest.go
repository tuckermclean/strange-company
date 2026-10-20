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
	m.ACMapping = []ManifestACMapping{}
	for _, c := range in.Red.Criteria {
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
			m.ACMapping = append(m.ACMapping, ManifestACMapping{ACID: c.ID, Kind: c.Kind, Tests: ids})
		}
	}
	m.Stubs = []ManifestStub{}
	for _, s := range in.Red.Scope.Stubs {
		acs := acTokenRE.FindAllString(s.Marker, -1)
		if len(acs) == 0 {
			return Manifest{}, fmt.Errorf("stub marker %q of %s names no criterion", s.Marker, s.Path)
		}
		m.Stubs = append(m.Stubs, ManifestStub{Path: s.Path, Symbol: s.Symbol, Marker: s.Marker, Shape: s.Shape, Language: languageOfPath(s.Path), ACIDs: acs})
	}
	m.DiscoveryProtection = fa.DiscoveryProtection
	if m.DiscoveryProtection.WholeFiles == nil {
		m.DiscoveryProtection.WholeFiles = []WholeFileProtection{}
	}
	if m.DiscoveryProtection.SemanticKeys == nil {
		m.DiscoveryProtection.SemanticKeys = []SemanticKeyProtection{}
	}
	for _, p := range []struct {
		key string
		dst *[]string
	}{{"/spec/testPaths", &m.TestPaths.Test}, {"/spec/fixturePaths", &m.TestPaths.Fixture}, {"/spec/testInfraPaths", &m.TestPaths.TestInfra}} {
		list, err := stringList(in.Red.Effective.Values[p.key])
		if err != nil {
			return Manifest{}, fmt.Errorf("effective %s: %w", p.key, err)
		}
		if list == nil {
			list = []string{}
		}
		*p.dst = list
	}
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
	if len(m.Files) > maxManifestFiles || len(m.Tests) > maxManifestTests || len(m.Stubs) > maxManifestStubs || len(m.ACMapping) > maxManifestACMapping {
		return Manifest{}, fmt.Errorf("manifest exceeds the schema limits")
	}
	d, err := manifestDigest(m)
	if err != nil {
		return Manifest{}, err
	}
	m.Digest = d
	return m, nil
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
	canon, err := json.Marshal(generic) // map keys are sorted
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canon)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
