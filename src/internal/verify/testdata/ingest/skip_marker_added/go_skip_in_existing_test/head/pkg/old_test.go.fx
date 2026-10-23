package pkg

import "testing"

func TestOld(t *testing.T) {
	t.Skip("later")
	if Old() != 1 {
		t.Fatal("want 1")
	}
}
