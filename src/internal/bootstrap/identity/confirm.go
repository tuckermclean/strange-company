package identity

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"

	"strange.company/sc/internal/bootstrap/engine"
	"strange.company/sc/internal/clock"
)

// BundleJournal is the journal's recovery_bundle record (engine.Engine implements it).
type BundleJournal interface {
	RecoveryBundle() engine.RecoveryBundleState
	SetRecoveryBundle(engine.RecoveryBundleState)
}

// BundleConfirmConfig configures plan.bundle-confirm.
type BundleConfirmConfig struct {
	Journal BundleJournal
	// Download fetches the S3 copy of the bundle that plan.bundle-upload verified.
	Download func(ctx context.Context) ([]byte, error)
	// Prompt asks the operator to re-enter the recovery passphrase; attempt counts from 1.
	Prompt   func(ctx context.Context, attempt int) (string, error)
	Clock    clock.Clock
	Attempts int
}

const (
	bundleConfirmName = "plan.bundle-confirm"
	exitBundle        = 8
)

type bundleConfirm struct{ cfg BundleConfirmConfig }

// NewBundleConfirm returns the plan.bundle-confirm step (plan 50 BOOT-10 step 9): the S3 copy is downloaded and
// opened with the re-entered passphrase, three wrong entries exit 8 and leave recovery_bundle.status at uploaded.
func NewBundleConfirm(cfg BundleConfirmConfig) engine.Unit {
	if cfg.Attempts <= 0 {
		cfg.Attempts = 3
	}
	return &bundleConfirm{cfg: cfg}
}

func (u *bundleConfirm) Name() string { return bundleConfirmName }

// Inputs is constant: the step's done check reads the journal, not a digest of its inputs.
func (u *bundleConfirm) Inputs(context.Context) (any, error) {
	return map[string]any{"step": bundleConfirmName}, nil
}

func (u *bundleConfirm) Check(context.Context) (engine.Check, error) {
	if u.cfg.Journal.RecoveryBundle().Status == "confirmed" {
		return engine.Check{State: engine.Done, Reason: "recovery_bundle.status is confirmed"}, nil
	}
	return engine.Check{State: engine.NotDone, Reason: "the recovery bundle is not confirmed"}, nil
}

func (u *bundleConfirm) Timeout() time.Duration { return 30 * time.Minute }

func (u *bundleConfirm) Retryable(error) bool { return false }

func bundleErr(code, msg string) error {
	return &engine.Error{Code: code, Message: msg, Exit: exitBundle, Procedure: "procedures/bootstrap-resume.md"}
}

func (u *bundleConfirm) Run(ctx context.Context) ([]engine.OutputRef, error) {
	st := u.cfg.Journal.RecoveryBundle()
	if st.Status != "uploaded" {
		return nil, bundleErr("BUNDLE_NOT_UPLOADED", "the recovery bundle is not uploaded (status "+st.Status+"); run plan.bundle-upload first")
	}
	data, err := u.cfg.Download(ctx)
	if err != nil {
		return nil, bundleErr("BUNDLE_MISSING", "the S3 copy of the recovery bundle cannot be downloaded: "+err.Error())
	}
	sum := sha256.Sum256(data)
	if want := strings.TrimPrefix(st.Sha256, "sha256:"); want != "" && want != hex.EncodeToString(sum[:]) {
		return nil, bundleErr("BUNDLE_SHA_MISMATCH", "the S3 copy of the recovery bundle differs from the uploaded digest")
	}
	for attempt := 1; attempt <= u.cfg.Attempts; attempt++ {
		pass, err := u.cfg.Prompt(ctx, attempt)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, bundleErr("BUNDLE_PASSPHRASE_PROMPT_FAILED", "the recovery passphrase could not be read: "+err.Error())
		}
		o, err := Open(data, pass)
		if err == nil && o != nil {
			st.Status = "confirmed"
			st.ConfirmedAt = u.cfg.Clock.Now().UTC().Format(time.RFC3339)
			u.cfg.Journal.SetRecoveryBundle(st)
			return nil, nil
		}
		var ie *Error
		if errors.As(err, &ie) && ie.Code != "BUNDLE_PASSPHRASE_WRONG" {
			return nil, bundleErr(ie.Code, ie.Message)
		}
	}
	return nil, bundleErr("BUNDLE_PASSPHRASE_WRONG",
		"the recovery bundle was not confirmed after "+strconv.Itoa(u.cfg.Attempts)+" wrong passphrase entries; the next run asks again")
}
