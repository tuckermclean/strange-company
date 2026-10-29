package install

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"strange.company/sc/internal/bootstrap/cli"
	"strange.company/sc/internal/bootstrap/commit"
	"strange.company/sc/internal/bootstrap/engine"
	"strange.company/sc/internal/bootstrap/identity"
	"strange.company/sc/internal/bootstrap/store"
	"strange.company/sc/internal/s3client"
	"strange.company/sc/internal/tofustate"
	"strange.company/sc/internal/version"
)

var sprintf = fmt.Sprintf

const (
	stepIdentity     = "plan.install-identity"
	stepConfirm      = "plan.storage-confirm"
	stepApply        = "plan.storage-apply"
	stepUpload       = "plan.bundle-upload"
	stepCredentials  = "plan.storage-credentials"
	stepConformance  = "plan.state-conformance"
	stepMigrate      = "plan.state-migrate"
	stepBundleVerify = "plan.bundle-confirm"
)

// pipeline builds the install pipeline: preflight, the plan phase, provision and, in test mode, the stubbed phases of
// plan 50 sections 14.5 and 14.6. SC_TEST_STOP_AFTER (test mode only) ends it after the named phase or plan step.
func (s *sess) pipeline() engine.Pipeline {
	pre := &phase{unit: unit{name: "preflight", timeout: 10 * time.Minute, inputs: map[string]string{"step": "preflight"}, run: s.runPreflight}}
	steps := []engine.Unit{
		s.stepIdentity(), s.stepStorageConfirm(), s.stepStorageApply(), s.stepBundleUpload(), s.stepStorageCredentials(),
		s.stepStateConformance(), s.stepStateMigrate(), s.stepBundleConfirm(), s.stepComputePlan(), s.stepPlanConfirm(),
	}
	stop := ""
	if s.env["SC_TEST_MODE"] == "1" {
		stop = s.env["SC_TEST_STOP_AFTER"]
	}
	if stop == "preflight" {
		return engine.Pipeline{Kind: "install", Phases: []engine.Phase{pre}}
	}
	stopAtStep := false
	for i, st := range steps {
		if st.Name() == stop {
			steps, stopAtStep = steps[:i+1], true
			break
		}
	}
	pl := &phase{unit: unit{name: "plan", timeout: 6 * time.Hour,
		inputs: map[string]any{"cluster": s.cluster, "prefix": s.oc.prefix, "bucketMode": s.oc.bucketMode}}, steps: steps}
	phases := []engine.Phase{pre, pl}
	if stopAtStep {
		// SC_TEST_STOP_AFTER named a plan step: the phases after plan are not part of this run.
		return engine.Pipeline{Kind: "install", Phases: phases}
	}
	phases = append(phases, s.stepProvision())
	if s.env["SC_TEST_MODE"] == "1" {
		for _, n := range stubPhases {
			phases = append(phases, s.stubPhase(n))
		}
	}
	for i, ph := range phases {
		if ph.Name() == stop {
			phases = phases[:i+1]
			break
		}
	}
	return engine.Pipeline{Kind: "install", Phases: phases}
}

func (s *sess) runPreflight(context.Context) ([]engine.OutputRef, error) {
	deps := s.deps.CLI
	// A confirmation of the same subject is recorded once: preflight runs on every invocation (plan 50 section 17.1 rule 3).
	deps.Record = func(c cli.Confirmation) {
		if !s.eng.Confirmed(c.Kind, c.SubjectDigest) {
			s.eng.RecordConfirmation(c)
		}
	}
	if code := cli.RunPreflight(s.inv, s.env, s.out, s.errw, deps); code != 0 {
		return nil, fail("PREFLIGHT_FAILED", code, procPreflight, "preflight failed (exit %d); the rows above name each failing check", code)
	}
	return nil, nil
}

// --- plan.install-identity

func (s *sess) register() (*identity.Register, error) {
	if s.reg != nil {
		return s.reg, nil
	}
	b, err := os.ReadFile(filepath.Join(s.repo, "contracts", "secret-classes.yaml"))
	if err != nil {
		return nil, fail("REGISTER_MISSING", 1, procResume, "contracts/secret-classes.yaml cannot be read: %v", err)
	}
	reg, err := identity.LoadRegister(b)
	if err != nil {
		return nil, err
	}
	s.reg = reg
	return reg, nil
}

func (s *sess) cdir() string { return filepath.Join(s.blackstart, s.cluster) }

// operatorSecrets maps the operator-supplied classes that in-cluster components need to their bootstrap.env variable.
var operatorSecrets = []struct{ class, variable string }{
	{"SC-DNS-ACME", "CLOUDFLARE_ACME_TOKEN"},
	{"SC-DNS-API", "CLOUDFLARE_API_TOKEN"},
	{"SC-GITHUB-MIRROR", "GITHUB_MIRROR_TOKEN"},
	{"SC-GITHUB-MIRROR-READ", "GITHUB_MIRROR_READ_TOKEN"},
	{"SC-GITHUB-PROBE", "GITHUB_PROBE_TOKEN"},
}

func (s *sess) addOperatorSecrets(set *identity.Set) {
	for _, o := range operatorSecrets {
		if v := s.vars[o.variable]; v != "" {
			if err := set.AddExternal(o.class, "", "", []byte(v)); err != nil {
				s.eng.Log("warn", "operator-secret-skipped", o.class+": "+err.Error(), nil)
			}
		}
	}
}

func (s *sess) stepIdentity() engine.Unit {
	return &unit{name: stepIdentity, timeout: 30 * time.Minute, inputs: map[string]any{"cluster": s.cluster},
		check: func(ctx context.Context) (engine.Check, error) {
			uid := s.eng.InstallUID()
			if uid == "" {
				return notDone("the journal has no install_uid"), nil
			}
			rb := s.eng.RecoveryBundle()
			if rb.LocalPath == "" || rb.Sha256 == "" {
				return notDone("no recovery bundle is recorded"), nil
			}
			data, err := os.ReadFile(rb.LocalPath)
			if err != nil || sha256Digest(data) != rb.Sha256 {
				return notDone("the local recovery bundle is missing or differs from the journal"), nil
			}
			_, found, err := commit.FindInstall(ctx, s.repo, uid)
			if err != nil {
				return engine.Check{}, err
			}
			if !found {
				return notDone("no commit carries SC-Install-Uid: " + uid), nil
			}
			s.reportInstallDrift()
			return done("the bundle opens by digest and HEAD holds the installation commit"), nil
		},
		run: s.runIdentity}
}

func (s *sess) writeBundleFiles(set *identity.Set, version int, sealed *identity.Sealed) (string, error) {
	path, err := identity.WriteBundle(s.cdir(), s.cluster, version, sealed.Data)
	if err != nil {
		return "", err
	}
	latest, _ := json.Marshal(map[string]any{"generation": version, "file": filepath.Base(path), "sha256": "sha256:" + sealed.SHA256})
	if err := writeAtomic(filepath.Join(s.cdir(), "bundles", "LATEST.json"), append(latest, '\n')); err != nil {
		return "", err
	}
	if _, err := os.Stat(filepath.Join(s.cdir(), "README.txt")); os.IsNotExist(err) {
		_ = writeAtomic(filepath.Join(s.cdir(), "README.txt"), []byte(readmeText))
	}
	return path, nil
}

