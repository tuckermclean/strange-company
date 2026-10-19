package policy

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Error codes of the cross-file invariants (plan 26 section 9.2 step 3, plan 31 section 9.3) and of the
// verification-policy invariants (plan 22 section 9.5).
const (
	CodeRoleHarnessUnknown         = "ROLE_HARNESS_UNKNOWN"
	CodeRoleHarnessRoleUnsupported = "ROLE_HARNESS_ROLE_UNSUPPORTED"
	CodeRoleProfileMissing         = "ROLE_PROFILE_MISSING"
	CodeRoleTierDefaultNotAllowed  = "ROLE_TIER_DEFAULT_NOT_ALLOWED"
	CodeRoleTierMismatch           = "ROLE_TIER_MISMATCH"
	CodeTierStaticallyEmpty        = "TIER_STATICALLY_EMPTY"
	CodeHarnessRepoConfig          = "HARNESS_REPO_CONFIG_NOT_IGNORED"
	CodeHarnessWorkerTimeout       = "HARNESS_WORKER_TIMEOUT_NOT_TEST_ONLY"
	CodeProfileMemoryOvercommit    = "PROFILE_MEMORY_OVERCOMMIT"
	CodeProfileCPU                 = "PROFILE_CPU_REQUEST_EXCEEDS_LIMIT"
	CodeProfileDeadline            = "PROFILE_DEADLINE_TOO_SHORT"
	CodeProfileReserve             = "PROFILE_HARNESS_RESERVE_TOO_LARGE"
	CodeProfileSetMismatch         = "PROFILE_SET_MISMATCH"
	CodeRoleMissing                = "ROLE_MISSING"
	CodeExemptionProducerRole      = "EXEMPTION_PRODUCER_ROLE_UNDEFINED"

	CodeExemptionDuplicate = "EXEMPTION_CLASS_DUPLICATE"
	CodeExemptionJudge     = "EXEMPTION_AUTO_JUDGE_NOT_DETERMINISTIC"
	CodeExemptionOverlap   = "EXEMPTION_AUTO_OVERLAPS_HUMAN"
	CodeExemptionPathDead  = "EXEMPTION_PATH_DEAD"
	CodeRegexInvalid       = "POLICY_REGEX_INVALID"
	CodeLoosening          = "POLICY_LOOSENING"
	CodeNoRedGreenSuite    = "VALIDATION_NO_RED_GREEN_SUITE"
	CodeGlobTooComplex     = "POLICY_GLOB_TOO_COMPLEX"
)

// Repository-side documents (plan 22 section 9.5, repository side).
const (
	repoSideDir              = ".strange-company"
	repoPolicyFile           = repoSideDir + "/policy.yaml"
	repoValidationFile       = repoSideDir + "/validation.yaml"
	repoPolicySchemaStem     = "repo-policy"
	repoValidationSchemaStem = "repo-validation"
)

// requiredRoles are the five roles of SPEC section 65.
var requiredRoles = []string{"researcher", "planner", "tester", "implementer", "reviewer"}

// deterministicJudges are the judge kinds an auto class may use (plan 22 section 9.5).
var deterministicJudges = map[string]bool{
	"required-facts": true, "validation-scenario": true, "invariants": true, "benchmark": true, "schema-check": true,
}

type doc struct {
	file string
	name string
	m    map[string]any
}

type invCtx struct {
	issues []Issue
}

func (c *invCtx) add(code, ptr, file, format string, args ...any) {
	c.issues = append(c.issues, Issue{Code: code, Pointer: ptr, File: file, Message: fmt.Sprintf(format, args...)})
}

func mapAt(v any, keys ...string) map[string]any {
	for _, k := range keys {
		m, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = m[k]
	}
	m, _ := v.(map[string]any)
	return m
}

func valAt(v any, keys ...string) any {
	for _, k := range keys {
		m, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = m[k]
	}
	return v
}

func strAt(v any, keys ...string) string {
	s, _ := valAt(v, keys...).(string)
	return s
}

func listAt(v any, keys ...string) []any {
	l, _ := valAt(v, keys...).([]any)
	return l
}

