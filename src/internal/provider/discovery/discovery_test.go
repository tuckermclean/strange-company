package discovery_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"strange.company/sc/internal/contracts"
	"strange.company/sc/internal/provider/discovery"
)

// 05-AC-2: the recorded (derived) node JSON of test/fixtures/existing/discovery/<case> yields the expected
// ProviderOutput.nodes[0] and the expected finding codes; the mutation tables cover every code of plan 05 section 14.5
// and the address normalization of section 7.2. The facts format is described in the fixtures' README.

const (
	fixtures  = "../../../../test/fixtures/existing/discovery"
	profiles  = "../../../../infra/profiles"
	contractD = "../../../../contracts"
	ourUID    = "6f1c2d3e-4b5a-4c6d-8e7f-0a1b2c3d4e5f"
	otherUID  = "0a0b0c0d-1111-4222-8333-444455556666"
	gib       = int64(1) << 30
)

// vocabulary is the set of codes the fixtures pin down; findings with other codes (capacity warnings) are not checked.
var vocabulary = map[string]bool{
	"NODE_BELOW_MINIMUM": true, "NODE_OS_UNSUPPORTED": true, "NODE_ARCH_UNSUPPORTED": true, "NODE_KERNEL_TOO_OLD": true,
	"NODE_APPARMOR_DISABLED": true, "EXISTING_MIXED_SIZE_CLASS": true, "NODE_CGROUP_V1": true, "NODE_NO_WIREGUARD": true,
	"NODE_SUDO_REQUIRED": true, "NODE_HAS_FOREIGN_PLATFORM": true, "NODE_BELONGS_TO_OTHER_INSTALL": true,
	"NODE_PORT_CONFLICT": true, "NODE_DISK_FREE_LOW": true, "NODE_FS_UNSUPPORTED": true, "NODE_TIME_UNSYNCED": true,
	"NODE_SWAP_ENABLED": true, "EXISTING_DUPLICATE_NODE": true, "NODE_INTERNAL_IP_INVALID": true,
	"DISK_CLASS_UNVERIFIED": true, "DISK_CLASS_ATTESTED": true, "DISK_CLASS_OVERRIDE_CONFLICT": true,
	"HOST_KEY_MISMATCH": true, "DISCOVERY_TOOL_MISSING": true,
}

type expectation struct {
	Node     map[string]any `json:"node"`
	Findings []struct {
		Code     string `json:"code"`
		Severity string `json:"severity"`
	} `json:"findings"`
	Exact  bool     `json:"exact"`
	Absent []string `json:"absent"`
}

func mustRead(t testing.TB, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func profileFile(t testing.TB, name string) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join(profiles, name+".yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatal(err)
	}
	return p
}

func ctxFor(t testing.TB) discovery.Context {
	return discovery.Context{ProfileFile: profileFile(t, "self-hosting-minimum"), InstallUID: ourUID}
}

func loadInput(t testing.TB, name string) discovery.Input {
	t.Helper()
	dir := filepath.Join(fixtures, name)
	var in struct {
		Node discovery.Node `json:"node"`
	}
	if err := json.Unmarshal(mustRead(t, filepath.Join(dir, "input.json")), &in); err != nil {
		t.Fatalf("%s/input.json: %v", name, err)
	}
	var keys []string
	for _, l := range strings.Split(string(mustRead(t, filepath.Join(dir, "hostkeys.txt"))), "\n") {
		if strings.TrimSpace(l) != "" {
			keys = append(keys, l)
		}
	}
	return discovery.Input{Node: in.Node, Facts: mustRead(t, filepath.Join(dir, "facts.json")), HostKeys: keys}
}

func loadExpected(t testing.TB, name string) expectation {
	t.Helper()
	var e expectation
	if err := json.Unmarshal(mustRead(t, filepath.Join(fixtures, name, "expected.json")), &e); err != nil {
		t.Fatalf("%s/expected.json: %v", name, err)
	}
	return e
}

// normalized converts any value to its decoded-JSON form so that expected and actual compare structurally.
func normalized(t testing.TB, v any) any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("result does not marshal to JSON: %v", err)
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// sortFingerprints sorts ssh.host_key_fingerprints in place (their order is not specified).
func sortFingerprints(v any) {
	n, _ := v.(map[string]any)
	ssh, _ := n["ssh"].(map[string]any)
	if fps, ok := ssh["host_key_fingerprints"].([]any); ok {
		sort.Slice(fps, func(i, j int) bool { return fmt.Sprint(fps[i]) < fmt.Sprint(fps[j]) })
	}
}

