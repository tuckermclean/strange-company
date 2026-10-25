package tdd

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
)

// runSpec selects one red-target run: a Make target with optional package patterns and -run filter.
type runSpec struct {
	target string
	pkgs   []string
	run    string
}

func (s runSpec) key() string {
	return s.target + "\x00" + strings.Join(s.pkgs, " ") + "\x00" + s.run
}

// tcase is one testcase of a run: status is passed, failed, skipped or build (a build error).
type tcase struct {
	id     string
	status string
}

// runResult is the outcome of one run of a red target at one commit.
type runResult struct {
	exit     int
	cases    []tcase
	failed   []string // ids of failed testcases, sorted
	passed   map[string]bool
	build    bool // a build error, or a crash with no failed testcase
	missing  bool // the Make target does not exist and there is no fallback
	skipped  bool // nothing was run: no package of the spec exists at the commit
	fallback bool // the redTargetFallbacks command ran instead of the Make target
	output   string
}

// safeWordRe is the character set of red-target values (plan 52 section 8.3, CD-58c).
var safeWordRe = regexp.MustCompile(`^[A-Za-z0-9_./:^$|()*+?\[\]{}, -]*$`)

// parseRedTarget implements the T4 line; problem is non-empty when the commit's line is unusable.
func (c *checker) parseRedTarget(ci commitInfo) (spec runSpec, problem string) {
	lines := redLineRe.FindAllStringSubmatch(ci.message, -1)
	switch len(lines) {
	case 0:
		return spec, "no TDD-Red-Target line (it must start a line of the commit message)"
	case 1:
	default:
		return spec, fmt.Sprintf("%d TDD-Red-Target lines; exactly one is allowed", len(lines))
	}
	fields := strings.Fields(lines[0][1])
	if len(fields) == 0 {
		return spec, "empty TDD-Red-Target line"
	}
	for _, f := range fields { // CD-58c: nothing from a commit message reaches a shell unchecked
		v := f
		if m := keyValueRe.FindStringSubmatch(f); m != nil {
			v = m[2]
		}
		if !safeWordRe.MatchString(v) || strings.Contains(v, "$(") {
			return spec, fmt.Sprintf("word %q has characters outside the safe set", f)
		}
	}
	spec.target = fields[0]
	known := false
	for _, t := range c.pol.RedTargets {
		if t == spec.target {
			known = true
		}
	}
	if !known {
		return spec, fmt.Sprintf("target %q is not in redTargets", spec.target)
	}
	var key string
	var runWords []string
	havePkgs := false
	for _, f := range fields[1:] {
		if m := keyValueRe.FindStringSubmatch(f); m != nil {
			key = m[1]
			switch key {
			case "GO_TEST_PKGS":
				havePkgs = true
			case "GO_TEST_RUN":
			default:
				return spec, fmt.Sprintf("unsupported key %s", key)
			}
			f = m[2]
			if f == "" {
				continue
			}
		}
		switch key {
		case "GO_TEST_PKGS":
			spec.pkgs = append(spec.pkgs, f)
		case "GO_TEST_RUN":
			runWords = append(runWords, f)
		default:
			return spec, fmt.Sprintf("unexpected word %q", f)
		}
	}
	spec.run = strings.Join(runWords, " ")
	if havePkgs && len(spec.pkgs) == 0 {
		return spec, "GO_TEST_PKGS is empty"
	}
	if !havePkgs && spec.target == "test" {
		seen := map[string]bool{}
		for _, f := range ci.files {
			if f.status == 'D' || !strings.HasSuffix(f.path, "_test.go") {
				continue
			}
			d := "."
			if i := strings.LastIndex(f.path, "/"); i >= 0 {
				d = "./" + f.path[:i]
			}
			if !seen[d] {
				seen[d] = true
				spec.pkgs = append(spec.pkgs, d)
			}
		}
		sort.Strings(spec.pkgs)
	}
	return spec, ""
}

