//go:build fxleak2

package fx

import "testing"

func TestLeakedViaGOENV(t *testing.T) {
	t.Fatal("GOENV of the launching process reached the suite")
}