// subset reports every place where got differs from want; keys of want must match, keys only in got are ignored.
func subset(path string, got, want any) []string {
	switch w := want.(type) {
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok {
			return []string{fmt.Sprintf("%s: got %v, want an object", path, got)}
		}
		var out []string
		keys := make([]string, 0, len(w))
		for k := range w {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			out = append(out, subset(path+"/"+k, g[k], w[k])...)
		}
		return out
	case []any:
		g, ok := got.([]any)
		if !ok || len(g) != len(w) {
			return []string{fmt.Sprintf("%s: got %v, want a list of %d", path, got, len(w))}
		}
		var out []string
		for i := range w {
			out = append(out, subset(fmt.Sprintf("%s/%d", path, i), g[i], w[i])...)
		}
		return out
	default:
		if !reflect.DeepEqual(got, want) {
			return []string{fmt.Sprintf("%s: got %v, want %v", path, got, want)}
		}
		return nil
	}
}

func findingSet(fs []discovery.Finding) map[string]bool {
	out := map[string]bool{}
	for _, f := range fs {
		out[f.Code+":"+f.Severity] = true
	}
	return out
}

func codeSet(fs []discovery.Finding) map[string]bool {
	out := map[string]bool{}
	for _, f := range fs {
		out[f.Code] = true
	}
	return out
}

func evalOne(t testing.TB, in discovery.Input, c discovery.Context) discovery.Result {
	t.Helper()
	res, err := discovery.Evaluate([]discovery.Input{in}, c)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(res) != 1 {
		t.Fatalf("Evaluate returned %d results for 1 input, want 1", len(res))
	}
	return res[0]
}

// validateNode embeds the node in a ProviderOutput skeleton of the existing provider and validates it against
// contracts/provider.schema.yaml.
func validateNode(t testing.TB, node map[string]any) {
	t.Helper()
	schemas, err := contracts.CompileSchemas(contractD)
	if err != nil {
		t.Fatal(err)
	}
	sc := schemas["provider"]
	if sc == nil {
		t.Fatal("no provider schema")
	}
	doc := map[string]any{
		"schema_version": "v1alpha1", "record_type": "provider_output", "cluster": "lab-one", "generated_at": "2026-11-01T12:00:00Z",
		"provider": map[string]any{"name": "existing", "implementation_version": "0.1.0", "product_line": "none",
			"capabilities": map[string]any{"provisions_compute": false, "paid": false, "reversible_within_hours": true,
				"supports_expand": true, "supports_destroy_compute": false, "host_key_source": "tofu"}},
		"region": map[string]any{"provider_region": "n/a"},
		"nodes":  []any{node},
		"cost_estimate": map[string]any{"currency": "EUR", "monthly_total": "0", "committed_minimum": "0", "max_monthly_cost": "0",
			"within_limit": true, "complete": true, "catalog_version": "none", "items": []any{}},
		"tofu": nil, "warnings": []any{},
	}
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("node does not marshal: %v", err)
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var inst any
	if err := dec.Decode(&inst); err != nil {
		t.Fatal(err)
	}
	if err := sc.Validate(inst); err != nil {
		t.Errorf("nodes[0] does not validate against provider.schema.yaml: %v", err)
	}
}

// nodeFindings reads node.substrate_validation.findings as code:severity pairs.
func nodeFindings(t testing.TB, node map[string]any) map[string]bool {
	t.Helper()
	sv, _ := node["substrate_validation"].(map[string]any)
	list, _ := sv["findings"].([]any)
	out := map[string]bool{}
	for _, f := range list {
		m, _ := f.(map[string]any)
		out[fmt.Sprint(m["code"])+":"+fmt.Sprint(m["severity"])] = true
	}
	return out
}

// 05-AC-2: each recorded node yields the expected nodes[0] and finding codes.
func TestDiscoveryFixtures_05AC2(t *testing.T) {
	cases := []string{"debian13-baremetal-nvme", "ubuntu2404-baremetal-ssd", "debian13-virtio-unverified", "debian13-virtio-attested",
		"ubuntu2404-cloud-scsi", "debian13-k3s-present", "ubuntu2404-cgroup-v1"}
	entries, err := os.ReadDir(fixtures)
	if err != nil {
		t.Fatal(err)
	}
	dirs := 0
	for _, e := range entries {
		if e.IsDir() {
			dirs++
		}
	}
	if dirs != len(cases) {
		t.Fatalf("%d fixture directories, the test lists %d: add the new case to the list", dirs, len(cases))
	}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			in := loadInput(t, name)
			want := loadExpected(t, name)
			r := evalOne(t, in, ctxFor(t))
			if r.Node == nil {
				t.Fatalf("Result.Node is nil")
			}
			got := normalized(t, r.Node)
			sortFingerprints(got)
			wantNode := normalized(t, want.Node)
			sortFingerprints(wantNode)
			for _, d := range subset("node", got, wantNode) {
				t.Error(d)
			}
			have := findingSet(r.Findings)
			for _, f := range want.Findings {
				if !have[f.Code+":"+f.Severity] {
					t.Errorf("finding %s (%s) missing; got %v", f.Code, f.Severity, r.Findings)
				}
			}
			codes := codeSet(r.Findings)
			for _, c := range want.Absent {
				if codes[c] {
					t.Errorf("finding %s must not be present; got %v", c, r.Findings)
				}
			}
			if want.Exact {
				allowed := map[string]bool{}
				for _, f := range want.Findings {
					allowed[f.Code+":"+f.Severity] = true
				}
				for _, f := range r.Findings {
					if vocabulary[f.Code] && !allowed[f.Code+":"+f.Severity] {
						t.Errorf("unexpected finding %s (%s): %s", f.Code, f.Severity, f.Message)
					}
				}
			}
			inNode := nodeFindings(t, r.Node)
			for k := range have {
				if !inNode[k] {
					t.Errorf("finding %s is not in node.substrate_validation.findings", k)
				}
			}
			validateNode(t, r.Node)
		})
	}
}

