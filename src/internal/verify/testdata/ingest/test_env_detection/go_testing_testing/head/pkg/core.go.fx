package pkg

import "testing"

func Core() int {
	if testing.Testing() {
		return 1
	}
	return 0
}
