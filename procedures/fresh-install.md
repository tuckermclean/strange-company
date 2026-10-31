# Procedure: Fresh installation

**Owning plan:** plans/50-bootstrap-orchestrator.md (with 02, 03, 04, 05, 06)
**SPEC references:** §7, §8, §11, §14, §15, §35, §111, §113 ("fresh installation"), §130
**Severity / urgency:** routine
**Expected duration:** 60 min of operator preparation (including GitHub preparation) + ≈ 150 min unattended pipeline (≤ 60 min provider delivery; the run warns above 180 min)
**Requires break-glass:** no

## When to use
Installing a new Strange Company from a clone, with profile `self-hosting-minimum`. Not for: resuming an interrupted install (`bootstrap-resume.md`), rebuilding after loss (`total-cluster-loss.md` / `github-black-start.md`, plan 45), adding nodes (`expand-to-small-ha.md`, plan 47).

## Preconditions and required material
- A Linux, macOS or WSL2 machine with **Git** and **Docker or Podman** (rootless is fine), 2 CPU, 4 GiB free RAM, 5 GiB free disk, outbound HTTPS and SSH.
- An **SSH key pair** (ed25519 preferred; RSA ≥ 3072), private key mode `0600`.
- **Compute:** either an OVHcloud account with a default payment method (`PROVIDER=ovh`), or SSH-reachable machines meeting SPEC §11 with passwordless sudo and Debian 13 or Ubuntu 24.04 (`PROVIDER=existing`).
- **S3-compatible object storage** account and an access key that can create buckets (or pre-created buckets, `S3_BUCKET_MODE=existing`; plan 14 lists them). The provider must support versioning and conditional writes (`If-None-Match`); preflight proves it.
- **Cloudflare** account with the zone for `BASE_DOMAIN` and an API token with plan 09's permission set.
- A **private GitHub repository** that will be the mirror: normally your own private copy of `strange-company`, the one you clone in step 1. It may contain history, but its `main` must be an ancestor of the commit you install (PF-16). Prepare it as in step 2a (plan 17 §14.1): organization and plan, rulesets or `GITHUB_MIRROR_PROTECTION_TIER=unavailable-accepted`, and three fine-grained tokens (mirror, probe, read).
- One **notification transport** (email, webhook, ntfy, Slack, Discord or Matrix bridge) and optionally an external heartbeat URL (ADR-0009).
- At least one **model API key** (plan 53).
- Somewhere safe **offline** to store a recovery passphrase and a small file (password manager + printed copy, or encrypted USB).

## Safety checks before acting
1. You are installing a **new** cluster name. If `gitops/clusters/<name>/` already exists in the repository, or `s3://<prefix>-tofu-state/<name>/journal/journal.json` exists, stop: that is an existing installation (`bootstrap-resume.md`).
2. Decide the paid footprint you accept: `MAX_MONTHLY_COST` must be ≥ the machine price (for one default OVH VPS a limit of 25 EUR is typical; plan 04 §9.2). An OVH VPS commits one month of spend the moment it is ordered.
3. Choose object storage in a **different region or provider** than the compute (SPEC §29; preflight warns otherwise).
4. Do NOT edit files under `gitops/clusters/`, `secrets/` or `.bootstrap/` by hand at any point.

## Steps

1. **Clone at a release tag.**
   ```sh
   git clone https://github.com/<owner>/strange-company.git
   cd strange-company
   git checkout -B main v<X.Y.Z>  # latest release tag on a local branch named main (PF-04 requires main; PF-05 warns on untagged HEAD)
   ```
   Expected: `git describe --exact-match` prints the tag. If not, either check out a tag or accept the PF-05 warning knowingly.

2. **Check the toolbox.**
   ```sh
   ./bootstrap version
   ```
   Expected: sc-bootstrap version, toolbox reference with `source: pulled`, and a tool table with every row `ok`. If it says `built`, the image is being built locally (≈ 10 min) — acceptable. If it fails with `WRAPPER_ENGINE_MISSING` or a build error, follow `bootstrap-preflight-failure.md#toolbox`.

2a. <a id="github-preparation"></a>**GitHub preparation (plan 17 §14.1; do it before preflight).**
   1. Use a GitHub **organization** on a plan that offers rulesets for private repositories, or accept `GITHUB_MIRROR_PROTECTION_TIER=unavailable-accepted` (weaker protection, recorded).
   2. Create a machine user for the mirror and a team `sc-mirror` with write access to the repository; disable secret-scanning push protection for that user's pushes as plan 17 describes (the mirror pushes SOPS-encrypted files).
   3. Create the two rulesets of plan 17 §14.1 (protected `main`/`release/*` and tags; only the mirror identity may push).
   4. Create three fine-grained tokens on the repository: **mirror** (`GITHUB_MIRROR_TOKEN`: Contents RW, Workflows RW, Metadata R), **probe** (`GITHUB_PROBE_TOKEN`: Contents R, Metadata R, Administration R, Actions R), **read** (`GITHUB_MIRROR_READ_TOKEN`: Contents R, Metadata R; used by the black-start restore test). Expiry ≤ 365 days; note the dates.
   Expected: `./bootstrap exec -- sc-bootstrap mirror check-github` (after step 5) reports every item `ok`. Otherwise fix per plan 17's table before continuing.

