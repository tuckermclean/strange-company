package toolbox_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func sum(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

func tarGz(t *testing.T, name, content string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	_, _ = tw.Write([]byte(content))
	_ = tw.Close()
	_ = gz.Close()
	return buf.Bytes()
}

type served struct {
	srv  *httptest.Server
	hits map[string]*atomic.Int32
}

func serve(t *testing.T, files map[string][]byte, failFirst map[string]int) *served {
	t.Helper()
	s := &served{hits: map[string]*atomic.Int32{}}
	for p := range files {
		s.hits[p] = &atomic.Int32{}
	}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, ok := files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		n := int(s.hits[r.URL.Path].Add(1))
		if n <= failFirst[r.URL.Path] {
			http.Error(w, "transient", http.StatusInternalServerError)
			return
		}
		_, _ = w.Write(b)
	}))
	t.Cleanup(s.srv.Close)
	return s
}

type tool struct{ name, set, path, sha, extract string }

func fetchLock(base string, tools ...tool) string {
	var sb strings.Builder
	sb.WriteString("apiVersion: strange.company/v1alpha1\nkind: ToolboxLock\nmetadata:\n  name: fixture\nspec:\n  tools:\n")
	for _, tl := range tools {
		install := fmt.Sprintf("{to: /usr/local/bin/%s}", tl.name)
		if tl.extract != "" {
			install = fmt.Sprintf("{extract: %s, to: /usr/local/bin/%s}", tl.extract, tl.name)
		}
		fmt.Fprintf(&sb, `    - name: %s
      set: %s
      version: "1.0.0"
      url: %s%s
      sha256:
        amd64: "%s"
        arm64: "%s"
      install: %s
      versionCommand: [%s, --version]
      versionRegex: '^{version}$'
`, tl.name, tl.set, base, tl.path, tl.sha, tl.sha, install, tl.name)
	}
	return sb.String()
}

// installed lists regular files below dest (the exact layout under dest is not specified).
func installed(t *testing.T, dest string) []string {
	t.Helper()
	var out []string
	_ = filepath.Walk(dest, func(p string, fi os.FileInfo, err error) error {
		if err == nil && fi.Mode().IsRegular() {
			out = append(out, p)
		}
		return nil
	})
	return out
}

func hasInstalled(t *testing.T, dest, base string) bool {
	for _, p := range installed(t, dest) {
		if filepath.Base(p) == base {
			return true
		}
	}
	return false
}

func fastRetry() map[string]string { return map[string]string{"SC_TOOLBOX_RETRY_DELAY": "1ms"} }

// 02-AC-3 (fetch side): a wrong sha256 fails with TOOLBOX_CHECKSUM_MISMATCH:<tool>, installs nothing and is never retried.
func TestFetch_ChecksumMismatch_NamesToolInstallsNothingNoRetry_02AC3(t *testing.T) {
	s := serve(t, map[string][]byte{"/bad": []byte("payload")}, nil)
	dir := t.TempDir()
	lock := write(t, filepath.Join(dir, "lock.yaml"), fetchLock(s.srv.URL, tool{"badtool", "runtime", "/bad", strings.Repeat("a", 64), ""}), 0o644)
	dest := filepath.Join(dir, "dest")
	r := run(t, fastRetry(), "toolbox", "fetch", "--lock", lock, "--arch", "amd64", "--dest", dest)
	if r.code == 0 {
		t.Fatalf("exit 0, want non-zero\n%s", r.all())
	}
	if !strings.Contains(r.all(), "TOOLBOX_CHECKSUM_MISMATCH:badtool") {
		t.Fatalf("missing TOOLBOX_CHECKSUM_MISMATCH:badtool in %q", r.all())
	}
	if f := installed(t, dest); len(f) != 0 {
		t.Fatalf("files installed despite mismatch: %v", f)
	}
	if n := s.hits["/bad"].Load(); n != 1 {
		t.Fatalf("download attempted %d times, a checksum mismatch is never retried", n)
	}
}

// 02-AC-3: the digest is verified before extraction, so a corrupt archive with a wrong digest reports the mismatch.
func TestFetch_VerifiesBeforeExtracting_02AC3(t *testing.T) {
	s := serve(t, map[string][]byte{"/a.tgz": []byte("this is not a tar archive")}, nil)
	dir := t.TempDir()
	lock := write(t, filepath.Join(dir, "lock.yaml"), fetchLock(s.srv.URL, tool{"arch", "runtime", "/a.tgz", strings.Repeat("b", 64), "arch"}), 0o644)
	dest := filepath.Join(dir, "dest")
	r := run(t, fastRetry(), "toolbox", "fetch", "--lock", lock, "--arch", "amd64", "--dest", dest)
	if r.code == 0 || !strings.Contains(r.all(), "TOOLBOX_CHECKSUM_MISMATCH:arch") {
		t.Fatalf("want TOOLBOX_CHECKSUM_MISMATCH:arch and non-zero, got %d %q", r.code, r.all())
	}
	if f := installed(t, dest); len(f) != 0 {
		t.Fatalf("files installed: %v", f)
	}
}

