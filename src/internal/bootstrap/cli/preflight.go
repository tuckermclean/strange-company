package cli

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"go.yaml.in/yaml/v3"
	"golang.org/x/text/language"
	"golang.org/x/text/message"

	"strange.company/sc/internal/bootstrap/config"
	"strange.company/sc/internal/bootstrap/preflight"
	"strange.company/sc/internal/clock"
	"strange.company/sc/internal/contracts"
	"strange.company/sc/internal/scrub"
	"strange.company/sc/internal/toolbox"
	"strange.company/sc/internal/version"
)

// The embedded file is a byte-identical copy of contracts/bootstrap-config.schema.yaml
// (TestEmbeddedSchema_MatchesContract compares them; refresh with `cp contracts/bootstrap-config.schema.yaml src/internal/bootstrap/cli/schemas/`).
//
//go:embed schemas/bootstrap-config.schema.yaml
var configSchemaYAML []byte

// placeholderUID stands in for the install identity, which plan.install-identity generates later; preflight only
// validates the shape of the normalized document.
const placeholderUID = "00000000-0000-4000-8000-000000000000"

// Exit codes of plan 50 section 7.2 used by the preflight driver.
const (
	exitUsage     = 2
	exitPreflight = 3
	exitConfirm   = 5
)

// row is a preflight result row with the exit code its failure maps to.
type row struct {
	toolbox.PFRow
	exit int
}

func pass(id, format string, a ...any) row {
	return row{PFRow: toolbox.PFRow{ID: id, Status: "pass", Msg: fmt.Sprintf(format, a...)}}
}

func failRow(id, code string, exit int, format string, a ...any) row {
	return row{PFRow: toolbox.PFRow{ID: id, Status: "fail", Code: code, Msg: fmt.Sprintf(format, a...)}, exit: exit}
}

// printer writes rows through a scrubber so no secret value can reach the output.
type printer struct {
	out  io.Writer
	json bool
	sc   *scrub.Scrubber
}

func (p *printer) row(r toolbox.PFRow) {
	if p.json {
		b, _ := json.Marshal(map[string]string{"id": r.ID, "status": r.Status, "code": r.Code, "message": p.sc.String(r.Msg)})
		fmt.Fprintln(p.out, string(b))
		return
	}
	r.Msg = p.sc.String(r.Msg)
	fmt.Fprintln(p.out, toolbox.FormatRow(r))
}

