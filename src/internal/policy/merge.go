package policy

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"strange.company/sc/internal/contracts"
)

// CodeRepoPolicyInvalid is the code of a repository policy that fails its schema (plan 22 section 9.4).
const CodeRepoPolicyInvalid = "REPO_POLICY_INVALID"

// CodePlatformPolicyInvalid reports a platform value the merge cannot compare with (a missing value or role, an
// empty or un-marshalable document, a malformed number or duration). The platform tree is validated by Check, so
// this signals a caller error; the merge fails closed.
const CodePlatformPolicyInvalid = "PLATFORM_POLICY_INVALID"

// Platform is the platform side of a merge: the VerificationPolicy document, the optional ModelRouting document
// (routing rows of plan 22 section 9.4) and the RoleDefinition documents by role name (diffLimits rows).
type Platform struct {
	Verification map[string]any
	ModelRouting map[string]any
	Roles        map[string]map[string]any
}

// Effective is the merged policy. Values maps the JSON pointer of each repository-policy field that was set to its
// merged value. It is nil when the repository policy is rejected (no partial result).
type Effective struct {
	Values map[string]any
	// Name is metadata.name of the merged repository policy; empty when the policy is rejected.
	Name string
}

// MergePolicy applies the tighten-only merge of plan 22 section 9.4 to a repository policy document.
//
// The repository document is validated against contracts/repo-policy.schema.yaml first (REPO_POLICY_INVALID with
// the pointer of each schema error). The merge then runs regardless, so that one call reports every problem:
// POLICY_LOOSENING:<pointer> for each value that would loosen the platform. Issues are sorted; Values is nil
// whenever there is any issue. Neither input is modified.
func MergePolicy(opts Options, platform Platform, repo map[string]any) (Effective, []Issue) {
	m := &merger{platform: platform, values: map[string]any{}}
	// Every document is normalised to JSON values first (a deep copy, so no input is modified and Go-native
	// types such as []string behave like YAML-parsed ones).
	doc, ok := m.normalize(opts, repo)
	if ok {
		m.repo = doc
		m.run()
	}
	if len(m.issues) == 0 {
		name, _ := valAt(m.repo, "metadata", "name").(string)
		return Effective{Values: m.values, Name: name}, nil
	}
	return Effective{}, normalizeIssues(m.issues)
}

type merger struct {
	platform Platform
	repo     map[string]any
	values   map[string]any
	issues   []Issue
}

func (m *merger) add(code, ptr, format string, args ...any) {
	m.issues = append(m.issues, Issue{Code: code, Pointer: ptr, File: repoPolicyFile, Message: fmt.Sprintf(format, args...)})
}

// normalizeIssues sorts the issues and joins the messages of issues with the same code and pointer.
func normalizeIssues(is []Issue) []Issue {
	sort.SliceStable(is, func(i, j int) bool {
		if is[i].Pointer != is[j].Pointer {
			return is[i].Pointer < is[j].Pointer
		}
		if is[i].Code != is[j].Code {
			return is[i].Code < is[j].Code
		}
		return is[i].Message < is[j].Message
	})
	var out []Issue
	for _, i := range is {
		if n := len(out); n > 0 && out[n-1].Code == i.Code && out[n-1].Pointer == i.Pointer {
			if out[n-1].Message != i.Message {
				out[n-1].Message += "; " + i.Message
			}
			continue
		}
		out = append(out, i)
	}
	return out
}

