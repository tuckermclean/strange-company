package cli_test

import (
	"testing"

	"strange.company/sc/internal/bootstrap/cli"
)

// F09 slice S8 attempt 2, 03-AC-12 (plan 50 section 7.1): the typed DR confirmation fence-nodes has the flag form
// --confirm-fence-nodes "<cluster> <count>", recorded as Confirm["fence-nodes"].
func TestParse_ConfirmFenceNodesFlag_03AC12(t *testing.T) {
	for _, args := range [][]string{
		{"--non-interactive", "recover", "--confirm-fence-nodes", "lab-one 2"},
		{"--confirm-fence-nodes", "lab-one 2", "recover", "--non-interactive"},
		{"recover", "--confirm-fence-nodes=lab-one 2"},
	} {
		inv, err := cli.Parse(args)
		if err != nil {
			t.Errorf("03-AC-12: %v: %v", args, err)
			continue
		}
		if inv.Confirm["fence-nodes"] != "lab-one 2" {
			t.Errorf("03-AC-12: %v: Confirm[fence-nodes] = %q, want %q", args, inv.Confirm["fence-nodes"], "lab-one 2")
		}
	}
}
