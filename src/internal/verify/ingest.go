package verify

import (
	"fmt"
	"path"
	"regexp"
	"sort"

	"strange.company/sc/internal/policy"
)

// Violation codes and sub-codes of plan 22 section 14.6.
const (
	codeTestTamper      = "TEST_TAMPER"
	codeProtectedPath   = "PROTECTED_PATH_VIOLATION"
	codeGitlinkDenied   = "GITLINK_DENIED"
	subProtectedPath    = "protected_path_changed"
	subManifestChanged  = "manifest_file_changed"
	subTestDeleted      = "test_file_deleted"
	subExistingModified = "existing_test_modified"
	subTestDependency   = "test_dependency_changed"
	subSkipMarker       = "skip_marker_added"
	subDiscoveryChanged = "discovery_config_changed"
	subTestEnvDetection = "test_env_detection"
	modeGitlink         = "160000"
	modeSymlink         = "120000"
)

// testDependencyKeys are the semantic keys that are test dependencies (file base name to keys): a change is
// test_dependency_changed (and is also a discovery key change when the platform lists it there).
var testDependencyKeys = map[string]map[string]bool{
	"go.mod":       {"replace": true},
	"Cargo.toml":   {"patch": true, "dev-dependencies": true},
	"package.json": {"devDependencies": true},
}

// CheckIngestion applies the test-protection rules of plan 22 section 14.6 to an implementer or producer diff. The
// diff is the Git tree difference Start..Head derived by the Runner (section 14.11); a range over MaxFilesChanged is
// DIFF_TOO_LARGE before any rule runs. The violations are sorted by (Path, Code, SubCode, Line).
func CheckIngestion(in IngestionInput) ([]Violation, error) {
	if in.Role != "implementer" && in.Role != "producer" {
		return nil, fmt.Errorf("ingestion judges implementer and producer diffs, not role %q", in.Role)
	}
	if err := checkRevision(in.Manifest.TestCommit); err != nil {
		return nil, fmt.Errorf("manifest test_commit: %w", err)
	}
	entries, err := DeriveDiff(in.Git, in.Start, in.Head, in.MaxFilesChanged)
	if err != nil {
		return nil, err
	}
	pol, err := loadIngestPolicy(in)
	if err != nil {
		return nil, err
	}
	c := &ingestCheck{in: in, pol: pol, seen: map[string]bool{}, manifest: map[string]bool{}, manifestWhole: map[string]bool{}, headWhole: map[string]bool{}, headBroken: map[string]string{}}
	for _, f := range in.Manifest.Files {
		c.manifest[f.Path] = true
	}
	for _, w := range in.Manifest.DiscoveryProtection.WholeFiles {
		c.manifestWhole[w.Path] = true
	}
	if in.Validation != nil && len(entries) > 0 {
		dp, bad, err := deriveDiscovery(DiscoveryInput{Git: in.Git, Rev: in.Head, Validation: in.Validation, Platform: in.Platform, Effective: in.Effective})
		if err != nil {
			return nil, fmt.Errorf("discovery protection at the head: %w", err)
		}
		for _, w := range dp.WholeFiles {
			c.headWhole[w.Path] = true
		}
		// A file the head derivation could not read is reported as a discovery change (D18 e), not as an error.
		for _, b := range bad {
			c.headBroken[b.Path] = b.Err.Error()
		}
	}
	for _, e := range entries {
		if err := c.entry(e); err != nil {
			return nil, err
		}
	}
	for p, why := range c.headBroken {
		c.tamper(subDiscoveryChanged, p, "the discovery protection of the head cannot be derived: "+why)
	}
	sort.SliceStable(c.out, func(i, j int) bool {
		a, b := c.out[i], c.out[j]
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		if a.Code != b.Code {
			return a.Code < b.Code
		}
		if a.SubCode != b.SubCode {
			return a.SubCode < b.SubCode
		}
		return a.Line < b.Line
	})
	return c.out, nil
}

type ingestPolicy struct {
	protected []string
	testClass []string
	disc      discoveryPolicy
	skip      map[string][]*regexp.Regexp
	env       map[string][]*regexp.Regexp
}

func loadIngestPolicy(in IngestionInput) (ingestPolicy, error) {
	var p ingestPolicy
	var err error
	if p.protected, err = protectedPaths(RedInput{Platform: in.Platform, Effective: in.Effective}); err != nil {
		return p, err
	}
	for _, k := range []string{"/spec/testPaths", "/spec/fixturePaths", "/spec/testInfraPaths"} {
		l, err := stringList(in.Effective.Values[k])
		if err != nil {
			return p, fmt.Errorf("effective %s: %w", k, err)
		}
		p.testClass = append(p.testClass, l...)
	}
	if p.disc, err = loadDiscoveryPolicy(in.Platform, in.Effective); err != nil {
		return p, err
	}
	if p.skip, err = staticPatterns(in, "skipMarkers"); err != nil {
		return p, err
	}
	if p.env, err = staticPatterns(in, "testEnvDetection"); err != nil {
		return p, err
	}
	return p, nil
}