const readmeText = `This directory belongs to one Strange Company installation.

bundles/sc-recovery-<cluster>-v<N>.tar.age   passphrase-encrypted recovery bundle (keep a copy offline)
bundles/LATEST.json                          pointer to the newest bundle
recovery-identity.txt                        the SC-AGE-RECOVERY identity; keep it offline, then remove it from this host
kubeconfig                                   cluster-admin access (after provision)

The recovery passphrase is not stored anywhere on this host.
`

func writeAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (s *sess) seal(set *identity.Set, version int, previous string) (*identity.Sealed, error) {
	return s.sealAs(set, version, previous, "initial-bootstrap")
}

func (s *sess) sealAs(set *identity.Set, version int, previous, trigger string) (*identity.Sealed, error) {
	tb := s.toolboxDigest()
	if tb == "" {
		tb = "sha256:" + strings.Repeat("0", 64)
	}
	return identity.Seal(identity.SealInput{
		Cluster: s.cluster, Version: version, Passphrase: s.pass, Set: set, Clock: s.clk, ToolVersion: versionString(),
		ToolboxImage: tb, SourceCommit: s.gitHead(context.Background()), PreviousSHA256: previous, Trigger: trigger,
		WorkFactor: s.deps.WorkFactor,
	})
}

func versionString() string { return version.Version }

func (s *sess) runIdentity(ctx context.Context) ([]engine.OutputRef, error) {
	reg, err := s.register()
	if err != nil {
		return nil, err
	}
	var set *identity.Set
	adopt, created := false, false
	if s.eng.InstallUID() == "" {
		if matches, _ := filepath.Glob(filepath.Join(s.cdir(), "bundles", "sc-recovery-"+s.cluster+"-v*.tar.age")); len(matches) > 0 {
			// The journal is missing but a bundle exists: the bundle is the source of truth (plan 50 section 14.3).
			if err := s.adoptBundle(ctx, reg, matches); err != nil {
				return nil, err
			}
			adopt = true
			// SC-AGE-RECOVERY is never in the bundle, so the adopt path cannot restore the file; it only warns.
			if _, serr := os.Stat(filepath.Join(s.cdir(), "recovery-identity.txt")); os.IsNotExist(serr) {
				s.errorf("warning: recovery-identity.txt is missing from %s; restore it from the offline copy of SC-AGE-RECOVERY (procedures/rotate-age-key.md if it is lost)\n", s.cdir())
			}
		}
	}
	if s.eng.InstallUID() == "" && !adopt {
		pass, err := s.newPassphrase(ctx)
		if err != nil {
			return nil, err
		}
		s.pass = pass
		set, err = identity.Generate(reg, s.cluster, identity.WithClock(s.clk))
		if err != nil {
			return nil, err
		}
		s.addOperatorSecrets(set)
		s.set = set
		identity.RegisterSecrets(s.red, set, pass)
		first, previous, ferr := s.firstGenerationFor(ctx)
		if ferr != nil {
			return nil, ferr
		}
		sealed, err := s.seal(set, first, previous)
		if err != nil {
			return nil, err
		}
		path, err := s.writeBundleFiles(set, first, sealed)
		if err != nil {
			return nil, err
		}
		if _, err := identity.WriteRecoveryIdentity(s.cdir(), set); err != nil {
			return nil, err
		}
		// the uid is recorded only together with its bundle: a failure above leaves no identity without a bundle
		s.eng.SetInstallUID(set.InstallUID)
		s.eng.SetRecoveryBundle(engine.RecoveryBundleState{Status: "created", Generation: first, Sha256: "sha256:" + sealed.SHA256,
			LocalPath: path, CreatedAt: s.clk.Now().UTC().Format(time.RFC3339)})
		if err := s.eng.Checkpoint(ctx); err != nil {
			return nil, err
		}
		created = true
	} else {
		// The bundle exists (journal and file) but HEAD holds no installation commit: the bundle is the source of
		// truth, so the commit is rendered from its contents (plan 50 section 14.3, resume).
		if err := s.requireSet(ctx); err != nil {
			return nil, err
		}
		set = s.set
		if err := set.CompleteGenerated(s.cluster, s.clk); err != nil {
			return nil, err
		}
		s.addOperatorSecrets(set)
		identity.RegisterSecrets(s.red, set, s.pass)
	}
	sha, found, err := commit.FindInstall(ctx, s.repo, s.eng.InstallUID())
	if err != nil {
		return nil, err
	}
	if !found {
		if sha, err = s.renderAndCommitInstall(ctx, set); err != nil {
			return nil, err
		}
	}
	return []engine.OutputRef{{Kind: "git-commit", Ref: sha}, s.identityFileRef(created, adopt)}, nil
}

// identityFileRef is the file output reference of plan.install-identity: the identity's first bundle, from which
// identityFirst derives the generation plan.storage-credentials compares against (BOOT-19: re-sealed at most once). A
// rerun of the step keeps the reference it recorded (the journal's bundle may already be the re-sealed one); an
// adopted bundle (the journal was lost) names the oldest local bundle of the identity.
func (s *sess) identityFileRef(created, adopt bool) engine.OutputRef {
	rb := s.eng.RecoveryBundle()
	ref := engine.OutputRef{Kind: "file", Ref: rb.LocalPath, Sha256: rb.Sha256}
	switch {
	case created:
	case adopt:
		prefix := "sc-recovery-" + s.cluster + "-v"
		matches, _ := filepath.Glob(filepath.Join(s.cdir(), "bundles", prefix+"*.tar.age"))
		best, bestV := "", 0
		for _, m := range matches {
			v, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(filepath.Base(m), prefix), ".tar.age"))
			if err == nil && v > 0 && (bestV == 0 || v < bestV) {
				best, bestV = m, v
			}
		}
		if best != "" && best != rb.LocalPath {
			if data, err := os.ReadFile(best); err == nil {
				ref = engine.OutputRef{Kind: "file", Ref: best, Sha256: sha256Digest(data)}
			}
		}
	default:
		for _, r := range s.eng.StepOutputRefs(stepIdentity) {
			if r.Kind != "file" {
				continue
			}
			if m := bundleFileRe.FindStringSubmatch(r.Ref); m != nil {
				if v, err := strconv.Atoi(m[1]); err == nil && v > 0 && v <= rb.Generation {
					return r
				}
			}
		}
		// No stored reference (the step's check path recorded it): the identity's first bundle, never the latest.
		if first := s.identityFirst(); first > 0 && first <= rb.Generation {
			p := filepath.Join(s.cdir(), "bundles", "sc-recovery-"+s.cluster+"-v"+strconv.Itoa(first)+".tar.age")
			if data, err := os.ReadFile(p); err == nil {
				return engine.OutputRef{Kind: "file", Ref: p, Sha256: sha256Digest(data)}
			}
		}
	}
	return ref
}

