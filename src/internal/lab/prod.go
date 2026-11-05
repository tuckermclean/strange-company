package lab

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"strange.company/sc/internal/clock"
)

// sshWaitEvery is the SSH poll interval; the 180 s bound is the context deadline Up gives each VM.
const sshWaitEvery = time.Second

// fetchTimeout bounds one image download.
const fetchTimeout = 30 * time.Minute

// ProductionEnv is the Env of the real sc-lab binary: /dev/kvm, an HTTP fetcher, qemu-system-x86_64, ssh-keygen,
// qemu-img, cloud-localds and a detached SeaweedFS s3-emulator.
func ProductionEnv(root string, stdout, stderr io.Writer) Env {
	return Env{
		Root:        root,
		CacheDir:    filepath.Join(root, ".cache", "lab-images"),
		RunBase:     "/tmp/sc-lab",
		RuntimeDir:  "/run/sc/lab",
		KVMPath:     "/dev/kvm",
		Fetcher:     httpFetcher{},
		Launcher:    qemuLauncher{},
		WaitSSH:     waitSSH,
		FreeBytes:   freeBytes,
		MemBytes:    memBytes,
		GenKeys:     genKeys,
		MakeOverlay: makeOverlay,
		MakeSeed:    makeSeed,
		StartS3:     startS3,
		Kill:        killProc,
		KillHard:    killHard,
		Alive:       alive,
		Clock:       clock.Real{},
		ProcName:    procName,
		PortOpen:    func(p int) bool { return sshBanner(p) == nil },
		Stdout:      stdout,
		Stderr:      stderr,
	}
}

type httpFetcher struct{}

