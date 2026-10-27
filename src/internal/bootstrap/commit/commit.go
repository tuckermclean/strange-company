// Package commit creates the generated commits of plan 50 section 13 and enforces their scope.
package commit

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"

	"strange.company/sc/internal/clock"
)

// Kind is the kind of generated commit.
type Kind int

const (
	// Install is the commit of plan.install-identity.
	Install Kind = iota
	// Recovery is a black-start recovery commit; it may raise emergencyStop.
	Recovery
)

// Change is one changed path with its content before and after (nil: absent).
type Change struct {
	Path   string
	Before []byte
	After  []byte
}

// Error is a refusal with a stable code.
type Error struct {
	Code    string
	Message string
	Paths   []string
}

// Error implements error.
func (e *Error) Error() string {
	if len(e.Paths) > 0 {
		return e.Code + ": " + e.Message + " (" + strings.Join(e.Paths, ", ") + ")"
	}
	return e.Code + ": " + e.Message
}

const budgetsPath = "policy/budgets.yaml"

// allowed reports whether p is inside the scope of a generated commit of cluster.
func allowed(cluster, p string) bool {
	if p == "" || path.IsAbs(p) || path.Clean(p) != p {
		return false
	}
	if p == ".sops.yaml" {
		return true
	}
	if cluster == "" {
		return false
	}
	for _, root := range []string{"gitops/clusters/", "secrets/offcluster/", "secrets/ci/"} {
		prefix := root + cluster + "/"
		if strings.HasPrefix(p, prefix) && len(p) > len(prefix) {
			return true
		}
	}
	return false
}

// CheckScope fails with COMMIT_SCOPE_VIOLATION when a change is outside the commit's scope (plan 50 section 13): the
// cluster's gitops, off-cluster and CI secret directories and .sops.yaml, plus, for a recovery commit only,
// policy/budgets.yaml with nothing changed but spec.emergencyStop (level workers, or all with --drill).
func CheckScope(cluster string, kind Kind, drill bool, changes []Change) error {
	var bad []string
	for _, c := range changes {
		switch {
		case c.Path == budgetsPath && kind == Recovery:
			if !budgetsOK(c, drill) {
				bad = append(bad, c.Path)
			}
		case allowed(cluster, c.Path):
		default:
			bad = append(bad, c.Path)
		}
	}
	if len(bad) == 0 {
		return nil
	}
	sort.Strings(bad)
	return &Error{Code: "COMMIT_SCOPE_VIOLATION", Message: "the commit changes paths outside its scope", Paths: bad}
}

// budgetsOK compares the parsed YAML of both versions: only spec.emergencyStop may differ, and its new level must be
// workers (all with drill).
func budgetsOK(c Change, drill bool) bool {
	if c.Before == nil || c.After == nil {
		return false
	}
	var before, after map[string]any
	if yaml.Unmarshal(c.Before, &before) != nil || yaml.Unmarshal(c.After, &after) != nil {
		return false
	}
	stop, ok := popEmergencyStop(after)
	if !ok {
		return false
	}
	popEmergencyStop(before)
	if !reflect.DeepEqual(before, after) {
		return false
	}
	level, _ := stop["level"].(string)
	return level == "workers" || (drill && level == "all")
}

// popEmergencyStop removes spec.emergencyStop from doc and returns it.
func popEmergencyStop(doc map[string]any) (map[string]any, bool) {
	spec, ok := doc["spec"].(map[string]any)
	if !ok {
		return nil, false
	}
	stop, ok := spec["emergencyStop"].(map[string]any)
	delete(spec, "emergencyStop")
	return stop, ok
}

// Request describes a commit to create.
type Request struct {
	Repo       string
	Cluster    string
	Kind       Kind
	Drill      bool
	Subject    string
	BaseDomain string
	InstallUID string
	RunID      string
	Clock      clock.Clock
	Paths      []string
}

