package verify

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"strange.company/sc/internal/policy"
)

// Slice S3 (ingestion) helpers. Every Git fact the tests assert is derived by running the real `git` binary on a
// scratch repository built in t.TempDir(); nothing is mocked and no expected value is a hand-written diff.
//
// Interface choices of S3 (plan 22 sections 14.6 and 14.11 are silent on the Go signatures):
//   - gitdiff.go: GitRepo{Dir} (a worktree or a bare mirror, `git -C Dir`), DeriveDiff(g, a, b, maxFiles),
//     AddedLines, CrossCheckCompare, RevList, IsAncestor, LsTree. Modes are the six digit raw modes and blobs the
//     40 hex ids of `git diff --raw`, all zeros for an absent side. DIFF_TOO_LARGE and DIFF_UNVERIFIABLE are
//     *DiffError values (errors.As) with Code set.
//   - ingest.go: CheckIngestion(IngestionInput) ([]Violation, error); the violations are sorted by (Path, Code,
//     SubCode, Line); DIFF_TOO_LARGE is an error and no violation list.
//   - ingest.go also holds DeriveDiscoveryProtection(DiscoveryInput) (DiscoveryProtection, error).

const s3ZeroBlob = "0000000000000000000000000000000000000000"

// s3Env pins the process environment so that the library's own `git` calls and the test's are deterministic and
// independent of the machine (no system or user configuration, fixed identity and dates).
func s3Env(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	for k, v := range map[string]string{
		"HOME": home, "XDG_CONFIG_HOME": home, "GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": "/dev/null",
		"GIT_AUTHOR_NAME": "Test Author", "GIT_AUTHOR_EMAIL": "author@example.invalid",
		"GIT_COMMITTER_NAME": "Test Committer", "GIT_COMMITTER_EMAIL": "committer@example.invalid",
		"GIT_AUTHOR_DATE": "2026-01-01T00:00:00Z", "GIT_COMMITTER_DATE": "2026-01-01T00:00:00Z",
		"LC_ALL": "C", "GIT_TERMINAL_PROMPT": "0",
	} {
		t.Setenv(k, v)
	}
	for _, k := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_EXTERNAL_DIFF", "GIT_CONFIG", "GIT_PAGER", "PAGER", "GIT_ATTR_SOURCE"} {
		t.Setenv(k, "x")
		if err := os.Unsetenv(k); err != nil {
			t.Fatal(err)
		}
	}
}

// scratch is a small real Git repository.
type scratch struct {
	t        *testing.T
	dir      string
	gitlinks map[string]string
}

func newScratch(t *testing.T) *scratch {
	t.Helper()
	s3Env(t)
	s := &scratch{t: t, dir: t.TempDir(), gitlinks: map[string]string{}}
	s.git("init", "-q", "-b", "main")
	s.git("config", "user.name", "Test Author")
	s.git("config", "user.email", "author@example.invalid")
	s.git("config", "commit.gpgsign", "false")
	s.git("config", "core.autocrlf", "false")
	return s
}

func (s *scratch) git(args ...string) string {
	s.t.Helper()
	cmd := exec.Command("git", append([]string{"-C", s.dir}, args...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		s.t.Fatalf("git %q: %v\n%s", args, err, stderr.String())
	}
	return strings.TrimRight(string(out), "\n")
}

func (s *scratch) path(p string) string { return filepath.Join(s.dir, p) }

func (s *scratch) write(p, content string) {
	s.t.Helper()
	s.writeBytes(p, []byte(content))
}

func (s *scratch) writeBytes(p string, content []byte) {
	s.t.Helper()
	full := s.path(p)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		s.t.Fatal(err)
	}
	_ = os.Remove(full)
	if err := os.WriteFile(full, content, 0o644); err != nil {
		s.t.Fatal(err)
	}
}

func (s *scratch) remove(p string) {
	s.t.Helper()
	if err := os.RemoveAll(s.path(p)); err != nil {
		s.t.Fatal(err)
	}
}

func (s *scratch) rename(from, to string) {
	s.t.Helper()
	if err := os.MkdirAll(filepath.Dir(s.path(to)), 0o755); err != nil {
		s.t.Fatal(err)
	}
	if err := os.Rename(s.path(from), s.path(to)); err != nil {
		s.t.Fatal(err)
	}
}

func (s *scratch) copyFile(from, to string) {
	s.t.Helper()
	b, err := os.ReadFile(s.path(from))
	if err != nil {
		s.t.Fatal(err)
	}
	s.writeBytes(to, b)
}

func (s *scratch) chmod(p string, mode os.FileMode) {
	s.t.Helper()
	if err := os.Chmod(s.path(p), mode); err != nil {
		s.t.Fatal(err)
	}
}

func (s *scratch) symlink(p, target string) {
	s.t.Helper()
	if err := os.MkdirAll(filepath.Dir(s.path(p)), 0o755); err != nil {
		s.t.Fatal(err)
	}
	_ = os.Remove(s.path(p))
	if err := os.Symlink(target, s.path(p)); err != nil {
		s.t.Fatal(err)
	}
}

// setGitlink makes path a gitlink (mode 160000) at the next commit; a gitlink has no worktree content, so the
// scratch re-applies it after `git add -A` on every commit until dropGitlink.
func (s *scratch) setGitlink(p, sha string) {
	s.t.Helper()
	s.remove(p)
	s.gitlinks[p] = sha
}

func (s *scratch) dropGitlink(p string) {
	s.t.Helper()
	delete(s.gitlinks, p)
	s.git("rm", "-q", "--cached", "--ignore-unmatch", "--", p)
}

