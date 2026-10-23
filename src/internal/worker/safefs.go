package worker

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

// Symlink-safe file operations (plan 27 section 13.3). Every read, write and delete sc-worker makes below a path
// the harness can influence (the workspace: out/claim.json, out/notes, .sc-shim, .git internals) resolves the path
// relative to a directory opened by sc-worker with openat2(RESOLVE_NO_SYMLINKS | RESOLVE_NO_MAGICLINKS |
// RESOLVE_BENEATH). Where openat2 is unavailable (ENOSYS, or a seccomp filter answering EPERM) the same walk is done
// with one openat(O_NOFOLLOW) per component. Only regular files are read or written (checked with fstat on the open
// descriptor; the open uses O_NONBLOCK so that a FIFO cannot block it), and entries are removed with unlinkat.

const (
	sysOpenat2          = 437 // the same number on every Linux architecture
	resolveNoMagiclinks = 0x02
	resolveNoSymlinks   = 0x04
	resolveBeneath      = 0x08
	atRemovedir         = 0x200
	oPath               = 0x200000
	removeDepth         = 32
)

var (
	errSymlink    = errors.New("symlink refused")
	errNotRegular = errors.New("not a regular file")
	errTooBig     = errors.New("file too large")
	errBadPath    = errors.New("invalid relative path")
)

// isRefusal reports whether err means that a link, a special file or an escaping path was refused.
func isRefusal(err error) bool {
	return errors.Is(err, errSymlink) || errors.Is(err, errNotRegular)
}

type openHow struct{ flags, mode, resolve uint64 }

func openRoot(base string) (int, error) {
	for {
		fd, err := syscall.Open(base, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC, 0)
		if errors.Is(err, syscall.EINTR) {
			continue
		}
		return fd, err
	}
}

func openat2(dirfd int, rel string, flags int, mode uint32) (int, error) {
	p, err := syscall.BytePtrFromString(rel)
	if err != nil {
		return -1, err
	}
	how := openHow{flags: uint64(flags | syscall.O_CLOEXEC), resolve: resolveNoMagiclinks | resolveNoSymlinks | resolveBeneath}
	if flags&syscall.O_CREAT != 0 {
		how.mode = uint64(mode)
	}
	for {
		fd, _, e := syscall.Syscall6(sysOpenat2, uintptr(dirfd), uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(&how)), unsafe.Sizeof(how), 0, 0)
		if e == syscall.EINTR {
			continue
		}
		if e != 0 {
			return -1, e
		}
		return int(fd), nil
	}
}

// openatWalk is the fallback: one openat per component, none of which follows a symlink.
func openatWalk(dirfd int, rel string, flags int, mode uint32) (int, error) {
	parts := strings.Split(rel, "/")
	cur, owned := dirfd, false
	for i, part := range parts {
		fl, m := syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, uint32(0)
		if i == len(parts)-1 {
			fl = flags | syscall.O_NOFOLLOW | syscall.O_CLOEXEC
			if flags&syscall.O_CREAT != 0 {
				m = mode
			}
		}
		var fd int
		var err error
		for {
			fd, err = syscall.Openat(cur, part, fl, m)
			if !errors.Is(err, syscall.EINTR) {
				break
			}
		}
		if owned {
			_ = syscall.Close(cur)
		}
		if err != nil {
			return -1, err
		}
		cur, owned = fd, true
	}
	return cur, nil
}

func validRel(rel string) bool {
	if rel == "" || strings.HasPrefix(rel, "/") {
		return false
	}
	for _, p := range strings.Split(rel, "/") {
		if p == "" || p == "." || p == ".." {
			return false
		}
	}
	return true
}

// hasLinkComponent reports whether a component of rel below base is a symbolic link (used only to classify an error).
func hasLinkComponent(base, rel string) bool {
	cur := base
	for _, p := range strings.Split(rel, "/") {
		cur = filepath.Join(cur, p)
		st, err := os.Lstat(cur)
		if err != nil {
			return false
		}
		if st.Mode()&os.ModeSymlink != 0 {
			return true
		}
	}
	return false
}

