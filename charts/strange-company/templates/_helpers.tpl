{{/*
Names
*/}}
{{- define "strange-company.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "strange-company.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- define "strange-company.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "strange-company.postgresql.fullname" -}}
{{- printf "%s-postgresql" (include "strange-company.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "strange-company.hermes.fullname" -}}
{{- printf "%s-hermes" (include "strange-company.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "strange-company.controlPlane.fullname" -}}
{{- printf "%s-control-plane" (include "strange-company.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
The bundled Vikunja Service name, as produced by the upstream chart's own
fullname helper. Reproduced here rather than assumed at call sites.
*/}}
{{- define "strange-company.vikunja.fullname" -}}
{{- if contains "vikunja" .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-vikunja" .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}

{{/*
Labels
*/}}
{{- define "strange-company.selectorLabels" -}}
app.kubernetes.io/name: {{ include "strange-company.name" .ctx }}
app.kubernetes.io/instance: {{ .ctx.Release.Name }}
{{- with .component }}
app.kubernetes.io/component: {{ . }}
{{- end }}
{{- end -}}

{{- define "strange-company.labels" -}}
helm.sh/chart: {{ include "strange-company.chart" .ctx }}
{{ include "strange-company.selectorLabels" . }}
{{- with .ctx.Chart.AppVersion }}
app.kubernetes.io/version: {{ . | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .ctx.Release.Service }}
app.kubernetes.io/part-of: strange-company
{{- with .ctx.Values.commonLabels }}
{{ toYaml . }}
{{- end }}
{{- end -}}

{{- define "strange-company.annotations" -}}
{{- with .Values.commonAnnotations }}
{{- toYaml . }}
{{- end }}
{{- end -}}

{{- define "strange-company.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "strange-company.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{- define "strange-company.imagePullSecrets" -}}
{{- with .Values.global.imagePullSecrets }}
imagePullSecrets:
{{- toYaml . | nindent 2 }}
{{- end }}
{{- end -}}

{{/*
Chart-created Secret holding credentials this chart owns.
*/}}
{{- define "strange-company.secretName" -}}
{{- printf "%s-secrets" (include "strange-company.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "strange-company.configMapName" -}}
{{- printf "%s-config" (include "strange-company.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/* ------------------------------------------------------------------ */}}
{{/* Endpoint resolution. Exactly one function per dependency per field.  */}}
{{/* Call sites never re-test `.enabled`.                                 */}}
{{/* ------------------------------------------------------------------ */}}

{{- define "strange-company.databaseHost" -}}
{{- if .Values.postgresql.enabled -}}
{{- include "strange-company.postgresql.fullname" . -}}
{{- else -}}
{{- required "postgresql.external.host is required when postgresql.enabled is false" .Values.postgresql.external.host -}}
{{- end -}}
{{- end -}}

{{- define "strange-company.databasePort" -}}
{{- if .Values.postgresql.enabled -}}
{{- .Values.postgresql.service.port -}}
{{- else -}}
{{- .Values.postgresql.external.port -}}
{{- end -}}
{{- end -}}

{{- define "strange-company.databaseName" -}}
{{- if .Values.postgresql.enabled -}}
{{- .Values.postgresql.auth.database -}}
{{- else -}}
{{- .Values.postgresql.external.database -}}
{{- end -}}
{{- end -}}

{{- define "strange-company.databaseSecretName" -}}
{{- if .Values.postgresql.enabled -}}
{{- default (include "strange-company.postgresql.fullname" .) .Values.postgresql.auth.existingSecret -}}
{{- else -}}
{{- required "postgresql.external.existingSecret is required when postgresql.enabled is false" .Values.postgresql.external.existingSecret -}}
{{- end -}}
{{- end -}}

{{- define "strange-company.databaseUserKey" -}}
{{- if .Values.postgresql.enabled -}}
{{- .Values.postgresql.auth.usernameKey -}}
{{- else -}}
{{- .Values.postgresql.external.usernameKey -}}
{{- end -}}
{{- end -}}

{{- define "strange-company.databasePasswordKey" -}}
{{- if .Values.postgresql.enabled -}}
{{- .Values.postgresql.auth.passwordKey -}}
{{- else -}}
{{- .Values.postgresql.external.passwordKey -}}
{{- end -}}
{{- end -}}

{{- define "strange-company.vikunjaUrl" -}}
{{- if .Values.vikunja.enabled -}}
{{- printf "http://%s:3456" (include "strange-company.vikunja.fullname" .) -}}
{{- else -}}
{{- required "vikunja.external.url is required when vikunja.enabled is false" .Values.vikunja.external.url -}}
{{- end -}}
{{- end -}}

{{- define "strange-company.vikunjaSecretName" -}}
{{- if and (not .Values.vikunja.enabled) .Values.vikunja.external.existingSecret -}}
{{- .Values.vikunja.external.existingSecret -}}
{{- else -}}
{{- include "strange-company.secretName" . -}}
{{- end -}}
{{- end -}}

{{- define "strange-company.vikunjaTokenKey" -}}
{{- if and (not .Values.vikunja.enabled) .Values.vikunja.external.existingSecret -}}
{{- .Values.vikunja.external.tokenKey -}}
{{- else -}}
{{- "vikunja-token" -}}
{{- end -}}
{{- end -}}

{{- define "strange-company.hermesGatewayUrl" -}}
{{- if .Values.hermes.enabled -}}
{{- printf "http://%s:%v" (include "strange-company.hermes.fullname" .) .Values.hermes.gateway.port -}}
{{- else -}}
{{- required "hermes.external.gatewayUrl is required when hermes.enabled is false" .Values.hermes.external.gatewayUrl -}}
{{- end -}}
{{- end -}}

{{/* May legitimately be empty; callers omit the key when it is. */}}
{{- define "strange-company.hermesDashboardUrl" -}}
{{- if .Values.hermes.enabled -}}
{{- if .Values.hermes.dashboard.enabled -}}
{{- printf "http://%s:%v" (include "strange-company.hermes.fullname" .) .Values.hermes.dashboard.port -}}
{{- end -}}
{{- else -}}
{{- .Values.hermes.external.dashboardUrl -}}
{{- end -}}
{{- end -}}

{{- define "strange-company.hermesSecretName" -}}
{{- if and (not .Values.hermes.enabled) .Values.hermes.external.existingSecret -}}
{{- .Values.hermes.external.existingSecret -}}
{{- else if .Values.hermes.existingSecret -}}
{{- .Values.hermes.existingSecret -}}
{{- else -}}
{{- include "strange-company.secretName" . -}}
{{- end -}}
{{- end -}}

{{- define "strange-company.hermesApiKeyKey" -}}
{{- if and (not .Values.hermes.enabled) .Values.hermes.external.existingSecret -}}
{{- .Values.hermes.external.apiKeyKey -}}
{{- else if .Values.hermes.existingSecret -}}
{{- .Values.hermes.apiKeyKey -}}
{{- else -}}
{{- "hermes-api-key" -}}
{{- end -}}
{{- end -}}

{{/*
Name of the Secret backing the Hermes managed scope (spec:
hermes-managed-scope.md). A BYO secret wins outright; otherwise a chart-owned
one is named after the Hermes workload.
*/}}
{{- define "strange-company.hermesManagedSecretName" -}}
{{- if .Values.hermes.managed.existingSecret -}}
{{- .Values.hermes.managed.existingSecret -}}
{{- else -}}
{{- printf "%s-managed" (include "strange-company.hermes.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}

{{/*
Fail-fast guards for hermes.managed (spec: hermes-managed-scope.md, "Rules").
Independent of hermes.enabled: a misconfigured managed block is a mistake
regardless of whether the bundled Hermes happens to be on.

Called unconditionally from the top of templates/hermes-managed-secret.yaml so
it always runs, even on the branch where that template goes on to render
nothing (the existingSecret branch).

Extended for hermes.managed.fromCredentials (spec:
single-source-credentials.md, Rules 4-6). Layered onto
strange-company.hermesManagedValidate above: fromCredentials feeds the same
generated .env as hermes.managed.env, so it inherits that block's constraints
(the mutual exclusivity with existingSecret, the environment-variable-name
regex) in addition to its own -- resolving to a non-empty `credentials` entry,
and requiring hermes.managed.enabled.

The `not $m.enabled` check runs unconditionally, ahead of every other guard in
this file (including the `hermes.managed.enabled` gate further down), because
Rule 6 must fire precisely when managed scope is OFF.
*/}}
{{- define "strange-company.hermesManagedValidate" -}}
{{- $ctx := . -}}
{{- $m := .Values.hermes.managed -}}
{{- $hasFromCredentials := gt (len $m.fromCredentials) 0 -}}
{{- if and $hasFromCredentials (not $m.enabled) -}}
{{- fail "hermes.managed.fromCredentials is set but hermes.managed.enabled is false. A pin with managed scope disabled would pin nothing; set hermes.managed.enabled: true or clear fromCredentials." -}}
{{- end -}}
{{- if $m.enabled -}}
{{- $hasEnv := gt (len $m.env) 0 -}}
{{- $hasConfig := gt (len $m.config) 0 -}}
{{- $hasExisting := ne $m.existingSecret "" -}}
{{- if and $hasExisting (or $hasEnv $hasConfig $hasFromCredentials) -}}
{{- $inline := list -}}
{{- if $hasEnv -}}{{- $inline = append $inline "hermes.managed.env" -}}{{- end -}}
{{- if $hasConfig -}}{{- $inline = append $inline "hermes.managed.config" -}}{{- end -}}
{{- if $hasFromCredentials -}}{{- $inline = append $inline "hermes.managed.fromCredentials" -}}{{- end -}}
{{- fail (printf "hermes.managed.existingSecret (%q) is set together with %s. These are mutually exclusive: choose an existing Secret or inline values, not both." $m.existingSecret (join " and " $inline)) -}}
{{- end -}}
{{- if not (or $hasEnv $hasConfig $hasExisting $hasFromCredentials) -}}
{{- fail "hermes.managed.enabled is true but hermes.managed.env, hermes.managed.config, hermes.managed.fromCredentials and hermes.managed.existingSecret are all empty. An empty managed scope pins nothing -- it reads as pinned credentials while pinning none. Set at least one of them, or leave hermes.managed.enabled false." -}}
{{- end -}}
{{- $keys := $m.existingSecretKeys -}}
{{- if and $hasExisting (ne (empty $keys.env) (empty $keys.config)) -}}
{{- fail (printf "hermes.managed.existingSecretKeys names %q but not both keys. A Kubernetes secret volume projects either every key under its own name or only the keys listed under `items`, so renaming one file hides the other. Name both, or leave both empty when the secret already uses `.env` and `config.yaml`." (default $keys.config $keys.env)) -}}
{{- end -}}
{{- range $k, $v := $m.env -}}
{{- if not (regexMatch "^[A-Za-z_][A-Za-z0-9_]*$" $k) -}}
{{- fail (printf "hermes.managed.env key %q is not a valid environment variable name (must match ^[A-Za-z_][A-Za-z0-9_]*$). hermes_cli's env loader silently skips lines it cannot parse, so an invalid key would look like a working pin until a provider call failed." $k) -}}
{{- end -}}
{{- end -}}
{{- range $k, $ref := $m.fromCredentials -}}
{{- if not (regexMatch "^[A-Za-z_][A-Za-z0-9_]*$" $k) -}}
{{- fail (printf "hermes.managed.fromCredentials key %q is not a valid environment variable name (must match ^[A-Za-z_][A-Za-z0-9_]*$). hermes_cli's env loader silently skips lines it cannot parse, so an invalid key would look like a working pin until a provider call failed." $k) -}}
{{- end -}}
{{- if hasKey $m.env $k -}}
{{- fail (printf "hermes.managed.env and hermes.managed.fromCredentials both set %q. Both feed the same generated .env; a key set on both sides would leave one silently overwritten by whichever happens to render last. Remove it from one side." $k) -}}
{{- end -}}
{{- $parts := splitn "." 2 $ref -}}
{{- $secretName := $parts._0 -}}
{{- $key := $parts._1 -}}
{{- $hasEntry := hasKey $ctx.Values.credentials $secretName -}}
{{- $keyPresent := false -}}
{{- $resolved := "" -}}
{{- if $hasEntry -}}
{{- $entry := index $ctx.Values.credentials $secretName -}}
{{- $keyPresent = hasKey $entry $key -}}
{{- if $keyPresent -}}{{- $resolved = index $entry $key -}}{{- end -}}
{{- end -}}
{{- if or (not $hasEntry) (not $keyPresent) (eq $resolved "") -}}
{{- fail (printf "hermes.managed.fromCredentials.%s references %q, which does not resolve to a non-empty value. fromCredentials values must be <secretName>.<key> naming a non-empty entry in `credentials` -- check that credentials.%s.%s exists and is set." $k $ref $secretName $key) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
Resolves a hermes.managed.fromCredentials reference ("<secretName>.<key>") to
its value in `credentials` (spec: single-source-credentials.md). Call with
(dict "ctx" $ "ref" $ref). strange-company.hermesManagedValidate has already
guaranteed this resolves to a non-empty string; this helper does not re-check,
so it must never be called without that guard having run first in the same
template.
*/}}
{{- define "strange-company.resolveCredential" -}}
{{- $parts := splitn "." 2 .ref -}}
{{- index (index .ctx.Values.credentials $parts._0) $parts._1 -}}
{{- end -}}

{{/*
Fail-fast guard for the `credentials` block (spec:
single-source-credentials.md, Rule 3). Runs unconditionally, independent of
whether any given entry happens to be populated this render: the collision is
about the NAME an operator chose, and a name that would shadow a chart-owned
Secret the moment it is populated is worth rejecting before that happens, not
after.
*/}}
{{- define "strange-company.credentialsValidate" -}}
{{- $ctx := . -}}
{{- $secretName := include "strange-company.secretName" $ctx -}}
{{- $pgName := include "strange-company.postgresql.fullname" $ctx -}}
{{- $vikunjaDbName := $ctx.Values.vikunja.databaseSecretName -}}
{{- $hermesEnvName := printf "%s-env" (include "strange-company.hermes.fullname" $ctx) -}}
{{- $owned := dict -}}
{{- $_ := set $owned $secretName "the chart's own credentials Secret (templates/secret.yaml)" -}}
{{- $_ := set $owned $pgName "the bundled PostgreSQL Secret (templates/postgresql-secret.yaml)" -}}
{{- $_ := set $owned $vikunjaDbName "the Vikunja database Secret (templates/postgresql-secret.yaml, vikunja.databaseSecretName)" -}}
{{- $_ := set $owned $hermesEnvName "the Hermes development-convenience Secret (templates/hermes-secret.yaml, hermes.secrets)" -}}
{{- range $name, $entry := $ctx.Values.credentials -}}
{{- if hasKey $owned $name -}}
{{- fail (printf "credentials.%s collides with %s. Rename the credentials entry: reusing a chart-owned Secret name would silently overwrite it." $name (index $owned $name)) -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
The bundled PostgreSQL password, resolved once per render.

Precedence: an explicit value, then the password already stored in the cluster
(so `helm upgrade` never rotates it), then a freshly generated one. Callers that
need it twice in one file must capture it in a variable, not call this twice.
*/}}
{{- define "strange-company.postgresql.password" -}}
{{- if .Values.postgresql.auth.password -}}
{{- .Values.postgresql.auth.password -}}
{{- else -}}
{{- $name := include "strange-company.postgresql.fullname" . -}}
{{- $key := .Values.postgresql.auth.passwordKey -}}
{{- $existing := lookup "v1" "Secret" .Release.Namespace $name -}}
{{- if and $existing $existing.data (hasKey $existing.data $key) -}}
{{- index $existing.data $key | b64dec -}}
{{- else -}}
{{- randAlphaNum 24 -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
A chart-owned credential that must stay stable across upgrades.

Precedence: an explicit value, then whatever is already stored under that key in
the chart's Secret, then a fresh random value. Call once per key per render.
*/}}
{{- define "strange-company.preserved" -}}
{{- if .value -}}
{{- .value -}}
{{- else -}}
{{- $ctx := .ctx -}}
{{- $existing := lookup "v1" "Secret" $ctx.Release.Namespace (include "strange-company.secretName" $ctx) -}}
{{- if and $existing $existing.data (hasKey $existing.data .key) -}}
{{- index $existing.data .key | b64dec -}}
{{- else -}}
{{- randAlphaNum (default 32 .length) -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
Guard rails for the one seam this chart cannot resolve automatically: the
upstream Vikunja chart does not run `tpl` over secretKeyRef names, so its
database Secret reference is a literal that has to agree with ours.
*/}}
{{- define "strange-company.validate" -}}
{{- if hasKey .Values.vikunja "ingress" -}}
{{- fail "vikunja.ingress is not supported: every key under `vikunja:` other than `vikunja.vikunja` is merged into the upstream chart's root values, where `ingress` must be a map of named ingresses. Configure the Vikunja Ingress at vikunja.vikunja.ingress.main instead." -}}
{{- end -}}
{{- if and .Values.postgresql.enabled .Values.vikunja.enabled -}}
{{- $ref := dig "vikunja" "env" "VIKUNJA_DATABASE_PASSWORD" "valueFrom" "secretKeyRef" "name" "" .Values.vikunja -}}
{{- if and $ref (ne $ref .Values.vikunja.databaseSecretName) -}}
{{- fail (printf "vikunja.databaseSecretName is %q but vikunja.vikunja.env.VIKUNJA_DATABASE_PASSWORD references Secret %q. These must match: this chart creates the former, and the upstream chart cannot template the latter." .Values.vikunja.databaseSecretName $ref) -}}
{{- end -}}
{{- $host := dig "vikunja" "env" "VIKUNJA_DATABASE_HOST" "" .Values.vikunja -}}
{{- if and (kindIs "string" $host) $host -}}
{{- $want := printf "%s:%v" (include "strange-company.postgresql.fullname" .) .Values.postgresql.service.port -}}
{{- $got := tpl $host . -}}
{{- if ne $got $want -}}
{{- fail (printf "vikunja.vikunja.env.VIKUNJA_DATABASE_HOST renders to %q but the bundled PostgreSQL Service is %q. The upstream chart cannot call this chart's helpers, so that value inlines the name; update it (or vikunja.databaseSecretName / fullnameOverride) to match." $got $want) -}}
{{- end -}}
{{- end -}}
{{- $user := dig "vikunja" "env" "VIKUNJA_DATABASE_USER" "" .Values.vikunja -}}
{{- if and (kindIs "string" $user) $user (ne $user .Values.postgresql.auth.username) -}}
{{- fail (printf "vikunja.vikunja.env.VIKUNJA_DATABASE_USER is %q but the bundled PostgreSQL user is %q. Vikunja would not be able to authenticate." $user .Values.postgresql.auth.username) -}}
{{- end -}}
{{- end -}}
{{- end -}}
