package lab_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"strange.company/sc/internal/contracts"
)

func realRoot(t *testing.T) string {
	t.Helper()
	d, _ := os.Getwd()
	for {
		if _, err := os.Stat(filepath.Join(d, "plans", "dag.yaml")); err == nil {
			return d
		}
		p := filepath.Dir(d)
		if p == d {
			t.Fatal("repository root not found")
		}
		d = p
	}
}

func readDoc(t *testing.T, root, rel string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatalf("%s: %v", rel, err)
	}
	var m map[string]any
	if err := yaml.Unmarshal(b, &m); err != nil {
		t.Fatalf("%s: %v", rel, err)
	}
	return m
}

func sub(m map[string]any, keys ...string) map[string]any {
	for _, k := range keys {
		n, _ := m[k].(map[string]any)
		if n == nil {
			return nil
		}
		m = n
	}
	return m
}

// 05-AC-7: versions/lab-images.lock.yaml pins both OS images with real https URLs and hex checksums.
func TestRealTree_LabImageLock_05AC7(t *testing.T) {
	root := realRoot(t)
	doc := readDoc(t, root, "versions/lab-images.lock.yaml")
	if doc["kind"] != "LabImageLock" {
		t.Fatalf("kind = %v, want LabImageLock", doc["kind"])
	}
	hex256, hex512 := regexp.MustCompile(`^[0-9a-f]{64}$`), regexp.MustCompile(`^[0-9a-f]{128}$`)
	for _, osName := range []string{"debian-13", "ubuntu-24.04"} {
		amd := sub(doc, "spec", "images", osName, "amd64")
		if amd == nil {
			t.Errorf("spec.images.%s.amd64 missing", osName)
			continue
		}
		if u, _ := amd["url"].(string); !strings.HasPrefix(u, "https://") || strings.Contains(u, "example") {
			t.Errorf("%s url %q is not a real https URL", osName, u)
		}
		s256, _ := amd["sha256"].(string)
		s512, _ := amd["sha512"].(string)
		if !hex256.MatchString(s256) && !hex512.MatchString(s512) {
			t.Errorf("%s has no 64-hex sha256 or 128-hex sha512", osName)
		}
	}
	// Plan 05 14.6: Ubuntu publishes sha256, Debian sha512; the task asks for a 64-hex sha256 for both images.
	for _, osName := range []string{"debian-13", "ubuntu-24.04"} {
		if s, _ := sub(doc, "spec", "images", osName, "amd64")["sha256"].(string); !hex256.MatchString(s) {
			t.Errorf("%s: sha256 %q is not 64 hex", osName, s)
		}
	}
}

// 05-AC-7: infra/lab/lab.yaml carries the plan 05 14.6 table.
func TestRealTree_LabSpec_05AC7(t *testing.T) {
	doc := readDoc(t, realRoot(t), "infra/lab/lab.yaml")
	if doc["kind"] != "LabSpec" {
		t.Fatalf("kind = %v, want LabSpec", doc["kind"])
	}
	if got := sub(doc, "spec")["internalNetwork"]; got != "10.77.0.0/24" {
		t.Errorf("internalNetwork = %v, want 10.77.0.0/24", got)
	}
	if got := sub(doc, "spec")["sshPortBase"]; got != 2200 {
		t.Errorf("sshPortBase = %v, want 2200 (node-01 -> 2201)", got)
	}
	want := map[string]map[string]any{
		"lab-small": {"nodes": 1, "vcpu": 2, "memory": "4Gi", "disk": "20Gi", "diskDevice": "virtio-blk"},
		"lab-ha":    {"nodes": 3, "vcpu": 2, "memory": "4Gi", "disk": "20Gi", "diskDevice": "scsi-hd-ssd"},
		"lab-seed":  {"nodes": 1, "vcpu": 4, "memory": "8Gi", "disk": "40Gi", "diskDevice": "virtio-blk"},
	}
	for name, fields := range want {
		got := sub(doc, "spec", "substrates", name)
		if got == nil {
			t.Errorf("substrate %s missing", name)
			continue
		}
		for k, v := range fields {
			if got[k] != v {
				t.Errorf("%s.%s = %v, want %v", name, k, got[k], v)
			}
		}
	}
}

// 05-AC-7 / plan 03 9.1: the three lab profiles exist, are lab-labeled BootstrapSubstrates sized like the lab VMs.
func TestRealTree_LabProfiles_05AC7(t *testing.T) {
	root := realRoot(t)
	type shape struct {
		count, cpu int
		mem, disk  string
	}
	want := map[string]shape{
		"lab-small": {1, 2, "4Gi", "20Gi"},
		"lab-ha":    {3, 2, "4Gi", "20Gi"},
		"lab-seed":  {1, 4, "8Gi", "40Gi"},
	}
	for name, w := range want {
		doc := readDoc(t, root, "infra/profiles/"+name+".yaml")
		if doc["kind"] != "BootstrapSubstrate" {
			t.Errorf("%s: kind = %v, want BootstrapSubstrate", name, doc["kind"])
		}
		if sub(doc, "metadata")["name"] != name {
			t.Errorf("%s: metadata.name = %v", name, sub(doc, "metadata")["name"])
		}
		if sub(doc, "metadata", "labels")["strange.company/lab"] != "true" {
			t.Errorf("%s: label strange.company/lab: \"true\" missing", name)
		}
		nodes := sub(doc, "spec", "nodes")
		if nodes["count"] != w.count {
			t.Errorf("%s: nodes.count = %v, want %d", name, nodes["count"], w.count)
		}
		minShape := sub(doc, "spec", "nodes", "minimumSupportedPerNode")
		if minShape["cpu"] != w.cpu || minShape["memory"] != w.mem || sub(minShape, "storage")["size"] != w.disk {
			t.Errorf("%s: minimumSupportedPerNode = %v, want cpu %d memory %s storage %s", name, minShape, w.cpu, w.mem, w.disk)
		}
	}
}

// 05-AC-7: the real lab files validate against their schemas in `make contracts` (instance rows for
// infra/profiles/*.yaml, versions/*.yaml and infra/lab/lab.yaml): at least one pass row each, no failures.
func TestRealTree_LabFilesValidate_05AC7(t *testing.T) {
	root := realRoot(t)
	rows := contracts.Run(root)
	for _, f := range []string{"versions/lab-images.lock.yaml", "infra/lab/lab.yaml", "infra/profiles/lab-small.yaml", "infra/profiles/lab-ha.yaml", "infra/profiles/lab-seed.yaml"} {
		pass := 0
		for _, r := range rows {
			if filepath.ToSlash(r.File) != f && !strings.HasSuffix(filepath.ToSlash(r.File), "/"+f) {
				continue
			}
			switch r.Result {
			case "fail":
				t.Errorf("%s: %s failed: %s", f, r.Check, r.Detail)
			case "pass":
				pass++
			}
		}
		if pass == 0 {
			t.Errorf("%s has no passing contracts row", f)
		}
	}
}
