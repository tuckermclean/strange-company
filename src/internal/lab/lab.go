// Package lab is the logic of sc-lab, the QEMU lab substrate driver (plan 05 section 14.6, ADR-0105). Every host
// effect (KVM, image fetch, QEMU launch, SSH wait, free disk, memory, key generation, ISO building, the S3
// emulator) enters through Env so that tests need no KVM or QEMU.
package lab

import (
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"hash/fnv"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"strange.company/sc/internal/clock"
)

const (
	gib = uint64(1) << 30

	seedMinFree = 60 * gib
	seedMinMem  = 8 * gib
)

// Fetcher downloads url into the file dst (the image cache).
type Fetcher interface {
	Fetch(ctx context.Context, url, dst string) error
}

// VM is one QEMU guest to start.
type VM struct {
	Name string
	Args []string
}

// Launcher starts a QEMU guest. Up never calls it before the KVM, runner and image-checksum checks pass.
type Launcher interface {
	Launch(ctx context.Context, vm VM) error
}

// HostKey is one generated ed25519 host key pair (OpenSSH text) and its SHA256 fingerprint.
type HostKey struct {
	Private, Public, Fingerprint string
}

// Keys is the generated SC-LAB-SSH material of a run.
type Keys struct {
	PrivateKeyPath string // the client key the harness uses (tmpfs)
	PublicKey      string // its public key line
	Hosts          []HostKey
}

// S3Info describes the started s3-emulator.
type S3Info struct {
	Endpoint string // toolbox-side endpoint
	CAFile   string // optional run-local CA
	PID      int
	FrontPID int // the TLS front in front of the emulator (s3-front.pid); 0 when there is none
}

// SeedFiles are the NoCloud inputs of one VM.
type SeedFiles struct {
	ISO, UserData, MetaData, NetworkConfig string
}

// Env is the injectable environment of sc-lab. A nil optional seam (WaitSSH, GenKeys, MakeOverlay, MakeSeed,
// StartS3, Kill, Stdout, Stderr) is a no-op; Fetcher, Launcher, FreeBytes and MemBytes are required.
type Env struct {
	Root       string // repository root holding versions/lab-images.lock.yaml and infra/lab/lab.yaml
	CacheDir   string // image cache (.cache/lab-images)
	RunBase    string // parent of the per-run directory (/tmp/sc-lab)
	RuntimeDir string // parent of the generated bootstrap.env (/run/sc/lab)
	KVMPath    string // /dev/kvm
	Fetcher    Fetcher
	Launcher   Launcher
	WaitSSH    func(ctx context.Context, port int) error
	FreeBytes  func(path string) (uint64, error) // free bytes of the filesystem holding path
	MemBytes   func() (uint64, error)            // total memory in bytes

	GenKeys     func(ctx context.Context, dir string, nodes int) (Keys, error)
	MakeOverlay func(ctx context.Context, base, overlay string, sizeGiB int) error
	MakeSeed    func(ctx context.Context, s SeedFiles) error
	StartS3     func(ctx context.Context, runDir string) (S3Info, error)
	Kill        func(pid int) error
	PortOpen    func(port int) bool // status: SSH forward reachable

	// ProcName returns the command name (/proc/<pid>/comm) of a pid; Down kills only qemu and weed.
	ProcName func(pid int) (string, error)

	// Alive reports whether pid still exists; Down waits for killed pids to exit (nil: no wait).
	Alive func(pid int) bool
	// KillHard sends SIGKILL to pids that survive Kill (SIGTERM) past the deadline.
	KillHard func(pid int) error
	// Clock measures the Down deadline.
	Clock clock.Clock

	Stdout io.Writer
	Stderr io.Writer
}

// UpOptions are the flags of `sc-lab up`.
type UpOptions struct {
	Substrate string // lab-small, lab-ha or lab-seed
	OS        string // debian-13 or ubuntu-24.04
	RunID     string
}

