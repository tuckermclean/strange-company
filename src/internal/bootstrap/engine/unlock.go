package engine

import "context"

// UnlockOptions are the inputs of `./bootstrap unlock [--run-id ID]` (plan 50 section 7.1 and 17.4).
type UnlockOptions struct {
	// RunID is --run-id: when set it must equal the run_id of the lock object.
	RunID string
	// Confirm is the typed confirmation: `unlock <cluster> <run_id>`. Empty means nothing was typed.
	Confirm string
}

// Unlock breaks a stale run lock: it checks the lock, requires the typed confirmation, deletes the lock object and
// the local lock file, and records the unlock-run-lock confirmation. It does not take the run lock itself.
//
// SC-STUB(F08 unlock)
func (e *Engine) Unlock(ctx context.Context, o UnlockOptions) Result {
	return Result{}
}