// runReds executes T5 (batched baselines at B) and T6 (one run per RED commit) for the runnable jobs, the
// independent runs concurrently. A RED run whose baseline turns out to fail is wasted, never judged.
func (c *checker) runReds(jobs []*redJob) error {
	var specs []runSpec
	for _, j := range jobs {
		if j.runnable {
			specs = append(specs, j.spec)
		}
	}
	b := newBatch(c, c.baseSHA, true, specs)
	tasks := b.first()
	for _, j := range jobs {
		if j.runnable {
			tasks = append(tasks, func() (err error) {
				j.rr, err = c.execute(j.ci.sha, j.spec, false)
				return err
			})
		}
	}
	if err := c.parallel(tasks); err != nil {
		return err
	}
	if err := c.parallel(b.second()); err != nil {
		return err
	}
	for _, j := range jobs {
		if j.runnable {
			j.base = b.result(j.spec)
		}
	}
	return nil
}

// redJob is one RED commit on its way through T3 to T6: prepared (T3, T4), run (T5, T6), then judged.
type redJob struct {
	ci       commitInfo
	spec     runSpec
	bad      []string // T3 violations
	problem  string   // T4 problem
	runnable bool
	base     runResult // T5 baseline at B
	rr       runResult // T6 run at the RED commit
}

// prepareRed runs the checks of T3 and T4 that need no target execution. It records no finding.
func (c *checker) prepareRed(ci commitInfo) *redJob {
	j := &redJob{ci: ci}
	for _, f := range ci.files {
		switch {
		case matchAny(c.tests, f.path):
		case matchAny(c.behav, f.path):
			j.bad = append(j.bad, c.stubViolations(ci.sha, f)...)
		default: // T3: a RED commit holds test files and declared stubs only
			j.bad = append(j.bad, fmt.Sprintf("%s: a RED commit may change test files and declared stubs only", f.path))
		}
	}
	if len(j.bad) > 0 {
		return j
	}
	j.spec, j.problem = c.parseRedTarget(ci)
	j.runnable = j.problem == ""
	return j
}

// evaluateRed records the findings of T3 to T6 for one prepared and executed RED commit and queues its T7 check.
func (c *checker) evaluateRed(j *redJob) {
	ci, spec := j.ci, j.spec
	for _, b := range j.bad {
		c.fail(CodeStubInvalid, ci.sha, "RED commit %s: %s", short(ci.sha), b)
	}
	if len(j.bad) > 0 {
		return
	}
	if j.problem != "" {
		c.fail(CodeRedTargetInvalid, ci.sha, "RED commit %s: %s", short(ci.sha), j.problem)
		return
	}

	// T5: baseline at B.
	base := j.base
	baseline := map[string]bool{}
	if !base.skipped && !base.missing && !(base.fallback && base.exit != 0 && len(base.failed) == 0) {
		if base.exit != 0 {
			c.fail(CodeBaselineFailed, ci.sha, "the red target %s fails at the merge base %s (main is broken): %s", spec.target, short(c.baseSHA), tail(base.output))
			return
		}
		for _, tc := range base.cases {
			baseline[tc.id] = true
		}
	}

	// T6: RED run.
	rr := j.rr
	run := RedRun{Commit: ci.sha, Target: spec.target, Packages: append([]string{}, spec.pkgs...), Failed: []string{}, BuildOK: true, FallbackUsed: rr.fallback}
	defer func() {
		c.rep.Red.Commits = append(c.rep.Red.Commits, run)
		if !run.BuildOK {
			c.rep.Red.BuildOK = false
		}
		have := map[string]bool{}
		for _, f := range c.rep.Red.Failed {
			have[f] = true
		}
		for _, f := range run.Failed {
			if !have[f] {
				c.rep.Red.Failed = append(c.rep.Red.Failed, f)
			}
		}
		sort.Strings(c.rep.Red.Failed)
	}()
	switch {
	case rr.missing:
		run.BuildOK = false
		c.fail(CodeRedBuildFailure, ci.sha, "RED commit %s: the red target %s does not exist and has no fallback", short(ci.sha), spec.target)
		return
	case rr.skipped:
		run.BuildOK = false
		c.fail(CodeRedBuildFailure, ci.sha, "RED commit %s: none of the packages %v exists", short(ci.sha), spec.pkgs)
		return
	case rr.exit == 0:
		c.fail(CodeRedPassed, ci.sha, "RED commit %s: the red target %s passes, so the behavior already exists", short(ci.sha), spec.target)
		return
	case rr.build:
		run.BuildOK = false
		c.fail(CodeRedBuildFailure, ci.sha, "RED commit %s: the red target %s did not build or crashed (exit %d): %s", short(ci.sha), spec.target, rr.exit, tail(rr.output))
		return
	}
	run.Failed = rr.failed
	changedBodies := c.changedTestBodies(ci.sha, ci.files)
	var pre []string
	for _, id := range rr.failed {
		if !baseline[id] {
			continue
		}
		if !bodyChanged(changedBodies, id) {
			pre = append(pre, id)
		}
	}
	if len(pre) > 0 {
		c.fail(CodeRedPreexisting, ci.sha, "RED commit %s: failing tests that exist at the merge base and whose body RED did not change: %s", short(ci.sha), strings.Join(pre, ", "))
		return
	}
	c.pending = append(c.pending, headCheck{commit: ci.sha, spec: spec, failed: rr.failed})
	return
}

