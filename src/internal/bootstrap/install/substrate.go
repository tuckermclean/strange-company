package install

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"go.yaml.in/yaml/v3"

	"strange.company/sc/internal/bootstrap/cli"
	"strange.company/sc/internal/bootstrap/config"
	"strange.company/sc/internal/provider"
	"strange.company/sc/internal/provider/fake"
	"strange.company/sc/internal/provider/price"
	"strange.company/sc/internal/toolbox"
)

// This file holds PF-03 (plan 03 sections 9.1 and 9.2: the profile kind rules and the lab refusal) and the price and cost
// checks of plan 03 sections 14.5 and 14.6.

const (
	kindSubstrate = "BootstrapSubstrate"
	kindCluster   = "ClusterProfile"

	defaultProfile = "self-hosting-minimum"

	// labLabel marks a lab substrate (plan 05 section 14.6); it is refused when the provider is paid (03-AC-11).
	labLabel = "strange.company/lab"

	// priceCatalogAging is the warning for a catalogue verified more than 90 days ago. Plan 03 section 14.5 names the
	// warning but no code; this one is invented.
	priceCatalogAging = "PRICE_CATALOG_AGING"

	procGuardrail = "procedures/bootstrap-preflight-failure.md#guardrail"
)

// profileInfo is what PF-03 reads of infra/profiles/<name>.yaml.
type profileInfo struct {
	Kind string
	Lab  bool
}

func profilePath(repo, name string) string {
	return filepath.Join(repo, "infra", "profiles", name+".yaml")
}

func readProfile(repo, name string) (profileInfo, error) {
	b, err := os.ReadFile(profilePath(repo, name))
	if err != nil {
		return profileInfo{}, fmt.Errorf("infra/profiles/%s.yaml cannot be read: %w", name, err)
	}
	var doc struct {
		Kind     string `yaml:"kind"`
		Metadata struct {
			Labels map[string]string `yaml:"labels"`
		} `yaml:"metadata"`
	}
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return profileInfo{}, fmt.Errorf("infra/profiles/%s.yaml is not YAML: %w", name, err)
	}
	return profileInfo{Kind: doc.Kind, Lab: doc.Metadata.Labels[labLabel] == "true"}, nil
}

// pf03 checks the profile against the kind the command needs (a fresh install needs a BootstrapSubstrate, expand a
// ClusterProfile) and, when the provider is paid, refuses a lab substrate. A profile file that cannot be read fails
// closed. It returns the failing row (nil on a pass).
func pf03(repo, name, wantKind string, paid bool) *toolbox.PFRow {
	if !config.ValidProfileName(name) {
		return &toolbox.PFRow{ID: "PF-03", Status: "fail", Code: "PROFILE_INVALID", Msg: fmt.Sprintf("profile %q is not a profile name (a name, not a path)", name)}
	}
	info, err := readProfile(repo, name)
	if err != nil {
		return &toolbox.PFRow{ID: "PF-03", Status: "fail", Code: "PROFILE_UNREADABLE", Msg: err.Error() + " (a profile that cannot be judged is not assumed fine; see procedures/bootstrap-preflight-failure.md#profile)"}
	}
	if info.Kind != wantKind {
		return &toolbox.PFRow{ID: "PF-03", Status: "fail", Code: "PROFILE_KIND_MISMATCH",
			Msg: fmt.Sprintf("profile %s is a %s; this command needs a %s", name, info.Kind, wantKind)}
	}
	if paid && info.Lab {
		return &toolbox.PFRow{ID: "PF-03", Status: "fail", Code: "LAB_SUBSTRATE_PAID_REFUSED",
			Msg: fmt.Sprintf("profile %s is a lab substrate (%s: \"true\") and the provider is paid", name, labLabel)}
	}
	return nil
}

// labTestMode is the one source of truth for CD-76 (a): test mode, a lab-labeled substrate and an unpaid provider. The
// preflight command and the install pipeline's preflight phase both ask it.
func labTestMode(inv cli.Invocation, env map[string]string) bool {
	if env["SC_TEST_MODE"] != "1" {
		return false
	}
	repo := env["SC_REPO_ROOT"]
	if repo == "" {
		repo = "."
	}
	name := commandProfile(inv, env)
	if !config.ValidProfileName(name) {
		return false
	}
	info, err := readProfile(repo, name)
	return err == nil && info.Lab && !commandPaid(repo, inv, env)
}

// commandProfile is the profile a command line names: --profile, else PROFILE of the env file, else the default.
func commandProfile(inv cli.Invocation, env map[string]string) string {
	if inv.Profile != "" {
		return inv.Profile
	}
	if v := envFileVars(inv, env)["PROFILE"]; v != "" {
		return v
	}
	return defaultProfile
}

// commandPaid reports whether the provider the env file names costs money. The fake asks its scenario; the existing
// provider is free; every other provider is paid (fail closed).
func commandPaid(repo string, inv cli.Invocation, env map[string]string) bool {
	switch name := envFileVars(inv, env)["PROVIDER"]; name {
	case "", "existing":
		return false
	case "fake":
		p, err := fake.New(env, filepath.Join(repo, ".bootstrap", "fake-provider"))
		return err == nil && p.Capabilities().Paid
	default:
		return true
	}
}

func envFileVars(inv cli.Invocation, env map[string]string) map[string]string {
	path := inv.EnvFile
	if path == "" {
		in := env["SC_INPUT_DIR"]
		if in == "" {
			in = "/run/sc-input"
		}
		path = filepath.Join(in, "bootstrap.env")
	}
	if b, err := os.ReadFile(path); err == nil {
		if vars, err := config.ParseEnv(b); err == nil {
			return vars
		}
	}
	return nil
}

