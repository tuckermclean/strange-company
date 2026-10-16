package lint_test

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	"strange.company/sc/internal/lint"
)

// Rule IDs of `sc-lint locks` (plan 01 section 6.3 checks 1-3). The plan names no IDs; chosen here.
const (
	ruleLockSchema   = "lock-schema"
	ruleImageDigest  = "image-digest"
	ruleActionPin    = "action-sha-pin"
	ruleChartExact   = "chart-version-exact"
	ruleFluxMatch    = "flux-version-match"
	ruleKubectlMinor = "kubectl-minor-match"
)

var allLockRules = []string{ruleLockSchema, ruleImageDigest, ruleActionPin, ruleChartExact, ruleFluxMatch, ruleKubectlMinor}

var (
	hex64a = strings.Repeat("a1", 32)
	hex64b = strings.Repeat("b2", 32)
	sha40  = "3d3c42e5aac5ba805825da76410c181273ba90b1"
)

const platformLockTmpl = `apiVersion: strange.company/v1alpha1
kind: PlatformVersionLock
metadata: {name: platform}
spec:
  kubernetes: {minor: "1.36"}
  flux: {version: "2.9.6", selectedBy: "baseline:flux", verifiedAt: "2026-10-14"}
  charts:
    - name: cert-manager
      source: {oci: oci://quay.io/jetstack/charts/cert-manager}
      version: "%s"
      digest: sha256:%s
      usedBy: gitops/platform/cert-manager
  images:
    - name: app
      ref: docker.io/library/nginx@sha256:%s
      tag: "1.27"
      usedBy: gitops/platform/x
`

const k3sLockTmpl = `apiVersion: strange.company/v1alpha1
kind: K3sVersionLock
metadata: {name: k3s}
spec:
  k3s:
    version: v1.36.5+k3s1
    kubernetesMinor: "%s"
    sha256: {amd64: %s}
    selectedBy: "baseline:k3s"
    verifiedAt: "2026-10-14"
  etcdctl: {version: "3.6.4", sha256: {amd64: %s}}
  coredns: {image: registry.k8s.io/coredns/coredns@sha256:%s}
`

const toolsLockTmpl = `apiVersion: strange.company/v1alpha1
kind: ToolboxLock
metadata: {name: seed-ci}
spec:
  tools:
    - name: flux
      set: ci
      version: "%s"
      selectedBy: "baseline:flux"
      url: https://github.com/fluxcd/flux2/releases/download/v{version}/flux_{version}_linux_{arch}.tar.gz
      sha256: {amd64: %s}
      install: {extract: flux, to: /usr/local/bin/flux}
      versionCommand: [flux, --version]
      versionRegex: '{version}'
      verifiedAt: "2026-10-15"
    - name: kubectl
      set: ci
      version: "%s"
      selectedBy: "baseline:kubectl"
      url: https://dl.k8s.io/release/v{version}/bin/linux/{arch}/kubectl
      sha256: {amd64: %s}
      install: {to: /usr/local/bin/kubectl}
      versionCommand: [kubectl, version, --client]
      versionRegex: 'v{version}'
      verifiedAt: "2026-10-15"
  baseImages:
    debian: {ref: docker.io/library/debian:trixie-slim, digest: "sha256:%s"}
    golang: {ref: docker.io/library/golang:1.27.2-trixie, digest: "sha256:%s"}
  debianSnapshot: 20261001T000000Z
  aptPackages:
    runtime: [ca-certificates]
    ci: [make]
  pythonRequirements: toolbox/requirements.txt
  ansibleCollections: []
`

func workflowTmpl(uses string) string {
	return "name: x\non: [push]\npermissions:\n  contents: read\njobs:\n  a:\n    runs-on: ubuntu-24.04\n    steps:\n      - uses: " + uses + "\n"
}

func deployTmpl(image string) string {
	return "apiVersion: apps/v1\nkind: Deployment\nmetadata: {name: x}\nspec:\n  template:\n    spec:\n      containers:\n        - name: x\n          image: " + image + "\n"
}

// locksTree writes a consistent, rule-clean tree (no git needed) for sc-lint locks.
func locksTree(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	write(t, d, "versions/platform.lock.yaml", fmt.Sprintf(platformLockTmpl, "1.19.1", hex64a, hex64b))
	write(t, d, "versions/k3s.lock.yaml", fmt.Sprintf(k3sLockTmpl, "1.36", hex64a, hex64b, hex64a))
	write(t, d, "toolbox/tools.lock.yaml", fmt.Sprintf(toolsLockTmpl, "2.9.6", hex64a, "1.36.5", hex64b, hex64a, hex64b))
	write(t, d, ".github/workflows/verify.yaml", workflowTmpl("actions/checkout@"+sha40+" # v7"))
	write(t, d, "gitops/platform/x/base/deploy.yaml", deployTmpl("docker.io/library/nginx@sha256:"+hex64a))
	return d
}

