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
	"syscall"
	"time"

	"strange.company/sc/internal/clock"
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

// transportGrace is how long a network git command may run without any child process (after it had one) before it is
// considered stuck on a transport that is gone: ssh exited, but a lingering descendant of it holds the pipe git reads,
// so git never sees the end of the stream (S4-NB-d). The wait is real time: the subject is a real process.
const transportGrace = 10 * time.Second

const transportPoll = 500 * time.Millisecond

// networkGit reports whether args (a git argument list, leading -c pairs included) is a command that talks to the
// remote through the ssh transport.
func networkGit(args []string) bool {
	for i := 0; i < len(args); i++ {
		if args[i] == "-c" {
			i++
			continue
		}
		switch args[i] {
		case "fetch", "push", "ls-remote", "clone":
			return true
		}
		return false
	}
	return false
}

// watchTransport polls the children of the git process pid until stop is closed. When git has had a child (its
// transport) and has had none for transportGrace, it kills git's process group and reports true.
func watchTransport(pid int, stop <-chan struct{}) bool {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-stop
		cancel()
	}()
	seen := false
	idle := time.Duration(0)
	for {
		if (clock.Real{}).Sleep(ctx, transportPoll) != nil {
			return false
		}
		live := false
		tab, _ := procTable("/proc")
		for _, p := range tab {
			if p.ppid != pid {
				continue
			}
			seen = true // an ended transport (a zombie git has not reaped) counts as a child git has had
			if !p.zombie {
				live = true
			}
		}
		switch {
		case live:
			idle = 0
		case seen:
			idle += transportPoll
			if idle >= transportGrace {
				_ = syscall.Kill(-pid, syscall.SIGKILL)
				return true
			}
		}
	}
}

// gitWaitDelay bounds how long Wait lingers for output pipes that a descendant of git still holds open.
const gitWaitDelay = 3 * time.Second

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
	return w.gitIn(timeout, dir, extraEnv, nil, args...)
}

