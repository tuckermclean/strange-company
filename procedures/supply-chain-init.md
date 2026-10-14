# Procedure: Supply-chain init (P11, after control transfer)

**Owning plan:** plans/23-supply-chain.md (secrets: plan 35; CI: plan 20)
**SPEC references:** §36, §37, §38, §116, §130 (P11 is outside the Bootstrap DoD, CD-19)
**Severity / urgency:** routine (one-time per installation; repeated after a signing-key or `SC-AGE-CI` loss)
**Expected duration:** 1.5–3 h, mostly the first release build and the off-cluster e2e gate
**Requires break-glass:** no (operator kubeconfig, recovery passphrase, the operator's Forgejo account for approvals)

## When to use

- After `CONTROL TRANSFER COMPLETE` (P12), to establish this installation's own trusted release chain (DAG node P11, which gates C01).
- After a black start in which the release credentials could not be recovered (tier 1b lost).

## Preconditions and required material

- `./bootstrap verify` passes (P12), with V-26 in bootstrap mode.
- Woodpecker is trusted CI (P08); `sc-ci-config` serves configurations; the build agent `woodpecker-agent-build` is registered (`SC-WOODPECKER-AGENT/build`).
- `versions/compat.yaml` contains `woodpecker.secretInjection` (recorded by plan 20's 20-AC-38).
- Recovery passphrase (the bundle gains `SC-AGE-CI`); a GitHub token with `write:packages` for the toolbox package (`SC-GHCR-PUSH`); access to the mirror's GitHub Actions results (`SC-GITHUB-PROBE`, Actions read).

## Safety checks before acting

1. **Release-lane protections (review SEC-1).** Run `./bootstrap exec -- make test-release-lane` (23-AC-20): a write deploy key of `strange-company` must be refused when creating `release/sc-probe` and pushing tag `v0.0.0-probe`. **If either push succeeds, stop.** Do not create release credentials until plans 16/17 fix protection; a worker could otherwise trigger releases.
2. `kubectl -n ci-build get pods` is empty and no release workflow is running.
3. Never paste credential values into chat, issues or PR bodies; the tool reads them from prompts into tmpfs only.

## Steps

1. **Generate and seal.** `./bootstrap exec -- sc-bootstrap supply-chain init`.
   - Prompts the recovery passphrase, generates `SC-AGE-CI` if absent and seals a new bundle version (`rotated_classes: [SC-AGE-CI]`); you re-enter the passphrase to confirm the S3 copy.
   - Generates the cosign key pair (`SC-RELEASE-SIGNING`) and creates bots `sc-release` and `sc-registry-reader` with their tokens (`SC-REGISTRY-PUSH`, `SC-FORGEJO-BOT-TOKENS/sc-release`, `SC-REGISTRY-PULL`) through the Forgejo admin API; prompts for `SC-GHCR-PUSH`.
   - Writes tier-1b SOPS files under `secrets/ci/<cluster>/` (recipients `SC-AGE-CI` + recovery) and the `sc-registry-pull` SOPS Secrets (tier 1) for every consuming namespace.
   - *Expected:* `supply-chain init: staged`, the new `bundle_version`, and exit 75 with the URL of PR `supply-chain/init-<ts>` (opened as `sc-admin`) that adds `security/cosign/sc-release-<YYYY>a.pub`, the `trusted-keys.yaml` entry (`scope: installation`, `status: active`) and the SOPS files. *If exit 13:* the release-lane check of safety step 1 failed inside the tool; stop.
2. **Review and merge the PR.** `make secrets-lint` (S-3: tier-1b recipients) and the PR's `ci` pipeline must pass. Approve and merge it (`sc-admins`, label `security-review`).
3. **Apply the Woodpecker secrets.** Re-run `./bootstrap exec -- sc-bootstrap supply-chain init` (resumes): it waits for Flux to apply `sc-registry-pull`, then runs `ci-secrets apply` with a ≤ 1 h `SC-WOODPECKER-ADMIN` token. Secrets `sc_cosign_key`, `sc_cosign_password`, `sc_registry_push_token`, `sc_release_pr_token`, `sc_ghcr_push_token`, `sc_github_probe_token`: events `[tag]`, image filter `sc-release`. If `woodpecker.secretInjection: inline`, it instead creates the out-of-band Secret `ci-build/sc-release-credentials` (23-AC-3). *Expected:* `ci-secrets: applied 6` and a ledger entry per class.
4. **Tag the first release.** In Forgejo (as a human in `engineers`/`sc-admins`), create the protected tag `v0.1.0` on the current `main` head. The mirror pushes it to GitHub, which starts the off-cluster `e2e` lane (ADR-0303, including `e2e-c10`, CD-22).
5. **Watch the release workflow** (`https://ci.<domain>`, repository `strange-company`, event tag): `plan` → `gate` (≤ 90 min) → per image `build-<image>` / `publish-<image>` → `promote`. *If `gate` fails:* fix the failing e2e job by PR and tag `v0.1.0-rc.<n>`. *If `VULN_GATE_FAILED`:* update the dependency or add a time-limited waiver by PR, then re-tag. *If `publish` exits 13:* the self-check refused the tag (wrong pusher, unprotected tag or commit not on `main`); treat as a security event (`credential-compromise.md#ci-and-supply-chain`).
6. **Merge the promotion PR** `promote/v0.1.0` (author `sc-release`). Its `ci` pipeline runs `sc-release verify-promotion` with keys from `main`; once plans 26/55 deliver it, the `strange-company/release-provenance` status must be green too. Approve and merge. Flux rolls the platform onto the signed Forgejo-registry digests.

## Verification

- `./bootstrap exec -- sc-release verify-running --mode strict` exits 0 (23-AC-17).
- `./bootstrap exec -- cosign verify --key security/cosign/sc-release-<YYYY>a.pub --insecure-ignore-tlog git.<domain>/strange-company/agent-runner@<digest>` exits 0 (23-AC-5).
- `toolbox/image.lock` names the GHCR digests from this release; `./bootstrap version` on a fresh clone reports source `pulled` (23-AC-14).
- `./bootstrap exec -- sc-bootstrap recovery-bundle status` shows `SC-AGE-CI` in the inventory and `currency: ok`.

## Rollback / abort

- Before step 2: close the PR and run `sc-bootstrap supply-chain init --abort` (deletes the bots' tokens and the staged key; the sealed bundle keeps `SC-AGE-CI`, which is harmless).
- After step 3, before a release: `sc-bootstrap ci-secrets apply --delete-all-release` removes the Woodpecker secrets.
- A bad promotion: revert the promotion PR; the previous digests stay in the registry.

## Evidence to record

PR URLs (init, promotion), the release pipeline URL, the gate result (GitHub run id and attempt), the active key id, the new bundle version, and the `verify-running --mode strict` output, in the P11 gate issue.

## Follow-ups

- Calendar the signing-key rotation (`signing-key-rotation.md`, ≤ 1 year).
- Start C01 (the Runner image is now signed and promoted).