// deriveInstallUID derives the install UUID from the cluster age identity. It is only the fallback for a bundle whose
// manifest carries no install_uid; Seal writes the uid into the manifest.
func deriveInstallUID(set *identity.Set) string {
	sec := set.Find("SC-AGE-CLUSTER", "")
	h := sha256.Sum256(append([]byte("sc-install-uid\x00"), sec.Value...))
	b := h[:16]
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// adoptBundle opens the newest local bundle and records its identity in the journal.
func (s *sess) adoptBundle(ctx context.Context, reg *identity.Register, matches []string) error {
	best, bestV := "", -1
	prefix := "sc-recovery-" + s.cluster + "-v"
	for _, m := range matches {
		v, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(filepath.Base(m), prefix), ".tar.age"))
		if err == nil && v > bestV {
			best, bestV = m, v
		}
	}
	if best == "" {
		return fail("BUNDLE_MISSING", 8, procResume, "no recovery bundle in %s has a readable version", s.cdir())
	}
	data, err := os.ReadFile(best)
	if err != nil {
		return fail("BUNDLE_MISSING", 8, procResume, "the local recovery bundle cannot be read: %v", err)
	}
	pass, err := s.existingPassphrase(ctx)
	if err != nil {
		return err
	}
	o, err := identity.Open(data, pass)
	if err != nil {
		return err
	}
	set, err := identity.SetFromBundle(reg, "", o)
	if err != nil {
		return err
	}
	if set.Find("SC-AGE-CLUSTER", "") == nil {
		return fail("BUNDLE_CORRUPT", 8, procResume, "the recovery bundle lacks the SC-AGE-CLUSTER identity")
	}
	if set.InstallUID == "" {
		// A bundle written before the manifest carried install_uid: the key-derived uid is the fallback.
		set.InstallUID = deriveInstallUID(set)
	}
	identity.RegisterSecrets(s.red, set, pass)
	s.set = set
	s.eng.SetInstallUID(set.InstallUID)
	s.eng.SetRecoveryBundle(engine.RecoveryBundleState{Status: "created", Generation: bestV, Sha256: sha256Digest(data),
		LocalPath: best, CreatedAt: s.clk.Now().UTC().Format(time.RFC3339)})
	return s.eng.Checkpoint(ctx)
}

// requireSet makes sure s.set holds the secrets: from memory, else by opening the local bundle.
func (s *sess) requireSet(ctx context.Context) error {
	if s.set != nil {
		return nil
	}
	reg, err := s.register()
	if err != nil {
		return err
	}
	rb := s.eng.RecoveryBundle()
	if rb.LocalPath == "" {
		return fail("BUNDLE_MISSING", 8, procResume, "the journal records no local recovery bundle to open")
	}
	data, err := os.ReadFile(rb.LocalPath)
	if err != nil {
		return fail("BUNDLE_MISSING", 8, procResume, "the local recovery bundle cannot be read: %v", err)
	}
	pass, err := s.existingPassphrase(ctx)
	if err != nil {
		return err
	}
	o, err := identity.Open(data, pass)
	if err != nil {
		return err
	}
	set, err := identity.SetFromBundle(reg, s.eng.InstallUID(), o)
	if err != nil {
		return err
	}
	identity.RegisterSecrets(s.red, set, pass)
	s.set = set
	return nil
}

func (s *sess) writeRepoFile(rel string, data []byte) error {
	p := filepath.Join(s.repo, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, data, 0o644)
}

func (s *sess) recipients(set *identity.Set) identity.Recipients {
	return identity.Recipients{Cluster: set.ClusterRecipient, Recovery: set.RecoveryRecipient}
}

// renderAndCommitInstall renders gitops/clusters/<cluster>/ and the off-cluster SOPS files and commits them.
func (s *sess) renderAndCommitInstall(ctx context.Context, set *identity.Set) (string, error) {
	uid := s.eng.InstallUID()
	base := "gitops/clusters/" + s.cluster + "/"
	res, err := s.bootstrapConfigYAML(uid)
	if err != nil {
		return "", err
	}
	if err := s.writeRepoFile(base+"bootstrap-config.yaml", res); err != nil {
		return "", err
	}
	vars, err := identity.RenderClusterVars(s.clusterVarsInput())
	if err != nil {
		return "", err
	}
	if err := s.writeRepoFile(base+"vars/cluster-vars.yaml", vars); err != nil {
		return "", err
	}
	window := "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: sc-cnpg-create-window\n  namespace: flux-system\ndata:\n  mode: initial-install\n"
	if err := s.writeRepoFile(base+"vars/sc-cnpg-create-window.yaml", []byte(window)); err != nil {
		return "", err
	}
	rcp := s.recipients(set)
	existing, _ := os.ReadFile(filepath.Join(s.repo, ".sops.yaml"))
	rules, err := identity.SOPSRules(s.cluster, rcp, existing)
	if err != nil {
		return "", err
	}
	if err := s.writeRepoFile(".sops.yaml", rules); err != nil {
		return "", err
	}
	if err := s.renderSOPS(set, rcp); err != nil {
		return "", err
	}
	sha, err := commit.Create(ctx, commit.Request{
		Repo: s.repo, Cluster: s.cluster, Kind: commit.Install, Subject: "bootstrap(" + s.cluster + "): installation config",
		BaseDomain: str(dig(s.doc, "spec", "cluster", "baseDomain")), InstallUID: uid, RunID: s.eng.RunID(), Clock: s.clk,
		Paths: []string{"gitops/clusters/" + s.cluster, "secrets/offcluster/" + s.cluster, ".sops.yaml"},
	})
	if err != nil {
		return "", err
	}
	return sha, nil
}

func (s *sess) clusterVarsInput() identity.ClusterVarsInput {
	return identity.ClusterVarsInput{
		ServiceCIDR: str(dig(s.doc, "spec", "network", "serviceCidr")), PodCIDR: str(dig(s.doc, "spec", "network", "podCidr")),
		S3Endpoint: s.oc.endpoint, S3PrivateCIDR: str(dig(s.doc, "spec", "objectStorage", "privateCidr")),
		Resolve: s.resolve, DNSUpstreams: []string{s.dnsUpstream()}, PinnedIPs: s.clusterDNSPins(str(dig(s.doc, "spec", "network", "serviceCidr"))),
	}
}

// provisionOwned reports the cluster-vars keys provision owns; install-identity neither renders nor compares them
// once it has run (plan 50 section 14.3, ownership BOOT-9).
func provisionOwned(key string) bool {
	return strings.HasPrefix(key, "SC_NODE_IP_") || strings.HasPrefix(key, "SC_DNS_UPSTREAM_")
}

// reportInstallDrift compares the cluster-vars keys install-identity owns with a fresh render and logs a difference.
// The keys provision owns (SC_NODE_IP_*, SC_DNS_UPSTREAM_*) are not compared, and nothing is re-rendered: from state
// planned on the drift is reported, never repaired. A render that cannot be made is not a difference.
func (s *sess) reportInstallDrift() {
	path := filepath.Join(s.repo, "gitops", "clusters", s.cluster, "vars", "cluster-vars.yaml")
	cur, err := os.ReadFile(path)
	if err != nil {
		return
	}
	fresh, err := identity.RenderClusterVars(s.clusterVarsInput())
	if err != nil {
		return
	}
	have, err1 := identity.ClusterVarsData(cur)
	want, err2 := identity.ClusterVarsData(fresh)
	if err1 != nil || err2 != nil {
		return
	}
	var diff []string
	for k, v := range want {
		if !provisionOwned(k) && have[k] != v {
			diff = append(diff, k)
		}
	}
	for k := range have {
		if _, ok := want[k]; !ok && !provisionOwned(k) {
			diff = append(diff, k)
		}
	}
	if len(diff) > 0 {
		sort.Strings(diff)
		s.eng.Log("warn", "install-identity-drift", "cluster-vars.yaml differs from a fresh render in "+strings.Join(diff, ", "), nil)
	}
}