func (httpFetcher) Fetch(ctx context.Context, url, dst string) error {
	if !strings.HasPrefix(url, "https://") {
		return fmt.Errorf("refusing non-https image URL %q", url)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	client := &http.Client{
		Timeout: fetchTimeout,
		CheckRedirect: func(r *http.Request, via []*http.Request) error {
			if r.URL.Scheme != "https" {
				return fmt.Errorf("refusing redirect to non-https %s", r.URL)
			}
			if len(via) >= 10 {
				return errors.New("too many redirects")
			}
			return nil
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// qemuLauncher starts QEMU detached from sc-lab (own session) so the guest outlives `sc-lab up`.
type qemuLauncher struct{}

func (qemuLauncher) Launch(_ context.Context, vm VM) error {
	cmd := exec.Command("qemu-system-x86_64", vm.Args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// sshBanner dials the forwarded port and expects an SSH identification line within two seconds.
func sshBanner(port int) error {
	c, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(port), 2*time.Second)
	if err != nil {
		return err
	}
	defer c.Close()
	if err := c.SetReadDeadline(clock.Real{}.Now().Add(2 * time.Second)); err != nil {
		return err
	}
	line, err := bufio.NewReader(c).ReadString('\n')
	if err != nil {
		return err
	}
	if !strings.HasPrefix(line, "SSH-") {
		return errors.New("no SSH banner")
	}
	return nil
}

func waitSSH(ctx context.Context, port int) error {
	clk := clock.Real{}
	var last error
	for {
		if last = sshBanner(port); last == nil {
			return nil
		}
		if err := clk.Sleep(ctx, sshWaitEvery); err != nil {
			return fmt.Errorf("SSH on 127.0.0.1:%d did not answer within 180 s: %v", port, last)
		}
	}
}

func freeBytes(path string) (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, err
	}
	return st.Bavail * uint64(st.Bsize), nil
}

func memBytes() (uint64, error) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if rest, ok := strings.CutPrefix(sc.Text(), "MemTotal:"); ok {
			fs := strings.Fields(rest)
			if len(fs) == 0 {
				break
			}
			kb, err := strconv.ParseUint(fs[0], 10, 64)
			return kb << 10, err
		}
	}
	return 0, errors.New("MemTotal not found in /proc/meminfo")
}

func run(ctx context.Context, name string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%s: %v: %s", name, err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func genKeys(ctx context.Context, dir string, nodes int) (Keys, error) {
	var k Keys
	k.PrivateKeyPath = filepath.Join(dir, "id_ed25519")
	if _, err := run(ctx, "ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "SC-LAB-SSH", "-f", k.PrivateKeyPath); err != nil {
		return k, err
	}
	pub, err := os.ReadFile(k.PrivateKeyPath + ".pub")
	if err != nil {
		return k, err
	}
	k.PublicKey = strings.TrimSpace(string(pub))
	for n := 1; n <= nodes; n++ {
		p := filepath.Join(dir, fmt.Sprintf("host-node-%02d", n))
		if _, err := run(ctx, "ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "", "-f", p); err != nil {
			return k, err
		}
		priv, err := os.ReadFile(p)
		if err != nil {
			return k, err
		}
		pb, err := os.ReadFile(p + ".pub")
		if err != nil {
			return k, err
		}
		fp, err := run(ctx, "ssh-keygen", "-lf", p+".pub")
		if err != nil {
			return k, err
		}
		fs := strings.Fields(fp)
		if len(fs) < 2 {
			return k, fmt.Errorf("unexpected ssh-keygen -l output %q", fp)
		}
		k.Hosts = append(k.Hosts, HostKey{Private: string(priv), Public: strings.TrimSpace(string(pb)), Fingerprint: fs[1]})
	}
	return k, nil
}

func makeOverlay(ctx context.Context, base, overlay string, sizeGiB int) error {
	_, err := run(ctx, "qemu-img", "create", "-q", "-f", "qcow2", "-b", base, "-F", "qcow2", overlay, strconv.Itoa(sizeGiB)+"G")
	return err
}

func makeSeed(ctx context.Context, s SeedFiles) error {
	_, err := run(ctx, "cloud-localds", "--filesystem", "iso9660", "--network-config", s.NetworkConfig, s.ISO, s.UserData, s.MetaData)
	return err
}

// startS3 starts the lab S3 (plan 05 section 14.6): `weed server -s3` detached on an internal 127.0.0.1 port over plain
// HTTP, and in front of it, detached, `sc-lab s3-front` on 127.0.0.1:9000 over TLS with the run-local certificate
// (PrepareS3). The front answers the bucket ?publicAccessBlock API that SeaweedFS lacks (it returns 501) and proxies the
// rest. It returns once both ports answer.
func startS3(ctx context.Context, runDir string) (S3Info, error) {
	prep, err := PrepareS3(runDir)
	if err != nil {
		return S3Info{}, err
	}
	self, err := os.Executable()
	if err != nil {
		return S3Info{}, err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return S3Info{}, err
	}
	upstream := ln.Addr().String()
	_, upPort, _ := net.SplitHostPort(upstream)
	_ = ln.Close()

	weedLog, err := os.Create(filepath.Join(runDir, "s3.log"))
	if err != nil {
		return S3Info{}, err
	}
	defer weedLog.Close()
	wcmd := exec.Command("weed", append([]string{"server"}, weedPlainArgs(prep.Args, upPort)...)...)
	wcmd.Stdout, wcmd.Stderr = weedLog, weedLog
	wcmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := wcmd.Start(); err != nil {
		return S3Info{}, err
	}
	pid := wcmd.Process.Pid
	_ = wcmd.Process.Release()

	frontLog, err := os.Create(filepath.Join(runDir, "s3-front.log"))
	if err != nil {
		_ = syscall.Kill(pid, syscall.SIGKILL)
		return S3Info{}, err
	}
	defer frontLog.Close()
	fcmd := exec.Command(self, "s3-front", "--listen", "127.0.0.1:9000", "--upstream", "http://"+upstream,
		"--cert", prep.CertFile, "--key", prep.KeyFile)
	fcmd.Stdout, fcmd.Stderr = frontLog, frontLog
	fcmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := fcmd.Start(); err != nil {
		_ = syscall.Kill(pid, syscall.SIGKILL)
		return S3Info{}, err
	}
	frontPID := fcmd.Process.Pid
	_ = fcmd.Process.Release()

	fail := func(err error) (S3Info, error) {
		_ = syscall.Kill(pid, syscall.SIGKILL)
		_ = syscall.Kill(frontPID, syscall.SIGKILL)
		return S3Info{}, err
	}
	for _, addr := range []string{upstream, "127.0.0.1:9000"} {
		if err := waitPort(ctx, addr); err != nil {
			return fail(fmt.Errorf("the s3-emulator did not answer on %s (see s3.log, s3-front.log): %w", addr, err))
		}
	}
	info := prep.Info
	info.PID, info.FrontPID = pid, frontPID
	return info, nil
}

// weedPlainArgs turns the TLS arguments of PrepareS3 into the internal plain-HTTP ones: the certificate flags go (the
// front terminates TLS) and the S3 port becomes port.
func weedPlainArgs(args []string, port string) []string {
	out := make([]string, 0, len(args))
	for _, a := range args {
		switch {
		case strings.HasPrefix(a, "-s3.cert.file="), strings.HasPrefix(a, "-s3.key.file="):
		case strings.HasPrefix(a, "-s3.port="):
			out = append(out, "-s3.port="+port)
		default:
			out = append(out, a)
		}
	}
	return out
}

// waitPort polls addr for up to a minute.
func waitPort(ctx context.Context, addr string) error {
	clk := clock.Real{}
	for i := 0; i < 120; i++ {
		if c, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
			c.Close()
			return nil
		}
		if err := clk.Sleep(ctx, 500*time.Millisecond); err != nil {
			return err
		}
	}
	return errors.New("timeout")
}

func killProc(pid int) error { return signalProc(pid, syscall.SIGTERM) }

// killHard sends SIGKILL.
func killHard(pid int) error { return signalProc(pid, syscall.SIGKILL) }

// alive reports whether pid exists and is not a zombie (a zombie qemu that nobody reaps has already exited).
func alive(pid int) bool {
	if err := syscall.Kill(pid, 0); err != nil && !errors.Is(err, syscall.EPERM) {
		return false
	}
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return false
	}
	// /proc/<pid>/stat is "pid (comm) S ..."; comm may contain spaces and parentheses, so cut at the last ')'.
	if i := strings.LastIndexByte(string(b), ')'); i >= 0 && len(b) > i+2 {
		return b[i+2] != 'Z' && b[i+2] != 'X'
	}
	return true
}

func signalProc(pid int, sig syscall.Signal) error {
	if err := syscall.Kill(pid, sig); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}

// procName reads the command name of a pid from /proc/<pid>/comm.
func procName(pid int) (string, error) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/comm")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}
