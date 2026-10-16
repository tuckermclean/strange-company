// Package tdd implements the bootstrap TDD discipline checker behind `sc-lint tdd-discipline`
// (plan 52 section 8.3).
package tdd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Policy is `spec.tddDiscipline` of policy/ci.yaml (plan 52 section 8.3).
type Policy struct {
	ExcludedBranches   []string                  `yaml:"excludedBranches"`
	BehaviorPaths      []string                  `yaml:"behaviorPaths"`
	TestPaths          []string                  `yaml:"testPaths"`
	RedTargets         []string                  `yaml:"redTargets"`
	RedTargetFallbacks map[string]string         `yaml:"redTargetFallbacks"`
	ExemptionClasses   map[string]ExemptionClass `yaml:"exemptionClasses"`
}

// ExemptionClass is one entry of Policy.ExemptionClasses.
type ExemptionClass struct {
	Paths        []string `yaml:"paths"`
	SubjectRegex string   `yaml:"subjectRegex"`
	Requires     string   `yaml:"requires"`
}

// Options select what Check judges. Root is a git work tree. Base is the target ref (for example
// origin/main); B is the merge base of Base and Head. Head defaults to HEAD. Branch is the PR source
// branch, Author the PR author login. A nil Policy is read from Root/policy/ci.yaml (spec.tddDiscipline).
// Env entries (KEY=VALUE) are added to the environment of every red-target run.
type Options struct {
	Root   string
	Base   string
	Head   string
	Branch string
	Author string
	Policy *Policy
	Env    []string
}

// Finding is one failure or warning: a code such as TDD_NO_RED_COMMIT, the commit it concerns (may be
// empty) and a human-readable message.
type Finding struct {
	Code    string `json:"code"`
	Commit  string `json:"commit,omitempty"`
	Message string `json:"message"`
}

// RedRun is the evaluation of one RED commit: its target, the packages it ran, F_R, whether the build
// succeeded and whether a redTargetFallbacks command replaced a missing Make target.
type RedRun struct {
	Commit       string   `json:"commit"`
	Target       string   `json:"target"`
	Packages     []string `json:"packages"`
	Failed       []string `json:"failed"`
	BuildOK      bool     `json:"build_ok"`
	FallbackUsed bool     `json:"fallback_used"`
}

// Red summarises all evaluated RED commits: Failed is the union of their failed sets, BuildOK is true
// only if every RED run built.
type Red struct {
	Commits []RedRun `json:"commits"`
	Failed  []string `json:"failed"`
	BuildOK bool     `json:"build_ok"`
}

// Report is the checker result (out/tdd-discipline/report.json). Result is one of "not-applicable",
// "exempt", "pass" or "fail". Failures hold the failure codes that make Result "fail". Warnings hold
// TDD_RED_TEST_MODIFIED and refused exclusions (TDD_EXCLUSION_INVALID). Exemption names the class
// of a valid exemption.
type Report struct {
	Result    string    `json:"result"`
	Branch    string    `json:"branch"`
	Exemption string    `json:"exemption,omitempty"`
	Failures  []Finding `json:"failures"`
	Warnings  []Finding `json:"warnings"`
	Red       Red       `json:"red"`
}

// Result values of Report.Result.
const (
	ResultNotApplicable = "not-applicable"
	ResultExempt        = "exempt"
	ResultPass          = "pass"
	ResultFail          = "fail"
)

// Failure and warning codes (plan 52 section 8.3).
const (
	CodeMergeCommit         = "TDD_MERGE_COMMIT"
	CodeExclusionInvalid    = "TDD_EXCLUSION_INVALID"
	CodeExemptionInvalid    = "TDD_EXEMPTION_INVALID"
	CodeNoRedCommit         = "TDD_NO_RED_COMMIT"
	CodeImplementationFirst = "TDD_IMPLEMENTATION_BEFORE_RED"
	CodeRedTargetInvalid    = "TDD_RED_TARGET_INVALID"
	CodeBaselineFailed      = "TDD_BASELINE_FAILED"
	CodeRedPassed           = "TDD_RED_PASSED"
	CodeRedBuildFailure     = "TDD_RED_BUILD_FAILURE"
	CodeRedPreexisting      = "TDD_RED_PREEXISTING_FAILURE"
	CodeRedNotGreen         = "TDD_RED_TESTS_NOT_GREEN"
	CodeRedTestModified     = "TDD_RED_TEST_MODIFIED"
	CodeStubInvalid         = "TDD_STUB_INVALID"
	CodeStubNotReplaced     = "TDD_STUB_NOT_REPLACED"
)

