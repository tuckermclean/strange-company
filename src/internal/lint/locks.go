package lint

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Rule IDs of `sc-lint locks` (plan 01 section 6.3 checks 1-3).
const (
	ruleLockSchema   = "lock-schema"
	ruleImageDigest  = "image-digest"
	ruleActionPin    = "action-sha-pin"
	ruleChartExact   = "chart-version-exact"
	ruleFluxMatch    = "flux-version-match"
	ruleKubectlMinor = "kubectl-minor-match"
	ruleCompatMinor  = "compat-minor"
)

// lockKinds maps each lock file to the $defs entry of contracts/version-locks.schema.yaml that governs it.
var lockKinds = map[string]string{
	"versions/platform.lock.yaml":        "PlatformVersionLock",
	"versions/k3s.lock.yaml":             "K3sVersionLock",
	"versions/compat.yaml":               "CompatibilityMatrix",
	"versions/internal-images.lock.yaml": "InternalImageLock",
	"versions/lab-images.lock.yaml":      "LabImageLock",
	"infra/lab/lab.yaml":                 "LabSpec",
	"toolbox/tools.lock.yaml":            "ToolboxLock",
	"toolbox/image.lock":                 "ToolboxImageLock",
}

// floatingScanDirs are the trees of plan 01 section 6.3 check 2.
var floatingScanDirs = []string{"gitops", "charts", "toolbox", "images", "harnesses", ".woodpecker", ".github/workflows", "infra"}

var (
	imageKeyRe   = regexp.MustCompile(`^\s*(?:-\s+)?image:\s*(\S.*)$`)
	usesKeyRe    = regexp.MustCompile(`^\s*(?:-\s+)?uses:\s*(\S.*)$`)
	fromRe       = regexp.MustCompile(`(?i)^\s*FROM\s+(?:--\S+\s+)*(\S+)(?:\s+AS\s+(\S+))?`)
	digestRefRe  = regexp.MustCompile(`@sha256:[0-9a-f]{64}`)
	actionPinRe  = regexp.MustCompile(`^[A-Za-z0-9._-]+/[A-Za-z0-9._/-]+@[0-9a-f]{40}$`)
	exactSemverR = regexp.MustCompile(`^v?[0-9]+\.[0-9]+\.[0-9]+([-+][0-9A-Za-z.+-]+)?$`)
)

// scalar strips a trailing comment and surrounding quotes from a YAML flow scalar written on one line.
func scalar(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 0 && (s[0] == '"' || s[0] == '\'') {
		if i := strings.IndexByte(s[1:], s[0]); i >= 0 {
			return s[1 : 1+i]
		}
	}
	if i := strings.Index(s, " #"); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// LocksCheck evaluates the version-lock rules of plan 01 section 6.3 on the tree under root.
func LocksCheck(root string) (Result, error) {
	var r Result
	files, err := listFiles(root)
	if err != nil {
		return r, err
	}
	if err := checkLockSchemas(root, &r); err != nil {
		return r, err
	}
	checkFloating(root, files, &r)
	checkLockConsistency(root, &r)
	return r, nil
}

func checkLockSchemas(root string, r *Result) error {
	r.ran(ruleLockSchema)
	var items []string
	for rel, kind := range lockKinds {
		if fileExists(filepath.Join(root, rel)) {
			items = append(items, filepath.Join(root, rel)+":"+kind)
		}
	}
	if len(items) == 0 {
		return nil
	}
	sort.Strings(items)
	vs, err := validateSchema(root, "version-locks.schema.yaml", "@kind", items)
	if err != nil {
		return err
	}
	for _, v := range vs {
		rel, _ := filepath.Rel(root, v.File)
		r.add(ruleLockSchema, filepath.ToSlash(rel), "%s: %s", v.Path, v.Message)
	}
	return nil
}

func isYAML(p string) bool { return strings.HasSuffix(p, ".yaml") || strings.HasSuffix(p, ".yml") }

func isContainerfile(p string) bool {
	b := path.Base(p)
	return strings.HasPrefix(b, "Containerfile") || strings.HasPrefix(b, "Dockerfile") || strings.HasSuffix(b, ".Containerfile") || strings.HasSuffix(b, ".Dockerfile")
}

func checkFloating(root string, files []string, r *Result) {
	r.ran(ruleImageDigest)
	r.ran(ruleActionPin)
	r.ran(ruleChartExact)
	for _, rel := range files {
		if !hasPrefixDir(rel, floatingScanDirs...) {
			continue
		}
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			continue
		}
		switch {
		case isYAML(rel):
			scanYAMLLines(rel, b, r)
			if !hasPrefixDir(rel, "charts") || path.Base(rel) == "Chart.yaml" {
				scanHelmVersions(rel, b, r)
			}
		case isContainerfile(rel):
			scanContainerfile(rel, b, r)
		}
	}
	if v, err := readYAML(root, "versions/platform.lock.yaml"); err == nil {
		if cs, ok := at(v, "spec", "charts").([]any); ok {
			for _, c := range cs {
				if ver := str(at(c, "version")); !exactSemverR.MatchString(ver) {
					r.add(ruleChartExact, "versions/platform.lock.yaml", "chart %q: version %q is not an exact semver", str(at(c, "name")), ver)
				}
			}
		}
	}
}

