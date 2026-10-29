package engine

import (
	"context"
	"encoding/json"
	"errors"
)

// RecoveryBundleState is the journal's recovery_bundle object (contracts/bootstrap-journal.schema.yaml).
type RecoveryBundleState struct {
	Status      string
	Generation  int
	Sha256      string
	LocalPath   string
	S3URI       string
	S3VersionID string
	CreatedAt   string
	ConfirmedAt string
}

// bundleJSON is the persisted form: optional fields that are empty are omitted so the object validates against the
// schema; created_at and confirmed_at are null until set.
type bundleJSON struct {
	Status      string  `json:"status"`
	Generation  int     `json:"generation"`
	Sha256      string  `json:"sha256,omitempty"`
	LocalPath   string  `json:"local_path,omitempty"`
	S3URI       string  `json:"s3_uri,omitempty"`
	S3VersionID string  `json:"s3_version_id,omitempty"`
	CreatedAt   *string `json:"created_at"`
	ConfirmedAt *string `json:"confirmed_at"`
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// SetRecoveryBundle replaces the journal's recovery_bundle object; the next journal save persists it.
func (e *Engine) SetRecoveryBundle(s RecoveryBundleState) {
	if e.jr == nil {
		return
	}
	if s.Status == "" {
		s.Status = "absent"
	}
	b, err := json.Marshal(bundleJSON{
		Status: s.Status, Generation: s.Generation, Sha256: s.Sha256, LocalPath: s.LocalPath,
		S3URI: s.S3URI, S3VersionID: s.S3VersionID, CreatedAt: nullable(s.CreatedAt), ConfirmedAt: nullable(s.ConfirmedAt),
	})
	if err != nil {
		return
	}
	e.jr.RecoveryBundle = json.RawMessage(b)
}

// RecoveryBundle returns the journal's current recovery_bundle object.
func (e *Engine) RecoveryBundle() (s RecoveryBundleState) {
	if e.jr == nil || len(e.jr.RecoveryBundle) == 0 {
		return RecoveryBundleState{Status: "absent"}
	}
	var b bundleJSON
	if err := json.Unmarshal(e.jr.RecoveryBundle, &b); err != nil {
		return RecoveryBundleState{Status: "absent"}
	}
	return RecoveryBundleState{
		Status: b.Status, Generation: b.Generation, Sha256: b.Sha256, LocalPath: b.LocalPath, S3URI: b.S3URI,
		S3VersionID: b.S3VersionID, CreatedAt: deref(b.CreatedAt), ConfirmedAt: deref(b.ConfirmedAt),
	}
}

// InstallUID returns the journal's install_uid, or "" while the journal still carries the placeholder of an
// installation whose plan.install-identity has not run.
func (e *Engine) InstallUID() string {
	if e.jr == nil || e.jr.InstallUID == "" || e.jr.InstallUID == zeroUID {
		return ""
	}
	return e.jr.InstallUID
}

// SetInstallUID records the install_uid plan.install-identity generated; the next journal save persists it, and
// the run lock written afterwards names it.
func (e *Engine) SetInstallUID(uid string) {
	e.cfg.InstallUID = uid
	if e.jr != nil {
		e.jr.InstallUID = uid
	}
}

// Confirmed reports whether the journal (or this run) holds a confirmation of the kind for the subject digest.
func (e *Engine) Confirmed(kind, subjectDigest string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, c := range e.pending {
		if c.Kind == kind && c.SubjectDigest == subjectDigest {
			return true
		}
	}
	if e.jr == nil {
		return false
	}
	for _, c := range e.jr.Confirmations {
		if c.Kind == kind && c.SubjectDigest == subjectDigest {
			return true
		}
	}
	return false
}

// StepSucceeded reports whether the journal records the named step (in any phase) as succeeded.
func (e *Engine) StepSucceeded(name string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.jr == nil {
		return false
	}
	for _, pl := range e.jr.Pipelines {
		for _, ph := range pl.Phases {
			for _, st := range ph.Steps {
				if st.Name == name && st.Status == stSucceeded {
					return true
				}
			}
		}
	}
	return false
}

// ConfirmedSubject returns the subject digest of the newest confirmation of the kind in the journal (or this run),
// "" when there is none.
func (e *Engine) ConfirmedSubject(kind string) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	subject := ""
	if e.jr != nil {
		for _, c := range e.jr.Confirmations {
			if c.Kind == kind {
				subject = c.SubjectDigest
			}
		}
	}
	for _, c := range e.pending {
		if c.Kind == kind {
			subject = c.SubjectDigest
		}
	}
	return subject
}

// InstallationState returns installation.state of the loaded journal ("new" before one is loaded).
func (e *Engine) InstallationState() string {
	if e.jr == nil {
		return "new"
	}
	return e.jr.Installation.State
}

// Checkpoint writes the journal now (local copy first, then S3 when a store is attached), so a step that does
// several things can make the first of them durable before the next one starts.
func (e *Engine) Checkpoint(ctx context.Context) error {
	if e.jr == nil {
		return errors.New("engine: no journal is loaded")
	}
	if res := e.save(ctx); res != nil {
		return &Error{Code: res.Code, Message: res.Message, Exit: res.Exit}
	}
	return nil
}
