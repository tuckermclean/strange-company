# Blind review: disaster recovery and black start (area DR)

Reviewer: blind adversarial review, Stage 0 engineering definition. Binding decisions read: `plans/reviews/consolidation-decisions.md` (CD-01..CD-34). No other file under `plans/reviews/` was read.

## 1. Scope read

- `SPEC.md` §15, §26, §29, §31, §32, §34, §35, §98, §99, §107–§114.
- Plans: 45 (all), 44 (RPO and test mapping, BK-09, RT list), 07 (snapshots, secrets-encryption, decision tree), 10 (certificate backup), 12 (archiver detach), 14 (buckets, providers, failure domains), 06 (locking, conformance), 20, 23, 35 (secret classes, bundle), 50 (CLI surface), `plans/dag.yaml`, `plans/spec-issues.md` (SI-27, SI-43).
- Procedures: total-cluster-loss, github-black-start, recovery-drill, restore-etcd, restore-forgejo, restore-longhorn, restore-cnpg, replace-object-storage, recover-opentofu-state, rotate-age-key, bootstrap-resume (lost-operator-machine), object-storage-outage (detach lines).
- Contracts: `recovery-record`, `restore-test-result` (schema and examples), `alert-catalog` (RepoBundle alert), `secret-classes` (grep).
- ADRs: 0158, 0554 (0012, 0204, 0253, 0500 only through plan references).
- Not read in full: plan 13 and 17 bodies, plans 19 and 47, `restore-authentik`, `lost-*`, `restore-test-failure`, ADR-0012/0204/0253/0500 bodies, `recovery-bundle` schema body.

## 2. Verdict

**PASS WITH FIXES.**

The black-start design is unusually complete. It has a fencing model, a generation-bump overlay, a two-stage completion report, a trust anchor and an explicit scenario matrix. I found no blocker. I found seven majors. All are gaps between what the documents claim is proven and what the mechanisms or evidence formats can actually prove, or holes in data classes that sit outside the strong backup path. Fix these before Stage 0 is declared closed.

## 3. Findings (ordered by severity)

| ID | Sev | Location | Summary |
|---|---|---|---|
| DR-B1 | major | plan 14 B7, plan 45 §7.2 S15/S16, §9.4, plan 23 §registry, `replace-object-storage.md` | `forgejo-objects` (internal OCI registry, LFS, packages) is a single-copy, unrecoverable data class. Black start and provider loss have no rebuild path for internal images. |
| DR-B2 | major | plan 45 §9.4 capsule contents, 45-AC-20 | The capsule does not contain the step 9 / verify-path components (Authentik, Woodpecker chart, monitoring stack). "No GitHub service needed" is false as specified. |
| DR-B3 | major | `contracts/restore-test-result.schema.yaml`, plan 45 45-AC-1, `recovery-drill.md` RD3 step 6 | The evidence schema cannot hold the three RTO measurements. `additionalProperties: false` rejects them, and the example uses the wrong `test_kind`. |
| DR-B4 | major | plan 45 §14.2 step 9.3, §14.3, `recovery-drill.md` RD3 | The RD3 measurements do not measure what they claim: RPO against a live source is trivially negative, the RTO clock excludes step 1, and "Forgejo main equals GitHub main at drill start" is false by construction. |
| DR-B5 | major | `dag.yaml` (H04 after R02), plan 45 §20.6, §23 | The first black start on real infrastructure with real Longhorn and CNPG is at H04, after the Self-Hosting Threshold. Every RTO figure until then is an unmeasured estimate. The self-hosting-minimum profile is never drilled by requirement. |
| DR-B6 | major | `restore-etcd.md` §Fence/§Compare | The fence is applied after k3s has already started kubelet and controllers, so the stale snapshot is live before the fence. The unfence order lets the CNPG operator run against a stale `serverName` generation. |
| DR-B7 | major | plan 45 §20.7 rules 1–5, `recovery-drill.md` | The RD3 isolation list is incomplete. The drill reuses the source secrets and no rule neutralises the external heartbeat, notification transport, tunnel secret or (in `shared` mode) the account-wide S3 key. |
| DR-B8 | minor | plan 45 §14.2 step 6.1, §15 | There is no fallback when the newest base backup is corrupt or a WAL gap exists. |
| DR-B9 | minor | plan 45 §13 trust anchor | The anchor check will routinely produce `GITHUB_COMMITS_UNTRUSTED`, so approval becomes habit. The mirror PAT can append malicious commits. |
| DR-B10 | minor | plan 10 line 69/245, `total-cluster-loss.md` Rollback | The wildcard certificate is re-issued on every black-start attempt. The 5/week duplicate limit has no black-start fallback. |
| DR-B11 | minor | plan 45 §7.1, plan 44 line 244, alert catalog | The project-repo RPO arithmetic exceeds 1 h, and the alert thresholds are stated three different ways. |
| DR-B12 | minor | `total-cluster-loss.md` step 13 | The black start never reconciles the ledger gap (up to 15 min of untracked model spend) before resuming workers. |
| DR-B13 | minor | `total-cluster-loss.md` step 12 | The finalize PR needs the whole merge gate working in a freshly restored cluster. There is no contingency if it is not. |
| DR-B14 | minor | plan 14 / plan 04 region guidance, plan 45 Q-45-3 | The reference deployment puts compute and object storage in the same OVH account. The failure-domain split is only a warning. |
| DR-B15 | minor | `rotate-age-key.md` `#lost-bundle-black-start`, plan 50 line 176 | `rebuild --black-start` is not in the plan 50 CLI surface (CD-14). |
| DR-B16 | minor | plan 45 §7.2 | There is no scenario row for loss of or an outage at the DNS/tunnel provider (Cloudflare) or the registrar during a black start. |

