// Package calc is the tiny library the fake harness scenarios work on (plan 28 section 14.5).
package calc

// Add returns the sum of a and b. It is deliberately wrong: the fake tester writes tests that fail against it and
// the fake implementer fixes it.
func Add(a, b int) int { return 0 }

// Sub returns a minus b.
func Sub(a, b int) int { return a - b }
