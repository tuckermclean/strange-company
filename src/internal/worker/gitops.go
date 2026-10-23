package worker

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Git layer of sc-worker (plan 27 sections 13.3 and 14.3 to 14.11). Every invocation runs /usr/bin/git with the
// hardening environment and flags of section 13.3, from a scratch environment.

const gitBinary = "/usr/bin/git"

// gitHardening are the -c flags of plan 27 section 13.3.
var gitHardening = []string{
	"-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-c", "protocol.allow=never",
	"-c", "protocol.ssh.allow=always", "-c", "submodule.recurse=false", "-c", "commit.gpgsign=false",
	"-c", "tag.gpgsign=false", "-c", "core.symlinks=true",
}

const gitOutCap = 16 << 20

type capBuf struct {
	b     []byte
	total int
}

func (c *capBuf) Write(p []byte) (int, error) {
	c.total += len(p)
	if room := gitOutCap - len(c.b); room > 0 {
		q := p
		if len(q) > room {
			q = q[:room]
		}
		c.b = append(c.b, q...)
	}
	return len(p), nil
}

// gitOut is the outcome of one git invocation.
type gitOut struct {
	stdout []byte
	total  int // all stdout bytes, of which stdout holds at most gitOutCap
	stderr string
	code   int // exit code; -1 when git could not run or timed out
	err    error
}

func (g gitOut) text() string { return strings.TrimRight(string(g.stdout), "\n") }

func (w *worker) privPath(parts ...string) string {
	return filepath.Join(append([]string{w.cfg.PrivateDir}, parts...)...)
}