// env is a mutable copy of the baseline fixture (Debian 13 bare metal, NVMe).
type env struct {
	facts map[string]any
	in    discovery.Input
	ctx   discovery.Context
}

func baseline(t testing.TB) *env {
	t.Helper()
	in := loadInput(t, "debian13-baremetal-nvme")
	var f map[string]any
	if err := json.Unmarshal(in.Facts, &f); err != nil {
		t.Fatal(err)
	}
	return &env{facts: f, in: in, ctx: ctxFor(t)}
}

func (e *env) eval(t testing.TB) discovery.Result {
	t.Helper()
	b, err := json.Marshal(e.facts)
	if err != nil {
		t.Fatal(err)
	}
	e.in.Facts = b
	return evalOne(t, e.in, e.ctx)
}

func (e *env) disk() map[string]any {
	return e.facts["lsblk"].(map[string]any)["blockdevices"].([]any)[0].(map[string]any)
}

func (e *env) setMem(n int64) { e.facts["meminfo"] = fmt.Sprintf("MemTotal:       %d kB\nMemFree: 1000 kB\n", n/1024) }

// makeSata turns the root disk into a SATA disk sda (rotational or not) with partitions sda1 and sda2.
func (e *env) makeSata(rotational bool) {
	d := e.disk()
	d["name"], d["kname"], d["tran"], d["rota"], d["model"] = "sda", "sda", "sata", rotational, "ST4000NM000A"
	for i, k := range d["children"].([]any) {
		kid := k.(map[string]any)
		kid["pkname"], kid["rota"] = "sda", rotational
		kid["name"], kid["kname"] = fmt.Sprintf("sda%d", i+1), fmt.Sprintf("sda%d", i+1)
	}
	e.facts["findmnt"].(map[string]any)["filesystems"].([]any)[0].(map[string]any)["source"] = "/dev/sda2"
}

func (e *env) setDiskSize(size int64) {
	e.disk()["size"] = size
	kids := e.disk()["children"].([]any)
	kids[len(kids)-1].(map[string]any)["size"] = size - gib
	fs := e.facts["findmnt"].(map[string]any)["filesystems"].([]any)[0].(map[string]any)
	fs["size"] = fmt.Sprint(size - 2*gib)
	fs["avail"] = fmt.Sprint(size - 6*gib)
}

func (e *env) setAvail(b int64) {
	e.facts["findmnt"].(map[string]any)["filesystems"].([]any)[0].(map[string]any)["avail"] = fmt.Sprint(b)
}

func (e *env) setFS(t string) {
	e.facts["findmnt"].(map[string]any)["filesystems"].([]any)[0].(map[string]any)["fstype"] = t
}

func (e *env) setOS(id, version, pretty string) {
	e.facts["os_release"] = fmt.Sprintf("PRETTY_NAME=%q\nNAME=%q\nVERSION_ID=%q\nID=%s\n", pretty, pretty, version, id)
}

func (e *env) listen(rows ...string) { e.facts["ss"] = e.facts["ss"].(string) + strings.Join(rows, "\n") + "\n" }

func nodeAt(n map[string]any, path ...string) any {
	var cur any = n
	for _, p := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[p]
	}
	return cur
}

// scaled is f*n rounded down, computed at run time (a constant expression would have to be integral).
func scaled(f float64, n int64) int64 { return int64(f * float64(n)) }

type mutation struct {
	name   string
	mutate func(e *env)
	want   []string // CODE:severity
	absent []string // CODE
	warn   bool     // some warn finding (any code) must be present
	check  func(t *testing.T, r discovery.Result)
}

