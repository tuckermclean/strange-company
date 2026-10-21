package fx

import (
	"fmt"
	"strings"
	"testing"
)

// TestNoisy prints an early mark, about 300 KiB of filler and a late mark, so that the tail of the log that emit
// prints is told apart from its beginning.
func TestNoisy(t *testing.T) {
	fmt.Println("EARLY-MARK")
	filler := strings.Repeat("x", 99)
	for i := 0; i < 3000; i++ {
		fmt.Println(filler)
	}
	fmt.Println("LATE-MARK")
}
