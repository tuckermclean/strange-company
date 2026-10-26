package config_test

import (
	"errors"
	"strings"
	"testing"

	"strange.company/sc/internal/bootstrap/config"
)

// goldenCase sets variables on a base install and lists the normalized fields (absolute pointers) it must yield.
type goldenCase struct {
	base string
	set  map[string]string
	drop []string
	want map[string]string
}

type m = map[string]string

const (
	hex32 = "0123456789abcdef0123456789abcdef"
	fp    = "SHA256:abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQ"
)

func ref(ptr, env, class string) m {
	return m{ptr + "/env": env, ptr + "/secretClass": class}
}

func join(parts ...m) m {
	out := m{}
	for _, p := range parts {
		for k, v := range p {
			out[k] = v
		}
	}
	return out
}

var emailSet = m{
	"NOTIFY_TRANSPORT": "email", "NOTIFY_SMTP_HOST": "smtp.example.net", "NOTIFY_SMTP_PORT": "2525",
	"NOTIFY_SMTP_FROM": "sc@example.net", "NOTIFY_SMTP_TO": "ops@example.net,oncall@example.net",
	"NOTIFY_SMTP_USERNAME": "fixture-secret-u", "NOTIFY_SMTP_PASSWORD": "fixture-secret-p",
}

var stateSet = m{
	"S3_CREDENTIAL_MODE": "shared", "S3_STATE_ENDPOINT": "https://state.example.net",
	"S3_STATE_ACCESS_KEY_ID": "fixture-secret-s1", "S3_STATE_SECRET_ACCESS_KEY": "fixture-secret-s2",
}

