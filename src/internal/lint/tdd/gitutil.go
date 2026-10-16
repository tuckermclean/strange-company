package tdd

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"
)

// change is one entry of a name-status listing.
type change struct {
	status byte // 'A', 'M', 'D', 'T'
	path   string
}

// LoadPolicy reads spec.tddDiscipline from a policy/ci.yaml file.
func LoadPolicy(path string) (Policy, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Policy{}, err
	}
	return ParsePolicy(path, b)
}

// ParsePolicy reads spec.tddDiscipline from the bytes of a policy/ci.yaml file; path is used in errors.
func ParsePolicy(path string, b []byte) (Policy, error) {
	var doc struct {
		Spec struct {
			TDD *Policy `yaml:"tddDiscipline"`
		} `yaml:"spec"`
	}
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return Policy{}, fmt.Errorf("%s: %w", path, err)
	}
	if doc.Spec.TDD == nil {
		return Policy{}, fmt.Errorf("%s: no spec.tddDiscipline", path)
	}
	return *doc.Spec.TDD, nil
}

// cleanEnv is the process environment without the variables of an enclosing make or git.
func cleanEnv(extra []string) []string {
	drop := map[string]bool{"MAKEFLAGS": true, "MAKELEVEL": true, "MFLAGS": true, "MAKEOVERRIDES": true,
		"GNUMAKEFLAGS": true, "MAKE_TERMOUT": true, "MAKE_TERMERR": true,
		"GIT_DIR": true, "GIT_WORK_TREE": true, "GIT_INDEX_FILE": true, "GIT_PREFIX": true}
	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if !drop[k] {
			env = append(env, kv)
		}
	}
	return append(env, extra...)
}

// gitIn runs git in dir and returns stdout.
func gitIn(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "core.quotepath=off"}, args...)...)
	cmd.Env = cleanEnv(nil)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
	}
	return out.String(), nil
}

func (c *checker) git(args ...string) (string, error) { return gitIn(c.root, args...) }

func (c *checker) revParse(ref string) (string, error) {
	out, err := c.git("rev-parse", "--verify", "--quiet", ref+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("cannot resolve %q: %w", ref, err)
	}
	return strings.TrimSpace(out), nil
}

func parseNameStatus(out string) []change {
	parts := strings.Split(out, "\x00")
	var chs []change
	for i := 0; i+1 < len(parts); i += 2 {
		if parts[i] == "" {
			break
		}
		chs = append(chs, change{status: parts[i][0], path: parts[i+1]})
	}
	return chs
}

func (c *checker) diffNames(a, b string) ([]change, error) {
	out, err := c.git("diff", "--name-status", "-z", "--no-renames", a, b)
	if err != nil {
		return nil, err
	}
	return parseNameStatus(out), nil
}

func (c *checker) commitChanges(sha string) ([]change, error) {
	out, err := c.git("diff-tree", "-r", "--root", "--no-commit-id", "--name-status", "-z", "--no-renames", sha)
	if err != nil {
		return nil, err
	}
	return parseNameStatus(out), nil
}

// show returns the content of path at commit sha.
func (c *checker) show(sha, path string) ([]byte, error) {
	cmd := exec.Command("git", "-C", c.root, "show", sha+":"+path)
	cmd.Env = cleanEnv(nil)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git show %s:%s: %v: %s", short(sha), path, err, strings.TrimSpace(errb.String()))
	}
	return out.Bytes(), nil
}

func (c *checker) exists(sha, path string) bool {
	_, err := c.git("cat-file", "-e", sha+":"+path)
	return err == nil
}

// globRegexp translates a path glob: ** crosses directories, * and ? do not (unless anyStar, for branch
// names), and "**/" also matches no directory at all.
func globRegexp(pat string, anyStar bool) (*regexp.Regexp, error) {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(pat); {
		switch {
		case strings.HasPrefix(pat[i:], "**/"):
			b.WriteString("(?:.*/)?")
			i += 3
		case strings.HasPrefix(pat[i:], "**"):
			b.WriteString(".*")
			i += 2
		case pat[i] == '*':
			if anyStar {
				b.WriteString(".*")
			} else {
				b.WriteString("[^/]*")
			}
			i++
		case pat[i] == '?':
			b.WriteString("[^/]")
			i++
		default:
			b.WriteString(regexp.QuoteMeta(pat[i : i+1]))
			i++
		}
	}
	b.WriteString("$")
	return regexp.Compile(b.String())
}

func compileGlobs(pats []string, anyStar bool) ([]*regexp.Regexp, error) {
	var res []*regexp.Regexp
	for _, p := range pats {
		re, err := globRegexp(p, anyStar)
		if err != nil {
			return nil, fmt.Errorf("glob %q: %w", p, err)
		}
		res = append(res, re)
	}
	return res, nil
}

func matchAny(res []*regexp.Regexp, p string) bool {
	for _, re := range res {
		if re.MatchString(p) {
			return true
		}
	}
	return false
}