// 05-AC-2: every code of plan 05 section 14.5 (and plan 03 section 14.3 tolerance) fires on its fact, and not beside it.
func TestDiscoveryMutations_05AC2(t *testing.T) {
	cases := []mutation{
		{name: "cpu-below-minimum", mutate: func(e *env) { e.facts["nproc"] = 2 }, want: []string{"NODE_BELOW_MINIMUM:fail"}},
		{name: "memory-0.919x8Gi-fails", mutate: func(e *env) { e.setMem(scaled(0.919, 8*gib)) }, want: []string{"NODE_BELOW_MINIMUM:fail"}},
		{name: "memory-0.921x8Gi-passes-with-warning", mutate: func(e *env) { e.setMem(scaled(0.921, 8*gib)) }, absent: []string{"NODE_BELOW_MINIMUM"}, warn: true,
			check: func(t *testing.T, r discovery.Result) {
				if got := nodeAt(r.Node, "substrate_validation", "meets_preferred"); got != false {
					t.Errorf("meets_preferred = %v, want false", got)
				}
			}},
		{name: "disk-0.91x100Gi-fails", mutate: func(e *env) { e.setDiskSize(scaled(0.91, 100*gib)) }, want: []string{"NODE_BELOW_MINIMUM:fail"}},
		{name: "disk-0.93x100Gi-passes", mutate: func(e *env) { e.setDiskSize(scaled(0.93, 100*gib)) }, absent: []string{"NODE_BELOW_MINIMUM"}},
		{name: "rotational-sata-is-hdd-and-below-minimum", mutate: func(e *env) { e.makeSata(true) }, want: []string{"NODE_BELOW_MINIMUM:fail"},
			check: func(t *testing.T, r discovery.Result) {
				if c := r.Node["hardware"].(map[string]any)["disks"].([]any)[0].(map[string]any)["class"]; c != "hdd" {
					t.Errorf("class = %v, want hdd (bare metal, ROTA=1)", c)
				}
			}},
		{name: "nvme-transport-wins-over-rota", mutate: func(e *env) { e.disk()["rota"] = true }, absent: []string{"NODE_BELOW_MINIMUM", "DISK_CLASS_UNVERIFIED"},
			check: func(t *testing.T, r discovery.Result) {
				if c := r.Node["hardware"].(map[string]any)["disks"].([]any)[0].(map[string]any)["class"]; c != "nvme" {
					t.Errorf("class = %v, want nvme (TRAN=nvme)", c)
				}
			}},
		{name: "os-fedora", mutate: func(e *env) { e.setOS("fedora", "41", "Fedora Linux 41") }, want: []string{"NODE_OS_UNSUPPORTED:fail"}},
		{name: "os-ubuntu-22.04", mutate: func(e *env) { e.setOS("ubuntu", "22.04", "Ubuntu 22.04.5 LTS") }, want: []string{"NODE_OS_UNSUPPORTED:fail"}},
		{name: "os-debian-12", mutate: func(e *env) { e.setOS("debian", "12", "Debian GNU/Linux 12 (bookworm)") }, want: []string{"NODE_OS_UNSUPPORTED:fail"}},
		{name: "os-ubuntu-24.04", mutate: func(e *env) { e.setOS("ubuntu", "24.04", "Ubuntu 24.04.3 LTS") }, absent: []string{"NODE_OS_UNSUPPORTED"}},
		{name: "arch-arm64", mutate: func(e *env) { e.facts["arch"] = "aarch64" }, want: []string{"NODE_ARCH_UNSUPPORTED:fail"}},
		{name: "kernel-5.15", mutate: func(e *env) { e.facts["kernel"] = "5.15.0-100-generic" }, want: []string{"NODE_KERNEL_TOO_OLD:fail"}},
		{name: "kernel-6.0", mutate: func(e *env) { e.facts["kernel"] = "6.0.19-amd64" }, want: []string{"NODE_KERNEL_TOO_OLD:fail"}},
		{name: "kernel-6.1-passes", mutate: func(e *env) { e.facts["kernel"] = "6.1.0-13-amd64" }, absent: []string{"NODE_KERNEL_TOO_OLD"}},
		{name: "kernel-6.10-passes", mutate: func(e *env) { e.facts["kernel"] = "6.10.2-amd64" }, absent: []string{"NODE_KERNEL_TOO_OLD"}},
		{name: "apparmor-off", mutate: func(e *env) { e.facts["apparmor"] = "N" }, want: []string{"NODE_APPARMOR_DISABLED:fail"}},
		{name: "apparmor-absent", mutate: func(e *env) { e.facts["apparmor"] = "" }, want: []string{"NODE_APPARMOR_DISABLED:fail"}},
		{name: "cgroup-v1", mutate: func(e *env) { e.facts["cgroup_fs"] = "tmpfs" }, want: []string{"NODE_CGROUP_V1:fail"}},
		{name: "cgroup-v2-passes", mutate: func(e *env) { e.facts["cgroup_fs"] = "cgroup2fs" }, absent: []string{"NODE_CGROUP_V1"}},
		{name: "no-wireguard", mutate: func(e *env) { e.facts["wireguard"] = false }, want: []string{"NODE_NO_WIREGUARD:fail"}},
		{name: "sudo-needs-password", mutate: func(e *env) { e.facts["sudo_ok"] = false }, want: []string{"NODE_SUDO_REQUIRED:fail"}},
		{name: "foreign-docker", mutate: func(e *env) { e.facts["platforms"] = []string{"dockerd"} }, want: []string{"NODE_HAS_FOREIGN_PLATFORM:fail"}},
		{name: "foreign-k3s-without-marker", mutate: func(e *env) { e.facts["platforms"] = []string{"k3s", "kubelet"} }, want: []string{"NODE_HAS_FOREIGN_PLATFORM:fail"}},
		{name: "own-install-marker-accepts-k3s-and-ports", mutate: func(e *env) {
			e.facts["platforms"] = []string{"k3s", "kubelet"}
			e.facts["install_uid"] = ourUID
			e.listen("tcp   LISTEN 0      4096               *:6443               *:*")
		}, absent: []string{"NODE_HAS_FOREIGN_PLATFORM", "NODE_BELONGS_TO_OTHER_INSTALL", "NODE_PORT_CONFLICT"}},
		{name: "other-install-marker", mutate: func(e *env) { e.facts["install_uid"] = otherUID }, want: []string{"NODE_BELONGS_TO_OTHER_INSTALL:fail"}},
		{name: "port-6443", mutate: func(e *env) { e.listen("tcp   LISTEN 0      4096               *:6443               *:*") }, want: []string{"NODE_PORT_CONFLICT:fail"}},
		{name: "port-2380-loopback", mutate: func(e *env) { e.listen("tcp   LISTEN 0      4096       127.0.0.1:2380         0.0.0.0:*") }, want: []string{"NODE_PORT_CONFLICT:fail"}},
		{name: "port-10250-ipv6", mutate: func(e *env) { e.listen("tcp   LISTEN 0      4096            [::]:10250            [::]:*") }, want: []string{"NODE_PORT_CONFLICT:fail"}},
		{name: "port-51820-udp", mutate: func(e *env) { e.listen("udp   UNCONN 0      0            0.0.0.0:51820        0.0.0.0:*") }, want: []string{"NODE_PORT_CONFLICT:fail"}},
		{name: "port-443", mutate: func(e *env) { e.listen("tcp   LISTEN 0      511          0.0.0.0:443          0.0.0.0:*") }, want: []string{"NODE_PORT_CONFLICT:fail"}},
		{name: "port-80", mutate: func(e *env) { e.listen("tcp   LISTEN 0      511          0.0.0.0:80           0.0.0.0:*") }, want: []string{"NODE_PORT_CONFLICT:fail"}},
		{name: "port-22-and-dns-are-fine", mutate: func(e *env) {}, absent: []string{"NODE_PORT_CONFLICT"}},
		{name: "free-space-40GiB", mutate: func(e *env) { e.setAvail(40 * gib) }, want: []string{"NODE_DISK_FREE_LOW:fail"}},
		{name: "free-space-150GiB", mutate: func(e *env) { e.setAvail(150 * gib) }, absent: []string{"NODE_DISK_FREE_LOW"}},
		{name: "fs-btrfs", mutate: func(e *env) { e.setFS("btrfs") }, want: []string{"NODE_FS_UNSUPPORTED:fail"}},
		{name: "fs-xfs-passes", mutate: func(e *env) { e.setFS("xfs") }, absent: []string{"NODE_FS_UNSUPPORTED"}},
		{name: "time-unsynced", mutate: func(e *env) { e.facts["ntp_synchronized"] = "no" }, want: []string{"NODE_TIME_UNSYNCED:warn"}},
		{name: "swap-on", mutate: func(e *env) { e.facts["swap"] = "1073741824\n" }, want: []string{"NODE_SWAP_ENABLED:info"}},
		{name: "internal-ip-not-configured", mutate: func(e *env) { e.in.Node.InternalIP = "10.9.9.9" }, want: []string{"NODE_INTERNAL_IP_INVALID:fail"}},
		{name: "internal-ip-configured", mutate: func(e *env) { e.in.Node.InternalIP = "203.0.113.10" }, absent: []string{"NODE_INTERNAL_IP_INVALID"}},
		{name: "override-conflicts-with-discovered-hdd", mutate: func(e *env) {
			e.makeSata(true)
			e.in.Node.DiskClassOverride = "ssd"
		}, want: []string{"DISK_CLASS_OVERRIDE_CONFLICT:fail"}, absent: []string{"DISK_CLASS_ATTESTED"}},
		{name: "tool-missing", mutate: func(e *env) {
			e.facts["missing_tools"] = []string{"lsblk"}
			delete(e.facts, "lsblk")
		}, want: []string{"DISCOVERY_TOOL_MISSING:fail"}},
		{name: "host-key-attestation-matches", mutate: func(e *env) {
			e.in.Node.HostKeyFingerprint = loadExpected(t, "debian13-baremetal-nvme").Node["ssh"].(map[string]any)["host_key_fingerprints"].([]any)[0].(string)
		}, absent: []string{"HOST_KEY_MISMATCH"},
			check: func(t *testing.T, r discovery.Result) {
				attested := loadExpected(t, "debian13-baremetal-nvme").Node["ssh"].(map[string]any)["host_key_fingerprints"].([]any)[0]
				fps, _ := nodeAt(r.Node, "ssh", "host_key_fingerprints").([]any)
				for _, f := range fps {
					if f == attested {
						return
					}
				}
				t.Errorf("host_key_fingerprints %v does not contain the attested %v", fps, attested)
			}},
		{name: "host-key-attestation-differs", mutate: func(e *env) { e.in.Node.HostKeyFingerprint = "SHA256:" + strings.Repeat("A", 43) }, want: []string{"HOST_KEY_MISMATCH:fail"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := baseline(t)
			c.mutate(e)
			r := e.eval(t)
			have := findingSet(r.Findings)
			for _, w := range c.want {
				if !have[w] {
					t.Errorf("finding %s missing; got %v", w, r.Findings)
				}
			}
			codes := codeSet(r.Findings)
			for _, a := range c.absent {
				if codes[a] {
					t.Errorf("finding %s must not be present; got %v", a, r.Findings)
				}
			}
			if c.warn {
				ok := false
				for _, f := range r.Findings {
					ok = ok || f.Severity == "warn"
				}
				if !ok {
					t.Errorf("no warn finding; got %v", r.Findings)
				}
			}
			if c.check != nil && r.Node != nil {
				c.check(t, r)
			} else if c.check != nil {
				t.Errorf("Result.Node is nil")
			}
			if len(r.Findings) == 0 && len(c.want) > 0 {
				t.Errorf("no findings at all")
			}
		})
	}
}

