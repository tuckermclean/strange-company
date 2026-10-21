package pkg

import "testing"

func TestOld(t *testing.T) {
	if Old() != 1 {
		t.Fatal("want 1")
	}
}