### DR-B1 (major): `forgejo-objects` and the internal registry have no second copy and no rebuild path

**Facts.**
- Plan 14 B7 holds LFS, attachments, packages and the **internal OCI registry**. In `scoped` mode it has no Object Lock, only versioning.
- Plan 45 §7.1 says "nothing to restore" for this stream. §9.4 explicitly excludes "images that the restored Forgejo registry serves" from the capsule.
- After P11, every internal reference (Runner, Gateway, Hermes, CI images, toolbox/ci) points at the Forgejo registry. Kubelets pull from it (CD-21).
- Plan 23 says only "if lost, re-run the release for the current tag … digests may differ".

**Scenarios.**
1. *S15/S16, provider or account loss.* The CNPG and Longhorn data are gone, which is accepted. In addition, every internal image is gone. A black start then starts `Forgejo` from upstream images, but the Runner, Gateway, Hermes and the Woodpecker CI images cannot be pulled. The rebuild needs a "release" that needs Woodpecker, which needs the internal CI images, which are the thing that was lost. The documents do not break this cycle. Plan 23's GHCR bootstrap digests are replaced by the first promotion PR and are never mentioned as a recovery source.
2. *Object-storage outage.* `object-storage-outage.md` already concedes that internal images are unavailable, so CI-image-dependent PRs fail. The consequence is that any pod restart of an internal image during the outage fails.
3. *Planned migration (`replace-object-storage.md`).* Steps 6–10 re-point CNPG, Longhorn, bundles and etcd. **No step copies `forgejo-objects` or `evidence` to the new provider.** Forgejo is re-pointed at an empty bucket, and registry, LFS and attachments silently disappear from Forgejo's view. Step 11 keeps the old provider only "read-only" and never says to copy data.
4. LFS content in project repositories is not in the git bundles (BK-09), so LFS-using projects lose their large files on any B7 loss.

