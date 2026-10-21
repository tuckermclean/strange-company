//go:build linux

package verify

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// stubMarkerLine is the text every stub marker line carries; the replay export has no such line (TDD-B6).
const stubMarkerLine = "SC-STUB("

// snapEntry is one file of a commit tree held in memory.
type snapEntry struct {
	Path    string
	Mode    string // 100644, 100755 or 120000
	Content []byte
}

// snapshot is the tree of a commit read straight from Git's object store. It is taken before any code under test
// runs, so that nothing the workspace does afterwards (attributes, filter drivers, config) can change it.
type snapshot struct{ Entries []snapEntry }

const maxSnapshotBytes = 1 << 30

// snapshotCommit reads the tree of rev with plumbing only (ls-tree, cat-file --batch). Neither applies
// .gitattributes, filters, eol conversion or export-ignore, so the bytes are exactly the committed blobs.
func snapshotCommit(g GitRepo, rev string) (*snapshot, error) {
	if err := checkRevision(rev); err != nil {
		return nil, err
	}
	ls, err := runGit(g, false, "ls-tree", "-r", "-z", "--full-tree", rev+"^{tree}")
	if err != nil {
		return nil, err
	}
	type item struct{ path, mode, oid string }
	var items []item
	for _, rec := range bytes.Split(ls.out, []byte{0}) {
		if len(rec) == 0 {
			continue
		}
		meta, name, ok := bytes.Cut(rec, []byte{'\t'})
		f := strings.Fields(string(meta))
		if !ok || len(f) != 3 {
			return nil, fmt.Errorf("git ls-tree: malformed entry")
		}
		switch f[0] {
		case "100644", "100755", "120000":
			items = append(items, item{string(name), f[0], f[2]})
		case "160000": // a submodule has no content in the commit
		default:
			return nil, fmt.Errorf("git ls-tree: unsupported mode %s", f[0])
		}
	}
	snap := &snapshot{}
	if len(items) == 0 {
		return snap, nil
	}
	bin, err := exec.LookPath("git")
	if err != nil {
		return nil, fmt.Errorf("git is not available: %w", err)
	}
	argv := []string{"-C", g.Dir, "--no-pager", "--no-replace-objects", "--literal-pathspecs", "--no-optional-locks"}
	for _, c := range hardenedConfig {
		argv = append(argv, "-c", c)
	}
	argv = append(argv, "cat-file", "--batch")
	cmd := exec.Command(bin, argv...)
	cmd.Env = gitEnv(dirOf(bin))
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	go func() {
		var w bytes.Buffer
		for _, it := range items {
			w.WriteString(it.oid + "\n")
		}
		_, _ = in.Write(w.Bytes())
		_ = in.Close()
	}()
	br := bufio.NewReader(out)
	var total int64
	var rerr error
	for _, it := range items {
		line, err := br.ReadString('\n')
		if err != nil {
			rerr = err
			break
		}
		f := strings.Fields(line)
		if len(f) != 3 || f[0] != it.oid || f[1] != "blob" {
			rerr = fmt.Errorf("git cat-file: unexpected answer %q", strings.TrimSpace(line))
			break
		}
		n, err := strconv.ParseInt(f[2], 10, 64)
		total += n
		if err != nil || n < 0 || total > maxSnapshotBytes {
			rerr = fmt.Errorf("git cat-file: object size %q is invalid or the tree is too large", f[2])
			break
		}
		data := make([]byte, n+1)
		if _, err := io.ReadFull(br, data); err != nil {
			rerr = err
			break
		}
		snap.Entries = append(snap.Entries, snapEntry{Path: it.path, Mode: it.mode, Content: data[:n]})
	}
	if rerr != nil {
		_ = cmd.Process.Kill()
	}
	_, _ = io.Copy(io.Discard, out)
	werr := cmd.Wait()
	if rerr != nil {
		return nil, fmt.Errorf("git cat-file: %v: %s", rerr, strings.TrimSpace(stderr.String()))
	}
	if werr != nil {
		return nil, fmt.Errorf("git cat-file failed: %v: %s", werr, strings.TrimSpace(stderr.String()))
	}
	return snap, nil
}

// extract writes the snapshot into dir, which must not exist. No .git is created and no entry can leave dir: a
// path with .. or an absolute path is refused, and no entry is written below a symlink.
func (s *snapshot) extract(dir string) error {
	if err := os.Mkdir(dir, 0o755); err != nil {
		return err
	}
	for _, e := range s.Entries {
		name := path.Clean(e.Path)
		if name == "." || path.IsAbs(name) || name == ".." || strings.HasPrefix(name, "../") || name == ".git" || strings.HasPrefix(name, ".git/") {
			return fmt.Errorf("git tree: unsafe path %q", e.Path)
		}
		full := filepath.Join(dir, filepath.FromSlash(name))
		if err := noSymlinkAbove(dir, filepath.Dir(full)); err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return err
		}
		if e.Mode == "120000" {
			if err := os.Symlink(string(e.Content), full); err != nil {
				return err
			}
			continue
		}
		mode := os.FileMode(0o644)
		if e.Mode == "100755" {
			mode = 0o755
		}
		f, err := os.OpenFile(full, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, mode)
		if err != nil {
			return err
		}
		if _, err := f.Write(e.Content); err != nil {
			f.Close()
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
		if err := os.Chmod(full, mode); err != nil {
			return err
		}
	}
	return nil
}

// noSymlinkAbove refuses a directory chain below root that passes through a symlink.
func noSymlinkAbove(root, dir string) error {
	rel, err := filepath.Rel(root, dir)
	if err != nil || strings.HasPrefix(rel, "..") {
		return fmt.Errorf("path %q leaves the export", dir)
	}
	cur := root
	if rel == "." {
		return nil
	}
	for _, c := range strings.Split(rel, string(filepath.Separator)) {
		cur = filepath.Join(cur, c)
		fi, err := os.Lstat(cur)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("export path %q passes through a symlink", cur)
		}
	}
	return nil
}

// stripStubMarkers removes every line that contains the stub marker from the text files below root.
func stripStubMarkers(root string) error {
	return filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if !bytes.Contains(b, []byte(stubMarkerLine)) {
			return nil
		}
		if bytes.IndexByte(b, 0) >= 0 {
			// A line-wise strip cannot be trusted on binary data: refuse instead of exporting the marker.
			return fmt.Errorf("%s holds a NUL byte and a stub marker; the replay export cannot remove it", p)
		}
		var out [][]byte
		for _, l := range bytes.SplitAfter(b, []byte("\n")) {
			if !bytes.Contains(l, []byte(stubMarkerLine)) {
				out = append(out, l)
			}
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		return os.WriteFile(p, bytes.Join(out, nil), fi.Mode().Perm())
	})
}
