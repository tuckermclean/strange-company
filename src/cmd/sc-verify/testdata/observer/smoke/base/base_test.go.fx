package fx

import "testing"

func TestBaseline(t *testing.T) {
	if Impl() != 0 {
		t.Fatal("baseline broken")
	}
}
