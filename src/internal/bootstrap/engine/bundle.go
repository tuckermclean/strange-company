package engine

import "encoding/json"

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