var (
	redSubjectRe  = regexp.MustCompile(`^test(\([^)]+\))?: `)
	exemptLineRe  = regexp.MustCompile(`(?m)^TDD-Exempt:[ \t]*(.*?)[ \t\r]*$`)
	exemptValueRe = regexp.MustCompile(`^([a-z][a-z0-9-]*):[ \t]*(.*)$`)
	redLineRe     = regexp.MustCompile(`(?m)^TDD-Red-Target:[ \t]*(.*?)[ \t\r]*$`)
	keyValueRe    = regexp.MustCompile(`^([A-Z][A-Z0-9_]*)=(.*)$`)
)

const (
	renovateAuthor     = "sc-renovate"
	dependencyClass    = "dependency-update"
	minExemptionReason = 20
)

// checker holds the state of one Check call.
type checker struct {
	opts    Options
	root    string
	pol     Policy
	rep     *Report
	tmp     string
	nwt     int
	behav   []*regexp.Regexp
	tests   []*regexp.Regexp
	baseSHA string // B, the merge base
	headSHA string
	// baselines caches the baseline result by run key.
	baselines map[string]runResult
	pending   []headCheck
}

// headCheck is a RED commit whose failed tests T7 verifies at HEAD.
type headCheck struct {
	commit string
	spec   runSpec
	failed []string
}

// commitInfo is one commit of B..HEAD.
type commitInfo struct {
	sha     string
	subject string
	message string
	files   []change
}

func (c *checker) fail(code, commit, format string, a ...any) {
	c.rep.Failures = append(c.rep.Failures, Finding{Code: code, Commit: commit, Message: fmt.Sprintf(format, a...)})
}

