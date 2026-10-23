package verify

import (
	"fmt"
	"sync"
	"unicode/utf8"

	"github.com/dlclark/regexp2"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// ---------------------------------------------------------------------------------------------------------------
// String limits of contracts/test-manifest.schema.yaml (decision D16 N1/N2).
//
// Every string constraint of the schema that a manifest item must satisfy is one entry of manifestLimits, at its
// schema location. The entries are evaluated by the same validator and configuration the platform uses to validate
// contracts (src/internal/contracts: draft 2020-12, asserted formats, the ECMA-262 regexp2 engine), compiled from
// this table in memory, so lengths are counted in code points, `.` and `\s` follow the engine, and date-time is the
// validator's. The library reads no file: TestManifestLimits_TableMatchesSchema compares the table with the schema
// file, so the two cannot drift. R14 (ClassifyRedGen) and BuildManifest both check items through this table.

// schemaString is a string constraint of the schema: a pattern, a maximum length (code points) or a format.
type schemaString struct {
	loc       string // JSON pointer of the constraint in test-manifest.schema.yaml ($ref resolved)
	pattern   string
	maxLength int
	format    string
}

// The limits by name.
const (
	limGitSha       = "GitSha"
	limObjectID     = "ObjectId"
	limSha256       = "Sha256"
	limACID         = "AcId"
	limRepoPath     = "RepoPath"
	limTestID       = "TestId"
	limTaskID       = "task_id"
	limRepository   = "repository"
	limCreatedAt    = "created_at"
	limSuite        = "tests.suite"
	limStubSymbol   = "stubs.symbol"
	limStubMarker   = "stubs.marker"
	limStubLanguage = "stubs.language"
	limSemanticKey  = "semantic_keys.key"
	limTestPathGlob = "test_paths.item"
)

var manifestLimits = map[string]schemaString{
	limGitSha:       {loc: "/$defs/GitSha", pattern: `^[a-f0-9]{40}$`},
	limObjectID:     {loc: "/$defs/ObjectId", pattern: `^([a-f0-9]{40}|[a-f0-9]{64})$`},
	limSha256:       {loc: "/$defs/Sha256", pattern: `^sha256:[a-f0-9]{64}$`},
	limACID:         {loc: "/$defs/AcId", pattern: `^AC-[1-9][0-9]{0,2}$`},
	limRepoPath:     {loc: "/$defs/RepoPath", pattern: `^(?!/)(?!.*(^|/)\.\.(/|$))[A-Za-z0-9._@+/-]{1,400}$`},
	limTestID:       {loc: "/$defs/TestId", pattern: `^[a-z][a-z0-9-]{0,40}::[^\s].{0,498}$`},
	limTaskID:       {loc: "/properties/task_id", pattern: `^sc-[0-9]+$`},
	limRepository:   {loc: "/properties/repository", pattern: `^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`},
	limCreatedAt:    {loc: "/properties/created_at", format: "date-time"},
	limSuite:        {loc: "/properties/tests/items/properties/suite", pattern: `^[a-z][a-z0-9-]{0,40}$`},
	limStubSymbol:   {loc: "/properties/stubs/items/properties/symbol", maxLength: 200},
	limStubMarker:   {loc: "/properties/stubs/items/properties/marker", pattern: `^SC-STUB\(sc-[0-9]+( AC-[1-9][0-9]{0,2})+\)$`},
	limStubLanguage: {loc: "/properties/stubs/items/properties/language", maxLength: 30},
	limSemanticKey:  {loc: "/properties/discovery_protection/properties/semantic_keys/items/properties/key", maxLength: 200},
	limTestPathGlob: {loc: "/properties/test_paths/properties/test/items", maxLength: 200},
}

// testPathLimitLocs are the further schema locations limTestPathGlob stands for (the three test_paths lists).
var testPathLimitLocs = []string{
	"/properties/test_paths/properties/test/items",
	"/properties/test_paths/properties/fixture/items",
	"/properties/test_paths/properties/test_infra/items",
}

const limitsURL = "https://strange.company/internal/verify/manifest-limits.json"

// compiledLimits compiles the table once. An error (impossible for the fixed table, but checked) makes every limit
// check fail, so nothing passes on a broken table (22-R-3).
var compiledLimits = sync.OnceValues(func() (map[string]*jsonschema.Schema, error) {
	defs := map[string]any{}
	for name, l := range manifestLimits {
		d := map[string]any{"type": "string"}
		if l.pattern != "" {
			d["pattern"] = l.pattern
		}
		if l.maxLength > 0 {
			d["maxLength"] = l.maxLength
		}
		if l.format != "" {
			d["format"] = l.format
		}
		defs[name] = d
	}
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	c.AssertFormat()
	c.UseRegexpEngine(func(s string) (jsonschema.Regexp, error) {
		re, err := regexp2.Compile(s, regexp2.ECMAScript)
		if err != nil {
			return nil, err
		}
		return limitRegexp{re}, nil
	})
	if err := c.AddResource(limitsURL, map[string]any{"$defs": defs}); err != nil {
		return nil, err
	}
	out := map[string]*jsonschema.Schema{}
	for _, name := range sortedLimitNames() {
		s, err := c.Compile(limitsURL + "#/$defs/" + name)
		if err != nil {
			return nil, fmt.Errorf("manifest limit %s: %w", name, err)
		}
		out[name] = s
	}
	return out, nil
})

func sortedLimitNames() []string {
	names := map[string]bool{}
	for n := range manifestLimits {
		names[n] = true
	}
	return sortedKeys(names)
}

// limitRegexp adapts regexp2 to the validator exactly as src/internal/contracts does.
type limitRegexp struct{ re *regexp2.Regexp }

func (e limitRegexp) MatchString(s string) bool {
	ok, err := e.re.MatchString(s)
	return err == nil && ok
}

func (e limitRegexp) String() string { return e.re.String() }

// withinLimit reports whether s satisfies the named schema limit. A string that is not valid UTF-8 cannot be
// serialized faithfully into the JSON document and never satisfies a limit.
func withinLimit(name, s string) bool {
	if !utf8.ValidString(s) {
		return false
	}
	ls, err := compiledLimits()
	if err != nil {
		return false
	}
	sch := ls[name]
	if sch == nil {
		return false
	}
	return sch.Validate(s) == nil
}

func validRepoPath(p string) bool { return withinLimit(limRepoPath, p) }

func validTestID(id string) bool { return withinLimit(limTestID, id) }

func validSuite(s string) bool { return withinLimit(limSuite, s) }

// itemViolations lists, in document order, the per-item limits the item lists of a manifest break: files, tests,
// ac_mapping, stubs and test_paths. R14 checks the would-be manifest of a RED with it, and validateManifestSchema
// checks the built one, so both apply the same limits. withGitFacts adds the files' blob ids, which are Git facts the
// Runner derives (like the commits), not RED content: R14 leaves them to BuildManifest.
func itemViolations(m Manifest, withGitFacts bool) []string {
	var out []string
	add := func(ok bool, what string) {
		if !ok {
			out = append(out, what)
		}
	}
	for i, f := range m.Files {
		add(withinLimit(limRepoPath, f.Path), fmt.Sprintf("files[%d].path", i))
		if withGitFacts {
			add(withinLimit(limObjectID, f.BlobSha), fmt.Sprintf("files[%d].blob_sha", i))
		}
		add(f.Class == "test" || f.Class == "fixture" || f.Class == "test_infra" || f.Class == "judge", fmt.Sprintf("files[%d].class", i))
		add(f.Change == "added" || f.Change == "modified", fmt.Sprintf("files[%d].change", i))
	}
	for i, t := range m.Tests {
		add(withinLimit(limTestID, t.ID), fmt.Sprintf("tests[%d].id", i))
		add(withinLimit(limSuite, t.Suite), fmt.Sprintf("tests[%d].suite", i))
		add(t.Kind == "new" || t.Kind == "modified", fmt.Sprintf("tests[%d].kind", i))
		add(t.ACIDs != nil, fmt.Sprintf("tests[%d].ac_ids", i))
		for j, ac := range t.ACIDs {
			add(withinLimit(limACID, ac), fmt.Sprintf("tests[%d].ac_ids[%d]", i, j))
		}
		add(t.RedStatus == "failed_assertion" || t.RedStatus == "stub_unimplemented" || t.RedStatus == "passed", fmt.Sprintf("tests[%d].red_status", i))
	}
	for i, a := range m.ACMapping {
		add(withinLimit(limACID, a.ACID), fmt.Sprintf("ac_mapping[%d].ac_id", i))
		add(a.Kind == "change" || a.Kind == "preservation", fmt.Sprintf("ac_mapping[%d].kind", i))
		add(len(a.Tests) >= 1, fmt.Sprintf("ac_mapping[%d].tests", i))
		for j, id := range a.Tests {
			add(withinLimit(limTestID, id), fmt.Sprintf("ac_mapping[%d].tests[%d]", i, j))
		}
	}
	for i, s := range m.Stubs {
		add(withinLimit(limRepoPath, s.Path), fmt.Sprintf("stubs[%d].path", i))
		add(withinLimit(limStubSymbol, s.Symbol), fmt.Sprintf("stubs[%d].symbol", i))
		add(withinLimit(limStubMarker, s.Marker), fmt.Sprintf("stubs[%d].marker", i))
		add(s.Shape == "zero_value" || s.Shape == "panic", fmt.Sprintf("stubs[%d].shape", i))
		add(s.Language == "" || withinLimit(limStubLanguage, s.Language), fmt.Sprintf("stubs[%d].language", i))
		add(len(s.ACIDs) >= 1, fmt.Sprintf("stubs[%d].ac_ids", i))
		for j, ac := range s.ACIDs {
			add(withinLimit(limACID, ac), fmt.Sprintf("stubs[%d].ac_ids[%d]", i, j))
		}
	}
	for _, p := range []struct {
		name string
		list []string
	}{{"test", m.TestPaths.Test}, {"fixture", m.TestPaths.Fixture}, {"test_infra", m.TestPaths.TestInfra}} {
		add(p.list != nil, "test_paths."+p.name)
		for i, g := range p.list {
			add(withinLimit(limTestPathGlob, g), fmt.Sprintf("test_paths.%s[%d]", p.name, i))
		}
	}
	return out
}