// renderSOPS writes the SOPS files of a set and logs every secret the render deferred.
func (s *sess) renderSOPS(set *identity.Set, rcp identity.Recipients) error {
	rep, err := identity.RenderSOPSReport(s.cluster, set, rcp, s.deps.Encrypt)
	if err != nil {
		return err
	}
	for _, f := range rep.Files {
		if err := s.writeRepoFile(f.Path, f.Data); err != nil {
			return err
		}
	}
	for _, d := range rep.Deferred {
		s.eng.Log("warn", "sops-deferred", fmt.Sprintf("%s/%s not rendered at %s: %s", d.Class, d.Instance, d.Path, d.Reason), nil)
	}
	return nil
}

// --- plan.storage-confirm

// footprint is the storage footprint text and its digest (plan 50 section 14.3 step 2).
func (s *sess) footprint() (string, string) {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Storage footprint for cluster %s (bucket mode %s, credential mode %s)\n", s.cluster, s.oc.bucketMode, s.oc.credMode)
	fmt.Fprintf(&sb, "  endpoint %s, region %s\n", s.oc.endpoint, s.oc.region)
	lockMode := "GOVERNANCE"
	if s.oc.credMode == "shared" {
		lockMode = "COMPLIANCE"
	}
	maxDays := 0
	for _, p := range bucketPurposes {
		line := fmt.Sprintf("  %s  versioning Enabled", s.bucketName(p))
		if d := lockDays[p]; d > 0 {
			line += fmt.Sprintf(", Object Lock %s %d days when the store supports it", lockMode, d)
			maxDays = max(maxDays, d)
		}
		if s.hasStateAccount() && stateAccountPurposes[p] {
			line += ", in the state account"
		}
		sb.WriteString(line + "\n")
	}
	switch s.oc.credMode {
	case "shared":
		sb.WriteString("  principals and keys: none created; the one account key serves every instance\n")
	default:
		sb.WriteString("  principals and keys: one scoped key per credential instance (S3_CRED_<INSTANCE>_* in existing mode)\n")
	}
	fmt.Fprintf(&sb, "usage-billed; cost bounded by retention; Object-Locked objects cannot be deleted for up to %d days\n", maxDays)
	text := sb.String()
	return text, sha256Digest([]byte(text))
}

func (s *sess) stepStorageConfirm() engine.Unit {
	_, digest := s.footprint()
	return &unit{name: stepConfirm, timeout: 30 * time.Minute, inputs: map[string]any{"footprint": digest},
		check: func(context.Context) (engine.Check, error) {
			if s.oc.bucketMode == "existing" {
				return done("existing buckets: only the verification list is printed"), nil
			}
			if s.eng.Confirmed("storage-footprint", digest) {
				return done("the storage footprint is confirmed"), nil
			}
			return notDone("the storage footprint is not confirmed"), nil
		},
		run: func(ctx context.Context) ([]engine.OutputRef, error) {
			text, digest := s.footprint()
			s.printf("%s", text)
			if s.oc.bucketMode == "existing" {
				return nil, nil
			}
			given := s.inv.Confirm["storage"]
			if given == "" {
				given = s.env["SC_CONFIRM_STORAGE"]
			}
			method := "flag"
			if given == "" {
				if s.inv.NonInteractive {
					return nil, fail("STORAGE_CONFIRMATION_MISSING", 5, procResume,
						"confirm the storage footprint with --confirm-storage %s or SC_CONFIRM_STORAGE=%s", s.cluster, s.cluster)
				}
				line, err := s.readLine(ctx, fmt.Sprintf("Type 'storage %s' to create these buckets: ", s.cluster), false)
				if err != nil {
					return nil, err
				}
				if strings.TrimSpace(line) != "storage "+s.cluster {
					return nil, fail("STORAGE_CONFIRMATION_MISMATCH", 5, procResume, "the typed confirmation does not match 'storage %s'", s.cluster)
				}
				method = "interactive"
			} else if given != s.cluster {
				return nil, fail("STORAGE_CONFIRMATION_MISMATCH", 5, procResume,
					"the confirmed cluster %q is not %q; the storage footprint stays unconfirmed", given, s.cluster)
			}
			s.eng.RecordConfirmation(cli.Confirmation{Kind: "storage-footprint", SubjectDigest: digest, Method: method})
			return nil, nil
		}}
}

// --- plan.storage-apply

func (s *sess) storageSpec(pass string) StorageSpec {
	sp := StorageSpec{
		WorkDir: filepath.Join(s.boot, "work", "storage"), LocalState: filepath.Join(s.boot, "work", "storage.local.tfstate"),
		Prefix: s.oc.prefix, Cluster: s.cluster, Buckets: s.bucketSpecs(), Main: s.oc.main.tofu(), Passphrase: pass, Generation: s.tofuGen, Previous: s.tofuPrev,
	}
	if s.oc.state != nil {
		t := s.oc.state.tofu()
		sp.State = &t
	}
	return sp
}

// tofuEncryption is the OpenTofu state encryption of the install, read from the recovery bundle (plan 06 sections 7.3
// and 20.3): the SC-TOFU-ENCRYPTION entry of its generation, plus the previous passphrase as the fallback while a
// rotated bundle still lists one. A bundle that never rotated holds the single instance "" (generation 1).
func (s *sess) tofuEncryption(ctx context.Context) (tofustate.Encryption, error) {
	if err := s.requireSet(ctx); err != nil {
		return tofustate.Encryption{}, err
	}
	return tofuEncryptionOf(s.set)
}

func tofuEncryptionOf(set *identity.Set) (tofustate.Encryption, error) {
	gen := func(x *identity.Secret) int {
		if x.Generation < 1 {
			return 1
		}
		return x.Generation
	}
	if cur := set.Find("SC-TOFU-ENCRYPTION", "current"); cur != nil {
		e := tofustate.Encryption{Current: string(cur.Value), Generation: gen(cur)}
		if prev := set.Find("SC-TOFU-ENCRYPTION", "previous"); prev != nil && gen(prev) == e.Generation-1 {
			e.Previous = string(prev.Value)
		}
		return e, nil
	}
	sec := set.Find("SC-TOFU-ENCRYPTION", "")
	if sec == nil || len(sec.Value) < 16 {
		return tofustate.Encryption{}, fail("TOFU_PASSPHRASE_MISSING", 1, procState, "the install has no SC-TOFU-ENCRYPTION secret")
	}
	return tofustate.Encryption{Current: string(sec.Value), Generation: gen(sec)}, nil
}

// tofuPassphrase is the current SC-TOFU-ENCRYPTION passphrase of the install.
func (s *sess) tofuPassphrase(ctx context.Context) (string, error) {
	e, err := s.tofuEncryption(ctx)
	if err != nil {
		return "", err
	}
	s.tofuGen, s.tofuPrev = e.Generation, e.Previous
	return e.Current, nil
}

