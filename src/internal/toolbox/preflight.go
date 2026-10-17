package toolbox

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type pfRow struct {
	ID, Status, Code, Msg string
}

func (c *cmdCtx) preflight(args []string) int {
	if len(args) != 0 {
		fmt.Fprintln(c.err, "usage: sc-bootstrap [--env-file <file>] preflight")
		return 2
	}
	vars, envErr := c.loadEnvFile()
	rows := []pfRow{c.pf01()}
	if envErr != nil {
		rows = append(rows, pfRow{"PF-06", "fail", "ENV_FILE_UNREADABLE", envErr.Error()})
	} else {
		rows = append(rows, c.pf06(vars))
	}
	failed := false
	for _, r := range rows {
		label := r.Status
		if r.Code != "" {
			label = r.Code + " (" + r.Status + ")"
		}
		fmt.Fprintf(c.out, "%s %s %s\n", r.ID, label, r.Msg)
		if r.Status == "fail" {
			failed = true
		}
	}
	if failed {
		return 3
	}
	return 0
}

func (c *cmdCtx) pf01() pfRow {
	rep, err := c.runSelfcheck(false)
	if err != nil {
		return pfRow{"PF-01", "fail", "TOOLBOX_UNREADABLE", err.Error()}
	}
	if rep.Stale {
		return pfRow{"PF-01", "fail", "TOOLBOX_STALE", "the running image was not built from this checkout's toolbox/tools.lock.yaml"}
	}
	var bad []string
	for _, t := range rep.Tools {
		if !t.OK {
			bad = append(bad, t.Tool)
		}
	}
	if len(bad) > 0 {
		return pfRow{"PF-01", "fail", "TOOLBOX_TOOL_MISMATCH", "tools not at their locked version: " + strings.Join(bad, ", ")}
	}
	msg := fmt.Sprintf("%d tools match tools.lock.yaml", len(rep.Tools))
	if ref := c.env["SC_TOOLBOX_IMAGE_REF"]; ref != "" {
		msg += "; image " + ref + " (" + c.env["SC_TOOLBOX_SOURCE"] + ")"
	}
	return pfRow{"PF-01", "pass", "", msg}
}

func (c *cmdCtx) expandHome(p string) string {
	if strings.HasPrefix(p, "~/") {
		if h := c.env["HOME"]; h != "" {
			return h + p[1:]
		}
	}
	return p
}

// reason is the error without the file name, which the caller shows as the operator wrote it.
func reason(err error) error {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err
	}
	return err
}

func (c *cmdCtx) pf06(vars map[string]string) pfRow {
	fail := func(code, msg string) pfRow { return pfRow{"PF-06", "fail", code, msg} }
	agent := strings.EqualFold(vars["SSH_USE_AGENT"], "true")
	if agent {
		sock := filepath.Join(c.getenv("SC_INPUT_DIR", "/run/sc-input"), "ssh-agent.sock")
		fi, err := os.Stat(sock)
		if err != nil || fi.Mode()&os.ModeSocket == 0 {
			return fail("SSH_AGENT_UNAVAILABLE", fmt.Sprintf("SSH_USE_AGENT=true but %s is not a unix socket", sock))
		}
		conn, err := net.DialTimeout("unix", sock, 2*time.Second)
		if err != nil {
			return fail("SSH_AGENT_UNAVAILABLE", fmt.Sprintf("SSH_USE_AGENT=true but %s does not accept connections: %v", sock, err))
		}
		conn.Close()
	}
	written := vars["SSH_PRIVATE_KEY_PATH"]
	if written == "" && agent {
		return pfRow{"PF-06", "pass", "", "ssh-agent socket present"}
	}
	// Inside the container the key is the mount $SC_INPUT_DIR/ssh/id_key; the env-file path is a host path.
	// The mount is checked first: a mounted key needs no HOME.
	var priv, shown string
	pubPath := vars["SSH_PUBLIC_KEY_PATH"]
	mounted := ""
	if in := c.env["SC_INPUT_DIR"]; in != "" {
		m := filepath.Join(in, "ssh", "id_key")
		if _, err := os.Stat(m); err == nil {
			mounted = m
		}
	}
	switch {
	case mounted != "":
		priv = mounted
		pubPath = mounted + ".pub"
		shown = c.expandHome(written)
		if written == "" {
			shown = mounted + " (mounted)"
		}
	case written != "":
		shown = c.expandHome(written)
		priv = shown
	default:
		// Plan 50 section 9.2 default, shown as the operator writes it (never the expanded container home).
		if c.env["HOME"] == "" {
			return fail("SSH_KEY_UNSET", "SSH_PRIVATE_KEY_PATH is not set and HOME is unset")
		}
		shown = "~/.ssh/id_ed25519"
		priv = c.expandHome(shown)
	}
	if pubPath == "" {
		pubPath = priv + ".pub"
	}
	pubShown := pubPath
	if written == "" && mounted == "" {
		pubShown = shown + ".pub"
	}
	pubPath = c.expandHome(pubPath)
	fi, err := os.Stat(priv)
	if err != nil {
		return fail("SSH_KEY_UNREADABLE", fmt.Sprintf("SSH_PRIVATE_KEY_PATH %s: %v", shown, reason(err)))
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return fail("SSH_KEY_PERMISSIONS", fmt.Sprintf("%s is accessible by group or others (mode %04o); chmod 600", shown, fi.Mode().Perm()))
	}
	data, err := os.ReadFile(priv)
	if err != nil {
		return fail("SSH_KEY_UNREADABLE", fmt.Sprintf("SSH_PRIVATE_KEY_PATH %s: %v", shown, reason(err)))
	}
	key, err := parseOpenSSHPrivate(data, agent)
	if err != nil {
		return fail("SSH_KEY_INVALID", fmt.Sprintf("%s: %v", shown, err))
	}
	if key.Type == "ssh-rsa" && key.Bits < 3072 {
		return fail("SSH_KEY_WEAK", fmt.Sprintf("%s: RSA key has %d bits, at least 3072 required", shown, key.Bits))
	}
	pb, err := os.ReadFile(pubPath)
	if err != nil {
		return fail("SSH_PUBKEY_UNREADABLE", fmt.Sprintf("SSH_PUBLIC_KEY_PATH %s: %v", pubShown, reason(err)))
	}
	typ, blob, err := parsePublicLine(pb)
	if err != nil {
		return fail("SSH_PUBKEY_INVALID", fmt.Sprintf("%s: %v", pubShown, err))
	}
	if typ != key.Type || !bytes.Equal(blob, key.Blob) {
		return fail("SSH_PUBKEY_MISMATCH", fmt.Sprintf("%s does not match the public key derived from %s", pubShown, shown))
	}
	return pfRow{"PF-06", "pass", "", fmt.Sprintf("%s: %s", shown, key.Type)}
}