// bodyChanged reports whether id names a test, or a subtest of a test, whose body RED changed.
func bodyChanged(changed map[string]bool, id string) bool {
	for name := range changed {
		if id == name || strings.HasPrefix(id, name+"/") {
			return true
		}
	}
	return false
}

// checkHead implements T7: at HEAD each queued target exits 0 and passes every test RED failed. A test id
// that is missing at HEAD is accepted when a valid TDD-Superseded line of the head commit names it (CD-57).
func (c *checker) checkHead() error {
	lines, err := c.supersededLines()
	if err != nil {
		return err
	}
	used := make([]bool, len(lines))
	var specs []runSpec
	for _, p := range c.pending {
		specs = append(specs, p.spec)
	}
	b := newBatch(c, c.headSHA, false, specs)
	if err := c.parallel(b.first()); err != nil {
		return err
	}
	if err := c.parallel(b.second()); err != nil {
		return err
	}
	for _, p := range c.pending {
		hr := b.result(p.spec)
		switch {
		case hr.missing:
			c.fail(CodeRedNotGreen, p.commit, "RED commit %s: the red target %s does not exist at HEAD", short(p.commit), p.spec.target)
			continue
		case hr.exit != 0:
			c.fail(CodeRedNotGreen, p.commit, "RED commit %s: the red target %s exits %d at HEAD; failing: %s; output: %s", short(p.commit), p.spec.target, hr.exit, firstIDs(hr.failed, 8), tail(hr.output))
			continue
		}
		var notGreen []string
		for _, id := range p.failed {
			if hr.passed[id] {
				continue
			}
			// CD-57: only an id ABSENT from HEAD's results can be superseded; a skipped one stays not green.
			if !hasCase(hr, id) && c.superseded(lines, used, id, p.commit) {
				continue
			}
			notGreen = append(notGreen, id)
		}
		if len(notGreen) > 0 {
			c.fail(CodeRedNotGreen, p.commit, "RED commit %s: at HEAD these tests are missing, skipped or not passed: %s", short(p.commit), strings.Join(notGreen, ", "))
		}
	}
	for i, l := range lines {
		if used[i] {
			c.rep.Superseded = append(c.rep.Superseded, l.id)
		} else {
			c.fail(CodeSupersededInvalid, c.headSHA, "TDD-Superseded %q: %s", l.raw, l.problem())
		}
	}
	return nil
}