func runPreflight(inv Invocation, env map[string]string, stdout, stderr io.Writer, deps Deps) int {
	if deps.Clock == nil {
		deps.Clock = clock.Real{}
	}
	if deps.Resolve == nil {
		deps.Resolve = lookupHost
	}
	p := &printer{out: stdout, json: inv.LogFormat == "json", sc: scrub.New()}

	envFile := inv.EnvFile
	if envFile == "" {
		in := env["SC_INPUT_DIR"]
		if in == "" {
			in = "/run/sc-input"
		}
		envFile = filepath.Join(in, "bootstrap.env")
	}
	// PF-01 (the toolbox self-check) comes first and is always printed; PF-06 needs the parsed variables.
	data, rerr := os.ReadFile(envFile)
	var vars map[string]string
	var perr error
	if rerr == nil {
		vars, perr = config.ParseEnv(data)
	}
	tb := toolbox.ToolboxRows(env, envFile, vars)
	pf01 := row{PFRow: tb[0]}
	if pf01.Status == "fail" {
		pf01.exit = exitPreflight
	}
	pf06 := row{PFRow: tb[1]}
	if pf06.Status == "fail" {
		pf06.exit = exitPreflight
	}
	// stop prints PF-01, the failing PF-02 row and, when more is evaluated, the extra rows. An invalid
	// configuration (unknown key, unparseable file, schema-invalid value, test mode with a paid provider) is a
	// usage error and exits 2 even when another check fails. A missing configuration (unreadable file, required
	// variables absent) exits 2 alone and 3 when another check also fails (plan 50 section 14.2, D10).
	stop := func(code string, exit int, extra []row, format string, a ...any) int {
		rows := append([]row{pf01, failRow("PF-02", code, exit, format, a...)}, extra...)
		out := exit
		missing := code == "ENV_FILE_UNREADABLE" || code == "CONFIG_REQUIRED_MISSING"
		for _, r := range rows {
			p.row(r.PFRow)
			if r.ID != "PF-02" && r.Status == "fail" && (missing || exit != exitUsage) {
				out = exitPreflight
			}
		}
		return out
	}
	if rerr != nil {
		var pe *os.PathError
		if errors.As(rerr, &pe) {
			rerr = pe.Err
		}
		return stop("ENV_FILE_UNREADABLE", exitUsage, nil, "%s: %v", envFile, rerr)
	}
	if perr != nil {
		return stop("ENV_FILE_UNPARSEABLE", exitUsage, nil, "%s: %v", envFile, perr)
	}
	var secrets []string
	for k, v := range vars {
		if config.IsSecretKey(k) && v != "" {
			secrets = append(secrets, v)
		}
	}
	p.sc = scrub.New(secrets...)
	if unknown := unknownKeys(vars); len(unknown) > 0 {
		return stop("UNKNOWN_KEY", exitUsage, nil, "%s", strings.Join(unknown, "; "))
	}
	// The test-mode variables come from the process environment, never from the file (plan 50 section 9.2).
	merged := map[string]string{}
	for k, v := range vars {
		merged[k] = v
	}
	for k, v := range env {
		if v != "" && (strings.HasPrefix(k, "SC_TEST_") || strings.HasPrefix(k, "SC_LAB_S3_")) {
			merged[k] = v
		}
	}
	res, nerr := config.Normalize(merged, config.Options{Clock: deps.Clock, InstallUID: placeholderUID, Version: version.Version, Profile: inv.Profile})
	if nerr != nil {
		code, exit := "CONFIG_INVALID", exitUsage
		var ce *config.Error
		if errors.As(nerr, &ce) {
			code, exit = ce.Code, ce.ExitCode
			nerr = errors.New(ce.Message)
		}
		var extra []row
		if code == "CONFIG_REQUIRED_MISSING" {
			extra = []row{pf06} // PF-06 only needs the parsed variables
		}
		return stop(code, exit, extra, "%v", nerr)
	}
	doc, derr := contracts.ParseYAML(res.YAML)
	if derr == nil {
		derr = validateConfig(doc)
	}
	if derr != nil {
		return stop("CONFIG_SCHEMA_INVALID", exitUsage, nil, "the normalized configuration violates bootstrap-config.schema.yaml: %s", schemaErrText(derr))
	}

	rows := []row{pf01, pass("PF-02", "bootstrap.env parses and validates"), pf06}
	if deps.ProbeModelKey == nil {
		deps.ProbeModelKey = policyProbe(env, filepath.Join(repoRoot(env), "policy", "model-routing.yaml"))
	}
	pc := &pfCtx{inv: inv, env: env, vars: vars, doc: doc, deps: deps, repo: repoRoot(env), cluster: str(dig(doc, "spec", "cluster", "name"))}
	rows = append(rows, pc.pf19(), pc.pf26(), pc.pf28(), pc.pf29(), pc.pf30())

	exit := 0
	for _, r := range rows {
		p.row(r.PFRow)
		if r.Status == "fail" {
			switch {
			case r.exit == exitPreflight && exit != exitPreflight:
				exit = exitPreflight
			case r.exit == exitConfirm && exit == 0:
				exit = exitConfirm
			case exit == 0:
				exit = exitPreflight
			}
		}
	}
	_ = stderr
	return exit
}

func validateConfig(doc any) error {
	schemas, err := contracts.CompileSchemaBytes(map[string][]byte{"bootstrap-config.schema.yaml": configSchemaYAML})
	if err != nil {
		return err
	}
	s := schemas["bootstrap-config"]
	if s == nil {
		return errors.New("bootstrap-config schema not compiled")
	}
	return s.Validate(doc)
}

func repoRoot(env map[string]string) string {
	if r := env["SC_REPO_ROOT"]; r != "" {
		return r
	}
	return "."
}

