package installf09_test

import "testing"

// requireExit is a copy of the install package helper (F09 install tests moved here for the CI time budget).
func requireExit(t *testing.T, got, want int, out string) {
	t.Helper()
	if got != want {
		t.Fatalf("exit %d, want %d:\n%s", got, want, out)
	}
}

func merge(ms ...map[string]string) map[string]string {
	out := map[string]string{}
	for _, m := range ms {
		for k, v := range m {
			out[k] = v
		}
	}
	return out
}