// supLine is one parsed TDD-Superseded line of the head commit.
type supLine struct {
	raw    string
	id     string
	sha    string // resolved commit, "" if unresolved
	reason string
	ok     bool // well-formed
	short  bool // reason too short
	idx    int  // index of sha in the PR commits, -1 if not in B..HEAD
	why    string
}

func (l supLine) problem() string {
	switch {
	case !l.ok:
		return "must read '<test-id> by <sha>: <reason of at least 20 characters>'"
	case l.short:
		return "the reason has fewer than 20 characters"
	case l.why != "":
		return l.why
	}
	return "no RED test with that id is missing at HEAD, or the commit is not a later RED commit of this PR"
}

var (
	supLineRe = regexp.MustCompile(`(?m)^TDD-Superseded:[ \t]*(.*?)[ \t\r]*$`)
	supBodyRe = regexp.MustCompile(`^(.+) by ([0-9a-f]{7,40}): (.*)$`)
)

// supersededLines parses the TDD-Superseded lines of the head commit message only.
func (c *checker) supersededLines() ([]supLine, error) {
	msg, err := c.git("log", "-1", "--format=%B", c.headSHA)
	if err != nil {
		return nil, err
	}
	var out []supLine
	for _, m := range supLineRe.FindAllStringSubmatch(msg, -1) {
		l := supLine{raw: m[1], idx: -1}
		b := supBodyRe.FindStringSubmatch(m[1])
		if b == nil {
			out = append(out, l)
			continue
		}
		l.ok, l.id, l.reason = true, strings.TrimSpace(b[1]), strings.TrimSpace(b[3])
		if l.id == "" {
			l.ok = false
		}
		if len([]rune(l.reason)) < minExemptionReason {
			l.short = true
		}
		if full, err := c.revParse(b[2]); err == nil {
			l.sha = full
			for i, ci := range c.commits {
				if ci.sha == full {
					l.idx = i
				}
			}
		}
		if l.idx < 0 {
			l.why = fmt.Sprintf("commit %s is not a commit of this PR", b[2])
		} else if !c.isRed(c.commits[l.idx]) {
			l.why = fmt.Sprintf("commit %s is not a RED commit", b[2])
		}
		out = append(out, l)
	}
	return out, nil
}

// superseded reports whether a valid line accepts id of RED commit redSHA as superseded, marking it used.
func (c *checker) superseded(lines []supLine, used []bool, id, redSHA string) bool {
	from := -1
	for i, ci := range c.commits {
		if ci.sha == redSHA {
			from = i
		}
	}
	for i, l := range lines {
		if !l.ok || l.short || l.id != id || l.idx < 0 || !c.isRed(c.commits[l.idx]) {
			continue
		}
		if l.idx > from {
			used[i] = true
			return true
		}
		lines[i].why = fmt.Sprintf("commit %s is not a later RED commit than %s, which introduced the test", short(l.sha), short(redSHA))
	}
	return false
}

// firstIDs lists up to n ids.
func firstIDs(ids []string, n int) string {
	if len(ids) == 0 {
		return "(no failed testcase)"
	}
	more := ""
	if len(ids) > n {
		more = fmt.Sprintf(" and %d more", len(ids)-n)
		ids = ids[:n]
	}
	return strings.Join(ids, ", ") + more
}

func tail(s string) string {
	s = strings.TrimSpace(s)
	const max = 1500
	if len(s) > max {
		s = "..." + s[len(s)-max:]
	}
	return strings.ReplaceAll(s, "\n", " | ")
}

// nextRunID returns a unique number for the worktree and junit file of one run, creating the temp dir on first use.
func (c *checker) nextRunID() (int, error) {
	c.idMu.Lock()
	defer c.idMu.Unlock()
	if c.tmp == "" {
		d, err := os.MkdirTemp("", "tdd-discipline-")
		if err != nil {
			return 0, err
		}
		c.tmp = d
	}
	c.nwt++
	return c.nwt, nil
}

