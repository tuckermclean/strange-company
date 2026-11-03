// Package lab is the logic of sc-lab, the QEMU lab substrate driver (plan 05 section 14.6, ADR-0105). Every host
// effect (KVM, image fetch, QEMU launch, SSH wait, free disk, memory) enters through Env so that tests need no
// KVM or QEMU.
package lab

import (
	"context"
	"io"
)

// Fetcher downloads url into the file dst (the image cache).
// SC-STUB(F09 05-AC-7)
type Fetcher interface {
	Fetch(ctx context.Context, url, dst string) error
}

// VM is one QEMU guest to start.
// SC-STUB(F09 05-AC-7)
type VM struct {
	Name string
	Args []string
}

// Launcher starts a QEMU guest. Up must never call it before the KVM, runner and image-checksum checks pass.
// SC-STUB(F09 05-AC-7)
type Launcher interface {
	Launch(ctx context.Context, vm VM) error
}

// Env is the injectable environment of sc-lab. A nil optional seam must be tolerated as a no-op by the
// implementation, except Fetcher, Launcher, FreeBytes and MemBytes.
// SC-STUB(F09 05-AC-7)
type Env struct {
	Root       string // repository root holding versions/lab-images.lock.yaml and infra/lab/lab.yaml
	CacheDir   string // image cache (.cache/lab-images)
	RunBase    string // parent of the per-run directory (/tmp/sc-lab)
	RuntimeDir string // parent of the generated bootstrap.env (/run/sc/lab)
	KVMPath    string // /dev/kvm
	Fetcher    Fetcher
	Launcher   Launcher
	WaitSSH    func(ctx context.Context, port int) error
	FreeBytes  func(path string) (uint64, error) // free bytes of the filesystem holding path
	MemBytes   func() (uint64, error)            // total memory in bytes
	Stdout     io.Writer
	Stderr     io.Writer
}

// UpOptions are the flags of `sc-lab up`.
// SC-STUB(F09 05-AC-7)
type UpOptions struct {
	Substrate string // lab-small, lab-ha or lab-seed
	OS        string // debian-13 or ubuntu-24.04
	RunID     string
}

// Error carries a stable error code (LAB_NO_KVM, LAB_IMAGE_CHECKSUM_MISMATCH, LAB_RUNNER_DISK_LOW,
// LAB_RUNNER_MEMORY_LOW).
// SC-STUB(F09 05-AC-7)
type Error struct {
	Code string
	Msg  string
}

// SC-STUB(F09 05-AC-7)
func (e *Error) Error() string { return "" }

// Up is `sc-lab up`: KVM check, runner check (lab-seed), image fetch and checksum, then launch.
// SC-STUB(F09 05-AC-7)
func Up(ctx context.Context, env Env, opts UpOptions) error { return nil }

// Run is the sc-lab entry point (args exclude the program name). It returns the process exit code and prints
// errors as "sc-lab: <CODE>: <message>" on env.Stderr.
// SC-STUB(F09 05-AC-7)
func Run(ctx context.Context, args []string, env Env) int { return 0 }
