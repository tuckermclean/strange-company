package worker

// ProcPids lists the pids found in a /proc-shaped directory (numeric entry names with a readable stat file); pids
// <= 1 are never listed, so that no sweep can ever signal -1, 0 or init.
// SC-STUB(F07 S4-N4-N5)
func ProcPids(root string) []int {
	return nil
}
