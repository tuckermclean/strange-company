package toolbox

import (
	"bytes"
	"fmt"
	"os"
	"strings"
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

func (c *cmdCtx) pf06(vars map[string]string) pfRow {
	fail := func(code, msg string) pfRow { return pfRow{"PF-06", "fail", code, msg} }
	priv := vars["SSH_PRIVATE_KEY_PATH"]
	if priv == "" {
		return fail("SSH_KEY_UNSET", "SSH_PRIVATE_KEY_PATH is not set")
	}
	priv = c.expandHome(priv)
	fi, err := os.Stat(priv)
	if err != nil {
		return fail("SSH_KEY_UNREADABLE", fmt.Sprintf("SSH_PRIVATE_KEY_PATH %s: %v", priv, err))
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return fail("SSH_KEY_PERMISSIONS", fmt.Sprintf("%s is accessible by group or others (mode %04o); chmod 600", priv, fi.Mode().Perm()))
	}
	data, err := os.ReadFile(priv)
	if err != nil {
		return fail("SSH_KEY_UNREADABLE", fmt.Sprintf("SSH_PRIVATE_KEY_PATH %s: %v", priv, err))
	}
	key, err := parseOpenSSHPrivate(data)
	if err != nil {
		return fail("SSH_KEY_INVALID", fmt.Sprintf("%s: %v", priv, err))
	}
	if key.Type == "ssh-rsa" && key.Bits < 3072 {
		return fail("SSH_KEY_WEAK", fmt.Sprintf("%s: RSA key has %d bits, at least 3072 required", priv, key.Bits))
	}
	if pubPath := vars["SSH_PUBLIC_KEY_PATH"]; pubPath != "" {
		pubPath = c.expandHome(pubPath)
		pb, err := os.ReadFile(pubPath)
		if err != nil {
			return fail("SSH_PUBKEY_UNREADABLE", fmt.Sprintf("SSH_PUBLIC_KEY_PATH %s: %v", pubPath, err))
		}
		typ, blob, err := parsePublicLine(pb)
		if err != nil {
			return fail("SSH_PUBKEY_INVALID", fmt.Sprintf("%s: %v", pubPath, err))
		}
		if typ != key.Type || !bytes.Equal(blob, key.Blob) {
			return fail("SSH_PUBKEY_MISMATCH", fmt.Sprintf("%s does not match the public key derived from %s", pubPath, priv))
		}
	}
	return pfRow{"PF-06", "pass", "", fmt.Sprintf("%s: %s", priv, key.Type)}
}
