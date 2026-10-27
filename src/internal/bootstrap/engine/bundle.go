package engine

// RecoveryBundleState is the journal's recovery_bundle object (contracts/bootstrap-journal.schema.yaml).
//
// SC-STUB(F08 50-AC-10)
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

// SetRecoveryBundle replaces the journal's recovery_bundle object; the next journal save persists it.
//
// SC-STUB(F08 50-AC-10)
func (e *Engine) SetRecoveryBundle(s RecoveryBundleState) {}

// RecoveryBundle returns the journal's current recovery_bundle object.
//
// SC-STUB(F08 50-AC-10)
func (e *Engine) RecoveryBundle() (s RecoveryBundleState) { return }
