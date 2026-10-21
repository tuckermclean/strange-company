package pkg

import "testing"

func TestOld(t *testing.T) {
	if Old() != 2 {
		t.Fatal("want 1")
	}
}