func shellWord(s string) string {
	if s != "" && regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`).MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// sshCommand is GIT_SSH_COMMAND of plan 27 section 10.
func (w *worker) sshCommand() string {
	prog := w.cfg.SSHProgram
	if prog == "" {
		prog = "/usr/bin/ssh"
	}
	return strings.Join([]string{
		shellWord(prog), "-i", shellWord(w.privPath("home", ".ssh", "id_ed25519")),
		"-o", "IdentitiesOnly=yes", "-o", "UserKnownHostsFile=" + shellWord(filepath.Join(w.cfg.SecretsDir, "known_hosts")),
		"-o", "StrictHostKeyChecking=yes", "-o", "BatchMode=yes",
	}, " ")
}

func (w *worker) gitEnv() []string {
	return []string{
		"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=" + w.privPath("home"), "TMPDIR=" + w.privPath("tmp"), "LC_ALL=C",
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + w.privPath("gitconfig"), "GIT_TERMINAL_PROMPT=0",
		"GIT_LFS_SKIP_SMUDGE=1", "GIT_SSH_COMMAND=" + w.sshCommand(), "GIT_ALLOW_PROTOCOL=ssh",
		"GIT_NO_REPLACE_OBJECTS=1",
	}
}

// pinRepoEnv pins git to the work tree: no discovery, so a broken .git cannot fall back to a parent repository.
func pinRepoEnv(repo string) []string {
	return []string{"GIT_DIR=" + filepath.Join(repo, ".git"), "GIT_WORK_TREE=" + repo, "GIT_CEILING_DIRECTORIES=" + filepath.Dir(repo)}
}

// git runs one hardened git command in dir. Its stderr is returned scrubbed.
func (w *worker) git(timeout time.Duration, dir string, extraEnv []string, args ...string) gitOut {
	ctx, cancel := context.WithTimeout(w.sig, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, gitBinary, append(append([]string{}, gitHardening...), args...)...)
	cmd.Dir = dir
	cmd.Env = append(w.gitEnv(), extraEnv...)
	var out capBuf
	var errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	res := gitOut{stdout: out.b, total: out.total, stderr: w.scrub.String(errb.String()), err: err}
	if err != nil {
		res.code = -1
		var ee *exec.ExitError
		if errors.As(err, &ee) && ctx.Err() == nil {
			res.code = ee.ExitCode()
		}
	}
	return res
}

// pinPrivateEnv pins git to the sc-worker-private GIT_DIR (plan 27 section 13.3, operator decision 2026-10-23):
// nothing the harness can write under the workspace .git is read, only its objects, through alternates.
func (pl *pipeline) pinPrivateEnv() []string {
	return []string{"GIT_DIR=" + pl.privGit(), "GIT_WORK_TREE=" + pl.repo, "GIT_CEILING_DIRECTORIES=" + filepath.Dir(pl.repo)}
}

func (pl *pipeline) privGit() string { return pl.w.privPath("git") }

// gitPost runs a git command against the private GIT_DIR, never trusting the commit-graph.
func (pl *pipeline) gitPost(timeout time.Duration, extraEnv []string, args ...string) gitOut {
	return pl.w.git(timeout, pl.repo, append(pl.pinPrivateEnv(), extraEnv...), append([]string{"-c", "core.commitGraph=false", "-c", "gc.auto=0", "-c", "maintenance.auto=false"}, args...)...)
}

// tail returns the last n bytes of s on a rune boundary.
func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	s = s[len(s)-n:]
	for len(s) > 0 && s[0]&0xC0 == 0x80 {
		s = s[1:]
	}
	return s
}

var gitSafeRe = regexp.MustCompile(`[\x00-\x08\x0b-\x1f\x7f]`)

func quoteConfig(s string) string {
	s = gitSafeRe.ReplaceAllString(strings.NewReplacer("\n", " ", "\r", " ").Replace(s), "")
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// setupCredentials is plan 27 section 14.3: the deploy key copy (0600), the global git config.
func (pl *pipeline) setupCredentials() *stageFail {
	w := pl.w
	sshDir := w.privPath("home", ".ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		return &stageFail{reason: "internal", detail: "private ssh dir: " + err.Error()}
	}
	key := w.readSecret("deploy-key")
	if !strings.HasSuffix(key, "\n") {
		key += "\n"
	}
	pl.keyPath = filepath.Join(sshDir, "id_ed25519")
	_ = os.Remove(pl.keyPath)
	if err := os.WriteFile(pl.keyPath, []byte(key), 0o600); err != nil {
		return &stageFail{reason: "internal", detail: "deploy key copy: " + err.Error()}
	}
	_ = os.Chmod(pl.keyPath, 0o600)
	c := pl.p.Git.Commit
	cfg := "[user]\n\tname = " + quoteConfig(c.AuthorName) + "\n\temail = " + quoteConfig(c.AuthorEmail) + "\n" +
		"[init]\n\tdefaultBranch = main\n" +
		"[advice]\n\tdetachedHead = false\n\tstatusHints = false\n\tpushUpdateRejected = false\n\tpushNonFastForward = false\n" +
		"\tpushFetchFirst = false\n\tskippedCherryPicks = false\n\tgraftFileDeprecated = false\n" +
		"[safe]\n\tdirectory = " + quoteConfig(pl.repo) + "\n"
	if err := os.WriteFile(w.privPath("gitconfig"), []byte(cfg), 0o600); err != nil {
		return &stageFail{reason: "internal", detail: "gitconfig: " + err.Error()}
	}
	return nil
}

var hostKeyRe = regexp.MustCompile(`(?i)host key verification failed|remote host identification has changed`)

// clone is plan 27 section 14.4.
func (pl *pipeline) clone() *stageFail {
	w := pl.w
	g := pl.p.Git
	if r := w.git(30*time.Second, w.cfg.WorkspaceDir, nil, "init", "-q", pl.repo); r.err != nil {
		return &stageFail{reason: "clone_failed", detail: "git init: " + tail(r.stderr, 1024)}
	}
	if r := w.git(30*time.Second, pl.repo, pinRepoEnv(pl.repo), "remote", "add", "origin", g.CloneURL); r.err != nil {
		return &stageFail{reason: "clone_failed", detail: "git remote add: " + tail(r.stderr, 1024)}
	}
	spec := "+" + g.FetchRef + ":refs/remotes/origin/" + strings.TrimPrefix(g.FetchRef, "refs/heads/")
	var last gitOut
	for attempt := 0; attempt < 2; attempt++ {
		last = w.git(300*time.Second, pl.repo, pinRepoEnv(pl.repo), "fetch", "-q", "--no-tags", "--prune", "origin", spec)
		if last.err == nil {
			return nil
		}
		if hostKeyRe.MatchString(last.stderr) {
			return &stageFail{reason: "clone_failed", detail: "host_key_mismatch"}
		}
		if w.sig.Err() != nil {
			return &stageFail{reason: "cancelled", detail: "sigterm"}
		}
		if attempt == 0 {
			_ = w.clk.Sleep(w.sig, 5*time.Second)
		}
	}
	return &stageFail{reason: "clone_failed", detail: tail(last.stderr, 1024)}
}

// remoteHead returns the remote head of ref (empty when absent) via git ls-remote, with one retry.
func (pl *pipeline) remoteHead(ref string) (string, error) {
	w := pl.w
	var last gitOut
	for attempt := 0; attempt < 2; attempt++ {
		if pl.priv {
			last = pl.gitPost(60*time.Second, nil, "ls-remote", "origin", ref)
		} else {
			last = w.git(60*time.Second, pl.repo, pinRepoEnv(pl.repo), "ls-remote", "origin", ref)
		}
		if last.err == nil {
			for _, l := range strings.Split(last.text(), "\n") {
				f := strings.Fields(l)
				if len(f) == 2 && f[1] == ref {
					return f[0], nil
				}
			}
			return "", nil
		}
		if attempt == 0 && !hostKeyRe.MatchString(last.stderr) {
			_ = w.clk.Sleep(w.sig, 2*time.Second)
		}
	}
	return "", errors.New(tail(last.stderr, 1024))
}

// checkout is plan 27 section 14.5.
func (pl *pipeline) checkout() *stageFail {
	w := pl.w
	g := pl.p.Git
	for _, c := range []string{g.StartCommit, g.BaseCommit} {
		if r := w.git(30*time.Second, pl.repo, pinRepoEnv(pl.repo), "cat-file", "-e", c+"^{commit}"); r.err != nil {
			return &stageFail{reason: "checkout_mismatch", detail: "commit not in the fetched history: " + c}
		}
	}
	if r := w.git(30*time.Second, pl.repo, pinRepoEnv(pl.repo), "merge-base", "--is-ancestor", g.BaseCommit, g.StartCommit); r.err != nil {
		return &stageFail{reason: "checkout_mismatch", detail: "base_commit is not an ancestor of start_commit"}
	}
	ref := "refs/heads/" + g.Branch
	switch g.BranchMode {
	case "create":
		head, err := pl.remoteHead(ref)
		if err != nil {
			return &stageFail{reason: "clone_failed", detail: "ls-remote: " + err.Error()}
		}
		if head != "" {
			return &stageFail{reason: "branch_exists", detail: g.Branch + " exists at " + head}
		}
		pl.lease = "--force-with-lease=" + ref + ":"
	case "continue":
		head, err := pl.remoteHead(ref)
		if err != nil {
			return &stageFail{reason: "clone_failed", detail: "ls-remote: " + err.Error()}
		}
		if head != g.StartCommit {
			if head == "" {
				head = "absent"
			}
			return &stageFail{reason: "branch_moved", detail: g.Branch + " is " + head + ", want " + g.StartCommit}
		}
		pl.lease = "--force-with-lease=" + ref + ":" + g.StartCommit
	}
	var r gitOut
	if g.BranchMode == "readonly" {
		r = w.git(60*time.Second, pl.repo, pinRepoEnv(pl.repo), "switch", "-q", "--detach", g.StartCommit)
	} else {
		r = w.git(60*time.Second, pl.repo, pinRepoEnv(pl.repo), "switch", "-q", "-c", g.Branch, g.StartCommit)
	}
	if r.err != nil {
		return &stageFail{reason: "checkout_mismatch", detail: "switch: " + tail(r.stderr, 1024)}
	}
	if r := w.git(30*time.Second, pl.repo, pinRepoEnv(pl.repo), "rev-parse", "HEAD"); r.err != nil || r.text() != g.StartCommit {
		return &stageFail{reason: "checkout_mismatch", detail: "HEAD is not start_commit after checkout"}
	}
	if err := pl.snapshotInternals(); err != nil {
		return &stageFail{reason: "internal", detail: "git internals snapshot: " + err.Error()}
	}
	if err := pl.setupPrivateGit(); err != nil {
		return &stageFail{reason: "internal", detail: "private git dir: " + err.Error()}
	}
	return nil
}

// setupPrivateGit builds the sc-worker-private GIT_DIR: its own config (written here, nothing from the
// workspace), a detached HEAD and an index at start_commit, empty hooks/ and info/, and an object directory whose
// alternates file points at the workspace clone's objects. New objects are written privately.
func (pl *pipeline) setupPrivateGit() error {
	gd := pl.privGit()
	if err := os.RemoveAll(gd); err != nil {
		return err
	}
	for _, d := range []string{"hooks", "info", "refs/heads", "refs/tags", "objects/info", "objects/pack"} {
		if err := os.MkdirAll(filepath.Join(gd, d), 0o700); err != nil {
			return err
		}
	}
	g := pl.p.Git
	cfg := "[core]\n\trepositoryformatversion = 0\n\tfilemode = true\n\tbare = false\n\tlogallrefupdates = false\n" +
		"[remote \"origin\"]\n\turl = " + quoteConfig(g.CloneURL) + "\n"
	files := map[string]string{
		"config":                  cfg,
		"HEAD":                    g.StartCommit + "\n",
		"objects/info/alternates": filepath.Join(pl.repo, ".git", "objects") + "\n",
	}
	for n, c := range files {
		if err := os.WriteFile(filepath.Join(gd, n), []byte(c), 0o600); err != nil {
			return err
		}
	}
	if r := pl.gitPost(60*time.Second, nil, "read-tree", g.StartCommit); r.err != nil {
		return errors.New("read-tree: " + tail(r.stderr, 300))
	}
	pl.priv = true
	return nil
}

var shaRe = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?\n?$`)