// verifyBuckets checks that every bucket exists and has versioning Enabled; it returns the first problem.
func (s *sess) verifyBuckets(ctx context.Context) error {
	for _, p := range bucketPurposes {
		name := s.bucketName(p)
		acct, err := s.account(s.credFor(p))
		if err != nil {
			return err
		}
		if err := acct.HeadBucket(ctx, name); err != nil {
			var se *s3client.Error
			if store.Is(err, store.NotFound) || (errors.As(err, &se) && (se.Status == 403 || se.Status == 404)) {
				return fail("BUCKET_MISSING", 3, procStorage, "the bucket %s does not exist or its credential cannot reach it: %v", name, err)
			}
			return err
		}
		on, err := acct.Versioning(ctx, name)
		if err != nil {
			return err
		}
		if !on {
			return fail("BUCKET_VERSIONING_DISABLED", 3, procStorage, "the bucket %s has versioning disabled; every plan 14 bucket needs it Enabled", name)
		}
	}
	return nil
}

func (s *sess) stepStorageApply() engine.Unit {
	return &unit{name: stepApply, timeout: 60 * time.Minute, inputs: map[string]any{"buckets": s.bucketSpecs(), "mode": s.oc.bucketMode},
		check: func(ctx context.Context) (engine.Check, error) {
			if err := s.verifyBuckets(ctx); err != nil {
				var ee *engine.Error
				if errors.As(err, &ee) {
					return notDone(ee.Code), nil
				}
				return engine.Check{}, err
			}
			if err := s.attach(ctx); err != nil {
				return engine.Check{}, err
			}
			return done("every bucket exists with versioning Enabled"), nil
		},
		run: func(ctx context.Context) ([]engine.OutputRef, error) {
			if s.oc.bucketMode != "existing" {
				pass, err := s.tofuPassphrase(ctx)
				if err != nil {
					return nil, err
				}
				if err := os.MkdirAll(filepath.Join(s.boot, "work"), 0o700); err != nil {
					return nil, err
				}
				if err := s.deps.Tofu.ApplyStorage(ctx, s.storageSpec(pass)); err != nil {
					return nil, fail("STORAGE_APPLY_FAILED", 1, procStorage, "the storage module failed: %v", err)
				}
			}
			if err := s.verifyBuckets(ctx); err != nil {
				return nil, err
			}
			if err := s.attach(ctx); err != nil {
				if store.Is(err, store.Exists) {
					return nil, fail("RUN_LOCK_HELD", 6, procResume, "the remote run lock object already exists")
				}
				return nil, err
			}
			return nil, nil
		}}
}

// --- bundle upload (plan.bundle-upload and the re-upload of plan.storage-credentials)

func bundleKey(cluster string, gen int) string {
	return fmt.Sprintf("%s/bundles/sc-recovery-%s-v%d.tar.age", cluster, cluster, gen)
}

// uploadBundle uploads the local bundle of the journal's generation to the recovery bucket and records it.
func (s *sess) uploadBundle(ctx context.Context) (engine.RecoveryBundleState, error) {
	rb := s.eng.RecoveryBundle()
	data, err := os.ReadFile(rb.LocalPath)
	if err != nil {
		return rb, fail("BUNDLE_MISSING", 8, procResume, "the local recovery bundle cannot be read: %v", err)
	}
	if sha256Digest(data) != rb.Sha256 {
		return rb, fail("BUNDLE_SHA_MISMATCH", 8, procResume, "the local recovery bundle differs from the digest in the journal")
	}
	bucket := s.bucketName("recovery")
	acct, err := s.account(s.credFor("recovery"))
	if err != nil {
		return rb, err
	}
	b := acct.Bucket(bucket)
	key := bundleKey(s.cluster, rb.Generation)
	res, err := b.PutObject(ctx, key, data, s3client.PutOptions{IfNoneMatch: true})
	if err != nil {
		if !store.Is(err, store.Exists) {
			return rb, err
		}
		obj, gerr := b.Get(ctx, key)
		if gerr != nil || sha256Digest(obj.Data) != rb.Sha256 {
			return rb, fail("BUNDLE_VERSION_EXISTS", 8, procResume, "s3://%s/%s exists with different content; bundle versions are immutable", bucket, key)
		}
		info, herr := b.Head(ctx, key)
		if herr != nil {
			return rb, herr
		}
		res.VersionID = info.VersionID
	}
	rb.Status, rb.S3URI, rb.S3VersionID = "uploaded", "s3://"+bucket+"/"+key, res.VersionID
	s.eng.SetRecoveryBundle(rb)
	return rb, nil
}

func (s *sess) bundleUploaded(ctx context.Context) (bool, error) {
	rb := s.eng.RecoveryBundle()
	if (rb.Status != "uploaded" && rb.Status != "confirmed") || rb.S3URI == "" {
		return false, nil
	}
	acct, err := s.account(s.credFor("recovery"))
	if err != nil {
		return false, err
	}
	obj, err := acct.Bucket(s.bucketName("recovery")).Get(ctx, bundleKey(s.cluster, rb.Generation))
	if err != nil {
		if store.Is(err, store.NotFound) {
			return false, nil
		}
		return false, err
	}
	return sha256Digest(obj.Data) == rb.Sha256, nil
}

func (s *sess) stepBundleUpload() engine.Unit {
	return &unit{name: stepUpload, timeout: 20 * time.Minute, inputs: map[string]any{"step": stepUpload},
		check: func(ctx context.Context) (engine.Check, error) {
			ok, err := s.bundleUploaded(ctx)
			if err != nil {
				return engine.Check{}, err
			}
			if ok {
				return done("the S3 copy of the bundle matches the journal"), nil
			}
			return notDone("the bundle is not in S3"), nil
		},
		run: func(ctx context.Context) ([]engine.OutputRef, error) {
			rb, err := s.uploadBundle(ctx)
			if err != nil {
				return nil, err
			}
			return []engine.OutputRef{{Kind: "s3-object", Ref: rb.S3URI + "?versionId=" + rb.S3VersionID, Sha256: rb.Sha256}}, nil
		}}
}

// --- plan.storage-credentials

// instanceCreds resolves one credential per SC-OBJECT-STORAGE instance of the register (plan 14 section 14.1 step 6).
func (s *sess) instanceCreds() (map[string]cred, error) {
	reg, err := s.register()
	if err != nil {
		return nil, err
	}
	out := map[string]cred{}
	var missing []string
	for _, name := range reg.Instances("SC-OBJECT-STORAGE") {
		switch {
		case name == "admin":
			out[name] = s.oc.main
		case s.oc.credMode == "shared":
			if name == "tofu-state" || name == "recovery" {
				out[name] = s.stateCred()
			} else {
				out[name] = s.oc.main
			}
		default:
			c, ok := s.oc.instances[name]
			if !ok {
				missing = append(missing, name)
				continue
			}
			out[name] = c
		}
	}
	if len(missing) > 0 {
		return nil, fail("OBJSTORE_CREDENTIALS_MISSING", 3, procPreflight,
			"credential mode %s needs a key per instance: set S3_CRED_<INSTANCE>_ACCESS_KEY_ID and _SECRET_ACCESS_KEY for %s (minting keys through a provider adapter is not built)",
			s.oc.credMode, strings.Join(missing, ", "))
	}
	return out, nil
}