// worktree checks sha out into a fresh directory and returns it with its cleanup.
func (c *checker) worktree(sha string, n int) (string, func(), error) {
	dir := filepath.Join(c.tmp, fmt.Sprintf("wt%d", n))
	c.wtMu.Lock()
	defer c.wtMu.Unlock()
	if _, err := c.git("worktree", "add", "--detach", "--quiet", "--force", dir, sha); err != nil {
		return "", nil, err
	}
	cleanup := func() {
		c.wtMu.Lock()
		defer c.wtMu.Unlock()
		_, _ = c.git("worktree", "remove", "--force", dir)
		_ = os.RemoveAll(dir)
	}
	// Share the Go module and build caches of the checkout with the worktree (.cache is ignored by git).
	if shared := filepath.Join(c.root, ".cache", "go"); dirExists(shared) {
		if err := os.MkdirAll(filepath.Join(dir, ".cache"), 0o755); err == nil {
			_ = os.Symlink(shared, filepath.Join(dir, ".cache", "go"))
		}
	}
	return dir, cleanup, nil
}

func dirExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// execute runs the spec's target at commit sha in a clean worktree. With baseline set, package patterns
// that do not exist at sha are dropped (a package absent at B contributes nothing).
func (c *checker) execute(sha string, spec runSpec, baseline bool) (runResult, error) {
	n, err := c.nextRunID()
	if err != nil {
		return runResult{}, err
	}
	dir, cleanup, err := c.worktree(sha, n)
	if err != nil {
		return runResult{}, err
	}
	defer cleanup()
	if baseline && len(spec.pkgs) > 0 {
		var keep []string
		for _, p := range spec.pkgs {
			d := strings.TrimSuffix(strings.TrimPrefix(p, "./"), "...")
			d = strings.TrimSuffix(d, "/")
			if d == "" || d == "." || dirExists(filepath.Join(dir, filepath.FromSlash(d))) {
				keep = append(keep, p)
			}
		}
		if len(keep) == 0 {
			return runResult{skipped: true}, nil
		}
		spec.pkgs = keep
	}
	env := cleanEnv(c.opts.Env)
	res := runResult{}
	probe := exec.Command("make", "-n", spec.target)
	probe.Dir, probe.Env = dir, env
	var cmd *exec.Cmd
	junit := ""
	if err := probe.Run(); err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			return res, fmt.Errorf("running make: %w", err)
		}
		fb := c.pol.RedTargetFallbacks[spec.target]
		if fb == "" {
			res.missing = true
			return res, nil
		}
		res.fallback = true
		cmd = exec.Command("sh", "-c", fb)
	} else {
		junit = filepath.Join(c.tmp, fmt.Sprintf("junit%d.xml", n))
		// JUNIT travels in the environment, not as a command-line override: overrides reach every nested make
		// through MAKEFLAGS and would change what the tests of a target observe (a ?= assignment honors both).
		env = append(env, "JUNIT="+junit)
		args := []string{spec.target}
		if len(spec.pkgs) > 0 {
			args = append(args, "GO_TEST_PKGS="+strings.Join(spec.pkgs, " "))
		}
		if spec.run != "" {
			args = append(args, "GO_TEST_RUN="+spec.run)
		}
		cmd = exec.Command("make", args...)
	}
	cmd.Dir, cmd.Env = dir, env
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			return res, fmt.Errorf("running %s: %w", spec.target, err)
		}
		res.exit = ee.ExitCode()
		if res.exit == 0 {
			res.exit = 1
		}
	}
	res.output = out.String()
	if junit != "" {
		if b, err := os.ReadFile(junit); err == nil {
			res.cases = parseJUnit(b)
		}
	}
	if len(res.cases) == 0 {
		res.cases = adaptOutput(res.output)
	}
	res.passed = map[string]bool{}
	for _, tc := range res.cases {
		switch tc.status {
		case "passed":
			res.passed[tc.id] = true
		case "failed":
			res.failed = append(res.failed, tc.id)
		case "build":
			res.build = true
		}
	}
	sort.Strings(res.failed)
	if res.exit != 0 && len(res.failed) == 0 {
		res.build = true // a crash without a failed testcase
	}
	return res, nil
}

