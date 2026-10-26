package worker_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Slice S6d of F07 (Layer 2, brief D2/D3/D4). The container-level suite (test/e2e/worker/run) is never a red target; these
// tests pin, statically and without a container engine or network, what that suite and the mk targets rely on:
// the harness-fake image definition (plan 28 section 13.1, harnesses/fake per brief D2), its package allowlist, the mk
// targets `harness-image-test` and `test-worker-component` (plan 01 section 8, tier E: not in VERIFY_TARGETS) and the
// script itself (mode 100755). The runtime evidence (28-AC-3, 27-AC-5, 27-AC-16, 27-AC-24, real ssh) is produced by the
// script in Layer 2.

const (
	imageDir       = "harnesses/fake"
	lockPath       = "toolbox/tools.lock.yaml"
	scriptPath     = "test/e2e/worker/run"
	sha256Ref      = `@sha256:[0-9a-f]{64}`
	denylistedPkgs = `^(docker|docker\.io|docker-ce.*|docker-cli|docker-compose.*|podman.*|containerd.*|runc|crun|buildah|skopeo|nerdctl|cri-tools|crictl|kubectl|kubernetes-client|awscli|google-cloud-.*|google-cloud-cli|azure-cli|oc)$`
)

func readRepoFile(t *testing.T, rel string) (string, bool) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot(t), filepath.FromSlash(rel)))
	if err != nil {
		t.Errorf("%s: %v", rel, err)
		return "", false
	}
	return string(b), true
}

// lockDigest returns the digest of baseImages.<name> in toolbox/tools.lock.yaml.
func lockDigest(t *testing.T, name string) string {
	t.Helper()
	lock, ok := readRepoFile(t, lockPath)
	if !ok {
		return ""
	}
	m := regexp.MustCompile(`(?m)^    ` + name + `: \{ref: [^,]*, digest: "(sha256:[0-9a-f]{64})"\}`).FindStringSubmatch(lock)
	if m == nil {
		t.Fatalf("%s has no baseImages.%s digest", lockPath, name)
	}
	return m[1]
}

type containerStage struct {
	ref   string // as written after FROM (flags removed)
	alias string
	lines []string // instructions of the stage, continuation lines joined, comments dropped
}

// parseContainerfile splits a Containerfile into stages.
func parseContainerfile(text string) []containerStage {
	var stages []containerStage
	var cur []string
	flush := func() {
		if len(cur) == 0 {
			return
		}
		ins := strings.Join(cur, " ")
		cur = nil
		f := strings.Fields(ins)
		if len(f) > 0 && strings.EqualFold(f[0], "FROM") {
			var args []string
			for _, a := range f[1:] {
				if !strings.HasPrefix(a, "--") {
					args = append(args, a)
				}
			}
			st := containerStage{}
			if len(args) > 0 {
				st.ref = args[0]
			}
			if len(args) >= 3 && strings.EqualFold(args[1], "AS") {
				st.alias = args[2]
			}
			stages = append(stages, st)
			return
		}
		if len(stages) > 0 {
			stages[len(stages)-1].lines = append(stages[len(stages)-1].lines, ins)
		}
	}
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimRight(raw, " \t\r")
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "#") && len(cur) == 0 {
			continue
		}
		if trim == "" && len(cur) == 0 {
			continue
		}
		if strings.HasSuffix(line, "\\") {
			cur = append(cur, strings.TrimSuffix(line, "\\"))
			continue
		}
		cur = append(cur, line)
		flush()
	}
	flush()
	return stages
}

