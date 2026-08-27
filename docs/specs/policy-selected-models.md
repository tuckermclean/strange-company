# Spec: the model policy selects must be the model that answers

Status: proposed
Related: [`reference/hermes-integration-notes.md`](../reference/hermes-integration-notes.md)

## Problem

The control plane's own model calls go through the Hermes gateway. The gateway
**ignores the requested model** and routes by its own global configuration, so
the model policy chose is not the model that answers.

Observed on a real deployment: the control plane logged
`screening_model: deepseek-v4-flash`, `modelclient` sent exactly that string,
and the session Hermes created to serve it recorded `model: claude-opus-4-6`.
Every §10.1 ambiguity screen was billed at frontier rates while the logs said it
was cheap.

The gateway is explicit about this if you look: a turn reports
`route_source: "global"` and `requested: {"provider": "", "model": ""}`, and
`GET /v1/models` only ever advertises `hermes-agent`. Nothing rejects the model
field; it is accepted and dropped.

This is not a Hermes bug. It is a wiring mistake in this repository:
`Resolution` already carries `BaseURL` and `Env`, and
`runSpecificationSupervisor` used neither. It built its client against
`HERMES_GATEWAY_URL` and passed the policy model string to an endpoint that does
not honour it.

The damage is worse than an unexpected bill. §22 exists to test the
model-tiering thesis empirically, and §12's ladder is the mechanism under test.
If the cheap rung is silently served by the expensive model, every measurement
of that thesis is measuring nothing.

## The rule

**A model call the control plane makes itself goes to the provider policy
named, at that provider's `baseUrl`, with that provider's credential.** The
Hermes gateway is for the human conversation, which is the one thing it is
uniquely for.

## Non-goals

- Changing where the §10.2 conversation lives. It stays in Hermes: a human
  continues it in the dashboard, and no model call is spent to open it.
- Giving coding Jobs their credentials differently. §29 constrains Jobs, and
  Jobs already receive only their own provider's Secret by reference.
- Choosing models. That stays entirely in `models.yaml`.

## Credential access

`policy.CredentialRef` names a Kubernetes Secret and a key. The control plane
resolves it by reading `<credentialsDir>/<secret>/<key>`, which is what a
projected Secret volume already looks like. No Kubernetes API access, no RBAC,
and an operator can see exactly what the pod can read by looking at the volume.

`CREDENTIALS_DIR` defaults to `/credentials`.

This does concentrate the credentials the control plane itself calls with into
one pod. That is a real trade and worth stating plainly: §29 constrains what a
*Job* receives, and this is not a Job. The mitigation is that only providers
named by phases the control plane executes in-process need to be projected at
all.

## Rules

1. An in-process model call resolves its provider from policy and uses
   `Resolution.BaseURL`. A provider with no `baseUrl` cannot be called this way.
2. A provider whose `baseUrl` is missing, or whose credential file is absent or
   empty, **disables that path with an error naming the provider, the phase, and
   the missing piece.** It must never fall back to another endpoint: a silent
   fallback is the failure this whole document exists to remove.
3. The failure is reported at boot, not at first use, so an operator sees it in
   the startup log rather than discovering it from a bill.
4. The startup log states the resolved endpoint host for each in-process phase,
   so "which model actually answers" is answerable from the log alone.
5. Credential values never appear in logs, errors, or `Config.Redacted()`.

## Acceptance criteria

- a resolution with a `baseUrl` and a readable credential builds a client
  pointed at that host, carrying that credential.
- a resolution with no `baseUrl` returns an error naming the provider and the
  phase, and builds nothing.
- a missing credential file, and a present-but-empty one, each return an error
  naming the provider and the env var; the two cases are distinguishable.
- a credential file with surrounding whitespace or a trailing newline is
  accepted, trimmed. A projected Secret routinely has one.
- the error never contains the credential value.
- `..` or a path separator in a Secret name or key is refused rather than
  resolved, so policy cannot read outside the credentials directory.
- the specification supervisor starts with a working screening path and refuses
  to start with a broken one, logging which.

## Chart

- project every entry of the `credentials` block into the control-plane pod at
  `/credentials/<secret>/<key>`, read-only.
- default (no credentials configured) renders no volume and no mount, exactly as
  today.
- `CREDENTIALS_DIR` is set whenever the volume is mounted.
