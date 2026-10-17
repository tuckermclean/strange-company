package toolbox

import (
	"fmt"
	"os"
	"path/filepath"
)

// init is the entrypoint helper of plan 02 section 14.5 steps 1: work dirs, nss_wrapper files, SSH key copy.
func (c *cmdCtx) init(args []string) int {
	if len(args) != 0 {
		fmt.Fprintln(c.err, "usage: sc-bootstrap toolbox init")
		return 2
	}
	run := c.getenv("SC_RUN_DIR", "/run/sc")
	in := c.getenv("SC_INPUT_DIR", "/run/sc-input")
	if err := os.MkdirAll(run, 0o700); err != nil {
		return c.fail(err)
	}
	for _, d := range []string{"home", "ssh", "aws", "tmp"} {
		if err := os.MkdirAll(filepath.Join(run, d), 0o700); err != nil {
			return c.fail(err)
		}
	}
	uid, gid := os.Getuid(), os.Getgid()
	passwd := fmt.Sprintf("sc-operator:x:%d:%d:sc-operator:%s:/bin/bash\n", uid, gid, filepath.Join(run, "home"))
	group := fmt.Sprintf("sc-operator:x:%d:\n", gid)
	if err := os.WriteFile(filepath.Join(run, "passwd"), []byte(passwd), 0o644); err != nil {
		return c.fail(err)
	}
	if err := os.WriteFile(filepath.Join(run, "group"), []byte(group), 0o644); err != nil {
		return c.fail(err)
	}
	for _, n := range []string{"id_key", "id_key.pub"} {
		b, err := os.ReadFile(filepath.Join(in, "ssh", n))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return c.fail(err)
		}
		dst := filepath.Join(run, "ssh", n)
		if err := os.WriteFile(dst, b, 0o600); err != nil {
			return c.fail(err)
		}
		if err := os.Chmod(dst, 0o600); err != nil {
			return c.fail(err)
		}
	}
	return 0
}

func (c *cmdCtx) fail(err error) int {
	fmt.Fprintln(c.err, "sc-bootstrap:", err)
	return 1
}