type addrCase struct {
	name     string
	addrs    func() []any // ip -j addr interfaces after lo
	prefsrc  string
	host     string
	internal string // EXISTING_NODES /internal_ip
	v4, v6   any
	wantInt  string
	external any
}

func iface(name string, idx int, infos ...map[string]any) map[string]any {
	ai := make([]any, 0, len(infos))
	for _, i := range infos {
		ai = append(ai, i)
	}
	return map[string]any{"ifindex": idx, "ifname": name, "flags": []any{"BROADCAST", "MULTICAST", "UP", "LOWER_UP"}, "mtu": 1500,
		"operstate": "UP", "link_type": "ether", "address": "52:54:00:12:34:56", "addr_info": ai}
}

func v4(ip string, scope string) map[string]any {
	return map[string]any{"family": "inet", "local": ip, "prefixlen": 24, "scope": scope}
}

func v6(ip string, scope string) map[string]any {
	return map[string]any{"family": "inet6", "local": ip, "prefixlen": 64, "scope": scope}
}

// 05-AC-2 and plan 05 section 7.2: public, private, CGNAT, NAT-forwarded, link-local and ULA addresses.
func TestDiscoveryAddressNormalization_05AC2(t *testing.T) {
	one := func(infos ...map[string]any) func() []any {
		return func() []any { return []any{iface("eth0", 2, infos...)} }
	}
	cases := []addrCase{
		{name: "public-only", addrs: one(v4("203.0.113.10", "global")), prefsrc: "203.0.113.10", host: "203.0.113.10",
			v4: "203.0.113.10", v6: nil, wantInt: "203.0.113.10", external: nil},
		{name: "private-behind-port-forward", addrs: one(v4("10.0.0.5", "global")), prefsrc: "10.0.0.5", host: "198.51.100.7",
			v4: nil, v6: nil, wantInt: "10.0.0.5", external: "198.51.100.7"},
		{name: "rfc1918-172.16-by-dns-name", addrs: one(v4("172.16.5.9", "global")), prefsrc: "172.16.5.9", host: "box.example.net",
			v4: nil, v6: nil, wantInt: "172.16.5.9", external: nil},
		{name: "rfc1918-192.168", addrs: one(v4("192.168.9.9", "global")), prefsrc: "192.168.9.9", host: "192.168.9.9",
			v4: nil, v6: nil, wantInt: "192.168.9.9", external: nil},
		{name: "cgnat-100.64", addrs: one(v4("100.64.3.4", "global")), prefsrc: "100.64.3.4", host: "198.51.100.7",
			v4: nil, v6: nil, wantInt: "100.64.3.4", external: "198.51.100.7"},
		{name: "100.128-is-not-cgnat", addrs: one(v4("100.128.0.1", "global")), prefsrc: "100.128.0.1", host: "100.128.0.1",
			v4: "100.128.0.1", v6: nil, wantInt: "100.128.0.1", external: nil},
		{name: "172.32-is-not-private", addrs: one(v4("172.32.0.1", "global")), prefsrc: "172.32.0.1", host: "172.32.0.1",
			v4: "172.32.0.1", v6: nil, wantInt: "172.32.0.1", external: nil},
		{name: "loopback-linklocal-and-ula-ignored",
			addrs:   one(v4("192.168.1.9", "global"), v4("169.254.7.7", "link"), v6("fe80::1", "link"), v6("fd12:3456:789a::5", "global")),
			prefsrc: "192.168.1.9", host: "192.168.1.9", v4: nil, v6: nil, wantInt: "192.168.1.9", external: nil},
		{name: "fc00-ula-ignored", addrs: one(v4("203.0.113.10", "global"), v6("fc00::7", "global")), prefsrc: "203.0.113.10", host: "203.0.113.10",
			v4: "203.0.113.10", v6: nil, wantInt: "203.0.113.10", external: nil},
		{name: "public-ipv6", addrs: one(v4("203.0.113.10", "global"), v6("2001:db8:100::10", "global"), v6("fe80::2", "link")), prefsrc: "203.0.113.10",
			host: "203.0.113.10", v4: "203.0.113.10", v6: "2001:db8:100::10", wantInt: "203.0.113.10", external: nil},
		{name: "lab-user-mode-nic-and-private-nic",
			addrs: func() []any {
				return []any{iface("ens3", 2, v4("10.0.2.15", "global")), iface("ens4", 3, v4("10.77.0.11", "global"))}
			},
			prefsrc: "10.0.2.15", host: "127.0.0.1", internal: "10.77.0.11", v4: nil, v6: nil, wantInt: "10.77.0.11", external: nil},
		{name: "public-and-private-nics-internal-given",
			addrs: func() []any {
				return []any{iface("eth0", 2, v4("203.0.113.10", "global")), iface("eth1", 3, v4("10.0.0.5", "global"))}
			},
			prefsrc: "203.0.113.10", host: "203.0.113.10", internal: "10.0.0.5", v4: "203.0.113.10", v6: nil, wantInt: "10.0.0.5", external: "203.0.113.10"},
		{name: "internal-defaults-to-route-source",
			addrs: func() []any {
				return []any{iface("eth0", 2, v4("10.1.1.1", "global")), iface("eth1", 3, v4("10.2.2.2", "global"))}
			},
			prefsrc: "10.2.2.2", host: "10.1.1.1", v4: nil, v6: nil, wantInt: "10.2.2.2", external: nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := baseline(t)
			lo := e.facts["ip_addr"].([]any)[0]
			e.facts["ip_addr"] = append([]any{lo}, c.addrs()...)
			e.facts["ip_route_get"] = []any{map[string]any{"dst": "1.1.1.1", "gateway": "192.0.2.1", "dev": "eth0", "prefsrc": c.prefsrc}}
			e.in.Node.Host, e.in.Node.InternalIP = c.host, c.internal
			r := e.eval(t)
			if r.Node == nil {
				t.Fatalf("Result.Node is nil")
			}
			addr, _ := r.Node["addresses"].(map[string]any)
			for k, want := range map[string]any{"public_ipv4": c.v4, "public_ipv6": c.v6, "internal_ip": c.wantInt, "external_ip": c.external} {
				if got := addr[k]; got != want {
					t.Errorf("addresses.%s = %v, want %v", k, got, want)
				}
			}
			if codeSet(r.Findings)["NODE_INTERNAL_IP_INVALID"] {
				t.Errorf("NODE_INTERNAL_IP_INVALID for an address that is configured: %v", r.Findings)
			}
			validateNode(t, r.Node)
		})
	}
}