3. <a id="ovh-credential"></a>**OVH only: create a restricted API credential.** In the OVH console create an application (`https://<endpoint>/createApp/`, e.g. `eu.api.ovh.com`; verify), then request a consumer key restricted to the rules in plan 04 §10 (`POST /auth/credential` with those `accessRules`; validate the returned URL in the browser). Record application key, secret and consumer key. Expected: `./bootstrap preflight` later reports PF-07 `pass`. A credential with `/*` rules fails PF-07 `OVH_CREDENTIAL_TOO_BROAD`.

4. <a id="manual-ovh-order"></a>**OVH fallback (c) only** (if plan 04's spike disabled `PROVIDER=ovh`): order a VPS of the default class (plan 04 §9.2) in the OVH control panel with Debian 13 and your SSH public key, wait for delivery, note its IPv4 and the SSH host-key fingerprint shown by `ssh-keyscan -t ed25519 <ip> | ssh-keygen -lf -`, then continue with `PROVIDER=existing` in step 5.

5. **Write `bootstrap.env`.**
   ```sh
   cp bootstrap.env.example bootstrap.env
   chmod 600 bootstrap.env
   $EDITOR bootstrap.env
   ```
   Fill every required key (plan 50 §9.2; the example file documents each). Minimum for OVH: `CLUSTER_NAME`, `BASE_DOMAIN`, `ADMIN_EMAIL`, `PROVIDER=ovh`, `PROVIDER_REGION`, `EXPECTED_NODE_COUNT=1`, `MACHINE_CLASS` (default `ovh-vps-6c-12g-100nvme`, VPS-2, ≈ 6–8 EUR/month: meets the SPEC v1.2 minimums, below the preferred 200 GiB disk; `ovh-vps-8c-24g-200nvme`, VPS-3, ≈ 13–16 EUR/month, is the documented upgrade path if the P12 memory or disk gates fail, CD-24 — verify current prices). Machines with less than 100 GiB disk are rejected (SPEC v1.2), `MAX_MONTHLY_COST`, `OVH_*`, `SSH_PRIVATE_KEY_PATH`, `CLOUDFLARE_ACCOUNT_ID`, `CLOUDFLARE_API_TOKEN` (toolbox: tunnel + DNS), `CLOUDFLARE_ACME_TOKEN` (zone-scoped DNS:Edit for in-cluster ACME, plan 10), `S3_ENDPOINT`, `S3_REGION`, `S3_ACCESS_KEY_ID`, `S3_SECRET_ACCESS_KEY`, `GITHUB_MIRROR_REPO`, `GITHUB_MIRROR_TOKEN`, `GITHUB_PROBE_TOKEN`, `GITHUB_MIRROR_READ_TOKEN`, `GITHUB_MIRROR_PROTECTION_TIER`, `NOTIFY_TRANSPORT` + its settings, one model key. For `existing`: `PROVIDER=existing`, `EXISTING_NODES=node-01=<user>@<host>`, `MAX_MONTHLY_COST=0`, and preferably `EXISTING_NODE_OVERRIDES=node-01:host_key=SHA256:<fingerprint>`.
   Expected: the file is not tracked (`git status --porcelain bootstrap.env` prints nothing).

6. **Run preflight.**
   ```sh
   ./bootstrap preflight --profile self-hosting-minimum
   ```
   Expected: exit 0; the table shows only `pass`, `deferred` (PF-14 before the bucket exists) and understood `warn` rows. A test notification arrives at your transport (PF-17) — confirm you received it. On exit 2 or 3, follow `bootstrap-preflight-failure.md` at the anchor printed next to each failed check, fix, and rerun this step. This step is read-only and safe to repeat.

7. **Preview the plan (read-only).**
   ```sh
   ./bootstrap plan --profile self-hosting-minimum
   ```
   Expected: the `PAID FOOTPRINT` block (or `0 paid resources` for `existing`) with monthly total ≤ your limit, the committed minimum, and the bucket list. Nothing is created (no bucket, no commit). On exit 4 see `bootstrap-preflight-failure.md#guardrail`.

8. <a id="passphrase"></a>**Start the install and create the recovery passphrase.**
   ```sh
   ./bootstrap --profile self-hosting-minimum
   ```
   The tool first prints the **storage footprint** (buckets, Object Lock periods, keys to be created; object storage is billed by usage and locked objects cannot be deleted for their lock period) and asks for `storage <cluster>`. Type it exactly; nothing is created before.
   When prompted, choose a **recovery passphrase** (≥ 20 characters or ≥ 5 words, not containing the cluster name) and type it twice. Then the tool shows what to store offline and asks you to **re-enter the passphrase once more**; it downloads the bundle back from S3 and decrypts it.
   Expected: `recovery bundle confirmed`. Before continuing, store the passphrase offline **now**. If you have forgotten it at the confirmation prompt (3 failed attempts, exit 8): no compute exists yet, but buckets, the remote journal and the bundle do. Run `./bootstrap abandon-identity` and type `abandon <cluster> <uid8>` (BOOT-6): it archives the remote journal, re-imports the existing buckets into fresh storage state and lets the next `./bootstrap` generate a new identity with a new passphrase (the buckets are reused). Do **not** just delete local files: the rerun would be refused (PF-22). If compute was already confirmed, `abandon-identity` refuses; follow `recover-opentofu-state.md#lost-passphrase`.

9. **Confirm the footprint and the model budget.** The tool prints the footprint again and asks for `create <cluster> <D8>` (OVH) or `<cluster>` (existing), then prints the model budgets (`policy/budgets.yaml`) and asks for `budget <cluster> <monthlyUsd>`. Type both exactly. For an unattended run, pass the same values as `SC_CONFIRM_STORAGE=<cluster>`, `SC_CONFIRM_PLAN=<D8>` (from `./bootstrap plan`), `SC_CONFIRM_BUDGET=<monthlyUsd>` with `--non-interactive`.
   Expected: provisioning starts. Typing anything else 3 times exits 5 with nothing created; rerun step 8 when ready (the passphrase is asked again, the bundle is reused).

10. **Wait.** Progress lines show `[n/9 <phase>]` with a heartbeat every 30 s. Do not close the terminal; if it closes or the machine sleeps, the run stops consistently — resume with `bootstrap-resume.md`.
    Expected phases: provision (≤ 60 min for OVH delivery) → seed-prep (tunnel, DNS) → configure (≤ 40 min) → seed (≤ 45 min, `SEED COMPLETE <sha>`) → forge-init → flux-bootstrap → sso-init → ci-init → verify (reconcile wait, the first run of every restore test, settle and the 30 min memory window). Nothing needs your input during this time; test notifications arrive at your transport.

11. **Read the final report.** The run ends with the block whose first line is exactly `CONTROL TRANSFER COMPLETE` (plan 50 §14.11) and exit 0. Re-print it any time with `./bootstrap report`.
    If verify fails (exit 1, no report), follow `bootstrap-verify.md`.

12. **Secure black-start material.**
    - Run `./bootstrap exec -- age-keygen -y /blackstart/<cluster>/recovery-identity.txt`. Expected: it prints the `age1…` recovery recipient that `.sops.yaml` lists for `<cluster>`. If it differs, stop and report a bug (the identity does not match the encrypted files).
    - Copy `~/.strange-company/<cluster>/recovery-identity.txt` to offline storage, re-run the same `age-keygen -y` check against the copy (from the offline medium), then delete the local file. PF-23 stops warning.
    - Keep `bootstrap.env` in your password manager (it holds provider, DNS, S3, GitHub and model credentials).
    - Keep a copy of the bundle file (`~/.strange-company/<cluster>/bundles/`) offline in addition to the S3 copy.

13. **Configure the heartbeat** (if `HEARTBEAT_URL` was empty): register a healthchecks-style check with your provider and add the URL by PR (plan 42 procedure). Until then the report says "total-outage detection is not configured".

## Verification
- `./bootstrap status --json | ./bootstrap exec -- jq -r '.installation.state, .recovery_bundle.status'` prints `transferred` and `confirmed`.
- `./bootstrap verify` exits 0 (re-runs all checks; takes ≈ 15–45 min).
- `https://git.<BASE_DOMAIN>/strange-company/strange-company` shows the repository; its `main` SHA equals GitHub's mirror `main` (report line "GitHub mirror … lag").
- `./bootstrap exec -- kubectl get nodes` lists `node-01 Ready`.
- Log in at `https://auth.<BASE_DOMAIN>` (plan 15) and confirm the break-glass credentials are documented in `procedures/break-glass-access.md`.

## Rollback / abort
- Before step 9: abort with Ctrl-C or by not typing the confirmation. Only buckets, the remote journal, the bundle and the local install commit exist; buckets are retained by design. To start over with a new identity use `./bootstrap abandon-identity` (step 8).
- After provisioning: there is no "undo". To stop spending, run `./bootstrap destroy` (`safe-destruction.md`); an OVH VPS month already started is not refunded.
- A half-finished install keeps its compute until you destroy it; the report says spend continues.

## Evidence to record
- The final report (`.bootstrap/runs/<run-id>/report.json`; also in `s3://<prefix>-tofu-state/<cluster>/reports/`).
- Date/place where the passphrase, recovery identity and bundle copy are stored (not the values).
- The notification test receipt and heartbeat configuration.

## Follow-ups
- Set up the installation's own release chain after transfer (CD-19; not part of this procedure): `./bootstrap supply-chain init`, review and merge the trusted-keys PR, then the first own release and promotion (plan 23).
- Optionally acknowledge the V-24 test notifications: `./bootstrap verify --ack-notifications` and type the nonces you received.
- Run `./bootstrap state verify` monthly (alert `SCTofuStateRestoreTestFailed` fires after 35 days without a passing run; `recover-opentofu-state.md#restore-test`).
- Run the break-glass drill within 30 days (plan 15).
- Plan expansion to small-ha when ready (`expand-to-small-ha.md`, plan 47).
