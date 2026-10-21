package verify

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

// maxDerivedPaths bounds the red..head diff the Git derivations read; the isolation set is not capped by a role limit.
const maxDerivedPaths = 1 << 30

// checkRecordedShas rejects a recorded commit list that holds anything but full lowercase object ids: a malformed
// record never vouches for a commit (TDD-9, 22-R-3).
func checkRecordedShas(what string, l []string) (map[string]bool, error) {
	out := make(map[string]bool, len(l))
	for _, s := range l {
		if !objectIDRE.MatchString(s) {
			return nil, unverifiable("recorded %s %q is not a full lowercase 40 hex commit id", what, s)
		}
		out[s] = true
	}
	return out, nil
}

// DeriveIntegrity derives the Integrity facts of red_commit..head from Git (section 14.7 G1, G2, G7; TDD-9): head
// descent from the red commit, the blob of every manifest file at the head, the foreign commits (every commit of
// red..head that is neither an accepted result commit nor a human commit, by full SHA), the stubs whose marker remains
// and the ingestion violations mapped to static findings and discovery changes. protected_tests_missing is a fact of
// the head run, not of Git: it is returned empty and ClassifyGreen recomputes it from the observed id sets.
func DeriveIntegrity(in IntegrityInput) (Integrity, error) {
	red := in.Manifest.TestCommit
	if err := checkRevision(red); err != nil {
		return Integrity{}, fmt.Errorf("manifest test_commit: %w", err)
	}
	if err := checkRevision(in.Head); err != nil {
		return Integrity{}, err
	}
	accepted, err := checkRecordedShas("accepted result commit", in.AcceptedCommits)
	if err != nil {
		return Integrity{}, err
	}
	human, err := checkRecordedShas("human commit", in.HumanCommits)
	if err != nil {
		return Integrity{}, err
	}
	integ := Integrity{
		Generation: in.Manifest.Generation, RedCommit: red, ManifestDigest: in.Manifest.Digest,
		ManifestFiles: []IntegrityFile{}, ProtectedTestsMissing: []string{}, DiscoveryChanges: []DiscoveryChange{},
		StaticFindings: []IntegrityFinding{}, StubsRemaining: []string{}, ForeignCommits: []string{},
	}
	if integ.HeadDescendsFromRed, err = IsAncestor(in.Git, red, in.Head); err != nil {
		return Integrity{}, err
	}
	commits, err := RevList(in.Git, red, in.Head)
	if err != nil {
		return Integrity{}, err
	}
	for _, c := range commits {
		if !accepted[c] && !human[c] {
			integ.ForeignCommits = append(integ.ForeignCommits, c)
		}
	}
	tree, err := LsTree(in.Git, in.Head)
	if err != nil {
		return Integrity{}, err
	}
	at := make(map[string]TreeEntry, len(tree))
	for _, e := range tree {
		at[e.Path] = e
	}
	for _, f := range in.Manifest.Files {
		file := IntegrityFile{Path: f.Path, ExpectedBlob: f.BlobSha, Status: "deleted"}
		if e, ok := at[f.Path]; ok {
			b := e.Blob
			file.ActualBlob = &b
			file.Status = "modified"
			if e.Blob == f.BlobSha {
				file.Status = "intact"
			}
		}
		integ.ManifestFiles = append(integ.ManifestFiles, file)
	}
	for _, s := range in.Manifest.Stubs {
		if s.Marker == "" {
			return Integrity{}, fmt.Errorf("manifest stub %s: %s has no marker", s.Path, s.Symbol)
		}
		e, ok := at[s.Path]
		if !ok || e.Mode == modeSymlink || e.Mode == modeGitlink {
			continue
		}
		blob, err := catBlob(in.Git, e.Blob)
		if err != nil {
			return Integrity{}, err
		}
		if bytes.Contains(blob, []byte(s.Marker)) {
			integ.StubsRemaining = append(integ.StubsRemaining, s.Path+": "+s.Symbol)
		}
	}
	seen := map[string]bool{}
	for _, v := range in.Violations {
		switch v.Code {
		case codeProtectedPath:
			integ.StaticFindings = append(integ.StaticFindings, finding("protected_path_changed", v))
		case codeGitlinkDenied:
			f := finding("protected_path_changed", v)
			f.Detail = truncRunes("gitlink denied: "+v.Detail, 300)
			integ.StaticFindings = append(integ.StaticFindings, f)
		case codeTestTamper:
			switch v.SubCode {
			case subManifestChanged:
				// reported by manifest_files
			case subDiscoveryChanged:
				d := DiscoveryChange{Path: v.Path, Kind: "whole_file_changed"}
				if k, ok := strings.CutPrefix(v.Detail, "semantic key "); ok {
					if name, ok := strings.CutSuffix(k, " changed"); ok {
						d.Key, d.Kind = name, "semantic_key_changed"
					}
				}
				if key := d.Path + "\x00" + d.Key + "\x00" + d.Kind; !seen[key] {
					seen[key] = true
					integ.DiscoveryChanges = append(integ.DiscoveryChanges, d)
				}
			case subTestDeleted, subExistingModified, subSkipMarker, subTestEnvDetection, subTestDependency:
				integ.StaticFindings = append(integ.StaticFindings, finding(v.SubCode, v))
			default:
				return Integrity{}, fmt.Errorf("ingestion violation with unknown sub-code %q at %s", v.SubCode, v.Path)
			}
		default:
			return Integrity{}, fmt.Errorf("ingestion violation with unknown code %q at %s", v.Code, v.Path)
		}
	}
	return integ, nil
}

func finding(code string, v Violation) IntegrityFinding {
	return IntegrityFinding{Code: code, Path: v.Path, Line: v.Line, Severity: "violation", Detail: truncRunes(v.Detail, 300)}
}

// DeriveIsolationFiles derives the files the G4b isolation run removes: every test-, fixture- or test-infra-classified
// file added in red..head (section 14.1 step 4, TDD-2). A rename is a delete and an add; a gitlink is not a file.
func DeriveIsolationFiles(in IsolationFilesInput) (IsolationFacts, error) {
	var globs []string
	for _, k := range []string{"/spec/testPaths", "/spec/fixturePaths", "/spec/testInfraPaths"} {
		l, err := stringList(in.Effective.Values[k])
		if err != nil {
			return IsolationFacts{}, fmt.Errorf("effective %s: %w", k, err)
		}
		globs = append(globs, l...)
	}
	entries, err := DeriveDiff(in.Git, in.Red, in.Head, maxDerivedPaths)
	if err != nil {
		return IsolationFacts{}, err
	}
	files := []string{}
	for _, e := range entries {
		if e.Status == "A" && e.NewMode != modeGitlink && matchAny(globs, e.Path) && !e2eProtected(in.Effective, e.Path) {
			files = append(files, e.Path)
		}
	}
	sort.Strings(files)
	return isolationFacts(files), nil
}

// isolationFacts attaches the count and the digest to an already sorted file list.
func isolationFacts(sorted []string) IsolationFacts {
	sum := sha256.Sum256([]byte(strings.Join(sorted, "\n")))
	return IsolationFacts{Files: sorted, Count: len(sorted), Sha256: hex.EncodeToString(sum[:])}
}
