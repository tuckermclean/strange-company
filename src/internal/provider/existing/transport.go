package existing

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"strange.company/sc/internal/clock"
	"strange.company/sc/internal/provider/discovery"
)

// DirTransport is the test-mode transport (SC_EXISTING_DISCOVERY_DIR, plan 05 section 14.7): nothing is dialled; the
// directory holds <node>.facts.json (the output of the discovery script) and <node>.hostkeys (ssh-keyscan output).
type DirTransport struct{ Dir string }

// HostKeys reads <node>.hostkeys.
func (d DirTransport) HostKeys(_ context.Context, n discovery.Node) ([]string, error) {
	b, err := os.ReadFile(filepath.Join(d.Dir, n.Name+".hostkeys"))
	if err != nil {
		return nil, err
	}
	var out []string
	for _, l := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out, nil
}

// Facts reads <node>.facts.json.
func (d DirTransport) Facts(_ context.Context, n discovery.Node, _ []string) ([]byte, error) {
	return os.ReadFile(filepath.Join(d.Dir, n.Name+".facts.json"))
}

// SSHTransport reaches the nodes with the OpenSSH client (plan 05 section 14.2 and 14.4).
type SSHTransport struct {
	// KeyPath is the operator's private key (SSH_PRIVATE_KEY_PATH); empty uses the agent or the default identities.
	KeyPath string
	// TempDir holds the per-connection known_hosts files.
	TempDir string
	Clock   clock.Clock
	// Timeout is the discovery timeout per node (infra/bootstrap/settings.yaml existing.discoveryTimeout, 60 s).
	Timeout time.Duration
}

// retryDelays are the waits between the three attempts (plan 05 section 16).
var retryDelays = []time.Duration{5 * time.Second, 15 * time.Second}

// checkHost refuses a host that ssh or ssh-keyscan would read as an option (-oProxyCommand=... runs a command).
func checkHost(host string) error {
	if host == "" || strings.HasPrefix(host, "-") {
		return fmt.Errorf("the host %q is not a valid ssh host (it is empty or starts with '-')", host)
	}
	return nil
}

// HostKeys runs ssh-keyscan once for ed25519, ecdsa and rsa keys.
func (t SSHTransport) HostKeys(ctx context.Context, n discovery.Node) ([]string, error) {
	if err := checkHost(n.Host); err != nil {
		return nil, err
	}
	var keys []string
	err := t.retry(ctx, func(ctx context.Context) error {
		cmd := exec.CommandContext(ctx, "ssh-keyscan", "-T", "10", "-p", strconv.Itoa(n.Port), "-t", "ed25519,ecdsa,rsa", "--", n.Host)
		var out, errb bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &errb
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("ssh-keyscan: %v: %s", err, strings.TrimSpace(errb.String()))
		}
		keys = keys[:0]
		for _, l := range strings.Split(out.String(), "\n") {
			if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") {
				keys = append(keys, l)
			}
		}
		if len(keys) == 0 {
			return errors.New("ssh-keyscan returned no host key")
		}
		return nil
	})
	return keys, err
}

// Facts runs the discovery script as sc-admin (set by configure's admin transition) and, if that user does not exist
// yet, as the EXISTING_NODES user, with StrictHostKeyChecking=yes against the captured keys.
func (t SSHTransport) Facts(ctx context.Context, n discovery.Node, hostKeys []string) ([]byte, error) {
	if err := checkHost(n.Host); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp(t.TempDir, "known-hosts-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	kh := filepath.Join(dir, "known_hosts")
	var lines strings.Builder
	for _, l := range hostKeys {
		f := strings.Fields(l)
		if len(f) < 3 {
			continue
		}
		host := n.Host
		if n.Port != 22 {
			host = fmt.Sprintf("[%s]:%d", n.Host, n.Port)
		}
		fmt.Fprintf(&lines, "%s %s %s\n", host, f[1], f[2])
	}
	if err := os.WriteFile(kh, []byte(lines.String()), 0o600); err != nil {
		return nil, err
	}
	users := []string{"sc-admin"}
	if n.User != "sc-admin" {
		users = append(users, n.User)
	}
	var lastErr error
	for _, u := range users {
		var out []byte
		lastErr = t.retry(ctx, func(ctx context.Context) error {
			tctx, cancel := context.WithTimeout(ctx, t.timeout())
			defer cancel()
			args := []string{"-o", "BatchMode=yes", "-o", "ConnectTimeout=10", "-o", "StrictHostKeyChecking=yes",
				"-o", "UserKnownHostsFile=" + kh, "-o", "GlobalKnownHostsFile=/dev/null", "-p", strconv.Itoa(n.Port)}
			if t.KeyPath != "" {
				args = append(args, "-i", t.KeyPath, "-o", "IdentitiesOnly=yes")
			}
			args = append(args, "--", u+"@"+n.Host, "sh -s")
			cmd := exec.CommandContext(tctx, "ssh", args...)
			cmd.Stdin = bytes.NewReader(discovery.Script())
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			if err := cmd.Run(); err != nil {
				return fmt.Errorf("ssh %s@%s: %v: %s", u, n.Host, err, strings.TrimSpace(stderr.String()))
			}
			out = stdout.Bytes()
			return nil
		})
		if lastErr == nil {
			return out, nil
		}
		if strings.Contains(lastErr.Error(), "Permission denied") {
			continue // the user does not exist or does not accept the key: try the next one
		}
		return nil, lastErr
	}
	return nil, lastErr
}

func (t SSHTransport) timeout() time.Duration {
	if t.Timeout > 0 {
		return t.Timeout
	}
	return 60 * time.Second
}

// retry tries f up to three times; connection failures right after a reboot are transient (plan 05 section 16). A
// permission failure is permanent and returned at once.
func (t SSHTransport) retry(ctx context.Context, f func(context.Context) error) error {
	clk := t.Clock
	if clk == nil {
		clk = clock.Real{}
	}
	err := f(ctx)
	for _, d := range retryDelays {
		if err == nil || ctx.Err() != nil || strings.Contains(err.Error(), "Permission denied") || strings.Contains(err.Error(), "Host key verification failed") {
			return err
		}
		if serr := clk.Sleep(ctx, d); serr != nil {
			return serr
		}
		err = f(ctx)
	}
	return err
}