// readRegular reads a small regular file below base without following a symlink at any component.
func readRegular(base, rel string, limit int64) ([]byte, bool) {
	cur := base
	parts := strings.Split(rel, "/")
	for i, part := range parts {
		if part == "" || part == "." || part == ".." {
			return nil, false
		}
		cur = filepath.Join(cur, part)
		st, err := os.Lstat(cur)
		if err != nil {
			return nil, false
		}
		if i < len(parts)-1 {
			if !st.IsDir() {
				return nil, false
			}
		} else if !st.Mode().IsRegular() || st.Size() > limit {
			return nil, false
		}
	}
	f, err := os.Open(cur)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	b := make([]byte, limit+1)
	n, _ := f.Read(b)
	if int64(n) > limit {
		return nil, false
	}
	return b[:n], true
}

// packedRef looks ref up in the workspace packed-refs file; "" when absent or corrupt.
func packedRef(gd, ref string) string {
	b, ok := readRegular(gd, "packed-refs", 1<<20)
	if !ok {
		return ""
	}
	for _, l := range strings.Split(string(b), "\n") {
		if l == "" || l[0] == '#' || l[0] == '^' {
			continue
		}
		if f := strings.Fields(l); len(f) == 2 && f[1] == ref {
			return f[0]
		}
	}
	return ""
}

