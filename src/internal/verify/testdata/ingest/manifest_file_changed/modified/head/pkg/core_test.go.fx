package pkg

import "testing"

func TestCore(t *testing.T) {
	if Core() != 2 {
		t.Fatal("want 2")
	}
}
