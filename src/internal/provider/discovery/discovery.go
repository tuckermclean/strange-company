// Package discovery is the shared node discovery library of plan 05 section 14.2: the fixed read-only POSIX script that
// a node runs over SSH, and the pure functions that turn its JSON output and the ssh-keyscan lines into a normalized
// ProviderOutput node (plan 05 section 7.2) plus the validation findings of section 14.5.
package discovery

import (
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/netip"
	"os"
	"sort"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

//go:embed discover.sh
var script []byte

// Node is one EXISTING_NODES entry with its EXISTING_NODE_OVERRIDES.
type Node struct {
	Name               string `json:"name"`
	Host               string `json:"host"`
	Port               int    `json:"port"`
	User               string `json:"user"`
	InternalIP         string `json:"internal_ip,omitempty"`
	DiskClassOverride  string `json:"disk_class_override,omitempty"`
	HostKeyFingerprint string `json:"host_key_fingerprint,omitempty"`
}

// Input is what is known about one node: its configuration, the JSON the script printed and the ssh-keyscan output.
type Input struct {
	Node     Node
	Facts    []byte
	HostKeys []string
}

// Context is what the evaluation needs besides the nodes.
type Context struct {
	// ProfileFile is the effective cluster profile (infra/profiles/<profile>.yaml).
	ProfileFile string
	// InstallUID is this installation's uid, compared with /etc/strange-company/install-uid.
	InstallUID string
}

// Finding is one validation result: Code is the plan 05 section 14.5 code without any suffix (the port or command goes in
// Message), Severity is info, warn or fail.
type Finding struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
}

// Result is the evaluation of one input. Node is the ProviderOutput node (contracts/provider.schema.yaml #/$defs/node)
// as a JSON-compatible value; Findings are the same findings as Node.substrate_validation.findings, including those that
// involve other nodes (EXISTING_MIXED_SIZE_CLASS, EXISTING_DUPLICATE_NODE, internal IP uniqueness).
type Result struct {
	Node     map[string]any
	Findings []Finding
	// Identity is what tells the machine apart from another one answering on the same address; it is not part of the
	// ProviderOutput node (it carries no schema field) and feeds the plan digest.
	Identity Identity
}

// Identity is the identifying facts of a discovered node: its machine id, the install marker it carries and the
// container platforms found on it.
type Identity struct {
	MachineID  string
	InstallUID string
	Platforms  []string
}

// Script returns the embedded discovery script (POSIX sh, read from stdin by `sh -s`): it prints one JSON document
// (test/fixtures/existing/discovery/README.md) and changes nothing on the node.
func Script() []byte { return append([]byte(nil), script...) }

const (
	sevInfo = "info"
	sevWarn = "warn"
	sevFail = "fail"
)

// flexInt accepts a JSON number or a numeric string (lsblk before util-linux 2.41 prints sizes as strings).
type flexInt int64

func (f *flexInt) UnmarshalJSON(b []byte) error {
	s := strings.Trim(strings.TrimSpace(string(b)), `"`)
	if s == "" || s == "null" {
		*f = 0
		return nil
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		fl, ferr := strconv.ParseFloat(s, 64)
		if ferr != nil {
			return fmt.Errorf("%q is not a number", s)
		}
		n = int64(fl)
	}
	*f = flexInt(n)
	return nil
}

// flexBool accepts a JSON boolean or "0", "1", "true", "false" (lsblk prints rota as a string before util-linux 2.41).
type flexBool bool

func (f *flexBool) UnmarshalJSON(b []byte) error {
	s := strings.Trim(strings.TrimSpace(string(b)), `"`)
	switch strings.ToLower(s) {
	case "1", "true", "yes", "y":
		*f = true
	default:
		*f = false
	}
	return nil
}