// harnessHead returns the commit the harness left on the workspace branch, read as a plain file (the workspace
// refs are not trusted beyond that); "" when it is missing or corrupt.
func (pl *pipeline) harnessHead() string {
	gd := filepath.Join(pl.repo, ".git")
	b, ok := readRegular(gd, "HEAD", 4096)
	if !ok {
		return ""
	}
	h := strings.TrimSpace(string(b))
	if strings.HasPrefix(h, "ref: ") {
		ref := strings.TrimPrefix(h, "ref: ")
		if !strings.HasPrefix(ref, "refs/") {
			return ""
		}
		if b, ok = readRegular(gd, ref, 256); ok {
			h = strings.TrimSpace(string(b))
		} else {
			h = packedRef(gd, ref)
		}
	}
	if !shaRe.MatchString(h) {
		return ""
	}
	if r := pl.gitPost(30*time.Second, nil, "cat-file", "-e", h+"^{commit}"); r.err != nil {
		return ""
	}
	return h
}

// ---------------------------------------------------------------------------------------------------------------
// git internals snapshot and restore (plan 27 section 13.3)

// redirectFiles are repository-redirecting files removed after the harness when they did not exist before it.
var redirectFiles = []string{"commondir", "gitdir", "info/grafts", "shallow", "objects/info/alternates"}

var headRe = regexp.MustCompile(`^(ref: refs/[^\x00-\x20]+|[0-9a-f]{40}|[0-9a-f]{64})\n?$`)

var snapFiles = []struct{ rel, name string }{
	{"config", "config"}, {filepath.Join("info", "attributes"), "info-attributes"}, {filepath.Join("info", "exclude"), "info-exclude"},
}

func (pl *pipeline) snapDir() string { return pl.w.privPath("gitsnap") }

func (pl *pipeline) snapshotInternals() error {
	dir := pl.snapDir()
	_ = os.RemoveAll(dir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	gd := filepath.Join(pl.repo, ".git")
	for _, f := range snapFiles {
		b, err := os.ReadFile(filepath.Join(gd, f.rel))
		if errors.Is(err, os.ErrNotExist) {
			if err := os.WriteFile(filepath.Join(dir, f.name+".absent"), nil, 0o600); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, f.name), b, 0o600); err != nil {
			return err
		}
	}
	for _, n := range redirectFiles {
		if _, err := os.Lstat(filepath.Join(gd, n)); err == nil {
			if err := os.WriteFile(filepath.Join(dir, "present-"+strings.ReplaceAll(n, "/", "_")), nil, 0o600); err != nil {
				return err
			}
		}
	}
	var hooks []string
	if ents, err := os.ReadDir(filepath.Join(gd, "hooks")); err == nil {
		for _, e := range ents {
			hooks = append(hooks, e.Name())
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "hooks.list"), []byte(strings.Join(hooks, "\n")), 0o600); err != nil {
		return err
	}
	if _, err := os.Lstat(filepath.Join(gd, "objects", "info", "alternates")); err == nil {
		return os.WriteFile(filepath.Join(dir, "alternates.present"), nil, 0o600)
	}
	return nil
}