func git(ctx context.Context, repo string, env []string, stdin []byte, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = repo
	cmd.Env = append(os.Environ(), env...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	if err := cmd.Run(); err != nil {
		return out.Bytes(), fmt.Errorf("git %s: %w: %s", args[len(args)-1], err, strings.TrimSpace(errb.String()))
	}
	return out.Bytes(), nil
}

// stagedChanges lists the staged diff against HEAD (or the empty tree) with both versions of every path.
func stagedChanges(ctx context.Context, repo string) ([]Change, error) {
	_, headErr := git(ctx, repo, nil, nil, "rev-parse", "--verify", "-q", "HEAD")
	hasHead := headErr == nil
	args := []string{"diff", "--cached", "--name-only", "-z", "--no-renames"}
	if !hasHead {
		args = append(args, "--root")
	}
	out, err := git(ctx, repo, nil, nil, args...)
	if err != nil {
		return nil, err
	}
	var changes []Change
	for _, p := range strings.Split(string(out), "\x00") {
		if p == "" {
			continue
		}
		c := Change{Path: p}
		if hasHead {
			if b, err := git(ctx, repo, nil, nil, "cat-file", "blob", "HEAD:"+p); err == nil {
				c.Before = nonNil(b)
			}
		}
		if b, err := git(ctx, repo, nil, nil, "cat-file", "blob", ":"+p); err == nil {
			c.After = nonNil(b)
		}
		changes = append(changes, c)
	}
	return changes, nil
}

func nonNil(b []byte) []byte {
	if b == nil {
		return []byte{}
	}
	return b
}

// Create stages Paths, scope-checks the staged diff and commits it. A refused commit leaves HEAD and the working tree
// untouched and unstages Paths.
func Create(ctx context.Context, req Request) (sha string, err error) {
	if len(req.Paths) == 0 {
		return "", &Error{Code: "COMMIT_NO_PATHS", Message: "no paths to commit"}
	}
	unstage := func() {
		_, _ = git(context.WithoutCancel(ctx), req.Repo, nil, nil, append([]string{"restore", "--staged", "--"}, req.Paths...)...)
	}
	for _, p := range req.Paths {
		// A path that exists neither on disk nor in the index is nothing to stage.
		if _, serr := os.Lstat(filepath.Join(req.Repo, filepath.FromSlash(p))); serr != nil {
			if out, lerr := git(ctx, req.Repo, nil, nil, "ls-files", "--", p); lerr != nil || len(bytes.TrimSpace(out)) == 0 {
				continue
			}
		}
		if _, err := git(ctx, req.Repo, nil, nil, "add", "-A", "--", p); err != nil {
			unstage()
			return "", err
		}
	}
	changes, err := stagedChanges(ctx, req.Repo)
	if err != nil {
		unstage()
		return "", err
	}
	if err := CheckScope(req.Cluster, req.Kind, req.Drill, changes); err != nil {
		unstage()
		return "", err
	}
	if len(changes) == 0 {
		return "", &Error{Code: "COMMIT_EMPTY", Message: "nothing to commit"}
	}
	subject := req.Subject
	if subject == "" {
		subject = "bootstrap(" + req.Cluster + "): installation config"
	}
	msg := subject + "\n\nSC-Install-Uid: " + req.InstallUID + "\nSC-Run-Id: " + req.RunID + "\n"
	email := "sc-bootstrap@" + req.BaseDomain
	date := req.Clock.Now().UTC().Format("2006-01-02T15:04:05Z")
	env := []string{
		"GIT_AUTHOR_NAME=sc-bootstrap", "GIT_AUTHOR_EMAIL=" + email, "GIT_AUTHOR_DATE=" + date,
		"GIT_COMMITTER_NAME=sc-bootstrap", "GIT_COMMITTER_EMAIL=" + email, "GIT_COMMITTER_DATE=" + date,
	}
	if _, err := git(ctx, req.Repo, env, []byte(msg), "-c", "commit.gpgsign=false", "commit", "-q", "--no-verify", "-F", "-"); err != nil {
		unstage()
		return "", err
	}
	out, err := git(ctx, req.Repo, nil, nil, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// FindInstall returns the commit in HEAD's history that carries the trailer SC-Install-Uid: uid.
func FindInstall(ctx context.Context, repo, uid string) (sha string, found bool, err error) {
	if _, herr := git(ctx, repo, nil, nil, "rev-parse", "--verify", "-q", "HEAD"); herr != nil {
		return "", false, nil
	}
	out, err := git(ctx, repo, nil, nil, "log", "--format=%H%x1f%(trailers:key=SC-Install-Uid,valueonly,separator=%x1f)%x1e")
	if err != nil {
		return "", false, err
	}
	for _, rec := range strings.Split(string(out), "\x1e") {
		fields := strings.Split(strings.TrimSpace(rec), "\x1f")
		if len(fields) < 2 {
			continue
		}
		for _, v := range fields[1:] {
			if strings.TrimSpace(v) == uid {
				return strings.TrimSpace(fields[0]), true, nil
			}
		}
	}
	return "", false, nil
}