// second is a copy of the baseline as node-02: another machine id, address and host.
func (e *env) second(t testing.TB, name, ip, mid string) discovery.Input {
	t.Helper()
	other := baseline(t)
	other.facts["machine_id"] = mid
	other.facts["ip_addr"] = []any{other.facts["ip_addr"].([]any)[0], iface("eno1", 2, v4(ip, "global"))}
	other.facts["ip_route_get"] = []any{map[string]any{"dst": "1.1.1.1", "gateway": "203.0.113.1", "dev": "eno1", "prefsrc": ip}}
	other.in.Node.Name, other.in.Node.Host = name, ip
	b, err := json.Marshal(other.facts)
	if err != nil {
		t.Fatal(err)
	}
	other.in.Facts = b
	return other.in
}

func (e *env) first(t testing.TB) discovery.Input {
	t.Helper()
	b, err := json.Marshal(e.facts)
	if err != nil {
		t.Fatal(err)
	}
	e.in.Facts = b
	return e.in
}

func anyHas(rs []discovery.Result, key string) bool {
	for _, r := range rs {
		if findingSet(r.Findings)[key] {
			return true
		}
	}
	return false
}

func evalAll(t testing.TB, in []discovery.Input, c discovery.Context) []discovery.Result {
	t.Helper()
	rs, err := discovery.Evaluate(in, c)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(rs) != len(in) {
		t.Fatalf("Evaluate returned %d results for %d inputs", len(rs), len(in))
	}
	return rs
}

