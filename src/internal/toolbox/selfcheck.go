package toolbox

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const defaultEmbeddedLock = "/usr/share/strange-company/tools.lock.yaml"

type toolRow struct {
	Tool     string `json:"tool"`
	Expected string `json:"expected"`
	Actual   string `json:"actual"`
	OK       bool   `json:"ok"`
}

type selfcheckReport struct {
	Tools []toolRow `json:"tools"`
	Stale bool      `json:"stale"`
	// Embedded and Repo are the sha256 of the two locks (Repo empty when not compared).
	Embedded string `json:"embeddedLockSha256,omitempty"`
	Repo     string `json:"repoLockSha256,omitempty"`
}

func (r selfcheckReport) rowsOK() bool {
	for _, t := range r.Tools {
		if !t.OK {
			return false
		}
	}
	return true
}

func hexSum(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

func (c *cmdCtx) runSelfcheck(embeddedOnly bool) (selfcheckReport, error) {
	var rep selfcheckReport
	embPath := c.getenv("SC_EMBEDDED_LOCK", defaultEmbeddedLock)
	lock, raw, err := readLock(embPath)
	if err != nil {
		return rep, fmt.Errorf("embedded lock: %w", err)
	}
	rep.Embedded = hexSum(raw)
	// A toolbox-target image does not carry the ci-only tools.
	skipCI := false
	if b, err := os.ReadFile(filepath.Join(filepath.Dir(embPath), "image-info.json")); err == nil {
		var info struct {
			Target string `json:"target"`
		}
		if json.Unmarshal(b, &info) == nil && info.Target == "toolbox" {
			skipCI = true
		}
	}
	rep.Tools = []toolRow{}
	for _, t := range lock.Spec.Tools {
		if skipCI && t.Set == "ci" {
			continue
		}
		rep.Tools = append(rep.Tools, checkTool(t))
	}
	if root := c.env["SC_REPO_ROOT"]; !embeddedOnly && root != "" {
		b, err := os.ReadFile(filepath.Join(root, "toolbox", "tools.lock.yaml"))
		if err != nil {
			return rep, fmt.Errorf("repository lock: %w", err)
		}
		rep.Repo = hexSum(b)
		rep.Stale = rep.Repo != rep.Embedded
	}
	return rep, nil
}

func checkTool(t lockTool) toolRow {
	row := toolRow{Tool: t.Name, Expected: t.Ver}
	if len(t.VersionCommand) == 0 {
		row.Actual = "no versionCommand in lock"
		return row
	}
	re, err := regexp.Compile("(?m)" + strings.ReplaceAll(t.VersionRegex, "{version}", regexp.QuoteMeta(t.Ver)))
	if err != nil {
		row.Actual = "bad versionRegex: " + err.Error()
		return row
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var buf bytes.Buffer
	cmd := exec.CommandContext(ctx, t.VersionCommand[0], t.VersionCommand[1:]...)
	cmd.Stdout, cmd.Stderr = &buf, &buf
	err = cmd.Run()
	if ctx.Err() != nil {
		row.Actual = "timeout"
		return row
	}
	out := strings.TrimSpace(buf.String())
	if m := re.FindString(out); m != "" && err == nil {
		row.OK, row.Actual = true, m
		return row
	}
	if len(out) > 200 {
		out = out[:200]
	}
	if out == "" && err != nil {
		out = err.Error()
	}
	row.Actual = out
	return row
}

func (c *cmdCtx) selfcheck(args []string) int {
	fs := flag.NewFlagSet("toolbox selfcheck", flag.ContinueOnError)
	fs.SetOutput(c.err)
	asJSON := fs.Bool("json", false, "machine-readable output")
	embOnly := fs.Bool("embedded-only", false, "skip the comparison with the checkout's lock")
	if fs.Parse(args) != nil {
		return 2
	}
	rep, err := c.runSelfcheck(*embOnly)
	if err != nil {
		fmt.Fprintln(c.err, "sc-bootstrap toolbox selfcheck:", err)
		return 1
	}
	if *asJSON {
		enc := json.NewEncoder(c.out)
		enc.SetIndent("", "  ")
		_ = enc.Encode(rep)
	} else {
		for _, r := range rep.Tools {
			st := "ok"
			if !r.OK {
				st = "FAIL"
			}
			fmt.Fprintf(c.out, "%-4s %s expected=%s actual=%s\n", st, r.Tool, r.Expected, r.Actual)
		}
	}
	for _, r := range rep.Tools {
		if !r.OK {
			fmt.Fprintf(c.err, "TOOLBOX_VERSION_MISMATCH:%s\n", r.Tool)
		}
	}
	if rep.Stale {
		fmt.Fprintln(c.err, "TOOLBOX_STALE: the embedded tools.lock.yaml differs from $SC_REPO_ROOT/toolbox/tools.lock.yaml")
		return 1
	}
	if !rep.rowsOK() {
		return 1
	}
	return 0
}
