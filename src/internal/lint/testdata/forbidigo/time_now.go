// Fixture for 52-AC-6 (plan 52 section 9.4 D8): reads the wall clock directly.
package forbidigo

import "time"

// Stamp must be flagged by forbidigo (time.Now outside internal/clock and main packages).
func Stamp() time.Time {
	return time.Now()
}

// Pause must be flagged by forbidigo (time.Sleep).
func Pause() {
	time.Sleep(time.Second)
}
