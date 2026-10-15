// Fixture for 52-AC-6 (plan 52 section 9.4 D7): uses the global math/rand source.
package forbidigo

import "math/rand"

// Pick must be flagged by forbidigo (math/rand global functions and rand.Seed outside testutil).
func Pick() int {
	rand.Seed(1)
	return rand.Intn(10)
}