func strList(v any, keys ...string) []string {
	var out []string
	for _, x := range listAt(v, keys...) {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sameStringSet(a, b []string) bool {
	x, y := map[string]bool{}, map[string]bool{}
	for _, s := range a {
		x[s] = true
	}
	for _, s := range b {
		y[s] = true
	}
	if len(x) != len(y) {
		return false
	}
	for s := range x {
		if !y[s] {
			return false
		}
	}
	return true
}

// crossFileInvariants checks the invariants among the validated documents of the kinds that are present
// (plan 26 section 9.2 step 3). names holds every (kind, name) seen, including documents rejected by their
// schema, so that a malformed role is not also reported as missing.
func crossFileInvariants(resources map[string]Resource, names map[string]map[string]bool) []Issue {
	c := &invCtx{}
	byKind := map[string][]doc{}
	paths := make([]string, 0, len(resources))
	for p := range resources {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		r := resources[p]
		byKind[r.Kind] = append(byKind[r.Kind], doc{file: p, name: r.Name, m: r.Doc})
	}

	// Harness registry (merged over all registries present).
	type harness struct {
		file, name string
		m          map[string]any
	}
	var harnesses []harness
	for _, d := range byKind["HarnessRegistry"] {
		hm := mapAt(d.m, "spec", "harnesses")
		for _, n := range sortedKeys(hm) {
			if m, ok := hm[n].(map[string]any); ok {
				harnesses = append(harnesses, harness{d.file, n, m})
			}
		}
	}
	haveRegistry := len(byKind["HarnessRegistry"]) > 0
	harnessByName := map[string]harness{}
	for _, h := range harnesses {
		harnessByName[h.name] = h
	}

	// Harness invariants.
	for _, h := range harnesses {
		stage := strAt(h.m, "stage")
		if stage != "test-only" {
			if v, ok := valAt(h.m, "capabilities", "ignoresRepoConfig").(bool); ok && !v {
				c.add(CodeHarnessRepoConfig, "/spec/harnesses/"+esc(h.name)+"/capabilities/ignoresRepoConfig", h.file,
					"production harness %q must set capabilities.ignoresRepoConfig: true", h.name)
			}
		}
		if v, ok := valAt(h.m, "fake", "disableWorkerTimeout").(bool); ok && v && stage != "test-only" {
			c.add(CodeHarnessWorkerTimeout, "/spec/harnesses/"+esc(h.name)+"/fake/disableWorkerTimeout", h.file,
				"harness %q sets fake.disableWorkerTimeout but its stage is %q, not test-only", h.name, stage)
		}
	}

	// Resource profiles.
	profileSets := map[string]map[string]any{}
	for _, d := range byKind["ResourceProfiles"] {
		sets := mapAt(d.m, "spec", "sets")
		for _, sn := range sortedKeys(sets) {
			set, _ := sets[sn].(map[string]any)
			profileSets[sn] = set
			for _, pn := range sortedKeys(set) {
				p, _ := set[pn].(map[string]any)
				profileInvariants(c, d.file, sn, pn, p)
			}
		}
		union := map[string]bool{}
		for _, set := range profileSets {
			for pn := range set {
				union[pn] = true
			}
		}
		for _, sn := range sortedKeys(sets) {
			var missing []string
			for pn := range union {
				if _, ok := profileSets[sn][pn]; !ok {
					missing = append(missing, pn)
				}
			}
			sort.Strings(missing)
			if len(missing) > 0 {
				c.add(CodeProfileSetMismatch, "/spec/sets/"+esc(sn), d.file,
					"profile set %q lacks the profile(s) %s that another set defines; every set defines the same profile names",
					sn, quoteList(missing))
			}
		}
	}

	// Routing.
	var routing *doc
	if rs := byKind["ModelRouting"]; len(rs) > 0 {
		routing = &rs[0]
	}

	// Roles.
	roles := byKind["RoleDefinition"]
	for _, r := range roles {
		name := r.name
		def := strAt(r.m, "spec", "defaultModelTier")
		allowed := strList(r.m, "spec", "allowedModelTiers")
		if def != "" && !contains(allowed, def) {
			c.add(CodeRoleTierDefaultNotAllowed, "/spec/defaultModelTier", r.file,
				"role %q: defaultModelTier %q is not in allowedModelTiers %s", name, def, quoteList(allowed))
		}
		if haveRegistry {
			for i, hn := range strList(r.m, "spec", "allowedHarnesses") {
				ptr := "/spec/allowedHarnesses/" + strconv.Itoa(i)
				h, ok := harnessByName[hn]
				switch {
				case !ok:
					c.add(CodeRoleHarnessUnknown, ptr, r.file, "role %q allows harness %q, which is not in the harness registry", name, hn)
				case !contains(strList(h.m, "supportedRoles"), name):
					c.add(CodeRoleHarnessRoleUnsupported, ptr, r.file,
						"role %q allows harness %q, which does not list %q in supportedRoles", name, hn, name)
				}
			}
		}
		if len(byKind["ResourceProfiles"]) > 0 {
			refs := map[string]string{}
			var order []string
			if dp := strAt(r.m, "spec", "defaultProfile"); dp != "" {
				refs["/spec/defaultProfile"] = dp
				order = append(order, "/spec/defaultProfile")
			}
			for i, p := range strList(r.m, "spec", "allowedProfiles") {
				ptr := "/spec/allowedProfiles/" + strconv.Itoa(i)
				refs[ptr] = p
				order = append(order, ptr)
			}
			for _, ptr := range order {
				var missing []string
				for _, sn := range sortedSetNames(profileSets) {
					if _, ok := profileSets[sn][refs[ptr]]; !ok {
						missing = append(missing, sn)
					}
				}
				if len(missing) > 0 {
					c.add(CodeRoleProfileMissing, ptr, r.file, "role %q references profile %q, which is missing from the profile set(s) %s",
						name, refs[ptr], strings.Join(missing, ", "))
				}
			}
		}
		if routing != nil {
			rr := mapAt(routing.m, "spec", "roles", name)
			if rr != nil {
				if rd := strAt(rr, "defaultTier"); rd != def {
					c.add(CodeRoleTierMismatch, "/spec/defaultModelTier", r.file,
						"role %q: defaultModelTier %q differs from ModelRouting roles.%s.defaultTier %q", name, def, name, rd)
				}
				if ra := strList(rr, "allowedTiers"); !sameStringSet(ra, allowed) {
					c.add(CodeRoleTierMismatch, "/spec/allowedModelTiers", r.file,
						"role %q: allowedModelTiers %s differ from ModelRouting roles.%s.allowedTiers %s",
						name, quoteList(allowed), name, quoteList(ra))
				}
			}
		}
	}

	// Exactly the five roles of SPEC section 65, once any RoleDefinition is present.
	if len(names["RoleDefinition"]) > 0 {
		for _, rn := range requiredRoles {
			if !names["RoleDefinition"][rn] {
				c.add(CodeRoleMissing, "", "roles/"+rn+".yaml", "role %q is not defined; exactly the five roles of SPEC section 65 must exist", rn)
			}
		}
		for _, v := range byKind["VerificationPolicy"] {
			for i, cl := range listAt(v.m, "spec", "exemptionClasses") {
				for j, pr := range strList(cl, "producerRoles") {
					if !names["RoleDefinition"][pr] {
						c.add(CodeExemptionProducerRole, fmt.Sprintf("/spec/exemptionClasses/%d/producerRoles/%d", i, j), v.file,
							"exemption class %q: producerRoles entry %q names no defined role", strAt(cl, "name"), pr)
					}
				}
			}
		}
	}

	// Static resolvability (plan 31 section 9.3 item 3).
	if routing != nil && haveRegistry {
		for _, h := range harnesses {
			if strAt(h.m, "stage") != "production" || valAt(h.m, "enabled") == false {
				continue
			}
			for i, role := range strList(h.m, "supportedRoles") {
				tier := strAt(routing.m, "spec", "roles", role, "defaultTier")
				if tier == "" {
					continue
				}
				if !staticCandidate(routing.m, h.name, h.m, tier) {
					c.add(CodeTierStaticallyEmpty, fmt.Sprintf("/spec/harnesses/%s/supportedRoles/%d", esc(h.name), i), h.file,
						"%s/%s/%s: tier %q has no candidate that passes the static filters (plan 31 section 14.2, filters 1-5)",
						h.name, role, tier, tier)
				}
			}
		}
	}
	return c.issues
}

func sortedSetNames(m map[string]map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func quoteList(l []string) string {
	q := make([]string, len(l))
	for i, s := range l {
		q[i] = strconv.Quote(s)
	}
	return "[" + strings.Join(q, ", ") + "]"
}

func esc(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "~", "~0"), "/", "~1")
}

// staticCandidate reports whether tier has a candidate for the harness that passes filters 1-5 of plan 31 section
// 14.2 (the repository override of filter 5 is not part of the platform policy).
func staticCandidate(routing map[string]any, hname string, h map[string]any, tier string) bool {
	stage := strAt(h, "stage")
	families := strList(h, "apiFamilies")
	constraint := strList(routing, "spec", "harnesses", hname, "providers")
	for _, id := range strList(routing, "spec", "tiers", tier, "candidates") {
		model := mapAt(routing, "spec", "models", id)
		if model == nil || valAt(model, "enabled") == false {
			continue
		}
		pname := strAt(model, "provider")
		prov := mapAt(routing, "spec", "providers", pname)
		if prov == nil || valAt(prov, "enabled") == false {
			continue
		}
		if strAt(prov, "stage") != stage {
			continue
		}
		if !contains(families, strAt(model, "apiFamily")) {
			continue
		}
		if len(constraint) > 0 && !contains(constraint, pname) {
			continue
		}
		return true
	}
	return false
}

var (
	durationRE = regexp.MustCompile(`^([1-9][0-9]*)(s|m|h)$`)
	memoryRE   = regexp.MustCompile(`^([1-9][0-9]*)(Mi|Gi)$`)
)

func durationSeconds(s string) (int64, bool) {
	m := durationRE.FindStringSubmatch(s)
	if m == nil {
		return 0, false
	}
	n, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil {
		return 0, false
	}
	switch m[2] {
	case "m":
		n *= 60
	case "h":
		n *= 3600
	}
	return n, true
}

func memoryBytes(s string) (int64, bool) {
	m := memoryRE.FindStringSubmatch(s)
	if m == nil {
		return 0, false
	}
	n, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil {
		return 0, false
	}
	if m[2] == "Gi" {
		return n << 30, true
	}
	return n << 20, true
}

// cpuMilli parses "250m", "1" or "0.5" as millicores.
func cpuMilli(s string) (int64, bool) {
	if strings.HasSuffix(s, "m") {
		n, err := strconv.ParseInt(strings.TrimSuffix(s, "m"), 10, 64)
		return n, err == nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	return int64(f*1000 + 0.5), true
}

func profileInvariants(c *invCtx, file, set, name string, p map[string]any) {
	base := "/spec/sets/" + esc(set) + "/" + esc(name)
	who := fmt.Sprintf("profile %q in set %q", name, set)
	rq, ok1 := memoryBytes(strAt(p, "memoryRequest"))
	lm, ok2 := memoryBytes(strAt(p, "memoryLimit"))
	if ok1 && ok2 && rq != lm {
		c.add(CodeProfileMemoryOvercommit, base+"/memoryRequest", file,
			"%s: memoryRequest %s differs from memoryLimit %s (no memory overcommit)", who, strAt(p, "memoryRequest"), strAt(p, "memoryLimit"))
	}
	cr, ok1 := cpuMilli(strAt(p, "cpuRequest"))
	cl, ok2 := cpuMilli(strAt(p, "cpuLimit"))
	if ok1 && ok2 && cr > cl {
		c.add(CodeProfileCPU, base+"/cpuRequest", file, "%s: cpuRequest %s exceeds cpuLimit %s", who, strAt(p, "cpuRequest"), strAt(p, "cpuLimit"))
	}
	dl, ok1 := durationSeconds(strAt(p, "deadline"))
	rs, ok2 := durationSeconds(strAt(p, "harnessReserve"))
	if ok1 && dl < 300 {
		c.add(CodeProfileDeadline, base+"/deadline", file, "%s: deadline %s is shorter than 5m", who, strAt(p, "deadline"))
	}
	if ok1 && ok2 && rs >= dl-120 {
		c.add(CodeProfileReserve, base+"/harnessReserve", file,
			"%s: harnessReserve %s must be less than deadline %s minus 2m", who, strAt(p, "harnessReserve"), strAt(p, "deadline"))
	}
}

// verificationInvariants checks one VerificationPolicy beyond its schema (plan 22 section 9.5).
func verificationInvariants(file string, spec map[string]any) []Issue {
	c := &invCtx{}
	classes := listAt(spec, "exemptionClasses")
	protected := strList(spec, "protectedPaths")

	seen := map[string]int{}
	for i, cl := range classes {
		name := strAt(cl, "name")
		ptr := fmt.Sprintf("/spec/exemptionClasses/%d", i)
		if first, dup := seen[name]; dup {
			c.add(CodeExemptionDuplicate, ptr+"/name", file, "exemption class name %q is already used by class %d", name, first)
		} else {
			seen[name] = i
		}
		if strAt(cl, "approval") == "auto" && valAt(cl, "producesMergeableChange") != false {
			if k := strAt(cl, "judge", "kind"); !deterministicJudges[k] {
				c.add(CodeExemptionJudge, ptr+"/judge/kind", file,
					"exemption class %q is approval: auto but its judge kind %q is not deterministic", name, k)
			}
		}
	}

	// Dead entries: an allowedPaths glob entirely inside the protected set.
	for i, cl := range classes {
		for k, g := range strList(cl, "allowedPaths") {
			inside, ok := globInsideUnion(g, protected)
			ptr := fmt.Sprintf("/spec/exemptionClasses/%d/allowedPaths/%d", i, k)
			switch {
			case !ok:
				c.add(CodeGlobTooComplex, ptr, file, "cannot decide whether %q lies inside the protected set (too many states)", g)
			case inside:
				c.add(CodeExemptionPathDead, ptr, file,
					"exemption class %q: allowedPaths entry %q lies entirely inside the protected set (dead entry)", strAt(cl, "name"), g)
			}
		}
	}

	// Auto classes against non-catch-all human classes: exact intersection of the effective paths.
	for i, a := range classes {
		if strAt(a, "approval") != "auto" {
			continue
		}
		denied := strList(a, "deniedPaths")
		for j, h := range classes {
			if i == j || strAt(h, "approval") != "human" || isCatchAll(strList(h, "allowedPaths")) {
				continue
			}
			var hit string
			complex := ""
		pairs:
			for _, ag := range strList(a, "allowedPaths") {
				for _, hg := range strList(h, "allowedPaths") {
					found, ok := globsIntersect(ag, hg, denied)
					if !ok {
						complex = fmt.Sprintf("%q and %q", ag, hg)
						continue
					}
					if found {
						hit = fmt.Sprintf("allowedPaths %q (minus deniedPaths) and %q", ag, hg)
						break pairs
					}
				}
			}
			ptr := fmt.Sprintf("/spec/exemptionClasses/%d/allowedPaths", i)
			switch {
			case hit != "":
				c.add(CodeExemptionOverlap, ptr, file, "auto class %q overlaps human class %q: %s match a common path",
					strAt(a, "name"), strAt(h, "name"), hit)
			case complex != "":
				c.add(CodeGlobTooComplex, ptr, file, "cannot decide whether auto class %q overlaps human class %q (%s; too many states)",
					strAt(a, "name"), strAt(h, "name"), complex)
			}
		}
	}

	// Every RE2 pattern compiles.
	checkRE := func(ptr, pat string) {
		if _, err := regexp.Compile(pat); err != nil {
			c.add(CodeRegexInvalid, ptr, file, "pattern %s is not valid RE2: %s", strconv.Quote(pat), oneLine(err.Error()))
		}
	}
	for _, group := range []string{"skipMarkers", "testEnvDetection", "unconditionalFailure"} {
		langs := mapAt(spec, "staticChecks", group)
		for _, lang := range sortedKeys(langs) {
			for i, p := range strList(langs, lang) {
				checkRE(fmt.Sprintf("/spec/staticChecks/%s/%s/%d", group, esc(lang), i), p)
			}
		}
	}
	adapters := mapAt(spec, "adapters")
	for _, an := range sortedKeys(adapters) {
		for _, f := range []string{"errorTypes", "assertionTypes", "buildFailurePatterns", "timeoutPatterns"} {
			for i, p := range strList(adapters[an], f) {
				checkRE(fmt.Sprintf("/spec/adapters/%s/%s/%d", esc(an), f, i), p)
			}
		}
	}
	return c.issues
}

// repoSideInvariants checks the repository documents of a directory against the platform protected set.
// protected is the VerificationPolicy protectedPaths of the directory; policy and validation are the validated
// RepositoryPolicy and RepositoryValidation documents (nil when absent or rejected by their schema).
//
// A definition glob "meets" the protected set when it lies entirely inside it (exact inclusion, plan 22 section
// 9.5; CD-68): the shipped definitions (`**/*_test.go`, `**/testdata/**`) intersect the protected set without
// being inside it, and ingestion subtracts the protected paths from them.
func repoSideInvariants(protected []string, policy, validation map[string]any) []Issue {
	c := &invCtx{}
	if policy != nil {
		set := append(append([]string{}, protected...), strList(policy, "spec", "protectedPaths")...)
		for _, field := range []string{"testPaths", "fixturePaths", "testInfraPaths", "stubPaths"} {
			for i, g := range strList(policy, "spec", field) {
				ptr := fmt.Sprintf("/spec/%s/%d", field, i)
				inside, ok := globInsideUnion(g, set)
				switch {
				case !ok:
					c.add(CodeGlobTooComplex, ptr, repoPolicyFile, "cannot decide whether %q lies inside the protected set (too many states)", g)
				case inside:
					c.add(CodeLoosening, ptr, repoPolicyFile, "%s entry %q lies entirely inside the protected set; a repository definition may not loosen it", field, g)
				}
			}
		}
	}
	if validation != nil {
		ok := false
		for _, s := range listAt(validation, "spec", "verification", "suites") {
			ph := strList(s, "phases")
			ok = ok || (contains(ph, "red") && contains(ph, "green"))
		}
		if !ok {
			c.add(CodeNoRedGreenSuite, "/spec/verification/suites", repoValidationFile,
				"no suite has both phases red and green; test-first tasks need one")
		}
	}
	return c.issues
}
