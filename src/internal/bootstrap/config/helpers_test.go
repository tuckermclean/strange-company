package config_test

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"strange.company/sc/internal/bootstrap/config"
	"strange.company/sc/internal/clock"
)

const fixtureUID = "6f1c2d3e-4b5a-4c6d-8e7f-0a1b2c3d4e5f"

func repoRoot(t testing.TB) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "plans", "50-bootstrap-orchestrator.md")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("repository root not found")
		}
		dir = parent
	}
}

func opts(profile string) config.Options {
	return config.Options{
		Clock:      clock.NewFake(time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)),
		InstallUID: fixtureUID,
		Version:    "sc-bootstrap v0.0.0-test",
		Profile:    profile,
	}
}

// planRow is one row of the plan 50 section 9.2 table.
type planRow struct{ first, cell string }

// planRows reads the rows of the section 9.2 table straight from the plan, so a new row cannot go unnoticed.
func planRows(t testing.TB) []planRow {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot(t), "plans", "50-bootstrap-orchestrator.md"))
	if err != nil {
		t.Fatal(err)
	}
	var rows []planRow
	in := false
	tick := regexp.MustCompile("`([^`]+)`")
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "### 9.2 ") {
			in = true
			continue
		}
		if in && strings.HasPrefix(line, "### ") {
			break
		}
		if !in || !strings.HasPrefix(line, "| `") {
			continue
		}
		cell := strings.Split(line, "|")[1]
		m := tick.FindStringSubmatch(cell)
		if m == nil {
			t.Fatalf("plan row without a variable: %q", line)
		}
		rows = append(rows, planRow{first: m[1], cell: cell})
	}
	if len(rows) == 0 {
		t.Fatal("no rows found in plan 50 section 9.2")
	}
	return rows
}

// get resolves a slash pointer in a parsed document. A segment key=value selects the array element whose key
// field equals value. Values are compared by their fmt.Sprint form; an absent or null node is "<nil>".
func get(doc any, ptr string) string {
	cur := doc
	for _, seg := range strings.Split(strings.TrimPrefix(ptr, "/"), "/") {
		switch c := cur.(type) {
		case map[string]any:
			cur = c[seg]
		case []any:
			if k, v, ok := strings.Cut(seg, "="); ok {
				var found any
				for _, e := range c {
					if m, ok := e.(map[string]any); ok && fmt.Sprint(m[k]) == v {
						found = e
					}
				}
				cur = found
			} else if i, err := strconv.Atoi(seg); err == nil && i >= 0 && i < len(c) {
				cur = c[i]
			} else {
				cur = nil
			}
		default:
			cur = nil
		}
	}
	return fmt.Sprint(cur)
}

func merge(base map[string]string, set map[string]string, drop []string) map[string]string {
	out := map[string]string{}
	for k, v := range base {
		out[k] = v
	}
	for _, k := range drop {
		delete(out, k)
	}
	for k, v := range set {
		out[k] = v
	}
	return out
}

// baseExisting is a minimal valid install on the existing provider with direct ingress.
func baseExisting() map[string]string {
	return map[string]string{
		"CLUSTER_NAME": "lab-one", "BASE_DOMAIN": "lab.example.net", "ADMIN_EMAIL": "ops@example.net",
		"PROVIDER": "existing", "EXPECTED_NODE_COUNT": "1", "MAX_MONTHLY_COST": "0",
		"EXISTING_NODES": "node-01=debian@192.0.2.10:2201/10.77.0.11",
		"INGRESS_MODE":   "direct", "DNS_PROVIDER": "manual", "ACME_SOLVER": "http01",
		"S3_ENDPOINT": "https://s3.example.net", "S3_REGION": "us-east-1", "S3_CREDENTIAL_MODE": "scoped",
		"S3_ACCESS_KEY_ID": "fixture-secret-a", "S3_SECRET_ACCESS_KEY": "fixture-secret-b",
		"GITHUB_MIRROR_REPO": "example-org/sc-lab", "GITHUB_MIRROR_TOKEN": "fixture-secret-c",
		"GITHUB_PROBE_TOKEN": "fixture-secret-d", "GITHUB_MIRROR_READ_TOKEN": "fixture-secret-e",
		"NOTIFY_TRANSPORT": "webhook", "NOTIFY_WEBHOOK_URL": "fixture-secret-f",
		"ANTHROPIC_API_KEY": "fixture-secret-g",
	}
}

// baseOVH is a minimal valid install on ovh with the default tunnel ingress.
func baseOVH() map[string]string {
	return map[string]string{
		"CLUSTER_NAME": "prod", "BASE_DOMAIN": "example.com", "ADMIN_EMAIL": "ops@example.com",
		"PROVIDER": "ovh", "PROVIDER_REGION": "GRA", "EXPECTED_NODE_COUNT": "1",
		"MACHINE_CLASS": "ovh-vps-6c-12g-100nvme", "MAX_MONTHLY_COST": "25.00",
		"OVH_APPLICATION_KEY": "fixture-secret-h", "OVH_APPLICATION_SECRET": "fixture-secret-i", "OVH_CONSUMER_KEY": "fixture-secret-j",
		"CLOUDFLARE_ACCOUNT_ID": "0123456789abcdef0123456789abcdef",
		"CLOUDFLARE_API_TOKEN":  "fixture-secret-k", "CLOUDFLARE_ACME_TOKEN": "fixture-secret-l",
		"S3_ENDPOINT": "https://s3.gra.io.cloud.ovh.net", "S3_REGION": "gra", "S3_PROVIDER": "ovh",
		"S3_ACCESS_KEY_ID": "fixture-secret-a", "S3_SECRET_ACCESS_KEY": "fixture-secret-b",
		"GITHUB_MIRROR_REPO": "example-org/strange-company", "GITHUB_MIRROR_TOKEN": "fixture-secret-c",
		"GITHUB_PROBE_TOKEN": "fixture-secret-d", "GITHUB_MIRROR_READ_TOKEN": "fixture-secret-e",
		"NOTIFY_TRANSPORT": "webhook", "NOTIFY_WEBHOOK_URL": "fixture-secret-f",
		"ANTHROPIC_API_KEY": "fixture-secret-g",
	}
}

func base(name string) map[string]string {
	if name == "ovh" {
		return baseOVH()
	}
	return baseExisting()
}

func mustNormalize(t *testing.T, env map[string]string, profile string) *config.Result {
	t.Helper()
	res, err := config.Normalize(env, opts(profile))
	if err != nil {
		t.Fatalf("Normalize: unexpected error: %v", err)
	}
	if res == nil || res.Doc == nil || len(res.YAML) == 0 {
		t.Fatalf("Normalize returned no document: %+v", res)
	}
	return res
}
