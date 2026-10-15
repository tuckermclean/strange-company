# 23 — Software supply chain

**Status:** Stage-0 plan (draft)
**DAG nodes:** **P11 supply-chain** (owner). Contributes verify check V-26 (P12), the harness signature interface used at C02/R01 (plans 26, 28), and the release gate of ADR-0303 (plan 52 §8.5).
**Related plans:** [01](01-repository.md), [02](02-bootstrap-toolbox.md), [16](16-forgejo.md), [17](17-github-mirror.md), [20](20-woodpecker.md), [22](22-verification-policy.md), [26](26-agent-runner.md), [28](28-harnesses.md), [34](34-security.md), [35](35-secrets.md), [36](36-network-policy.md), [37](37-admission-policy.md), [42](42-alerting-notifications.md), [43](43-retention-gc.md), [48](48-upgrade.md), [51](51-capacity-and-scheduling.md), [52](52-ci-strategy.md), [54](54-strange-company-chart.md), [55](55-forge-workflow.md)
**Contracts:** consumes `harness.schema.yaml` (28), `secret-classes.yaml` (35); owns `contracts/release-trace.schema.yaml` (`sc-release trace` output, §7.3); defines the config kinds `TrustedSigningKeys`, `VulnerabilityWaivers`, `UpstreamSignaturePolicy` (schemas added under `contracts/` at F02 by this plan's implementation task; shapes in §9)
**ADRs:** relies on ADR-0013, ADR-0014, ADR-0018, ADR-0301, ADR-0303, ADR-0305; introduces **ADR-0501** (buildah in a `baseline` build lane) and **ADR-0502** (release trust chain: protected-tag release, plugin-only release credentials, key-based cosign without public transparency log, Git-committed signatures, human-merged promotion)
**Procedures owned:** `signing-key-rotation.md`, `supply-chain-init.md`

> Every section below is mandatory (SPEC §121).

## 1. Purpose

Make every running Strange Company artefact traceable to the work that produced it and trustworthy by construction [SPEC §37]:

```text
running digest → trusted CI build (pipeline, builder) → source commit → pull request → task → acceptance criteria
```

This plan defines how images are built (only in trusted CI, only from protected refs), what is pinned, how SBOMs, vulnerability reports and provenance are produced and attached, how images are signed with `SC-RELEASE-SIGNING` (never available to workers or untrusted CI), the verification policy (MVP: at promotion and by the Runner for harness images; post-MVP: at admission), the **promotion flow** (CI never deploys: release pipeline → digest-bump PR by bot `sc-release` → CI → human merge → Flux), Renovate, toolbox publication to GHCR, and the trace query.

## 2. Requirements derived from SPEC.md

1. Pinned production dependencies where practical; immutable production image references; vulnerability scanning; SBOM generation; build provenance; image/artifact signing where practical; a signature verification policy; automated dependency-update PRs [SPEC §37].
2. Trace from running digest to acceptance criteria [SPEC §37, §129 "which commit ultimately shipped"].
3. Stochastic workers never receive release-signing keys [SPEC §37]; workers never receive Docker/containerd sockets, privileged DinD, registry-push or deployment credentials [SPEC §38]; trusted CI owns trusted artefact creation [SPEC §38].
4. Woodpecker performs image builds, SBOM generation and release artefacts [SPEC §36].
5. CI does not normally deploy production; production changes go PR → CI → merge → Git → Flux [SPEC §4.2, §33].
6. Critical images and dependencies are pinned [SPEC §116]. Production harness images are digest-pinned [SPEC §63].
7. No Bitnami dependencies [SPEC §20].
8. Retention of OCI images and Forgejo packages is intentional [SPEC §107] (rules: plan 43).

Derived requirements:

- **23-R-1** Only protected-mode workflows served by `sc-ci-config` (ADR-0301) can reach the build lane (`sc-lane: build`, namespace `ci-build`). Releases run on **protected tags matching `releaseTagPattern` whose commit is reachable from `main` (or from a `release/*` branch explicitly listed in `forge.yaml`), created by an allowed identity (§8 self-check)**; cron workflows on `main` run supply-chain maintenance but never publish. No worker-pushable ref or content may reach a signing step (stronger reading of SPEC §37 recorded for `spec-issues.md`, review SEC-1).
- **23-R-2** Release credentials are injected only into the `sc-release` plugin step, never into a build, test or shell step (Woodpecker image filter + plugin-only semantics; VAP `sc-ci-build-release-steps`; `sc-release` self-check).
- **23-R-3** The build step has no credentials at all. It produces an OCI archive; a separate pod (the `sc-release` plugin) scans, attests, signs and pushes it.
- **23-R-4** Every internal image digest referenced anywhere in Git (`versions/internal-images.lock.yaml`, `toolbox/image.lock`, `policy/harnesses.yaml`, rendered manifests) has a signature and provenance committed under `security/attestations/` that verify offline against a non-revoked key in `security/cosign/trusted-keys.yaml` **as of the PR's base revision**.
- **23-R-5** The release pipeline never holds Kubernetes credentials and never writes `main`; it opens a PR from `promote/<tag>`.
- **23-R-6** Vulnerability gate: no image is published with a **Critical** vulnerability that has a fixed version available, unless an unexpired waiver covers it.
- **23-R-7** Every third-party artefact is pinned per plan 01 01-R-2, and updates arrive only as Renovate (or human) PRs; **no automerge** in MVP.

## 3. Inputs

| Input | Source |
|---|---|
| Image definitions | `images/<name>/Containerfile` (+ context) for every internal image; `toolbox/Containerfile` (plan 02); harness images `harnesses/<name>/Containerfile` (plan 28) |
| Go sources and modules | `src/**`, `go.mod`, `go.sum` |
| Locks | `versions/platform.lock.yaml`, `versions/internal-images.lock.yaml`, `toolbox/tools.lock.yaml`, `toolbox/image.lock` (plan 01 §6.3) |
| Signing keys (public) | `security/cosign/trusted-keys.yaml`, `security/cosign/<key-id>.pub` |
| Waivers | `security/vuln-waivers.yaml` |
| Upstream signature policy | `security/upstream-signatures.yaml` |
| CI policy | `policy/ci.yaml` (`CIPolicy`, plan 20) incl. `releaseTagPattern`, `offclusterLane` |
| Release credentials | Woodpecker secrets (§10) |
| Renovate configuration | `renovate.json5` (repo root) |

## 4. Outputs

- Signed images in the Forgejo OCI registry `git.<domain>/strange-company/<name>@sha256:…` (pushed through the in-cluster Service), each with cosign signature, SPDX SBOM attestation, SLSA v1 provenance attestation, vulnerability-report attestation and a Strange Company trace attestation.
- `toolbox` and `ci` images additionally on `ghcr.io/<owner>/strange-company-{toolbox,ci}` (signed).
- Promotion PRs `promote/<tag>` updating locks, `policy/harnesses.yaml`, generated admission params (plan 37) and `security/attestations/**`.
- `ci-build` namespace definition (PSA, quota, NetworkPolicy requirements, admission parameters).
- Renovate CronJob and configuration.
- Check CronJobs `supply-chain-signatures` and `supply-chain-vulns` (namespace `renovate`).
- `sc-release` CLI/plugin (Go, `src/cmd/sc-release`): `plan`, `gate`, `publish`, `promote`, `verify-promotion`, `verify-running`, `verify-upstream`, `trace`, `checks`.

## 5. Dependencies

**Hard (DAG):** P11 ← P08 (Woodpecker trusted CI with `sc-ci-config`, both lanes). C01 ← P11 (Runner image built, signed and promoted by this pipeline).

**Interfaces assumed:**

| Plan | Assumption |
|---|---|
| 20 / ADR-0301 | Exclusive config extension classifies `tag` events matching `releaseTagPattern` (`^v[0-9]+\.[0-9]+\.[0-9]+(-(rc|resign)\.[0-9]+)?$`) with a commit reachable from a protected branch as protected; protected workflows may declare `labels: {sc-lane: build}`; untrusted workflows lose every `from_secret`. Build agent: one workflow at a time, namespace `ci-build`, workspace PVC `ci-workspace` of **10Gi** (CD-06; plan 37 bounds it), sequential steps, no service containers. `WOODPECKER_PLUGINS_PRIVILEGED` empty (no privileged plugins). |
| 16 | `forge.yaml` adds bots `sc-release` (team `sc-release`: write on `strange-company/strange-company`, units code, pulls, packages) and `sc-registry-reader` (team `sc-registry-readers`: read, unit packages, all repos of org `strange-company`); protected tags `v*` allow only `sc-admin` and the `engineers` team with approval; deploy keys cannot push protected tags (verify, 17-AC-4); package cleanup rules keep tags matching `^sha256-[a-f0-9]{64}\.(sig|att)$` and every digest referenced by a lock file (plan 43). |
| 17 | Mirror pushes tags to GitHub so the off-cluster e2e lane runs on the tag commit (ADR-0303); `SC-GITHUB-PROBE` has `Actions: read`. |
| 52 | §8.5 release-gate rule; `make verify` includes `make lint-containerfiles`, `make vuln-waivers-check`, `make supply-chain-verify`. |
| 37 | Admission for `ci-build` (PSA baseline + `sc-ci-build-pod`, `sc-ci-build-release-steps`, `sc-ci-workspace`); `requireUserNamespaces` param. |
| 36 / ADR-0150 (review GC-04, SEC-7) | Until GATE-C02 the proxy does not exist (it ships with the chart at C01), so `ci-build` uses **direct** TCP 443 egress (plan 36 CB-4) plus Forgejo HTTP and DNS; P11 runs in this mode. From C02, `ci-build` reaches the Internet only through the **dedicated** build listener `:3130` (plan 36 CB-3, profile `ci-build`: Debian snapshot, quay.io, ghcr.io and its blob CDN, proxy.golang.org, sum.golang.org, registry.npmjs.org, github.com/api.github.com and release CDNs, trivy DB) reachable only from `ci-build`, never the shared `ci-base` listener used by untrusted `ci-jobs`. `renovate` egresses only through the dedicated listener `:3132` with its own Renovate profile (`HTTPS_PROXY`; ADR-0150, `contracts/egress-policy.schema.yaml`; blind review SEC-B13), never directly and never through `:3129`/`:3130`; until GATE-C02 it keeps direct 443 egress (plan 36 R-1). Its check CronJobs additionally reach Prometheus 9090. buildah and `sc-release` honour `HTTPS_PROXY`. |
| 26 / 28 | Runner verifies harness signatures before dispatch using ConfigMap `sc-cosign-trusted-keys` and registry pull credential `sc-registry-pull` in `strange-company`; plan 28's `signature.keyRef` names a key id of `trusted-keys.yaml`. |
| 54 | Chart values take image digests from `versions/internal-images.lock.yaml`; `imagePullSecrets: [sc-registry-pull]`. |
| 01 / 02 | Lock file kinds and `make locks-check`; toolbox/ci images built from `toolbox/Containerfile`; `cosign`, `syft`, `trivy`, `buildah` versions pinned in `toolbox/tools.lock.yaml` (`sc-release` image embeds cosign/syft/trivy libraries or binaries at the same versions). |
| 45 / 14 / 43 / 44 (CD-33) | Plan 45 §9.4 defines capsule contents and `--from-capsule`; plan 14 provisions `capsule/` under `<prefix>-recovery` with the `capsule-writer` policy; plan 43 never deletes capsules younger than 35 days or the one `LATEST.json` names; plan 44 runs `rt-bootstrap-capsule`. |
| 55 | Promotion PRs and Renovate PRs are "non-task PRs": they need the Woodpecker `ci` status, one human approval, and no TDD provenance (plan 55 defines the non-task merge path). |

## 6. Owned state

| State | System of record | Writers |
|---|---|---|
| Internal images, signatures, attestations | Forgejo OCI registry (blobs in S3 `forgejo-objects`) | `sc-release` with `SC-REGISTRY-PUSH` |
| Committed signatures and provenance | `security/attestations/<image>/<digest>/{signature.json,provenance.dsse.json,trace.dsse.json}` | promotion PRs (human-merged) |
| Trusted keys | `security/cosign/trusted-keys.yaml` + `.pub` files | human PRs (protected path) |
| Waivers, upstream signature policy | `security/vuln-waivers.yaml`, `security/upstream-signatures.yaml` | human PRs |
| Internal image lock | `versions/internal-images.lock.yaml`, `toolbox/image.lock` | promotion PRs |
| SBOMs and full scan reports | registry attestations (summary in the promotion PR body) | `sc-release` |
| GHCR toolbox/ci images | GHCR | `sc-release` with `SC-GHCR-PUSH` |

## 7. External interfaces

### 7.1 `sc-release` commands

| Command | Where | Purpose |
|---|---|---|
| `sc-release plan --tag <t>` | release workflow | compute the build set: images whose **inputs id** (sha256 over the image context files, `go.sum`, referenced base digests and tool lock entries) differs from the lock |
| `sc-release gate --offcluster <sha>` | release workflow (plugin) | plan 52 §8.5; codes `OFFCLUSTER_LANE_MISSING`, `…_FAILED`, `…_TIMEOUT`, `WOODPECKER_MAIN_NOT_GREEN` |
| `sc-release publish` | release workflow (plugin) | self-check → scan gate → SBOM → push → sign → attest → evidence |
| `sc-release promote` | release workflow (plugin) | create `promote/<tag>` and the PR |
| `sc-release verify-promotion --keys-from-ref origin/<base>` | `ci` workflow on every PR (image pinned in the workflow served from `main`) | 23-R-4 offline check |
| `sc-release verify-running` | toolbox (V-26) and CronJob | running internal images verify; third-party images are in `platform.lock.yaml` |
| `sc-release verify-upstream` | `ci` workflow on PRs that change `versions/platform.lock.yaml` | upstream keyless signatures per `upstream-signatures.yaml` |
| `sc-release trace <ref@digest> [--json]` | toolbox | §7.3 |

Exit codes: 0 ok; 10 verification failed; 11 gate failed; 12 vulnerability gate; 13 self-check refused (not a protected ref); 14 registry error; 2 usage.

### 7.2 Release workflow `.woodpecker/release.yaml` (shape)

```yaml
labels: {sc-lane: build}
when:
  - event: tag
    ref: refs/tags/v*
steps:                                      # sequential; no service containers (CD-06)
  - name: plan
    image: <sc-release@sha256>              # plugin: no commands
    settings: {action: plan}
  - name: gate                              # before anything is published (plan 52 §8.5)
    image: <sc-release@sha256>
    settings: {action: gate}
    environment: {SC_GITHUB_PROBE: {from_secret: sc_github_probe_token}}
  # The two steps below repeat per image in the build set (generated list), so the 10Gi
  # workspace holds at most ONE image's buildah storage or ONE archive at a time.
  - name: build-<image>
    image: <buildah@sha256>
    commands:
      - export BUILDAH_ROOT=/woodpecker/.buildah TMPDIR=/woodpecker/.tmp
      - buildah --root "$BUILDAH_ROOT" build --isolation=chroot --storage-driver=vfs --layers=false --pull=always
          --platform=linux/amd64 --timestamp="$CI_COMMIT_TIMESTAMP_EPOCH"
          -f images/<image>/Containerfile -t oci-archive:/woodpecker/out/<image>.tar images/<image>
      - buildah --root "$BUILDAH_ROOT" rmi --all --force && rm -rf "$BUILDAH_ROOT" "$TMPDIR"   # free vfs storage before the next step
    # no secrets; runAsUser 0 inside ci-build (baseline), builder capabilities only (ADR-0501)
  - name: publish-<image>
    image: <sc-release@sha256>
    settings: {action: publish, image: <image>}   # scans, SBOMs, pushes, signs, attests, then deletes /woodpecker/out/<image>.tar
    environment:                                  # trivy DB cache lives in the container filesystem (ephemeral-storage limit 2Gi), not in the workspace
      SC_COSIGN_KEY: {from_secret: sc_cosign_key}
      SC_COSIGN_PASSWORD: {from_secret: sc_cosign_password}
      SC_REGISTRY_PUSH_TOKEN: {from_secret: sc_registry_push_token}
      SC_GHCR_TOKEN: {from_secret: sc_ghcr_push_token}
      SC_RELEASE_PR_TOKEN: {from_secret: sc_release_pr_token}
  - name: promote
    image: <sc-release@sha256>
    settings: {action: promote}
    environment: {SC_RELEASE_PR_TOKEN: {from_secret: sc_release_pr_token}}
```

(Field names follow Woodpecker 3.x; verify `from_secret` under `environment` and plugin-only injection, 23-AC-3.) A cron workflow `.woodpecker/supply-chain.yaml` (`event: cron`, `cron: provider-contract`, lane build) runs plan 04's provider contract tests with `sc_ci_ovh_readonly` (image filter `provider-contract`).

### 7.3 Trace output (`sc-release trace --json`)

```json
{
  "schema_version": "v1alpha1",
  "digest": "sha256:…",
  "image": "git.example.org/strange-company/agent-runner",
  "signature": {"key_id": "sc-release-2026a", "verified": true},
  "build": {"tag": "v0.4.0", "pipeline": "https://ci.example.org/repos/1/pipeline/812", "builder": "woodpecker-agent-build", "builder_image": "quay.io/buildah/stable@sha256:…", "started_at": "…", "finished_at": "…"},
  "source": {"repository": "strange-company/strange-company", "commit": "0123…", "previous_release_commit": "…", "inputs_id": "sha256:…"},
  "pull_requests": [
    {"number": 41, "title": "…", "merged_by": "alice", "merge_commit": "…", "kind": "task",
     "tasks": [{"id": "sc-184", "acceptance_criteria": ["AC-1", "AC-2"], "red_evidence_sha256": "…", "green_evidence_sha256": "…"}]},
    {"number": 42, "kind": "renovate", "tasks": []}
  ],
  "deployed_by": {"promotion_pr": 43, "merged_at": "…"}
}
```

Resolution steps: (1) provenance from `security/attestations/` at `main` (or the registry), signature verified; (2) source commit and previous release commit (previous `v*` tag reachable from it); (3) merge commits on the first-parent path between them → PR numbers (Forgejo API `GET /repos/{o}/{r}/pulls?state=closed` matched by `merge_commit_sha`; verify field); (4) PR body `Task:` field and commit trailers `SC-Task:` (plan 55) → task ids; (5) task record via the Runner internal API (`GET /v1/tasks/{id}`, port-forward with `SC-RUNNER-ADMIN`) → acceptance criteria and evidence digests; (6) the promotion PR that introduced the digest (`git log -S <digest> -- versions/internal-images.lock.yaml`).

## 8. Internal interfaces

- **Build lane → plugin hand-off:** the workspace PVC holds `/woodpecker/out/<image>.tar` (OCI archive) and `/woodpecker/out/plan.json`. `publish` recomputes the archive digest, never trusts a digest written by the build step.
- **Self-check (23-R-2):** before reading any credential file/env, `sc-release publish|promote` calls Forgejo `GET /repos/{o}/{r}/git/refs/tags/{tag}` and `GET /repos/{o}/{r}/compare/{commit}...main` (and each `release/*`) with `SC_RELEASE_PR_TOKEN` (read use) to prove: the tag exists, points to `CI_COMMIT_SHA`, matches `releaseTagPattern`, is protected (`GET /repos/{o}/{r}/tag_protections`), the commit is reachable from `main` (or a `release/*` branch listed in `forge.yaml`; a name match alone is not enough, the branch must have a Forgejo protection rule), and **the tag was pushed by an allowed identity** (review SEC-1): before any release credential is used, `sc-release` calls the Runner's `GET /v1/ci/push-records?repo=<o>/<r>&ref=refs/tags/<tag>` (hooks listener, bearer `SC-CI-INTENTS`, plan 26 push recorder fed by the org-level webhooks of CD-18) and requires the recorded pusher (kind `human` or `bot`) to be in the **release-pusher allow-list** `security/release-pushers.yaml` (kind `ReleasePusherAllowlist`: `teams: [sc-admins, engineers]`, `bots: [sc-admin]`; kind `deploy_key` is never allowed). The same allow-list is the one plan 55's `strange-company/release-provenance` check uses (protected path; changed only by human-reviewed PR). No record or Runner unreachable → exit 13. Deploy-key pushes are attributed to the repository owner (26-AC-41) and are never allowed. Otherwise exit 13 without touching other credentials. It also refuses unless `CI_PIPELINE_EVENT=tag` and the pod runs in namespace `ci-build` (downward API label check).
- **Signing (baseline: cosign v3.1.x, plans/compatibility-baseline.yaml):** cosign library `sign --key env://SC_COSIGN_KEY --tlog-upload=false` with the Sigstore bundle format (v3 default; `--bundle <file>` is required wherever the bundle is written to disk, including `sign-blob`); attestations with `attest --type spdxjson|slsaprovenance1|vuln|https://strange.company/attestations/trace/v1`. Storage: cosign v3 stores signatures and attestations as OCI 1.1 referrers by default; the Forgejo registry's referrers support is verified in 23-AC-5, and if absent `sc-release` falls back to the tag scheme `sha256-<digest>.{sig,att}` (plan 43 retention keeps both). Verification uses `cosign verify --key … --insecure-ignore-tlog=true` (key-based, no transparency log) or the keyless flags of §14.9; legacy-bundle verification is disabled (v3.1.3 fixes GHSA-fx35-mq7g-6g98). Tool baselines: syft v1.54.x (SPDX JSON), trivy v0.76.x (DB fetched at run time), buildah v1.45.x, gitleaks v8.30.x.
- **Committed copies:** `signature.json` = `{payload_b64, signature_b64, key_id}` (cosign simple-signing payload whose `critical.image.docker-manifest-digest` equals the digest); `provenance.dsse.json` and `trace.dsse.json` = DSSE envelopes. Offline verification is pure crypto plus payload checks (no registry, no network).

## 9. Configuration

| File / knob | Kind / format | Default | Who changes |
|---|---|---|---|
| `security/cosign/trusted-keys.yaml` | `TrustedSigningKeys`: `keys: [{id, type: key\|keyless, publicKeyFile (key) or issuer + subjectRegex (keyless), scope: installation\|upstream, status: active\|retired\|revoked, notBefore, revokedAt?, reason?}]`; exactly one `active` key with `scope: installation` (after P11); `upstream` entries (keyless GitHub OIDC identity, §14.9) verify images published to GHCR by the upstream project and are accepted only by `verify-running --mode bootstrap` | one key `sc-release-<YYYY>a` | human PR (`security-review`) |
| `security/release-pushers.yaml` | `ReleasePusherAllowlist` (single source; plans 20 and 55 reference it): `apiVersion: strange.company/v1alpha1`, `kind: ReleasePusherAllowlist`, `spec.teams` (Forgejo `org/team` names whose human members may push release tags), `spec.bots` (account logins), `spec.denyKinds: [deploy_key]` (fixed); matched against the Runner push record's pusher login and kind | `teams: [strange-company/sc-admins, strange-company/engineers]`, `bots: [sc-admin]` | human PR (`security-review`); consumed by `sc-release` self-check, `sc-ci-config` (plan 20) and plan 55 release-provenance |
| `security/vuln-waivers.yaml` | `VulnerabilityWaivers`: `waivers: [{id (CVE/GHSA), images: [glob], package, reason, approvedBy, created, expires}]`; `expires − created ≤ 30 d` (≤ 90 d with a second approver listed) | empty | human PR |
| `security/upstream-signatures.yaml` | `UpstreamSignaturePolicy`: `publishers: [{repositoryPattern, keyless: {issuer, subjectRegex}} \| {key: <pem file>}]` | Flux controllers, cert-manager, CNPG (verify each publisher's current signing practice at implementation) | human PR |
| `policy/ci.yaml` (plan 20) `releaseTagPattern` | regex | `^v[0-9]+\.[0-9]+\.[0-9]+(-(rc|resign)\.[0-9]+)?$` | PR |
| Vulnerability gate severity | `sc-release` flag in `release.yaml` | `CRITICAL`, fixed-only | PR |
| Build platform | `release.yaml` | `linux/amd64` (k3s is amd64-only in MVP, plan 07) | PR |
| Renovate schedule | CronJob `renovate/renovate` | `17 */6 * * *` | PR |
| Renovate config | `renovate.json5` | §14.6 | PR (protected path) |
| Check schedules | CronJobs in `renovate` | `supply-chain-signatures` 05:40 UTC, `supply-chain-vulns` 05:50 UTC | PR |

## 10. Secrets

| Class | Use | Delivery |
|---|---|---|
| `SC-RELEASE-SIGNING` | sign/attest | Woodpecker secrets `sc_cosign_key`, `sc_cosign_password` (events `[tag]`; image filter `sc-release`), source of truth `secrets/ci/<cluster>/release-signing.sops.yaml` (plan 35 tier 1b) |
| `SC-RELEASE-SIGNING` (capsule copy) | sign `capsule.json` (§14.10) | `sc_capsule_cosign_key`, `sc_capsule_cosign_password` (events `[tag, cron]`; `sc-release`; cron `capsule-refresh` on `main` only) |
| `SC-OBJECT-STORAGE/capsule-writer` | upload capsule objects with retention, no delete (§14.10) | `sc_capsule_writer` (events `[tag, cron]`; `sc-release`), source `secrets/ci/<cluster>/s3-capsule-writer.sops.yaml` |
| `SC-REGISTRY-PUSH` | push to Forgejo registry | `sc_registry_push_token` (events `[tag]`; `sc-release`) |
| `SC-FORGEJO-BOT-TOKENS/sc-release` | self-check reads, `promote/*` branch push, PR creation | `sc_release_pr_token` (events `[tag]`; `sc-release`) |
| `SC-GHCR-PUSH` | publish toolbox/ci to GHCR | `sc_ghcr_push_token` (events `[tag]`; `sc-release`) |
| `SC-GITHUB-PROBE` | off-cluster gate (read Actions) | `sc_github_probe_token` (events `[tag]`; `sc-release`) |
| `SC-CI-OVH-READONLY` | provider contract tests | `sc_ci_ovh_readonly` (events `[cron]`; `provider-contract`) |
| `SC-REGISTRY-PULL` | kubelet pulls of internal images; Runner signature checks; check CronJobs | dockerconfigjson Secret `sc-registry-pull` in `strange-company`, `agent-jobs`, `ci-jobs`, `ci-build`, `renovate`, `dr-tests`, `monitoring`, `woodpecker` (SOPS, Flux) |
| `SC-RENOVATE` | Renovate | Secret `renovate/forgejo-sc-renovate` (+ `github-com` instance) |

`sc-bootstrap supply-chain init` (P11; plan 50 command surface) generates the cosign key pair, mints the `sc-release` and `sc-registry-reader` tokens (Forgejo admin API), writes the SOPS files, commits the public key (`security/cosign/sc-release-<YYYY>a.pub`, `trusted-keys.yaml`) by PR, and applies the Woodpecker secrets with `SC-WOODPECKER-ADMIN` (`sc-bootstrap ci-secrets apply`, idempotent: compares secret names and update timestamps, never prints values). Plan 20's ADR-0301 lists release secret events as `[cron, tag]`; this plan narrows publishing credentials to `[tag]` (cron workflows never publish).

## 11. Storage

- Registry: internal images ≈ 6–10 images × ≤ 1.5 GiB (toolbox/ci, harness images) — S3 `forgejo-objects`. Retention (plan 43): keep every digest referenced by any lock file at any `v*` tag of the last 12 months, plus the last 20 releases; delete untagged and `sha-*` tags older than 30 d; never delete `.sig`/`.att` of a kept digest.
- `security/attestations/`: ≈ 6 KiB per image per release in Git.
- No object-storage writes from the build lane (review SEC-7); SBOMs and reports live in the registry with the image (retention plan 43).
- `ci-workspace` PVC (10Gi cap, CD-06, build lane): one image's buildah vfs storage or archive at a time (§14.3 step 5); deleted at workflow end (ADR-0305). The trivy DB cache (≈ 0.7 GiB) lives in the `publish` container's writable layer, bounded by its ephemeral-storage limit (2Gi), not in the workspace.

## 12. Resource requirements

| Workload | requests | limits | Notes |
|---|---|---|---|
| build step (buildah) | cpu 500m, memory 1Gi | cpu 2, memory 3Gi, ephemeral 1Gi | requests `strange.company/heavy-slot` (plan 51 §8.4, CD-05; injected by `sc-ci-config` lane defaults) |
| `sc-release` steps | cpu 100m, memory 256Mi | cpu 1, memory 1Gi, ephemeral 2Gi | trivy DB cache in the container filesystem |
| Renovate CronJob | cpu 200m, memory 512Mi | cpu 1, memory 1536Mi, ephemeral 4Gi | `activeDeadlineSeconds: 3600`, PriorityClass `ci`, `concurrencyPolicy: Forbid` |
| check CronJobs | cpu 50m, memory 128Mi | cpu 500m, memory 768Mi | trivy DB download once per run |

## 13. Security boundary

- **Build lane (`ci-build`):** PSA `baseline` (ADR-0501); admission per plan 37 (`sc-ci-build-pod`: images only clone/buildah/`sc-release`/`provider-contract`; root only for buildah with capabilities ⊆ {CHOWN, DAC_OVERRIDE, FOWNER, FSETID, KILL, SETGID, SETUID, SETFCAP, SYS_CHROOT}; no privileged, no host namespaces/paths; seccomp RuntimeDefault; `hostUsers: false` once 23-AC-6 passes). ServiceAccount `default` without token; RBAC none. Reachable only by protected-mode workflows (ADR-0301).
- **Credentials:** release credentials exist only as Woodpecker secrets (`woodpecker-pg`) and tier-1b SOPS files; only the `sc-release` plugin step receives them; the build step never does (23-R-3).
- **No deployment authority:** neither lane has a kubeconfig, a Flux credential or write access to `main`; `make lint-ci` fails if a `.woodpecker/*.yaml` references `kubectl`, `flux`, `helm upgrade`, a kubeconfig, or `SC-K3S-ADMIN-KUBECONFIG` / `SC-K3S-TOKEN` material.
- **Registry:** private (org `strange-company` is private, plan 16). Push only `sc-release`; pull with `sc-registry-reader` (read-only). The registry is reached for push over `http://forgejo-http.forgejo.svc:3000` (no Cloudflare body limits) and for pull by kubelets at `git.<domain>`, which every node resolves through a managed `/etc/hosts` entry to the pinned Traefik ClusterIP `10.43.0.20` (CD-21, plans 08/09), so image pulls never leave the cluster and survive a Cloudflare outage (23-AC-22).
- **Verification trust root:** `security/cosign/trusted-keys.yaml` at the PR base revision (promotion) or at the deployed revision (Runner, checks). A PR can never add a key and use it in the same PR.
- **Bootstrap trust (P08 → P11, CD-19):** P11 runs **after** `CONTROL TRANSFER COMPLETE` (it needs human-merged PRs). Until then `sc-ci-config`, the toolbox/ci images and the first `sc-release` image come from GHCR images built by the interim lane (ADR-0018) and pinned by digest in reviewed Git. The first P11 release rebuilds every internal image in the trusted lane; from then on `sc-release` is used at the digest produced by the previous trusted release (the plugin image never signs itself in the same run).

Threats and residual risks: plan 34 T-SUP, T-DEP-BUILD, R-14, R-15.

## 14. Happy path

### 14.1 Build mechanism (ADR-0501)

**Decision:** **buildah** (`quay.io/buildah/stable`, digest-pinned in `release.yaml`), daemonless, `--isolation=chroot`, `--storage-driver=vfs`, running as uid 0 **without** `privileged`, without `Unconfined` seccomp/AppArmor, in namespace `ci-build` at PSA `baseline` with only the default-set capabilities listed in §13. Output is an OCI archive in the workspace.

Rationale: rootless BuildKit requires `Unconfined` seccomp and AppArmor plus `--oci-worker-no-process-sandbox` (PSA `privileged`); rootful BuildKit requires `privileged: true` (even inside a user namespace); kaniko was archived upstream in 2025 and its maintained forks (chainguard-dev v1.25.x, osscontainertools v1.28.x, the pre-approved fallback in plans/compatibility-baseline.yaml) publish no freely available official image (web search 2026-10-13; not re-checked by the baseline; verify only if 23-AC-1 fails). buildah chroot isolation is maintained upstream (Red Hat) and its documented non-privileged mode needs only default capabilities (verify at implementation: 23-AC-1 is the deciding test).

**Fallback (pre-approved):** if 23-AC-1 fails on the pinned k3s/containerd/kernel, use the osscontainertools kaniko fork, built from source in the interim lane and digest-pinned, under the same `baseline` namespace and admission rules (kaniko also runs as root with default capabilities). The ADR records which one is in use. `privileged`, `Unconfined` and Docker/containerd sockets remain forbidden in every case [SPEC §38].

**Hardening:** when 23-AC-6 shows that pods with `hostUsers: false` run buildah correctly (Kubernetes ≥ 1.33 user namespaces, containerd ≥ 2.0, kernel with idmapped mounts), plan 37's `requireUserNamespaces` is set to `true` by PR, so the builder's uid 0 maps to an unprivileged host uid.

### 14.2 Pinned dependencies

| Ecosystem | Pinning | Enforced by |
|---|---|---|
| Base images | `FROM <repo>@sha256:<digest>` | `make lint-containerfiles` (sc-lint): FROM without digest fails |
| OS packages | Debian snapshot timestamp (`snapshot.debian.org/archive/debian/<ts>`) | lint: `apt-get install` requires the snapshot sources file from the same Containerfile |
| Downloaded binaries | version + sha256 constant checked with `sha256sum -c` in the same `RUN` | lint: every `curl`/`wget` output is checksum-verified |
| Go modules | `go.sum`, `GOFLAGS=-mod=readonly`, `GOSUMDB=sum.golang.org`, `GOPROXY=https://proxy.golang.org` | build fails on missing sums |
| npm (harness CLIs) | `package-lock.json` with integrity hashes, `npm ci --ignore-scripts` (verify the CLI works without install scripts; 28's harness image test) | lint: no `npm install` |
| pip (toolbox) | `--require-hashes` (plan 02) | lint |
| Helm charts / upstream images | `versions/platform.lock.yaml` (plan 01) | `make locks-check`, F11 `images-pinned` |
| CI step images, actions | digests / full SHAs | `locks-check`, `sc-ci-config` image rule |

### 14.3 Release pipeline (tag `v*` on a protected commit)

1. A human creates the protected tag (Forgejo UI or push by an allowed user). The mirror pushes it to GitHub, triggering the off-cluster e2e lane (ADR-0303).
2. `sc-ci-config` serves `release.yaml` in protected mode with `sc-lane: build`; the build agent runs it in `ci-build`.
3. `plan`: compute the build set (inputs-id diff against `versions/internal-images.lock.yaml`; `toolbox`/`ci` against `toolbox/image.lock`). Empty set → the release still produces a promotion PR only if `--force-all` is given.
4. `gate`: plan 52 §8.5 (Woodpecker `push/ci` green on `T`; GitHub `e2e` run success on `T`, **including the `e2e-c10` job — the C10 fake end-to-end regression is a release gate, not a merge gate, CD-22, 52-AC-22**; polled ≤ 90 min). Nothing is built or published before it passes.
5. For each image in the build set, **sequentially** (CD-06: the build-lane workspace is capped at 10Gi):
   1. `build-<image>`: buildah builds the image to `/woodpecker/out/<image>.tar` (no credentials, network only through the egress proxy), then removes its vfs storage;
   2. `publish-<image>`: self-check (§8); trivy scan of the archive (`--severity CRITICAL --ignore-unfixed --exit-code 1`, waivers from `vuln-waivers.yaml` at `T`; fail → exit 12 `VULN_GATE_FAILED`, nothing of that image published, the workflow stops); a full all-severity report is kept; syft SBOM (SPDX 2.3 JSON); push to `forgejo-http.forgejo.svc:3000/strange-company/<image>` with tags `<tag>` and `sha-<commit12>`; read back the manifest digest; `cosign sign`; attest SBOM (`spdxjson`), provenance (SLSA v1, §14.4), vulnerability report (`vuln`), trace (§14.4); for `toolbox`/`ci` also copy to `ghcr.io/<owner>/strange-company-{toolbox,ci}` and sign there; keep only the small signature/attestation files under `/woodpecker/out/attestations/`; delete the archive. SBOMs and reports are stored only as registry attestations (and summarised in the promotion PR body); the build lane holds **no** object-storage credential (review SEC-7).
   Workspace budget: clone (≈ 0.3 GiB) + the largest single image's vfs build peak (≈ 2–3× its uncompressed size) + its archive must stay ≤ 9 GiB (23-AC-19 measures it for `toolbox`, the largest image). If it does not fit, the remedies, in order, are: slimming the image (multi-stage, fewer layers), then splitting the image; raising the cap needs an amendment to CD-06.
6. If a later image fails, already published images stay in the registry unreferenced (retention removes them, plan 43); a re-run skips published digests (§17).
7. `promote`: branch `promote/<tag>` from the current `main` head; update `versions/internal-images.lock.yaml`, `toolbox/image.lock`, harness digests in `policy/harnesses.yaml`; run `sc-gitops admission-params` (plan 37) and the chart value renderer; write `security/attestations/**`; commit (author `sc-release`, trailers `SC-Release: <tag>`, `SC-Pipeline: <n>`); push; open the PR with a body listing images, digests, SBOM package counts, vulnerability summary (all severities), the included PRs/tasks (from the trace resolver) and the gate evidence.
8. Woodpecker `ci` on the PR (untrusted mode, no secrets) runs `make verify` and `sc-release verify-promotion` using the `sc-release` image pinned in `ci.yaml` **at `main`** and keys from `origin/main`. The PR diff may touch only the paths listed in 23-AC-8 (`sc-release verify-promotion --paths`).
9. A human approves and merges. Flux reconciles: chart values change; the Runner loads new harness digests (with admission params from the same commit).
10. The previous digests remain in the registry (retention) for rollback by reverting the promotion PR.

### 14.4 Provenance and trace attestations

- **SLSA v1 provenance** (`predicateType: https://slsa.dev/provenance/v1`): `buildDefinition.buildType: https://strange.company/buildtypes/woodpecker-release/v1`; `externalParameters: {repository, ref: refs/tags/<tag>, commit, workflow: .woodpecker/release.yaml, image, containerfile}`; `internalParameters: {lane: build, builder_image, buildah_version, platform}`; `resolvedDependencies`: the source commit (`gitCommit`), every base image digest (parsed from `FROM` lines), the Debian snapshot timestamp, `go.sum` digest, `package-lock.json` digest; `runDetails.builder.id: https://ci.<domain>/agents/woodpecker-agent-build`; `metadata.invocationId`: the Woodpecker pipeline URL; `startedOn`/`finishedOn`; `byproducts`: SBOM and vulnerability-report digests. Target level: **SLSA Build L2** (hosted build platform, signed provenance) with L3-oriented isolation of signing material (credentials outside the build step).
- **Trace attestation** (`https://strange.company/attestations/trace/v1`): `{tag, commit, previous_release_commit, pull_requests: [number], task_ids: [sc-…], pipeline}` resolved at release time from Forgejo (no `sc-pg` access from CI). The full AC-level trace is resolved on demand by `sc-release trace` (§7.3).

### 14.5 Verification policy

| Point | MVP | Mechanism | Failure effect |
|---|---|---|---|
| Promotion PR CI | **yes** | `sc-release verify-promotion` (offline, base-revision keys): every internal digest referenced at the PR head has a valid committed signature and provenance whose subject is that digest and whose `commit` is reachable from the base branch | PR check fails → no merge |
| Any PR changing `versions/platform.lock.yaml` | yes (for publishers listed) | `sc-release verify-upstream` (keyless, needs network, no secrets) | PR check fails |
| Runner dispatch (harness images) | **yes** | online `cosign verify` of the harness digest against `sc-cosign-trusted-keys` (non-revoked keys), cached per (digest, key-set revision) for 24 h | `HARNESS_NOT_ALLOWED` detail `unsigned` (plan 28) |
| Bootstrap verify V-26 (P12; CD-19) | yes | `sc-release verify-running --mode bootstrap` via `./bootstrap exec`: every running image is digest-pinned and listed in a lock; every running **internal** image (toolbox, ci, `sc-ci-config`, `sc-release`, `restore-tester`, and any other GHCR-published Strange Company image) verifies against the `scope: upstream` keyless identity in `security/cosign/trusted-keys.yaml` (§14.9; review NC-4). No unsigned exception exists | P12 fails |
| After P11 | yes | `sc-release verify-running --mode strict`: every running internal image is signed by this installation's key | 23-AC-17 |
| Promotion PRs (`promote/*`), release origin | **yes** (review SEC-11) | the Runner posts required status `strange-company/release-provenance` after checking that the provenance `invocationId` names an existing Woodpecker pipeline with event `tag`, run on the registered build agent's ID, for a tag that exists, is protected, was pushed by an allowed identity and points to a commit on `main` (plans 26/55 implement; context added to the non-task merge path) | PR not mergeable |
| Daily | yes | CronJob `supply-chain-signatures` (running digests from Prometheus `kube_pod_container_info`, verify each internal one; any internal digest not in a Git lock fails) | `SCUnsignedImageRunning` |
| Daily | yes | CronJob `supply-chain-vulns`: re-scan the SBOMs of every digest in the locks with a fresh trivy DB; Critical-with-fix not waived (or waiver expired) fails | `SCVulnerabilityCriticalDeployed` |
| Admission | **post-MVP** | sigstore policy-controller or Kyverno `verifyImages` for `strange-company`, `agent-jobs`, `hermes`, `ci-*` (H-2, plan 34); requires a webhook component (ADR-0014 revisit) | — |

### 14.6 Renovate

- CronJob `renovate/renovate`, image `ghcr.io/renovatebot/renovate@sha256:…` (full image; pinned in `platform.lock.yaml`; baseline: Renovate 44.x, plans/compatibility-baseline.yaml, a major is a plan amendment), `HTTPS_PROXY`/`NO_PROXY` set to the `:3132` Renovate listener and Forgejo (SEC-B13), token `sc-renovate` limited to the repositories it manages with write at most and never approve or merge (CD-38), schedule `17 */6 * * *`, PSA restricted (non-root), no ServiceAccount token, Secret `forgejo-sc-renovate` mounted as file and passed via `RENOVATE_TOKEN` (exception listed in `security/secret-env-exceptions.yaml`).
- `renovate.json5` essentials: `platform: "forgejo"` (Renovate's Forgejo platform; fallback `gitea`, verify), `endpoint: "http://forgejo-http.forgejo.svc:3000/api/v1"`, `repositories: ["strange-company/strange-company", …managed project repos]`, `automerge: false`, `platformAutomerge: false`, `prConcurrentLimit: 5`, `prHourlyLimit: 2`, `branchPrefix: "renovate/"`, `labels: ["dependencies"]`, `dependencyDashboard: true`, `allowedPostUpgradeCommands: []`, `allowScripts: false`, `ignoreScripts: true`, `pinDigests: true`, `osvVulnerabilityAlerts: true` (security updates bypass the weekly schedule), `schedule: ["before 6am on monday"]` for non-security updates, `packageRules` disabling Kubernetes/k3s **minor/major** bumps (plan 48 handles them), custom regex managers for `versions/*.lock.yaml`, `toolbox/tools.lock.yaml` (version + checksum via the `github-release-attachments` datasource digest support, verify; otherwise the PR fails `locks-check` and a human runs `./bootstrap exec -- make locks-refresh` on the branch), managers `gomod` (with `gomodTidy`), `dockerfile`, `flux`, `woodpecker`, `github-actions`, `npm`. A `packageRules` entry for `images.hermes` (plan 24 §9, 24-AC-18) uses `versioning: "regex:^v(?<major>0)\\.(?<minor>\\d+)\\.(?<patch>\\d+)$"` together with `allowedVersions: "/^v0\\.\\d+\\.\\d+$/"`. Calendar tags (`v2026.*`), `latest`, `stable` and pre-release tags therefore never match, so the old calendar tags can never rank above the semver line, and Renovate cannot propose a downgrade across the tag-scheme change. It also sets `minimumReleaseAge: "14 days"`, which is the plan 24 soak period.
- Renovate PRs are non-task PRs (plan 55): Woodpecker `ci` + `verify-upstream` + one human approval. No automerge in MVP (SPEC §127).
- Liveness: `SCRenovateStale` (check-cronjob semantics on `renovate/renovate`, maxAge 26h).

### 14.7 Toolbox publication (ADR-0013, plan 02)

On every `v*` tag whose build set contains `toolbox` or `ci`: build `linux/amd64` with buildah, sign, push to the Forgejo registry and GHCR (`SC-GHCR-PUSH`), and record the GHCR digests and inputs id in `toolbox/image.lock` via the promotion PR. Operators pull by digest; a missing or unreachable image falls back to the local build (plan 02). Plan 02 asks for multi-arch (`linux/arm64`) indexes; cross-architecture `RUN` steps need emulation that the unprivileged build lane cannot provide, so MVP publishes amd64 only (open question 2).

### 14.8 Trace query

`./bootstrap exec -- sc-release trace git.<domain>/strange-company/agent-runner@sha256:<d>` per §7.3. To start from a **running** artefact: `kubectl get pod -n strange-company -l app.kubernetes.io/name=agent-runner -o jsonpath='{.items[0].status.containerStatuses[0].imageID}'` gives the digest. SPEC §129's list maps as: models/harnesses/costs → ledger (plans 32/53) per task; test that specified the behaviour and that it failed first → RED evidence; implementation that made it green → GREEN evidence and commit; CI run → evidence pipeline; commit that shipped → provenance `commit` + promotion PR.

### 14.9 Upstream publication signing (review NC-4, CD-19)

Images that a fresh installation runs **before** its own P11 (toolbox, ci, `sc-ci-config`, `sc-release`, `restore-tester`) are published to GHCR by the **upstream project's** publication workflow `.github/workflows/publish.yaml` on protected `v*` tags of the GitHub repository (the interim lane of ADR-0018, and afterwards the upstream project's own release process). That workflow signs every published digest with **cosign keyless signing via GitHub Actions OIDC** (Fulcio certificate, Rekor transparency-log entry; the images are public, so public-log publication leaks nothing). Verification identity, committed as the `scope: upstream` entry of `security/cosign/trusted-keys.yaml`:

```yaml
- id: upstream-github-publish
  scope: upstream
  type: keyless
  issuer: https://token.actions.githubusercontent.com
  subjectRegex: '^https://github\.com/<upstream-owner>/strange-company/\.github/workflows/publish\.yaml@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+(-rc\.[0-9]+)?$'
  # extra Fulcio certificate claims, all required (blind review SEC-B9):
  certificateGithubWorkflowRef: refs/tags/v*            # same tag ref as the subject
  certificateGithubWorkflowTrigger: push
  certificateExtensions: {githubWorkflowName: publish, githubEnvironment: release}   # verify the OIDC claim names for the baseline cosign
  status: active
```

**Binding to workflow, environment and commit (SEC-B9, residual R-30 of plan 34).** `publish.yaml` runs its signing job in the GitHub Environment `release`, which requires a human reviewer (not the mirror user) and restricts deployment to `v*` tags; the job fails unless `GITHUB_SHA` is reachable from the mirrored `main` of the repository (`git merge-base --is-ancestor`) and the tag is a signed tag. Verification therefore requires (a) the workflow path and tag ref in the subject, (b) the `release` environment claim, (c) the signing commit (certificate claim `sourceRepositoryDigest`) to be an ancestor of the verifier's Git `main` and, at P12, to equal the commit recorded next to the pinned digests in `toolbox/image.lock`. A leaked mirror PAT (Contents and Workflows write) can therefore push a workflow change and a tag but cannot obtain a valid signature without the environment reviewer's approval. ADR-0253's sentence "GitHub Actions run with read-only tokens and no secrets" is **contradicted** by this workflow (`packages: write`, `id-token: write`): the real posture is read-only Actions by default with exactly this workflow granted those two permissions through the `release` environment; the ADR text is owned by plan 17 (cross-owner item below).

`<upstream-owner>` is the GitHub owner whose GHCR namespace the pinned digests name (an operator who republishes from a fork changes it by PR). `sc-release verify-running --mode bootstrap` and V-26 accept only this identity or the installation's own key; verification needs network access to the public Sigstore trust root (TUF) and Rekor (cosign v3.1.x flags verified by 23-AC-23; Sigstore bundles saved with `--bundle` may replace Rekor lookups offline).

### 14.10 Bootstrap capsule workflow (CD-33; plan 45 §9.4; implemented at H04)

`.woodpecker/capsule.yaml`, protected mode, `labels: {sc-lane: build}`:
- **Triggers:** `tag` (every release, after the release workflow's `gate` passed for the same commit) and `cron` `capsule-refresh` (daily, branch `main`, created by a Woodpecker admin), which builds only when the sha256 of any capsule-relevant lock file (`toolbox/image.lock`, `versions/k3s.lock.yaml`, `versions/charts.lock.yaml`, `versions/images.lock.yaml`, `.terraform.lock.hcl` files, `infra/ansible/requirements.lock.yml`) differs from `capsule/LATEST.json`'s capsule, or the newest capsule is older than 25 days (so refresh lands within 24 h of an upgrade merge and at least every 30 days).
- **Steps** (sequential, one artifact at a time to fit the 10Gi workspace, CD-06): `collect` (no credentials: pulls each pinned artifact through the build egress listener, verifies upstream checksums/signatures where published, writes it to the workspace and streams it out in `publish` before fetching the next large one) → `publish` (`sc-release` plugin, action `capsule`: self-check, upload each object with `SC-OBJECT-STORAGE/capsule-writer` and an explicit 35-day Object Lock retention, then `capsule.json` (digest manifest: path, size, sha256 of every file; image digests; chart versions/digests; the release commit; lock-file sha256s), then `cosign sign-blob --bundle capsule.json.sig` with `SC-RELEASE-SIGNING`, then `LATEST.json` last with `If-Match` on the previous ETag).
- **Credentials:** Woodpecker secrets `sc_capsule_writer`, `sc_capsule_cosign_key`, `sc_capsule_cosign_password` (events `[tag, cron]`, image filter `sc-release`); the self-check for `cron` requires event `cron`, cron name `capsule-refresh`, branch `main` (protected) and the commit equal to the `main` head. This is the only use of the signing key outside tag releases, and it signs only the capsule manifest (no image).
- **Verification key:** the recovery bundle carries a copy of `security/cosign/trusted-keys.yaml` (`public_files`, plan 35 §14.4), so `./bootstrap recover --from-capsule` verifies `capsule.json.sig` without GitHub.

## 15. Failure modes

| Failure | Detection | Effect | Automated response | Procedure |
|---|---|---|---|---|
| Build fails (code, network, pinned download changed) | step failure on the tag pipeline | no release | none | fix and re-tag (`v…-rc.n+1`) |
| Vulnerability gate fails | exit 12 `VULN_GATE_FAILED`, report in evidence | no release | none | update base/dependency via PR, or add a waiver PR |
| Off-cluster lane missing/failed | `OFFCLUSTER_LANE_*` | no release | none | plan 52 procedure |
| Self-check refuses | exit 13; failed tag pipeline visible to the tagger; no promotion PR | no publish; possible attack on the release path | none (the plugin holds no Alertmanager credential by design) | `credential-compromise.md#ci-and-supply-chain` |
| Registry push fails | exit 14 | partial publish (some images) | promote skipped | rerun the pipeline (idempotent, §17) |
| Promotion PR CI fails | PR check | not merged | none | investigate attestation mismatch |
| Unsigned/unknown image running | `SCUnsignedImageRunning` | possible tampering or drift | none | `signing-key-rotation.md` §investigate |
| Critical vuln in deployed image | `SCVulnerabilityCriticalDeployed` | exposure | none | Renovate/human update PR, re-release |
| Signing key compromise | operator report, unexpected signatures | trust root lost | none | `signing-key-rotation.md#emergency-revocation` |
| Renovate stale | `SCRenovateStale` | updates stop | none | check CronJob logs, token (`rotate-secrets.md#sc-renovate`) |
| Upstream digest withdrawn | `locks-verify-online` nightly (plan 52) | future rebuilds fail | none | pin replacement via PR |
| Forgejo registry unavailable | pull errors, `SCForgejoDown` | new pods cannot start; running pods unaffected | none | `restore-forgejo.md` |

## 16. Retry behavior

- No automatic retries of release workflows (plan 52 §16 rule). A human re-runs the pipeline; `sc-release` steps are idempotent.
- `gate` polls (60 s, ≤ 90 min) by design.
- Registry/HTTP calls inside `sc-release`: 3 attempts with 2/4/8 s backoff on 5xx/timeouts.
- Renovate: next schedule.

## 17. Idempotency behavior

- `publish`: before pushing, checks whether the digest already exists and is already signed by the active key with identical attestations; then skips (re-runs after a partial failure only fill gaps).
- `promote`: if `promote/<tag>` exists with the same tree, reuses it; if an open PR exists, updates its body only.
- `supply-chain init`: check-then-act per artefact (key file present and committed → skip; Woodpecker secret present with matching update marker → skip).

## 18. Bootstrap behavior

0. **Order (CD-19):** P11 is not part of the Bootstrap DoD. It runs after `CONTROL TRANSFER COMPLETE`, as the documented operator procedure below ("supply-chain init"), and gates C01.
1. Phase 1: the upstream publication workflow (§14.9) builds, **signs (cosign keyless, GitHub OIDC)** and publishes the bootstrap images (`toolbox`, `ci`, `sc-ci-config`, `sc-release`, `restore-tester` (plan 44, used by `dr-tests` until this installation's first trusted release, STO-1)) to GHCR; digests are pinned in Git by human PRs. They are not signed with `SC-RELEASE-SIGNING` (which does not exist yet); their trust is the reviewed digest pin **plus** the upstream keyless signature that V-26 verifies at P12.
2. P08: Woodpecker + `sc-ci-config` up (plan 20); build lane exists but no release secrets.
3. P11 supply-chain init (operator procedure `procedures/supply-chain-init.md`, after transfer): (a) confirm the release-lane protections first — 23-AC-20 (deploy keys cannot create `release/*` or push protected tags) must pass, otherwise stop; (b) `./bootstrap exec -- sc-bootstrap supply-chain init` → `SC-AGE-CI` (sealed into a new bundle version), cosign key pair, bots, tokens, tier-1b SOPS files, Woodpecker secrets (`ci-secrets apply`), trusted-keys PR (human merge); (c) Then the operator tags `v0.1.0`; the first trusted release builds **every** internal image and opens the first promotion PR, which switches all internal references from GHCR bootstrap digests to signed Forgejo-registry digests. P11 gate: §23.
4. C01 can begin once the Runner image digest is promoted.

## 19. Upgrade behavior

- buildah, cosign, syft, trivy, sc-release: pinned; Renovate PRs; a builder or signer upgrade must pass 23-AC-1/5 in the PR (k3d build test in the off-cluster lane).
- Cosign major upgrades (baseline v3.1.x; a newer major is a plan amendment) re-verify flag semantics (tlog, bundle format) and keep verifying signatures produced by the previous version (fixture signatures under `security/testdata/`).
- Key rotation: `signing-key-rotation.md` (yearly).

## 20. Recovery behavior

- Registry contents are restored with Forgejo (S3 `forgejo-objects`, plan 16/45). If lost, re-run the release for the current tag: images are reproducible from Git **up to non-deterministic layers**, so digests may differ; the new promotion PR replaces references and committed attestations.
- Committed signatures and provenance in Git survive registry loss, so the trace query works from Git alone for every deployed digest.
- Signing key loss → rotation (previous signatures stay verifiable with the retired public key).
- Black start: toolbox from GHCR or local build; internal images come back with Forgejo (ADR-0013); until then nothing internal can start, which plan 45 orders.

## 21. Observability

**Alerts** (catalogue names, `category: security` except Renovate):

| Alert | Source | Severity |
|---|---|---|
| `SCUnsignedImageRunning` | check-cronjob `supply-chain-signatures` (maxAge 26h) | critical |
| `SCVulnerabilityCriticalDeployed` | check-cronjob `supply-chain-vulns` (maxAge 26h) | warning |
| `SCRenovateStale` | check-cronjob `renovate/renovate` (maxAge 26h) | warning (category `ci`) |

Metrics: KSM CronJob series; release summary attestation per image (`https://strange.company/attestations/release-summary/v1`: digests, durations, workspace peak, vuln counts by severity). Grafana (plan 39) may show release history from the summary objects (post-MVP).

## 22. Testing strategy

- **Unit (Go, `sc-release`):** inputs-id computation; Containerfile lint rules (positive/negative fixtures); waiver schema and expiry; trusted-key status handling (active/retired/revoked); offline signature verification with fixture keys; provenance construction (golden JSON); trace resolver with a fake Forgejo and fake Runner; self-check decision table.
- **Integration (off-cluster lane, tier E, `make e2e-supply-chain`):** k3d with Forgejo (pinned) as registry, a fake Woodpecker environment (env vars), PSA baseline namespace + plan 37 policies: buildah builds a fixture image under admission; `sc-release publish` with a test key pushes and signs; `cosign verify` passes; `verify-promotion` passes on the generated PR tree and fails on tampered copies.
- **Live (P11):** first real release (§18 step 3) and the checks.
- No paid model APIs are involved anywhere.

## 23. Acceptance criteria

### GATE-P11

- [ ] **23-AC-1** Build mechanism (version re-verification, decides ADR-0501): in k3d at the pinned k3s version, a pod in `ci-build` (PSA baseline, plan 37 policies, uid 0, capabilities exactly §13, seccomp RuntimeDefault, no privileged) runs `buildah build --isolation=chroot --storage-driver=vfs` on `test/supply-chain/fixtures/run-steps/Containerfile` (contains `RUN apt-get install`, `RUN useradd`, `RUN chown`) and writes an OCI archive whose `index.json` validates; the same pod spec with `privileged: true` is denied (`ADM-45`).
- [ ] **23-AC-2** (with 20-AC-12..19) A PR adding a workflow with `labels: {sc-lane: build}` and `from_secret: sc_cosign_key` produces no step in `ci-build` (`kubectl -n ci-build get pods -l strange.company/ci-lane=build` stays empty during the run) and no secret injection.
- [ ] **23-AC-3** Release-credential confinement (review SEC-3): the release workflow reads the injection mechanism from `versions/compat.yaml` `woodpecker.secretInjection` (`inline` | `secret-ref`), which plan 20's 20-AC-38 records and re-verifies at every Woodpecker bump; `sc-release plan` fails closed when the key is missing. If `inline`, the release workflow switches to a Secret `ci-build/sc-release-credentials`, created out-of-band by `sc-bootstrap ci-secrets apply` (from tier 1b, never Flux), mounted only into the `sc-release` pod through `backend_options.kubernetes.secrets` (verify) and allowed by ADM-46 for that image only; the test then re-runs and finds no value in any Pod spec (E48). In both cases: on a test tag, (a) a `commands` step using the `sc-release` image and (b) a plugin step using `golang@sha256:…`, both referencing `sc_cosign_key`, do not receive the value (step prints `${#SC_COSIGN_KEY}` = 0) or are rejected; and a pod spec carrying `SC_COSIGN_KEY` env on a non-`sc-release` image is denied by `ADM-46`.
- [ ] **23-AC-4** `sc-release publish` invoked with a tag that is unprotected, or whose commit is not reachable from `main`/`release/*` (fake Forgejo responses), exits 13 before opening any credential file (asserted by an audit hook in the test harness).
- [ ] **23-AC-5** After the first trusted release `v0.1.0`: for every image in `versions/internal-images.lock.yaml`, `./bootstrap exec -- cosign verify --key security/cosign/<active>.pub --insecure-ignore-tlog=true git.<domain>/strange-company/<image>@<digest>` exits 0 (cosign v3.1.x baseline; legacy bundle formats rejected); `cosign verify-attestation --type slsaprovenance1` and `--type spdxjson` exit 0; the registry stores signatures as OCI 1.1 referrers (v3 default) or, if the Forgejo registry lacks referrers, as `sha256-<digest>.sig` tags (recorded in `versions/compat.yaml` `registry.referrers`).
- [ ] **23-AC-6** (hardening probe, non-blocking for P11) The 23-AC-1 pod with `hostUsers: false` succeeds or the observed error is recorded in `versions/compat.yaml` (`userNamespaces.buildah: unsupported|supported`).
- [ ] **23-AC-7** Vulnerability gate: a release including `test/supply-chain/fixtures/vulnerable-base/Containerfile` (base digest with a known Critical fixed CVE, pinned fixture) exits 12 `VULN_GATE_FAILED` and pushes nothing (registry tag absent); with a waiver for that CVE (`expires` in 7 days) it passes; `make vuln-waivers-check` fails on a waiver whose `expires` is in the past or more than 30 days after `created` without a second approver.
- [ ] **23-AC-8** The promotion PR is authored by `sc-release`, its head branch is `promote/v0.1.0`, and `git diff --name-only main...promote/v0.1.0` lists only paths matching `versions/internal-images.lock.yaml`, `toolbox/image.lock`, `policy/harnesses.yaml`, `gitops/platform/admission/generated/**`, `charts/strange-company/values-images.yaml`, `security/attestations/**`; `sc-release verify-promotion` passes on it and fails (exit 10) when one byte of one `signature.json` is changed.
- [ ] **23-AC-9** CI never deploys: `make lint-ci` passes and its negative fixture (a workflow step running `kubectl apply`) fails; `kubectl auth can-i --list --as=system:serviceaccount:ci-build:default` shows no resource rights; no `ci-build` pod mounts a ServiceAccount token (`ADM-45`).
- [ ] **23-AC-10** Off-cluster gate negative test: `sc-release gate --offcluster --dry-run <sha>` against a commit without an `e2e` run exits 11 with `OFFCLUSTER_LANE_MISSING` (shared with 52-AC-20).
- [ ] **23-AC-11** Trace: `./bootstrap exec -- sc-release trace git.<domain>/strange-company/agent-runner@<digest> --json` returns JSON with `source.commit` equal to the provenance commit, `build.pipeline` resolving to an existing Woodpecker pipeline, a non-empty `pull_requests` list, and `deployed_by.promotion_pr` equal to the merged promotion PR number; the output validates against `contracts/release-trace.schema.yaml`.
- [ ] **23-AC-12** Registry credential separation: `docker`/`crane` push with the `sc-registry-reader` token returns 401/403; pull with it succeeds; pull without credentials of an internal image returns 401.
- [ ] **23-AC-13** Renovate: `kubectl -n renovate create job --from=cronjob/renovate rn-test` completes; against the fixture repository `sc-e2e/renovate-fixture` (an outdated digest-pinned image and Go module) it opens ≥ 1 PR authored by `sc-renovate` with label `dependencies`; `jq '.automerge, .platformAutomerge'` on the resolved config prints `false false`; the job log contains no `postUpgradeTasks` execution.
- [ ] **23-AC-14** Toolbox publication: `toolbox/image.lock` after the promotion PR names GHCR digests; on a fresh clone at that commit `./bootstrap version` reports source `pulled`; `cosign verify --key security/cosign/<active>.pub --insecure-ignore-tlog=true ghcr.io/<owner>/strange-company-toolbox@<digest>` exits 0.
- [ ] **23-AC-15** `make lint-containerfiles` exits 0 on `main`; each negative fixture (tag-only `FROM`, unverified `curl`, `npm install`, `pip install` without hashes, `apt-get` without snapshot) exits non-zero naming its rule.
- [ ] **23-AC-16** Checks: `supply-chain-signatures` and `supply-chain-vulns` CronJobs complete with exit 0 on a healthy install; deploying a digest-pinned but unsigned internal image in `apps` (test) makes `supply-chain-signatures` exit 1 and `SCUnsignedImageRunning` fire within `failAfter + 2m`.
- [ ] **23-AC-17** After the first trusted release and its promotion PR are merged, `./bootstrap exec -- sc-release verify-running --mode strict` exits 0 (V-26 in bootstrap mode is evaluated at P12, §14.5).
- [ ] **23-AC-20** (review SEC-1, escape case E47) With a write deploy key of `strange-company`: `git push` of a new branch `release/sc-probe` and of tag `v0.0.0-probe` are both rejected by Forgejo; a tag pushed by a human on a commit reachable only from an unprotected branch starts no build-lane pod; and `sc-release publish` run against a fixture where `GET /v1/ci/push-records` reports the tag pushed with kind `deploy_key` (or by a user outside `security/release-pushers.yaml`, or returns no record) exits 13 before opening any credential. If either Forgejo push succeeds (plan 17 17-AC-4 / plan 16), P11 **fails** (no residual-risk acceptance).
- [ ] **23-AC-21** (review SEC-11) A promotion PR whose attestations are validly signed but whose provenance `invocationId` names a non-existent pipeline (forged-provenance fixture) gets `strange-company/release-provenance` = failure and cannot be merged.
- [ ] **23-AC-23** (review NC-4) For each GHCR digest in `toolbox/image.lock` and the bootstrap entries of `versions/internal-images.lock.yaml`, `./bootstrap exec -- cosign verify --certificate-oidc-issuer https://token.actions.githubusercontent.com --certificate-identity-regexp '<subjectRegex>' --certificate-github-workflow-ref <tag-ref> <image>@<digest>` exits 0, and fixture images signed by a different workflow path, outside the `release` environment, or from a commit not reachable from `main` (SEC-B9) fail.
- [ ] **23-AC-25** (verify, baseline cosign v3.1.x) `cosign sign --key env://… --tlog-upload=false`, `cosign attest --type spdxjson|slsaprovenance1|vuln` and `cosign sign-blob --bundle` run with the pinned cosign without deprecated-flag errors; signature storage (referrers or tags) is detected and recorded; `cosign verify` of a fixture signed by cosign v2.6.x (`security/testdata/`) still passes, and a legacy-format bundle is rejected.
- [ ] **23-AC-26** (SEC-B13) The `renovate` pod's only egress path is the `:3132` listener (from GATE-C02): a request to a host outside the Renovate profile through `:3132` is denied, direct 443 from the pod is dropped by NetworkPolicy, and the `sc-renovate` token cannot approve or merge (34-AC-10).
- [ ] **23-AC-27** (SEC-B9) A fixture workflow run outside the `release` environment, or whose commit is not an ancestor of mirrored `main`, produces a signature that `sc-release verify-running --mode bootstrap` rejects.
- [ ] **23-AC-22** (CD-21) With `cloudflared` scaled to 0, deleting the Runner pod (cold image cache on that node: `crictl rmi` of the Runner image first) brings it back Ready; the node pulls from `git.<domain>` via `10.43.0.20`.
- [ ] **23-AC-19** Workspace budget (CD-06): during the `toolbox` build of the first trusted release, the peak `du -sx /woodpecker` (sampled every 10 s by the build step and printed as a final `workspace-peak-bytes=<n>` line in the step log, which `publish` copies into the release summary attestation) is ≤ 9 GiB, and the workflow's PVC request is ≤ 10Gi.
- [ ] **23-AC-18** SBOM content: the SPDX SBOM of `agent-runner` lists the main module with version equal to the release commit and every module of `go list -m all` used by `src/cmd/agent-runner` (set comparison in the test).

### GATE-H04 (contributed; CD-33)

- [ ] **23-AC-24** After a release tag, `s3://<prefix>-recovery/<cluster>/capsule/LATEST.json` names a capsule whose `capsule.json` verifies with `cosign verify-blob --key security/cosign/<active>.pub --bundle capsule.json.sig` using the key copy from the unlocked recovery bundle; every listed object has Object Lock retention ≥ 35 days; a `DeleteObject` with `capsule-writer` is denied; merging a PR that changes `versions/k3s.lock.yaml` produces a new capsule within 24 h (cron `capsule-refresh`), and plan 44's `rt-bootstrap-capsule` passes.

## 24. Explicit non-goals

- Keyless (Fulcio/OIDC) signing of internal images and public transparency-log uploads (private registry; key-based signing is simpler to operate offline). Revisit post-MVP.
- Admission-time signature verification in MVP (post-MVP, §14.5).
- SLSA Build L3 certification; reproducible bit-for-bit images.
- Multi-arch internal images in MVP.
- Automerge of any dependency update in MVP [SPEC §127].
- Mirroring all third-party images into the Forgejo registry (ADR-0013: it does not help black start).

## 25. Open questions

1. **Woodpecker secret semantics** (blocking: **no**; re-verified by 23-AC-3): whether image-filtered secrets are withheld from `commands` steps, and the env-var mapping of `from_secret`. A safe fallback exists, so P11 is not blocked: (a) ADR-0301's exclusive config extension already guarantees that only protected-mode configurations from reviewed `main`/protected tags can reference any secret at all; (b) secrets are scoped to events `[tag]` on protected tags; (c) the only steps in `release.yaml` that reference them use the digest-pinned `sc-release` image without `commands`, and the release workflow file is itself reviewed; (d) admission `sc-ci-build-release-steps` (ADM-46) denies any `ci-build` pod carrying release-secret env on another image or with an entrypoint override. If 23-AC-3 shows Woodpecker does inject image-filtered secrets into `commands` steps, (a)–(d) still hold; the residual is a malicious change to `release.yaml` itself, which requires a human-merged PR (plan 34 R-15). Owner: 20/23.
2. **Multi-arch toolbox** (blocking: no): plan 02 specifies `linux/amd64` + `linux/arm64` indexes. The unprivileged build lane cannot emulate arm64. Proposed: amd64 in MVP; arm64 operators use the local build path; post-MVP arm64 via an operator-provided off-cluster agent (ADR-0303 §5 style). Owner: 02/23.
3. **Workspace size** (blocking: no): resolved by CD-06 — 10Gi cap, sequential per-image build/publish (§14.3 step 5); 23-AC-19 measures the peak. Owner: 23.
4. **Forgejo registry and cosign storage** (blocking: no): support for OCI referrers vs. tag scheme, and cleanup rules that keep `.sig`/`.att` (23-AC-5, plan 43). Owner: 16/43.
5. **Release cadence** (blocking: no): releases happen only on human-created tags, so merged Runner changes reach production at tag time. A self-hosting loop may want a faster path; a post-MVP `release-candidate` cron on `main` would need the same gate. Owner: 23/46.
6. **Upstream publisher list** in `upstream-signatures.yaml` (blocking: no): which pinned upstreams publish verifiable signatures is version-sensitive (verify at implementation). Owner: 23.
7. **Release-lane trust inputs from other plans** (blocking: no, **resolved in the closure round**; review SEC-1): plan 20 must serve the tag release config from the head of `main` (not the tag commit) and require reachability from `main` or `forge.yaml`-listed `release/*` branches; plan 16 must add ACs that a deploy key cannot create `release/*` or push protected tags; plan 17 must turn 17-AC-4's "not enforced" outcome into a failure. 23-AC-20 checks the combined result. Resolution: plan 20 §8.2 serves tag release config only for commits reachable from `main` or a protected `release/*` branch and checks `GET /v1/ci/push-records`; plan 16 has the deploy-key ACs; 17-AC-4 now fails outright. 23-AC-20 verifies the combination at P11. Owner: 16/17/20/23.
8. **`supply-chain init` procedure file**: resolved — `procedures/supply-chain-init.md` (CD-19). Owner: 23.
9. **Release-provenance status** (blocking: no for P11, yes for merging the first promotion PR after C09; review SEC-11): the Runner implements the `strange-company/release-provenance` check (plans 26/55). Until it exists, the human reviewer verifies the pipeline link in the PR body. Owner: 26/55.
