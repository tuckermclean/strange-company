package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"strange.company/sc/internal/bootstrap/store"
)

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
// A local holder is probed with the OFD lock of .bootstrap/run.lock (alive: exit 6). A holder on another host cannot be
// probed: the typed confirmation is the operator's statement that it is dead (procedures/bootstrap-resume.md#lock).
func (e *Engine) Unlock(ctx context.Context, o UnlockOptions) Result {
	if err := os.MkdirAll(e.cfg.Dir, 0o700); err != nil {
		return Result{Exit: 1, Code: "UNLOCK_FAILED", Message: err.Error()}
	}
	path := filepath.Join(e.cfg.Dir, "run.lock")
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return Result{Exit: 1, Code: "UNLOCK_FAILED", Message: err.Error()}
	}
	defer f.Close()
	got, err := tryLockFile(f)
	if err != nil {
		return Result{Exit: 1, Code: "UNLOCK_FAILED", Message: "the local run lock cannot be probed: " + err.Error()}
	}
	if !got {
		return heldResult(e.localHolder(path))
	}

	// What the lock says: the object is authoritative, the local file is what a dead run on this machine left.
	var body []byte
	var etag string
	if e.cfg.Store != nil {
		obj, gerr := e.cfg.Store.Get(ctx, e.lockObjectKey())
		switch {
		case gerr == nil:
			body, etag = obj.Data, obj.ETag
		case !store.Is(gerr, store.NotFound):
			return Result{Exit: 1, Code: "RUN_LOCK_UNAVAILABLE", Message: e.red.String("the run lock object cannot be read: " + gerr.Error())}
		}
	}
	remote := body != nil
	if !remote {
		if b, rerr := os.ReadFile(path); rerr == nil {
			body = b
		}
	}
	var h lockBody
	if len(strings.TrimSpace(string(body))) == 0 {
		return Result{Message: "no run lock is held"}
	}
	_ = json.Unmarshal(body, &h)

	if e.cfg.InstallUID != "" && h.InstallUID != "" && h.InstallUID != e.cfg.InstallUID {
		return Result{Exit: 2, Code: "UNLOCK_INSTALL_MISMATCH", Message: fmt.Sprintf(
			"the run lock belongs to installation %s, not to %s; it is not broken from here", h.InstallUID, e.cfg.InstallUID)}
	}
	if o.RunID != "" && o.RunID != h.RunID {
		return Result{Exit: 2, Code: "UNLOCK_RUN_ID_MISMATCH", Message: fmt.Sprintf(
			"--run-id %s does not match the run that holds the lock (%s)", o.RunID, h.RunID)}
	}
	holder := h.RunID
	if holder == "" {
		holder = "unknown"
	}
	want := "unlock " + e.cfg.Cluster + " " + holder
	if strings.TrimSpace(o.Confirm) != want {
		code := "UNLOCK_CONFIRMATION_MISMATCH"
		if o.Confirm == "" {
			code = "UNLOCK_CONFIRMATION_REQUIRED"
		}
		return Result{Exit: 5, Code: code, Message: e.red.String(fmt.Sprintf(
			"the run lock is held by run %s (command %s, %s@%s, started %s, lease until %s); "+
				"break it only when that run is certainly dead; type '%s' to break it",
			holder, h.Command, h.User, h.Host, h.StartedAt, h.LeaseExpiresAt, want))}
	}

	// The confirmation is recorded first: an unlock leaves its trace even when the delete then fails.
	lr, fail := e.load(ctx)
	if fail != nil {
		return *fail
	}
	if lr.conflict {
		return e.conflictResult(lr)
	}
	if !lr.fresh {
		e.jr, e.remoteETag = lr.doc, lr.remoteETag
		sum := sha256.Sum256(body)
		e.addConfirmation("unlock-run-lock", "sha256:"+hex.EncodeToString(sum[:]), "interactive")
		if res := e.save(ctx); res != nil {
			return *res
		}
	}
	if remote {
		ifm := ""
		if e.ifMatch {
			ifm = etag
		}
		derr := e.cfg.Store.Delete(ctx, e.lockObjectKey(), ifm)
		if store.Is(derr, store.Unsupported) && ifm != "" {
			derr = e.cfg.Store.Delete(ctx, e.lockObjectKey(), "")
		}
		switch {
		case derr == nil, store.Is(derr, store.NotFound):
		case store.Is(derr, store.PreconditionFailed):
			return Result{Exit: 6, Code: "RUN_LOCK_HELD", Message: "the run lock object changed while it was being broken; look at it again"}
		default:
			return Result{Exit: 1, Code: "UNLOCK_FAILED", Message: e.red.String("the run lock object cannot be deleted: " + derr.Error())}
		}
	}
	if rerr := os.Remove(path); rerr != nil && !errors.Is(rerr, os.ErrNotExist) {
		return Result{Exit: 1, Code: "UNLOCK_FAILED", Message: rerr.Error()}
	}
	return Result{Message: fmt.Sprintf("lock released (run %s)", holder)}
}