func credEnv(c cred) []byte {
	return []byte(fmt.Sprintf("ACCESS_KEY_ID=%s\nACCESS_SECRET_KEY=%s\nREGION=%s\nENDPOINT=%s\n", c.AccessKey, c.SecretKey, c.Region, c.Endpoint))
}

// unprotected reports the state protection rule (BOOT-3, plan 14 14-R-2a): shared mode without a second account.
func (s *sess) unprotected() bool { return s.oc.credMode == "shared" && !s.hasStateAccount() }

func (s *sess) stepStorageCredentials() engine.Unit {
	return &unit{name: stepCredentials, timeout: 60 * time.Minute,
		inputs: map[string]any{"credentialMode": s.oc.credMode, "stateAccount": s.hasStateAccount(), "allowUnprotected": s.oc.allowUnprotected},
		check: func(ctx context.Context) (engine.Check, error) {
			if s.unprotected() && !s.oc.allowUnprotected {
				return notDone("shared credential mode needs a state account"), nil
			}
			rb := s.eng.RecoveryBundle()
			if first := s.identityFirst(); rb.Generation <= first || (rb.Status != "uploaded" && rb.Status != "confirmed") {
				return notDone("the bundle has not been re-sealed with the credentials"), nil
			}
			if !s.commitForInstall(ctx, "bootstrap("+s.cluster+"): object-storage credentials") {
				return notDone("the object-storage credentials commit is missing"), nil
			}
			if err := s.authenticateAll(ctx); err != nil {
				return notDone("an instance credential does not authenticate"), nil
			}
			ok, err := s.bundleUploaded(ctx)
			if err != nil || !ok {
				return notDone("the re-sealed bundle is not in S3"), nil
			}
			return done("every instance authenticates and bundle generation 2 is uploaded"), nil
		},
		run: s.runCredentials}
}

// authenticateAll checks every distinct credential with a bucket listing.
func (s *sess) authenticateAll(ctx context.Context) error {
	creds := []cred{s.oc.main}
	if s.oc.state != nil {
		creds = append(creds, *s.oc.state)
	}
	for _, c := range s.oc.instances {
		creds = append(creds, c)
	}
	for _, c := range creds {
		acct, err := s.account(c)
		if err != nil {
			return err
		}
		if _, err := acct.ListBuckets(ctx); err != nil {
			return fail("OBJSTORE_CREDENTIAL_INVALID", 3, procStorage, "a credential for %s does not authenticate: %v", c.Endpoint, err)
		}
	}
	return nil
}

// commitForInstall reports whether a commit with the subject carries this installation's SC-Install-Uid trailer.
func (s *sess) commitForInstall(ctx context.Context, subject string) bool {
	uid := s.eng.InstallUID()
	if uid == "" {
		return false
	}
	out, err := s.git(ctx, "log", "--format=%s%x1f%(trailers:key=SC-Install-Uid,valueonly,separator=%x1f)%x1e")
	if err != nil {
		return false
	}
	for _, rec := range strings.Split(out, "\x1e") {
		f := strings.Split(strings.TrimSpace(rec), "\x1f")
		if len(f) < 2 || f[0] != subject {
			continue
		}
		for _, v := range f[1:] {
			if strings.TrimSpace(v) == uid {
				return true
			}
		}
	}
	return false
}

func (s *sess) runCredentials(ctx context.Context) ([]engine.OutputRef, error) {
	if s.unprotected() {
		if !s.oc.allowUnprotected {
			return nil, fail("OBJSTORE_STATE_UNPROTECTED", 3, "procedures/bootstrap-preflight-failure.md#s3-locking",
				"shared credential mode without S3_STATE_ACCESS_KEY_ID/S3_STATE_SECRET_ACCESS_KEY: the OpenTofu state and the recovery bundle would be reachable with a key the cluster holds; "+
					"create a second object-storage account and set S3_STATE_*, or set S3_ALLOW_UNPROTECTED_STATE=true to accept the risk")
		}
		s.eng.RecordConfirmation(cli.Confirmation{Kind: "accept-state-unprotected",
			SubjectDigest: sha256Digest([]byte("accept-state-unprotected:" + s.cluster)), Method: "flag"})
		s.errorf("warning: OBJSTORE_STATE_UNPROTECTED accepted (S3_ALLOW_UNPROTECTED_STATE=true): the state and the bundle share the cluster's account; this stays flagged in every report\n")
	}
	creds, err := s.instanceCreds()
	if err != nil {
		return nil, err
	}
	if err := s.authenticateAll(ctx); err != nil {
		return nil, err
	}
	if err := s.requireSet(ctx); err != nil {
		return nil, err
	}
	for _, name := range sortedKeys(creds) {
		if err := s.set.AddExternal("SC-OBJECT-STORAGE", name, "env", credEnv(creds[name])); err != nil {
			return nil, err
		}
		s.red.Register("SC-OBJECT-STORAGE", creds[name].SecretKey)
		s.red.Register("SC-OBJECT-STORAGE", creds[name].AccessKey)
	}
	var refs []engine.OutputRef
	subject := "bootstrap(" + s.cluster + "): object-storage credentials"
	if !s.commitForInstall(ctx, subject) {
		var objs []identity.Secret
		for _, sec := range s.set.Secrets {
			if sec.Class == "SC-OBJECT-STORAGE" {
				objs = append(objs, sec)
			}
		}
		sub := &identity.Set{InstallUID: s.set.InstallUID, ClusterRecipient: s.set.ClusterRecipient, RecoveryRecipient: s.set.RecoveryRecipient,
			Register: s.set.Register, Secrets: objs}
		if err := s.renderSOPS(sub, s.recipients(sub)); err != nil {
			return nil, err
		}
		sha, err := commit.Create(ctx, commit.Request{
			Repo: s.repo, Cluster: s.cluster, Kind: commit.Install, Subject: subject,
			BaseDomain: str(dig(s.doc, "spec", "cluster", "baseDomain")), InstallUID: s.eng.InstallUID(), RunID: s.eng.RunID(), Clock: s.clk,
			Paths: []string{"gitops/clusters/" + s.cluster, "secrets/offcluster/" + s.cluster},
		})
		if err != nil {
			return nil, err
		}
		refs = append(refs, engine.OutputRef{Kind: "git-commit", Ref: sha})
	}
	// Re-seal once with the credentials (generation N+1) and upload it.
	cur := s.eng.RecoveryBundle()
	if first := s.identityFirst(); cur.Generation <= first {
		sealed, err := s.seal(s.set, cur.Generation+1, strings.TrimPrefix(cur.Sha256, "sha256:"))
		if err != nil {
			return nil, err
		}
		path, err := s.writeBundleFiles(s.set, cur.Generation+1, sealed)
		if err != nil {
			return nil, err
		}
		s.eng.SetRecoveryBundle(engine.RecoveryBundleState{Status: "created", Generation: cur.Generation + 1, Sha256: "sha256:" + sealed.SHA256,
			LocalPath: path, CreatedAt: s.clk.Now().UTC().Format(time.RFC3339)})
		if err := s.eng.Checkpoint(ctx); err != nil {
			return nil, err
		}
	}
	rb, err := s.uploadBundle(ctx)
	if err != nil {
		return nil, err
	}
	refs = append(refs, engine.OutputRef{Kind: "s3-object", Ref: rb.S3URI + "?versionId=" + rb.S3VersionID, Sha256: rb.Sha256})
	return refs, nil
}