// checkContainerfile returns the violations of the static rules of plan 28 section 13.1 in a harness image definition;
// debian is the digest of baseImages.debian in the lock.
func checkContainerfile(text, debian string) []string {
	var bad []string
	stages := parseContainerfile(text)
	if len(stages) == 0 {
		return []string{"the Containerfile has no FROM"}
	}
	pinned := regexp.MustCompile(sha256Ref + `$`)
	aliases := map[string]string{} // stage alias -> external base ref
	for i, s := range stages {
		if base, isStage := aliases[s.ref]; isStage {
			if s.alias != "" {
				aliases[s.alias] = base
			}
			continue
		}
		if !pinned.MatchString(s.ref) {
			bad = append(bad, fmt.Sprintf("stage %d FROM %q is not digest-pinned (@sha256:<64 hex>, 28 section 13.1: base image digest-pinned)", i, s.ref))
		}
		if strings.HasSuffix(s.ref, ":latest") {
			bad = append(bad, fmt.Sprintf("stage %d FROM %q uses a latest tag", i, s.ref))
		}
		if s.alias != "" {
			aliases[s.alias] = s.ref
		}
	}
	final := stages[len(stages)-1]
	base := final.ref
	if b, ok := aliases[final.ref]; ok {
		base = b
	}
	if !strings.HasSuffix(base, "@"+debian) {
		bad = append(bad, fmt.Sprintf("the final stage is based on %q, want the debian digest %s of %s (brief D2)", base, debian, lockPath))
	}
	body := strings.Join(final.lines, "\n")

	user := ""
	for _, l := range final.lines {
		if f := strings.Fields(l); len(f) == 2 && strings.EqualFold(f[0], "USER") {
			user = f[1]
		}
	}
	if user != "65532:65532" {
		bad = append(bad, fmt.Sprintf("the final USER is %q, want 65532:65532 (28 section 13.1)", user))
	}

	// both binaries are installed under /usr/local/bin (test/e2e/worker/run relies on these paths)
	for _, bin := range []string{"/usr/local/bin/sc-worker", "/usr/local/bin/sc-fake-harness"} {
		if !strings.Contains(text, bin) {
			bad = append(bad, fmt.Sprintf("the Containerfile never installs %s (one image serves both pod containers, plan 27 R-2 / 28 section 13.1)", bin))
		}
	}

	// labels (28 section 13.1), declared in the final stage
	for _, key := range []string{"strange.company/harness", "strange.company/cli-version", "strange.company/sc-worker-version",
		"org.opencontainers.image.source", "org.opencontainers.image.revision"} {
		found := false
		for _, l := range final.lines {
			if f := strings.Fields(l); len(f) > 1 && strings.EqualFold(f[0], "LABEL") && strings.Contains(l, key) {
				found = true
			}
		}
		if !found {
			bad = append(bad, "the final stage declares no LABEL "+key)
		}
	}
	if !regexp.MustCompile(`strange\.company/harness["=\s]+"?fake`).MatchString(body) {
		bad = append(bad, "the final stage does not set LABEL strange.company/harness to fake")
	}

	// no package manager usable at run time: removed in the final stage
	rm := regexp.MustCompile(`(?i)(\brm\b[^;&|]*\b(apt-get|apt|dpkg)\b|\bchmod\b[^;&|]*\b(apt-get|dpkg)\b|\bdpkg\s+(--purge|-P|--remove|-r)\b[^;&|]*\b(apt|dpkg)\b)`)
	if !rm.MatchString(body) {
		bad = append(bad, "the final stage never removes or disables apt-get/apt/dpkg (28 section 13.1: no package manager usable at run time)")
	}

	// OS packages come from the lock's dated snapshot; nothing is fetched by URL into the final stage
	if !strings.Contains(text, "snapshot.debian.org") && !strings.Contains(text, "tools.lock.yaml") {
		bad = append(bad, fmt.Sprintf("OS packages are not pinned to the dated snapshot of %s (28 section 13.1)", lockPath))
	}
	if regexp.MustCompile(`(?im)^\s*ADD\s+https?://`).MatchString(body) {
		bad = append(bad, "the final stage ADDs a remote URL")
	}
	cred := regexp.MustCompile(`(?i)(token|secret|password|api_?key|private_?key)`)
	for _, l := range final.lines {
		if f := strings.Fields(l); len(f) > 1 && (strings.EqualFold(f[0], "ENV") || strings.EqualFold(f[0], "ARG")) && cred.MatchString(f[1]) {
			bad = append(bad, fmt.Sprintf("%q declares a credential-looking name in the image", l))
		}
	}
	return bad
}

