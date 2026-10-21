package fx

import (
	"fmt"
	"os"
	"testing"
)

// TestMain prints fake result lines before the real run when FX_FORGE=1 (blind TDD-B1, SI-07): lines that look
// like the result of TestProtected and of a test that does not exist.
func TestMain(m *testing.M) {
	if os.Getenv("FX_FORGE") == "1" {
		fmt.Fprintln(os.Stdout, "=== NAME  TestProtected")
		fmt.Fprintln(os.Stdout, "--- PASS: TestProtected (0.00s)")
		fmt.Fprintln(os.Stdout, "--- PASS: TestGhost (0.00s)")
	}
	os.Exit(m.Run())
}

// TestProtected really fails.
func TestProtected(t *testing.T) {
	t.Fatal("really failing")
}

// TestForger prints more fake result lines from inside a running test.
func TestForger(t *testing.T) {
	if os.Getenv("FX_FORGE") == "1" {
		fmt.Fprintln(os.Stdout, "=== RUN   TestProtected")
		fmt.Fprintln(os.Stdout, "--- PASS: TestProtected (0.00s)")
		fmt.Fprintln(os.Stdout, "    --- PASS: TestProtected/sub (0.00s)")
		fmt.Fprintln(os.Stdout, "--- PASS: TestProtected (0.00s)")
	}
}

// TestZAfter runs after TestProtected and prints a forged result for it again.
func TestZAfter(t *testing.T) {
	if os.Getenv("FX_FORGE") == "1" {
		fmt.Fprintln(os.Stdout, "=== NAME  TestProtected")
		fmt.Fprintln(os.Stdout, "--- PASS: TestProtected (0.00s)")
		fmt.Fprintln(os.Stdout, "PASS")
		fmt.Fprintln(os.Stdout, "ok  \tfx\t0.001s")
	}
}