// --- plan.state-conformance

type conformanceMarker struct {
	CredSet string `json:"cred_set"`
	tofustate.Flags
}

func (s *sess) conformanceCredSet() string {
	c := s.stateCred()
	return sha256Digest([]byte(c.Endpoint + "|" + c.AccessKey + "|" + s.bucketName("tofu-state")))
}

func (s *sess) markerPath() string { return filepath.Join(s.boot, "state-conformance.json") }

func (s *sess) readMarker() (conformanceMarker, bool) {
	b, err := os.ReadFile(s.markerPath())
	var m conformanceMarker
	if err != nil || json.Unmarshal(b, &m) != nil || m.CredSet != s.conformanceCredSet() {
		return conformanceMarker{}, false
	}
	return m, true
}

func (s *sess) lockProbe() tofustate.LockProbe {
	if s.deps.LockProbe != nil {
		return s.deps.LockProbe
	}
	c := s.stateCred()
	return tofustate.ExecLockProbe{Tofu: "tofu", Dir: filepath.Join(s.boot, "work", "conformance"),
		Backend: tofustate.Backend{Bucket: s.bucketName("tofu-state"), Region: c.Region, Endpoint: c.Endpoint, PathStyle: c.PathStyle},
		Env:     tofuEnv(s.env, c.tofu(), tofustate.Encryption{})}
}

func (s *sess) stepStateConformance() engine.Unit {
	return &unit{name: stepConformance, timeout: 15 * time.Minute, inputs: map[string]any{"credSet": s.conformanceCredSet()},
		check: func(context.Context) (engine.Check, error) {
			if _, ok := s.readMarker(); ok {
				return done("the conformance result is recorded for the current credential set"), nil
			}
			return notDone("no conformance result for the current credential set"), nil
		},
		run: func(ctx context.Context) ([]engine.OutputRef, error) {
			acct, err := s.account(s.stateCred())
			if err != nil {
				return nil, err
			}
			st := acct.Bucket(s.bucketName("tofu-state"))
			rep, err := tofustate.Conformance(ctx, tofustate.ConformanceParams{
				Store: st, Cluster: s.cluster, RunID: s.eng.RunID(), Clock: s.clk, Probe: s.lockProbe(), Cleanup: st,
			})
			if err != nil {
				return nil, err
			}
			reportPath := filepath.Join(s.boot, "runs", s.eng.RunID(), "s3-conformance.json")
			_ = tofustate.WriteReport(reportPath, rep)
			if len(rep.Fatal) > 0 {
				code := rep.Fatal[0]
				return nil, fail(code, 3, procLocking, "the object store failed the state conformance suite (%s); there is no override (plan 06 section 14.3)", strings.Join(rep.Fatal, ", "))
			}
			m, _ := json.Marshal(conformanceMarker{CredSet: s.conformanceCredSet(), Flags: rep.Flags})
			if err := writeAtomic(s.markerPath(), m); err != nil {
				return nil, err
			}
			return []engine.OutputRef{{Kind: "report", Ref: reportPath}}, nil
		}}
}

// --- plan.state-migrate

func (s *sess) remoteState() RemoteState {
	skip := false
	if m, ok := s.readMarker(); ok {
		skip = m.SkipS3Checksum
	}
	return RemoteState{Bucket: s.bucketName("tofu-state"), Key: tofustate.StateKey(s.cluster, "storage"), S3: s.stateCred().tofu(), SkipS3Checksum: skip}
}

func localStateFiles(spec StorageSpec) []string {
	m, _ := filepath.Glob(spec.LocalState + "*")
	return m
}

func sameSet(a, b []string) bool {
	x, y := append([]string(nil), a...), append([]string(nil), b...)
	sort.Strings(x)
	sort.Strings(y)
	return strings.Join(x, "\n") == strings.Join(y, "\n")
}

// storageStateList lists the remote storage state with this identity's passphrase. applicable is false when nothing
// can be judged: the Tofu cannot import, or the passphrase is not available (it comes from the recovery bundle).
func (s *sess) storageStateList(ctx context.Context, spec StorageSpec, remote RemoteState) (applicable bool, err error) {
	if _, ok := s.deps.Tofu.(StorageImporter); !ok {
		return false, nil
	}
	pass, perr := s.tofuPassphrase(ctx)
	if perr != nil {
		return false, nil
	}
	spec.Passphrase, spec.Generation, spec.Previous = pass, s.tofuGen, s.tofuPrev
	_, err = s.deps.Tofu.StateList(ctx, spec, &remote)
	return true, err
}

// storageStateStale reports whether the remote storage state exists but this identity's passphrase does not open it
// (the identity was abandoned after the migration) and the Tofu can import the buckets afresh. Only an identity that
// never completed plan.state-migrate can be stale: for one that did, a failing listing is a store problem and not a
// reason to replace the remote state.
func (s *sess) storageStateStale(ctx context.Context, spec StorageSpec, remote RemoteState) bool {
	if s.eng.StepEverSucceeded(stepMigrate) {
		return false
	}
	ok, err := s.storageStateList(ctx, spec, remote)
	return ok && err != nil
}

// importStorage rebuilds the storage state (plan 06 section 20.4): the import is built in a scratch path next to the
// local state, so a crash or a failure leaves no local state that a rerun could migrate; the plan after the import
// must show no changes; only then the state becomes the local state. It returns with no local state left when the
// Tofu wrote the remote object itself.
func (s *sess) importStorage(ctx context.Context, spec StorageSpec, remote RemoteState) error {
	imp := s.deps.Tofu.(StorageImporter)
	tmp := filepath.Join(filepath.Dir(spec.LocalState), "importing", filepath.Base(spec.LocalState))
	scratch := spec
	scratch.LocalState = tmp
	cleanup := func() { removeAll(localStateFiles(scratch)) }
	if err := os.MkdirAll(filepath.Dir(tmp), 0o700); err != nil {
		return fail("STATE_IMPORT_FAILED", 1, procState, "the scratch directory of the import cannot be created: %v", err)
	}
	cleanup()
	if err := imp.ImportStorage(ctx, scratch, remote); err != nil {
		cleanup()
		return fail("STATE_IMPORT_FAILED", 1, procState, "the storage buckets cannot be re-imported: %v", err)
	}
	built := localStateFiles(scratch)
	var from *RemoteState
	if len(built) == 0 {
		from = &remote
	}
	n, err := s.deps.Tofu.Changes(ctx, scratch, from)
	if err != nil {
		cleanup()
		return fail("STATE_PLAN_FAILED", 1, procState, "tofu plan after the import failed: %v", err)
	}
	if n != 0 {
		cleanup()
		return fail("STATE_IMPORT_MISMATCH", 1, procState, "tofu plan after the import shows %d changes; the remote state is unchanged", n)
	}
	if len(built) == 0 {
		if _, err := s.deps.Tofu.StateList(ctx, spec, &remote); err != nil {
			return fail("STATE_LIST_FAILED", 1, procState, "the re-imported storage state cannot be listed: %v", err)
		}
		return nil
	}
	for _, f := range built {
		if err := os.Rename(f, spec.LocalState+strings.TrimPrefix(f, tmp)); err != nil {
			cleanup()
			removeAll(localStateFiles(spec))
			return fail("STATE_IMPORT_FAILED", 1, procState, "the imported state cannot be moved into place: %v", err)
		}
	}
	return nil
}

