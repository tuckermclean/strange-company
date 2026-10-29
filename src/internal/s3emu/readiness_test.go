package s3emu_test

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"strange.company/sc/internal/s3emu"
)

// init runs before TestMain, so this file adds a second fake-weed mode without touching the existing TestMain.
// "impostor" models B1: the process answers HTTP 404 with an empty body on the S3 port (as a foreign listener that
// grabbed the port would), never logs weed's "Start Seaweed S3 API Server ... at http port <n>" line, and exits.
func init() {
	if os.Getenv("SC_S3EMU_FAKE_WEED") != "impostor" {
		return
	}
	port := ""
	for _, a := range os.Args[1:] {
		if v, ok := strings.CutPrefix(a, "-s3.port="); ok {
			port = v
		}
	}
	l, err := net.Listen("tcp", "127.0.0.1:"+port)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fake weed:", err)
		os.Exit(2)
	}
	go func() { _ = http.Serve(l, http.NotFoundHandler()) }()
	time.Sleep(1500 * time.Millisecond)
	fmt.Fprintln(os.Stderr, "fake weed: listen tcp: bind: address already in use")
	os.Exit(1)
}

// 06-AC-1 (review S8b2 B1): Start succeeds only once its own weed is serving. When the S3 port answers 404 but the
// weed process never reports its S3 listener and then dies, Start must return an error, not a ready emulator.
func TestStart_FailsWhenOwnWeedNeverBecomesReady_06AC1(t *testing.T) {
	t.Parallel()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	e, err := s3emu.Start(ctx, s3emu.Options{
		Binary:  exe,
		DataDir: t.TempDir(),
		Env:     []string{"SC_S3EMU_FAKE_WEED=impostor"},
	})
	if err == nil {
		_ = e.Stop()
		t.Fatalf("Start reported ready at %s against a listener that is not this weed (weed exited without serving S3)", e.Endpoint())
	}
	if ctx.Err() != nil {
		t.Fatalf("Start did not fail promptly after weed exited: %v", err)
	}
}
