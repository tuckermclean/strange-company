package pkg

import "testing"

func TestNew(t *testing.T) {
	if New() != 1 {
		t.Fatal("want 1")
	}
}
