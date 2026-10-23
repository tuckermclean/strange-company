//go:build fxleak

package fx

import "testing"

func TestLeakedViaGOFLAGS(t *testing.T) {
	t.Fatal("GOFLAGS of the launching process reached the suite")
}