// golden has one case per row of plan 50 section 9.2, keyed by the first variable of the row.
var golden = map[string]goldenCase{
	"CLUSTER_NAME": {base: "ovh", set: m{"CLUSTER_NAME": "prod-one"},
		want: m{"/spec/cluster/name": "prod-one", "/metadata/name": "prod-one"}},
	"BASE_DOMAIN":         {base: "ovh", set: m{"BASE_DOMAIN": "ops.example.org"}, want: m{"/spec/cluster/baseDomain": "ops.example.org"}},
	"ADMIN_EMAIL":         {base: "ovh", set: m{"ADMIN_EMAIL": "admin@example.org"}, want: m{"/spec/cluster/adminEmail": "admin@example.org"}},
	"PROFILE":             {base: "ovh", set: m{"PROFILE": "lab-small"}, want: m{"/spec/cluster/profile": "lab-small"}},
	"PROVIDER":            {base: "existing", want: m{"/spec/provider/name": "existing"}},
	"PROVIDER_REGION":     {base: "ovh", set: m{"PROVIDER_REGION": "SBG"}, want: m{"/spec/provider/region": "SBG"}},
	"EXPECTED_NODE_COUNT": {base: "ovh", set: m{"EXPECTED_NODE_COUNT": "3"}, want: m{"/spec/provider/expectedNodeCount": "3"}},
	"MACHINE_CLASS":       {base: "ovh", set: m{"MACHINE_CLASS": "ovh-vps-8c-16g-160nvme"}, want: m{"/spec/provider/machineClass": "ovh-vps-8c-16g-160nvme"}},
	"MAX_MONTHLY_COST":    {base: "ovh", set: m{"MAX_MONTHLY_COST": "40.50"}, want: m{"/spec/provider/maxMonthlyCost/amount": "40.50"}},
	"COST_CURRENCY":       {base: "ovh", set: m{"COST_CURRENCY": "USD"}, want: m{"/spec/provider/maxMonthlyCost/currency": "USD"}},
	"OVH_ENDPOINT":        {base: "ovh", set: m{"OVH_ENDPOINT": "ovh-ca"}, want: m{"/spec/provider/ovh/endpoint": "ovh-ca"}},
	"OVH_SUBSIDIARY":      {base: "ovh", set: m{"OVH_SUBSIDIARY": "FR"}, want: m{"/spec/provider/ovh/subsidiary": "FR"}},
	"OVH_PRODUCT_LINE": {base: "ovh", set: m{"OVH_PRODUCT_LINE": "public-cloud", "OVH_CLOUD_PROJECT_ID": hex32},
		want: m{"/spec/provider/ovh/productLine": "public-cloud"}},
	"OVH_CLOUD_PROJECT_ID": {base: "ovh", set: m{"OVH_PRODUCT_LINE": "public-cloud", "OVH_CLOUD_PROJECT_ID": hex32},
		want: m{"/spec/provider/ovh/cloudProjectId": hex32}},
	"OVH_APPLICATION_KEY": {base: "ovh", want: join(
		ref("/spec/provider/ovh/credentials/applicationKey", "OVH_APPLICATION_KEY", "SC-INFRA-PROVIDER"),
		ref("/spec/provider/ovh/credentials/applicationSecret", "OVH_APPLICATION_SECRET", "SC-INFRA-PROVIDER"),
		ref("/spec/provider/ovh/credentials/consumerKey", "OVH_CONSUMER_KEY", "SC-INFRA-PROVIDER"))},
	"EXISTING_NODES": {base: "existing", set: m{"EXPECTED_NODE_COUNT": "2",
		"EXISTING_NODES": "node-01=debian@192.0.2.10:2201/10.77.0.11,node-02=ops@192.0.2.11"},
		want: m{
			"/spec/provider/existing/nodes/0/name": "node-01", "/spec/provider/existing/nodes/0/ssh/host": "192.0.2.10",
			"/spec/provider/existing/nodes/0/ssh/port": "2201", "/spec/provider/existing/nodes/0/ssh/user": "debian",
			"/spec/provider/existing/nodes/0/internalIp": "10.77.0.11",
			"/spec/provider/existing/nodes/1/name":       "node-02", "/spec/provider/existing/nodes/1/ssh/host": "192.0.2.11",
			"/spec/provider/existing/nodes/1/ssh/user": "ops",
		}},
	"EXISTING_NODE_OVERRIDES": {base: "existing", set: m{"EXISTING_NODE_OVERRIDES": "node-01:disk_class=ssd;node-01:host_key=" + fp},
		want: m{"/spec/provider/existing/nodes/0/diskClassOverride": "ssd", "/spec/provider/existing/nodes/0/hostKeyFingerprint": fp}},
	"SSH_PRIVATE_KEY_PATH": {base: "existing", set: m{"SSH_PRIVATE_KEY_PATH": "~/.ssh/other"},
		want: m{"/spec/ssh/privateKey/file": "/run/sc/ssh/id_key", "/spec/ssh/privateKey/secretClass": "SC-SSH-OPERATOR"}},
	"SSH_PUBLIC_KEY_PATH": {base: "existing", set: m{"SSH_PUBLIC_KEY_PATH": "~/.ssh/other.pub"}},
	"SSH_USE_AGENT":       {base: "existing", set: m{"SSH_USE_AGENT": "true"}, want: m{"/spec/ssh/useAgent": "true"}},
	"SSH_ALLOWED_CIDRS":   {base: "existing", set: m{"SSH_ALLOWED_CIDRS": "192.0.2.0/24"}, want: m{"/spec/ssh/allowedCidrs/0": "192.0.2.0/24"}},
	"INGRESS_MODE":        {base: "ovh", set: m{"INGRESS_MODE": "direct"}, want: m{"/spec/ingress/mode": "direct"}},
	"CLOUDFLARE_ACCOUNT_ID": {base: "ovh", set: m{"CLOUDFLARE_ACCOUNT_ID": "fedcba9876543210fedcba9876543210"},
		want: m{"/spec/ingress/cloudflare/accountId": "fedcba9876543210fedcba9876543210"}},
	"CLOUDFLARE_ZONE_ID":    {base: "ovh", set: m{"CLOUDFLARE_ZONE_ID": hex32}, want: m{"/spec/ingress/cloudflare/zoneId": hex32}},
	"CLOUDFLARE_API_TOKEN":  {base: "ovh", want: ref("/spec/ingress/cloudflare/apiToken", "CLOUDFLARE_API_TOKEN", "SC-DNS-API")},
	"CLOUDFLARE_ACME_TOKEN": {base: "ovh", want: ref("/spec/ingress/acme/apiToken", "CLOUDFLARE_ACME_TOKEN", "SC-DNS-ACME")},
	"DNS_PROVIDER":          {base: "existing", want: m{"/spec/ingress/dnsProvider": "manual"}},
	"ACME_SOLVER":           {base: "existing", want: m{"/spec/ingress/acme/solver": "http01"}},
	"POD_CIDR": {base: "existing", set: m{"POD_CIDR": "10.50.0.0/16", "SERVICE_CIDR": "10.51.0.0/16"},
		want: m{"/spec/network/podCidr": "10.50.0.0/16", "/spec/network/serviceCidr": "10.51.0.0/16"}},
	"NODE_MAINTENANCE_WINDOW": {base: "existing", set: m{"NODE_MAINTENANCE_WINDOW": "Mon 01:00-03:00"},
		want: m{"/spec/node/maintenanceWindow/weekday": "Mon", "/spec/node/maintenanceWindow/start": "01:00",
			"/spec/node/maintenanceWindow/end": "03:00", "/spec/node/maintenanceWindow/tz": "UTC"}},
	"NODE_SSH_IPV6":            {base: "existing", set: m{"NODE_SSH_IPV6": "true"}, want: m{"/spec/node/sshIpv6": "true"}},
	"S3_ENDPOINT":              {base: "existing", set: m{"S3_ENDPOINT": "https://objects.example.org"}, want: m{"/spec/objectStorage/endpoint": "https://objects.example.org"}},
	"S3_REGION":                {base: "existing", set: m{"S3_REGION": "eu-west-3"}, want: m{"/spec/objectStorage/region": "eu-west-3"}},
	"S3_BUCKET_PREFIX":         {base: "existing", set: m{"S3_BUCKET_PREFIX": "sc-custom"}, want: m{"/spec/objectStorage/bucketPrefix": "sc-custom"}},
	"S3_FORCE_PATH_STYLE":      {base: "existing", set: m{"S3_FORCE_PATH_STYLE": "true"}, want: m{"/spec/objectStorage/forcePathStyle": "true"}},
	"S3_BUCKET_MODE":           {base: "existing", set: join(m{"S3_BUCKET_MODE": "existing"}, stateSet), want: m{"/spec/objectStorage/bucketMode": "existing"}},
	"S3_PROVIDER":              {base: "existing", set: m{"S3_PROVIDER": "aws"}, want: m{"/spec/objectStorage/provider": "aws"}},
	"S3_CREDENTIAL_MODE":       {base: "existing", set: stateSet, want: m{"/spec/objectStorage/credentialMode": "shared"}},
	"S3_OBJECT_LOCK":           {base: "existing", set: m{"S3_OBJECT_LOCK": "required"}, want: m{"/spec/objectStorage/objectLock": "required"}},
	"S3_ALLOW_WEAK_PROTECTION": {base: "existing", set: m{"S3_ALLOW_WEAK_PROTECTION": "true"}, want: m{"/spec/objectStorage/allowWeakProtection": "true"}},
	"S3_STATE_ACCESS_KEY_ID": {base: "existing", set: stateSet, want: join(
		ref("/spec/objectStorage/stateAccount/accessKeyId", "S3_STATE_ACCESS_KEY_ID", "SC-OBJECT-STORAGE"),
		ref("/spec/objectStorage/stateAccount/secretAccessKey", "S3_STATE_SECRET_ACCESS_KEY", "SC-OBJECT-STORAGE"),
		m{"/spec/objectStorage/stateAccount/endpoint": "https://state.example.net"})},
	"S3_ALLOW_UNPROTECTED_STATE": {base: "existing", set: m{"S3_ALLOW_UNPROTECTED_STATE": "true"}, want: m{"/spec/objectStorage/allowUnprotectedState": "true"}},
	"S3_PRIVATE_CIDR": {base: "existing", set: m{"S3_ENDPOINT": "https://10.20.0.5:9000", "S3_PRIVATE_CIDR": "10.20.0.0/24"},
		want: m{"/spec/objectStorage/privateCidr": "10.20.0.0/24"}},
	"S3_CRED_<INSTANCE>_ACCESS_KEY_ID": {base: "existing", set: m{"S3_BUCKET_MODE": "existing",
		"S3_CRED_TOFU_STATE_ACCESS_KEY_ID": "fixture-secret-t1", "S3_CRED_TOFU_STATE_SECRET_ACCESS_KEY": "fixture-secret-t2"},
		want: join(
			ref("/spec/objectStorage/instanceCredentials/instance=tofu-state/accessKeyId", "S3_CRED_TOFU_STATE_ACCESS_KEY_ID", "SC-OBJECT-STORAGE"),
			ref("/spec/objectStorage/instanceCredentials/instance=tofu-state/secretAccessKey", "S3_CRED_TOFU_STATE_SECRET_ACCESS_KEY", "SC-OBJECT-STORAGE"))},
	"S3_ACCESS_KEY_ID": {base: "existing", want: join(
		ref("/spec/objectStorage/credentials/accessKeyId", "S3_ACCESS_KEY_ID", "SC-OBJECT-STORAGE"),
		ref("/spec/objectStorage/credentials/secretAccessKey", "S3_SECRET_ACCESS_KEY", "SC-OBJECT-STORAGE"))},
	"GITHUB_MIRROR_REPO":         {base: "existing", set: m{"GITHUB_MIRROR_REPO": "acme/mirror.git-1"}, want: m{"/spec/githubMirror/repository": "acme/mirror.git-1"}},
	"GITHUB_MIRROR_TOKEN":        {base: "existing", want: ref("/spec/githubMirror/token", "GITHUB_MIRROR_TOKEN", "SC-GITHUB-MIRROR")},
	"GITHUB_MIRROR_ALLOW_PUBLIC": {base: "existing", set: m{"GITHUB_MIRROR_ALLOW_PUBLIC": "true"}, want: m{"/spec/githubMirror/allowPublic": "true"}},
	"GITHUB_PROBE_TOKEN":         {base: "existing", want: ref("/spec/githubMirror/probeToken", "GITHUB_PROBE_TOKEN", "SC-GITHUB-PROBE")},
	"GITHUB_MIRROR_READ_TOKEN":   {base: "existing", want: ref("/spec/githubMirror/readToken", "GITHUB_MIRROR_READ_TOKEN", "SC-GITHUB-MIRROR-READ")},
	"GITHUB_MIRROR_PROTECTION_TIER": {base: "existing", set: m{"GITHUB_MIRROR_PROTECTION_TIER": "unavailable-accepted"},
		want: m{"/spec/githubMirror/protectionTier": "unavailable-accepted"}},
	"NOTIFY_TRANSPORT":   {base: "existing", set: m{"NOTIFY_TRANSPORT": "ntfy"}, want: m{"/spec/notifications/transport": "ntfy"}},
	"NOTIFY_WEBHOOK_URL": {base: "existing", want: ref("/spec/notifications/webhookUrl", "NOTIFY_WEBHOOK_URL", "SC-NOTIFY-TRANSPORT")},
	"NOTIFY_NTFY_TOKEN": {base: "existing", set: m{"NOTIFY_TRANSPORT": "ntfy", "NOTIFY_NTFY_TOKEN": "fixture-secret-n"},
		want: ref("/spec/notifications/ntfyToken", "NOTIFY_NTFY_TOKEN", "SC-NOTIFY-TRANSPORT")},
	"NOTIFY_SMTP_HOST": {base: "existing", set: emailSet, drop: []string{"NOTIFY_WEBHOOK_URL"}, want: m{
		"/spec/notifications/email/smtpHost": "smtp.example.net", "/spec/notifications/email/smtpPort": "2525",
		"/spec/notifications/email/from": "sc@example.net", "/spec/notifications/email/to/0": "ops@example.net",
		"/spec/notifications/email/to/1": "oncall@example.net"}},
	"NOTIFY_SMTP_USERNAME": {base: "existing", set: emailSet, drop: []string{"NOTIFY_WEBHOOK_URL"}, want: join(
		ref("/spec/notifications/email/username", "NOTIFY_SMTP_USERNAME", "SC-NOTIFY-TRANSPORT"),
		ref("/spec/notifications/email/password", "NOTIFY_SMTP_PASSWORD", "SC-NOTIFY-TRANSPORT"))},
	"HEARTBEAT_URL": {base: "existing", set: m{"HEARTBEAT_URL": "fixture-secret-hb"},
		want: ref("/spec/notifications/heartbeatUrl", "HEARTBEAT_URL", "SC-NOTIFY-TRANSPORT")},
	"ANTHROPIC_API_KEY": {base: "existing", set: m{"OPENAI_API_KEY": "fixture-secret-o1", "DEEPSEEK_API_KEY": "fixture-secret-o2",
		"ZAI_API_KEY": "fixture-secret-o3", "GOOGLE_API_KEY": "fixture-secret-o4"}, want: join(
		ref("/spec/models/providers/name=anthropic/apiKey", "ANTHROPIC_API_KEY", "SC-MODEL-API-KEYS"),
		ref("/spec/models/providers/name=openai/apiKey", "OPENAI_API_KEY", "SC-MODEL-API-KEYS"),
		ref("/spec/models/providers/name=deepseek/apiKey", "DEEPSEEK_API_KEY", "SC-MODEL-API-KEYS"),
		ref("/spec/models/providers/name=zai/apiKey", "ZAI_API_KEY", "SC-MODEL-API-KEYS"),
		ref("/spec/models/providers/name=google/apiKey", "GOOGLE_API_KEY", "SC-MODEL-API-KEYS"))},
	"OPENAI_COMPATIBLE_API_KEY": {base: "existing", set: m{"OPENAI_COMPATIBLE_API_KEY": "fixture-secret-oc", "OPENAI_COMPATIBLE_BASE_URL": "https://llm.example.net/v1"},
		want: join(ref("/spec/models/providers/name=openai-compatible/apiKey", "OPENAI_COMPATIBLE_API_KEY", "SC-MODEL-API-KEYS"),
			m{"/spec/models/providers/name=openai-compatible/baseUrl": "https://llm.example.net/v1"})},
}

