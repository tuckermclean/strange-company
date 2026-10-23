package fx

import "testing"

func TestImplReturns42(t *testing.T) {
	if got := Impl(); got != 42 {
		t.Errorf("Impl() = %d, want 42", got)
	}
}

func TestImplIsInt(t *testing.T) {}
