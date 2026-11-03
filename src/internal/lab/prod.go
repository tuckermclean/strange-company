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

// sshWaitPolls polls of sshWaitEvery bound the SSH wait of one VM to 180 seconds (plan 05 section 14.6).
const (
	sshWaitPolls = 180
	sshWaitEvery = time.Second
)

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
		PortOpen:    func(p int) bool { return sshBanner(p) == nil },
		Stdout:      stdout,
		Stderr:      stderr,
	}
}

type httpFetcher struct{}

func (httpFetcher) Fetch(ctx context.Context, url, dst string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
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
	res := make(chan error, 1)
	go func() {
		line, err := bufio.NewReader(c).ReadString('\n')
		if err == nil && !strings.HasPrefix(line, "SSH-") {
			err = errors.New("no SSH banner")
		}
		res <- err
	}()
	select {
	case err := <-res:
		return err
	case <-time.After(2 * time.Second):
		return errors.New("no SSH banner within 2 s")
	}
}

func waitSSH(ctx context.Context, port int) error {
	clk := clock.Real{}
	var last error
	for i := 0; i < sshWaitPolls; i++ {
		if last = sshBanner(port); last == nil {
			return nil
		}
		if err := clk.Sleep(ctx, sshWaitEvery); err != nil {
			return err
		}
	}
	return fmt.Errorf("SSH on 127.0.0.1:%d did not answer within 180 s: %v", port, last)
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

// startS3 starts `weed server -s3` detached on port 9000 and returns once the port answers. The run-local TLS CA
// of plan 05 section 14.6 is not generated yet, so the endpoint is plain HTTP.
func startS3(ctx context.Context, runDir string) (S3Info, error) {
	data := filepath.Join(runDir, "s3")
	if err := os.MkdirAll(data, 0o755); err != nil {
		return S3Info{}, err
	}
	logf, err := os.Create(filepath.Join(runDir, "s3.log"))
	if err != nil {
		return S3Info{}, err
	}
	defer logf.Close()
	cmd := exec.Command("weed", "server", "-s3", "-ip=0.0.0.0", "-dir="+data, "-s3.port=9000",
		"-master.volumeSizeLimitMB=64", "-volume.max=16")
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return S3Info{}, err
	}
	pid := cmd.Process.Pid
	_ = cmd.Process.Release()
	clk := clock.Real{}
	for i := 0; i < 120; i++ {
		if c, err := net.DialTimeout("tcp", "127.0.0.1:9000", time.Second); err == nil {
			c.Close()
			return S3Info{Endpoint: "http://127.0.0.1:9000", PID: pid}, nil
		}
		if err := clk.Sleep(ctx, 500*time.Millisecond); err != nil {
			return S3Info{}, err
		}
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
	return S3Info{}, errors.New("the s3-emulator did not answer on 127.0.0.1:9000 (see s3.log)")
}

func killProc(pid int) error {
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}