// normalize validates the repository document against its schema and returns its JSON-normalised copy; ok is
// false when there is nothing to merge. It also normalises the platform documents.
func (m *merger) normalize(opts Options, repo map[string]any) (map[string]any, bool) {
	if repo == nil {
		m.add(CodeRepoPolicyInvalid, "", "the repository policy is empty")
		return nil, false
	}
	doc, err := jsonRoundTrip(repo)
	if err != nil {
		m.add(CodeRepoPolicyInvalid, "", "%s", err.Error())
		return nil, false
	}
	rm, isMap := doc.(map[string]any)
	if !isMap {
		m.add(CodeRepoPolicyInvalid, "", "the repository policy is not an object")
		return nil, false
	}
	m.schema(opts, doc)
	p := &m.platform
	if p.Verification == nil {
		m.add(CodePlatformPolicyInvalid, "", "the platform verification policy is empty")
		return nil, false
	}
	for _, e := range []struct {
		name string
		d    *map[string]any
	}{{"verification", &p.Verification}, {"model routing", &p.ModelRouting}} {
		name, d := e.name, e.d
		if *d == nil {
			continue
		}
		nm, err := normalizePlatformDoc(*d)
		if err != nil {
			m.add(CodePlatformPolicyInvalid, "", "the platform %s policy cannot be compared with: %s", name, oneLine(err.Error()))
			return nil, false
		}
		*d = nm
	}
	if p.Roles != nil {
		roles := map[string]map[string]any{}
		for k, d := range p.Roles {
			nm, err := normalizePlatformDoc(d)
			if err != nil {
				m.add(CodePlatformPolicyInvalid, "", "the platform role %q cannot be compared with: %s", k, oneLine(err.Error()))
				return nil, false
			}
			roles[k] = nm
		}
		p.Roles = roles
	}
	return rm, true
}

// normalizePlatformDoc returns the JSON-normalised copy of a platform document.
func normalizePlatformDoc(d map[string]any) (map[string]any, error) {
	n, err := jsonRoundTrip(d)
	if err != nil {
		return nil, err
	}
	nm, ok := n.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("not an object")
	}
	return nm, nil
}

func (m *merger) schema(opts Options, doc any) {
	schemas, err := contracts.CompileSchemas(opts.ContractsDir)
	if err != nil || schemas[repoPolicySchemaStem] == nil {
		msg := "no repo-policy schema"
		if err != nil {
			msg = err.Error()
		}
		m.add(CodeUnreadable, "", "cannot load the repository policy schema: %s", oneLine(msg))
		return
	}
	for _, e := range validate(schemas[repoPolicySchemaStem], doc) {
		m.add(CodeRepoPolicyInvalid, e[0], "%s", e[1])
	}
}

func jsonRoundTrip(v any) (any, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return contracts.ParseYAML(b)
}