// 28-AC-3 (static part of plan 28 section 13.1): the Containerfile of the fake harness image.
func TestHarnessFakeImage_ContainerfileRules_28AC3(t *testing.T) {
	text, ok := readRepoFile(t, imageDir+"/Containerfile")
	if !ok {
		return
	}
	for _, msg := range checkContainerfile(text, lockDigest(t, "debian")) {
		t.Error(msg)
	}
}

// The rule checker itself: a model definition passes, and each mutation is caught (guard: passes now).
func TestHarnessImageRules_CheckerCatchesViolations_28AC3(t *testing.T) {
	debian := lockDigest(t, "debian")
	golang := lockDigest(t, "golang")
	model := `FROM docker.io/library/golang@` + golang + ` AS gobuild
COPY src/ /src/
RUN cd /src && go build -o /out/ ./cmd/sc-worker ./cmd/sc-fake-harness

# final
FROM docker.io/library/debian@` + debian + `
ARG SC_INPUTS_ID=unknown
COPY toolbox/tools.lock.yaml /usr/share/strange-company/tools.lock.yaml
RUN set -eu; \
    apt-get update; apt-get install -y --no-install-recommends git openssh-client ca-certificates make; \
    dpkg-query -W -f='${Package}\n' > /usr/share/strange-company/packages.txt; \
    rm -f /usr/bin/apt /usr/bin/apt-get /usr/bin/dpkg
COPY --from=gobuild /out/sc-worker /usr/local/bin/sc-worker
COPY --from=gobuild /out/sc-fake-harness /usr/local/bin/sc-fake-harness
LABEL strange.company/harness="fake" strange.company/cli-version="0.1.0" strange.company/sc-worker-version="x" \
      org.opencontainers.image.source="x" org.opencontainers.image.revision="y"
USER 65532:65532
`
	if bad := checkContainerfile(model, debian); len(bad) != 0 {
		t.Fatalf("the model definition is rejected: %v", bad)
	}
	for name, mutate := range map[string]func(string) string{
		"tag instead of digest": func(s string) string { return strings.Replace(s, "debian@"+debian, "debian:trixie-slim", 1) },
		"other digest":          func(s string) string { return strings.Replace(s, debian, "sha256:"+strings.Repeat("0", 64), 1) },
		"root user":             func(s string) string { return strings.Replace(s, "USER 65532:65532", "USER root", 1) },
		"no label":              func(s string) string { return strings.Replace(s, "strange.company/cli-version=\"0.1.0\"", "x=y", 1) },
		"apt kept": func(s string) string {
			return strings.Replace(s, "rm -f /usr/bin/apt /usr/bin/apt-get /usr/bin/dpkg", "true", 1)
		},
		"worker missing":  func(s string) string { return strings.Replace(s, "/usr/local/bin/sc-worker", "/opt/sc-worker", 1) },
		"harness missing": func(s string) string { return strings.Replace(s, "/usr/local/bin/sc-fake-harness", "/opt/h", 1) },
	} {
		if bad := checkContainerfile(mutate(model), debian); len(bad) == 0 {
			t.Errorf("mutation %q is not caught by checkContainerfile", name)
		}
	}
}

func packageAllowlist(t *testing.T) ([]string, bool) {
	t.Helper()
	text, ok := readRepoFile(t, imageDir+"/allowed-packages.txt")
	if !ok {
		return nil, false
	}
	var pkgs []string
	for _, l := range strings.Split(text, "\n") {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		pkgs = append(pkgs, l)
	}
	return pkgs, true
}

