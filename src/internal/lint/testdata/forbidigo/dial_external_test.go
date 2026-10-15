// Fixture for 52-AC-6 (plan 52 section 9.4 D5): a unit test dialing a non-loopback address.
package forbidigo

import (
	"net"
	"testing"
)

// TestDialExternal must be flagged by netcheck (non-loopback dial address in a _test.go file).
func TestDialExternal(t *testing.T) {
	c, err := net.Dial("tcp", "example.com:80")
	if err != nil {
		t.Skip("offline")
	}
	_ = c.Close()
}