func isRealDir(p string) bool {
	st, err := os.Lstat(p)
	return err == nil && st.IsDir()
}

// restoreInternals restores the snapshot before any post-harness git command and logs git_internals_restored
// when anything differed. A .git (or its hooks / objects/info) that is not a real directory is tampering.
func (pl *pipeline) restoreInternals() *stageFail {
	if pl.restored {
		return nil
	}
	w := pl.w
	gd := filepath.Join(pl.repo, ".git")
	for _, d := range []string{gd, filepath.Join(gd, "hooks"), filepath.Join(gd, "objects", "info")} {
		if st, err := os.Lstat(d); err == nil && !st.IsDir() {
			return &stageFail{reason: "internal", detail: "workspace_tampered"}
		} else if err != nil && d == gd {
			return &stageFail{reason: "internal", detail: "workspace_tampered"}
		}
	}
	if st, err := os.Lstat(filepath.Join(gd, "HEAD")); err != nil || !st.Mode().IsRegular() {
		return &stageFail{reason: "internal", detail: "workspace_tampered"}
	}
	if b, err := os.ReadFile(filepath.Join(gd, "HEAD")); err != nil || len(b) > 4096 || !headRe.Match(b) {
		return &stageFail{reason: "internal", detail: "workspace_tampered"}
	}
	dir := pl.snapDir()
	var differed []string
	for _, n := range redirectFiles {
		if n == "objects/info/alternates" {
			continue // handled below
		}
		if _, err := os.Lstat(filepath.Join(dir, "present-"+strings.ReplaceAll(n, "/", "_"))); err == nil {
			continue
		}
		if _, err := os.Lstat(filepath.Join(gd, n)); err == nil {
			_ = os.RemoveAll(filepath.Join(gd, n))
			differed = append(differed, n)
		}
	}
	for _, f := range snapFiles {
		target := filepath.Join(gd, f.rel)
		want, werr := os.ReadFile(filepath.Join(dir, f.name))
		_, absentErr := os.Lstat(filepath.Join(dir, f.name+".absent"))
		wantAbsent := werr != nil && absentErr == nil
		st, lerr := os.Lstat(target)
		switch {
		case wantAbsent:
			if lerr == nil {
				_ = os.RemoveAll(target)
				differed = append(differed, f.rel)
			}
		case werr != nil:
			return &stageFail{reason: "internal", detail: "git internals snapshot unreadable"}
		default:
			same := false
			if lerr == nil && st.Mode().IsRegular() {
				if cur, err := os.ReadFile(target); err == nil && bytes.Equal(cur, want) {
					same = true
				}
			}
			if !same {
				_ = os.RemoveAll(target)
				if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
					return &stageFail{reason: "internal", detail: "restore " + f.rel + ": " + err.Error()}
				}
				if err := os.WriteFile(target, want, 0o644); err != nil {
					return &stageFail{reason: "internal", detail: "restore " + f.rel + ": " + err.Error()}
				}
				differed = append(differed, f.rel)
			}
		}
	}
	known := map[string]bool{}
	if b, err := os.ReadFile(filepath.Join(dir, "hooks.list")); err == nil {
		for _, n := range strings.Split(string(b), "\n") {
			known[n] = true
		}
	}
	if ents, err := os.ReadDir(filepath.Join(gd, "hooks")); err == nil {
		for _, e := range ents {
			if !known[e.Name()] {
				_ = os.RemoveAll(filepath.Join(gd, "hooks", e.Name()))
				differed = append(differed, filepath.Join("hooks", e.Name()))
			}
		}
	}
	if _, err := os.Lstat(filepath.Join(dir, "alternates.present")); err != nil {
		alt := filepath.Join(gd, "objects", "info", "alternates")
		if _, err := os.Lstat(alt); err == nil {
			_ = os.RemoveAll(alt)
			differed = append(differed, filepath.Join("objects", "info", "alternates"))
		}
	}
	for _, n := range []string{"commit-graph", "commit-graphs"} {
		p := filepath.Join(gd, "objects", "info", n)
		if _, err := os.Lstat(p); err == nil {
			_ = os.RemoveAll(p)
			differed = append(differed, filepath.Join("objects", "info", n))
		}
	}
	pl.restored = true
	if len(differed) > 0 {
		sort.Strings(differed)
		w.log("warn", "git_internals_restored", map[string]any{"files": differed})
	}
	return nil
}
