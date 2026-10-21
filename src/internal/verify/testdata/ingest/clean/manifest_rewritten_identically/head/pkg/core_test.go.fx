package pkg

import "testing"

func TestCore(t *testing.T) {
	if Core() != 1 {
		t.Fatal("want 1")
	}
}
