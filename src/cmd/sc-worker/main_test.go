package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// 27-AC-16 (plan 27 section 7.4): `sc-worker shim` is the shim process; it works in $SC_WORKSPACE like `run` and
// exits 0 when sc-worker has written `done`.
func TestShimSubcommand_ExitsZeroOnDone_27AC16(t *testing.T) {
	ws := t.TempDir()
	dir := filepath.Join(ws, ".sc-shim")
	if err := os.MkdirAll(dir, 0o770); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "done"), []byte("{}"), 0o660); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SC_WORKSPACE", ws)
	res := make(chan int, 1)
	go func() { res <- run([]string{"shim"}) }()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	select {
	case code := <-res:
		if code != 0 {
			t.Errorf("sc-worker shim exit code = %d, want 0", code)
		}
	case <-ctx.Done():
		t.Fatalf("sc-worker shim did not exit within 20 s of done")
	}
}