// defaults check the Default column where the BootstrapConfig carries the defaulted value.
var defaults = map[string]goldenCase{
	"PROFILE":          {base: "existing", want: m{"/spec/cluster/profile": "self-hosting-minimum"}},
	"COST_CURRENCY":    {base: "ovh", want: m{"/spec/provider/maxMonthlyCost/currency": "EUR"}},
	"OVH_ENDPOINT":     {base: "ovh", want: m{"/spec/provider/ovh/endpoint": "ovh-eu"}},
	"OVH_PRODUCT_LINE": {base: "ovh", want: m{"/spec/provider/ovh/productLine": "vps"}},
	"SSH_USE_AGENT":    {base: "existing", want: m{"/spec/ssh/useAgent": "false"}},
	"INGRESS_MODE":     {base: "ovh", want: m{"/spec/ingress/mode": "tunnel"}},
	"DNS_PROVIDER":     {base: "ovh", want: m{"/spec/ingress/dnsProvider": "cloudflare"}},
	"ACME_SOLVER":      {base: "ovh", want: m{"/spec/ingress/acme/solver": "dns01-cloudflare"}},
	"POD_CIDR":         {base: "existing", want: m{"/spec/network/podCidr": "10.42.0.0/16", "/spec/network/serviceCidr": "10.43.0.0/16"}},
	"NODE_MAINTENANCE_WINDOW": {base: "existing", want: m{"/spec/node/maintenanceWindow/weekday": "Sun",
		"/spec/node/maintenanceWindow/start": "04:00", "/spec/node/maintenanceWindow/end": "06:00", "/spec/node/maintenanceWindow/tz": "UTC"}},
	"NODE_SSH_IPV6":       {base: "existing", want: m{"/spec/node/sshIpv6": "false"}},
	"S3_BUCKET_PREFIX":    {base: "existing", set: m{"CLUSTER_NAME": "prod-one"}, want: m{"/spec/objectStorage/bucketPrefix": "sc-prod-one"}},
	"S3_FORCE_PATH_STYLE": {base: "existing", want: m{"/spec/objectStorage/forcePathStyle": "false"}},
	"S3_BUCKET_MODE":      {base: "existing", want: m{"/spec/objectStorage/bucketMode": "create"}},
	"S3_PROVIDER":         {base: "existing", want: m{"/spec/objectStorage/provider": "generic"}},
	"S3_CREDENTIAL_MODE/aws": {base: "existing", set: m{"S3_PROVIDER": "aws"}, drop: []string{"S3_CREDENTIAL_MODE"},
		want: m{"/spec/objectStorage/credentialMode": "scoped"}},
	"S3_CREDENTIAL_MODE/generic": {base: "existing", set: stateSet, drop: []string{"S3_CREDENTIAL_MODE"},
		want: m{"/spec/objectStorage/credentialMode": "shared"}},
	"S3_OBJECT_LOCK":                {base: "existing", want: m{"/spec/objectStorage/objectLock": "auto"}},
	"S3_ALLOW_WEAK_PROTECTION":      {base: "existing", want: m{"/spec/objectStorage/allowWeakProtection": "false"}},
	"S3_ALLOW_UNPROTECTED_STATE":    {base: "existing", want: m{"/spec/objectStorage/allowUnprotectedState": "false"}},
	"GITHUB_MIRROR_ALLOW_PUBLIC":    {base: "existing", want: m{"/spec/githubMirror/allowPublic": "false"}},
	"GITHUB_MIRROR_PROTECTION_TIER": {base: "existing", want: m{"/spec/githubMirror/protectionTier": "rulesets"}},
	"NOTIFY_SMTP_PORT": {base: "existing", set: emailSet, drop: []string{"NOTIFY_WEBHOOK_URL", "NOTIFY_SMTP_PORT"},
		want: m{"/spec/notifications/email/smtpPort": "587"}},
	"HEARTBEAT_URL": {base: "existing", want: m{"/spec/notifications/heartbeatUrl": "<nil>"}},
}