// 02-AC-3 (positive control): correct digests install a plain binary and an extracted archive member.
func TestFetch_InstallsVerifiedBinaryAndArchiveMember_02AC3(t *testing.T) {
	bin := []byte("#!/bin/sh\necho plain\n")
	arc := tarGz(t, "x/inner", "#!/bin/sh\necho inner\n")
	s := serve(t, map[string][]byte{"/plain": bin, "/a.tgz": arc}, nil)
	dir := t.TempDir()
	lock := write(t, filepath.Join(dir, "lock.yaml"), fetchLock(s.srv.URL,
		tool{"plain", "runtime", "/plain", sum(bin), ""},
		tool{"inner", "runtime", "/a.tgz", sum(arc), "x/inner"}), 0o644)
	dest := filepath.Join(dir, "dest")
	r := run(t, fastRetry(), "toolbox", "fetch", "--lock", lock, "--arch", "amd64", "--dest", dest)
	if r.code != 0 {
		t.Fatalf("exit %d: %s", r.code, r.all())
	}
	for _, n := range []string{"plain", "inner"} {
		if !hasInstalled(t, dest, n) {
			t.Fatalf("%s not installed below %s: %v", n, dest, installed(t, dest))
		}
	}
}

// 02-AC-3 / plan 02 section 16: transport failures are retried, 3 attempts per file.
func TestFetch_RetriesTransportErrorsThreeAttempts_02AC3(t *testing.T) {
	bin := []byte("payload")
	s := serve(t, map[string][]byte{"/flaky": bin}, map[string]int{"/flaky": 2})
	dir := t.TempDir()
	lock := write(t, filepath.Join(dir, "lock.yaml"), fetchLock(s.srv.URL, tool{"flaky", "runtime", "/flaky", sum(bin), ""}), 0o644)
	dest := filepath.Join(dir, "dest")
	r := run(t, fastRetry(), "toolbox", "fetch", "--lock", lock, "--arch", "amd64", "--dest", dest)
	if r.code != 0 || !hasInstalled(t, dest, "flaky") {
		t.Fatalf("two transient failures then success must install: exit %d %q", r.code, r.all())
	}
	if n := s.hits["/flaky"].Load(); n != 3 {
		t.Fatalf("attempts = %d, want 3", n)
	}
}

func TestFetch_GivesUpAfterThreeAttempts_02AC3(t *testing.T) {
	bin := []byte("payload")
	s := serve(t, map[string][]byte{"/down": bin}, map[string]int{"/down": 100})
	dir := t.TempDir()
	lock := write(t, filepath.Join(dir, "lock.yaml"), fetchLock(s.srv.URL, tool{"down", "runtime", "/down", sum(bin), ""}), 0o644)
	dest := filepath.Join(dir, "dest")
	r := run(t, fastRetry(), "toolbox", "fetch", "--lock", lock, "--arch", "amd64", "--dest", dest)
	if r.code == 0 {
		t.Fatalf("exit 0 although the download never succeeded")
	}
	if n := s.hits["/down"].Load(); n != 3 {
		t.Fatalf("attempts = %d, want 3", n)
	}
	if strings.Contains(r.all(), "TOOLBOX_CHECKSUM_MISMATCH") {
		t.Fatalf("a transport failure must not be reported as a checksum mismatch: %q", r.all())
	}
}

// Plan 02 section 8: --set selects the tool set; a ci-only tool is not downloaded for --set runtime.
func TestFetch_SetRuntimeSkipsCiOnlyTools_02AC3(t *testing.T) {
	bin := []byte("payload")
	s := serve(t, map[string][]byte{"/rt": bin, "/ci": []byte("other")}, nil)
	dir := t.TempDir()
	lock := write(t, filepath.Join(dir, "lock.yaml"), fetchLock(s.srv.URL,
		tool{"rt", "runtime", "/rt", sum(bin), ""},
		tool{"cionly", "ci", "/ci", strings.Repeat("c", 64), ""}), 0o644)
	dest := filepath.Join(dir, "dest")
	r := run(t, fastRetry(), "toolbox", "fetch", "--lock", lock, "--arch", "amd64", "--dest", dest, "--set", "runtime")
	if r.code != 0 || !hasInstalled(t, dest, "rt") || hasInstalled(t, dest, "cionly") {
		t.Fatalf("exit %d, installed %v, out %q", r.code, installed(t, dest), r.all())
	}
}
