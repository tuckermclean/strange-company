package fx

import "testing"

// TestStillRed fails against the stub and passes against the implementation.
func TestStillRed(t *testing.T) {
	if got := Impl(); got != 42 {
		t.Errorf("Impl() = %d, want 42", got)
	}
}
