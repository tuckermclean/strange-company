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
Guard rails for the one seam this chart cannot resolve automatically: the
upstream Vikunja chart does not run `tpl` over secretKeyRef names, so its
database Secret reference is a literal that has to agree with ours.
*/}}
{{- define "strange-company.validate" -}}
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
