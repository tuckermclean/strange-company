package verify

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Git derivation (plan 22 section 14.11). Every diff-based control uses facts the Runner computes from Git itself.
// Git runs through os/exec with a from-scratch environment and a pinned configuration (no system or user
// configuration, no repository configuration that changes what a diff shows: renames, external diff, textconv,
// ignored submodules, quoting, colour, replace refs, attributes), and every revision is a 40 hex object id, so a
// revision can never be an option, a range, a ref or a pathspec (22-R-3: fail closed).

// Error returns "<code>: <detail>".
func (e *DiffError) Error() string {
	if e == nil {
		return "DIFF_UNVERIFIABLE"
	}
	if e.Detail == "" {
		return e.Code
	}
	return e.Code + ": " + e.Detail
}

const (
	codeDiffTooLarge    = "DIFF_TOO_LARGE"
	codeDiffUnverifable = "DIFF_UNVERIFIABLE"
)

func unverifiable(format string, args ...any) error {
	return &DiffError{Code: codeDiffUnverifable, Detail: fmt.Sprintf(format, args...)}
}

var (
	objectIDRE = regexp.MustCompile(`^[0-9a-f]{40}$`)
	rawModeRE  = regexp.MustCompile(`^[0-7]{6}$`)
	zeroObject = strings.Repeat("0", 40)
)

// checkRevision accepts only a full lowercase object id that is not the all-zero id.
func checkRevision(rev string) error {
	if !objectIDRE.MatchString(rev) || rev == zeroObject {
		return unverifiable("revision %q is not a 40 hex object id", rev)
	}
	return nil
}

// hardenedConfig is passed to every git invocation as `-c key=value` pairs.
var hardenedConfig = []string{
	"core.quotePath=false", "core.fsmonitor=false", "core.hooksPath=/dev/null", "core.attributesFile=/dev/null",
	"core.excludesFile=/dev/null", "protocol.allow=never", "core.autocrlf=false", "core.pager=cat", "diff.renames=false", "diff.ignoreSubmodules=none",
	"diff.noprefix=false", "diff.mnemonicPrefix=false", "diff.relative=false", "diff.suppressBlankEmpty=false",
	"color.ui=never", "color.diff=never", "status.submoduleSummary=false", "submodule.recurse=false",
}

// gitEnv is the complete environment of a git child: nothing is inherited except the lookup path of the binary.
func gitEnv(gitDir string) []string {
	return []string{
		"PATH=" + gitDir + ":/usr/bin:/bin",
		"HOME=/nonexistent", "XDG_CONFIG_HOME=/nonexistent",
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_ATTR_NOSYSTEM=1",
		"GIT_TERMINAL_PROMPT=0", "GIT_GRAFT_FILE=/dev/null", "GIT_NO_LAZY_FETCH=1", "GIT_OPTIONAL_LOCKS=0", "GIT_PAGER=cat", "GIT_ASKPASS=/bin/false",
		"LC_ALL=C", "LANG=C", "LANGUAGE=C",
	}
}

type gitResult struct {
	out  []byte
	exit int
}

// runGit runs `git -C Dir <flags> args...`. A non-zero exit status is returned in the result with a nil error only
// when the caller allowed it; otherwise it is an error carrying git's stderr.
func runGit(g GitRepo, allowExit1 bool, args ...string) (gitResult, error) {
	if g.Dir == "" {
		return gitResult{}, unverifiable("no repository directory")
	}
	bin, err := exec.LookPath("git")
	if err != nil {
		return gitResult{}, unverifiable("git is not available: %v", err)
	}
	argv := []string{"-C", g.Dir, "--no-pager", "--no-replace-objects", "--literal-pathspecs", "--no-optional-locks"}
	for _, c := range hardenedConfig {
		argv = append(argv, "-c", c)
	}
	argv = append(argv, args...)
	cmd := exec.Command(bin, argv...)
	cmd.Env = gitEnv(dirOf(bin))
	cmd.Stdin = nil
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err = cmd.Run()
	if err == nil {
		return gitResult{out: stdout.Bytes()}, nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) && allowExit1 && ee.ExitCode() == 1 {
		return gitResult{out: stdout.Bytes(), exit: 1}, nil
	}
	msg := strings.TrimSpace(stderr.String())
	if len(msg) > 300 {
		msg = msg[:300]
	}
	return gitResult{}, unverifiable("git %s failed: %v: %s", args[0], err, msg)
}

func dirOf(p string) string {
	if i := strings.LastIndexByte(p, '/'); i > 0 {
		return p[:i]
	}
	return "/usr/bin"
}

