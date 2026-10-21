//go:build linux

package verify

import (
	"archive/tar"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"syscall"
)

// stubMarkerLine is the text every stub marker line carries; the replay export has no such line (TDD-B6).
const stubMarkerLine = "SC-STUB("

// exportCommit writes the tree of a commit (git archive, hardened Git environment) into dir, which must not exist.
// No .git is created and no entry can leave dir: a path with .. or an absolute path is refused, and no entry is
// written below a symlink.
func exportCommit(g GitRepo, rev, dir string) error {
	if err := checkRevision(rev); err != nil {
		return err
	}
	bin, err := exec.LookPath("git")
	if err != nil {
		return fmt.Errorf("git is not available: %w", err)
	}
	argv := []string{"-C", g.Dir, "--no-pager", "--no-replace-objects", "--literal-pathspecs", "--no-optional-locks"}
	for _, c := range hardenedConfig {
		argv = append(argv, "-c", c)
	}
	argv = append(argv, "archive", "--format=tar", rev)
	cmd := exec.Command(bin, argv...)
	cmd.Env = gitEnv(dirOf(bin))
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	exErr := extractTar(out, dir)
	_, _ = io.Copy(io.Discard, out)
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("git archive failed: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	return exErr
}

func extractTar(r io.Reader, dir string) error {
	if err := os.Mkdir(dir, 0o755); err != nil {
		return err
	}
	tr := tar.NewReader(r)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("git archive: %w", err)
		}
		if h.Typeflag == tar.TypeXGlobalHeader {
			continue
		}
		name := path.Clean(h.Name)
		if name == "." {
			continue
		}
		if path.IsAbs(name) || name == ".." || strings.HasPrefix(name, "../") || name == ".git" || strings.HasPrefix(name, ".git/") {
			return fmt.Errorf("git archive: unsafe path %q", h.Name)
		}
		full := filepath.Join(dir, filepath.FromSlash(name))
		if err := noSymlinkAbove(dir, filepath.Dir(full)); err != nil {
			return err
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(full, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				return err
			}
			mode := os.FileMode(0o644)
			if h.Mode&0o111 != 0 {
				mode = 0o755
			}
			f, err := os.OpenFile(full, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, mode)
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return err
			}
			if err := f.Close(); err != nil {
				return err
			}
		case tar.TypeSymlink:
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				return err
			}
			if err := os.Symlink(h.Linkname, full); err != nil {
				return err
			}
		default:
			return fmt.Errorf("git archive: entry %q has unsupported type %q", h.Name, h.Typeflag)
		}
	}
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
		if !bytes.Contains(b, []byte(stubMarkerLine)) || bytes.IndexByte(b, 0) >= 0 {
			return nil
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