type blockDevice struct {
	Name        string        `json:"name"`
	KName       string        `json:"kname"`
	PKName      *string       `json:"pkname"`
	Size        flexInt       `json:"size"`
	Rota        flexBool      `json:"rota"`
	Type        string        `json:"type"`
	Tran        *string       `json:"tran"`
	Model       *string       `json:"model"`
	Mountpoint  *string       `json:"mountpoint"`
	Mountpoints []*string     `json:"mountpoints"`
	Children    []blockDevice `json:"children"`
}

type facts struct {
	ScriptVersion int     `json:"script_version"`
	Nproc         flexInt `json:"nproc"`
	CPUModel      string  `json:"cpu_model"`
	Meminfo       string  `json:"meminfo"`
	Lsblk         *struct {
		Blockdevices []blockDevice `json:"blockdevices"`
	} `json:"lsblk"`
	Findmnt *struct {
		Filesystems []struct {
			Target string   `json:"target"`
			Source string   `json:"source"`
			FSType string   `json:"fstype"`
			Size   *flexInt `json:"size"`
			Avail  *flexInt `json:"avail"`
		} `json:"filesystems"`
	} `json:"findmnt"`
	OSRelease string   `json:"os_release"`
	Kernel    string   `json:"kernel"`
	Arch      string   `json:"arch"`
	Virt      string   `json:"virt"`
	CgroupFS  string   `json:"cgroup_fs"`
	AppArmor  string   `json:"apparmor"`
	WireGuard flexBool `json:"wireguard"`
	NTP       string   `json:"ntp_synchronized"`
	Swap      string   `json:"swap"`
	IPAddr    []struct {
		IfName   string `json:"ifname"`
		AddrInfo []struct {
			Family string `json:"family"`
			Local  string `json:"local"`
			Scope  string `json:"scope"`
		} `json:"addr_info"`
	} `json:"ip_addr"`
	IPRoute []struct {
		PrefSrc string `json:"prefsrc"`
	} `json:"ip_route_get"`
	SS           string   `json:"ss"`
	Platforms    []string `json:"platforms"`
	InstallUID   string   `json:"install_uid"`
	SudoOK       flexBool `json:"sudo_ok"`
	MachineID    string   `json:"machine_id"`
	MissingTools []string `json:"missing_tools"`
}

// shape is one per-node capacity requirement of the effective profile.
type shape struct {
	cpu     int64
	memory  int64
	storage int64
	class   string
}

type profile struct {
	name        string
	constrained *shape
	minimum     *shape
	preferred   *shape
	servers     int
	storageSize int64 // minimumSupportedPerNode.storage.size, the base of the free-space rule
}

var classRank = map[string]int{"nvme": 3, "ssd": 2, "hdd": 1, "unknown": 0}

// parseQuantity parses 12Gi, 100Gi, 512Mi and plain integers.
func parseQuantity(s string) (int64, error) {
	s = strings.TrimSpace(s)
	mult := int64(1)
	for suffix, m := range map[string]int64{"Ki": 1 << 10, "Mi": 1 << 20, "Gi": 1 << 30, "Ti": 1 << 40, "K": 1000, "M": 1000000, "G": 1000000000, "T": 1000000000000} {
		if strings.HasSuffix(s, suffix) {
			mult = m
			s = strings.TrimSuffix(s, suffix)
			break
		}
	}
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%q is not a quantity", s)
	}
	return n * mult, nil
}