**Fix.**
- Plan 45 §7.1/§7.2 and plan 14: classify B7 (registry part) as "reconstructible only if the capsule or a second registry holds it" and state the RPO truthfully.
- Plan 23 and plan 43: add a **second registry copy**. Options are a post-release `crane copy` of every digest in `versions/internal-images.lock.yaml` into the recovery failure domain (the capsule's `recovery` bucket or the second S3 account), or extend the capsule (DR-B2) to all internal images (≈ 6–10 images, a few GiB).
- Plan 45 §14.2 step 6/9 and `total-cluster-loss.md`: add a documented `image-source` override component for "internal registry empty". Plan 18 owns the component; the recovery commit adds it automatically when `forgejo-objects` is unavailable.
- `replace-object-storage.md`: add an explicit step "copy B7 and B9 (and optionally B10) with `rclone`/server-side copy before step 6", and an AC.
- Plan 14: decide whether LFS and attachments need Object Lock or a second-account copy.

### DR-B2 (major): the capsule does not cover step 9 and verify

**Facts.**
- §9.4 lists the capsule contents as: toolbox, k3s air-gap, "seed-set and restore-path charts" (Flux, cert-manager, CNPG + plugin, Longhorn, Traefik, cloudflared, Forgejo), providers, collections and the repository bundle.
- Step 9 of the black start starts Authentik (image `ghcr.io/goauthentik/server`, plan 15), Woodpecker (chart `oci://ghcr.io/woodpecker-ci/helm/woodpecker`, plan 20, images on docker.io), the Runner, Gateway and Hermes, and then runs the P12 verify profile.
- 45-AC-20 requires the sinkholed drill (`ghcr.io`, `*.github.io`, `*.githubusercontent.com` all blocked) to reach `RECOVERY VERIFIED — MIRROR PENDING`.

**Problem.** Authentik's image and the Woodpecker chart are both GitHub-hosted and not in the capsule. With GitHub sinkholed, step 9 cannot complete, verify fails, and 45-AC-20 either fails or silently passes only because the drill pulls something from another host. The claim "no GitHub service is needed" (S19b, `github-black-start.md` §E) is false for the components after the source switch. The same applies to the monitoring stack, to kube-state-metrics, and to any chart on `*.github.io`.

**Fix.** Plan 45 §9.4: define the capsule as "every external image, chart and binary referenced by `versions/*.lock.yaml` for components that verify checks as required", generated from the lock files, not from a hand-written list. Add a capsule-completeness check to `rt-bootstrap-capsule`: render the cluster directory offline, collect every image and chart reference, and fail if any is neither in the capsule nor served by the restored Forgejo registry (after DR-B1). 45-AC-20 should assert the sinkhole log is empty for every host in the list, not only that the report was produced.

### DR-B3 (major): the evidence contract cannot carry the RTO measurements

**Facts.** `restore-test-result.schema.yaml` `measurements` has `additionalProperties: false` and defines `restore_duration_seconds`, `rpo_observed_seconds`, `per_subject`, `step_durations` and a few others. `grep rto_` returns nothing. Plan 45 45-AC-1 and `recovery-drill.md` RD3 step 6 require `measurements.rto_data_availability_seconds`, `rto_sr1_seconds`, `rto_sr2_seconds`. The schema's own drill example uses `test_id: rt-blackstart-drill` with `test_kind: recovery-drill` (the enum has a separate `blackstart-drill`). `RecoveryRecord.spec.rto` uses different names again (`dataAvailabilitySeconds`, `serviceRestorationSeconds`, `*At`) and has no SR-2.

**Problem.** The sole artefact that is supposed to prove the CD-32 RTO split (and that `rt-drill-freshness` reads) fails schema validation as specified. A schema-valid drill document cannot carry the numbers the H04 gate tests.

**Fix.** `contracts/restore-test-result.schema.yaml`: add `rto_data_availability_seconds`, `rto_sr1_seconds`, `rto_sr2_seconds`, `decided_at` to `measurements` (or a `drill` object) and an `allOf` rule: `test_kind: blackstart-drill` requires them plus `per_subject` and `step_durations`. Correct the example's `test_kind`. Align the names with `RecoveryRecord.spec.rto`, or document the mapping in one place. Add a negative example for a drill document lacking the RTO fields.

### DR-B4 (major): the RD3 measurements do not measure RPO or RTO faithfully

1. **RPO against a live source.** Observed RPO is "drill start minus newest restored heartbeat" (§14.2 step 9.3, RD3 step 3). The source keeps running and archiving. The drill reads the archive hours after `decidedAt`, so the newest restored heartbeat is *later* than the drill start. The result is zero or negative and the AC (≤ 900 s) always passes. This is the central RPO evidence for the three CNPG streams, and it is vacuous. The design bound itself is also exactly 15 min (5 min heartbeat + 10 min `archive_timeout`) with zero margin for upload latency, so a faithful measurement would be flaky.
2. **Clock start.** `decidedAt` is typed at the `recover` prompt in step 2 (`total-cluster-loss.md`). Step 1 (clone, toolbox pull 10 min or build +15 min) is in the §14.3 budget but outside the measurement. The data-availability margin is 25 min. Operator-typed timestamps are also trivially gameable.
3. **Main equality.** 45-AC-2 requires the restored `main` to equal GitHub `main` at drill start. Ref reconciliation takes `max(F, G)` and the source keeps merging, so equality is false whenever a merge happened during the 5–6 h drill.
4. **SR-2.** In production it ends at the first dispatch, which depends on the operator resuming tasks and on there being a task. The drill ends at a different event (`resume-ready`). The two numbers are not the same quantity.

**Fix.**
- Plan 45 §14.2 step 9.3 / §20.7: for drills, measure RPO from source-side facts: for each heartbeat in the last 48 h, `LastModified` of the WAL object that contains it minus the heartbeat timestamp, taking the maximum plus the heartbeat interval. Alternatively restore with `--pitr-target` equal to a time chosen just after a heartbeat and before the next archive, and assert the loss. Set the AC threshold to the design bound plus a stated tolerance.
- `recover` should default `decidedAt` to the process start time, record the typed time separately and report both. Move the toolbox pull inside the measured window.
- 45-AC-2: compare to "GitHub `main` at the time Forgejo `main` was restored (recorded by the drill)" or require the restored `main` to be a descendant of the drill-start SHA.
- Define SR-2 identically in production and drill (`resume-ready` = all resume preconditions met, plus a synthetic fake-worker dispatch), and report the human-dependent time separately.

### DR-B5 (major): no real-infrastructure black start before production data

**Facts.** `dag.yaml`: H04 depends on H02, H03, R02. R02 is the Self-Hosting Threshold. The RD3 drill (45-AC-1) is gated at H04 and targets small-ha, with an "or one node with the minimum overlay" option that is optional and recorded as a cost choice. Before then the evidence is: the k3d fixture (Longhorn-less, CNPG real), the lab-VM gate for N4–N6 (CD-31, seed→Flux only), `recover --scope state-only` at P12, and an opt-in `make e2e-recover-vps`.

**Problem.**
- SPEC §109 and §111 ("entire cluster SHALL be recoverable") are first proven on small-ha after the factory has been running in self-hosting-minimum, with real Forgejo history, ledger and project repositories, for the whole of Phase 3–4. The profile that actually runs in production for that period (one node, Longhorn single replica) never gets a required real black start.
- The RTO budgets (§14.3) are sums of assumed times (provider delivery 45–75 min, forgejo-pg recovery 15 min, Longhorn 12 min). None is measured until H04, and plan 45 §25 Q-45-11 already shows the margin is 25 min. Authentik/Woodpecker/sc-pg WAL replay time scales with DB size and the 24 h base-backup interval; nothing bounds it.
- The CNPG `bootstrap.recovery` with the Barman plugin on real Longhorn volumes and a real `$${…}` Flux substitution is exercised only in k3d (45-AC-3), where the plugin and PVC behaviour differs.

**Fix.** `dag.yaml` and plan 45 §23: add a required **RD3-minimum on a disposable real VPS (single node, real Longhorn, real S3)** to GATE-P12 or to R02 as a precondition of the Self-Hosting Threshold (SPEC §117). It need not be the full small-ha drill. It must record the three RTOs and the observed RPO per database, and it repeats after any change in the §19 list. State in plan 45 §7.1 that the RTO columns are unmeasured estimates until that drill passes.

### DR-B6 (major): `restore-etcd.md` fences too late, then unfences in a hazardous order

**Facts.** §A–§C start k3s, wait for `/readyz`, and only then run §Fence ("immediately after the first `/readyz`"). The procedure itself says the snapshot (up to 24 h old, ADR-0158) "resurrects the Runner, worker Jobs, CronJobs and stale CNPG/Longhorn status".

**Scenarios.**
1. Kubelet and the controllers start with k3s. By the time `/readyz` is `ok`, the CNPG operator, longhorn-manager, the Runner and CronJobs are scheduling from stale state. Longhorn replica and volume CRs from up to 24 h ago can cause manager actions (marking replicas failed, rebuilds, deletion of "orphans"). The CNPG operator can act on a stale `currentPrimary`. The Runner can dispatch before `sc-emergency-stop` exists. A human cannot win this race. §Compare then reports a mismatch after the damage is possible.
2. §Compare checks timeline and replica freshness but not the **generation** (`serverName`). If a `restore-cnpg.md` replace or `replace-object-storage.md` ran after the snapshot, the restored `Cluster` says `g<N>` while PGDATA is `g<N+1>`. The unfence order is: remove the Longhorn selector, scale the CNPG operator back, then `flux resume`. The operator therefore reconciles the stale `Cluster` first and the archiver can write into the old `g<N>` path before Flux corrects the object.

**Fix.** `restore-etcd.md`: start the restored server with `disable-agent: true` (k3s `--disable-agent`) in `/etc/rancher/k3s/config.yaml` for the first boot, so no pod runs until §Fence and §Compare are complete, then remove it and restart. Alternatively cordon and taint the node before kubelet starts. Add to §Compare: compare each `Cluster` `serverName` and `CNPG_*_GEN` in Git against the newest generation in the bucket. Change the unfence order to: resume Flux db layers first, confirm `serverName` equals Git, then scale the CNPG operator. Add an AC to plan 07 (lab, 07-AC-23) that asserts no pod starts before the fence.

### DR-B7 (major): RD3 isolation misses shared external side channels

**Facts.** §20.7 rule 1: the drill "reuses the source's cluster directory and its secrets". Rules 2–5 cover buckets, names, DNS/tunnel, the GitHub mirror and the emergency stop. Fencing does not run in a drill.

**Problem.** Nothing says which secrets the drill commit overwrites. At least the following are live production credentials inside a drill that runs for hours:
- the **external dead-man's-switch heartbeat URL** (`SC-NOTIFY-TRANSPORT`): the drill's Alertmanager Watchdog pings the production heartbeat. If production dies during the drill, the heartbeat stays green and detection is masked. `recovery-drill.md` safety check 4 says only "keep the source's external heartbeat untouched";
- the notification transport itself (the drill pages the production channel);
- the production Cloudflare tunnel credential in the source SOPS files, unless the drill commit replaces it. Rule 4 says the drill "creates a temporary tunnel" but §20.7 rule 3 only says the commit "writes … like a real recovery plus the drill overrides". If the old tunnel credential is loaded, drill connectors join production's tunnel and receive production traffic;
- in `S3_CREDENTIAL_MODE=shared`, the account-wide admin key (plan 14 14-R-2a), which gives the drill write and delete over every production bucket (Object Lock `COMPLIANCE` only on B4–B9);
- the GitHub mirror token, model API keys, and any Hermes channel tokens (two live bots on one token).

The k3d fixture cannot catch this and the RD3 isolation checks (step 5) look at the source only after the fact.

**Fix.** Plan 45 §20.7: add a normative **drill secret-override table** (class, drill value). It must cover heartbeat URL (unset), notification transport (drill-only receiver), tunnel (new), S3 instances (new, drill buckets only; admin never in the drill), GitHub token (none), model keys (none), Hermes channel tokens (none). Add `45-AC-21`: in the drill, every decrypted Secret is compared against the source Secret set, and any equal external credential outside the allowed read-only list fails the drill. State explicitly that the drill Alertmanager has no heartbeat route.

### DR-B8 (minor): no fallback for a corrupt base backup or a WAL gap during black start

Backup discovery picks the highest generation with a "completed base backup" by listing (§14.2 step 6.1). The only failure codes are `NO_CNPG_BACKUP` (none), `GENERATION_MISMATCH` and a stale-WAL warning. If the newest base backup is unreadable or a middle WAL segment is missing, PostgreSQL recovery stops at the gap and can promote at the last good segment. There is no step that selects an older base backup, and a silent truncation is only noticed through the heartbeat-RPO print after the fact. Weekly restore tests catch corruption within a week at best. A WAL-archiver detach (plan 12, `object-storage-outage.md` 4b) creates a known gap, and if a total loss happens in that window, recovery is "latest" with no warning other than the age check.

Fix: plan 45 §14.2 step 6.1 and plan 12: add a WAL-continuity check (`barman-cloud-check-wal-archive` or listing-based segment-sequence check) and a `backupID` selection fallback with a typed `accept-older-backup <cluster> <id>`. `recover` should refuse to claim `latest` when the archive has a recorded detach window.

### DR-B9 (minor): trust anchor degrades into routine approval

The anchor is the newest `repo-backups` manifest, written at most 30 min before the loss. The mirror pushes on every push. Every black start where anything merged in the last 30 minutes yields `GITHUB_COMMITS_UNTRUSTED`, and the operator types `accept-github-commits n`. The inspection signal ("author, merge-PR trailer") is forgeable by anyone holding the mirror PAT, which the design itself says can append to `main`. The clone is executed with full authority on the machine that holds the bundle.

Fix: plan 45 §13 / plan 16 / plan 17: sign Forgejo merge commits with the instance signing key (Forgejo supports signing web merges) and have `recover` verify against a public key held in the bundle, instead of deferring to "post-MVP". Until then, make the prompt show the diff of non-`gitops/` paths (`bootstrap`, `infra/`, `toolbox/`) separately and require a second typed confirmation if any are touched.

### DR-B10 (minor): certificate re-issuance on every black-start attempt

The public wildcard certificate is "reissuable; no backup" (plan 10). Each restart of the black start (Rollback section: destroy the new installation and start again) issues a new, identical-identifier certificate, which counts toward the Let's Encrypt 5-per-week duplicate limit; RD3 uses production ACME for `*.drill.<domain>` too. The documented answer (`certificate-failure.md` §B) is for a live cluster. In a black start, kubelet pulls from `git.<domain>` (CD-21) require a valid certificate, so a rate limit or an LE outage blocks the whole sequence.

Fix: plan 45 §14.2 step 6: add a documented fallback in the recovery overlay: the seed trusts `sc-internal-ca` for node-side registry pulls (containerd `ca` entry for `git.<domain>`), or seals the issued certificate into the bundle at each renewal and restores it. Count black-start attempts in the journal and warn at 3.

### DR-B11 (minor): project-repository RPO arithmetic and alert thresholds are inconsistent

Plan 45 §7.1 gives "≈ 30 min + run time (≤ 55 min)" for project repositories, which is up to 85 min, against SPEC §108 ≤ 1 h. §7.2 S11 says "≤ 55 min". The plan 45 table says the alert fires at 2 h; the catalogue summary also says 2 h while its source has `maxAge: 1h`, `failAfter: 30m` (plan 44 line 244 claims it fires at 1 h). A skipped run already breaches 1 h before any alert, and the alert can fire up to 90 min after the last success.

Fix: pick one set of numbers. Bound run time (for example, per-repository timeout 10 min with a lag metric), set the alert to `maxAge: 45m`, and correct the catalogue summary and plan 45 §7.1 so "worst-case RPO by design" ≤ 60 min is true.

### DR-B12 (minor): no ledger reconciliation after a black start

`restore-cnpg.md` safety check 4 requires reconciling provider usage for the window after `T` before releasing the stop. `total-cluster-loss.md` step 13 resumes tasks without it, though `sc-pg` loses up to 15 min of ledger rows and in-flight reservations, and model API keys are "kept". The budget invariants (ADR-0450) are then wrong by that window.

Fix: add a step 12.5 to `total-cluster-loss.md` and plan 45 §14.2 step 10.4: run the plan 32 provider-usage reconciliation for `[newest restored heartbeat, resume]` and post a correcting ledger entry, or block resume until done.

### DR-B13 (minor): the finalize PR is on the critical path of SR-2 with no contingency

Step 12 needs Woodpecker CI, its internal CI images (DR-B1), the Runner's status posting and a human merge in a cluster that was restored an hour earlier. If any of these fails, the emergency stop cannot be cleared through the normal path. Q-45-9 only addresses the restore-of-a-merge-gate-component case. Fix: add a documented break-glass alternative to `total-cluster-loss.md` step 12 (direct apply of the same patch with the Flux field manager and a follow-up PR, recorded) and put it in the SR-2 budget.

### DR-B14 (minor): default deployment couples compute and object-storage failure domains

OVH is both the reference compute provider and a "supported, reference" object-storage provider. Plan 04 warns only if the S3 host *name* contains the compute region (`PF-10 S3_SAME_REGION_AS_COMPUTE`). An account suspension, billing dispute or credential compromise at OVH takes compute, state, bundle copy and every backup together (S16). Plan 45 Q-45-3 is "recommend, non-blocking".

Fix: plan 14 preflight: detect same account or same provider and require `S3_ACK_SAME_FAILURE_DOMAIN=true` (recorded in the bootstrap report and alerted), and make the second account for B1/B2 mandatory in `scoped` mode too, as `shared` mode already requires.

### DR-B15 (minor): CLI surface gap

`rotate-age-key.md` `#lost-bundle-black-start` invokes `recovery-bundle rebuild --from-recovery-identity --black-start`. Plan 50 line 176 lists `rebuild --from-recovery-identity` only. Under CD-14 the flag must be added to plan 50, together with an exit-code contract for "no cluster exists".

### DR-B16 (minor): no scenario for DNS/tunnel provider loss during black start

Fencing creates a new Cloudflare tunnel credential and the seed needs a tunnel and ACME DNS-01. SPEC §111 lists DNS/tunnel credentials as black-start material, but §7.2 has no row for Cloudflare outage, account loss or registrar problems. `edge-ingress-failure.md` covers a live cluster. Fix: add S27 (edge/DNS provider unavailable at black start) with the direct-ingress fallback or the operator-VPN path, and a statement of which steps still work (seed, restore, verify all work with port-forward; only public access and ACME wait).

## 4. Scenarios tried that the design withstood

- **Total loss from GitHub + object storage + operator material.** The step order matches SPEC §112. Fencing happens only after `state-recover` and the liveness probe. Generation bump, `[mirror] ENABLED=false` (guarded by 45-AC-4), `expectMirror` and the two-stage report prevent the old Forgejo state from overwriting GitHub. The creation guard (VAP) stops a stale recovery stanza from re-creating a live `Cluster`. Resume from the journal is idempotent.
- **Split brain / old installation alive.** The liveness probe has four independent signals, a typed override, a 401 verification of the old GitHub token, and `FENCE_UNVERIFIED` as a halt.
- **Object-storage provider loss, live cluster.** `replace-object-storage.md` bumps every CNPG generation, re-points Longhorn, forces full bundle chains, re-seals the bundle, and keeps the cluster in emergency stop until the first backups exist (except for the B7/B9 hole in DR-B1).
- **GitHub repository deleted or untrusted.** Alternative seeds from `repo-backups`, an operator clone or the capsule (modulo DR-B2). History is never force-pushed.
- **Operator laptop loss.** The bundle is in S3 plus the operator's copy. `bundle export-env` rebuilds `bootstrap.env`. The offline identity covers lost bundle or passphrase. Pins come from S3 and are never TOFU. The remaining dependency on provider console access is out of scope and noted in DR-B14.
- **Cluster age key lost or bundle lost.** `rotate-age-key.md` has distinct sections for each combination, including the black-start case where no cluster exists. S24 (both identities lost with the cluster) is stated honestly as unrecoverable for SOPS values.
- **etcd corruption.** The decision tree (ADR-0158) separates quorum loss with a surviving member, disks intact with snapshot, and rebuild. Snapshots are encrypted at rest (`secrets-encryption: true`) and token-dependent. §Compare detects the data-versus-API contradiction (modulo DR-B6).
- **CNPG PITR.** Side-by-side verification, generation windows chosen by `T`, the merge-gate bypass path for the four clusters and the create-window guard are all specified. The hazard of leaving a `-latest-` component in place after a black start is explicitly handled.
- **Forgejo Longhorn volume loss.** `restore-forgejo.md` and `restore-longhorn.md` cover Multi-Attach, volume restore, bundle rehydrate, `SECRET_KEY` hash comparison, hook regeneration and DB-versus-disk reconciliation by bundle fast-forward and GitHub.
- **OpenTofu state.** Locking is tested at bootstrap and fails closed. Versioned history, import rebuild and the passphrase fallback via the offline identity all exist. The recovery does not need the cluster.
- **RTO arithmetic.** The §14.3 sums are internally consistent (155–185 min to SR-1, +30 to data availability, +25 to verify), and the weakest margin (25 min) is disclosed in Q-45-11.