// Error carries a stable error code (LAB_NO_KVM, LAB_IMAGE_CHECKSUM_MISMATCH, LAB_RUNNER_DISK_LOW,
// LAB_RUNNER_MEMORY_LOW, ...).
type Error struct {
	Code string
	Msg  string
}

func (e *Error) Error() string { return e.Code + ": " + e.Msg }

func errf(code, format string, a ...any) *Error {
	return &Error{Code: code, Msg: fmt.Sprintf(format, a...)}
}

type labSpec struct {
	Spec struct {
		InternalNetwork string                `yaml:"internalNetwork"`
		SSHPortBase     int                   `yaml:"sshPortBase"`
		Substrates      map[string]substrateS `yaml:"substrates"`
	} `yaml:"spec"`
}

type substrateS struct {
	Nodes      int    `yaml:"nodes"`
	VCPU       int    `yaml:"vcpu"`
	Memory     string `yaml:"memory"`
	Disk       string `yaml:"disk"`
	DiskDevice string `yaml:"diskDevice"`
}

type imageLock struct {
	Spec struct {
		Images map[string]struct {
			Serial string `yaml:"serial"`
			AMD64  struct {
				URL    string `yaml:"url"`
				SHA256 string `yaml:"sha256"`
				SHA512 string `yaml:"sha512"`
			} `yaml:"amd64"`
		} `yaml:"images"`
	} `yaml:"spec"`
}

func loadYAML(path string, out any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return errf("LAB_CONFIG", "%v", err)
	}
	if err := yaml.Unmarshal(b, out); err != nil {
		return errf("LAB_CONFIG", "%s: %v", path, err)
	}
	return nil
}

func (e Env) out() io.Writer {
	if e.Stdout == nil {
		return io.Discard
	}
	return e.Stdout
}

func (e Env) errw() io.Writer {
	if e.Stderr == nil {
		return io.Discard
	}
	return e.Stderr
}