func (m *merger) run() {
	v := m.platform.Verification
	r := m.repo

	// Union rows.
	m.union("/spec/protectedPaths", strList(v, "spec", "protectedPaths"), strList(r, "spec", "protectedPaths"), has(r, "spec", "protectedPaths"))
	m.union("/spec/protectedBranches", strList(v, "spec", "protectedBranches"), strList(r, "spec", "protectedBranches"), has(r, "spec", "protectedBranches"))
	m.union("/spec/discoveryConfig/wholeFiles", strList(v, "spec", "testProtection", "discoveryConfig", "wholeFiles"),
		strList(r, "spec", "discoveryConfig", "wholeFiles"), has(r, "spec", "discoveryConfig", "wholeFiles"))
	m.union("/spec/humanAuthority/machineAccounts", strList(v, "spec", "humanAuthority", "machineAccounts"),
		strList(r, "spec", "humanAuthority", "machineAccounts"), has(r, "spec", "humanAuthority", "machineAccounts"))
	m.union("/spec/merge/elevatedPaths", strList(v, "spec", "merge", "elevatedPaths"), strList(r, "spec", "merge", "elevatedPaths"), has(r, "spec", "merge", "elevatedPaths"))
	m.union("/spec/merge/additionalStatusContexts", strList(v, "spec", "merge", "requiredStatusContexts"),
		strList(r, "spec", "merge", "additionalStatusContexts"), has(r, "spec", "merge", "additionalStatusContexts"))
	m.semanticKeys()
	for _, check := range []string{"skipMarkers", "testEnvDetection", "unconditionalFailure"} {
		for _, lang := range sortedKeys(mapAt(r, "spec", "staticChecks", check)) {
			for i, pat := range strList(r, "spec", "staticChecks", check, lang) {
				if _, err := regexp.Compile(pat); err != nil {
					m.add(CodeRegexInvalid, fmt.Sprintf("/spec/staticChecks/%s/%s/%d", check, lang, i), "pattern %q is not valid RE2: %s", pat, oneLine(err.Error()))
				}
			}
			m.union("/spec/staticChecks/"+check+"/"+lang, strList(v, "spec", "staticChecks", check, lang),
				strList(r, "spec", "staticChecks", check, lang), true)
		}
	}
	if has(r, "spec", "reservedBranchPrefixes") {
		m.values["/spec/reservedBranchPrefixes"] = anyStrings(strList(r, "spec", "reservedBranchPrefixes"))
	}

	// Allow sets (intersection).
	var classes []string
	for _, c := range listAt(v, "spec", "exemptionClasses") {
		classes = append(classes, strAt(c, "name"))
	}
	m.intersect("/spec/verification/allowedExemptionClasses", classes, r, "spec", "verification", "allowedExemptionClasses")
	m.intersect("/spec/verification/stubPanicLanguages", strList(v, "spec", "stubPolicy", "allowPanicStubsFor"), r, "spec", "verification", "stubPanicLanguages")
	m.intersect("/spec/humanAuthority/forgejoTeams", strList(v, "spec", "humanAuthority", "forgejoTeams"), r, "spec", "humanAuthority", "forgejoTeams")
	m.intersect("/spec/modelRouting/allowedProviders", sortedKeys(mapAt(m.platform.ModelRouting, "spec", "providers")), r, "spec", "modelRouting", "allowedProviders")

	// Maxima (min).
	for _, k := range []string{"maxAutonomousAttempts", "maxTesterAttempts", "maxReviewExecutions", "maxAuxiliaryExecutions"} {
		m.bound("/spec/retry/"+k, valAt(v, "spec", "retry", k), valAt(r, "spec", "retry", k), true)
	}
	m.bound("/spec/testSpecConflict/maxConflictsPerTask", valAt(v, "spec", "testSpecConflict", "maxConflictsPerTask"), valAt(r, "spec", "testSpecConflict", "maxConflictsPerTask"), true)
	m.bound("/spec/task/maxBaseLag/commits", valAt(v, "spec", "task", "maxBaseLag", "commits"), valAt(r, "spec", "task", "maxBaseLag", "commits"), true)
	for _, role := range sortedKeys(mapAt(r, "spec", "diffLimits")) {
		if m.platform.Roles[role] == nil {
			m.add(CodePlatformPolicyInvalid, "/spec/diffLimits/"+role, "the platform has no role definition %q", role)
			continue
		}
		for _, k := range []string{"maxFilesChanged", "maxDiffBytes"} {
			m.bound("/spec/diffLimits/"+role+"/"+k, valAt(m.platform.Roles[role], "spec", "diffRules", k), valAt(r, "spec", "diffLimits", role, k), true)
		}
	}
	m.tiers()

	// Minima (max).
	m.bound("/spec/merge/humanApprovalsRequired", valAt(v, "spec", "merge", "humanApprovalsRequired"), valAt(r, "spec", "merge", "humanApprovalsRequired"), false)

	// Durations (min by value).
	m.duration("/spec/task/maxBaseLag/age", valAt(v, "spec", "task", "maxBaseLag", "age"), valAt(r, "spec", "task", "maxBaseLag", "age"), true)
	for _, g := range sortedKeys(mapAt(r, "spec", "watchdog", "groups")) {
		for _, k := range []string{"alertAfter", "escalateAfter"} {
			m.duration("/spec/watchdog/groups/"+g+"/"+k, valAt(v, "spec", "watchdog", "groups", g, k), valAt(r, "spec", "watchdog", "groups", g, k), false)
		}
	}

	// Ordered enums (stricter wins).
	m.enum("/spec/testProtection/existingTestModification", strAt(v, "spec", "testProtection", "existingTestModification"),
		strAt(r, "spec", "testProtection", "existingTestModification"), []string{"flag", "deny"})
	m.enum("/spec/testSpecConflict/resolutionApproval", strAt(v, "spec", "testSpecConflict", "resolutionApproval"),
		strAt(r, "spec", "testSpecConflict", "resolutionApproval"), []string{"adjudicator", "human"})
	if has(r, "spec", "verification", "requireHumanApproval") {
		var human []string
		for _, c := range listAt(v, "spec", "exemptionClasses") {
			if strAt(c, "approval") == "human" {
				human = append(human, strAt(c, "name"))
			}
		}
		m.union("/spec/verification/requireHumanApproval", human, strList(r, "spec", "verification", "requireHumanApproval"), true)
	}

	m.definitions()
	m.enforcementPlane()
}

