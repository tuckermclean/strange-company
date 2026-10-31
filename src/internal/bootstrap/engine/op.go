package engine

import (
	"context"
	"errors"
)

// LoadJournal loads the journal (local and S3 copy) without taking the run lock or writing anything, so a read-only
// command can read the recovery bundle record and the install uid. A journal conflict is returned as the result.
func (e *Engine) LoadJournal(ctx context.Context) Result {
	lr, fail := e.load(ctx)
	if fail != nil {
		return *fail
	}
	if lr.conflict {
		return e.conflictResult(lr)
	}
	e.jr, e.remoteETag = lr.doc, lr.remoteETag
	return Result{}
}

// ExecuteOp runs one operation that is not a pipeline (`rotate tofu-passphrase`, `state restore`): it takes the run
// lock, loads the journal, records the invocation as a run, calls fn and releases the lock. An *Error from fn sets the
// exit code (zero: 1) and the stable code; any other error is a plain failure.
func (e *Engine) ExecuteOp(ctx context.Context, fn func(ctx context.Context) error) Result {
	if res := e.Acquire(ctx); res.Exit != 0 {
		res.Message = e.red.String(res.Message)
		return res
	}
	res := e.executeOp(ctx, fn)
	res.Message = e.red.String(res.Message)
	e.uploadLog(ctx)
	if rel := e.Release(ctx); rel.Exit != 0 && res.Exit == 0 {
		res = rel
		res.Message = e.red.String(res.Message)
	}
	return res
}

func (e *Engine) executeOp(ctx context.Context, fn func(ctx context.Context) error) Result {
	if r := e.LoadJournal(ctx); r.Exit != 0 {
		return r
	}
	if e.jr.Cluster == "" {
		e.jr.Cluster = e.cfg.Cluster
	}
	run := e.beginRun()
	if res := e.save(ctx); res != nil {
		return *res
	}
	e.Log("info", "run-start", "run started", map[string]any{"command": e.cfg.Command})
	out := Result{}
	if err := fn(ctx); err != nil {
		var ee *Error
		if errors.As(err, &ee) {
			out = Result{Exit: ee.Exit, Code: ee.Code, Message: ee.Message}
		} else {
			out = Result{Code: "OPERATION_FAILED", Message: err.Error()}
		}
		if out.Exit == 0 {
			out.Exit = 1
		}
	}
	return e.finish(ctx, run, &pipelineJ{}, out)
}
