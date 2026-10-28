package install

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"net/netip"
	"os"
	"path/filepath"

	"strange.company/sc/internal/bootstrap/cli"
	"strange.company/sc/internal/bootstrap/config"
	"strange.company/sc/internal/bootstrap/engine"
	"strange.company/sc/internal/bootstrap/preflight"
	"strange.company/sc/internal/version"
)

// configFailure reports a configuration that does not load the way preflight does (rows and plan 50 exit codes).
func (s *sess) configFailure(loadErr error) int {
	code := cli.RunPreflight(s.inv, s.env, s.out, s.errw, s.deps.CLI)
	if code == 0 {
		s.errorf("sc-bootstrap: the configuration cannot be loaded: %v\n", loadErr)
		return 2
	}
	return code
}

// bootstrapConfigYAML normalizes bootstrap.env with the install uid; the document is gitops/clusters/<c>/bootstrap-config.yaml.
func (s *sess) bootstrapConfigYAML(uid string) ([]byte, error) {
	res, err := config.Normalize(s.merged, config.Options{Clock: s.clk, InstallUID: uid, Version: version.Version, Profile: s.inv.Profile})
	if err != nil {
		return nil, err
	}
	return res.YAML, nil
}

// clusterDNSPins is the pinned ClusterIP set of PF-30: the cluster DNS IP, from the preflight override when given.
func (s *sess) clusterDNSPins(serviceCIDR string) []netip.Addr {
	if s.deps.CLI.ClusterDNSIP.IsValid() {
		return []netip.Addr{s.deps.CLI.ClusterDNSIP}
	}
	if ip, err := preflight.ClusterDNSIP(serviceCIDR); err == nil {
		return []netip.Addr{ip}
	}
	return nil
}

func (s *sess) ensureTofu() {
	if s.deps.Tofu == nil {
		s.deps.Tofu = &ExecTofu{Repo: s.repo, Env: s.env}
	}
}

// cmdPlan is the read-only preview (plan 50 section 7.1): preflight, the storage footprint and a storage plan against
// a throwaway local backend when the remote state does not exist yet. It creates no bucket, object or commit.
func (s *sess) cmdPlan(ctx context.Context) int {
	if err := s.load(); err != nil {
		return s.configFailure(err)
	}
	s.ensureDefaults()
	deps := s.deps.CLI
	deps.ReadOnly, deps.Record = true, nil
	if code := cli.RunPreflight(s.inv, s.env, s.out, s.errw, deps); code != 0 {
		return code
	}
	text, _ := s.footprint()
	s.printf("%s", text)
	if acct, err := s.account(s.stateCred()); err == nil && acct.HeadBucket(ctx, s.bucketName("tofu-state")) == nil {
		s.printf("The state bucket %s exists: the storage plan is the plan of the next run.\n", s.bucketName("tofu-state"))
		return 0
	}
	if s.oc.bucketMode == "existing" {
		return 0
	}
	if err := os.MkdirAll(filepath.Join(s.boot, "work"), 0o700); err != nil {
		s.errorf("sc-bootstrap: %v\n", err)
		return 1
	}
	dir, err := os.MkdirTemp(filepath.Join(s.boot, "work"), "plan-storage-")
	if err != nil {
		s.errorf("sc-bootstrap: %v\n", err)
		return 1
	}
	defer os.RemoveAll(dir)
	var rnd [24]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		s.errorf("sc-bootstrap: %v\n", err)
		return 1
	}
	spec := s.storageSpec(base64.RawURLEncoding.EncodeToString(rnd[:]))
	spec.WorkDir, spec.LocalState = filepath.Join(dir, "storage"), filepath.Join(dir, "storage.local.tfstate")
	summary, err := s.deps.Tofu.PlanStorage(ctx, spec)
	if err != nil {
		s.errorf("STORAGE_PLAN_FAILED: the storage plan failed: %v\n", err)
		return 1
	}
	s.printf("Storage plan (throwaway local backend; nothing was applied): %s\n", summary)
	return 0
}

// ensureDefaults fills the production implementations of the seams the tests replace.
func (s *sess) ensureDefaults() {
	s.ensureTofu()
	if s.deps.NewS3 == nil {
		s.deps.NewS3 = NewS3Account
	}
	if s.deps.Encrypt == nil {
		s.deps.Encrypt = SopsEncrypt
	}
}

// cmdStatus prints the journal summary; it neither locks nor writes anything.
func (s *sess) cmdStatus(ctx context.Context) int {
	cfg := engine.Config{Dir: s.boot, Clock: s.clk, Redactor: s.red, Env: s.env}
	if err := s.load(); err == nil {
		s.ensureDefaults()
		s.tryAttach(ctx)
		cfg = s.engineConfig("status")
	}
	return engine.Status(ctx, cfg, s.inv.Flags["json"] == "true", s.out, s.errw)
}

// cmdRun runs the install pipeline.
func (s *sess) cmdRun(ctx context.Context) int {
	if err := s.load(); err != nil {
		return s.configFailure(err)
	}
	s.ensureDefaults()
	if s.vars["PROVIDER"] == "fake" || str(dig(s.doc, "spec", "provider", "name")) == "fake" {
		if err := s.startFakeProvider(); err != nil {
			s.errorf("TEST_MODE_REQUIRED: %v\n", err)
			return 2
		}
	}
	s.tryAttach(ctx)
	eng, err := engine.New(s.engineConfig("run"))
	if err != nil {
		s.errorf("sc-bootstrap: %v\n", err)
		return 1
	}
	s.eng = eng
	opts := engine.Options{FromPhase: s.inv.Flags["from-phase"], RetryPhase: s.inv.Flags["retry-phase"]}
	res := eng.Execute(ctx, s.pipeline(), opts)
	if res.Exit != 0 {
		s.errorf("%s: %s\n", res.Code, res.Message)
		return res.Exit
	}
	for _, d := range res.Drift {
		s.errorf("warning: phase %s drifted from the configuration its output was made for\n", d)
	}
	s.printf("Install pipeline finished through the plan phase (installation state %s).\n", eng.InstallationState())
	return 0
}