// enforcementPlaneGlobs is the enforcement plane of plan 22 section 9.3 (jobbuilder is read as jobs, the directory
// of the Job builder, brief D3).
var enforcementPlaneGlobs = []string{
	"src/internal/verify/**", "src/internal/statemachine/**", "src/internal/policy/**", "src/internal/runner/**",
	"src/internal/forge/**", "src/internal/gateway/**", "src/internal/ledger/**", "src/internal/jobs/**",
	"src/internal/scrub/**", "src/internal/lint/**",
	"src/cmd/sc-verify/**", "src/cmd/sc-ci-config/**", "src/cmd/agent-runner/**", "src/cmd/sc-worker/**",
	"src/cmd/model-gateway/**", "src/cmd/sc-gitops/**", "src/cmd/egress-proxy/**", "src/cmd/sc-release/**",
	"src/cmd/sc-e2e/**",
}

// enforcementPlane requires, for the repository strange-company, every glob of the enforcement plane to lie inside
// the union of its elevatedPaths and its protected paths (exact glob inclusion; plan 22 section 9.3, TDD-B4).
func (m *merger) enforcementPlane() {
	var cover []string
	cover = append(cover, strList(m.platform.Verification, "spec", "protectedPaths")...)
	cover = append(cover, strList(m.platform.Verification, "spec", "merge", "elevatedPaths")...)
	for _, is := range planeIssues(cover, m.repo) {
		m.add(is.Code, is.Pointer, "%s", is.Message)
	}
}

// planeIssues reports one issue per enforcement plane glob not covered by the union of platformCover (the platform
// protected and elevated paths) and the protectedPaths and merge.elevatedPaths of repo. It applies to the repository
// strange-company only. Shared by MergePolicy and Check (D20, 22-AC-33).
func planeIssues(platformCover []string, repo map[string]any) []Issue {
	if name, _ := valAt(repo, "metadata", "name").(string); name != "strange-company" {
		return nil
	}
	const ptr = "/spec/merge/elevatedPaths"
	cover := append([]string{}, platformCover...)
	cover = append(cover, strList(repo, "spec", "protectedPaths")...)
	cover = append(cover, strList(repo, "spec", "merge", "elevatedPaths")...)
	var out []Issue
	for _, g := range enforcementPlaneGlobs {
		inside, ok := globInsideUnion(g, cover)
		switch {
		case !ok:
			out = append(out, Issue{Code: CodeGlobTooComplex, Pointer: ptr, File: repoPolicyFile, Message: fmt.Sprintf("cannot decide whether the enforcement plane glob %q is covered (too many states)", g)})
		case !inside:
			out = append(out, Issue{Code: "ENFORCEMENT_PLANE_NOT_ELEVATED", Pointer: ptr, File: repoPolicyFile, Message: fmt.Sprintf("the enforcement plane glob %q is not covered by merge.elevatedPaths or the protected paths", g)})
		}
	}
	return out
}

