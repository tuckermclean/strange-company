package install

import (
	"context"
	"errors"
	"time"

	"strange.company/sc/internal/bootstrap/commit"
	"strange.company/sc/internal/bootstrap/engine"
	"strange.company/sc/internal/bootstrap/identity"
	"strange.company/sc/internal/bootstrap/store"
	"strange.company/sc/internal/s3client"
	"strange.company/sc/internal/tofustate"
)

// unit is a function-backed engine unit (a step or a phase without steps).
type unit struct {
	name    string
	timeout time.Duration
	inputs  any
	check   func(ctx context.Context) (engine.Check, error)
	run     func(ctx context.Context) ([]engine.OutputRef, error)
}

var _ engine.Unit = (*unit)(nil)

func (u *unit) Name() string                        { return u.name }
func (u *unit) Inputs(context.Context) (any, error) { return u.inputs, nil }
func (u *unit) Timeout() time.Duration              { return u.timeout }
func (u *unit) Retryable(error) bool                { return false }
func (u *unit) Check(ctx context.Context) (engine.Check, error) {
	if u.check == nil {
		return engine.Check{State: engine.NotDone}, nil
	}
	return u.check(ctx)
}

func (u *unit) Run(ctx context.Context) ([]engine.OutputRef, error) {
	ref, err := u.run(ctx)
	return ref, wrapErr(err)
}

// phase is a unit with steps.
type phase struct {
	unit
	steps []engine.Unit
}

var _ engine.Phase = (*phase)(nil)

func (p *phase) Steps() []engine.Unit { return p.steps }

func done(reason string) engine.Check    { return engine.Check{State: engine.Done, Reason: reason} }
func notDone(reason string) engine.Check { return engine.Check{State: engine.NotDone, Reason: reason} }

// procedure paths named in failures (contracts/bootstrap-journal.schema.yaml: procedures/<name>.md[#anchor]).
const (
	procPreflight = "procedures/bootstrap-preflight-failure.md"
	procLocking   = "procedures/bootstrap-preflight-failure.md#s3-locking"
	procResume    = "procedures/bootstrap-resume.md"
	procState     = "procedures/recover-opentofu-state.md"
	procStorage   = "procedures/object-storage-outage.md"
)

func fail(code string, exit int, procedure, format string, a ...any) error {
	return &engine.Error{Code: code, Message: sprintf(format, a...), Exit: exit, Procedure: procedure}
}

// wrapErr turns the errors of the libraries into engine failures with the plan 50 exit code.
func wrapErr(err error) error {
	if err == nil {
		return nil
	}
	var ee *engine.Error
	if errors.As(err, &ee) {
		return err
	}
	var f *failure
	if errors.As(err, &f) {
		return &engine.Error{Code: f.code, Message: f.msg, Exit: f.exit}
	}
	var ie *identity.Error
	if errors.As(err, &ie) {
		exit := 1
		switch {
		case ie.Code == "BUNDLE_PASSPHRASE_WRONG" || ie.Code == "BUNDLE_CORRUPT":
			exit = 8
		case len(ie.Code) > 11 && ie.Code[:11] == "PASSPHRASE_":
			exit = 5
		}
		return &engine.Error{Code: ie.Code, Message: ie.Message, Exit: exit, Procedure: procResume}
	}
	var ce *commit.Error
	if errors.As(err, &ce) {
		return &engine.Error{Code: ce.Code, Message: ce.Message}
	}
	var te *tofustate.Error
	if errors.As(err, &te) {
		return &engine.Error{Code: te.Code, Message: te.Msg, Procedure: procState}
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if errors.Is(err, errNoInput) {
		return &engine.Error{Code: "INPUT_MISSING", Message: "the run needed an answer but the input ended", Exit: 5}
	}
	var se *s3client.Error
	if errors.As(err, &se) || store.Is(err, store.Unavailable) {
		return &engine.Error{Code: "S3_REQUEST_FAILED", Message: err.Error(), Procedure: procStorage}
	}
	return err
}