func (s *sess) stepStateMigrate() engine.Unit {
	return &unit{name: stepMigrate, timeout: 30 * time.Minute, inputs: map[string]any{"key": tofustate.StateKey(s.cluster, "storage")},
		check: func(ctx context.Context) (engine.Check, error) {
			spec := s.storageSpec("")
			remote := s.remoteState()
			acct, err := s.account(s.stateCred())
			if err != nil {
				return engine.Check{}, err
			}
			if _, err := acct.Bucket(remote.Bucket).Head(ctx, remote.Key); err != nil {
				if store.Is(err, store.NotFound) {
					return notDone("no remote state object"), nil
				}
				return engine.Check{}, err
			}
			local := localStateFiles(spec)
			if len(local) == 0 {
				if s.eng.StepEverSucceeded(stepMigrate) {
					// an established install: a listing failure is the store's, not a stale passphrase
					if _, lerr := s.storageStateList(ctx, spec, remote); lerr != nil {
						return engine.Check{}, lerr
					}
				} else if s.storageStateStale(ctx, spec, remote) {
					return notDone("the remote storage state is not readable with this identity's passphrase"), nil
				}
				if !s.eng.StepEverSucceeded(stepMigrate) {
					// a success must carry its output reference, so that it is durable in the journal
					return notDone("the remote state exists but this identity has not recorded the migration"), nil
				}
				return done("the remote state exists and no local state is left"), nil
			}
			// A crash between the migration and the deletion: delete the local file only after re-verification.
			pass, err := s.tofuPassphrase(ctx)
			if err != nil {
				return notDone("the local state is left but the passphrase is not available: " + err.Error()), nil
			}
			spec.Passphrase, spec.Generation, spec.Previous = pass, s.tofuGen, s.tofuPrev
			a, aerr := s.deps.Tofu.StateList(ctx, spec, nil)
			b, berr := s.deps.Tofu.StateList(ctx, spec, &remote)
			if aerr != nil || berr != nil || !sameSet(a, b) {
				return notDone("the local and the remote state differ"), nil
			}
			removeAll(local)
			if !s.eng.StepEverSucceeded(stepMigrate) {
				return notDone("the remote state matches and the local state was deleted; the migration is recorded by the run"), nil
			}
			return done("the remote state matches; the local state was deleted"), nil
		},
		run: func(ctx context.Context) ([]engine.OutputRef, error) {
			pass, err := s.tofuPassphrase(ctx)
			if err != nil {
				return nil, err
			}
			spec := s.storageSpec(pass)
			remote := s.remoteState()
			if len(localStateFiles(spec)) == 0 {
				if s.storageStateStale(ctx, spec, remote) {
					// the buckets exist and their state is encrypted under an abandoned identity's passphrase: rebuild it
					if err := s.importStorage(ctx, spec, remote); err != nil {
						return nil, err
					}
				}
				if len(localStateFiles(spec)) == 0 {
					if acct, aerr := s.account(s.stateCred()); aerr == nil {
						if info, herr := acct.Bucket(remote.Bucket).Head(ctx, remote.Key); herr == nil {
							return []engine.OutputRef{{Kind: "tofu-state", Ref: stateRef(remote, info.VersionID)}}, nil
						}
					}
				}
			}
			before, err := s.deps.Tofu.StateList(ctx, spec, nil)
			if err != nil {
				return nil, fail("STATE_LIST_FAILED", 1, procState, "the local storage state cannot be listed: %v", err)
			}
			s.eng.Log("info", "state-migrate-before", "storage state addresses before the migration", map[string]any{"addresses": before})
			if err := s.deps.Tofu.Migrate(ctx, spec, remote); err != nil {
				return nil, fail("STATE_MIGRATE_FAILED", 1, procState, "tofu init -migrate-state failed: %v", err)
			}
			after, err := s.deps.Tofu.StateList(ctx, spec, &remote)
			if err != nil {
				return nil, fail("STATE_LIST_FAILED", 1, procState, "the remote storage state cannot be listed: %v", err)
			}
			if !sameSet(before, after) {
				return nil, fail("STATE_MIGRATE_MISMATCH", 1, procState,
					"the remote state lists %d addresses, the local state listed %d; the local state is kept", len(after), len(before))
			}
			n, err := s.deps.Tofu.Changes(ctx, spec, &remote)
			if err != nil {
				return nil, fail("STATE_PLAN_FAILED", 1, procState, "tofu plan against the remote state failed: %v", err)
			}
			if n != 0 {
				return nil, fail("STATE_MIGRATE_MISMATCH", 1, procState, "tofu plan against the remote state shows %d changes; the local state is kept", n)
			}
			acct, err := s.account(s.stateCred())
			if err != nil {
				return nil, err
			}
			info, err := acct.Bucket(remote.Bucket).Head(ctx, remote.Key)
			if err != nil {
				return nil, fail("STATE_MIGRATE_MISMATCH", 1, procState, "the remote state object cannot be read back: %v", err)
			}
			removeAll(localStateFiles(spec))
			return []engine.OutputRef{{Kind: "tofu-state", Ref: stateRef(remote, info.VersionID)}}, nil
		}}
}

// stateRef is the tofu-state output reference of plan.state-migrate: the remote object with its version id.
func stateRef(remote RemoteState, versionID string) string {
	ref := "s3://" + remote.Bucket + "/" + remote.Key
	if versionID != "" {
		ref += "?versionId=" + versionID
	}
	return ref
}

func removeAll(paths []string) {
	for _, p := range paths {
		_ = os.Remove(p)
	}
}

// --- plan.bundle-confirm

func (s *sess) stepBundleConfirm() engine.Unit {
	return identity.NewBundleConfirm(identity.BundleConfirmConfig{
		Journal: s.eng,
		Clock:   s.clk,
		Download: func(ctx context.Context) ([]byte, error) {
			rb := s.eng.RecoveryBundle()
			acct, err := s.account(s.credFor("recovery"))
			if err != nil {
				return nil, err
			}
			obj, err := acct.Bucket(s.bucketName("recovery")).Get(ctx, bundleKey(s.cluster, rb.Generation))
			return obj.Data, err
		},
		Prompt: func(ctx context.Context, attempt int) (string, error) {
			if f := s.env["SC_RECOVERY_PASSPHRASE_FILE"]; f != "" {
				return identity.ReadPassphraseFile(f)
			}
			if s.inv.NonInteractive {
				return "", errors.New("--non-interactive without SC_RECOVERY_PASSPHRASE_FILE: the passphrase cannot be re-entered")
			}
			if attempt == 1 {
				s.printf("\x1b[2J\x1b[H")
				s.printf("RECOVERY BUNDLE CHECK\n\nStore these offline before you continue:\n")
				s.printf("  - the recovery passphrase (only you know it)\n  - %s\n  - the bundle copies in %s\n\n", filepath.Join(s.cdir(), "recovery-identity.txt"), filepath.Join(s.cdir(), "bundles"))
			}
			return s.readLine(ctx, fmt.Sprintf("Re-enter the recovery passphrase (attempt %d of 3): ", attempt), true)
		},
	})
}