// unknownKeys reports every variable that is not in the plan 50 section 9.2 table, with the nearest known name.
func unknownKeys(vars map[string]string) []string {
	known := config.KnownKeys()
	var names []string
	for k := range vars {
		if !config.IsKnownKey(k) {
			names = append(names, k)
		}
	}
	sort.Strings(names)
	var out []string
	for _, k := range names {
		msg := "unknown key " + k
		best, bd := "", 1<<30
		for _, c := range known {
			if d := levenshtein(k, c); d < bd {
				best, bd = c, d
			}
		}
		if bd <= 3 && bd*2 < len(k) {
			msg += " (did you mean " + best + "?)"
		}
		out = append(out, msg)
	}
	return out
}

func levenshtein(a, b string) int {
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			c := 1
			if a[i-1] == b[j-1] {
				c = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+c)
		}
		prev = cur
	}
	return prev[len(b)]
}

// pfCtx carries what the section 14.2 checks of this slice read.
type pfCtx struct {
	inv     Invocation
	env     map[string]string
	vars    map[string]string
	doc     any
	deps    Deps
	repo    string
	cluster string
}

func dig(v any, path ...string) any {
	for _, p := range path {
		m, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = m[p]
	}
	return v
}

func str(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}

// loadPolicy reads policy/<name> as a document and its sha256 digest; a missing file is POLICY_FILE_MISSING (D4).
func (c *pfCtx) loadPolicy(name string) (doc any, digest string, err *preflight.Error) {
	b, rerr := os.ReadFile(filepath.Join(c.repo, "policy", name))
	if rerr != nil {
		return nil, "", &preflight.Error{Code: "POLICY_FILE_MISSING", Message: fmt.Sprintf("policy/%s cannot be read: it must exist in the repository (fail closed)", name)}
	}
	d, perr := contracts.ParseYAML(b)
	if perr != nil {
		return nil, "", &preflight.Error{Code: "POLICY_FILE_INVALID", Message: fmt.Sprintf("policy/%s is not valid YAML: %v", name, perr)}
	}
	sum := sha256.Sum256(b)
	return d, "sha256:" + hex.EncodeToString(sum[:]), nil
}

// keyVarFor is the bootstrap.env variable that holds a provider's API key (D8): the policy's credential.secretKey,
// upper-cased with "-" turned into "_", plus "_API_KEY". Plans 50 and 53 do not name the variable; a follow-up owns it.
// A provider without a secretKey has no variable (ok false): the caller fails closed, the id is never guessed.
func keyVarFor(routing any, provider string) (name string, ok bool) {
	sk := str(dig(routing, "spec", "providers", provider, "credential", "secretKey"))
	if sk == "" {
		return "", false
	}
	return strings.ToUpper(strings.ReplaceAll(sk, "-", "_")) + "_API_KEY", true
}

