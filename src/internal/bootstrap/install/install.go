// Package install is the install pipeline of sc-bootstrap through provision (plan 50 sections 14.1, 14.3 and 14.4,
// BOOT-10): `plan`, `run` and `status` over the phase engine, the S3 side, the OpenTofu storage module, the recovery
// bundle and the compute provider. The phases after provision are placeholders in test mode.
package install

import (
	"context"
	"io"
	"os"
	"time"

	"strange.company/sc/internal/bootstrap/cli"
	"strange.company/sc/internal/bootstrap/identity"
	"strange.company/sc/internal/clock"
	"strange.company/sc/internal/s3client"
	"strange.company/sc/internal/tofustate"
)

// Account is one S3 account (one endpoint and one access key) as the plan steps use it. Buckets are created by the
// OpenTofu storage module (Tofu), never through this interface.
type Account interface {
	HeadBucket(ctx context.Context, name string) error
	Versioning(ctx context.Context, name string) (enabled bool, err error)
	ListBuckets(ctx context.Context) ([]string, error)
	Bucket(name string) tofustate.Store
}

// TofuS3 is the S3 endpoint and credential a tofu invocation runs with.
type TofuS3 struct {
	Endpoint, Region, AccessKey, SecretKey string
	PathStyle                              bool
}

// BucketSpec is one bucket of the storage module; StateAccount places it in the second account (S3_STATE_*).
type BucketSpec struct {
	Name         string
	StateAccount bool
}

// StorageSpec is what the storage root module is planned and applied with (plan 06 sections 8 and 14.2).
type StorageSpec struct {
	WorkDir    string
	LocalState string
	Prefix     string
	Cluster    string
	Buckets    []BucketSpec
	Main       TofuS3
	State      *TofuS3
	Passphrase string
	// Generation and Previous name the key provider of the passphrase and the fallback passphrase of a rotation (plan 06
	// section 20.3); a zero Generation is the first passphrase (generation 1).
	Generation int
	Previous   string
}

// RemoteState names the remote backend object of the storage module's state.
type RemoteState struct {
	Bucket, Key    string
	S3             TofuS3
	SkipS3Checksum bool
}

// Tofu is the OpenTofu seam. Production runs the tofu binary in the work directory; tests inject a fake.
type Tofu interface {
	// PlanStorage plans the storage module against a throwaway local backend and returns the plan summary.
	PlanStorage(ctx context.Context, s StorageSpec) (summary string, err error)
	// ApplyStorage applies the storage module with an encrypted local state at s.LocalState.
	ApplyStorage(ctx context.Context, s StorageSpec) error
	// StateList lists the resource addresses of the storage state: the local file when remote is nil, else the remote object.
	StateList(ctx context.Context, s StorageSpec, remote *RemoteState) ([]string, error)
	// Migrate moves the local storage state to the remote backend (init -migrate-state -force-copy).
	Migrate(ctx context.Context, s StorageSpec, remote RemoteState) error
	// Changes counts the changes a plan shows against the given backend (local when remote is nil).
	Changes(ctx context.Context, s StorageSpec, remote *RemoteState) (int, error)
}

// Deps are the injectable collaborators of Run. A nil field takes its production default.
type Deps struct {
	// CLI carries ProbeModelKey, Resolve, ClusterDNSIP and Sleep for the preflight; Clock and Record are set by Run.
	CLI cli.Deps
	// Clock is the one time source of the run (journal, confirmations, S3 fakes).
	Clock clock.Clock
	// Stdin feeds the prompts (passphrase twice, then the typed confirmations and the re-entered passphrase), one line each.
	Stdin io.Reader
	// NewS3 opens the S3 account of a credential.
	NewS3 func(cfg s3client.Config) (Account, error)
	// Tofu runs OpenTofu.
	Tofu Tofu
	// LockProbe is the C7 conformance probe (production: tofustate.ExecLockProbe).
	LockProbe tofustate.LockProbe
	// Encrypt encrypts the SOPS files (production: the sops binary).
	Encrypt identity.Encryptor
	// WorkFactor is the age scrypt work factor of the recovery bundle (zero: 20).
	WorkFactor int
	// State runs OpenTofu for the state commands (production: ExecStateTofu).
	State StateTofu
}

// Run executes one sc-bootstrap command line of the install pipeline (plan, run, status) and returns the plan 50
// section 7.2 exit code. Every other command is handed to the command tree of package cli.
func Run(args []string, env map[string]string, stdout, stderr io.Writer, deps Deps) int {
	if deps.Clock == nil {
		deps.Clock = clock.Real{}
	}
	if deps.CLI.Clock == nil {
		deps.CLI.Clock = deps.Clock
	}
	if deps.CLI.Sleep == nil {
		deps.CLI.Sleep = func(d time.Duration) { _ = clock.Real{}.Sleep(context.Background(), d) }
	}
	inv, err := cli.Parse(args)
	if err != nil {
		return cli.Run(args, env, stdout, stderr, deps.CLI)
	}
	switch inv.Command[0] {
	case "plan", "run", "status":
	case "state":
		if len(inv.Command) < 2 || (inv.Command[1] != "status" && inv.Command[1] != "restore" && inv.Command[1] != "verify") {
			return cli.Run(args, env, stdout, stderr, deps.CLI)
		}
	case "rotate":
		if len(inv.Command) < 2 || inv.Command[1] != "tofu-passphrase" {
			return cli.Run(args, env, stdout, stderr, deps.CLI)
		}
	default:
		return cli.Run(args, env, stdout, stderr, deps.CLI)
	}
	if deps.Stdin == nil {
		deps.Stdin = os.Stdin
	}
	s := newSession(inv, env, stdout, stderr, deps)
	ctx, stop := s.signalContext()
	defer stop()
	switch inv.Command[0] {
	case "plan":
		return s.cmdPlan(ctx)
	case "status":
		return s.cmdStatus(ctx)
	case "state":
		return s.cmdState(ctx)
	case "rotate":
		return s.cmdRotate(ctx)
	}
	return s.cmdRun(ctx)
}