func runCase(t *testing.T, c goldenCase) {
	t.Helper()
	res := mustNormalize(t, merge(base(c.base), c.set, c.drop), "")
	for ptr, want := range c.want {
		if got := get(res.Doc, ptr); got != want {
			t.Errorf("%s = %q, want %q", ptr, got, want)
		}
	}
}

// 50-AC-1: one golden subtest per row of plan 50 section 9.2.
func TestEnvNormalization(t *testing.T) {
	rows := planRows(t)
	ran := 0
	for _, r := range rows {
		c, ok := golden[r.first]
		t.Run(r.first, func(t *testing.T) {
			if !ok {
				t.Fatalf("plan 50 section 9.2 row %q has no golden case", r.first)
			}
			runCase(t, c)
		})
		ran++
	}
	for name, c := range defaults {
		t.Run("default/"+name, func(t *testing.T) { runCase(t, c) })
		ran++
	}
	if ran < len(rows) {
		t.Errorf("%d golden subtests for %d rows", ran, len(rows))
	}
}

// 50-AC-1: the Req. column; a missing or empty required variable is a configuration error (exit 2).
func TestEnvNormalization_MissingRequired(t *testing.T) {
	required := map[string][]string{
		"existing": {"CLUSTER_NAME", "BASE_DOMAIN", "ADMIN_EMAIL", "PROVIDER", "EXPECTED_NODE_COUNT", "MAX_MONTHLY_COST",
			"EXISTING_NODES", "S3_ENDPOINT", "S3_REGION", "S3_ACCESS_KEY_ID", "S3_SECRET_ACCESS_KEY",
			"GITHUB_MIRROR_REPO", "GITHUB_MIRROR_TOKEN", "NOTIFY_TRANSPORT", "ANTHROPIC_API_KEY"},
		"ovh": {"PROVIDER_REGION", "MACHINE_CLASS", "OVH_APPLICATION_KEY", "OVH_APPLICATION_SECRET", "OVH_CONSUMER_KEY"},
	}
	for b, vars := range required {
		for _, v := range vars {
			for _, how := range []string{"absent", "empty"} {
				t.Run(b+"/"+v+"/"+how, func(t *testing.T) {
					env := merge(base(b), nil, nil)
					if how == "empty" {
						env[v] = ""
					} else {
						delete(env, v)
					}
					res, err := config.Normalize(env, opts(""))
					var ce *config.Error
					if err == nil || !errors.As(err, &ce) {
						t.Fatalf("want a *config.Error, got res=%v err=%v", res, err)
					}
					if ce.ExitCode != 2 {
						t.Errorf("exit code %d, want 2 (section 7.2)", ce.ExitCode)
					}
				})
			}
		}
	}
}