// 28-AC-3 (static part of plan 28 section 13.1, brief D2): the SBOM allowlist.
func TestHarnessFakeImage_AllowedPackages_28AC3(t *testing.T) {
	pkgs, ok := packageAllowlist(t)
	if !ok {
		return
	}
	valid := regexp.MustCompile(`^[a-z0-9][a-z0-9+.-]+$`)
	deny := regexp.MustCompile(denylistedPkgs)
	seen := map[string]bool{}
	for _, p := range pkgs {
		if !valid.MatchString(p) {
			t.Errorf("allowed-packages.txt: %q is not a Debian package name (one name per line)", p)
		}
		if deny.MatchString(p) {
			t.Errorf("allowed-packages.txt lists %q, a container client, kubectl or cloud CLI (28 section 13.1 denylist)", p)
		}
		if seen[p] {
			t.Errorf("allowed-packages.txt lists %q twice", p)
		}
		seen[p] = true
	}
	// the contents row of 28 section 13.1: git, openssh-client, ca-certificates, make, a /bin/sh
	for _, want := range []string{"git", "openssh-client", "ca-certificates", "make"} {
		if !seen[want] {
			t.Errorf("allowed-packages.txt lacks %q (28 section 13.1 contents)", want)
		}
	}
	if !seen["dash"] && !seen["bash"] && !seen["busybox"] {
		t.Errorf("allowed-packages.txt lacks a package providing /bin/sh (dash or bash)")
	}
	for _, banned := range []string{"apt", "apt-utils", "aptitude", "dpkg-dev"} {
		if seen[banned] {
			t.Errorf("allowed-packages.txt lists %q, but no package manager may be usable at run time", banned)
		}
	}
}

type makeTarget struct {
	file   string
	recipe string
}

// findMakeTarget finds `name:` at the start of a line in mk/*.mk and Makefile and returns its recipe text.
func findMakeTarget(t *testing.T, name string) (makeTarget, bool) {
	t.Helper()
	root := repoRoot(t)
	files, _ := filepath.Glob(filepath.Join(root, "mk", "*.mk"))
	files = append(files, filepath.Join(root, "Makefile"))
	head := regexp.MustCompile(`^` + regexp.QuoteMeta(name) + `\s*:([^=]|$)`)
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		lines := strings.Split(string(b), "\n")
		for i, l := range lines {
			if !head.MatchString(l) {
				continue
			}
			var rec []string
			rec = append(rec, l)
			for _, r := range lines[i+1:] {
				if strings.HasPrefix(r, "\t") {
					rec = append(rec, r)
					continue
				}
				break
			}
			rel, _ := filepath.Rel(root, f)
			return makeTarget{file: rel, recipe: strings.Join(rec, "\n")}, true
		}
	}
	return makeTarget{}, false
}

// 28-AC-3 / 27-AC-1 (plan 01 section 8, brief D3 and D4): the two tier-E targets call the container-level script and
// are not part of `make verify`.
func TestMakeTargets_WorkerLayer2_NotInVerify_28AC3(t *testing.T) {
	img, ok := findMakeTarget(t, "harness-image-test")
	if !ok {
		t.Errorf("no mk/*.mk declares the target harness-image-test (make harness-image-test IMAGE=harness-fake, 28-AC-3)")
	} else {
		if !strings.Contains(img.recipe, scriptPath+" image-test") {
			t.Errorf("%s: harness-image-test does not run `%s image-test <image>`:\n%s", img.file, scriptPath, img.recipe)
		}
		if !strings.Contains(img.recipe, "$(IMAGE)") {
			t.Errorf("%s: harness-image-test does not pass $(IMAGE) to the script", img.file)
		}
	}
	comp, ok := findMakeTarget(t, "test-worker-component")
	if !ok {
		t.Errorf("no mk/*.mk declares the target test-worker-component (27-AC-1, plan 01 section 8)")
	} else if !strings.Contains(comp.recipe, scriptPath+" component") {
		t.Errorf("%s: test-worker-component does not run `%s component`:\n%s", comp.file, scriptPath, comp.recipe)
	}
	verify, ok := readRepoFile(t, "mk/verify.mk")
	if !ok {
		return
	}
	m := regexp.MustCompile(`(?m)^VERIFY_TARGETS\s*:?=\s*(.*)$`).FindStringSubmatch(verify)
	if m == nil {
		t.Fatalf("mk/verify.mk has no VERIFY_TARGETS")
	}
	for _, name := range strings.Fields(m[1]) {
		if name == "harness-image-test" || name == "test-worker-component" {
			t.Errorf("VERIFY_TARGETS contains %s, but the container-level suite is tier E (brief D4)", name)
		}
	}
}