// DeriveDiff computes the changed paths of a..b from Git itself (plan 22 section 14.11 step 2): the raw tree
// difference without rename or copy detection, so that a rename is a delete plus an add, and mode and type changes
// and gitlinks are entries. The whole list is read and sorted by path; a list longer than maxFiles is
// DIFF_TOO_LARGE and no entries are returned.
func DeriveDiff(g GitRepo, a, b string, maxFiles int) ([]DiffEntry, error) {
	if maxFiles < 0 {
		return nil, unverifiable("negative maxFiles %d", maxFiles)
	}
	if err := checkRevision(a); err != nil {
		return nil, err
	}
	if err := checkRevision(b); err != nil {
		return nil, err
	}
	res, err := runGit(g, false, "diff", "--raw", "-z", "--no-renames", "--no-ext-diff", "--no-textconv", "--ignore-submodules=none",
		"--no-color", "--no-abbrev", a, b, "--")
	if err != nil {
		return nil, err
	}
	entries, err := parseRaw(res.out)
	if err != nil {
		return nil, err
	}
	if len(entries) > maxFiles {
		return nil, &DiffError{Code: codeDiffTooLarge, Detail: fmt.Sprintf("%d changed paths exceed the limit of %d", len(entries), maxFiles)}
	}
	return entries, nil
}

// parseRaw parses `git diff --raw -z --no-renames` output: ":<oldmode> <newmode> <oldblob> <newblob> <status>\0<path>\0".
func parseRaw(out []byte) ([]DiffEntry, error) {
	var entries []DiffEntry
	toks := strings.Split(string(out), "\x00")
	if n := len(toks); n > 0 && toks[n-1] == "" {
		toks = toks[:n-1]
	} else if n > 0 {
		return nil, unverifiable("raw diff output is not NUL terminated")
	}
	seen := map[string]bool{}
	for i := 0; i < len(toks); i += 2 {
		meta := toks[i]
		if i+1 >= len(toks) || !strings.HasPrefix(meta, ":") {
			return nil, unverifiable("malformed raw diff entry %q", meta)
		}
		f := strings.Split(meta[1:], " ")
		if len(f) != 5 || !rawModeRE.MatchString(f[0]) || !rawModeRE.MatchString(f[1]) || !objectIDRE.MatchString(f[2]) || !objectIDRE.MatchString(f[3]) {
			return nil, unverifiable("malformed raw diff entry %q", meta)
		}
		switch f[4] {
		case "A", "D", "M", "T":
		default:
			return nil, unverifiable("unexpected diff status %q", f[4])
		}
		p := toks[i+1]
		if p == "" || seen[p] {
			return nil, unverifiable("empty or repeated path in raw diff")
		}
		seen[p] = true
		entries = append(entries, DiffEntry{Path: p, Status: f[4], OldMode: f[0], NewMode: f[1], OldBlob: f[2], NewBlob: f[3]})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return entries, nil
}

var hunkRE = regexp.MustCompile(`^@@ -([0-9]+)(?:,([0-9]+))? \+([0-9]+)(?:,([0-9]+))? @@`)

// AddedLines returns the lines a..b adds to one path (plan 22 section 14.11 step 4), with their line numbers at b,
// from a zero-context diff read hunk by hunk: header-like content cannot be mistaken for a header. The path is
// literal (no glob or pathspec magic), attributes, textconv and external diff programs are off and the file is
// treated as text, so nothing hides an added line. Deleted, binary-identical and untouched paths have no lines.
func AddedLines(g GitRepo, a, b, path string) ([]AddedLine, error) {
	if err := checkRevision(a); err != nil {
		return nil, err
	}
	if err := checkRevision(b); err != nil {
		return nil, err
	}
	if path == "" || strings.ContainsRune(path, 0) {
		return nil, unverifiable("invalid path %q", path)
	}
	res, err := runGit(g, false, "diff", "-U0", "--no-color", "--no-ext-diff", "--no-textconv", "--text", "--no-renames",
		"--ignore-submodules=none", "--no-abbrev", a, b, "--", path)
	if err != nil {
		return nil, err
	}
	return parseAddedLines(string(res.out))
}

func parseAddedLines(out string) ([]AddedLine, error) {
	lines := strings.Split(out, "\n")
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	var added []AddedLine
	for i := 0; i < len(lines); {
		m := hunkRE.FindStringSubmatch(lines[i])
		if m == nil {
			i++
			continue
		}
		oldRem, newRem := atoiDefault(m[2], 1), atoiDefault(m[4], 1)
		next, err := strconv.Atoi(m[3])
		if err != nil {
			return nil, unverifiable("bad hunk header %q", lines[i])
		}
		i++
		for oldRem > 0 || newRem > 0 {
			if i >= len(lines) {
				return nil, unverifiable("diff hunk ends early")
			}
			l := lines[i]
			i++
			switch {
			case strings.HasPrefix(l, "\\"):
				continue // "\ No newline at end of file"
			case strings.HasPrefix(l, "-"):
				oldRem--
			case strings.HasPrefix(l, "+"):
				newRem--
				added = append(added, AddedLine{Line: next, Text: l[1:]})
				next++
			case strings.HasPrefix(l, " "):
				oldRem--
				newRem--
				next++
			default:
				return nil, unverifiable("unexpected line in a diff hunk: %q", l)
			}
			if oldRem < 0 || newRem < 0 {
				return nil, unverifiable("diff hunk longer than its header")
			}
		}
		for i < len(lines) && strings.HasPrefix(lines[i], "\\") {
			i++
		}
	}
	return added, nil
}

func atoiDefault(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return -1
	}
	return n
}