// commit stages the worktree and commits; it returns the new commit id.
func (s *scratch) commit(msg string) string {
	s.t.Helper()
	s.git("add", "-A")
	var paths []string
	for p := range s.gitlinks {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		s.git("update-index", "--add", "--cacheinfo", "160000,"+s.gitlinks[p]+","+p)
	}
	s.git("commit", "-q", "--allow-empty", "--no-verify", "--no-gpg-sign", "-m", msg)
	return s.git("rev-parse", "HEAD")
}

func (s *scratch) repo() GitRepo { return GitRepo{Dir: s.dir} }

// blobAt is `git rev-parse <rev>:<path>`, the zero id when the path is absent.
func (s *scratch) blobAt(rev, p string) string {
	s.t.Helper()
	cmd := exec.Command("git", "-C", s.dir, "rev-parse", "--verify", "-q", rev+":"+p)
	out, err := cmd.Output()
	if err != nil {
		return s3ZeroBlob
	}
	return strings.TrimSpace(string(out))
}

// modeAt is the raw mode of a path at a revision from `git ls-tree -z`, "000000" when absent.
func (s *scratch) modeAt(rev, p string) string {
	s.t.Helper()
	out := s.git("ls-tree", "-z", rev, "--", ":(literal)"+p)
	if out == "" {
		return "000000"
	}
	return strings.Fields(out)[0]
}

// s3Gitlink is an arbitrary commit id used as a gitlink target (a submodule commit that is not in this repository).
const s3Gitlink = "0123456789abcdef0123456789abcdef01234567"

// s3RepoPolicy is the repository policy the ingestion tests merge with the real platform policy. Test paths cover
// Go, Python, JavaScript, TypeScript and Rust layouts; nothing under mk/ or scripts/ is protected so that the
// discovery tests show the discovery protection alone.
func s3RepoPolicy(extraProtected ...string) map[string]any {
	protected := make([]any, len(extraProtected))
	for i, p := range extraProtected {
		protected[i] = p
	}
	spec := map[string]any{
		"testPaths":      []any{"**/*_test.go", "tests/**", "**/test_*.py", "**/*.test.js", "**/*.test.ts", "**/__mocks__/**", "**/__tests__/**"},
		"fixturePaths":   []any{"**/testdata/**"},
		"testInfraPaths": []any{"internal/testutil/**"},
	}
	if len(protected) > 0 {
		spec["protectedPaths"] = protected
	}
	return map[string]any{
		"apiVersion": "strange.company/v1alpha1", "kind": "RepositoryPolicy",
		"metadata": map[string]any{"name": "demo"}, "spec": spec,
	}
}

var (
	s3EffMu    sync.Mutex
	s3EffCache = map[string]s3EffEntry{}
)

type s3EffEntry struct {
	plat policy.Platform
	eff  policy.Effective
}

// s3Effective merges the test repository policy with the real platform policy. The result is cached per set of extra
// protected paths (merging validates the schema and costs ~0.5 s); the library must not modify its inputs.
func s3Effective(t *testing.T, extraProtected ...string) (policy.Platform, policy.Effective) {
	t.Helper()
	key := strings.Join(extraProtected, "\x00")
	s3EffMu.Lock()
	defer s3EffMu.Unlock()
	if e, ok := s3EffCache[key]; ok {
		return e.plat, e.eff
	}
	plat := platformPolicy(t)
	eff, issues := policy.MergePolicy(policy.Options{ContractsDir: filepath.Join(repoRoot(t), "contracts")}, plat, s3RepoPolicy(extraProtected...))
	if len(issues) != 0 {
		t.Fatalf("test repository policy rejected: %v", issues)
	}
	s3EffCache[key] = s3EffEntry{plat, eff}
	return plat, eff
}

// s3Manifest is a manifest naming the given paths as protected test files at the red commit. Blob ids come from
// `git rev-parse`, and TestCommit is the red commit.
func (s *scratch) s3Manifest(red string, files ...string) Manifest {
	s.t.Helper()
	m := Manifest{SchemaVersion: "v1alpha1", Kind: "test", TaskID: "sc-9", Generation: 1, ACVersion: 1, Repository: "o/demo",
		BaseCommit: red, GenerationStartCommit: red, TestCommit: red}
	for _, p := range files {
		m.Files = append(m.Files, ManifestFile{Path: p, BlobSha: s.blobAt(red, p), Class: "test", Change: "added"})
	}
	return m
}

func (s *scratch) ingestionInput(t *testing.T, role, start, head string, man Manifest, extraProtected ...string) IngestionInput {
	t.Helper()
	plat, eff := s3Effective(t, extraProtected...)
	return IngestionInput{Role: role, Git: s.repo(), Start: start, Head: head, MaxFilesChanged: 2000, Manifest: man, Platform: plat, Effective: eff}
}

// violationKey is the comparable form of a violation: "<code>/<sub_code> <path>".
func violationKey(v Violation) string {
	k := v.Code
	if v.SubCode != "" {
		k += "/" + v.SubCode
	}
	return k + " " + v.Path
}

func violationKeys(vs []Violation) []string {
	out := make([]string, 0, len(vs))
	for _, v := range vs {
		out = append(out, violationKey(v))
	}
	sort.Strings(out)
	return out
}

func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func tamper(sub, path string) string { return "TEST_TAMPER/" + sub + " " + path }

func protectedKey(path string) string {
	return "PROTECTED_PATH_VIOLATION/protected_path_changed " + path
}