// 50-AC-1: --profile overrides PROFILE (Default column of the PROFILE row).
func TestEnvNormalization_ProfileFlagOverrides(t *testing.T) {
	res := mustNormalize(t, merge(baseExisting(), m{"PROFILE": "lab-small"}, nil), "self-hosting-ha")
	if got := get(res.Doc, "/spec/cluster/profile"); got != "self-hosting-ha" {
		t.Errorf("profile = %q, want the --profile value", got)
	}
}

// 50-AC-1: runtime-only variables never reach the BootstrapConfig (plan 50 section 9.2).
func TestEnvNormalization_RuntimeOnlyVariablesNotInConfig(t *testing.T) {
	env := merge(baseExisting(), m{"SC_CONFIRM_PLAN": "yes", "SC_CONFIRM_STORAGE": "yes", "SC_CONFIRM_BUDGET": "yes",
		"SC_RECOVERY_PASSPHRASE_FILE": "/run/sc/pass", "SC_VERIFY_SETTLE": "11m", "SC_CONTAINER_ENGINE": "podman"}, nil)
	res := mustNormalize(t, env, "")
	for _, name := range []string{"SC_CONFIRM_", "SC_RECOVERY_PASSPHRASE_FILE", "SC_VERIFY_SETTLE", "SC_CONTAINER_ENGINE", "/run/sc/pass"} {
		if strings.Contains(string(res.YAML), name) {
			t.Errorf("normalized output mentions runtime-only %q", name)
		}
	}
}