func has(v any, keys ...string) bool { return valAt(v, keys...) != nil }

func anyStrings(l []string) []any {
	out := make([]any, len(l))
	for i, s := range l {
		out[i] = s
	}
	return out
}

// union sets Values[ptr] to the platform entries followed by the repository entries not already present.
func (m *merger) union(ptr string, platform, repo []string, set bool) {
	if !set {
		return
	}
	seen := map[string]bool{}
	out := []any{}
	for _, l := range [][]string{platform, repo} {
		for _, s := range l {
			if !seen[s] {
				seen[s] = true
				out = append(out, s)
			}
		}
	}
	m.values[ptr] = out
}

// semanticKeys unions discoveryConfig.semanticKeys: entries with the same path and format merge their keys.
func (m *merger) semanticKeys() {
	const ptr = "/spec/discoveryConfig/semanticKeys"
	if !has(m.repo, "spec", "discoveryConfig", "semanticKeys") {
		return
	}
	type ent struct {
		path, format string
		keys         []string
	}
	var ents []*ent
	add := func(l []any) {
		for _, e := range l {
			path, format := strAt(e, "path"), strAt(e, "format")
			var t *ent
			for _, x := range ents {
				if x.path == path && x.format == format {
					t = x
				}
			}
			if t == nil {
				t = &ent{path: path, format: format}
				ents = append(ents, t)
			}
			for _, k := range strList(e, "keys") {
				if !contains(t.keys, k) {
					t.keys = append(t.keys, k)
				}
			}
		}
	}
	add(listAt(m.platform.Verification, "spec", "testProtection", "discoveryConfig", "semanticKeys"))
	add(listAt(m.repo, "spec", "discoveryConfig", "semanticKeys"))
	out := []any{}
	for _, e := range ents {
		o := map[string]any{"path": e.path, "keys": anyStrings(e.keys)}
		if e.format != "" {
			o["format"] = e.format
		}
		out = append(out, o)
	}
	m.values[ptr] = out
}

// intersect merges an allow set: the result keeps the repository entries that the platform allows; an entry the
// platform does not list is POLICY_LOOSENING at its index.
func (m *merger) intersect(ptr string, platform []string, repo map[string]any, keys ...string) {
	if !has(repo, keys...) {
		return
	}
	out := []any{}
	for i, s := range strList(repo, keys...) {
		if contains(platform, s) {
			out = append(out, s)
		} else {
			m.add(CodeLoosening, fmt.Sprintf("%s/%d", ptr, i), "%q is not in the platform set; a repository may only narrow it", s)
		}
	}
	m.values[ptr] = out
}

func toNum(v any) (float64, bool) {
	switch n := v.(type) {
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case uint64:
		return float64(n), true
	case float64:
		return n, true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	}
	return 0, false
}

// bound merges a number: a maximum (min wins, larger is loosening) or, when isMax is false, a minimum (max wins,
// smaller is loosening). A missing platform value fails closed (PLATFORM_POLICY_INVALID).
func (m *merger) bound(ptr string, platform, repo any, isMax bool) {
	if repo == nil {
		return
	}
	rv, ok := toNum(repo)
	if !ok {
		m.add(CodeRepoPolicyInvalid, ptr, "not a number")
		return
	}
	if platform == nil {
		m.add(CodePlatformPolicyInvalid, ptr, "the platform policy has no value to compare with")
		return
	}
	pv, ok := toNum(platform)
	if !ok {
		m.add(CodePlatformPolicyInvalid, ptr, "the platform value is not a number")
		return
	}
	switch {
	case isMax && rv > pv:
		m.add(CodeLoosening, ptr, "%v exceeds the platform maximum %v", repo, platform)
	case !isMax && rv < pv:
		m.add(CodeLoosening, ptr, "%v is below the platform minimum %v", repo, platform)
	default:
		m.values[ptr] = repo
	}
}

