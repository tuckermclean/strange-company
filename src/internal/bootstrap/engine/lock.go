package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"strange.company/sc/internal/bootstrap/store"
)

const (
	leaseDuration = 12 * time.Hour
	renewEvery    = 10 * time.Minute
)

// lockBody is the content of the lock object and of .bootstrap/run.lock (plan 50 section 17.4).
type lockBody struct {
	RunID          string `json:"run_id"`
	Command        string `json:"command"`
	User           string `json:"user"`
	Host           string `json:"host"`
	InstallUID     string `json:"install_uid"`
	StartedAt      string `json:"started_at"`
	LeaseExpiresAt string `json:"lease_expires_at"`
}

func (e *Engine) lockObjectKey() string { return e.cfg.Cluster + "/locks/bootstrap.lock" }

func (e *Engine) lockBytes(started time.Time) []byte {
	b, _ := json.Marshal(lockBody{
		RunID: e.runID, Command: e.cfg.Command, User: e.cfg.User, Host: e.cfg.Host, InstallUID: e.uid(),
		StartedAt: started.Format(time.RFC3339), LeaseExpiresAt: started.Add(leaseDuration).Format(time.RFC3339),
	})
	return b
}

func heldResult(h lockBody) Result {
	who := h.RunID
	if who == "" {
		who = "unknown"
	}
	msg := fmt.Sprintf("the run lock is held by run %s (command %s, %s@%s, started %s, lease until %s); "+
		"wait for it to finish, or break a lock whose holder is certainly dead with ./bootstrap unlock (procedures/bootstrap-resume.md#lock)",
		who, h.Command, h.User, h.Host, h.StartedAt, h.LeaseExpiresAt)
	return Result{Exit: 6, Code: "RUN_LOCK_HELD", Message: msg}
}

// Acquire takes the run lock (plan 50 section 17.4): the local OFD lock, then the remote object by conditional
// create. Exit 6 when another live run holds it.
func (e *Engine) Acquire(ctx context.Context) Result {
	if e.lockFile != nil {
		return Result{}
	}
	if err := os.MkdirAll(e.cfg.Dir, 0o700); err != nil {
		return Result{Exit: 1, Code: "RUN_LOCK_FAILED", Message: err.Error()}
	}
	path := filepath.Join(e.cfg.Dir, "run.lock")
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return Result{Exit: 1, Code: "RUN_LOCK_FAILED", Message: err.Error()}
	}
	got, err := tryLockFile(f)
	if err != nil {
		f.Close()
		return Result{Exit: 1, Code: "RUN_LOCK_FAILED", Message: "the local run lock failed: " + err.Error()}
	}
	if !got {
		f.Close()
		return heldResult(e.localHolder(path))
	}
	started := e.now()
	body := e.lockBytes(started)
	if err := f.Truncate(0); err == nil {
		_, _ = f.WriteAt(body, 0)
	}
	if e.cfg.Store != nil {
		if res := e.acquireRemote(ctx, body); res != nil {
			e.dropLocal(f)
			return *res
		}
	}
	e.lockFile, e.lastRenew = f, started
	return Result{}
}

// localHolder reads the details the holder of the local lock wrote. The holder writes them right after taking the
// lock, so an empty file is given a few chances to fill.
func (e *Engine) localHolder(path string) lockBody {
	var h lockBody
	for i := 0; i < 20000; i++ {
		if b, err := os.ReadFile(path); err == nil && json.Unmarshal(b, &h) == nil && h.RunID != "" {
			return h
		}
		runtime.Gosched()
	}
	return lockBody{Command: "another run on this machine", User: "?", Host: e.cfg.Host}
}