func mustLocks(t *testing.T, dir string) lint.Result {
	t.Helper()
	r, err := lint.LocksCheck(dir)
	if err != nil {
		t.Fatalf("LocksCheck: %v", err)
	}
	return r
}

// 01-AC-7: the consistent fixture passes and every rule was evaluated.
func TestLocksCheck_ValidFixturePasses_01AC7(t *testing.T) {
	r := mustLocks(t, locksTree(t))
	ranRules(t, r, allLockRules...)
	if f := errorsOf(r); len(f) != 0 {
		t.Errorf("valid fixture has findings: %v", f)
	}
}

// 01-AC-7: each fixture mutation fails with its specific rule ID, in the findings and via sc-lint locks.
func TestLocksCheck_Mutations_01AC7(t *testing.T) {
	cases := []struct {
		name, rule string
		mutate     func(t *testing.T, d string)
	}{
		{"image without digest in gitops/platform/x/base", ruleImageDigest, func(t *testing.T, d string) {
			write(t, d, "gitops/platform/x/base/deploy.yaml", deployTmpl("docker.io/library/nginx:1.27"))
		}},
		{"uses actions/checkout@v4", ruleActionPin, func(t *testing.T, d string) {
			write(t, d, ".github/workflows/verify.yaml", workflowTmpl("actions/checkout@v4"))
		}},
		{"flux CLI version differs from platform.lock", ruleFluxMatch, func(t *testing.T, d string) {
			write(t, d, "toolbox/tools.lock.yaml", fmt.Sprintf(toolsLockTmpl, "2.9.5", hex64a, "1.36.5", hex64b, hex64a, hex64b))
		}},
		{"kubectl minor differs from k3s minor", ruleKubectlMinor, func(t *testing.T, d string) {
			write(t, d, "toolbox/tools.lock.yaml", fmt.Sprintf(toolsLockTmpl, "2.9.6", hex64a, "1.35.2", hex64b, hex64a, hex64b))
		}},
		{"chart version range in platform.lock", ruleChartExact, func(t *testing.T, d string) {
			write(t, d, "versions/platform.lock.yaml", fmt.Sprintf(platformLockTmpl, "^1.2", hex64a, hex64b))
		}},
		{"chart version range in a HelmRelease", ruleChartExact, func(t *testing.T, d string) {
			write(t, d, "gitops/platform/x/base/release.yaml", "apiVersion: helm.toolkit.fluxcd.io/v2\nkind: HelmRelease\nmetadata: {name: x}\nspec:\n  chart:\n    spec:\n      chart: cert-manager\n      version: \"^1.2\"\n")
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := locksTree(t)
			c.mutate(t, d)
			r := mustLocks(t, d)
			if !hasRule(r, c.rule) {
				t.Errorf("want rule %s, got findings %v", c.rule, r.Findings)
			}
			var out, errb bytes.Buffer
			code := lint.Main([]string{"locks", "--root", d}, &out, &errb)
			if code != 1 || !strings.Contains(out.String(), c.rule) {
				t.Errorf("sc-lint locks: exit %d (want 1), output must name %s:\n%s%s", code, c.rule, out.String(), errb.String())
			}
		})
	}
}

// 01-AC-7: the lock files must validate against their schema (rule lock-schema).
func TestLocksCheck_SchemaViolation_01AC7(t *testing.T) {
	d := locksTree(t)
	write(t, d, "versions/k3s.lock.yaml", "apiVersion: strange.company/v1alpha1\nkind: K3sVersionLock\nspec: {}\n")
	r := mustLocks(t, d)
	if !hasRule(r, ruleLockSchema) {
		t.Errorf("want rule %s, got %v", ruleLockSchema, r.Findings)
	}
}

// 01-AC-7: `make locks-check` exits 0 on the real tree (it is a placeholder that exits 1 until S3).
func TestRealTree_MakeLocksCheck_01AC7(t *testing.T) {
	runMake(t, "locks-check")
}

func TestRealTree_LocksCheckPasses_01AC7(t *testing.T) {
	r := mustLocks(t, repoRoot(t))
	ranRules(t, r, allLockRules...)
	for _, f := range errorsOf(r) {
		t.Errorf("real tree: %s", f)
	}
}

// runMake runs `make <target>` in the real repository root and fails the test unless it exits 0.
func runMake(t *testing.T, target string) {
	t.Helper()
	cmd := exec.Command("make", target)
	cmd.Dir = repoRoot(t)
	cmd.Env = append(os.Environ(), "SC_IN_CONTAINER=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("make %s: %v\n%s", target, err, out)
	}
}
