package existing_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"strange.company/sc/internal/clock"
	"strange.company/sc/internal/provider"
	"strange.company/sc/internal/provider/discovery"
	"strange.company/sc/internal/provider/existing"
)

// 05-AC-3 (plan 05 sections 9 and 16): a host key verification failure is permanent. The provider reports
// HOST_KEY_MISMATCH, ssh is invoked exactly once (no retry, no second user), and nothing is retryable.
func TestSSHTransport_HostKeyVerificationFailedIsMismatchWithoutRetry_05AC3(t *testing.T) {
	node, tr := fixture(t)
	dir := t.TempDir()
	log := filepath.Join(dir, "ssh.log")
	keyscan := "#!/bin/sh\ncat <<'EOF'\n" + strings.Join(tr.keys, "\n") + "\nEOF\n"
	ssh := "#!/bin/sh\necho \"$0 $*\" >> " + log + "\necho 'Host key verification failed.' >&2\nexit 255\n"
	for name, body := range map[string]string{"ssh-keyscan": keyscan, "ssh": ssh} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	p := existing.New(existing.Config{
		Nodes: []discovery.Node{node}, ProfileFile: profile, InstallUID: func() string { return uidA },
		Transport: existing.SSHTransport{TempDir: t.TempDir(), Clock: clock.NewFake(time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC))},
	})
	_, err := p.Plan(context.Background(), provider.Desired{Cluster: "lab-one", NodeCount: 1})
	var pe *provider.Error
	if !errors.As(err, &pe) || pe.Code != "HOST_KEY_MISMATCH" {
		t.Fatalf("Plan error = %v, want code HOST_KEY_MISMATCH", err)
	}
	if pe.Retryable() {
		t.Errorf("HOST_KEY_MISMATCH is retryable, want permanent")
	}
	if got := calls(t, log); len(got) != 1 {
		t.Errorf("ssh was invoked %d times, want exactly 1 (no retry, no second user): %v", len(got), got)
	}
}