// staticPatterns compiles the effective patterns of a static check per language: the merged list when the
// repository extended it, else the platform's.
func staticPatterns(in IngestionInput, check string) (map[string][]*regexp.Regexp, error) {
	spec, err := specMap(in.Platform)
	if err != nil {
		return nil, err
	}
	langs := map[string][]string{}
	if sc, ok := pathMap(spec, "staticChecks", check); ok {
		for lang, v := range sc {
			if langs[lang], err = stringList(v); err != nil {
				return nil, fmt.Errorf("staticChecks.%s.%s: %w", check, lang, err)
			}
		}
	}
	prefix := "/spec/staticChecks/" + check + "/"
	for k, v := range in.Effective.Values {
		if len(k) > len(prefix) && k[:len(prefix)] == prefix {
			if langs[k[len(prefix):]], err = stringList(v); err != nil {
				return nil, fmt.Errorf("effective %s: %w", k, err)
			}
		}
	}
	out := map[string][]*regexp.Regexp{}
	for lang, pats := range langs {
		for _, pat := range pats {
			re, err := regexp.Compile(pat)
			if err != nil {
				return nil, fmt.Errorf("staticChecks.%s.%s pattern %q: %w", check, lang, pat, err)
			}
			out[lang] = append(out[lang], re)
		}
	}
	return out, nil
}

type ingestCheck struct {
	in            IngestionInput
	pol           ingestPolicy
	manifest      map[string]bool
	manifestWhole map[string]bool
	headWhole     map[string]bool
	headBroken    map[string]string
	redTree       map[string]TreeEntry
	out           []Violation
	seen          map[string]bool
}

func (c *ingestCheck) add(v Violation) {
	k := fmt.Sprintf("%s\x00%s\x00%s\x00%d", v.Code, v.SubCode, v.Path, v.Line)
	if c.seen[k] {
		return
	}
	c.seen[k] = true
	c.out = append(c.out, v)
}

func (c *ingestCheck) tamper(sub, p, detail string) {
	c.add(Violation{Code: codeTestTamper, SubCode: sub, Path: p, Detail: detail})
}

// red returns the tree of the red commit (the manifest's test_commit), read once.
func (c *ingestCheck) red() (map[string]TreeEntry, error) {
	if c.redTree != nil {
		return c.redTree, nil
	}
	tree, err := LsTree(c.in.Git, c.in.Manifest.TestCommit)
	if err != nil {
		return nil, err
	}
	c.redTree = make(map[string]TreeEntry, len(tree))
	for _, e := range tree {
		c.redTree[e.Path] = e
	}
	return c.redTree, nil
}

func (c *ingestCheck) entry(e DiffEntry) error {
	p := e.Path
	if e.OldMode == modeGitlink || e.NewMode == modeGitlink {
		c.add(Violation{Code: codeGitlinkDenied, Path: p, Detail: "a gitlink (mode 160000) is denied in an autonomous diff"})
	}
	if matchAny(c.pol.protected, p) {
		c.add(Violation{Code: codeProtectedPath, SubCode: subProtectedPath, Path: p, Detail: "protected path " + e.Status})
	}
	inManifest := c.manifest[p]
	if inManifest {
		c.tamper(subManifestChanged, p, "manifest file "+e.Status)
	}
	testClass := matchAny(c.pol.testClass, p)
	if testClass && e.Status == "D" {
		c.tamper(subTestDeleted, p, "test, fixture or test infrastructure file deleted")
	}
	if testClass && !inManifest && e.Status != "D" {
		red, err := c.red()
		if err != nil {
			return err
		}
		if _, existed := red[p]; existed {
			c.tamper(subExistingModified, p, "file existing at the red commit modified")
		}
	}
	if matchAny(c.pol.disc.testDepPaths, p) {
		c.tamper(subTestDependency, p, "test dependency path changed")
	}
	if matchAny(c.pol.disc.wholeGlobs, p) || c.manifestWhole[p] || c.headWhole[p] {
		c.tamper(subDiscoveryChanged, p, "discovery-protected file "+e.Status)
	}
	if e.Status == "A" && e.NewMode != modeGitlink && matchAny(c.pol.disc.forbidNew, p) { // a gitlink is GITLINK_DENIED alone
		c.tamper(subDiscoveryChanged, p, "a file that changes test discovery was added")
	}
	if err := c.semantic(e); err != nil {
		return err
	}
	return c.static(e, testClass)
}