type junitCase struct {
	Classname string        `xml:"classname,attr"`
	Name      string        `xml:"name,attr"`
	Failure   *junitMessage `xml:"failure"`
	Error     *junitMessage `xml:"error"`
	Skipped   *junitMessage `xml:"skipped"`
}

type junitMessage struct {
	Type string `xml:"type,attr"`
}

type junitDoc struct {
	Suites []struct {
		Cases []junitCase `xml:"testcase"`
	} `xml:"testsuite"`
	Cases []junitCase `xml:"testcase"`
}

// parseJUnit turns JUnit XML into testcases; ids are classname.name.
func parseJUnit(b []byte) []tcase {
	var doc junitDoc
	if err := xml.Unmarshal(b, &doc); err != nil {
		return nil
	}
	all := doc.Cases
	for _, s := range doc.Suites {
		all = append(all, s.Cases...)
	}
	var out []tcase
	for _, jc := range all {
		tc := tcase{id: jc.Classname + "." + jc.Name, status: "passed"}
		switch {
		case jc.Error != nil && jc.Error.Type == "build":
			tc.status = "build"
		case jc.Error != nil || jc.Failure != nil:
			tc.status = "failed"
		case jc.Skipped != nil:
			tc.status = "skipped"
		}
		out = append(out, tc)
	}
	return out
}

var (
	adapterLineRe   = regexp.MustCompile(`^\s*(ok|FAIL)\s+(.+)$`)
	adapterSummary  = regexp.MustCompile(`^\s*(\S+):\s+\d+ checks?, \d+ failed\s*$`)
	adapterFallback = "wrapper"
)

// adaptOutput is the non-Go red-target adapter (plan 52 section 8.3, CD-56): every "ok <check>" or
// "FAIL <check>" line is a testcase whose classname is the script named by the next "<script>: N checks,
// M failed" summary line; repeated ids get " #2", " #3", ... in output order.
func adaptOutput(out string) []tcase {
	type pend struct {
		name   string
		status string
	}
	var cases []tcase
	var block []pend
	seen := map[string]int{}
	flush := func(class string) {
		for _, p := range block {
			id := class + "." + p.name
			seen[id]++
			if seen[id] > 1 {
				id = fmt.Sprintf("%s #%d", id, seen[id])
			}
			cases = append(cases, tcase{id: id, status: p.status})
		}
		block = nil
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if m := adapterSummary.FindStringSubmatch(line); m != nil {
			flush(m[1])
			continue
		}
		if m := adapterLineRe.FindStringSubmatch(line); m != nil {
			st := "passed"
			if m[1] == "FAIL" {
				st = "failed"
			}
			block = append(block, pend{name: strings.TrimSpace(m[2]), status: st})
		}
	}
	flush(adapterFallback)
	return cases
}

// hasCase reports whether the run has a testcase with that id, whatever its status.
func hasCase(r runResult, id string) bool {
	for _, tc := range r.cases {
		if tc.id == id {
			return true
		}
	}
	return false
}

// jobs is the number of runs that may be in flight: Options.Jobs, or min(NumCPU, 4) when it is not positive.
func (c *checker) jobs() int {
	if c.opts.Jobs > 0 {
		return c.opts.Jobs
	}
	return min(runtime.NumCPU(), 4)
}

