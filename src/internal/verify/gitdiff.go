package verify

// SC-STUB(F06 22-AC-31)
func (e *DiffError) Error() string { return "" }

// DeriveDiff computes the changed paths of a..b from Git itself (plan 22 section 14.11 step 2).
//
// SC-STUB(F06 22-AC-31)
func DeriveDiff(g GitRepo, a, b string, maxFiles int) ([]DiffEntry, error) { return nil, nil }

// AddedLines returns the lines a..b adds to one path (plan 22 section 14.11 step 4).
//
// SC-STUB(F06 22-AC-8 22-AC-31)
func AddedLines(g GitRepo, a, b, path string) ([]AddedLine, error) { return nil, nil }

// CrossCheckCompare compares the Runner's own list with a Forgejo compare response (section 14.11 step 5).
//
// SC-STUB(F06 22-AC-31)
func CrossCheckCompare(entries []DiffEntry, compare []CompareFile) error { return nil }

// RevList lists the commits of a..b, oldest first (section 14.11 step 3).
//
// SC-STUB(F06 22-AC-31)
func RevList(g GitRepo, a, b string) ([]string, error) { return nil, nil }

// IsAncestor reports whether a is an ancestor of b (section 14.11 step 3).
//
// SC-STUB(F06 22-AC-31)
func IsAncestor(g GitRepo, a, b string) (bool, error) { return false, nil }

// LsTree lists every blob, symlink and gitlink of a revision (section 14.11 step 3).
//
// SC-STUB(F06 22-AC-31)
func LsTree(g GitRepo, rev string) ([]TreeEntry, error) { return nil, nil }

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