// semantic compares the protected keys of a changed file at the red commit (or as the manifest recorded them)
// with their value at the head.
func (c *ingestCheck) semantic(e DiffEntry) error {
	if e.NewMode == modeGitlink {
		return nil // a gitlink has no blob to parse; GITLINK_DENIED already covers it
	}
	type key struct{ format, name string }
	keys := map[string]key{}
	for _, ent := range c.pol.disc.semantic {
		if policy.MatchGlob(ent.Path, e.Path) {
			for _, k := range ent.Keys {
				keys[k] = key{ent.Format, k}
			}
		}
	}
	recorded := map[string]*string{}
	for _, k := range c.in.Manifest.DiscoveryProtection.SemanticKeys {
		if k.Path == e.Path {
			recorded[k.Key] = k.ValueSha256
			if _, ok := keys[k.Key]; !ok {
				keys[k.Key] = key{"json", k.Key}
			}
		}
	}
	if len(keys) == 0 {
		return nil
	}
	red, err := c.red()
	if err != nil {
		return err
	}
	var redContent, headContent []byte
	if t, ok := red[e.Path]; ok && t.Mode != modeGitlink {
		if redContent, err = catBlob(c.in.Git, t.Blob); err != nil {
			return err
		}
	}
	if e.Status != "D" && e.NewMode != modeGitlink {
		if headContent, err = catBlob(c.in.Git, e.NewBlob); err != nil {
			return err
		}
	}
	names := make([]string, 0, len(keys))
	for k := range keys {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, name := range names {
		k := keys[name]
		head, herr := semanticValueHash(k.format, e.Path, headContent, name)
		var base *string
		var berr error
		if r, ok := recorded[name]; ok {
			base = r
		} else {
			base, berr = semanticValueHash(k.format, e.Path, redContent, name)
		}
		changed := false
		switch {
		case herr != nil:
			changed = true // an unparsable head file hides its keys (fail closed)
		case berr != nil:
			changed = redContent != nil && string(redContent) != string(headContent)
		default:
			changed = !equalHash(base, head)
		}
		if !changed {
			continue
		}
		if testDependencyKeys[path.Base(e.Path)][name] {
			c.tamper(subTestDependency, e.Path, "test dependency key "+name+" changed")
		}
		c.tamper(subDiscoveryChanged, e.Path, "semantic key "+name+" changed")
	}
	return nil
}

func equalHash(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// static applies the skip-marker check to the lines added to test-classified files and the test-environment
// detection check to the lines added to other, unprotected files.
func (c *ingestCheck) static(e DiffEntry, testClass bool) error {
	if e.Status == "D" || (e.NewMode != "100644" && e.NewMode != "100755") {
		return nil
	}
	lang := languageOfPath(e.Path)
	var pats []*regexp.Regexp
	sub := subSkipMarker
	switch {
	case testClass:
		pats = c.pol.skip[lang]
	case !matchAny(c.pol.protected, e.Path):
		pats, sub = c.pol.env[lang], subTestEnvDetection
	}
	if lang == "" || len(pats) == 0 {
		return nil
	}
	lines, err := AddedLines(c.in.Git, c.in.Start, c.in.Head, e.Path)
	if err != nil {
		return err
	}
	for _, l := range lines {
		for _, re := range pats {
			if re.MatchString(l.Text) {
				c.add(Violation{Code: codeTestTamper, SubCode: sub, Path: e.Path, Line: l.Line, Detail: "matches " + re.String()})
				break
			}
		}
	}
	return nil
}

// IngestionInput is everything CheckIngestion needs: the role, the range Start..Head in the Git repository, the
// role's maxFilesChanged, the manifest (its TestCommit is the red commit) and the policies. Validation is the parsed
// validation.yaml of the base revision; when it is not nil the discovery-protected files are also derived at Head
// (a file that make would now pick up, for example through a literal $(wildcard), is a discovery change).
type IngestionInput struct {
	Role            string
	Git             GitRepo
	Start           string
	Head            string
	MaxFilesChanged int
	Manifest        Manifest
	Platform        policy.Platform
	Effective       policy.Effective
	Validation      map[string]any
}

// Violation is one ingestion violation. Code is TEST_TAMPER (with SubCode), PROTECTED_PATH_VIOLATION or
// GITLINK_DENIED.
type Violation struct {
	Code    string
	SubCode string
	Path    string
	Line    int
	Detail  string
}

// DiscoveryInput names the revision to snapshot, the parsed validation.yaml and the policies.
type DiscoveryInput struct {
	Git        GitRepo
	Rev        string
	Validation map[string]any
	Platform   policy.Platform
	Effective  policy.Effective
}