func (e *Engine) acquireRemote(ctx context.Context, body []byte) *Result {
	st, key := e.cfg.Store, e.lockObjectKey()
	for attempt := 0; attempt < 3; attempt++ {
		etag, err := st.PutIfAbsent(ctx, key, body)
		if err == nil {
			e.lockETag, e.remoteHeld = etag, true
			return nil
		}
		switch {
		case store.Is(err, store.Unsupported):
			return &Result{Exit: 3, Code: "PF_S3_LOCK_UNSUPPORTED", Message: "the object store lacks conditional create (If-None-Match), so the run lock is unsafe (procedures/bootstrap-preflight-failure.md#s3-locking)"}
		case !store.Is(err, store.Exists):
			return &Result{Exit: 1, Code: "RUN_LOCK_UNAVAILABLE", Message: e.red.String("the run lock object cannot be created: " + err.Error())}
		}
		obj, gerr := st.Get(ctx, key)
		if store.Is(gerr, store.NotFound) {
			continue
		}
		if gerr != nil {
			return &Result{Exit: 1, Code: "RUN_LOCK_UNAVAILABLE", Message: e.red.String("the run lock object cannot be read: " + gerr.Error())}
		}
		var h lockBody
		_ = json.Unmarshal(obj.Data, &h)
		if h.Host == "" || h.Host != e.cfg.Host || h.InstallUID != e.uid() {
			res := heldResult(h)
			return &res
		}
		// Same host and install, and the local OFD lock was free: the holder is provably dead (BOOT-5).
		ifm := ""
		if e.ifMatch {
			ifm = obj.ETag
		}
		etag, perr := st.Put(ctx, key, body, ifm)
		if store.Is(perr, store.Unsupported) && ifm != "" {
			etag, perr = st.Put(ctx, key, body, "")
		}
		if store.Is(perr, store.PreconditionFailed) {
			continue
		}
		if perr != nil {
			return &Result{Exit: 1, Code: "RUN_LOCK_UNAVAILABLE", Message: e.red.String("the stale run lock cannot be taken over: " + perr.Error())}
		}
		sum := sha256.Sum256(obj.Data)
		e.addConfirmation("stale-lock-takeover", "sha256:"+hex.EncodeToString(sum[:]), "flag")
		e.lockETag, e.remoteHeld = etag, true
		return nil
	}
	return &Result{Exit: 1, Code: "RUN_LOCK_UNAVAILABLE", Message: "the run lock object kept changing; try again"}
}

// dropLocal frees the local lock.
func (e *Engine) dropLocal(f *os.File) {
	_ = f.Truncate(0)
	_ = f.Close()
}

// Renew extends the lease with an If-Match PUT when the store supports it; otherwise it does nothing.
func (e *Engine) Renew(ctx context.Context) Result {
	now := e.now()
	if !e.remoteHeld || !e.ifMatch {
		e.lastRenew = now
		return Result{}
	}
	body := e.lockBytes(now)
	etag, err := e.cfg.Store.Put(ctx, e.lockObjectKey(), body, e.lockETag)
	switch {
	case err == nil:
		e.lockETag, e.lastRenew = etag, now
		return Result{}
	case store.Is(err, store.Unsupported):
		e.ifMatch, e.lastRenew = false, now
		return Result{}
	case store.Is(err, store.PreconditionFailed):
		return Result{Exit: 6, Code: "RUN_LOCK_LOST", Message: "the run lock object changed under this run; another run took it over"}
	}
	return Result{Exit: 1, Code: "RUN_LOCK_UNAVAILABLE", Message: e.red.String("the run lock lease cannot be renewed: " + err.Error())}
}

// Release deletes the remote lock object (conditional on its ETag where supported) and frees the local lock.
func (e *Engine) Release(ctx context.Context) Result {
	ctx = context.WithoutCancel(ctx)
	var res Result
	if e.remoteHeld {
		ifm := ""
		if e.ifMatch {
			ifm = e.lockETag
		}
		err := e.cfg.Store.Delete(ctx, e.lockObjectKey(), ifm)
		if store.Is(err, store.Unsupported) && ifm != "" {
			err = e.cfg.Store.Delete(ctx, e.lockObjectKey(), "")
		}
		if err != nil && !store.Is(err, store.NotFound) && !store.Is(err, store.PreconditionFailed) {
			res = Result{Exit: 1, Code: "RUN_LOCK_RELEASE_FAILED", Message: e.red.String("the run lock object cannot be deleted: " + err.Error())}
		}
		e.remoteHeld = false
	}
	if e.lockFile != nil {
		e.dropLocal(e.lockFile)
		e.lockFile = nil
	}
	return res
}
