package pkg

import "testing"

func TestNew(t *testing.T) {
	t.Skipper()
	skipped := testing.Shorty()
	_ = skipped
}