// 50-AC-1: the section 9.1 grammar.
func TestParseEnv_Grammar(t *testing.T) {
	in := "# comment\nA_ONE=plain\nB_TWO=\"double quoted\"\nC_THREE='single quoted'\nD_FOUR=\nE_FIVE=x\nE_FIVE=\nF_SIX=~/keys/id\nG_SEVEN=a$HOME`b`\n"
	got, err := config.ParseEnv([]byte(in))
	if err != nil {
		t.Fatalf("ParseEnv: %v", err)
	}
	want := m{"A_ONE": "plain", "B_TWO": "double quoted", "C_THREE": "single quoted", "F_SIX": "~/keys/id", "G_SEVEN": "a$HOME`b`"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
}

// 50-AC-1: the code's table has one row per row of the plan, in order.
func TestRows_MatchPlanTable(t *testing.T) {
	plan := planRows(t)
	rows := config.Rows()
	if len(rows) != len(plan) {
		t.Fatalf("config.Rows() has %d rows, plan 50 section 9.2 has %d", len(rows), len(plan))
	}
	for i, r := range rows {
		if len(r.Vars) == 0 || r.Vars[0] != plan[i].first {
			t.Errorf("row %d: vars %v, plan row starts with %q", i, r.Vars, plan[i].first)
		}
	}
}