func parseDuration(v any) (time.Duration, bool) {
	s, ok := v.(string)
	if !ok {
		return 0, false
	}
	d, err := time.ParseDuration(strings.TrimSpace(s))
	return d, err == nil && d >= 0
}

// duration merges a duration (the shorter wins, compared by value; longer is loosening).
func (m *merger) duration(ptr string, platform, repo any, required bool) {
	if repo == nil {
		return
	}
	rd, ok := parseDuration(repo)
	if !ok {
		m.add(CodeRepoPolicyInvalid, ptr, "%v is not a duration", repo)
		return
	}
	if platform == nil {
		if required {
			m.add(CodePlatformPolicyInvalid, ptr, "the platform policy has no value to compare with")
		} else {
			m.values[ptr] = repo
		}
		return
	}
	pd, ok := parseDuration(platform)
	if !ok {
		m.add(CodePlatformPolicyInvalid, ptr, "the platform value %v is not a duration", platform)
		return
	}
	if rd > pd {
		m.add(CodeLoosening, ptr, "%v is longer than the platform value %v", repo, platform)
		return
	}
	m.values[ptr] = repo
}

// enum merges an ordered enum; order lists the values from loosest to strictest. The stricter value wins; an
// unknown platform value ranks lowest. (The schema already forbids a looser repository value.)
func (m *merger) enum(ptr, platform, repo string, order []string) {
	if repo == "" {
		return
	}
	rank := func(s string) int {
		for i, o := range order {
			if o == s {
				return i + 1
			}
		}
		return 0
	}
	if rank(platform) > rank(repo) {
		repo = platform
	}
	m.values[ptr] = repo
}

var tierOrder = []string{"clerical", "bargain", "standard", "frontier"}

func tierRank(t string) int {
	for i, o := range tierOrder {
		if o == t {
			return i + 1
		}
	}
	return 0
}

// platformMaxTier is the highest tier of the routing document that has at least one candidate.
func (m *merger) platformMaxTier() string {
	best := ""
	for _, t := range tierOrder {
		if len(listAt(m.platform.ModelRouting, "spec", "tiers", t, "candidates")) > 0 {
			best = t
		}
	}
	return best
}

// tiers merges modelRouting.maxTier and roleMaxTier (a ceiling: the lower tier wins, a higher one is loosening).
func (m *merger) tiers() {
	ceiling := m.platformMaxTier()
	one := func(ptr, t string) {
		if t == "" {
			return
		}
		if ceiling != "" && tierRank(t) > tierRank(ceiling) {
			m.add(CodeLoosening, ptr, "tier %q is above the platform maximum tier %q", t, ceiling)
			return
		}
		m.values[ptr] = t
	}
	one("/spec/modelRouting/maxTier", strAt(m.repo, "spec", "modelRouting", "maxTier"))
	for _, role := range sortedKeys(mapAt(m.repo, "spec", "modelRouting", "roleMaxTier")) {
		one("/spec/modelRouting/roleMaxTier/"+role, strAt(m.repo, "spec", "modelRouting", "roleMaxTier", role))
	}
	for _, k := range []string{"deniedProviders", "deniedModels"} {
		if has(m.repo, "spec", "modelRouting", k) {
			m.values["/spec/modelRouting/"+k] = anyStrings(strList(m.repo, "spec", "modelRouting", k))
		}
	}
}

// definitions passes the repository-only definition globs through; a glob that lies entirely inside the
// protected set (platform plus repository protectedPaths) is POLICY_LOOSENING (plan 22 sections 9.4 and 9.5).
func (m *merger) definitions() {
	protected := strList(m.platform.Verification, "spec", "protectedPaths")
	m.issues = append(m.issues, repoSideInvariants(protected, m.repo, nil)...)
	for _, f := range []string{"testPaths", "fixturePaths", "testInfraPaths", "stubPaths"} {
		if has(m.repo, "spec", f) {
			m.values["/spec/"+f] = anyStrings(strList(m.repo, "spec", f))
		}
	}
}