// reRunID keeps lab-<id> a valid CLUSTER_NAME (2-31 characters, no edge dashes).
var reRunID = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,26}[a-z0-9])?$`)

var validID = reRunID.MatchString

const runIDUsage = "--run-id must be 1-28 characters of a-z, 0-9 and '-', not starting or ending with '-'"

// sshWaitTimeout bounds the SSH wait of one VM (plan 05 section 14.6).
const sshWaitTimeout = 180 * time.Second

// checkKVM opens the KVM device; there is no TCG fallback.
func checkKVM(path string) error {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return errf("LAB_NO_KVM", "cannot open %s (%v); lab VMs need hardware virtualization, TCG emulation is not supported", path, err)
	}
	return f.Close()
}

func parseGi(s string) (int, error) {
	n, err := strconv.Atoi(strings.TrimSuffix(s, "Gi"))
	if err != nil || n < 1 || !strings.HasSuffix(s, "Gi") {
		return 0, errf("LAB_CONFIG", "bad size %q (want <n>Gi)", s)
	}
	return n, nil
}

// segmentPort derives the multicast port of the private L2 segment from the run id.
func segmentPort(runID string) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(runID))
	return 20000 + int(h.Sum32()%20000)
}

func verifyFile(path, want256, want512 string) error {
	check := func(h hash.Hash, want, name string) error {
		if want == "" {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		h.Reset()
		if _, err := io.Copy(h, f); err != nil {
			return err
		}
		if got := hex.EncodeToString(h.Sum(nil)); got != want {
			return errf("LAB_IMAGE_CHECKSUM_MISMATCH", "%s of %s is %s, the lock says %s", name, filepath.Base(path), got, want)
		}
		return nil
	}
	if err := check(sha256.New(), want256, "sha256"); err != nil {
		return err
	}
	return check(sha512.New(), want512, "sha512")
}

// ensureImage returns the verified cached image; a download is verified before it enters the cache.
func ensureImage(ctx context.Context, env Env, url, s256, s512 string) (string, error) {
	if err := os.MkdirAll(env.CacheDir, 0o755); err != nil {
		return "", errf("LAB_CONFIG", "%v", err)
	}
	if s256 == "" && s512 == "" {
		return "", errf("LAB_IMAGE_CHECKSUM_MISMATCH", "the lock entry for %s has neither sha256 nor sha512; refusing an unverifiable image", url)
	}
	// The cache path carries a digest of the pinned checksums, so a lock bump that keeps the basename re-fetches.
	key := sha256.Sum256([]byte(s256 + "\n" + s512))
	dst := filepath.Join(env.CacheDir, hex.EncodeToString(key[:6])+"-"+filepath.Base(url))
	if _, err := os.Stat(dst); err == nil {
		if err := verifyFile(dst, s256, s512); err != nil {
			return "", fmt.Errorf("%w (cached file %s; delete it to fetch again)", err, dst)
		}
		return dst, nil
	}
	part := dst + ".part"
	_ = os.Remove(part)
	if err := env.Fetcher.Fetch(ctx, url, part); err != nil {
		_ = os.Remove(part)
		return "", errf("LAB_IMAGE_FETCH_FAILED", "%s: %v", url, err)
	}
	if err := verifyFile(part, s256, s512); err != nil {
		_ = os.Remove(part)
		return "", err
	}
	if err := os.Rename(part, dst); err != nil {
		return "", errf("LAB_CONFIG", "%v", err)
	}
	return dst, nil
}

// Up is `sc-lab up`: KVM check, runner check (lab-seed), image fetch and checksum, then launch.
func Up(ctx context.Context, env Env, opts UpOptions) error {
	if !validID(opts.RunID) {
		return errf("LAB_USAGE", runIDUsage)
	}
	var spec labSpec
	if err := loadYAML(filepath.Join(env.Root, "infra", "lab", "lab.yaml"), &spec); err != nil {
		return err
	}
	sub, ok := spec.Spec.Substrates[opts.Substrate]
	if !ok {
		return errf("LAB_USAGE", "unknown substrate %q", opts.Substrate)
	}
	var lock imageLock
	if err := loadYAML(filepath.Join(env.Root, "versions", "lab-images.lock.yaml"), &lock); err != nil {
		return err
	}
	img, ok := lock.Spec.Images[opts.OS]
	if !ok {
		return errf("LAB_USAGE", "unknown --os %q", opts.OS)
	}
	if opts.Substrate == "lab-seed" && opts.OS != "debian-13" {
		return errf("LAB_USAGE", "lab-seed runs Debian 13 only")
	}
	memGi, err := parseGi(sub.Memory)
	if err != nil {
		return err
	}
	diskGi, err := parseGi(sub.Disk)
	if err != nil {
		return err
	}

	if err := checkKVM(env.KVMPath); err != nil {
		return err
	}
	fmt.Fprintln(env.out(), "sc-lab: kvm ok")

	runDir := filepath.Join(env.RunBase, opts.RunID)
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		return errf("LAB_CONFIG", "%v", err)
	}
	if opts.Substrate == "lab-seed" {
		free, err := env.FreeBytes(runDir)
		if err != nil {
			return errf("LAB_CONFIG", "free space of %s: %v", runDir, err)
		}
		if free < seedMinFree {
			return errf("LAB_RUNNER_DISK_LOW", "%s has %d GiB free, lab-seed needs 60", runDir, free/gib)
		}
		mem, err := env.MemBytes()
		if err != nil {
			return errf("LAB_CONFIG", "memory: %v", err)
		}
		if mem < seedMinMem {
			return errf("LAB_RUNNER_MEMORY_LOW", "%d GiB of memory, lab-seed needs 8", mem/gib)
		}
	}

	base, err := ensureImage(ctx, env, img.AMD64.URL, img.AMD64.SHA256, img.AMD64.SHA512)
	if err != nil {
		return err
	}
	fmt.Fprintf(env.out(), "sc-lab: image %s verified\n", filepath.Base(base))

	rtDir := filepath.Join(env.RuntimeDir, opts.RunID)
	if err := os.MkdirAll(rtDir, 0o700); err != nil {
		return errf("LAB_CONFIG", "%v", err)
	}
	var keys Keys
	if env.GenKeys != nil {
		if keys, err = env.GenKeys(ctx, rtDir, sub.Nodes); err != nil {
			return errf("LAB_KEYGEN_FAILED", "%v", err)
		}
	}

	user := "debian"
	if strings.HasPrefix(opts.OS, "ubuntu") {
		user = "ubuntu"
	}
	netPrefix := strings.TrimSuffix(spec.Spec.InternalNetwork, "0/24")
	port := segmentPort(opts.RunID)
	var vms []VM
	var nodeLines, overrides []string
	for n := 1; n <= sub.Nodes; n++ {
		name := fmt.Sprintf("node-%02d", n)
		sshPort := spec.Spec.SSHPortBase + n
		ip := fmt.Sprintf("%s1%d", netPrefix, n)
		overlay := filepath.Join(runDir, name+".qcow2")
		if env.MakeOverlay != nil {
			if err := env.MakeOverlay(ctx, base, overlay, diskGi); err != nil {
				return errf("LAB_OVERLAY_FAILED", "%s: %v", name, err)
			}
		}
		seed := SeedFiles{
			ISO:           filepath.Join(runDir, name+"-seed.iso"),
			UserData:      filepath.Join(runDir, name+"-user-data"),
			MetaData:      filepath.Join(runDir, name+"-meta-data"),
			NetworkConfig: filepath.Join(runDir, name+"-network-config"),
		}
		hostname := fmt.Sprintf("lab-%s-node-%02d", opts.RunID, n)
		var hk HostKey
		if n-1 < len(keys.Hosts) {
			hk = keys.Hosts[n-1]
		}
		if err := writeSeed(seed, user, hostname, keys.PublicKey, hk, ip, n); err != nil {
			return errf("LAB_CONFIG", "%v", err)
		}
		if env.MakeSeed != nil {
			if err := env.MakeSeed(ctx, seed); err != nil {
				return errf("LAB_SEED_FAILED", "%s: %v", name, err)
			}
		}
		vms = append(vms, VM{Name: name, Args: qemuArgs(runDir, name, n, sshPort, port, sub, memGi, overlay, seed.ISO)})
		nodeLines = append(nodeLines, fmt.Sprintf("%s=%s@127.0.0.1:%d/%s", name, user, sshPort, ip))
		if sub.DiskDevice == "virtio-blk" {
			overrides = append(overrides, name+":disk_class=ssd")
		}
		if hk.Fingerprint != "" {
			overrides = append(overrides, name+":host_key="+hk.Fingerprint)
		}
	}

	for _, vm := range vms {
		if err := env.Launcher.Launch(ctx, vm); err != nil {
			return errf("LAB_LAUNCH_FAILED", "%s: %v", vm.Name, err)
		}
	}
	if env.WaitSSH != nil {
		for n := range vms {
			wctx, cancel := context.WithDeadline(ctx, clock.Real{}.Now().Add(sshWaitTimeout))
			err := env.WaitSSH(wctx, spec.Spec.SSHPortBase+n+1)
			cancel()
			if err != nil {
				return errf("LAB_SSH_TIMEOUT", "%s: %v", vms[n].Name, err)
			}
		}
	}
	var s3 S3Info
	if env.StartS3 != nil {
		if s3, err = env.StartS3(ctx, runDir); err != nil {
			return errf("LAB_S3_FAILED", "%v", err)
		}
		if s3.PID > 0 {
			_ = os.WriteFile(filepath.Join(runDir, "s3.pid"), []byte(strconv.Itoa(s3.PID)+"\n"), 0o644)
		}
		if s3.FrontPID > 0 {
			_ = os.WriteFile(filepath.Join(runDir, "s3-front.pid"), []byte(strconv.Itoa(s3.FrontPID)+"\n"), 0o644)
		}
	}
	envFile := filepath.Join(rtDir, "bootstrap.env")
	body := bootstrapEnv(opts, sub.Nodes, nodeLines, overrides, keys, s3)
	if err := os.WriteFile(envFile, []byte(body), 0o600); err != nil {
		return errf("LAB_CONFIG", "%v", err)
	}
	fmt.Fprintln(env.out(), envFile)
	return nil
}

func qemuArgs(runDir, name string, n, sshPort, segPort int, sub substrateS, memGi int, overlay, iso string) []string {
	a := []string{
		"-enable-kvm", "-cpu", "host", "-machine", "q35",
		"-smp", strconv.Itoa(sub.VCPU), "-m", strconv.Itoa(memGi * 1024),
		"-nographic", "-monitor", "none",
		"-serial", "file:" + filepath.Join(runDir, name+".console"),
		"-pidfile", filepath.Join(runDir, name+".pid"),
	}
	if sub.DiskDevice == "scsi-hd-ssd" {
		a = append(a, "-device", "virtio-scsi-pci,id=scsi0",
			"-drive", "file="+overlay+",if=none,id=d0,format=qcow2",
			"-device", "scsi-hd,drive=d0,rotation_rate=1")
	} else {
		a = append(a, "-drive", "file="+overlay+",if=none,id=d0,format=qcow2",
			"-device", "virtio-blk-pci,drive=d0")
	}
	a = append(a,
		"-drive", "file="+iso+",if=none,id=cd0,format=raw,media=cdrom,readonly=on",
		"-device", "ide-cd,drive=cd0",
		"-netdev", fmt.Sprintf("user,id=n0,hostfwd=tcp:127.0.0.1:%d-:22", sshPort),
		"-device", "virtio-net-pci,netdev=n0,mac="+mac(0, n),
		"-netdev", fmt.Sprintf("socket,id=n1,mcast=230.0.0.1:%d,localaddr=127.0.0.1", segPort),
		"-device", "virtio-net-pci,netdev=n1,mac="+mac(1, n),
	)
	return a
}

func mac(nic, n int) string { return fmt.Sprintf("52:54:00:77:%02x:%02x", nic, n) }

func indent(s, pad string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i := range lines {
		lines[i] = pad + lines[i]
	}
	return strings.Join(lines, "\n")
}

func writeSeed(s SeedFiles, user, hostname, pubKey string, hk HostKey, ip string, n int) error {
	var u strings.Builder
	u.WriteString("#cloud-config\n")
	fmt.Fprintf(&u, "hostname: %s\nmanage_etc_hosts: false\nssh_pwauth: false\ndisable_root: true\n", hostname)
	fmt.Fprintf(&u, "users:\n  - name: %s\n    sudo: ALL=(ALL) NOPASSWD:ALL\n    shell: /bin/bash\n    lock_passwd: true\n", user)
	if pubKey != "" {
		fmt.Fprintf(&u, "    ssh_authorized_keys:\n      - %s\n", strings.TrimSpace(pubKey))
	}
	if hk.Private != "" {
		fmt.Fprintf(&u, "ssh_keys:\n  ed25519_private: |\n%s\n  ed25519_public: %s\n", indent(hk.Private, "    "), strings.TrimSpace(hk.Public))
	}
	u.WriteString("bootcmd:\n  - [ sh, -c, \"grep -q s3.lab.internal /etc/hosts || echo '10.0.2.2 s3.lab.internal' >> /etc/hosts\" ]\n")
	meta := fmt.Sprintf("instance-id: %s\nlocal-hostname: %s\n", hostname, hostname)
	netc := fmt.Sprintf("version: 2\nethernets:\n  nic0:\n    match: {macaddress: \"%s\"}\n    set-name: nic0\n    dhcp4: true\n"+
		"  nic1:\n    match: {macaddress: \"%s\"}\n    set-name: nic1\n    dhcp4: false\n    addresses: [%s/24]\n", mac(0, n), mac(1, n), ip)
	for p, c := range map[string]string{s.UserData: u.String(), s.MetaData: meta, s.NetworkConfig: netc} {
		if err := os.WriteFile(p, []byte(c), 0o600); err != nil {
			return err
		}
	}
	return nil
}

func bootstrapEnv(o UpOptions, nodes int, nodeLines, overrides []string, k Keys, s3 S3Info) string {
	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format+"\n", a...) }
	w("CLUSTER_NAME=lab-%s", o.RunID)
	w("BASE_DOMAIN=lab.example.net")
	w("ADMIN_EMAIL=ci@example.net")
	w("PROFILE=%s", o.Substrate)
	w("PROVIDER=existing")
	w("EXPECTED_NODE_COUNT=%d", nodes)
	w("MAX_MONTHLY_COST=0")
	w("EXISTING_NODES=%s", strings.Join(nodeLines, ","))
	if len(overrides) > 0 {
		w("EXISTING_NODE_OVERRIDES=%s", strings.Join(overrides, ";"))
	}
	if k.PrivateKeyPath != "" {
		w("SSH_PRIVATE_KEY_PATH=%s", k.PrivateKeyPath)
		w("SSH_PUBLIC_KEY_PATH=%s.pub", k.PrivateKeyPath)
	}
	w("INGRESS_MODE=direct")
	if s3.Endpoint != "" {
		w("S3_ENDPOINT=%s", s3.Endpoint)
		// The emulator listens on loopback (placement check PF-28, plan 36): the toolbox-side address is the private CIDR.
		w("S3_PRIVATE_CIDR=127.0.0.1/32")
		scheme, hostport, _ := strings.Cut(s3.Endpoint, "://")
		_, p, _ := strings.Cut(hostport, ":")
		w("SC_LAB_S3_INCLUSTER_ENDPOINT=%s://10.0.2.2:%s", scheme, p)
		if s3.CAFile != "" {
			w("SC_LAB_S3_CA_FILE=%s", s3.CAFile)
		}
	}
	w("S3_REGION=us-east-1")
	w("S3_FORCE_PATH_STYLE=true")
	// The lab has one S3 emulator in shared credential mode, so its state is genuinely unprotected; plan 06 BOOT-3
	// requires the explicit opt-in.
	w("S3_ALLOW_UNPROTECTED_STATE=true")
	// Fixed fakes (plan 05 section 14.6/14.7): the lab never calls Cloudflare, GitHub, a notification transport or a model
	// API (test mode with a lab-labeled substrate skips those checks); the s3-emulator accepts any credentials.
	w("S3_ACCESS_KEY_ID=lab-access-key")
	w("S3_SECRET_ACCESS_KEY=lab-secret-key-not-a-secret")
	w("CLOUDFLARE_ACCOUNT_ID=00000000000000000000000000000000")
	w("CLOUDFLARE_API_TOKEN=lab-fake-cloudflare-api-token")
	w("CLOUDFLARE_ACME_TOKEN=lab-fake-cloudflare-acme-token")
	w("GITHUB_MIRROR_REPO=lab-example/lab-mirror")
	w("GITHUB_MIRROR_TOKEN=lab-fake-github-mirror-token")
	w("GITHUB_PROBE_TOKEN=lab-fake-github-probe-token")
	w("GITHUB_MIRROR_READ_TOKEN=lab-fake-github-read-token")
	w("NOTIFY_TRANSPORT=webhook")
	w("NOTIFY_WEBHOOK_URL=https://notify.lab.example.net/hook")
	w("ANTHROPIC_API_KEY=lab-fake-anthropic-key")
	return b.String()
}

func readPIDs(runDir string) map[string]int {
	m := map[string]int{}
	files, _ := filepath.Glob(filepath.Join(runDir, "*.pid"))
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && pid > 1 {
			m[strings.TrimSuffix(filepath.Base(f), ".pid")] = pid
		}
	}
	return m
}

// ownedProc reports whether pid is a qemu, weed or sc-lab (the S3 front) process; without a ProcName seam or on a lookup error it is not.
// A lookup error is logged with the pid, unless the process is already gone.
func ownedProc(env Env, name string, pid int) bool {
	if env.ProcName == nil || pid == os.Getpid() {
		return false
	}
	comm, err := env.ProcName(pid)
	if err != nil {
		if env.Alive == nil || env.Alive(pid) {
			fmt.Fprintf(env.errw(), "sc-lab: skipping %s (pid %d): cannot read its process name: %v\n", name, pid, err)
		}
		return false
	}
	comm = strings.TrimSpace(comm)
	switch name {
	case "s3-front":
		return comm == "sc-lab"
	case "s3":
		return comm == "weed"
	}
	return strings.HasPrefix(comm, "qemu-system")
}

const (
	downPollEvery = 250 * time.Millisecond
	downTermWait  = 30 * time.Second // SIGTERM grace before SIGKILL
	downKillWait  = 10 * time.Second // wait after SIGKILL before giving up
)

// waitGone polls Alive for pids until none remain or d passes on the injected clock; it returns the survivors.
func waitGone(env Env, pids map[int]string, d time.Duration) map[int]string {
	clk := env.Clock
	if clk == nil {
		clk = clock.Real{}
	}
	var waited time.Duration
	for {
		alive := map[int]string{}
		for pid, name := range pids {
			if env.Alive(pid) {
				alive[pid] = name
			}
		}
		if len(alive) == 0 || waited >= d {
			return alive
		}
		if err := clk.Sleep(context.Background(), downPollEvery); err != nil {
			return alive
		}
		waited += downPollEvery
		pids = alive
	}
}

func pidList(m map[int]string) string {
	var parts []string
	for pid, name := range m {
		parts = append(parts, fmt.Sprintf("%s (pid %d)", name, pid))
	}
	sort.Strings(parts)
	return strings.Join(parts, ", ")
}

// Down kills the run's processes, waits for them to exit (SIGTERM, then SIGKILL after a bounded grace) and
// removes its directories. It is idempotent. If a process remains, the run directory is kept so a rerun can retry.
func Down(env Env, runID string) error {
	if !validID(runID) {
		return errf("LAB_USAGE", runIDUsage)
	}
	runDir := filepath.Join(env.RunBase, runID)
	killed := map[int]string{}
	if env.Kill != nil {
		for name, pid := range readPIDs(runDir) {
			if !ownedProc(env, name, pid) {
				continue
			}
			killed[pid] = name
			if err := env.Kill(pid); err != nil {
				fmt.Fprintf(env.errw(), "sc-lab: kill %s (pid %d): %v\n", name, pid, err)
			}
		}
	}
	if env.Alive != nil && len(killed) > 0 {
		left := waitGone(env, killed, downTermWait)
		if len(left) > 0 && env.KillHard != nil {
			for pid, name := range left {
				if err := env.KillHard(pid); err != nil {
					fmt.Fprintf(env.errw(), "sc-lab: SIGKILL %s (pid %d): %v\n", name, pid, err)
				}
			}
			left = waitGone(env, left, downKillWait)
		}
		if len(left) > 0 {
			return errf("LAB_DOWN", "processes still running: %s; run directory kept, rerun sc-lab down", pidList(left))
		}
	}
	for _, d := range []string{runDir, filepath.Join(env.RuntimeDir, runID)} {
		if err := os.RemoveAll(d); err != nil {
			return errf("LAB_CONFIG", "%v", err)
		}
	}
	return nil
}

// EnvFile returns the generated bootstrap.env of a run.
func EnvFile(env Env, runID string) ([]byte, error) {
	if !validID(runID) {
		return nil, errf("LAB_USAGE", runIDUsage)
	}
	b, err := os.ReadFile(filepath.Join(env.RuntimeDir, runID, "bootstrap.env"))
	if err != nil {
		return nil, errf("LAB_NO_RUN", "no generated bootstrap.env for run %q", runID)
	}
	return b, nil
}

// Status prints the pids, SSH reachability and console tail of every run.
func Status(env Env) error {
	var spec labSpec
	if err := loadYAML(filepath.Join(env.Root, "infra", "lab", "lab.yaml"), &spec); err != nil {
		return err
	}
	runs, _ := os.ReadDir(env.RunBase)
	for _, r := range runs {
		if !r.IsDir() {
			continue
		}
		dir := filepath.Join(env.RunBase, r.Name())
		for name, pid := range readPIDs(dir) {
			line := fmt.Sprintf("run=%s vm=%s pid=%d", r.Name(), name, pid)
			if n, err := strconv.Atoi(strings.TrimPrefix(name, "node-")); err == nil && env.PortOpen != nil {
				line += fmt.Sprintf(" ssh=%t", env.PortOpen(spec.Spec.SSHPortBase+n))
			}
			fmt.Fprintln(env.out(), line)
			if b, err := os.ReadFile(filepath.Join(dir, name+".console")); err == nil {
				ls := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
				if len(ls) > 5 {
					ls = ls[len(ls)-5:]
				}
				for _, l := range ls {
					fmt.Fprintln(env.out(), "  console: "+l)
				}
			}
		}
	}
	return nil
}

// Run is the sc-lab entry point (args exclude the program name). It returns the process exit code and prints
// errors as "sc-lab: <CODE>: <message>" on env.Stderr.
func Run(ctx context.Context, args []string, env Env) int {
	fail := func(err error) int {
		var le *Error
		if errors.As(err, &le) {
			fmt.Fprintf(env.errw(), "sc-lab: %s: %s\n", le.Code, le.Msg)
			if le.Code == "LAB_USAGE" {
				return 2
			}
			return 1
		}
		fmt.Fprintf(env.errw(), "sc-lab: LAB_ERROR: %v\n", err)
		return 1
	}
	if len(args) == 0 {
		return fail(errf("LAB_USAGE", "usage: sc-lab up|env|status|down [flags]"))
	}
	kv := map[string]string{}
	rest := args[1:]
	for i := 0; i < len(rest); i++ {
		k, ok := strings.CutPrefix(rest[i], "--")
		if !ok {
			return fail(errf("LAB_USAGE", "unexpected argument %q", rest[i]))
		}
		if name, v, has := strings.Cut(k, "="); has {
			kv[name] = v
			continue
		}
		if i+1 >= len(rest) {
			return fail(errf("LAB_USAGE", "flag --%s needs a value", k))
		}
		i++
		kv[k] = rest[i]
	}
	switch args[0] {
	case "up":
		if err := Up(ctx, env, UpOptions{Substrate: kv["substrate"], OS: kv["os"], RunID: kv["run-id"]}); err != nil {
			return fail(err)
		}
	case "env":
		b, err := EnvFile(env, kv["run-id"])
		if err != nil {
			return fail(err)
		}
		_, _ = env.out().Write(b)
	case "status":
		if err := Status(env); err != nil {
			return fail(err)
		}
	case "s3-front": // hidden: started by startS3, never by an operator
		if err := RunS3Front(ctx, kv["listen"], kv["upstream"], kv["cert"], kv["key"]); err != nil {
			return fail(err)
		}
	case "down":
		if err := Down(env, kv["run-id"]); err != nil {
			return fail(err)
		}
	default:
		return fail(errf("LAB_USAGE", "unknown command %q (want up, env, status or down)", args[0]))
	}
	return 0
}
