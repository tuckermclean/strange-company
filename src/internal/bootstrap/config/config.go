// Package config normalizes bootstrap.env into a BootstrapConfig (plan 50 section 9).
package config

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"strange.company/sc/internal/clock"
)

// Row is one row of the plan 50 section 9.2 variable table. Vars lists the variable names of the row in table
// order; names with a placeholder (S3_CRED_<INSTANCE>_...) keep the placeholder.
type Row struct {
	Vars        []string
	Required    string
	Default     string
	Field       string
	SecretClass string
}

// Options are the inputs of Normalize that do not come from bootstrap.env.
type Options struct {
	Clock      clock.Clock
	InstallUID string
	Version    string
	// Profile is the --profile flag; it overrides PROFILE.
	Profile string
}

// Result is a normalized BootstrapConfig: the YAML document and its parsed form.
type Result struct {
	YAML []byte
	Doc  map[string]any
}

// Error is a configuration refusal; ExitCode is the plan 50 section 7.2 code.
type Error struct {
	Code     string
	ExitCode int
	Message  string
}

// Error implements error.
func (e *Error) Error() string { return e.Code + ": " + e.Message }

func fail(code, format string, a ...any) *Error {
	return &Error{Code: code, ExitCode: 2, Message: fmt.Sprintf(format, a...)}
}

// Rows returns the section 9.2 table in plan order.
func Rows() []Row {
	out := make([]Row, len(rows))
	copy(out, rows)
	return out
}

var envLine = regexp.MustCompile(`^[A-Z][A-Z0-9_]*=`)

// ParseEnv reads the plan 50 section 9.1 grammar with the same semantics and the same scanner limits (default 64 KiB line) as the toolbox parser (CD-67): KEY=VALUE
// lines matching ^[A-Z][A-Z0-9_]*=, one pair of single or double quotes stripped, no expansion, other lines assign
// nothing, the last assignment wins and an empty value counts as unset.
func ParseEnv(data []byte) (map[string]string, error) {
	out := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := sc.Text()
		if !envLine.MatchString(line) {
			continue
		}
		k, v, _ := strings.Cut(line, "=")
		v = strings.Trim(v, " \t\r\n\v\f")
		if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
			v = v[1 : len(v)-1]
		}
		if v != "" {
			out[k] = v
		} else {
			delete(out, k)
		}
	}
	return out, sc.Err()
}

type norm struct {
	env  map[string]string
	errs []string
	miss []string
}

func (n *norm) get(k string) string { return n.env[k] }

func (n *norm) def(k, d string) string {
	if v := n.env[k]; v != "" {
		return v
	}
	return d
}

func (n *norm) need(k string) string {
	v := n.env[k]
	if v == "" {
		n.miss = append(n.miss, k)
	}
	return v
}

func (n *norm) bad(format string, a ...any) { n.errs = append(n.errs, fmt.Sprintf(format, a...)) }

func (n *norm) enum(k, d string, allowed ...string) string {
	v := n.def(k, d)
	for _, a := range allowed {
		if v == a {
			return v
		}
	}
	if v != "" {
		n.bad("%s=%q is not one of %s", k, v, strings.Join(allowed, ", "))
	}
	return v
}

func (n *norm) boolean(k string) bool {
	switch v := n.env[k]; v {
	case "", "false":
		return false
	case "true":
		return true
	default:
		n.bad("%s=%q is not true or false", k, v)
		return false
	}
}

func (n *norm) match(k string, re *regexp.Regexp, what string) string {
	v := n.env[k]
	if v != "" && !re.MatchString(v) {
		n.bad("%s=%q is not %s", k, v, what)
	}
	return v
}

func (n *norm) match2(k, d string, re *regexp.Regexp, what string) string {
	v := n.def(k, d)
	if !re.MatchString(v) {
		n.bad("%s=%q is not %s", k, v, what)
	}
	return v
}

func secretRef(env, class string) map[string]any {
	return map[string]any{"env": env, "secretClass": class}
}

// secret returns the secretRef of a required secret variable.
func (n *norm) secret(k, class string) map[string]any {
	n.need(k)
	return secretRef(k, class)
}

// optSecret returns the secretRef of an optional secret variable, or nil when it is unset.
func (n *norm) optSecret(k, class string) map[string]any {
	if n.env[k] == "" {
		return nil
	}
	return secretRef(k, class)
}