// openBeneath opens rel below the trusted directory base without following any link; the result is a raw descriptor.
func openBeneath(base, rel string, flags int, mode uint32) (int, error) {
	if !validRel(rel) {
		return -1, errBadPath
	}
	root, err := openRoot(base)
	if err != nil {
		return -1, err
	}
	defer syscall.Close(root)
	fd, err := openat2(root, rel, flags, mode)
	if errors.Is(err, syscall.ENOSYS) || errors.Is(err, syscall.EPERM) {
		fd, err = openatWalk(root, rel, flags, mode)
	}
	switch {
	case err == nil:
		return fd, nil
	case errors.Is(err, syscall.ELOOP), errors.Is(err, syscall.EXDEV):
		return -1, errSymlink
	case errors.Is(err, syscall.ENOTDIR), errors.Is(err, syscall.ENXIO):
		if hasLinkComponent(base, rel) {
			return -1, errSymlink
		}
	}
	return -1, err
}

// openRegular opens a regular file (O_NONBLOCK, so that a FIFO is never waited for) and refuses everything else.
func openRegular(base, rel string, flags int) (*os.File, error) {
	fd, err := openBeneath(base, rel, flags|syscall.O_NONBLOCK, 0o600)
	if err != nil {
		return nil, err
	}
	var st syscall.Stat_t
	if err := syscall.Fstat(fd, &st); err != nil {
		_ = syscall.Close(fd)
		return nil, err
	}
	if st.Mode&syscall.S_IFMT != syscall.S_IFREG {
		_ = syscall.Close(fd)
		return nil, errNotRegular
	}
	return os.NewFile(uintptr(fd), filepath.Join(base, rel)), nil
}

// openDir opens a real directory below base.
func openDir(base, rel string) (*os.File, error) {
	fd, err := openBeneath(base, rel, syscall.O_RDONLY|syscall.O_DIRECTORY, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), filepath.Join(base, rel)), nil
}

// readRegularFile reads a regular file of at most limit bytes below base.
func readRegularFile(base, rel string, limit int64) ([]byte, error) {
	f, err := openRegular(base, rel, syscall.O_RDONLY)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, errTooBig
	}
	return b, nil
}

func unlinkat(dirfd int, name string, flags int) error {
	p, err := syscall.BytePtrFromString(name)
	if err != nil {
		return err
	}
	for {
		_, _, e := syscall.Syscall(syscall.SYS_UNLINKAT, uintptr(dirfd), uintptr(unsafe.Pointer(p)), uintptr(flags))
		if e == syscall.EINTR {
			continue
		}
		if e != 0 {
			return e
		}
		return nil
	}
}

// removeAllAt removes the entry name of the directory dirfd with its content, never following a link.
func removeAllAt(dirfd int, name string, depth int) error {
	err := unlinkat(dirfd, name, 0)
	if err == nil || errors.Is(err, syscall.ENOENT) {
		return err
	}
	if !errors.Is(err, syscall.EISDIR) && !errors.Is(err, syscall.EPERM) {
		return err
	}
	if depth > removeDepth {
		return syscall.ELOOP
	}
	fd, oerr := syscall.Openat(dirfd, name, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if oerr != nil {
		return oerr
	}
	d := os.NewFile(uintptr(fd), name)
	names, _ := d.Readdirnames(-1)
	for _, n := range names {
		_ = removeAllAt(fd, n, depth+1)
	}
	_ = d.Close()
	return unlinkat(dirfd, name, atRemovedir)
}

// removeRel removes the entry rel below base (a symlink is removed itself). removed is false when nothing was there.
func removeRel(base, rel string) (removed bool, err error) {
	parent, name := "", rel
	if i := strings.LastIndexByte(rel, '/'); i >= 0 {
		parent, name = rel[:i], rel[i+1:]
	}
	var d *os.File
	if parent == "" {
		fd, rerr := openRoot(base)
		if rerr != nil {
			return false, rerr
		}
		d = os.NewFile(uintptr(fd), base)
	} else if d, err = openDir(base, parent); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	defer d.Close()
	if !validRel(name) {
		return false, errBadPath
	}
	err = removeAllAt(int(d.Fd()), name, 0)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

// mkdirRel creates the directory rel below base when it does not exist.
func mkdirRel(base, rel string, mode uint32) error {
	i := strings.LastIndexByte(rel, '/')
	if i < 0 {
		return errBadPath
	}
	d, err := openDir(base, rel[:i])
	if err != nil {
		return err
	}
	defer d.Close()
	if err := syscall.Mkdirat(int(d.Fd()), rel[i+1:], mode); err != nil && !errors.Is(err, syscall.EEXIST) {
		return err
	}
	return nil
}

// writeRelNew creates the file rel below base (it must not exist) with exactly mode.
func writeRelNew(base, rel string, data []byte, mode uint32) error {
	fd, err := openBeneath(base, rel, syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL|syscall.O_NONBLOCK, mode)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), filepath.Join(base, rel))
	defer f.Close()
	if err := syscall.Fchmod(fd, mode); err != nil {
		return err
	}
	_, err = f.Write(data)
	return err
}

