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
	"sort"
	"strings"
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

// evaluateRed runs T3 (stubs), T4, T5 and T6 for one RED commit and queues the T7 check.
func (c *checker) evaluateRed(ci commitInfo) error {
	var bad []string
	for _, f := range ci.files {
		if matchAny(c.behav, f.path) && !matchAny(c.tests, f.path) {
			bad = append(bad, c.stubViolations(ci.sha, f)...)
		}
	}
	for _, b := range bad {
		c.fail(CodeStubInvalid, ci.sha, "RED commit %s: %s", short(ci.sha), b)
	}
	if len(bad) > 0 {
		return nil
	}
	spec, problem := c.parseRedTarget(ci)
	if problem != "" {
		c.fail(CodeRedTargetInvalid, ci.sha, "RED commit %s: %s", short(ci.sha), problem)
		return nil
	}

	// T5: baseline at B.
	base, ok := c.baselines[spec.key()]
	if !ok {
		var err error
		if base, err = c.execute(c.baseSHA, spec, true); err != nil {
			return err
		}
		c.baselines[spec.key()] = base
	}
	baseline := map[string]bool{}
	if !base.skipped && !base.missing && !(base.fallback && base.exit != 0 && len(base.failed) == 0) {
		if base.exit != 0 {
			c.fail(CodeBaselineFailed, ci.sha, "the red target %s fails at the merge base %s (main is broken): %s", spec.target, short(c.baseSHA), tail(base.output))
			return nil
		}
		for _, tc := range base.cases {
			baseline[tc.id] = true
		}
	}

	// T6: RED run.
	rr, err := c.execute(ci.sha, spec, false)
	if err != nil {
		return err
	}
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
		return nil
	case rr.skipped:
		run.BuildOK = false
		c.fail(CodeRedBuildFailure, ci.sha, "RED commit %s: none of the packages %v exists", short(ci.sha), spec.pkgs)
		return nil
	case rr.exit == 0:
		c.fail(CodeRedPassed, ci.sha, "RED commit %s: the red target %s passes, so the behavior already exists", short(ci.sha), spec.target)
		return nil
	case rr.build:
		run.BuildOK = false
		c.fail(CodeRedBuildFailure, ci.sha, "RED commit %s: the red target %s did not build or crashed (exit %d): %s", short(ci.sha), spec.target, rr.exit, tail(rr.output))
		return nil
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
		return nil
	}
	c.pending = append(c.pending, headCheck{commit: ci.sha, spec: spec, failed: rr.failed})
	return nil
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

// checkHead implements T7: at HEAD each queued target exits 0 and passes every test RED failed.
func (c *checker) checkHead() error {
	results := map[string]runResult{}
	for _, p := range c.pending {
		hr, ok := results[p.spec.key()]
		if !ok {
			var err error
			if hr, err = c.execute(c.headSHA, p.spec, false); err != nil {
				return err
			}
			results[p.spec.key()] = hr
		}
		switch {
		case hr.missing:
			c.fail(CodeRedNotGreen, p.commit, "RED commit %s: the red target %s does not exist at HEAD", short(p.commit), p.spec.target)
			continue
		case hr.exit != 0:
			c.fail(CodeRedNotGreen, p.commit, "RED commit %s: the red target %s exits %d at HEAD: %s", short(p.commit), p.spec.target, hr.exit, tail(hr.output))
			continue
		}
		var notGreen []string
		for _, id := range p.failed {
			if !hr.passed[id] {
				notGreen = append(notGreen, id)
			}
		}
		if len(notGreen) > 0 {
			c.fail(CodeRedNotGreen, p.commit, "RED commit %s: at HEAD these tests are missing, skipped or not passed: %s", short(p.commit), strings.Join(notGreen, ", "))
		}
	}
	return nil
}

func tail(s string) string {
	s = strings.TrimSpace(s)
	const max = 1500
	if len(s) > max {
		s = "..." + s[len(s)-max:]
	}
	return strings.ReplaceAll(s, "\n", " | ")
}

// worktree checks sha out into a fresh directory and returns it with its cleanup.
func (c *checker) worktree(sha string) (string, func(), error) {
	if c.tmp == "" {
		d, err := os.MkdirTemp("", "tdd-discipline-")
		if err != nil {
			return "", nil, err
		}
		c.tmp = d
	}
	c.nwt++
	dir := filepath.Join(c.tmp, fmt.Sprintf("wt%d", c.nwt))
	if _, err := c.git("worktree", "add", "--detach", "--quiet", "--force", dir, sha); err != nil {
		return "", nil, err
	}
	cleanup := func() {
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
	dir, cleanup, err := c.worktree(sha)
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
		junit = filepath.Join(c.tmp, fmt.Sprintf("junit%d.xml", c.nwt))
		args := []string{spec.target, "JUNIT=" + junit}
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