// parallel runs the tasks in up to jobs() workers and returns the error of the lowest-numbered failed task.
// Every task writes only its own result slot, so no further locking is needed.
func (c *checker) parallel(tasks []func() error) error {
	errs := make([]error, len(tasks))
	next := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < min(c.jobs(), len(tasks)); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range next {
				errs[i] = tasks[i]()
			}
		}()
	}
	for i := range tasks {
		next <- i
	}
	close(next)
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

// batchGroup holds the distinct specs of one target and their results (plan 52 section 8.3, gh-159).
type batchGroup struct {
	specs    []runSpec
	union    runSpec
	unionRes runResult
	batched  bool // the union run was usable and serves every spec
	per      []runResult
}

// batch runs the specs of one phase (T5 at B, or T7 at HEAD) with one run per target when a target has
// several distinct specs. A union run that is not clean is discarded and each spec runs on its own, so the
// verdicts are those of the unbatched algorithm.
type batch struct {
	c        *checker
	sha      string
	baseline bool
	groups   []*batchGroup
	index    map[string][2]int // spec key -> group, position
}

func newBatch(c *checker, sha string, baseline bool, specs []runSpec) *batch {
	b := &batch{c: c, sha: sha, baseline: baseline, index: map[string][2]int{}}
	byTarget := map[string]int{}
	for _, s := range specs {
		if _, ok := b.index[s.key()]; ok {
			continue
		}
		gi, ok := byTarget[s.target]
		if !ok {
			gi = len(b.groups)
			byTarget[s.target] = gi
			b.groups = append(b.groups, &batchGroup{})
		}
		g := b.groups[gi]
		b.index[s.key()] = [2]int{gi, len(g.specs)}
		g.specs = append(g.specs, s)
	}
	for _, g := range b.groups {
		g.per = make([]runResult, len(g.specs))
		if len(g.specs) > 1 {
			g.union = unionSpec(g.specs)
		}
	}
	return b
}

// unionSpec is the target with the sorted, de-duplicated union of the package patterns and no -run filter;
// a spec without packages makes the union run without packages.
func unionSpec(specs []runSpec) runSpec {
	u := runSpec{target: specs[0].target}
	seen := map[string]bool{}
	for _, s := range specs {
		if len(s.pkgs) == 0 {
			u.pkgs = nil
			return u
		}
		for _, p := range s.pkgs {
			if !seen[p] {
				seen[p] = true
				u.pkgs = append(u.pkgs, p)
			}
		}
	}
	sort.Strings(u.pkgs)
	return u
}

// clean reports whether a union run may stand in for the runs of the individual specs.
func clean(r runResult) bool {
	return r.exit == 0 && !r.missing && !r.skipped && !r.fallback && !r.build
}

// first returns the first wave of runs: the single spec of a one-spec target, the union of the others.
func (b *batch) first() []func() error {
	var tasks []func() error
	for _, g := range b.groups {
		if len(g.specs) == 1 {
			tasks = append(tasks, func() (err error) {
				g.per[0], err = b.c.execute(b.sha, g.specs[0], b.baseline)
				return err
			})
			continue
		}
		tasks = append(tasks, func() (err error) {
			g.unionRes, err = b.c.execute(b.sha, g.union, b.baseline)
			g.batched = err == nil && clean(g.unionRes)
			return err
		})
	}
	return tasks
}

// second returns the fallback runs, one per spec of every target whose union run was not usable. It must be
// called after the tasks of first have finished.
func (b *batch) second() []func() error {
	var tasks []func() error
	for _, g := range b.groups {
		if len(g.specs) == 1 || g.batched {
			continue
		}
		for i := range g.specs {
			tasks = append(tasks, func() (err error) {
				g.per[i], err = b.c.execute(b.sha, g.specs[i], b.baseline)
				return err
			})
		}
	}
	return tasks
}

// result is the run result that stands for spec once both waves have finished.
func (b *batch) result(s runSpec) runResult {
	ix := b.index[s.key()]
	g := b.groups[ix[0]]
	if g.batched {
		return g.unionRes
	}
	return g.per[ix[1]]
}
