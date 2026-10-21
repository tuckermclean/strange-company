package verify

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"strange.company/sc/internal/contracts"
)

// The observer (sc-verify run, plan 22 sections 8 and 14.1) reads the repository's validation.yaml itself. The
// document is checked against the rules of contracts/repo-validation.schema.yaml for every field the observer uses;
// the schema files are not shipped with the static binary, so the rules are restated here and anything the observer
// does not understand makes the run refuse (22-R-1: fail closed).

// obsSuite is one spec.verification.suites entry.
type obsSuite struct {
	Name, Adapter, Language string
	Argv                    []string
	WorkingDir              string
	ReportPath              string // "-" is the suite's stdout
	TimeoutSeconds          int
	Env                     map[string]string
	Phases                  []string
	SelectArgv, RedScope    []string
}

// obsCheck is one spec.verification.checks entry.
type obsCheck struct {
	Name           string
	Argv           []string
	ToolingFrom    string // base (default) or head
	TimeoutSeconds int
	Env            map[string]string
}

// obsConfig is the part of validation.yaml the observer executes.
type obsConfig struct {
	Image  string
	Suites []obsSuite
	Checks []obsCheck
}

var (
	obsNameRE   = regexp.MustCompile(`^[a-z][a-z0-9-]{0,40}$`)
	obsImageRE  = regexp.MustCompile(`^[a-z0-9.-]+(:[0-9]+)?/[a-z0-9._/-]+@sha256:[a-f0-9]{64}$`)
	obsEnvKeyRE = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)
	obsRepoRE   = regexp.MustCompile(`^[A-Za-z0-9._@+/{}-]{1,400}$`)
	obsLangs    = map[string]bool{"go": true, "python": true, "javascript": true, "typescript": true, "rust": true, "java": true, "kotlin": true, "csharp": true, "shell": true}
)

// obsFixedEnv are the child environment variables the observer owns; a suite's env cannot replace them.
var obsFixedEnv = map[string]bool{"PATH": true, "HOME": true, "TMPDIR": true, "CI": true}

func obsRepoPath(p string) bool {
	if !obsRepoRE.MatchString(p) || strings.HasPrefix(p, "/") {
		return false
	}
	for _, c := range strings.Split(p, "/") {
		if c == ".." {
			return false
		}
	}
	return true
}

func obsMap(v any, what string, allowed ...string) (map[string]any, error) {
	m, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s is not a mapping", what)
	}
	ok2 := map[string]bool{}
	for _, a := range allowed {
		ok2[a] = true
	}
	var extra []string
	for k := range m {
		if !ok2[k] {
			extra = append(extra, k)
		}
	}
	sort.Strings(extra)
	if len(extra) > 0 {
		return nil, fmt.Errorf("%s has unknown keys %v", what, extra)
	}
	return m, nil
}

func obsStr(m map[string]any, key, what string) (string, error) {
	s, ok := m[key].(string)
	if !ok {
		return "", fmt.Errorf("%s.%s is not a string", what, key)
	}
	return s, nil
}

func obsArgv(v any, what string, min, max int) ([]string, error) {
	if v == nil {
		if min == 0 {
			return nil, nil
		}
		return nil, fmt.Errorf("%s is required", what)
	}
	l, err := stringList(v)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", what, err)
	}
	if len(l) < min || len(l) > max {
		return nil, fmt.Errorf("%s has %d entries, want %d to %d", what, len(l), min, max)
	}
	for _, s := range l {
		if len(s) > 500 || (min > 0 && s == "") {
			return nil, fmt.Errorf("%s: entry %q is empty or longer than 500 bytes", what, s)
		}
	}
	return l, nil
}

func obsEnv(v any, what string) (map[string]string, error) {
	if v == nil {
		return nil, nil
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s is not a mapping", what)
	}
	out := map[string]string{}
	for k, raw := range m {
		s, ok := raw.(string)
		if !ok || len(s) > 500 || !obsEnvKeyRE.MatchString(k) {
			return nil, fmt.Errorf("%s: entry %q is not a valid name with a string value of at most 500 bytes", what, k)
		}
		// The child environment holds no SC_ variable (plan 22 section 8): a suite cannot reintroduce one.
		if strings.HasPrefix(k, "SC_") {
			return nil, fmt.Errorf("%s: %s is reserved (no SC_ variable reaches the code under test)", what, k)
		}
		if obsFixedEnv[k] {
			continue
		}
		out[k] = s
	}
	return out, nil
}