// 05-AC-2: findings that involve several nodes, and the roles of plan 05 section 7.2.
func TestDiscoveryCrossNode_05AC2(t *testing.T) {
	t.Run("duplicate-machine-id", func(t *testing.T) {
		e := baseline(t)
		rs := evalAll(t, []discovery.Input{e.first(t), e.second(t, "node-02", "203.0.113.11", e.facts["machine_id"].(string))}, e.ctx)
		if !anyHas(rs, "EXISTING_DUPLICATE_NODE:fail") {
			t.Errorf("no EXISTING_DUPLICATE_NODE for two entries with one machine id: %v / %v", rs[0].Findings, rs[1].Findings)
		}
	})
	t.Run("distinct-nodes-no-cross-findings-and-roles", func(t *testing.T) {
		e := baseline(t)
		// reversed input order: results follow the input order, roles follow the name order
		rs := evalAll(t, []discovery.Input{e.second(t, "node-02", "203.0.113.11", "ffffffffffffffffffffffffffffffff"), e.first(t)}, e.ctx)
		for _, k := range []string{"EXISTING_DUPLICATE_NODE:fail", "EXISTING_MIXED_SIZE_CLASS:fail", "NODE_INTERNAL_IP_INVALID:fail"} {
			if anyHas(rs, k) {
				t.Errorf("unexpected %s for two distinct identical nodes", k)
			}
		}
		if rs[0].Node == nil || rs[1].Node == nil {
			t.Fatalf("missing nodes")
		}
		if rs[0].Node["name"] != "node-02" || rs[1].Node["name"] != "node-01" {
			t.Fatalf("results are not in input order: %v, %v", rs[0].Node["name"], rs[1].Node["name"])
		}
		if got := fmt.Sprint(rs[1].Node["roles"]); got != "[server]" {
			t.Errorf("node-01 roles = %s, want [server] (kubernetes.servers is 1)", got)
		}
		if got := fmt.Sprint(rs[0].Node["roles"]); got != "[agent]" {
			t.Errorf("node-02 roles = %s, want [agent]", got)
		}
	})
	t.Run("mixed-size-class", func(t *testing.T) {
		e := baseline(t)
		small := e.second(t, "node-02", "203.0.113.11", "ffffffffffffffffffffffffffffffff")
		var f map[string]any
		if err := json.Unmarshal(small.Facts, &f); err != nil {
			t.Fatal(err)
		}
		f["meminfo"] = fmt.Sprintf("MemTotal:       %d kB\n", 8*gib/1024) // 8 GiB: the constrained class (CD-23)
		b, _ := json.Marshal(f)
		small.Facts = b
		rs := evalAll(t, []discovery.Input{e.first(t), small}, e.ctx)
		if !anyHas(rs, "EXISTING_MIXED_SIZE_CLASS:fail") {
			t.Errorf("no EXISTING_MIXED_SIZE_CLASS for a 64 GiB and an 8 GiB node: %v / %v", rs[0].Findings, rs[1].Findings)
		}
	})
	t.Run("internal-ip-shared", func(t *testing.T) {
		e := baseline(t)
		e.in.Node.InternalIP = "203.0.113.10"
		second := e.second(t, "node-02", "203.0.113.10", "ffffffffffffffffffffffffffffffff")
		second.Node.InternalIP = "203.0.113.10"
		rs := evalAll(t, []discovery.Input{e.first(t), second}, e.ctx)
		if !anyHas(rs, "NODE_INTERNAL_IP_INVALID:fail") {
			t.Errorf("no NODE_INTERNAL_IP_INVALID for two nodes with one internal IP: %v / %v", rs[0].Findings, rs[1].Findings)
		}
	})
}

// 05-AC-2: undecodable facts are an error, not a guessed node.
func TestDiscoveryUndecodableFacts_05AC2(t *testing.T) {
	e := baseline(t)
	e.in.Facts = []byte("{not json")
	if _, err := discovery.Evaluate([]discovery.Input{e.in}, e.ctx); err == nil {
		t.Errorf("Evaluate accepted facts that are not JSON")
	}
}
