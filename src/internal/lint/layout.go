package lint

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Rule IDs of `sc-lint layout` (plan 01 sections 6.1, 6.6, 7.3).
const (
	ruleSymlink  = "no-symlinks"
	ruleExec     = "executable-files"
	ruleIgnore   = "gitignore-entries"
	ruleTopLevel = "top-level-dirs"
	ruleEnvFile  = "no-tracked-bootstrap-env"
	ruleSize     = "file-size-limit"
	ruleAgentsSz = "agents-md-size"
	ruleMarkers  = "agents-md-policy-markers"
	ruleMakeTgt  = "make-targets-known"
)

const maxFileSize = 2 * 1024 * 1024
const maxAgentsLines = 300

// gitignoreEntries are the exact .gitignore lines of plan 01 section 6.6.
var gitignoreEntries = []string{
	"/bootstrap.env", "/bootstrap.env.*", "!/bootstrap.env.example", "/.bootstrap/", "/.cache/", "/out/",
	"*.tfstate", "*.tfstate.*", "*.tfplan", ".terraform/", "*.agekey", "!/test/fixtures/age/ci-test.agekey",
	"*.tar.age", "kubeconfig", ".DS_Store", "/.claude/settings.local.json", "/.claude/scheduled_tasks.lock",
}

// topLevelDirs are the directories of plan 01 section 6.1 (a directory exists only if a plan owns it).
var topLevelDirs = map[string]bool{
	".claude": true, ".strange-company": true, ".woodpecker": true, ".github": true, "contracts": true,
	"plans": true, "procedures": true, "adr": true, "context": true, "policy": true, "versions": true,
	"toolbox": true, "src": true, "images": true, "harnesses": true, "charts": true, "gitops": true,
	"forge": true, "observability": true, "infra": true, "secrets": true, "security": true,
	"templates": true, "evidence": true, "tools": true, "test": true, "mk": true,
}

type trackedFile struct {
	mode string
	path string
}

func trackedFiles(root string) ([]trackedFile, error) {
	cmd := exec.Command("git", "-C", root, "ls-files", "-s", "-z")
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git ls-files: %v: %s", err, strings.TrimSpace(errb.String()))
	}
	var files []trackedFile
	for _, rec := range strings.Split(out.String(), "\x00") {
		if rec == "" {
			continue
		}
		meta, p, ok := strings.Cut(rec, "\t")
		if !ok {
			continue
		}
		files = append(files, trackedFile{mode: strings.Fields(meta)[0], path: p})
	}
	return files, nil
}

// execAllowed implements plan 01 section 6.1: bootstrap, toolbox/bin/* and test/e2e/**/run.
func execAllowed(p string) bool {
	return p == "bootstrap" || strings.HasPrefix(p, "toolbox/bin/") ||
		(strings.HasPrefix(p, "test/e2e/") && path.Base(p) == "run")
}

func sizeExempt(p string) bool {
	if ok, _ := path.Match("contracts/*.yaml", p); ok {
		return true
	}
	ok, _ := path.Match("gitops/clusters/*/flux-system/gotk-components.yaml", p)
	return ok
}

// LayoutCheck evaluates the repository layout rules on the tracked files under root.
func LayoutCheck(root string) (Result, error) {
	var r Result
	files, err := trackedFiles(root)
	if err != nil {
		return r, err
	}
	for _, id := range []string{ruleSymlink, ruleExec, ruleTopLevel, ruleEnvFile, ruleSize} {
		r.ran(id)
	}
	seenTop := map[string]bool{}
	for _, f := range files {
		switch f.mode {
		case "120000":
			r.add(ruleSymlink, f.path, "symlinks are not allowed")
		case "100755":
			if !execAllowed(f.path) {
				r.add(ruleExec, f.path, "executable bit set; only bootstrap, toolbox/bin/* and test/e2e/**/run may be executable")
			}
		}
		if top, rest, found := strings.Cut(f.path, "/"); found && rest != "" && !topLevelDirs[top] && !seenTop[top] {
			seenTop[top] = true
			r.add(ruleTopLevel, top+"/", "unknown top-level directory (plan 01 section 6.1)")
		}
		if b := path.Base(f.path); f.path == b && b != "bootstrap.env.example" && (b == "bootstrap.env" || strings.HasPrefix(b, "bootstrap.env.")) {
			r.add(ruleEnvFile, f.path, "bootstrap.env files must never be tracked")
		}
		if f.mode == "120000" || f.mode == "160000" || sizeExempt(f.path) {
			continue
		}
		if st, err := os.Lstat(filepath.Join(root, f.path)); err == nil && st.Size() > maxFileSize {
			r.add(ruleSize, f.path, "%d bytes exceeds the 2 MiB limit", st.Size())
		}
	}
	checkGitignore(&r, root)
	checkAgents(&r, root)
	if err := checkMakeTargets(&r, root); err != nil {
		return r, err
	}
	return r, nil
}