// CrossCheckCompare compares the Runner's own list with a Forgejo compare response (section 14.11 step 5). The
// response is only a cross-check: the set of names it lists (each file and, for a rename, its previous name) must
// equal the set of paths of the Runner's list, else DIFF_UNVERIFIABLE.
func CrossCheckCompare(entries []DiffEntry, compare []CompareFile) error {
	own := map[string]bool{}
	for _, e := range entries {
		own[e.Path] = true
	}
	theirs := map[string]bool{}
	for _, c := range compare {
		if c.Filename == "" {
			return unverifiable("compare response lists a file without a name")
		}
		theirs[c.Filename] = true
		if c.PreviousFilename != "" {
			theirs[c.PreviousFilename] = true
		}
	}
	var missing, extra []string
	for p := range own {
		if !theirs[p] {
			missing = append(missing, p)
		}
	}
	for p := range theirs {
		if !own[p] {
			extra = append(extra, p)
		}
	}
	if len(missing) == 0 && len(extra) == 0 {
		return nil
	}
	sort.Strings(missing)
	sort.Strings(extra)
	return unverifiable("the compare response disagrees with the Runner's own diff: %d path(s) missing from it %s, %d unexpected %s",
		len(missing), firstFew(missing), len(extra), firstFew(extra))
}

func firstFew(l []string) string {
	if len(l) > 3 {
		l = l[:3]
	}
	return fmt.Sprintf("%q", l)
}

// RevList lists the commits of a..b, oldest first, parents before children (section 14.11 step 3).
func RevList(g GitRepo, a, b string) ([]string, error) {
	if err := checkRevision(a); err != nil {
		return nil, err
	}
	if err := checkRevision(b); err != nil {
		return nil, err
	}
	res, err := runGit(g, false, "rev-list", "--topo-order", "--reverse", a+".."+b, "--")
	if err != nil {
		return nil, err
	}
	var out []string
	for _, l := range strings.Split(string(res.out), "\n") {
		if l == "" {
			continue
		}
		if !objectIDRE.MatchString(l) {
			return nil, unverifiable("unexpected rev-list line %q", l)
		}
		out = append(out, l)
	}
	return out, nil
}

// IsAncestor reports whether a is an ancestor of b (section 14.11 step 3). An unknown commit is an error.
func IsAncestor(g GitRepo, a, b string) (bool, error) {
	if err := checkRevision(a); err != nil {
		return false, err
	}
	if err := checkRevision(b); err != nil {
		return false, err
	}
	res, err := runGit(g, true, "merge-base", "--is-ancestor", a, b)
	if err != nil {
		return false, err
	}
	return res.exit == 0, nil
}

// LsTree lists every blob, symlink and gitlink of a revision (section 14.11 step 3), sorted by path.
func LsTree(g GitRepo, rev string) ([]TreeEntry, error) {
	if err := checkRevision(rev); err != nil {
		return nil, err
	}
	res, err := runGit(g, false, "ls-tree", "-r", "-z", "--full-tree", rev)
	if err != nil {
		return nil, err
	}
	var out []TreeEntry
	for _, rec := range strings.Split(string(res.out), "\x00") {
		if rec == "" {
			continue
		}
		tab := strings.IndexByte(rec, '\t')
		if tab < 0 {
			return nil, unverifiable("malformed ls-tree record %q", rec)
		}
		f := strings.Split(rec[:tab], " ")
		if len(f) != 3 || !rawModeRE.MatchString(f[0]) || !objectIDRE.MatchString(f[2]) {
			return nil, unverifiable("malformed ls-tree record %q", rec)
		}
		out = append(out, TreeEntry{Path: rec[tab+1:], Mode: f[0], Blob: f[2]})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// catBlob returns the content of a blob object.
func catBlob(g GitRepo, blob string) ([]byte, error) {
	if err := checkRevision(blob); err != nil {
		return nil, err
	}
	res, err := runGit(g, false, "cat-file", "blob", blob)
	if err != nil {
		return nil, err
	}
	return res.out, nil
}

// GitRepo is a local repository or bare mirror the Git derivation helper runs `git -C Dir` in.
type GitRepo struct {
	Dir string
}

// DiffEntry is one `git diff --raw -z --no-renames` entry. Status is A, D, M or T; modes are the six digit raw
// modes and blobs the 40 hex object ids, all zeros for an absent side.
type DiffEntry struct {
	Path    string
	Status  string
	OldMode string
	NewMode string
	OldBlob string
	NewBlob string
}

// DiffError carries DIFF_TOO_LARGE or DIFF_UNVERIFIABLE.
type DiffError struct {
	Code   string
	Detail string
}

// AddedLine is one added line of a path: its line number at the head and its text without the line feed.
type AddedLine struct {
	Line int
	Text string
}

// CompareFile is one file of a Forgejo compare response.
type CompareFile struct {
	Filename         string
	PreviousFilename string
}

// TreeEntry is one `git ls-tree -r -z` entry.
type TreeEntry struct {
	Path string
	Mode string
	Blob string
}