// gitIn is git with the given standard input.
func (w *worker) gitIn(timeout time.Duration, dir string, extraEnv []string, stdin []byte, args ...string) gitOut {
	ctx, cancel := context.WithTimeout(w.sig, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, gitBinary, append(append([]string{}, gitHardening...), args...)...)
	cmd.Dir = dir
	cmd.Env = append(w.gitEnv(), extraEnv...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var out capBuf
	var errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	// git runs in a process group of its own: a timeout kills the whole group, and a background child that outlives git
	// (an ssh left holding the output pipes) can delay Wait by gitWaitDelay at most and is killed with the group (S4-NB-d).
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		return cmd.Process.Kill()
	}
	cmd.WaitDelay = gitWaitDelay
	var err error
	lingered := false
	if err = cmd.Start(); err == nil {
		stop := make(chan struct{})
		flag := make(chan bool, 1)
		if networkGit(args) {
			go func() { flag <- watchTransport(cmd.Process.Pid, stop) }()
		} else {
			flag <- false
		}
		err = cmd.Wait()
		close(stop)
		lingered = <-flag
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	if errors.Is(err, exec.ErrWaitDelay) && cmd.ProcessState != nil && cmd.ProcessState.Success() {
		err = nil // git succeeded; only a lingering descendant kept the pipes open
	}
	msg := errb.String()
	if lingered {
		msg += "sc-worker: git was killed: its transport process had ended but a lingering process kept git waiting\n"
	}
	res := gitOut{stdout: out.b, total: out.total, stderr: w.scrub.String(msg), err: err}
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

// The helpers below resolve a path below the repository beneath the workspace directory ("repo/<rel>", no symlink
// followed at any component, repo itself included).
func (pl *pipeline) rReadFile(rel string, limit int64) ([]byte, error) {
	return readRegularFile(pl.w.cfg.WorkspaceDir, "repo/"+rel, limit)
}

func (pl *pipeline) rOpenDir(rel string) (*os.File, error) {
	return openDir(pl.w.cfg.WorkspaceDir, "repo/"+rel)
}

func (pl *pipeline) rRemove(rel string) (bool, error) {
	return removeRel(pl.w.cfg.WorkspaceDir, "repo/"+rel)
}

func (pl *pipeline) rMkdir(rel string, mode uint32) error {
	return mkdirRel(pl.w.cfg.WorkspaceDir, "repo/"+rel, mode)
}

func (pl *pipeline) rWriteReplace(rel string, data []byte, mode uint32) error {
	return writeRelReplace(pl.w.cfg.WorkspaceDir, "repo/"+rel, data, mode)
}

// recordRepo opens <workspace>/repo without following a link and remembers its (device, inode).
func (pl *pipeline) recordRepo() error {
	root, err := openRoot(pl.w.cfg.WorkspaceDir)
	if err != nil {
		return err
	}
	defer syscall.Close(root)
	fd, err := syscall.Openat(root, "repo", syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer syscall.Close(fd)
	var st syscall.Stat_t
	if err := syscall.Fstat(fd, &st); err != nil {
		return err
	}
	pl.repoDev, pl.repoIno, pl.repoRec = uint64(st.Dev), uint64(st.Ino), true
	return nil
}

// errWorkspaceTampered is returned by a post-harness git call when the workspace no longer is what sc-worker recorded.
var errWorkspaceTampered = errors.New("workspace_tampered")

// verifyRepo checks, without following a link, that <workspace>/repo is still the directory recorded at clone time
// and, once the harness has ended, that the workspace directory itself (inode and ctime) did not change since the
// first check: a rename pair of entries below it (the repo toggled away and back) moves its ctime.
func (pl *pipeline) verifyRepo() *stageFail {
	if !pl.repoRec {
		return nil
	}
	tampered := &stageFail{reason: "internal", detail: "workspace_tampered"}
	fail := func(err error) *stageFail {
		pl.tampered = true
		pl.refuse(pl.repo, err)
		return tampered
	}
	root, err := openRoot(pl.w.cfg.WorkspaceDir)
	if err != nil {
		return &stageFail{reason: "internal", detail: "workspace: " + err.Error()}
	}
	defer syscall.Close(root)
	var ws syscall.Stat_t
	if err := syscall.Fstat(root, &ws); err != nil {
		return &stageFail{reason: "internal", detail: "workspace: " + err.Error()}
	}
	if pl.harnessRan {
		ino, sec, nsec := uint64(ws.Ino), int64(ws.Ctim.Sec), int64(ws.Ctim.Nsec)
		if !pl.wsRec {
			pl.wsIno, pl.wsCtimSec, pl.wsCtimNsec, pl.wsRec = ino, sec, nsec, true
		} else if ino != pl.wsIno || sec != pl.wsCtimSec || nsec != pl.wsCtimNsec {
			return fail(errNotRegular)
		}
	}
	// openat with O_PATH|O_NOFOLLOW opens the entry itself, a link included; fstat then tells what it is.
	fd, err := syscall.Openat(root, "repo", oPath|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return fail(errNotRegular)
	}
	defer syscall.Close(fd)
	var st syscall.Stat_t
	if err := syscall.Fstat(fd, &st); err != nil {
		return fail(errNotRegular)
	}
	switch {
	case st.Mode&syscall.S_IFMT == syscall.S_IFLNK:
		return fail(errSymlink)
	case st.Mode&syscall.S_IFMT != syscall.S_IFDIR, uint64(st.Dev) != pl.repoDev, uint64(st.Ino) != pl.repoIno:
		return fail(errNotRegular)
	}
	return nil
}

func (pl *pipeline) privGit() string { return pl.w.privPath("git") }

// gitPost runs a git command against the private GIT_DIR, never trusting the commit-graph.
func (pl *pipeline) gitPost(timeout time.Duration, extraEnv []string, args ...string) gitOut {
	return pl.gitPostIn(timeout, extraEnv, nil, args...)
}

// postFlags keep the post-harness git commands from trusting the commit-graph, a multi-pack-index or reachability
// bitmaps of the shared (harness-writable) object store.
var postFlags = []string{
	"-c", "core.commitGraph=false", "-c", "core.multiPackIndex=false", "-c", "pack.useBitmaps=false",
	"-c", "gc.auto=0", "-c", "maintenance.auto=false",
}

func (pl *pipeline) gitPostIn(timeout time.Duration, extraEnv []string, stdin []byte, args ...string) gitOut {
	if pl.verifyRepo() != nil {
		return gitOut{err: errWorkspaceTampered, code: -1}
	}
	r := pl.w.gitIn(timeout, pl.repo, append(pl.pinPrivateEnv(), extraEnv...), stdin, append(append([]string{}, postFlags...), args...)...)
	if r.err != nil && pl.verifyRepo() != nil {
		// Whatever git said, a swapped workspace is what failed.
		return gitOut{err: errWorkspaceTampered, code: -1}
	}
	return r
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
	if err := pl.recordRepo(); err != nil {
		return &stageFail{reason: "internal", detail: "repo directory: " + err.Error()}
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
	// The commit and trees of start_commit are copied into the private object store, which git consults before the
	// alternates: an object the harness forges in the shared store cannot change what the diff is taken against.
	objs := pl.gitPost(120*time.Second, nil, "rev-list", "--objects", "--filter=blob:none", "-n1", g.StartCommit)
	if objs.err != nil {
		return errors.New("rev-list: " + tail(objs.stderr, 300))
	}
	if r := pl.gitPostIn(120*time.Second, nil, objs.stdout, "pack-objects", "-q", filepath.Join(gd, "objects", "pack", "pack")); r.err != nil {
		return errors.New("pack-objects: " + tail(r.stderr, 300))
	}
	pl.priv = true
	return nil
}

var shaRe = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?\n?$`)

// packedRef looks ref up in the content of the workspace packed-refs file; "" when absent or corrupt.
func packedRef(b []byte, ref string) string {
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

// readGit reads a small regular file of the workspace .git without following a link at any component.
func (pl *pipeline) readGit(rel string, limit int64) ([]byte, bool) {
	b, err := pl.rReadFile(".git/"+rel, limit)
	if err != nil {
		if isRefusal(err) {
			pl.refuse(filepath.Join(pl.repo, ".git", rel), err)
		}
		return nil, false
	}
	return b, true
}

// harnessHead returns the commit the harness left on the workspace branch, read as a plain file (the workspace
// refs are not trusted beyond that); "" when it is missing or corrupt.
func (pl *pipeline) harnessHead() string {
	b, ok := pl.readGit("HEAD", 4096)
	if !ok {
		return ""
	}
	h := strings.TrimSpace(string(b))
	if strings.HasPrefix(h, "ref: ") {
		ref := strings.TrimPrefix(h, "ref: ")
		if !strings.HasPrefix(ref, "refs/") {
			return ""
		}
		if b, ok = pl.readGit(ref, 256); ok {
			h = strings.TrimSpace(string(b))
		} else if pb, pok := pl.readGit("packed-refs", 1<<20); pok {
			h = packedRef(pb, ref)
		} else {
			h = ""
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

// restoreInternals restores the snapshot before any post-harness git command and logs git_internals_restored
// when anything differed. A .git (or hooks, info, objects, objects/info) that is not a real directory is tampering.
// Every read, write and delete below goes through safefs.go: nothing is followed through a link the harness planted.
func (pl *pipeline) restoreInternals() *stageFail {
	if f := pl.verifyRepo(); f != nil {
		return f
	}
	if pl.restored {
		return nil
	}
	w := pl.w
	tampered := &stageFail{reason: "internal", detail: "workspace_tampered"}
	for _, d := range []string{".git", ".git/hooks", ".git/info", ".git/objects", ".git/objects/info"} {
		f, err := pl.rOpenDir(d)
		switch {
		case err == nil:
			_ = f.Close()
		case errors.Is(err, os.ErrNotExist) && d != ".git":
		default:
			pl.refuse(filepath.Join(pl.repo, d), err)
			return tampered
		}
	}
	if b, ok := pl.readGit("HEAD", 4096); !ok || !headRe.Match(b) {
		return tampered
	}
	// guard turns a refused removal or write into workspace_tampered; other errors are tolerated as before.
	guard := func(rel string, err error) *stageFail {
		if err != nil && isRefusal(err) {
			pl.refuse(filepath.Join(pl.repo, rel), err)
			return tampered
		}
		return nil
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
		removed, err := pl.rRemove(".git/" + n)
		if f := guard(".git/"+n, err); f != nil {
			return f
		}
		if removed {
			differed = append(differed, n)
		}
	}
	for _, f := range snapFiles {
		rel := ".git/" + f.rel
		want, werr := os.ReadFile(filepath.Join(dir, f.name))
		_, absentErr := os.Lstat(filepath.Join(dir, f.name+".absent"))
		wantAbsent := werr != nil && absentErr == nil
		switch {
		case wantAbsent:
			removed, err := pl.rRemove(rel)
			if sf := guard(rel, err); sf != nil {
				return sf
			}
			if removed {
				differed = append(differed, f.rel)
			}
		case werr != nil:
			return &stageFail{reason: "internal", detail: "git internals snapshot unreadable"}
		default:
			cur, rerr := pl.rReadFile(rel, int64(len(want))+1)
			if rerr == nil && bytes.Equal(cur, want) {
				continue
			}
			if isRefusal(rerr) {
				pl.refuse(filepath.Join(pl.repo, rel), rerr)
			}
			if _, err := pl.rRemove(rel); err != nil {
				return &stageFail{reason: "internal", detail: "restore " + f.rel + ": " + err.Error()}
			}
			if strings.Contains(f.rel, "/") {
				if err := pl.rMkdir(rel, 0o755); err != nil && !errors.Is(err, syscall.EEXIST) {
					return &stageFail{reason: "internal", detail: "restore " + f.rel + ": " + err.Error()}
				}
			}
			if err := pl.rWriteReplace(rel, want, 0o644); err != nil {
				return &stageFail{reason: "internal", detail: "restore " + f.rel + ": " + err.Error()}
			}
			differed = append(differed, f.rel)
		}
	}
	known := map[string]bool{}
	if b, err := os.ReadFile(filepath.Join(dir, "hooks.list")); err == nil {
		for _, n := range strings.Split(string(b), "\n") {
			known[n] = true
		}
	}
	if hd, err := pl.rOpenDir(".git/hooks"); err == nil {
		names, _ := hd.Readdirnames(-1)
		_ = hd.Close()
		for _, n := range names {
			if !known[n] {
				if _, err := pl.rRemove(".git/hooks/" + n); err == nil {
					differed = append(differed, filepath.Join("hooks", n))
				}
			}
		}
	}
	if _, err := os.Lstat(filepath.Join(dir, "alternates.present")); err != nil {
		removed, err := pl.rRemove(".git/objects/info/alternates")
		if f := guard(".git/objects/info/alternates", err); f != nil {
			return f
		}
		if removed {
			differed = append(differed, filepath.Join("objects", "info", "alternates"))
		}
	}
	for _, n := range []string{"commit-graph", "commit-graphs"} {
		removed, err := pl.rRemove(".git/objects/info/" + n)
		if f := guard(".git/objects/info/"+n, err); f != nil {
			return f
		}
		if removed {
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
