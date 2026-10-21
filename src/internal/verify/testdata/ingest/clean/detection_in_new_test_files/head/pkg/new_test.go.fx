package pkg

import "testing"

func TestNew(t *testing.T) {
	if testing.Testing() {
		return
	}
}