// parseValidation parses the RepositoryValidation document.
func parseValidation(doc []byte, adapters map[string]Adapter) (obsConfig, error) {
	var cfg obsConfig
	v, err := contracts.ParseYAML(doc)
	if err != nil {
		return cfg, fmt.Errorf("validation.yaml: %w", err)
	}
	top, err := obsMap(v, "validation.yaml", "apiVersion", "kind", "metadata", "spec")
	if err != nil {
		return cfg, err
	}
	if top["apiVersion"] != "strange.company/v1alpha1" || top["kind"] != "RepositoryValidation" {
		return cfg, fmt.Errorf("validation.yaml: apiVersion or kind is wrong")
	}
	spec, err := obsMap(top["spec"], "spec", "verification", "local")
	if err != nil {
		return cfg, err
	}
	ver, err := obsMap(spec["verification"], "spec.verification", "image", "suites", "checks", "protectedTestIds")
	if err != nil {
		return cfg, err
	}
	if cfg.Image, err = obsStr(ver, "image", "spec.verification"); err != nil {
		return cfg, err
	}
	if !obsImageRE.MatchString(cfg.Image) {
		return cfg, fmt.Errorf("spec.verification.image %q is not digest pinned", cfg.Image)
	}
	suites, ok := ver["suites"].([]any)
	if !ok || len(suites) < 1 || len(suites) > 10 {
		return cfg, fmt.Errorf("spec.verification.suites must list 1 to 10 suites")
	}
	seen := map[string]bool{}
	for i, raw := range suites {
		what := fmt.Sprintf("suites[%d]", i)
		m, err := obsMap(raw, what, "name", "adapter", "language", "argv", "workingDir", "report", "timeoutSeconds", "env", "phases", "selectArgv", "redScope")
		if err != nil {
			return cfg, err
		}
		var s obsSuite
		if s.Name, err = obsStr(m, "name", what); err != nil {
			return cfg, err
		}
		if !obsNameRE.MatchString(s.Name) || seen[s.Name] {
			return cfg, fmt.Errorf("%s: name %q is invalid or duplicated", what, s.Name)
		}
		seen[s.Name] = true
		if s.Adapter, err = obsStr(m, "adapter", what); err != nil {
			return cfg, err
		}
		if _, ok := adapters[s.Adapter]; !ok {
			return cfg, fmt.Errorf("%s: adapter %q is not in the platform verification policy", what, s.Adapter)
		}
		if s.Language, err = obsStr(m, "language", what); err != nil {
			return cfg, err
		}
		if !obsLangs[s.Language] {
			return cfg, fmt.Errorf("%s: unknown language %q", what, s.Language)
		}
		if s.Argv, err = obsArgv(m["argv"], what+".argv", 1, 40); err != nil {
			return cfg, err
		}
		if m["workingDir"] != nil {
			if s.WorkingDir, err = obsStr(m, "workingDir", what); err != nil {
				return cfg, err
			}
			if !obsRepoPath(s.WorkingDir) {
				return cfg, fmt.Errorf("%s: workingDir %q is not a repository path", what, s.WorkingDir)
			}
		}
		rep, err := obsMap(m["report"], what+".report", "path")
		if err != nil {
			return cfg, err
		}
		if s.ReportPath, err = obsStr(rep, "path", what+".report"); err != nil {
			return cfg, err
		}
		if s.ReportPath == "-" {
			if adapters[s.Adapter].Format != "go-test-json" {
				return cfg, fmt.Errorf("%s: report path - needs a go-test-json adapter", what)
			}
		} else if !obsRepoPath(s.ReportPath) || strings.ContainsAny(s.ReportPath, "{}") {
			return cfg, fmt.Errorf("%s: report path %q is not a repository path", what, s.ReportPath)
		}
		n, ok := intValue(m["timeoutSeconds"])
		if !ok || n < 30 || n > 3600 {
			return cfg, fmt.Errorf("%s: timeoutSeconds must be an integer from 30 to 3600", what)
		}
		s.TimeoutSeconds = n
		if s.Env, err = obsEnv(m["env"], what+".env"); err != nil {
			return cfg, err
		}
		phases, err := stringList(m["phases"])
		if err != nil || len(phases) < 1 {
			return cfg, fmt.Errorf("%s: phases must list red and/or green", what)
		}
		seenPhase := map[string]bool{}
		for _, p := range phases {
			if (p != "red" && p != "green") || seenPhase[p] {
				return cfg, fmt.Errorf("%s: phase %q is invalid or repeated", what, p)
			}
			seenPhase[p] = true
		}
		s.Phases = phases
		if s.SelectArgv, err = obsArgv(m["selectArgv"], what+".selectArgv", 0, 10); err != nil {
			return cfg, err
		}
		if s.RedScope, err = obsArgv(m["redScope"], what+".redScope", 0, 10); err != nil {
			return cfg, err
		}
		cfg.Suites = append(cfg.Suites, s)
	}
	if raw, ok := ver["checks"]; ok && raw != nil {
		checks, ok := raw.([]any)
		if !ok || len(checks) > 10 {
			return cfg, fmt.Errorf("spec.verification.checks must list at most 10 checks")
		}
		seenCheck := map[string]bool{}
		for i, raw := range checks {
			what := fmt.Sprintf("checks[%d]", i)
			m, err := obsMap(raw, what, "name", "toolingFrom", "configPaths", "argv", "timeoutSeconds", "env")
			if err != nil {
				return cfg, err
			}
			var c obsCheck
			if c.Name, err = obsStr(m, "name", what); err != nil {
				return cfg, err
			}
			if !obsNameRE.MatchString(c.Name) || seenCheck[c.Name] {
				return cfg, fmt.Errorf("%s: name %q is invalid or duplicated", what, c.Name)
			}
			seenCheck[c.Name] = true
			c.ToolingFrom = "base"
			if m["toolingFrom"] != nil {
				if c.ToolingFrom, err = obsStr(m, "toolingFrom", what); err != nil {
					return cfg, err
				}
				if c.ToolingFrom != "base" && c.ToolingFrom != "head" {
					return cfg, fmt.Errorf("%s: toolingFrom %q is neither base nor head", what, c.ToolingFrom)
				}
			}
			if c.Argv, err = obsArgv(m["argv"], what+".argv", 1, 40); err != nil {
				return cfg, err
			}
			n, ok := intValue(m["timeoutSeconds"])
			if !ok || n < 10 || n > 3600 {
				return cfg, fmt.Errorf("%s: timeoutSeconds must be an integer from 10 to 3600", what)
			}
			c.TimeoutSeconds = n
			if c.Env, err = obsEnv(m["env"], what+".env"); err != nil {
				return cfg, err
			}
			cfg.Checks = append(cfg.Checks, c)
		}
	}
	return cfg, nil
}

func (s obsSuite) inPhase(phase string) bool {
	for _, p := range s.Phases {
		if p == phase {
			return true
		}
	}
	return false
}