var (
	reClusterName = regexp.MustCompile(`^[a-z][a-z0-9-]{1,30}[a-z0-9]$`)
	reDomain      = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$`)
	reProfile     = regexp.MustCompile(`^[a-z][a-z0-9-]{1,61}[a-z0-9]$`)
	reAmount      = regexp.MustCompile(`^(0|[1-9][0-9]{0,6})(\.[0-9]{1,2})?$`)
	reCurrency    = regexp.MustCompile(`^[A-Z]{3}$`)
	reHex32       = regexp.MustCompile(`^[0-9a-f]{32}$`)
	reCIDR        = regexp.MustCompile(`^([0-9]{1,3}\.){3}[0-9]{1,3}/[0-9]{1,2}$`)
	reHTTPS       = regexp.MustCompile(`^https://`)
	rePrefix      = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,40}[a-z0-9]$`)
	reRepo        = regexp.MustCompile(`^[A-Za-z0-9-]{1,39}/[A-Za-z0-9._-]{1,100}$`)
	reNode        = regexp.MustCompile(`^node-[0-9]{2}$`)
	reUser        = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)
	reFingerprint = regexp.MustCompile(`^SHA256:[A-Za-z0-9+/]{43}$`)
	reWindow      = regexp.MustCompile(`^(Mon|Tue|Wed|Thu|Fri|Sat|Sun) (([01][0-9]|2[0-3]):[0-5][0-9])-(([01][0-9]|2[0-3]):[0-5][0-9])$`)
	reSubsidiary  = regexp.MustCompile(`^[A-Z]{2,4}$`)
	reCred        = regexp.MustCompile(`^S3_CRED_([A-Z0-9_]+)_(ACCESS_KEY_ID|SECRET_ACCESS_KEY)$`)
	reEmail       = regexp.MustCompile(`^[^@\s]+@[^@\s]+$`)
)

// Normalize turns a parsed bootstrap.env into a BootstrapConfig (plan 50 section 9.2). It never copies a secret
// value: every secret becomes a secretRef naming its variable. All problems are reported at once.
func Normalize(env map[string]string, opt Options) (*Result, error) {
	clk := opt.Clock
	if clk == nil {
		clk = clock.Real{}
	}
	e := map[string]string{}
	for k, v := range env {
		if v != "" {
			e[k] = v
		}
	}
	n := &norm{env: e}

	provider := e["PROVIDER"]
	if provider != "" && provider != "existing" && provider != "fake" && provider != "ovh" {
		return nil, fail("CONFIG_INVALID", "PROVIDER=%q is not one of ovh, existing", provider)
	}
	if provider == "fake" && e["SC_TEST_MODE"] != "1" {
		return nil, fail("CONFIG_INVALID", "PROVIDER=fake needs SC_TEST_MODE=1")
	}
	if provider == "ovh" {
		for _, k := range testOnly {
			if e[k] != "" {
				return nil, fail("TEST_MODE_REFUSED_PAID", "%s is a test-only variable and is refused with the paid provider %s", k, provider)
			}
		}
	}
	if opt.InstallUID == "" {
		return nil, fail("CONFIG_INVALID", "no install uid supplied")
	}
	n.need("PROVIDER")

	name := n.match("CLUSTER_NAME", reClusterName, "a cluster name (lowercase, 3-32 characters)")
	n.need("CLUSTER_NAME")
	profile := n.def("PROFILE", "self-hosting-minimum")
	if opt.Profile != "" {
		profile = opt.Profile
	}
	if !reProfile.MatchString(profile) {
		n.bad("profile %q is not a profile name", profile)
	}
	cluster := map[string]any{
		"name":       name,
		"baseDomain": n.match("BASE_DOMAIN", reDomain, "a lowercase domain name"),
		"adminEmail": e["ADMIN_EMAIL"],
		"profile":    profile,
	}
	n.need("BASE_DOMAIN")
	if n.need("ADMIN_EMAIL") != "" && !reEmail.MatchString(e["ADMIN_EMAIL"]) {
		n.bad("ADMIN_EMAIL=%q is not an email address", e["ADMIN_EMAIL"])
	}

	// provider
	count := 0
	if v := n.need("EXPECTED_NODE_COUNT"); v != "" {
		c, err := strconv.Atoi(v)
		if err != nil || c < 1 || c > 9 {
			n.bad("EXPECTED_NODE_COUNT=%q is not an integer from 1 to 9", v)
		}
		count = c
	}
	prov := map[string]any{"name": provider, "expectedNodeCount": count}
	cost := n.need("MAX_MONTHLY_COST")
	if cost != "" && !reAmount.MatchString(cost) {
		n.bad("MAX_MONTHLY_COST=%q is not an amount such as 25 or 25.00", cost)
	}
	prov["maxMonthlyCost"] = map[string]any{"amount": cost, "currency": n.match2("COST_CURRENCY", "EUR", reCurrency, "a three-letter currency code")}
	switch provider {
	case "ovh":
		prov["region"] = n.need("PROVIDER_REGION")
		prov["machineClass"] = n.need("MACHINE_CLASS")
		ovh := map[string]any{
			"endpoint":    n.enum("OVH_ENDPOINT", "ovh-eu", "ovh-eu", "ovh-ca", "ovh-us"),
			"productLine": n.enum("OVH_PRODUCT_LINE", "vps", "vps", "public-cloud"),
			"credentials": map[string]any{
				"applicationKey":    n.secret("OVH_APPLICATION_KEY", "SC-INFRA-PROVIDER"),
				"applicationSecret": n.secret("OVH_APPLICATION_SECRET", "SC-INFRA-PROVIDER"),
				"consumerKey":       n.secret("OVH_CONSUMER_KEY", "SC-INFRA-PROVIDER"),
			},
		}
		if v := n.match("OVH_SUBSIDIARY", reSubsidiary, "a billing subsidiary code"); v != "" {
			ovh["subsidiary"] = v
		}
		if ovh["productLine"] == "public-cloud" {
			n.need("OVH_CLOUD_PROJECT_ID")
		}
		if v := n.match("OVH_CLOUD_PROJECT_ID", reHex32, "32 lowercase hex digits"); v != "" {
			ovh["cloudProjectId"] = v
		}
		prov["ovh"] = ovh
	case "existing":
		if v := e["PROVIDER_REGION"]; v != "" {
			prov["region"] = v
		}
		if v := e["MACHINE_CLASS"]; v != "" {
			prov["machineClass"] = v
		}
		prov["existing"] = map[string]any{"nodes": n.nodes()}
	}

	ssh := map[string]any{
		"privateKey":    map[string]any{"file": "/run/sc/ssh/id_key", "secretClass": "SC-SSH-OPERATOR"},
		"publicKeyPath": "/run/sc/ssh/id_key.pub",
		"useAgent":      n.boolean("SSH_USE_AGENT"),
		"allowedCidrs":  n.cidrList("SSH_ALLOWED_CIDRS"),
	}

	mode := n.enum("INGRESS_MODE", "tunnel", "tunnel", "direct")
	dns := n.enum("DNS_PROVIDER", "cloudflare", "cloudflare", "manual")
	solver := n.enum("ACME_SOLVER", "dns01-cloudflare", "dns01-cloudflare", "dns01-rfc2136", "dns01-route53", "http01")
	if mode == "tunnel" && dns != "cloudflare" {
		n.bad("DNS_PROVIDER=%s is only allowed in direct ingress mode", dns)
	}
	acme := map[string]any{"solver": solver}
	if solver == "dns01-cloudflare" {
		acme["apiToken"] = n.secret("CLOUDFLARE_ACME_TOKEN", "SC-DNS-ACME")
	} else if r := n.optSecret("CLOUDFLARE_ACME_TOKEN", "SC-DNS-ACME"); r != nil {
		acme["apiToken"] = r
	}
	ingress := map[string]any{"mode": mode, "dnsProvider": dns, "acme": acme}
	if mode == "tunnel" || dns == "cloudflare" || solver == "dns01-cloudflare" {
		cf := map[string]any{
			"accountId": n.match("CLOUDFLARE_ACCOUNT_ID", reHex32, "32 lowercase hex digits"),
			"apiToken":  n.secret("CLOUDFLARE_API_TOKEN", "SC-DNS-API"),
		}
		n.need("CLOUDFLARE_ACCOUNT_ID")
		if v := n.match("CLOUDFLARE_ZONE_ID", reHex32, "32 lowercase hex digits"); v != "" {
			cf["zoneId"] = v
		}
		ingress["cloudflare"] = cf
	}

	network := map[string]any{
		"podCidr":     n.match2("POD_CIDR", "10.42.0.0/16", reCIDR, "an IPv4 CIDR"),
		"serviceCidr": n.match2("SERVICE_CIDR", "10.43.0.0/16", reCIDR, "an IPv4 CIDR"),
	}
	node := map[string]any{"maintenanceWindow": n.window(), "sshIpv6": n.boolean("NODE_SSH_IPV6")}
	s3 := n.objectStorage(name)

	mirror := map[string]any{
		"repository":     n.match("GITHUB_MIRROR_REPO", reRepo, "an owner/name repository"),
		"token":          n.secret("GITHUB_MIRROR_TOKEN", "SC-GITHUB-MIRROR"),
		"allowPublic":    n.boolean("GITHUB_MIRROR_ALLOW_PUBLIC"),
		"protectionTier": n.enum("GITHUB_MIRROR_PROTECTION_TIER", "rulesets", "rulesets", "unavailable-accepted"),
	}
	n.need("GITHUB_MIRROR_REPO")
	if mirror["allowPublic"] == true {
		if r := n.optSecret("GITHUB_PROBE_TOKEN", "SC-GITHUB-PROBE"); r != nil {
			mirror["probeToken"] = r
		}
		if r := n.optSecret("GITHUB_MIRROR_READ_TOKEN", "SC-GITHUB-MIRROR-READ"); r != nil {
			mirror["readToken"] = r
		}
	} else {
		mirror["probeToken"] = n.secret("GITHUB_PROBE_TOKEN", "SC-GITHUB-PROBE")
		mirror["readToken"] = n.secret("GITHUB_MIRROR_READ_TOKEN", "SC-GITHUB-MIRROR-READ")
	}

	notif := n.notifications()
	models := n.models()

	if len(n.miss) > 0 {
		return nil, fail("CONFIG_REQUIRED_MISSING", "missing or empty required variables: %s", strings.Join(dedupe(n.miss), ", "))
	}
	if len(n.errs) > 0 {
		return nil, fail("CONFIG_INVALID", "%s", strings.Join(n.errs, "; "))
	}

	doc := map[string]any{
		"apiVersion": "strange.company/v1alpha1",
		"kind":       "BootstrapConfig",
		"metadata": map[string]any{
			"name":        name,
			"installUid":  opt.InstallUID,
			"generatedAt": clk.Now().UTC().Format(time.RFC3339),
			"generatedBy": opt.Version,
			"envDigest":   envDigest(e, profile),
		},
		"spec": map[string]any{
			"cluster": cluster, "provider": prov, "ssh": ssh, "ingress": ingress, "network": network, "node": node,
			"objectStorage": s3, "githubMirror": mirror, "notifications": notif, "models": models,
		},
	}
	y, err := yaml.Marshal(doc)
	if err != nil {
		return nil, fail("CONFIG_INVALID", "marshal: %v", err)
	}
	return &Result{YAML: y, Doc: doc}, nil
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func (n *norm) cidrList(k string) []any {
	out := []any{}
	for _, c := range strings.Split(n.env[k], ",") {
		if c = strings.TrimSpace(c); c != "" {
			if !reCIDR.MatchString(c) {
				n.bad("%s entry %q is not an IPv4 CIDR", k, c)
			}
			out = append(out, c)
		}
	}
	return out
}

func (n *norm) window() map[string]any {
	v := n.def("NODE_MAINTENANCE_WINDOW", "Sun 04:00-06:00")
	m := reWindow.FindStringSubmatch(v)
	if m == nil {
		n.bad("NODE_MAINTENANCE_WINDOW=%q is not <Mon..Sun> HH:MM-HH:MM", v)
		return nil
	}
	return map[string]any{"weekday": m[1], "start": m[2], "end": m[4], "tz": "UTC"}
}

// nodes parses EXISTING_NODES and EXISTING_NODE_OVERRIDES.
func (n *norm) nodes() []any {
	raw := n.need("EXISTING_NODES")
	byName := map[string]map[string]any{}
	var out []any
	for _, ent := range strings.Split(raw, ",") {
		ent = strings.TrimSpace(ent)
		if ent == "" {
			continue
		}
		nm, rest, ok := strings.Cut(ent, "=")
		user, hostpart, ok2 := strings.Cut(rest, "@")
		if !ok || !ok2 || !reNode.MatchString(nm) || !reUser.MatchString(user) {
			n.bad("EXISTING_NODES entry %q is not node-NN=user@host[:port][/internal_ip]", ent)
			continue
		}
		hostport, internal, _ := strings.Cut(hostpart, "/")
		host, port := hostport, 22
		if strings.Count(hostport, ":") == 1 {
			h, p, _ := strings.Cut(hostport, ":")
			pi, err := strconv.Atoi(p)
			if err != nil || pi < 1 || pi > 65535 {
				n.bad("EXISTING_NODES entry %q has an invalid port", ent)
				continue
			}
			host, port = h, pi
		}
		if host == "" {
			n.bad("EXISTING_NODES entry %q has no host", ent)
			continue
		}
		nd := map[string]any{"name": nm, "ssh": map[string]any{"host": host, "port": port, "user": user}}
		if internal != "" {
			nd["internalIp"] = internal
		}
		byName[nm] = nd
		out = append(out, nd)
	}
	for _, ov := range strings.Split(n.env["EXISTING_NODE_OVERRIDES"], ";") {
		ov = strings.TrimSpace(ov)
		if ov == "" {
			continue
		}
		nm, kv, _ := strings.Cut(ov, ":")
		k, v, ok := strings.Cut(kv, "=")
		nd := byName[nm]
		switch {
		case !ok || nd == nil:
			n.bad("EXISTING_NODE_OVERRIDES entry %q names no node of EXISTING_NODES or is malformed", ov)
		case k == "disk_class" && (v == "ssd" || v == "nvme"):
			nd["diskClassOverride"] = v
		case k == "host_key" && reFingerprint.MatchString(v):
			nd["hostKeyFingerprint"] = v
		default:
			n.bad("EXISTING_NODE_OVERRIDES entry %q is not disk_class=ssd|nvme or host_key=SHA256:...", ov)
		}
	}
	return out
}

func (n *norm) objectStorage(cluster string) map[string]any {
	n.need("S3_ENDPOINT")
	n.need("S3_REGION")
	s3prov := n.enum("S3_PROVIDER", "generic", "aws", "ovh", "b2", "generic")
	credDefault := "shared"
	if s3prov == "aws" || s3prov == "ovh" {
		credDefault = "scoped"
	}
	prefix := n.def("S3_BUCKET_PREFIX", "sc-"+cluster)
	if !rePrefix.MatchString(prefix) {
		n.bad("bucket prefix %q is not a valid bucket name prefix", prefix)
	}
	s3 := map[string]any{
		"endpoint":              n.match("S3_ENDPOINT", reHTTPS, "an https:// URL"),
		"region":                n.get("S3_REGION"),
		"bucketPrefix":          prefix,
		"forcePathStyle":        n.boolean("S3_FORCE_PATH_STYLE"),
		"bucketMode":            n.enum("S3_BUCKET_MODE", "create", "create", "existing"),
		"provider":              s3prov,
		"credentialMode":        n.enum("S3_CREDENTIAL_MODE", credDefault, "scoped", "shared"),
		"objectLock":            n.enum("S3_OBJECT_LOCK", "auto", "auto", "required", "off"),
		"allowWeakProtection":   n.boolean("S3_ALLOW_WEAK_PROTECTION"),
		"allowUnprotectedState": n.boolean("S3_ALLOW_UNPROTECTED_STATE"),
		"credentials": map[string]any{
			"accessKeyId":     n.secret("S3_ACCESS_KEY_ID", "SC-OBJECT-STORAGE"),
			"secretAccessKey": n.secret("S3_SECRET_ACCESS_KEY", "SC-OBJECT-STORAGE"),
		},
	}
	if v := n.match("S3_PRIVATE_CIDR", reCIDR, "an IPv4 CIDR"); v != "" {
		s3["privateCidr"] = v
	}
	if n.get("S3_STATE_ACCESS_KEY_ID") != "" || n.get("S3_STATE_SECRET_ACCESS_KEY") != "" || n.get("S3_STATE_ENDPOINT") != "" {
		ep := n.def("S3_STATE_ENDPOINT", n.get("S3_ENDPOINT"))
		if !reHTTPS.MatchString(ep) {
			n.bad("S3_STATE_ENDPOINT=%q is not an https:// URL", ep)
		}
		s3["stateAccount"] = map[string]any{
			"endpoint":        ep,
			"accessKeyId":     n.secret("S3_STATE_ACCESS_KEY_ID", "SC-OBJECT-STORAGE"),
			"secretAccessKey": n.secret("S3_STATE_SECRET_ACCESS_KEY", "SC-OBJECT-STORAGE"),
		}
	}
	// S3_CRED_<INSTANCE>_*: instance names are upper-case with '-' mapped to '_'.
	inst := map[string]bool{}
	for k := range n.env {
		if m := reCred.FindStringSubmatch(k); m != nil {
			inst[m[1]] = true
		}
	}
	var names []string
	for i := range inst {
		names = append(names, i)
	}
	sort.Strings(names)
	var creds []any
	for _, i := range names {
		creds = append(creds, map[string]any{
			"instance":        strings.ToLower(strings.ReplaceAll(i, "_", "-")),
			"accessKeyId":     n.secret("S3_CRED_"+i+"_ACCESS_KEY_ID", "SC-OBJECT-STORAGE"),
			"secretAccessKey": n.secret("S3_CRED_"+i+"_SECRET_ACCESS_KEY", "SC-OBJECT-STORAGE"),
		})
	}
	if creds != nil {
		s3["instanceCredentials"] = creds
	}
	return s3
}

func (n *norm) notifications() map[string]any {
	transport := n.enum("NOTIFY_TRANSPORT", "", "email", "webhook", "ntfy", "slack", "discord", "matrix")
	n.need("NOTIFY_TRANSPORT")
	out := map[string]any{"transport": transport, "heartbeatUrl": nil}
	if r := n.optSecret("HEARTBEAT_URL", "SC-NOTIFY-TRANSPORT"); r != nil {
		out["heartbeatUrl"] = r
	}
	if transport == "email" {
		port := 587
		if v := n.get("NOTIFY_SMTP_PORT"); v != "" {
			p, err := strconv.Atoi(v)
			if err != nil || p < 1 || p > 65535 {
				n.bad("NOTIFY_SMTP_PORT=%q is not a port", v)
			}
			port = p
		}
		var to []any
		for _, a := range strings.Split(n.need("NOTIFY_SMTP_TO"), ",") {
			if a = strings.TrimSpace(a); a != "" {
				if !reEmail.MatchString(a) {
					n.bad("NOTIFY_SMTP_TO entry %q is not an email address", a)
				}
				to = append(to, a)
			}
		}
		out["email"] = map[string]any{
			"smtpHost": n.need("NOTIFY_SMTP_HOST"), "smtpPort": port, "from": n.need("NOTIFY_SMTP_FROM"), "to": to,
			"username": n.secret("NOTIFY_SMTP_USERNAME", "SC-NOTIFY-TRANSPORT"),
			"password": n.secret("NOTIFY_SMTP_PASSWORD", "SC-NOTIFY-TRANSPORT"),
		}
		if r := n.optSecret("NOTIFY_WEBHOOK_URL", "SC-NOTIFY-TRANSPORT"); r != nil {
			out["webhookUrl"] = r
		}
	} else if transport != "" {
		out["webhookUrl"] = n.secret("NOTIFY_WEBHOOK_URL", "SC-NOTIFY-TRANSPORT")
	}
	if r := n.optSecret("NOTIFY_NTFY_TOKEN", "SC-NOTIFY-TRANSPORT"); r != nil {
		out["ntfyToken"] = r
	}
	return out
}

func (n *norm) models() map[string]any {
	var ps []any
	for _, p := range [][2]string{{"anthropic", "ANTHROPIC_API_KEY"}, {"openai", "OPENAI_API_KEY"}, {"deepseek", "DEEPSEEK_API_KEY"},
		{"zai", "ZAI_API_KEY"}, {"google", "GOOGLE_API_KEY"}} {
		if n.get(p[1]) != "" {
			ps = append(ps, map[string]any{"name": p[0], "apiKey": secretRef(p[1], "SC-MODEL-API-KEYS")})
		}
	}
	if n.get("OPENAI_COMPATIBLE_API_KEY") != "" {
		ent := map[string]any{"name": "openai-compatible", "apiKey": secretRef("OPENAI_COMPATIBLE_API_KEY", "SC-MODEL-API-KEYS")}
		if u := n.need("OPENAI_COMPATIBLE_BASE_URL"); u != "" {
			ent["baseUrl"] = u
		}
		ps = append(ps, ent)
	}
	if len(ps) == 0 {
		n.miss = append(n.miss, "ANTHROPIC_API_KEY (or another model API key)")
	}
	return map[string]any{"providers": ps}
}

// secretVar reports whether the variable carries a secret value (it is a secretRef target): such a variable is
// excluded from the digest and must stay empty in the example.
func secretVar(k string) bool {
	if k == "SSH_PRIVATE_KEY_PATH" || k == "S3_STATE_ENDPOINT" {
		return false
	}
	if reCred.MatchString(k) {
		return true
	}
	for _, r := range rows {
		if r.SecretClass == "" {
			continue
		}
		for _, v := range r.Vars {
			if v == k {
				return true
			}
		}
	}
	return false
}

func isRuntime(k string) bool {
	for _, r := range runtimeOnly {
		if r == k {
			return true
		}
	}
	return false
}

// envDigest hashes the canonical non-secret variable set: known variables only, no secrets, no runtime-only
// variables, no host-specific key paths, plus the effective profile.
func envDigest(env map[string]string, profile string) string {
	known := knownKeys()
	var lines []string
	for k, v := range env {
		if !known[k] && !reCred.MatchString(k) {
			continue
		}
		if secretVar(k) || isRuntime(k) || k == "SSH_PRIVATE_KEY_PATH" || k == "SSH_PUBLIC_KEY_PATH" || k == "PROFILE" {
			continue
		}
		lines = append(lines, k+"="+v)
	}
	lines = append(lines, "PROFILE="+profile)
	sort.Strings(lines)
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n") + "\n"))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func knownKeys() map[string]bool {
	m := map[string]bool{}
	for _, r := range rows {
		for _, v := range r.Vars {
			m[v] = true
		}
	}
	for _, v := range runtimeOnly {
		m[v] = true
	}
	return m
}

// ExampleEnv renders bootstrap.env.example from the table: one comment block and one empty assignment per row
// variable, so copying it unchanged configures nothing (an empty value counts as unset).
func ExampleEnv() []byte {
	var b strings.Builder
	b.WriteString("# bootstrap.env.example - generated from plan 50 section 9.2 by `sc-lint config-docs --write`.\n")
	b.WriteString("# Do not edit by hand: `make config-docs-check` fails when this file drifts from the table.\n")
	b.WriteString("# Copy to bootstrap.env and fill in values. An empty value counts as unset. Never commit bootstrap.env.\n")
	for _, r := range rows {
		b.WriteString("\n# " + r.Field + "\n")
		b.WriteString("# required: " + r.Required)
		if r.Default != "" {
			b.WriteString("; default: " + r.Default)
		}
		if r.SecretClass != "" {
			b.WriteString("; secret class: " + r.SecretClass)
		}
		b.WriteString("\n")
		for _, v := range r.Vars {
			b.WriteString(v + "=\n")
		}
	}
	return []byte(b.String())
}

var exampleKey = regexp.MustCompile(`^([A-Z][A-Z0-9_<>]*)=(.*)$`)

// CheckExample compares an example file with the table and returns the problems: variables of the table with no
// assignment line, assignments of undocumented variables, and secret variables with a value.
func CheckExample(example []byte) []string {
	have := map[string]string{}
	for _, line := range strings.Split(string(example), "\n") {
		if m := exampleKey.FindStringSubmatch(strings.TrimRight(line, "\r")); m != nil {
			have[m[1]] = strings.TrimSpace(m[2])
		}
	}
	known := map[string]bool{}
	var problems []string
	for _, r := range rows {
		for _, v := range r.Vars {
			known[v] = true
			if _, ok := have[v]; !ok {
				problems = append(problems, "missing variable "+v)
			}
		}
	}
	for k, v := range have {
		switch {
		case !known[k] && !isRuntime(k):
			problems = append(problems, "undocumented variable "+k)
		case v != "" && secretVar(k):
			problems = append(problems, "secret variable "+k+" must be empty in the example")
		}
	}
	sort.Strings(problems)
	return problems
}

// KnownKeys lists every variable name of the section 9.2 table and the runtime-only variables, sorted. The
// S3_CRED_<INSTANCE>_* pattern is not enumerable; IsKnownKey covers it.
func KnownKeys() []string {
	var out []string
	for k := range knownKeys() {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// IsKnownKey reports whether k is a bootstrap.env variable of plan 50 section 9.2 (PF-02).
func IsKnownKey(k string) bool { return knownKeys()[k] || reCred.MatchString(k) }

// IsSecretKey reports whether the variable carries a secret value (its value must never be printed).
func IsSecretKey(k string) bool { return secretVar(k) }
