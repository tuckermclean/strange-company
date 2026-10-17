package toolbox

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

const maxDownload = 1 << 30

// errChecksum is permanent: a checksum mismatch is never retried (plan 02 section 16).
type errChecksum struct{ tool string }

func (e errChecksum) Error() string { return "TOOLBOX_CHECKSUM_MISMATCH:" + e.tool }

func (c *cmdCtx) retryDelays() [2]time.Duration {
	d := [2]time.Duration{5 * time.Second, 15 * time.Second}
	if v := c.env["SC_TOOLBOX_RETRY_DELAY"]; v != "" {
		if x, err := time.ParseDuration(v); err == nil {
			d = [2]time.Duration{x, x}
		}
	}
	return d
}

func (c *cmdCtx) fetch(args []string) int {
	fs := flag.NewFlagSet("toolbox fetch", flag.ContinueOnError)
	fs.SetOutput(c.err)
	lockPath := fs.String("lock", "", "tools.lock.yaml")
	arch := fs.String("arch", "amd64", "amd64|arm64")
	dest := fs.String("dest", "", "install root")
	set := fs.String("set", "", "runtime|ci (default: all)")
	if fs.Parse(args) != nil {
		return 2
	}
	if *lockPath == "" || *dest == "" || (*arch != "amd64" && *arch != "arm64") || (*set != "" && *set != "runtime" && *set != "ci") {
		fmt.Fprintln(c.err, "usage: sc-bootstrap toolbox fetch --lock <file> --arch <amd64|arm64> --dest <dir> [--set runtime|ci]")
		return 2
	}
	lock, _, err := readLock(*lockPath)
	if err != nil {
		fmt.Fprintln(c.err, "sc-bootstrap toolbox fetch:", err)
		return 2
	}
	delays := c.retryDelays()
	for _, t := range lock.Spec.Tools {
		if *set != "" && t.Set != *set {
			continue
		}
		want := strings.ToLower(t.SHA256[*arch])
		if want == "" || t.Inst.To == "" {
			fmt.Fprintf(c.err, "sc-bootstrap toolbox fetch: tool %s lacks a sha256 for %s or install.to\n", t.Name, *arch)
			return 2
		}
		var body []byte
		for attempt := 0; ; attempt++ {
			body, err = download(t.expandURL(*arch))
			if err == nil || attempt >= 2 {
				break
			}
			fmt.Fprintf(c.err, "toolbox fetch: %s: %v (attempt %d of 3)\n", t.Name, err, attempt+1)
			c.sleep(delays[attempt])
		}
		if err != nil {
			fmt.Fprintf(c.err, "TOOLBOX_DOWNLOAD_FAILED:%s: %v\n", t.Name, err)
			return 1
		}
		h := sha256.Sum256(body)
		if hex.EncodeToString(h[:]) != want {
			fmt.Fprintln(c.err, errChecksum{t.Name}.Error())
			return 1
		}
		data := body
		if t.Inst.Extract != "" {
			if data, err = extractMember(body, t.expand(t.Inst.Extract, *arch)); err != nil {
				fmt.Fprintf(c.err, "TOOLBOX_EXTRACT_FAILED:%s: %v\n", t.Name, err)
				return 1
			}
		}
		if err := install(*dest, t.Inst.To, data); err != nil {
			fmt.Fprintf(c.err, "TOOLBOX_INSTALL_FAILED:%s: %v\n", t.Name, err)
			return 1
		}
		fmt.Fprintf(c.out, "installed %s %s\n", t.Name, t.Ver)
	}
	return 0
}

func download(url string) ([]byte, error) {
	cl := &http.Client{Timeout: 10 * time.Minute}
	resp, err := cl.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxDownload+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxDownload {
		return nil, errors.New("download exceeds size limit")
	}
	return b, nil
}

func install(dest, to string, data []byte) error {
	target := filepath.Join(dest, filepath.Clean("/"+to))
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	tmp := target + ".tmp"
	if err := os.WriteFile(tmp, data, 0o755); err != nil {
		return err
	}
	return os.Rename(tmp, target)
}

func clean(n string) string { return strings.TrimPrefix(path.Clean(n), "./") }

// extractMember returns the named regular file from a tar, tar.gz or zip archive.
func extractMember(b []byte, member string) ([]byte, error) {
	member = clean(member)
	if zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b))); err == nil {
		for _, f := range zr.File {
			if clean(f.Name) == member {
				rc, err := f.Open()
				if err != nil {
					return nil, err
				}
				defer rc.Close()
				return io.ReadAll(io.LimitReader(rc, maxDownload))
			}
		}
		return nil, fmt.Errorf("member %q not in archive", member)
	}
	var r io.Reader = bytes.NewReader(b)
	if len(b) > 2 && b[0] == 0x1f && b[1] == 0x8b {
		gz, err := gzip.NewReader(r)
		if err != nil {
			return nil, err
		}
		r = gz
	}
	tr := tar.NewReader(r)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil, fmt.Errorf("member %q not in archive", member)
		}
		if err != nil {
			return nil, err
		}
		if h.Typeflag == tar.TypeReg && clean(h.Name) == member {
			return io.ReadAll(io.LimitReader(tr, maxDownload))
		}
	}
}