func loadProfile(path string) (*profile, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("discovery: the effective profile cannot be read: %w", err)
	}
	type rawShape struct {
		CPU     int64  `yaml:"cpu"`
		Memory  string `yaml:"memory"`
		Storage struct {
			Size  string `yaml:"size"`
			Class string `yaml:"class"`
		} `yaml:"storage"`
	}
	var doc struct {
		Metadata struct {
			Name string `yaml:"name"`
		} `yaml:"metadata"`
		Spec struct {
			Nodes struct {
				Minimum     *rawShape `yaml:"minimumSupportedPerNode"`
				Constrained *rawShape `yaml:"constrainedLowerBoundPerNode"`
				Preferred   *rawShape `yaml:"preferredPerNode"`
			} `yaml:"nodes"`
			Kubernetes struct {
				Servers int `yaml:"servers"`
			} `yaml:"kubernetes"`
		} `yaml:"spec"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("discovery: the effective profile %s is not YAML: %w", path, err)
	}
	conv := func(r *rawShape) (*shape, error) {
		if r == nil {
			return nil, nil
		}
		mem, err := parseQuantity(r.Memory)
		if err != nil {
			return nil, err
		}
		size, err := parseQuantity(r.Storage.Size)
		if err != nil {
			return nil, err
		}
		return &shape{cpu: r.CPU, memory: mem, storage: size, class: r.Storage.Class}, nil
	}
	p := &profile{name: doc.Metadata.Name, servers: doc.Spec.Kubernetes.Servers}
	var cerr error
	for _, x := range []struct {
		src *rawShape
		dst **shape
	}{{doc.Spec.Nodes.Minimum, &p.minimum}, {doc.Spec.Nodes.Constrained, &p.constrained}, {doc.Spec.Nodes.Preferred, &p.preferred}} {
		if *x.dst, cerr = conv(x.src); cerr != nil {
			return nil, fmt.Errorf("discovery: the effective profile %s: %w", path, cerr)
		}
	}
	if p.name == "" || p.minimum == nil {
		return nil, fmt.Errorf("discovery: the effective profile %s has no name or no minimumSupportedPerNode", path)
	}
	if p.constrained == nil {
		p.constrained = p.minimum
	}
	if p.servers < 1 {
		p.servers = 1
	}
	p.storageSize = p.minimum.storage
	return p, nil
}

// at least reports whether have >= 0.92 * want (plan 03 section 14.3), in integer arithmetic.
func atLeast(have, want int64) bool { return have*100 >= want*92 }

// eval is the evaluation of one input before the cross-node checks.
type eval struct {
	in       Input
	f        facts
	findings []Finding
	node     map[string]any

	memory       int64
	diskSize     int64
	diskClass    string
	cpu          int64
	meetsLower   bool
	meetsMinimum bool
	meetsPref    bool
	internal     string
	machineID    string
}

func (e *eval) add(code, sev, format string, a ...any) {
	e.findings = append(e.findings, Finding{Code: code, Severity: sev, Message: fmt.Sprintf(format, a...)})
}

// Evaluate normalizes and validates the nodes. Results are in the order of in, and node roles follow name order. Every
// failed check is a Finding of severity fail (also DISCOVERY_TOOL_MISSING and HOST_KEY_MISMATCH); the error is reserved
// for input that cannot be decoded at all.
func Evaluate(in []Input, c Context) ([]Result, error) {
	prof, err := loadProfile(c.ProfileFile)
	if err != nil {
		return nil, err
	}
	evs := make([]*eval, len(in))
	for i, inp := range in {
		e := &eval{in: inp}
		if err := json.Unmarshal(inp.Facts, &e.f); err != nil {
			return nil, fmt.Errorf("discovery: the facts of %s cannot be decoded: %w", inp.Node.Name, err)
		}
		e.check(prof, c)
		evs[i] = e
	}
	crossChecks(evs)

	names := make([]string, len(evs))
	for i, e := range evs {
		names[i] = e.in.Node.Name
	}
	sort.Strings(names)
	rank := map[string]int{}
	for i, n := range names {
		rank[n] = i
	}

	out := make([]Result, len(evs))
	for i, e := range evs {
		role := "agent"
		if rank[e.in.Node.Name] < prof.servers {
			role = "server"
		}
		e.node["roles"] = []string{role}
		fs := make([]any, 0, len(e.findings))
		for _, f := range e.findings {
			fs = append(fs, map[string]any{"code": f.Code, "severity": f.Severity, "message": f.Message})
		}
		sv := e.node["substrate_validation"].(map[string]any)
		sv["findings"] = fs
		out[i] = Result{Node: e.node, Findings: append([]Finding(nil), e.findings...),
			Identity: Identity{MachineID: e.machineID, InstallUID: strings.TrimSpace(e.f.InstallUID), Platforms: append([]string(nil), e.f.Platforms...)}}
	}
	return out, nil
}

// tier is the capacity class of plan 07 Q-07-04 as far as this plan compares it: constrained (below the supported
// minimum) or standard.
func (e *eval) tier() string {
	if e.meetsMinimum {
		return "standard"
	}
	return "constrained"
}

func crossChecks(evs []*eval) {
	byMachine := map[string][]*eval{}
	byIP := map[string][]*eval{}
	for _, e := range evs {
		if e.machineID != "" {
			byMachine[e.machineID] = append(byMachine[e.machineID], e)
		}
		if e.internal != "" {
			byIP[e.internal] = append(byIP[e.internal], e)
		}
	}
	for id, group := range byMachine {
		if len(group) < 2 {
			continue
		}
		for _, e := range group {
			e.add("EXISTING_DUPLICATE_NODE", sevFail, "%s answers with the machine id %s that another entry of EXISTING_NODES also resolves to: two entries are one machine", e.in.Node.Name, id)
		}
	}
	for ip, group := range byIP {
		if len(group) < 2 {
			continue
		}
		for _, e := range group {
			e.add("NODE_INTERNAL_IP_INVALID", sevFail, "%s: the internal IP %s is used by more than one node", e.in.Node.Name, ip)
		}
	}
	var comparable []*eval
	for _, e := range evs {
		// a node whose disk class is unknown is reported by DISK_CLASS_UNVERIFIED, not compared
		if e.meetsLower && e.diskClass != "unknown" {
			comparable = append(comparable, e)
		}
	}
	for _, e := range comparable {
		if e.tier() != comparable[0].tier() {
			for _, x := range comparable {
				x.add("EXISTING_MIXED_SIZE_CLASS", sevFail, "%s is %s but the nodes of the cluster are not all in one capacity class", x.in.Node.Name, x.tier())
			}
			break
		}
	}
}

// Fingerprints computes the SHA256 fingerprints of ssh-keyscan lines (ed25519, ecdsa and rsa keys), in line order
// without duplicates.
func Fingerprints(lines []string) []string { return fingerprints(lines) }

// fingerprints computes the SHA256 fingerprints of the ssh-keyscan lines (ed25519, ecdsa and rsa keys).
func fingerprints(lines []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		f := strings.Fields(l)
		if len(f) < 3 {
			continue
		}
		switch {
		case f[1] == "ssh-ed25519", f[1] == "ssh-rsa", strings.HasPrefix(f[1], "ecdsa-sha2-"):
		default:
			continue
		}
		blob, err := base64.StdEncoding.DecodeString(f[2])
		if err != nil {
			continue
		}
		sum := sha256.Sum256(blob)
		fp := "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:])
		if !seen[fp] {
			seen[fp] = true
			out = append(out, fp)
		}
	}
	return out
}

func parseOSRelease(s string) map[string]string {
	m := map[string]string{}
	for _, l := range strings.Split(s, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(l), "=")
		if !ok || strings.HasPrefix(k, "#") {
			continue
		}
		v = strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
			v = v[1 : len(v)-1]
		}
		m[k] = v
	}
	return m
}

func memTotal(meminfo string) int64 {
	for _, l := range strings.Split(meminfo, "\n") {
		if !strings.HasPrefix(l, "MemTotal:") {
			continue
		}
		f := strings.Fields(strings.TrimPrefix(l, "MemTotal:"))
		if len(f) == 0 {
			return 0
		}
		n, err := strconv.ParseInt(f[0], 10, 64)
		if err != nil {
			return 0
		}
		if len(f) > 1 && strings.EqualFold(f[1], "kB") {
			n *= 1024
		}
		return n
	}
	return 0
}

// kernelAtLeast reports whether a kernel release such as 6.12.48+deb13-amd64 is at least major.minor.
func kernelAtLeast(rel string, major, minor int) bool {
	var nums [2]int
	rest := rel
	for i := 0; i < 2; i++ {
		j := 0
		for j < len(rest) && rest[j] >= '0' && rest[j] <= '9' {
			j++
		}
		if j == 0 {
			return false
		}
		nums[i], _ = strconv.Atoi(rest[:j])
		rest = rest[j:]
		if i == 0 {
			if !strings.HasPrefix(rest, ".") {
				return false
			}
			rest = rest[1:]
		}
	}
	return nums[0] > major || (nums[0] == major && nums[1] >= minor)
}

func isPrivateOrLocal(a netip.Addr) bool {
	if a.IsPrivate() || a.IsLoopback() || a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() || a.IsUnspecified() || a.IsMulticast() {
		return true
	}
	if a.Is4() {
		b := a.As4()
		return b[0] == 100 && b[1]&0xc0 == 0x40 // 100.64.0.0/10 (RFC 6598)
	}
	return false
}

// bypath finds the device holding the filesystem source (/dev/nvme0n1p2) in the lsblk tree and returns the whole disk
// above it.
func rootDisk(devs []blockDevice, source, target string) *blockDevice {
	base := strings.TrimPrefix(source, "/dev/")
	var walk func(d *blockDevice, top *blockDevice) *blockDevice
	walk = func(d *blockDevice, top *blockDevice) *blockDevice {
		if top == nil {
			top = d
		}
		if d.Name == base || d.KName == base || (strings.HasPrefix(base, "mapper/") && d.Name == strings.TrimPrefix(base, "mapper/")) {
			return top
		}
		if target != "" {
			mps := append([]*string{d.Mountpoint}, d.Mountpoints...)
			for _, mp := range mps {
				if mp != nil && *mp == target && len(d.Children) == 0 {
					return top
				}
			}
		}
		for i := range d.Children {
			if r := walk(&d.Children[i], top); r != nil {
				return r
			}
		}
		return nil
	}
	for i := range devs {
		if r := walk(&devs[i], nil); r != nil {
			return r
		}
	}
	return nil
}

func (e *eval) check(prof *profile, c Context) {
	f := &e.f
	n := e.in.Node
	missing := map[string]bool{}
	for _, t := range f.MissingTools {
		missing[t] = true
		e.add("DISCOVERY_TOOL_MISSING", sevFail, "the command %s is missing on %s: its facts cannot be discovered (install it, see procedures/bootstrap-preflight-failure.md#node-size)", t, n.Name)
	}

	// host keys
	fps := fingerprints(e.in.HostKeys)
	if n.HostKeyFingerprint != "" {
		ok := false
		for _, fp := range fps {
			ok = ok || fp == n.HostKeyFingerprint
		}
		if !ok {
			e.add("HOST_KEY_MISMATCH", sevFail, "none of the host keys of %s has the attested fingerprint %s (found: %s)", n.Name, n.HostKeyFingerprint, strings.Join(fps, ", "))
		}
	}

	// os
	osr := parseOSRelease(f.OSRelease)
	osOK := (osr["ID"] == "debian" && osr["VERSION_ID"] == "13") || (osr["ID"] == "ubuntu" && osr["VERSION_ID"] == "24.04")
	if !osOK {
		e.add("NODE_OS_UNSUPPORTED", sevFail, "%s runs %q %q; Debian 13 and Ubuntu 24.04 are supported", n.Name, osr["ID"], osr["VERSION_ID"])
	}
	arch := f.Arch
	switch arch {
	case "x86_64", "amd64":
		arch = "amd64"
	case "aarch64", "arm64":
		arch = "arm64"
		e.add("NODE_ARCH_UNSUPPORTED", sevFail, "%s is arm64; only amd64 is supported", n.Name)
	default:
		e.add("NODE_ARCH_UNSUPPORTED", sevFail, "%s reports the architecture %q; only amd64 is supported", n.Name, f.Arch)
	}
	if !kernelAtLeast(f.Kernel, 6, 1) {
		e.add("NODE_KERNEL_TOO_OLD", sevFail, "%s runs kernel %q; 6.1 or newer is required", n.Name, f.Kernel)
	}
	if f.AppArmor != "Y" {
		e.add("NODE_APPARMOR_DISABLED", sevFail, "AppArmor is not enabled on %s (/sys/module/apparmor/parameters/enabled = %q)", n.Name, f.AppArmor)
	}
	if f.CgroupFS != "cgroup2fs" && !(f.CgroupFS == "" && missing["stat"]) {
		e.add("NODE_CGROUP_V1", sevFail, "%s does not use the unified cgroup v2 hierarchy (/sys/fs/cgroup is %q)", n.Name, f.CgroupFS)
	}
	if !bool(f.WireGuard) {
		e.add("NODE_NO_WIREGUARD", sevFail, "the WireGuard kernel module is not available on %s", n.Name)
	}
	if !bool(f.SudoOK) {
		e.add("NODE_SUDO_REQUIRED", sevFail, "passwordless sudo (sudo -n true) does not work for %s on %s", n.User, n.Name)
	}
	own := c.InstallUID != "" && f.InstallUID == c.InstallUID
	foreign := false
	for _, p := range f.Platforms {
		foreign = foreign || p == "k3s" || p == "kubelet" || p == "dockerd"
	}
	if foreign && !own {
		e.add("NODE_HAS_FOREIGN_PLATFORM", sevFail, "%s already runs %s and carries no marker of this installation", n.Name, strings.Join(f.Platforms, ", "))
	}
	if f.InstallUID != "" && !own {
		e.add("NODE_BELONGS_TO_OTHER_INSTALL", sevFail, "%s carries the install marker %s, which is not this installation", n.Name, f.InstallUID)
	}
	if !own {
		for _, p := range conflictPorts(f.SS) {
			e.add("NODE_PORT_CONFLICT", sevFail, "%s: something already listens on %s", n.Name, p)
		}
	}
	if f.NTP != "yes" && !(f.NTP == "" && missing["timedatectl"]) {
		e.add("NODE_TIME_UNSYNCED", sevWarn, "%s does not report a synchronized clock (NTPSynchronized=%q)", n.Name, f.NTP)
	}
	if strings.TrimSpace(f.Swap) != "" {
		e.add("NODE_SWAP_ENABLED", sevInfo, "swap is enabled on %s (configure disables it)", n.Name)
	}

	// filesystem
	var fsSource, fsTarget, fsType string
	var avail int64
	if f.Findmnt != nil && len(f.Findmnt.Filesystems) > 0 {
		fs := f.Findmnt.Filesystems[0]
		fsSource, fsTarget, fsType = fs.Source, fs.Target, fs.FSType
		sizeKnown := fs.Size != nil && fs.Avail != nil
		if sizeKnown {
			avail = int64(*fs.Avail)
		} else {
			e.add("DISCOVERY_TOOL_MISSING", sevFail, "findmnt on %s reported no size or free space for %s: the free space cannot be discovered (findmnt must support -o SIZE,AVAIL)", n.Name, fsTarget)
		}
		if fsType != "ext4" && fsType != "xfs" {
			e.add("NODE_FS_UNSUPPORTED", sevFail, "%s keeps /var/lib/rancher on a %q filesystem; ext4 or xfs is required", n.Name, fsType)
		}
		need := prof.storageSize
		switch {
		case !sizeKnown:
		case avail*10 < need*8:
			e.add("NODE_DISK_FREE_LOW", sevFail, "%s has %d bytes free on %s; %d are required", n.Name, avail, fsTarget, need*8/10)
		case avail < need:
			e.add("NODE_DISK_FREE_LOW", sevWarn, "%s has %d bytes free on %s; %d are recommended", n.Name, avail, fsTarget, need)
		}
	}

	// disk
	diskName, virt := "", f.Virt
	e.diskClass = "unknown"
	classSource := "discovered"
	if f.Lsblk != nil {
		if d := rootDisk(f.Lsblk.Blockdevices, fsSource, fsTarget); d != nil {
			diskName, e.diskSize = d.KName, int64(d.Size)
			if diskName == "" {
				diskName = d.Name
			}
			tran := ""
			if d.Tran != nil {
				tran = *d.Tran
			}
			switch {
			case tran == "nvme" || strings.HasPrefix(d.KName, "nvme"):
				e.diskClass = "nvme"
			case virt == "none" && !bool(d.Rota):
				e.diskClass = "ssd"
			case virt == "none":
				e.diskClass = "hdd"
			case !bool(d.Rota):
				e.diskClass = "ssd"
			default:
				e.diskClass = "unknown"
			}
		}
	}
	if diskName == "" {
		diskName = "unknown"
	}
	switch {
	case e.diskClass == "unknown" && n.DiskClassOverride != "":
		e.diskClass, classSource = n.DiskClassOverride, "operator-attested"
		e.add("DISK_CLASS_ATTESTED", sevWarn, "the disk class of %s (%s) is attested by the operator, not discovered", n.Name, n.DiskClassOverride)
	case e.diskClass == "unknown":
		e.add("DISK_CLASS_UNVERIFIED", sevFail, "the disk class of %s cannot be discovered (virtual disk); confirm the medium with your provider and set EXISTING_NODE_OVERRIDES=%s:disk_class=ssd|nvme", n.Name, n.Name)
	case e.diskClass == "hdd" && n.DiskClassOverride != "":
		e.add("DISK_CLASS_OVERRIDE_CONFLICT", sevFail, "%s: the disk is discovered as hdd; disk_class=%s cannot override a discovered class", n.Name, n.DiskClassOverride)
	}

	// capacity
	e.memory, e.cpu = memTotal(f.Meminfo), int64(f.Nproc)
	e.capacity(prof, n.Name)

	// addresses
	e.machineID = strings.TrimSpace(f.MachineID)
	configured := map[string]bool{}
	var pub4, pub6 any
	for _, ifc := range f.IPAddr {
		for _, a := range ifc.AddrInfo {
			configured[a.Local] = true
			ip, err := netip.ParseAddr(a.Local)
			if err != nil || a.Scope != "global" || isPrivateOrLocal(ip) {
				continue
			}
			if a.Family == "inet" && pub4 == nil {
				pub4 = a.Local
			}
			if a.Family == "inet6" && pub6 == nil {
				pub6 = a.Local
			}
		}
	}
	e.internal = n.InternalIP
	if e.internal == "" && len(f.IPRoute) > 0 {
		e.internal = f.IPRoute[0].PrefSrc
	}
	if _, err := netip.ParseAddr(e.internal); err != nil || !configured[e.internal] {
		if !(missing["ip"]) {
			e.add("NODE_INTERNAL_IP_INVALID", sevFail, "%s: the internal IP %q is not configured on an interface of the node", n.Name, e.internal)
		}
	}
	var external any
	if h, err := netip.ParseAddr(n.Host); err == nil && !isPrivateOrLocal(h) && h.String() != e.internal {
		external = n.Host
	}

	disks := []any{}
	if e.diskSize > 0 {
		disks = append(disks, map[string]any{"name": diskName, "size_bytes": e.diskSize, "class": e.diskClass, "class_source": classSource, "role": "root"})
	}
	var pref any = e.meetsPref
	e.node = map[string]any{
		"name":          n.Name,
		"provider_id":   "existing:" + n.Name,
		"machine_class": "existing",
		"roles":         []string{"agent"},
		"addresses": map[string]any{
			"public_ipv4": pub4, "public_ipv6": pub6, "internal_ip": e.internal, "external_ip": external,
		},
		"ssh": map[string]any{
			"host": n.Host, "port": n.Port, "user": n.User, "admin_user": nil, "host_key_fingerprints": stringsToAny(fps),
		},
		"hardware": map[string]any{
			"cpu_count": e.cpu, "memory_bytes": e.memory, "disks": disks, "virtualization": virt, "architecture": arch,
		},
		"os": map[string]any{
			"id": osr["ID"], "version_id": osr["VERSION_ID"], "pretty_name": osr["PRETTY_NAME"], "kernel": f.Kernel,
		},
		"location": map[string]any{"region": "existing", "failure_domain": "existing"},
		"state":    "running",
		"substrate_validation": map[string]any{
			"profile": prof.name, "meets_minimum": e.meetsMinimum, "meets_preferred": pref, "findings": []any{},
		},
	}
}

// capacity compares e.cpu, e.memory, e.diskSize and e.diskClass with the lower bound, the minimum and the preferred shape of
// the profile (plan 03 section 14.3) and records the findings.
func (e *eval) capacity(prof *profile, name string) {
	meets := func(s *shape, classKnown bool) bool {
		if s == nil {
			return false
		}
		ok := e.cpu >= s.cpu && atLeast(e.memory, s.memory) && atLeast(e.diskSize, s.storage)
		if classKnown {
			ok = ok && classRank[e.diskClass] >= classRank[s.class]
		}
		return ok
	}
	// An unknown class is reported by DISK_CLASS_UNVERIFIED; it is not also counted as undersized.
	e.meetsLower = meets(prof.constrained, e.diskClass != "unknown")
	e.meetsMinimum = e.diskClass != "unknown" && meets(prof.minimum, true)
	e.meetsPref = e.diskClass != "unknown" && meets(prof.preferred, true)
	if !e.meetsLower {
		e.add("NODE_BELOW_MINIMUM", sevFail, "%s (%d CPUs, %d bytes memory, %d bytes %s disk) is below the lower bound of profile %s", name, e.cpu, e.memory, e.diskSize, e.diskClass, prof.name)
	} else if e.diskClass != "unknown" {
		if !e.meetsMinimum {
			e.add("NODE_CONSTRAINED", sevWarn, "%s meets only the constrained lower bound of profile %s: no capacity guarantee (ADR-0020)", name, prof.name)
		} else if !e.meetsPref {
			e.add("BELOW_PREFERRED_DISK", sevWarn, "%s meets the minimum of profile %s but not the preferred shape", name, prof.name)
		}
	}
}

func stringsToAny(s []string) []any {
	out := make([]any, len(s))
	for i, v := range s {
		out[i] = v
	}
	return out
}

// conflictPorts lists the listeners on the ports k3s, etcd, WireGuard and the ingress need (ss -Hltnu output).
func conflictPorts(ss string) []string {
	tcp := map[string]bool{"6443": true, "2379": true, "2380": true, "2381": true, "10250": true, "80": true, "443": true}
	udp := map[string]bool{"51820": true, "51821": true}
	seen := map[string]bool{}
	var out []string
	for _, l := range strings.Split(ss, "\n") {
		f := strings.Fields(l)
		if len(f) < 5 {
			continue
		}
		i := strings.LastIndex(f[4], ":")
		if i < 0 {
			continue
		}
		port := f[4][i+1:]
		var key string
		switch {
		case strings.HasPrefix(f[0], "tcp") && tcp[port]:
			key = port + "/tcp"
		case strings.HasPrefix(f[0], "udp") && udp[port]:
			key = port + "/udp"
		default:
			continue
		}
		if !seen[key] {
			seen[key] = true
			out = append(out, key)
		}
	}
	return out
}