// enabledProviders are the enabled production providers of policy/model-routing.yaml, sorted by id.
func enabledProviders(routing any) []string {
	pm, _ := dig(routing, "spec", "providers").(map[string]any)
	var out []string
	for name, pol := range pm {
		if dig(pol, "enabled") != true || str(dig(pol, "stage")) == "test-only" {
			continue
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func (c *pfCtx) pf19() row {
	routing, _, err := c.loadPolicy("model-routing.yaml")
	if err != nil {
		return failRow("PF-19", err.Code, exitPreflight, "%s", err.Message)
	}
	var providers []string // enabled production providers that have a key in the env
	for _, name := range enabledProviders(routing) {
		kv, ok := keyVarFor(routing, name)
		if !ok {
			return failRow("PF-19", "POLICY_FILE_INVALID", exitPreflight,
				"policy/model-routing.yaml: provider %q is enabled but names no credential.secretKey (fail closed)", name)
		}
		if c.vars[kv] != "" {
			providers = append(providers, name)
		}
	}
	for _, name := range providers {
		kv, _ := keyVarFor(routing, name)
		key := c.vars[kv]
		if prefixes, ok := dig(routing, "spec", "providers", name, "credential", "forbiddenKeyPrefixes").([]any); ok {
			for _, pf := range prefixes {
				if s := str(pf); s != "" && strings.HasPrefix(key, s) {
					return failRow("PF-19", "MODEL_KEY_FORBIDDEN_PREFIX", exitPreflight,
						"%s: the key starts with the forbidden prefix %q (subscription or OAuth tokens are not API keys)", kv, s)
				}
			}
		}
	}
	for _, name := range providers {
		kv, _ := keyVarFor(routing, name)
		if perr := c.deps.ProbeModelKey(name, c.vars[kv]); perr != nil {
			return failRow("PF-19", "MODEL_KEY_REJECTED", exitPreflight, "%s: the authenticated probe failed: %v", name, perr)
		}
	}
	if len(providers) == 0 {
		return pass("PF-19", "no enabled production model provider has a key to probe")
	}
	return pass("PF-19", "%d model key(s) accepted: %s", len(providers), strings.Join(providers, ", "))
}

func amount(v any) (float64, bool) {
	f, err := strconv.ParseFloat(strings.TrimSpace(str(v)), 64)
	return f, err == nil && !math.IsNaN(f) && !math.IsInf(f, 0)
}

func (c *pfCtx) pf26() row {
	budgets, digest, err := c.loadPolicy("budgets.yaml")
	if err != nil {
		return failRow("PF-26", err.Code, exitPreflight, "%s", err.Message)
	}
	monthly, ok1 := amount(dig(budgets, "spec", "global", "monthlyUsd"))
	_, ok2 := amount(dig(budgets, "spec", "global", "dailyUsd"))
	if !ok1 || !ok2 {
		return failRow("PF-26", "BUDGET_POLICY_INVALID", exitPreflight, "policy/budgets.yaml lacks spec.global.monthlyUsd or dailyUsd")
	}
	summary := fmt.Sprintf("model budget: global monthly %s USD, daily %s USD", str(dig(budgets, "spec", "global", "monthlyUsd")), str(dig(budgets, "spec", "global", "dailyUsd")))
	if pm, ok := dig(budgets, "spec", "providers").(map[string]any); ok {
		var names []string
		for n := range pm {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			if m := str(dig(pm[n], "monthlyUsd")); m != "" {
				summary += fmt.Sprintf("; %s monthly %s", n, m)
			}
		}
	}
	given := c.inv.Confirm["budget"]
	if given == "" {
		given = c.env["SC_CONFIRM_BUDGET"]
	}
	if given == "" {
		return failRow("PF-26", "BUDGET_CONFIRMATION_MISSING", exitConfirm,
			"%s; confirm it with --confirm-budget %s or SC_CONFIRM_BUDGET (typed confirmation: budget %s %s)", summary, str(dig(budgets, "spec", "global", "monthlyUsd")), c.cluster, str(dig(budgets, "spec", "global", "monthlyUsd")))
	}
	g, ok := amount(given)
	if !ok || math.Abs(g-monthly) > 1e-9 {
		return failRow("PF-26", "BUDGET_CONFIRMATION_MISMATCH", exitConfirm, "%s; the confirmed amount %q does not equal global.monthlyUsd", summary, given)
	}
	if c.deps.Record != nil && !c.deps.ReadOnly {
		c.deps.Record(Confirmation{Kind: "model-budget", SubjectDigest: digest, Method: "flag"})
	}
	return pass("PF-26", "%s; confirmed", summary)
}

func (c *pfCtx) pf28() row {
	vars, err := preflight.EgressVars(c.vars["S3_ENDPOINT"], c.vars["S3_PRIVATE_CIDR"], c.deps.Resolve)
	if err != nil {
		code := "S3_ENDPOINT_INVALID"
		var pe *preflight.Error
		if errors.As(err, &pe) {
			code = pe.Code
		}
		return failRow("PF-28", code, exitPreflight, "%v", err)
	}
	if len(vars) == 0 {
		return pass("PF-28", "S3 endpoint is public on 443: no SC_S3_CIDR or SC_S3_PORT")
	}
	return pass("PF-28", "SC_S3_CIDR=%s SC_S3_PORT=%s", vars["SC_S3_CIDR"], vars["SC_S3_PORT"])
}

func (c *pfCtx) pf30() row {
	cidr := str(dig(c.doc, "spec", "network", "serviceCidr"))
	dns := c.deps.ClusterDNSIP
	if !dns.IsValid() {
		dns, _ = preflight.ClusterDNSIP(cidr) // a CIDR error resurfaces from CheckPinnedIPs
	}
	var pinned []netip.Addr
	if dns.IsValid() {
		pinned = append(pinned, dns)
	}
	ip, err := preflight.CheckPinnedIPs(cidr, pinned)
	if err != nil {
		code := "SERVICE_CIDR_INVALID"
		var pe *preflight.Error
		if errors.As(err, &pe) {
			code = pe.Code
		}
		return failRow("PF-30", code, exitPreflight, "%v", err)
	}
	return pass("PF-30", "SC_TRAEFIK_CLUSTER_IP=%s (SERVICE_CIDR %s)", ip, cidr)
}

type attestation struct {
	Limit float64
	MFA   bool
}

// pf29 is the provider spend backstop check (BUD-B6, plan 33 section 9.2a).
func (c *pfCtx) pf29() row {
	routing, _, err := c.loadPolicy("model-routing.yaml")
	if err != nil {
		return failRow("PF-29", err.Code, exitPreflight, "%s", err.Message)
	}
	budgets, _, err := c.loadPolicy("budgets.yaml")
	if err != nil {
		return failRow("PF-29", err.Code, exitPreflight, "%s", err.Message)
	}
	required := enabledProviders(routing)
	if len(required) == 0 {
		return pass("PF-29", "no enabled production model provider needs a backstop")
	}
	recordPath := filepath.Join(c.repo, "gitops", "clusters", c.cluster, "config", "provider-backstops.yaml")
	existing, _ := readAttestations(recordPath)
	fileArg := c.inv.Confirm["backstops"]
	if fileArg == "" {
		fileArg = c.env["SC_CONFIRM_BACKSTOPS"]
	}
	var given map[string]attestation
	var fileDigest string
	if fileArg != "" {
		b, rerr := os.ReadFile(fileArg)
		if rerr != nil {
			return failRow("PF-29", "BACKSTOP_FILE_UNREADABLE", exitConfirm, "--confirm-backstops %s cannot be read", fileArg)
		}
		var perr error
		if given, perr = parseAttestations(b); perr != nil {
			return failRow("PF-29", "BACKSTOP_FILE_INVALID", exitConfirm, "%s: %v", fileArg, perr)
		}
		sum := sha256.Sum256(b)
		fileDigest = "sha256:" + hex.EncodeToString(sum[:])
	}
	var missing []string
	fresh := map[string]attestation{}
	for _, name := range required {
		monthly, ok := amount(dig(budgets, "spec", "providers", name, "monthlyUsd"))
		if !ok {
			return failRow("PF-29", "BUDGET_POLICY_INVALID", exitPreflight, "policy/budgets.yaml has no spec.providers.%s.monthlyUsd", name)
		}
		a, fromFile := given[name]
		if !fromFile {
			a, ok = existing[name]
			if !ok {
				missing = append(missing, name)
				continue
			}
		}
		if !a.MFA {
			return failRow("PF-29", "BACKSTOP_MFA_ATTESTATION_MISSING", exitConfirm, "%s: the attestation lacks consoleMfaEnabled: true (MFA must be enabled on the provider console account)", name)
		}
		if a.Limit <= 0 {
			return failRow("PF-29", "BACKSTOP_LIMIT_INVALID", exitConfirm, "%s: limit %s USD must be greater than zero", name, strconv.FormatFloat(a.Limit, 'f', -1, 64))
		}
		if a.Limit > 1.25*monthly+1e-9 {
			return failRow("PF-29", "BACKSTOP_LIMIT_TOO_HIGH", exitConfirm, "%s: limit %s USD exceeds 1.25 x monthlyUsd (%s USD)", name,
				strconv.FormatFloat(a.Limit, 'f', -1, 64), strconv.FormatFloat(1.25*monthly, 'f', -1, 64))
		}
		if fromFile {
			fresh[name] = a
		}
	}
	if len(missing) > 0 {
		return failRow("PF-29", "BACKSTOP_MISSING", exitConfirm,
			"no spend backstop attested for %s; pass --confirm-backstops <file> or set SC_CONFIRM_BACKSTOPS to a file that lists providers.<id>.limitUsd and providers.<id>.consoleMfaEnabled: true (the limit may be at most 1.25 x monthlyUsd and the provider console needs MFA)",
			strings.Join(missing, ", "))
	}
	changed := false
	for k, v := range fresh {
		if e, ok := existing[k]; !ok || e.Limit != v.Limit || e.MFA != v.MFA {
			changed = true
		}
	}
	// An unchanged attestation is a no-op (plan 50 section 17.1 rule 3): no rewrite, no second confirmation.
	if changed && !c.deps.ReadOnly {
		merged := map[string]attestation{}
		for k, v := range existing {
			merged[k] = v
		}
		for k, v := range fresh {
			merged[k] = v
		}
		if werr := writeAttestations(recordPath, merged, c.deps.Clock.Now()); werr != nil {
			return failRow("PF-29", "BACKSTOP_RECORD_UNWRITABLE", exitPreflight, "%v", werr)
		}
		if c.deps.Record != nil {
			c.deps.Record(Confirmation{Kind: "provider-backstop", SubjectDigest: fileDigest, Method: "flag"})
		}
	}
	return pass("PF-29", "spend backstop attested for %s", strings.Join(required, ", "))
}

func parseAttestations(b []byte) (map[string]attestation, error) {
	var doc struct {
		Providers map[string]struct {
			Limit any  `yaml:"limitUsd"`
			MFA   bool `yaml:"consoleMfaEnabled"`
		} `yaml:"providers"`
	}
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return nil, err
	}
	out := map[string]attestation{}
	for k, v := range doc.Providers {
		l, ok := amount(v.Limit)
		if !ok {
			return nil, fmt.Errorf("providers.%s.limitUsd is not a number", k)
		}
		out[k] = attestation{Limit: l, MFA: v.MFA}
	}
	return out, nil
}

func readAttestations(path string) (map[string]attestation, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return parseAttestations(b)
}

func writeAttestations(path string, m map[string]attestation, now time.Time) error {
	providers := map[string]any{}
	for k, v := range m {
		providers[k] = map[string]any{"limitUsd": v.Limit, "consoleMfaEnabled": v.MFA, "attestedAt": now.UTC().Format(time.RFC3339)}
	}
	b, err := yaml.Marshal(map[string]any{"providers": providers})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// --- production defaults

func lookupHost(host string) ([]netip.Addr, error) {
	return net.DefaultResolver.LookupNetIP(context.Background(), "ip", host)
}

// policyProbe builds the production PF-19 probe from policy/model-routing.yaml when the probe runs (D7). The
// endpoint (baseUrl + /models) is not defined by plan 53: (verify).
func policyProbe(_ map[string]string, path string) func(provider, key string) error {
	return func(provider, key string) error {
		b, err := os.ReadFile(path)
		if err != nil {
			return errors.New("policy/model-routing.yaml cannot be read")
		}
		doc, err := contracts.ParseYAML(b)
		if err != nil {
			return errors.New("policy/model-routing.yaml is not valid YAML")
		}
		targets := map[string]ProbeTarget{}
		pm, _ := dig(doc, "spec", "providers").(map[string]any)
		for name, pol := range pm {
			targets[name] = ProbeTarget{BaseURL: str(dig(pol, "upstream", "baseUrl")), AuthScheme: str(dig(pol, "credential", "authScheme"))}
		}
		return NewModelKeyProbe(&http.Client{Timeout: 20 * time.Second}, targets)(provider, key)
	}
}

// schemaErrText names the failing field of a schema validation error instead of the bare document root.
func schemaErrText(err error) string {
	var ve *jsonschema.ValidationError
	if errors.As(err, &ve) {
		var leaves []string
		var walk func(e *jsonschema.ValidationError)
		walk = func(e *jsonschema.ValidationError) {
			if len(e.Causes) == 0 {
				leaves = append(leaves, "/"+strings.Join(e.InstanceLocation, "/")+": "+e.ErrorKind.LocalizedString(message.NewPrinter(language.English)))
				return
			}
			for _, c := range e.Causes {
				walk(c)
			}
		}
		walk(ve)
		if len(leaves) > 0 {
			return strings.Join(leaves, "; ")
		}
	}
	return err.Error()
}
