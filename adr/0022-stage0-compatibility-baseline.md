# ADR-0022: A Stage-0 compatibility baseline freezes the third-party versions the plans were reasoned against

**Status:** Accepted (Stage 0, post-review) · **Date:** 2026-10-14 · **Resolves:** external review X#3 (CD-29)

## Context
Stage-0 plans chose third-party versions by "selection rules", such as "latest Forgejo LTS at implementation time", "latest Tempo 2.x minor" or "latest stable". Master plan §6.3 left exact versions to implementation. The plans' reasoning, however, depends on version-specific behavior, which they mark `(verify)`: Woodpecker's exclusive config extension, Flux Bucket and substitution semantics, Longhorn settings, cosign flags and harness CLI flags. Under a moving target, an upstream release made between Stage 0 and implementation can silently invalidate a plan. When that happens, the change shows up as an implementation detail instead of an architecture decision. The external review of PR #138 (finding #3) asked for a baseline to be frozen now, with every upgrade beyond it made an explicit plan-amendment task.

## Decision
- **`plans/compatibility-baseline.yaml`** (kind `CompatibilityBaseline`, schema `contracts/compatibility-baseline.schema.yaml`) records one entry for every third-party component the plans depend on. Each entry has:
  - `name`;
  - `baselineVersion`: the exact stable release on the evidence date;
  - `line`, the release line inside which updates are free, plus an optional `lineKind`;
  - `evidence`: the source URL, the date checked, and whether the version was verified;
  - `planRefs`;
  - `versionSensitiveAssumptions`: the `(verify)` behaviors tied to the version;
  - `notes`.

  Chart-delivered components also record the `chart` that ships them.
- **Implementation pins within the baseline line.** Any release in `line` may be pinned in the version locks of plan 01 (ADR-0106). Patch updates inside the line flow through Renovate as ordinary PRs, under the plans' existing release-age soaks and re-verification tests. A chart version is in-line if its appVersion stays in the component's line.
- **A newer minor or major is a plan-amendment task.** The same applies to a new LTS line, a new distribution release or a new protocol revision. The task:
  1. updates the baseline entry, with new evidence;
  2. re-runs the `(verify)` acceptance criteria of every plan in the entry's `planRefs` that the entry's `versionSensitiveAssumptions` name;
  3. amends any plan whose assumption no longer holds;
  4. is reviewed like an architecture change.

  Renovate may open such PRs, but labels them `upgrade/baseline-amendment`, and they are never merged as routine bumps.
- **`line` is the minor by default.** `lineKind: major` is allowed only for components that ship a new minor with almost every release (Renovate, SeaweedFS, kube-prometheus-stack, the hashicorp/aws provider), and the entry's `notes` give the reason. For kube-prometheus-stack, the bundled Prometheus, Alertmanager and kube-state-metrics keep their own minor lines. The other kinds are `lts` (Forgejo, Node.js), `distribution` (Debian, Ubuntu, Debian-packaged Python and QEMU) and `exact` (the MCP revision range).
- **Plan selection rules are superseded.** Where a plan states a version selection rule (plans 02, 06, 07, 08, 09, 10, 12, 13, 14, 15, 16, 18, 20, 22, 24, 26, 28, 35, 38, 39, 40, 41, 48, 54), the baseline entry decides. The rule only explains how the baseline was chosen and guides the next amendment. Release-age soaks (for example "≥ 14 days old") remain Renovate policy for in-line updates.
- **Unverifiable versions are recorded honestly.** If no authoritative source substantiates a version, the entry records the newest substantiated version with `evidence: unverified` (or `status: unverified`) and a note. No version is invented.
- **The Stage-0 validator** (`tools/stage0/validate.py`, check C03; ADR-0021) checks that:
  - the file parses and validates against the schema, so every entry has the required fields;
  - every `planRefs` path exists;
  - component names are unique.

## Consequences
- An upstream release can no longer change architecture silently. Moving to it is a reviewed, evidenced diff of the baseline plus the re-run ACs.
- The baseline was first checked on 2026-10-14. Several plan assumptions already differ from current upstream, and those differences are recorded in the entries' `notes`:
  - Tempo 3.x is current while plan 41 targets 2.x.
  - cosign's current major is v3, while plan 23 states v2 flag names.
  - The Grafana, Loki and Tempo charts moved to `grafana-community`.
  - Hermes Agent changed its tag scheme from calendar to semver.
  - Flux ≥ 2.8 embeds Helm v4, with server-side apply on by default.

  Each is either already inside a plan's (verify) scope or needs a plan amendment before the affected node starts.
- Fast-moving components cost a small amendment more often (OpenTelemetry Collector, Codex, syft, trivy). That is accepted, because these are the components most likely to break the assumptions the plans rely on.
- Plan 01's lock entries (`selectedBy`, `verifiedAt`) should cite the baseline entry instead of a selection-rule id, and `make locks-check` should reject a pin outside its baseline line. Plan 01 and plan 48 adopt this through the plan-amendment path.

## Alternatives rejected
- **Keep "latest at implementation time":** this is the defect the review identified. Version drift reopens architecture without review.
- **Pin exact patch versions in Stage 0:** these go stale within weeks and would force amendments for security patches. The minor line keeps assumptions stable and lets fixes through.
- **Record versions only in the implementation lock files:** the lock files do not exist until F01. They record what was pinned, not what the plans were reasoned against, and they carry no assumption list to re-verify.

## Verification
- `python3 tools/stage0/validate.py` C03 passes. A baseline entry without `planRefs`, with verified evidence that has no source, with an unknown `lineKind`, or with a `planRefs` path that does not exist makes it fail. The first three cases are the schema's `x-invalid-examples`.
- Every entry's `evidence.checked` is no later than `metadata.checkedAt`. The first re-check is due before node F01 starts, and later ones at each phase start (PLAN.md).