func (c *checker) warn(code, commit, format string, a ...any) {
	c.rep.Warnings = append(c.rep.Warnings, Finding{Code: code, Commit: commit, Message: fmt.Sprintf(format, a...)})
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// Check judges the commits Base..Head of the repository at opts.Root against the bootstrap TDD
// discipline. Rule violations are Report.Failures; the error is for problems running the check itself.
func Check(opts Options) (Report, error) {
	root := opts.Root
	if root == "" {
		root = "."
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return Report{}, err
	}
	if opts.Base == "" {
		return Report{}, errors.New("tdd: no base ref")
	}
	var pol Policy
	if opts.Policy != nil {
		pol = *opts.Policy
	} else if pol, err = LoadPolicy(filepath.Join(root, "policy", "ci.yaml")); err != nil {
		return Report{}, err
	}
	c := &checker{opts: opts, root: root, pol: pol, baselines: map[string]runResult{}}
	c.rep = &Report{
		Branch:   opts.Branch,
		Failures: []Finding{},
		Warnings: []Finding{},
		Red:      Red{Commits: []RedRun{}, Failed: []string{}, BuildOK: true},
	}
	if c.behav, err = compileGlobs(pol.BehaviorPaths, false); err != nil {
		return Report{}, err
	}
	if c.tests, err = compileGlobs(pol.TestPaths, false); err != nil {
		return Report{}, err
	}
	head := opts.Head
	if head == "" {
		head = "HEAD"
	}
	if c.headSHA, err = c.revParse(head); err != nil {
		return Report{}, err
	}
	baseSHA, err := c.revParse(opts.Base)
	if err != nil {
		return Report{}, err
	}
	if c.baseSHA, err = c.git("merge-base", baseSHA, c.headSHA); err != nil {
		return Report{}, err
	}
	c.baseSHA = strings.TrimSpace(c.baseSHA)

	result, err := c.evaluate()
	if err != nil {
		return Report{}, err
	}
	if c.tmp != "" {
		_, _ = c.git("worktree", "prune")
		_ = os.RemoveAll(c.tmp)
	}
	if len(c.rep.Failures) > 0 {
		result = ResultFail
	}
	c.rep.Result = result
	return *c.rep, nil
}

// evaluate runs T0 to T8 and returns the result when no failure was recorded.
func (c *checker) evaluate() (string, error) {
	rng := c.baseSHA + ".." + c.headSHA
	merges, err := c.git("rev-list", "--merges", rng)
	if err != nil {
		return "", err
	}
	for _, m := range strings.Fields(merges) {
		c.fail(CodeMergeCommit, m, "merge commit %s in %s: the PR must be linear, rebase instead", short(m), short(c.baseSHA)+".."+short(c.headSHA))
	}
	changed, err := c.diffNames(c.baseSHA, c.headSHA)
	if err != nil {
		return "", err
	}
	changedPaths := make([]string, len(changed))
	for i, ch := range changed {
		changedPaths[i] = ch.path
	}

	// T0
	if na, err := c.excluded(changedPaths); err != nil {
		return "", err
	} else if na {
		return ResultNotApplicable, nil
	}
	// T1
	applicable := false
	for _, p := range changedPaths {
		if matchAny(c.behav, p) && !matchAny(c.tests, p) {
			applicable = true
			break
		}
	}
	if !applicable {
		return ResultNotApplicable, nil
	}
	// T2
	headMsg, err := c.git("log", "-1", "--format=%B", c.headSHA)
	if err != nil {
		return "", err
	}
	if exempt, claimed, err := c.exemption(headMsg, changedPaths); err != nil {
		return "", err
	} else if claimed {
		if exempt != "" {
			c.rep.Exemption = exempt
			return ResultExempt, nil
		}
		return ResultFail, nil
	}

	commits, err := c.loadCommits(rng)
	if err != nil {
		return "", err
	}
	if err := c.redGreen(commits, changed); err != nil {
		return "", err
	}
	return ResultPass, nil
}

// excluded implements T0.
func (c *checker) excluded(changedPaths []string) (bool, error) {
	branch := c.opts.Branch
	if branch == "" {
		return false, nil
	}
	for _, pat := range c.pol.ExcludedBranches {
		re, err := globRegexp(pat, true)
		if err != nil {
			return false, err
		}
		if !re.MatchString(branch) {
			continue
		}
		if !strings.HasPrefix(pat, "renovate/") {
			return true, nil
		}
		// A renovate/* branch is excluded only for sc-renovate and only for dependency-update files.
		class, ok := c.pol.ExemptionClasses[dependencyClass]
		var dep []*regexp.Regexp
		if ok {
			if dep, err = compileGlobs(class.Paths, false); err != nil {
				return false, err
			}
		}
		allDeps := ok && len(class.Paths) > 0
		for _, p := range changedPaths {
			if !matchAny(dep, p) {
				allDeps = false
				break
			}
		}
		if c.opts.Author == renovateAuthor && allDeps {
			return true, nil
		}
		c.warn(CodeExclusionInvalid, "", "branch %q matches %q but the exclusion is refused (needs author %s and only %s files; author %q): evaluated normally",
			branch, pat, renovateAuthor, dependencyClass, c.opts.Author)
	}
	return false, nil
}

// exemption implements T2. claimed reports whether the head commit has a TDD-Exempt line; class is the
// valid class name, or "" after a TDD_EXEMPTION_INVALID failure.
func (c *checker) exemption(msg string, changedPaths []string) (class string, claimed bool, err error) {
	m := exemptLineRe.FindAllStringSubmatch(msg, -1)
	if len(m) == 0 {
		return "", false, nil
	}
	head := short(c.headSHA)
	if len(m) > 1 {
		c.fail(CodeExemptionInvalid, c.headSHA, "%d TDD-Exempt lines in the head commit; exactly one is allowed", len(m))
		return "", true, nil
	}
	v := exemptValueRe.FindStringSubmatch(m[0][1])
	if v == nil {
		c.fail(CodeExemptionInvalid, c.headSHA, "head commit %s: TDD-Exempt must read '<class>: <reason>'", head)
		return "", true, nil
	}
	name, reason := v[1], strings.TrimSpace(v[2])
	cl, ok := c.pol.ExemptionClasses[name]
	if !ok {
		c.fail(CodeExemptionInvalid, c.headSHA, "head commit %s: exemption class %q is not in exemptionClasses", head, name)
		return "", true, nil
	}
	if len([]rune(reason)) < minExemptionReason {
		c.fail(CodeExemptionInvalid, c.headSHA, "head commit %s: the reason of the %s exemption has fewer than %d characters", head, name, minExemptionReason)
		return "", true, nil
	}
	if len(cl.Paths) > 0 {
		res, err := compileGlobs(cl.Paths, false)
		if err != nil {
			return "", true, err
		}
		for _, p := range changedPaths {
			if !matchAny(res, p) {
				c.fail(CodeExemptionInvalid, c.headSHA, "exemption %s does not cover changed file %s", name, p)
				return "", true, nil
			}
		}
	}
	if cl.SubjectRegex != "" {
		re, err := regexp.Compile(cl.SubjectRegex)
		if err != nil {
			return "", true, fmt.Errorf("exemption class %s: subjectRegex: %w", name, err)
		}
		subject, _, _ := strings.Cut(strings.TrimLeft(msg, "\n"), "\n")
		if !re.MatchString(subject) {
			c.fail(CodeExemptionInvalid, c.headSHA, "exemption %s: the head subject %q does not match %s", name, subject, cl.SubjectRegex)
			return "", true, nil
		}
	}
	return name, true, nil
}

func (c *checker) loadCommits(rng string) ([]commitInfo, error) {
	out, err := c.git("rev-list", "--reverse", "--no-merges", rng)
	if err != nil {
		return nil, err
	}
	var commits []commitInfo
	for _, sha := range strings.Fields(out) {
		msg, err := c.git("log", "-1", "--format=%B", sha)
		if err != nil {
			return nil, err
		}
		subject, _, _ := strings.Cut(strings.TrimLeft(msg, "\n"), "\n")
		files, err := c.commitChanges(sha)
		if err != nil {
			return nil, err
		}
		commits = append(commits, commitInfo{sha: sha, subject: strings.TrimSpace(subject), message: msg, files: files})
	}
	return commits, nil
}

// isRed reports whether the commit is a RED commit by T3: a test(...) subject and at least one changed file in testPaths.
func (c *checker) isRed(ci commitInfo) bool {
	if !redSubjectRe.MatchString(ci.subject) {
		return false
	}
	for _, f := range ci.files {
		if matchAny(c.tests, f.path) {
			return true
		}
	}
	return false
}

// redGreen runs T3 to T8.
func (c *checker) redGreen(commits []commitInfo, all []change) error {
	first := -1
	for i, ci := range commits {
		if c.isRed(ci) {
			first = i
			break
		}
	}
	if first < 0 {
		c.fail(CodeNoRedCommit, "", "no RED commit: a commit with subject 'test(...): ' that changes test files is required before the implementation")
	} else {
		for _, ci := range commits[:first] {
			for _, f := range ci.files {
				if matchAny(c.behav, f.path) && !matchAny(c.tests, f.path) {
					c.fail(CodeImplementationFirst, ci.sha, "commit %s changes %s before the first RED commit %s", short(ci.sha), f.path, short(commits[first].sha))
					break
				}
			}
		}
	}
	for i, ci := range commits {
		if !c.isRed(ci) {
			continue
		}
		if err := c.evaluateRed(ci); err != nil {
			return err
		}
		c.testModified(commits, i)
	}
	if err := c.checkHead(); err != nil {
		return err
	}
	return c.markersAtHead(all)
}

// testModified implements T8 for RED commit commits[i].
func (c *checker) testModified(commits []commitInfo, i int) {
	red := commits[i]
	for _, f := range red.files {
		if f.status == 'D' || !matchAny(c.tests, f.path) {
			continue
		}
		for _, later := range commits[i+1:] {
			for _, lf := range later.files {
				if lf.path == f.path {
					c.warn(CodeRedTestModified, later.sha, "%s, changed in RED commit %s, is modified again in %s", f.path, short(red.sha), short(later.sha))
				}
			}
		}
	}
}