// writeNoFollow replaces the file path (whose directory is opened without following a link): an existing entry of
// that name is unlinked, never followed, and the file is created with O_EXCL|O_NOFOLLOW and fchmod'ed.
func writeNoFollow(path string, b []byte, mode os.FileMode) error {
	dfd, err := syscall.Open(filepath.Dir(path), syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer syscall.Close(dfd)
	return writeAt(dfd, filepath.Base(path), b, mode)
}

func writeAt(dfd int, name string, b []byte, mode os.FileMode) error {
	if err := removeAllAt(dfd, name, 0); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	fd, err := syscall.Openat(dfd, name, syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, uint32(mode.Perm()))
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), name)
	if err := syscall.Fchmod(fd, uint32(mode.Perm())); err != nil {
		_ = f.Close()
		return err
	}
	_, err = f.Write(b)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// writeAtomic writes path through path.tmp and a rename; files of the shim directory are group-readable and not
// world-accessible (plan 27 section 7.4). The temporary name is created exclusively and never followed, so a link the
// harness planted at it is unlinked, not written through.
func writeAtomic(path string, b []byte) error {
	dfd, err := syscall.Open(filepath.Dir(path), syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer syscall.Close(dfd)
	name := filepath.Base(path)
	if err := writeAt(dfd, name+".tmp", b, 0o640); err != nil {
		return err
	}
	// The rename is resolved through the descriptor of the directory the temporary file was created in.
	if err := renameat(dfd, name+".tmp", name); err != nil {
		_ = unlinkat(dfd, name+".tmp", 0)
		return err
	}
	return nil
}

func renameat(dfd int, from, to string) error {
	for {
		err := syscall.Renameat(dfd, from, dfd, to)
		if !errors.Is(err, syscall.EINTR) {
			return err
		}
	}
}

// refuse logs symlink_refused once per path.
func (pl *pipeline) refuse(path string, err error) {
	if pl.refused == nil {
		pl.refused = map[string]bool{}
	}
	if pl.refused[path] {
		return
	}
	pl.refused[path] = true
	kind := "symlink"
	switch {
	case errors.Is(err, errNotRegular):
		kind = "not_regular"
	case !errors.Is(err, errSymlink):
		kind = "not_directory"
	}
	pl.w.log("warn", "symlink_refused", map[string]any{"path": path, "kind": kind})
}

// scanNotes looks at every entry below /workspace/out/notes without following links and refuses (logs) links and
// special files; sc-worker reads none of them.
func (pl *pipeline) scanNotes() {
	count := 0
	pl.scanNotesDir("out/notes", 0, &count)
}

func (pl *pipeline) scanNotesDir(rel string, depth int, count *int) {
	ws := pl.w.cfg.WorkspaceDir
	d, err := openDir(ws, rel)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			pl.refuse(filepath.Join(ws, rel), err)
		}
		return
	}
	names, _ := d.Readdirnames(-1)
	_ = d.Close()
	for _, n := range names {
		if *count++; *count > 1000 {
			return
		}
		r := rel + "/" + n
		fd, err := openBeneath(ws, r, oPath, 0)
		if err != nil {
			if isRefusal(err) {
				pl.refuse(filepath.Join(ws, r), err)
			}
			continue
		}
		var st syscall.Stat_t
		serr := syscall.Fstat(fd, &st)
		_ = syscall.Close(fd)
		switch {
		case serr != nil:
		case st.Mode&syscall.S_IFMT == syscall.S_IFDIR:
			if depth < 4 {
				pl.scanNotesDir(r, depth+1, count)
			}
		case st.Mode&syscall.S_IFMT == syscall.S_IFLNK:
			pl.refuse(filepath.Join(ws, r), errSymlink)
		case st.Mode&syscall.S_IFMT != syscall.S_IFREG:
			pl.refuse(filepath.Join(ws, r), errNotRegular)
		}
	}
}
