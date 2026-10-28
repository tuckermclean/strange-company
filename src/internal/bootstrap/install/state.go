package install

import (
	"context"

	"strange.company/sc/internal/tofustate"
)

// SC-STUB(F08 06-AC-6 06-AC-8)
// StateTofu is the OpenTofu seam of the state commands (plan 06 sections 14.5, 20.1 to 20.3). Production runs tofu in
// a scratch work dir with the encryption in the process environment; tests inject a fake.
type StateTofu interface {
	// Decrypt returns the plaintext tfstate of one stored version of a module, decrypted with e. It fails when neither e.Current
	// nor the fallback e.Previous opens it.
	Decrypt(ctx context.Context, module string, raw []byte, e tofustate.Encryption) ([]byte, error)
	// PlanCopy runs `tofu plan -refresh=false` against a local, encrypted copy of raw and returns the number of changes.
	PlanCopy(ctx context.Context, module string, raw []byte, e tofustate.Encryption) (changes int, err error)
	// RefreshOnly runs `tofu apply -refresh-only` for the module against the remote backend: the state is rewritten
	// and encrypted with e.Current (e.Previous is the fallback that still reads the old version).
	RefreshOnly(ctx context.Context, module string, remote RemoteState, e tofustate.Encryption) error
}
