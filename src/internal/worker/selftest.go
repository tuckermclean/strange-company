package worker

import (
	"fmt"
	"io"
	"os"
	"syscall"
)

// The image self-check of plan 28 section 13.1: `sc-worker selftest`, run inside the harness image as uid 65532 with a
// read-only root file system. It proves the rows of the content table that a process can observe from within: the
// image runs as USER 65532:65532, files are writable only below /workspace, /home/worker and /tmp, and no package
// manager is usable. It is side-effect free (plan 28 section 17): nothing is created, written or removed.

const imageUID = 65532

// SelfTestEnv is what `sc-worker selftest` probes (plan 28 section 13.1). It is injected so that Layer 1 needs neither
// root nor a container.
type SelfTestEnv struct {
	Uid, Gid        int
	WritableDirs    []string // must be writable
	ReadOnlyDirs    []string // must not be writable
	PackageManagers []string // must be absent or not executable
}

// DefaultSelfTestEnv is the production environment of the selftest: the identity of the process, the writable roots of
// the image, /usr as the directory that must stay read-only and the package managers of a Debian base.
func DefaultSelfTestEnv() SelfTestEnv {
	return SelfTestEnv{
		Uid: os.Getuid(), Gid: os.Getgid(),
		WritableDirs:    []string{"/workspace", "/home/worker", "/tmp"},
		ReadOnlyDirs:    []string{"/", "/etc", "/var", "/opt", "/bin", "/home", "/usr"},
		PackageManagers: []string{"/usr/bin/apt-get", "/usr/bin/apt", "/usr/bin/dpkg", "/usr/bin/apk", "/usr/bin/yum", "/usr/bin/dnf"},
	}
}

// SelfTest runs the image self-check and returns 0 (ok) or 1 (a check failed). Every failed check is named on
// stderr; "selftest ok" goes to stdout when all pass.
func SelfTest(env SelfTestEnv, stdout, stderr io.Writer) int {
	failed := 0
	fail := func(format string, args ...any) {
		failed++
		fmt.Fprintf(stderr, "selftest: "+format+"\n", args...)
	}
	if env.Uid != imageUID {
		fail("uid is %d, want %d", env.Uid, imageUID)
	}
	if env.Gid != imageUID {
		fail("gid is %d, want %d", env.Gid, imageUID)
	}
	for _, d := range env.WritableDirs {
		if err := syscall.Access(d, 2); err != nil {
			fail("%s is not writable: %v", d, err)
		}
	}
	for _, d := range env.ReadOnlyDirs {
		if _, err := os.Stat(d); err != nil {
			fail("%s cannot be inspected: %v", d, err)
			continue
		}
		if err := syscall.Access(d, 2); err == nil {
			fail("%s is writable, want a read-only file system", d)
		}
	}
	for _, p := range env.PackageManagers {
		if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Mode().Perm()&0o111 != 0 {
			fail("package manager %s is present and executable", p)
		}
	}
	if failed > 0 {
		return 1
	}
	fmt.Fprintln(stdout, "selftest ok")
	return 0
}