// profileCommand runs PF-03 for the commands that do not enter the install pipeline. `expand` needs a ClusterProfile and
// is judged before the command tree reports it as not implemented; `preflight` appends the PF-03 row to its own rows.
// handled is true when the command is finished.
func profileCommand(args []string, inv cli.Invocation, env map[string]string, stdout, stderr io.Writer, deps Deps) (code int, handled bool) {
	repo := env["SC_REPO_ROOT"]
	if repo == "" {
		repo = "."
	}
	switch inv.Command[0] {
	case "expand":
		if row := pf03(repo, commandProfile(inv, env), kindCluster, false); row != nil {
			fmt.Fprintln(stdout, toolbox.FormatRow(*row))
			return 3, true
		}
	case "preflight":
		code := cli.Run(args, env, stdout, stderr, deps.CLI)
		if row := pf03(repo, commandProfile(inv, env), kindSubstrate, commandPaid(repo, inv, env)); row != nil {
			fmt.Fprintln(stdout, toolbox.FormatRow(*row))
			if code == 0 {
				code = 3
			}
		}
		return code, true
	}
	return 0, false
}

// profileName is the effective profile of the installation (PROFILE or --profile, normalized).
func (s *sess) profileName() string {
	if p := str(dig(s.doc, "spec", "cluster", "profile")); p != "" {
		return p
	}
	return defaultProfile
}

// --- price catalogue and cost check (plan 03 sections 14.5 and 14.6)

func (s *sess) maxMonthlyCost() (amount, currency string) {
	amount = str(dig(s.doc, "spec", "provider", "maxMonthlyCost", "amount"))
	currency = str(dig(s.doc, "spec", "provider", "maxMonthlyCost", "currency"))
	if amount == "" {
		amount = "0"
	}
	if currency == "" {
		currency = "EUR"
	}
	return amount, currency
}

// costCheck prices the plan before anything is created. An unpaid provider costs 0, which any limit (also 0) admits. A
// paid provider's nodes are priced from infra/catalog/prices/<provider>.yaml: a catalogue older than 180 days is
// PRICE_CATALOG_STALE (exit 4) unless SC_ACCEPT_STALE_PRICES=1, which is recorded as the confirmation accept-stale-prices;
// an unpriced product is COST_INCOMPLETE and a total above MAX_MONTHLY_COST is COST_OVER_LIMIT (both exit 4).
func (s *sess) costCheck(plan *provider.ProviderPlan) error {
	caps := s.prov.Capabilities()
	limit, currency := s.maxMonthlyCost()
	if !caps.Paid {
		return nil
	}
	rel := filepath.Join("infra", "catalog", "prices", caps.Name+".yaml")
	path := filepath.Join(s.repo, rel)
	raw, rerr := os.ReadFile(path)
	cat, err := price.Load(path)
	if err == nil {
		err = rerr
	}
	if err == nil && cat.Provider != caps.Name {
		err = fmt.Errorf("%s is the catalogue of provider %q, not %q", filepath.ToSlash(rel), cat.Provider, caps.Name)
	}
	if err != nil {
		return fail("COST_INCOMPLETE", 4, procGuardrail, "the plan cannot be priced: %v", err)
	}
	product := plan.MachineClass
	if product == "" {
		product = s.vars["MACHINE_CLASS"]
	}
	entry, ok := cat.Entry(product)
	if !ok {
		return fail("COST_INCOMPLETE", 4, procGuardrail, "the product %q is not in %s; the estimate is incomplete", product, filepath.ToSlash(rel))
	}
	if cat.Currency != currency {
		return fail("PRICE_CURRENCY_MISMATCH", 4, procGuardrail, "the catalogue %s is in %s, COST_CURRENCY is %s (no conversion)", filepath.ToSlash(rel), cat.Currency, currency)
	}
	age, err := entry.AgeDays(s.clk.Now())
	if err != nil {
		return fail("COST_INCOMPLETE", 4, procGuardrail, "%v", err)
	}
	switch {
	case age > price.FailAfterDays:
		if s.env["SC_ACCEPT_STALE_PRICES"] != "1" {
			return fail("PRICE_CATALOG_STALE", 4, procGuardrail,
				"%s was verified %d days ago (limit %d); update the catalogue or accept it for this run with SC_ACCEPT_STALE_PRICES=1", filepath.ToSlash(rel), age, price.FailAfterDays)
		}
		digest := sha256Digest(raw)
		if !s.eng.Confirmed("accept-stale-prices", digest) {
			s.eng.RecordConfirmation(cli.Confirmation{Kind: "accept-stale-prices", SubjectDigest: digest, Method: "flag"})
		}
		s.errorf("warning: PRICE_CATALOG_STALE accepted with SC_ACCEPT_STALE_PRICES=1: %s was verified %d days ago\n", filepath.ToSlash(rel), age)
	case age > price.WarnAfterDays:
		s.errorf("warning: %s: %s was verified %d days ago (warning after %d)\n", priceCatalogAging, filepath.ToSlash(rel), age, price.WarnAfterDays)
	}
	est := cat.Compute(product, len(plan.Nodes))
	switch {
	case !est.Complete:
		return fail("COST_INCOMPLETE", 4, procGuardrail, "the product %q has no usable price in %s", product, filepath.ToSlash(rel))
	case !price.WithinLimit(est.MonthlyTotal, limit):
		return fail("COST_OVER_LIMIT", 4, procGuardrail, "the monthly total %s %s is above MAX_MONTHLY_COST %s %s", est.MonthlyTotal, est.Currency, limit, currency)
	}
	plan.MonthlyCost = est.MonthlyTotal + " " + est.Currency
	return nil
}