// 28-AC-3 / 27-AC-1: the two targets drive the container engine themselves (the script starts the ci image through
// ./bootstrap), so like `make toolbox` they must run on the host and not be re-executed in the ci image, where no engine
// is mounted (Makefile section 8.1). A dry run on a host (SC_IN_CONTAINER unset) must print the script call itself.
func TestMakeTargets_WorkerLayer2_RunOnTheHost_28AC3(t *testing.T) {
	makeBin, err := exec.LookPath("make")
	if err != nil {
		t.Skip("make is not installed")
	}
	root := repoRoot(t)
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"harness-image-test", "IMAGE=harness-fake"}, scriptPath + " image-test harness-fake"},
		{[]string{"test-worker-component"}, scriptPath + " component"},
	} {
		cmd := exec.Command(makeBin, append([]string{"-n"}, c.args...)...)
		cmd.Dir = root
		for _, kv := range os.Environ() {
			if !strings.HasPrefix(kv, "SC_IN_CONTAINER=") {
				cmd.Env = append(cmd.Env, kv)
			}
		}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Errorf("make -n %s: %v\n%s", strings.Join(c.args, " "), err, out)
			continue
		}
		if !strings.Contains(string(out), c.want) {
			t.Errorf("make -n %s on a host does not print %q:\n%s", strings.Join(c.args, " "), c.want, out)
		}
		if strings.Contains(string(out), "bootstrap exec") {
			t.Errorf("make -n %s is re-executed in the ci image (./bootstrap exec), but the script needs the host's container engine:\n%s", strings.Join(c.args, " "), out)
		}
	}
}

// Guards for test infrastructure that already exists in this commit (they pass): the script is the only executable under
// test/e2e/worker, it states its interface, and its tools image uses the lock's debian digest.
func TestWorkerLayer2Script_Guards_28AC3(t *testing.T) {
	root := repoRoot(t)
	out, err := exec.Command("git", "-C", root, "ls-files", "-s", "--", "test/e2e/worker").Output()
	if err != nil {
		t.Skipf("git ls-files: %v", err)
	}
	if len(strings.TrimSpace(string(out))) == 0 {
		t.Skip("test/e2e/worker is not tracked yet (uncommitted tree)")
	}
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.Fields(l)
		if len(f) < 4 {
			continue
		}
		wantMode := "100644"
		if f[3] == scriptPath {
			wantMode = "100755"
		}
		if f[0] != wantMode {
			t.Errorf("%s has git mode %s, want %s (only test/e2e/**/run is executable)", f[3], f[0], wantMode)
		}
	}
	script, ok := readRepoFile(t, scriptPath)
	if !ok {
		return
	}
	for _, want := range []string{"set -euo pipefail", "trap cleanup EXIT", "image-test <image>", "component", "SC_ENGINE"} {
		if !strings.Contains(script, want) {
			t.Errorf("%s lacks %q", scriptPath, want)
		}
	}
	if regexp.MustCompile(`:latest\b`).MatchString(script) {
		t.Errorf("%s uses a latest tag", scriptPath)
	}
	want := lockDigest(t, "debian")
	cf, ok := readRepoFile(t, "test/e2e/worker/tools/Containerfile")
	if !ok {
		return
	}
	if !strings.Contains(cf, "debian@"+want) {
		t.Errorf("test/e2e/worker/tools/Containerfile is not FROM debian@%s", want)
	}
	if !strings.Contains(script, "PRELUDE_DEBIAN_DIGEST="+want) {
		t.Errorf("%s pins a debian digest other than %s", scriptPath, want)
	}
}