func scanYAMLLines(rel string, b []byte, r *Result) {
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for n := 1; sc.Scan(); n++ {
		line := sc.Text()
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		if m := usesKeyRe.FindStringSubmatch(line); m != nil && hasPrefixDir(rel, ".github/workflows") {
			v := scalar(m[1])
			if !strings.HasPrefix(v, "./") && !actionPinRe.MatchString(v) {
				r.add(ruleActionPin, fmt.Sprintf("%s:%d", rel, n), "uses %q is not owner/repo@<40-hex commit SHA>", v)
			}
			continue
		}
		if m := imageKeyRe.FindStringSubmatch(line); m != nil {
			v := scalar(m[1])
			if v == "" || strings.ContainsAny(v[:1], "|>{&*[") || strings.Contains(v, "{{") {
				continue
			}
			if !digestRefRe.MatchString(v) {
				r.add(ruleImageDigest, fmt.Sprintf("%s:%d", rel, n), "image %q lacks @sha256:<64 hex>", v)
			}
		}
	}
}

func scanContainerfile(rel string, b []byte, r *Result) {
	stages := map[string]bool{"scratch": true}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for n := 1; sc.Scan(); n++ {
		m := fromRe.FindStringSubmatch(sc.Text())
		if m == nil {
			continue
		}
		if !stages[m[1]] && !digestRefRe.MatchString(m[1]) {
			r.add(ruleImageDigest, fmt.Sprintf("%s:%d", rel, n), "FROM %q lacks @sha256:<64 hex>", m[1])
		}
		if m[2] != "" {
			stages[m[2]] = true
		}
	}
}

// scanHelmVersions applies chart-version-exact to HelmRelease, HelmChart and Chart.yaml dependencies.
func scanHelmVersions(rel string, b []byte, r *Result) {
	docs, _ := yamlDocs(b) // unparsable files are gitops-validate's concern
	for _, d := range docs {
		switch str(at(d, "kind")) {
		case "HelmRelease":
			if spec, ok := at(d, "spec", "chart", "spec").(map[string]any); ok {
				if v := str(spec["version"]); !exactSemverR.MatchString(v) {
					r.add(ruleChartExact, rel, "HelmRelease chart %q: version %q is not an exact semver", str(spec["chart"]), v)
				}
			}
		case "HelmChart":
			if v := str(at(d, "spec", "version")); !exactSemverR.MatchString(v) {
				r.add(ruleChartExact, rel, "HelmChart: version %q is not an exact semver", v)
			}
		}
		if path.Base(rel) == "Chart.yaml" {
			if deps, ok := at(d, "dependencies").([]any); ok {
				for _, dep := range deps {
					if v := str(at(dep, "version")); !exactSemverR.MatchString(v) {
						r.add(ruleChartExact, rel, "dependency %q: version %q is not an exact semver", str(at(dep, "name")), v)
					}
				}
			}
		}
	}
}

func minorOf(version string) string {
	p := strings.Split(strings.TrimPrefix(version, "v"), ".")
	if len(p) < 2 {
		return ""
	}
	return p[0] + "." + p[1]
}

func toolVersion(tools any, name string) string {
	ts, _ := tools.([]any)
	for _, t := range ts {
		if str(at(t, "name")) == name {
			return str(at(t, "version"))
		}
	}
	return ""
}

func checkLockConsistency(root string, r *Result) {
	r.ran(ruleFluxMatch)
	r.ran(ruleKubectlMinor)
	platform, _ := readYAML(root, "versions/platform.lock.yaml")
	k3s, _ := readYAML(root, "versions/k3s.lock.yaml")
	tools, _ := readYAML(root, "toolbox/tools.lock.yaml")
	toolList := at(tools, "spec", "tools")

	fluxPlatform := strings.TrimPrefix(str(at(platform, "spec", "flux", "version")), "v")
	fluxCLI := strings.TrimPrefix(toolVersion(toolList, "flux"), "v")
	if fluxPlatform != "" && fluxCLI != "" && fluxPlatform != fluxCLI {
		r.add(ruleFluxMatch, "toolbox/tools.lock.yaml", "Flux CLI version %s differs from versions/platform.lock.yaml flux.version %s", fluxCLI, fluxPlatform)
	}

	minors := []struct{ file, what, minor string }{
		{"versions/platform.lock.yaml", "kubernetes.minor", str(at(platform, "spec", "kubernetes", "minor"))},
		{"versions/k3s.lock.yaml", "k3s.kubernetesMinor", str(at(k3s, "spec", "k3s", "kubernetesMinor"))},
		{"toolbox/tools.lock.yaml", "kubectl minor", minorOf(toolVersion(toolList, "kubectl"))},
	}
	var ref *struct{ file, what, minor string }
	for i := range minors {
		m := &minors[i]
		if m.minor == "" {
			continue
		}
		if ref == nil {
			ref = m
			continue
		}
		if m.minor != ref.minor {
			r.add(ruleKubectlMinor, m.file, "%s %s differs from %s %s in %s (skew 0)", m.what, m.minor, ref.what, ref.minor, ref.file)
		}
	}

	r.ran(ruleCompatMinor)
	compat, err := readYAML(root, "versions/compat.yaml")
	if err != nil || ref == nil {
		return
	}
	if comps, ok := at(compat, "spec", "components").(map[string]any); ok {
		for name, c := range comps {
			found := false
			ms, _ := at(c, "kubernetesMinors").([]any)
			for _, m := range ms {
				found = found || str(m) == ref.minor
			}
			if !found {
				r.add(ruleCompatMinor, "versions/compat.yaml", "component %s does not list Kubernetes minor %s as supported", name, ref.minor)
			}
		}
	}
}