func checkGitignore(r *Result, root string) {
	r.ran(ruleIgnore)
	b, err := os.ReadFile(filepath.Join(root, ".gitignore"))
	if err != nil {
		r.add(ruleIgnore, ".gitignore", "missing: %v", err)
		return
	}
	have := map[string]bool{}
	for _, l := range strings.Split(string(b), "\n") {
		have[strings.TrimSpace(l)] = true
	}
	for _, e := range gitignoreEntries {
		if !have[e] {
			r.add(ruleIgnore, ".gitignore", "missing entry %q", e)
		}
	}
}

var markerRE = regexp.MustCompile(`<!--\s*policy:\s*([A-Za-z0-9_.-]+)\s*-->`)

func checkAgents(r *Result, root string) {
	r.ran(ruleAgentsSz)
	r.ran(ruleMarkers)
	b, err := os.ReadFile(filepath.Join(root, "AGENTS.md"))
	if err != nil {
		r.add(ruleAgentsSz, "AGENTS.md", "missing: %v", err)
		return
	}
	body := strings.TrimRight(string(b), "\n")
	lines := strings.Split(body, "\n")
	if len(lines) > maxAgentsLines {
		r.add(ruleAgentsSz, "AGENTS.md", "%d lines exceeds the %d line limit", len(lines), maxAgentsLines)
	}
	policy := ""
	for _, p := range []string{".strange-company/policy.yaml", "policy/verification.yaml"} {
		if pb, err := os.ReadFile(filepath.Join(root, p)); err == nil {
			policy += string(pb) + "\n"
		}
	}
	in := false
	for _, l := range lines {
		if strings.HasPrefix(l, "#") {
			in = strings.HasPrefix(l, "## ") && strings.TrimSpace(strings.TrimPrefix(l, "## ")) == "Enforced rules"
			continue
		}
		t := strings.TrimSpace(l)
		if !in || !strings.HasPrefix(t, "- ") {
			continue
		}
		m := markerRE.FindStringSubmatch(t)
		if m == nil {
			r.add(ruleMarkers, "AGENTS.md", "bullet without a <!-- policy: key --> marker: %q", t)
			continue
		}
		if !regexp.MustCompile(`(?m)(^|[\s{,"'])` + regexp.QuoteMeta(m[1]) + `:`).MatchString(policy) {
			r.add(ruleMarkers, "AGENTS.md", "policy key %q not found in .strange-company/policy.yaml or policy/verification.yaml", m[1])
		}
	}
}

var spanRE = regexp.MustCompile("`[^`\n]*`")
var makeMentionRE = regexp.MustCompile(`(?:^|[^<\w-])make ([a-z][a-z0-9_-]*)([^a-z0-9_<-]|$)`)
var makeRuleRE = regexp.MustCompile(`^([A-Za-z0-9][A-Za-z0-9_.-]*)\s*:(\s|$)`)

func makeTargets(root string) (map[string]bool, error) {
	cmd := exec.Command("make", "-C", root, "-pn")
	cmd.Env = append(os.Environ(), "SC_IN_CONTAINER=1", "MAKEFLAGS=")
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("make -pn: %v: %s", err, strings.TrimSpace(errb.String()))
	}
	targets := map[string]bool{}
	prev := ""
	for _, l := range strings.Split(out.String(), "\n") {
		if m := makeRuleRE.FindStringSubmatch(l); m != nil && !strings.HasPrefix(prev, "# Not a target") {
			targets[m[1]] = true
		}
		prev = l
	}
	return targets, nil
}

func plannedEntries(root string) map[string]bool {
	planned := map[string]bool{}
	b, err := os.ReadFile(filepath.Join(root, "test", "fixtures", "layout", "planned-paths.txt"))
	if err != nil {
		return planned
	}
	for _, l := range strings.Split(string(b), "\n") {
		l = strings.TrimSpace(l)
		if l != "" && !strings.HasPrefix(l, "#") {
			planned[l] = true
		}
	}
	return planned
}

func checkMakeTargets(r *Result, root string) error {
	r.ran(ruleMakeTgt)
	targets, err := makeTargets(root)
	if err != nil {
		return err
	}
	planned := plannedEntries(root)
	plans, _ := filepath.Glob(filepath.Join(root, "plans", "*.md"))
	sort.Strings(plans)
	for _, p := range plans {
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		reported := map[string]bool{}
		var mentions [][]string
		for _, sp := range spanRE.FindAllString(string(b), -1) {
			mentions = append(mentions, makeMentionRE.FindAllStringSubmatch(sp, -1)...)
		}
		for _, m := range mentions {
			t := m[1]
			if strings.HasSuffix(t, "-") {
				continue
			}
			if targets[t] || planned["make "+t] || reported[t] {
				continue
			}
			reported[t] = true
			r.add(ruleMakeTgt, relPath(root, p), "`make %s` is neither a Makefile target nor listed as \"make %s\" in test/fixtures/layout/planned-paths.txt", t, t)
		}
	}
	return nil
}

func relPath(root, p string) string {
	if rel, err := filepath.Rel(root, p); err == nil {
		return filepath.ToSlash(rel)
	}
	return p
}
