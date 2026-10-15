# Procedure: Recovery drill (RD2 recovery-material drill, RD3 scratch black start)

**Owning plan:** plans/45-recovery.md (§20.6–§20.8)
**SPEC references:** §22 (break-glass periodically tested), §109 (a backup never restored is unproven), §111, §112, §113 (procedure added by Stage 0)
**Severity / urgency:** routine (scheduled). `SCRecoveryDrillOverdue` makes it due; `SCHADrillOverdue` covers plan 47's HA drills.
**Expected duration:** RD2 ≈ 1 h, no paid infrastructure. RD3 ≈ 5–6 h including teardown (the drill measures both RTO kinds of CD-32); one or three small VPS for a few hours.
**Requires break-glass:** RD2 yes — the break-glass drill part logs in with each break-glass credential (plan 15 §14.4.5), which pages `SCBreakGlassUsed` by design. RD3 no.

Drill IDs: `RD1` (automated restore tests, no procedure; failures go to `restore-test-failure.md`), `RD2`, `RD3` (this procedure). Plan 47's HA drills are `HA1–HA8`.

## When to use

| Trigger | Drill |
|---|---|
| Quarterly (RD2 evidence older than 90 days; alert at 100) | RD2 |
| Within 30 days after control transfer (first RD2; the P12 gate runs only the non-interactive state-only check) | RD2 |
| Yearly (RD3 older than ~360 days; alert at 400), at H04, and within 30 days after a change to a recovery-path plan (06, 07, 12, 13, 16, 17, 19, 35, 45, 50) | RD3 |
| `SCRecoveryDrillOverdue` | whichever is overdue (the alert annotation names it) |

## Preconditions and required material

- A clean machine (or a fresh container) that is **not** the usual operator workstation — the point is to prove the off-cluster material alone suffices.
- Recovery passphrase and the offline recovery age identity (RD2 decrypts with it; RD3 uses the bundled `drill-reader` credential and needs the identity only if the bundle is unusable).
- `bootstrap.env` credentials.
- RD3: a drill env file `bootstrap.drill.env` that overrides, for the drill only, `CLUSTER_NAME=<cluster>-drill`, `S3_BUCKET_PREFIX=<prefix>-drill`, `BASE_DOMAIN=drill.<domain>`, `INGRESS_MODE=tunnel` (a temporary drill tunnel), the cheapest compliant `MACHINE_CLASS` (or `existing` lab hosts) and `MAX_MONTHLY_COST` for that footprint (plan 45 §20.7). The drill reuses the source's cluster directory and secrets through the **source** bundle.
- Announce the drill in the ops channel. RD2 pages `SCBreakGlassUsed` (security-audit class): acknowledge it, **never silence it** (a silence pages `SCBreakGlassAlertSilenced`).

## Safety checks before acting

1. RD2 must not mutate anything: `./bootstrap recover --scope state-only` never fences, commits or writes (plan 45 45-R-6). If any prompt mentions fencing, a commit or provisioning, answer no and abort — you are in the wrong command.
2. RD3 must never touch the source installation: different `CLUSTER_NAME`, own buckets under `<prefix>-drill`, read-only source credentials (`drill-reader`), no production DNS or tunnel, no GitHub push (`expectMirror: false`), emergency stop `all`. `recover --drill` refuses if `CLUSTER_NAME` equals `--drill-source`.
3. Do not run RD3 while the source installation is itself recovering or upgrading.
4. Keep the source's external heartbeat untouched.
5. RD2 must not leave break-glass sessions open: log out of every system after its test.

## Steps

### RD2 — recovery-material drill (≈ 1 h)
1. Fresh clone from GitHub on the clean machine (`github-black-start.md` §A steps 1–5). **Expected:** all checks pass.
2. Bundle: `./bootstrap exec -- sc-bootstrap recovery-bundle verify --full` — enter the passphrase and paste the offline recovery identity when prompted. **Expected:** every `*.sops.yaml` decrypts with the bundled cluster key **and** with the offline identity; an attestation is written to `s3://<prefix>-recovery/<cluster>/attestations/<ts>.json` (plan 35 §14.6.4). Failure → `rotate-age-key.md` (lost-key variants) immediately: a failed RD2 is a black-start threat.
3. State: `./bootstrap recover --scope state-only`. **Expected:** exit 0; `state verify` passes; provider inventory matches the live installation; the summary line says `no fencing, no commit, no mutation (state-only)`.
4. Break-glass: `./bootstrap exec -- sc-bootstrap breakglass drill` (plan 15 §14.4.5; Grafana is tested through the login form). **Expected:** cluster, Authentik, Forgejo and Grafana logins succeed; `SCBreakGlassUsed` / `SCK3sBreakGlassAuditSeen` / `SCGrafanaLocalLogin` reach the notification transport within 10 min; `monitoring/breakglass-drill-status` updated.
5. Notifications: `./bootstrap exec -- sc-alertgen post-test --category black-start-threat --class page --nonce <N>` and confirm receipt (`notification-failure.md` §G).
6. Evidence: `./bootstrap exec -- sc-bootstrap drill record --type recovery-drill --outcome passed --notes "<who, where>"` writes `s3://<prefix>-evidence/restore-tests/rt-recovery-drill/<ts>.json` (`restore-test-result` schema: `test_kind: recovery-drill`, field `outcome`) with per-step results and durations. **Expected:** `rt-drill-freshness` succeeds on its next run; `SCRecoveryDrillOverdue` resolves.
7. Destroy any local copy of the offline identity on the drill machine (`shred` the file if one was written; normally it is pasted into the tmpfs prompt only).

### RD3 — scratch black start (≈ 5–6 h)
1. On the clean machine, fresh clone (`github-black-start.md` §A), then:
   ```sh
   ./bootstrap recover --env-file bootstrap.drill.env --drill --drill-source <cluster>
   ```
   Enter `decidedAt` (the drill start) and the **source** passphrase (`drill-reader` comes from the source bundle). **Expected:** preflight shows `drill mode: source=<cluster>, prefix=<prefix>-drill, domain=drill.<domain>, mirror=off, archiving→drill buckets, estop=all`; the storage-apply step creates the drill buckets and copies the source's `forgejo-objects` into `<prefix>-drill-forgejo-objects`; the plan shows only the drill footprint; type `create <cluster>-drill <D8>`. No fencing happens in drill mode.
2. Follow the checkpoints of `total-cluster-loss.md` steps 4–10 (provision, configure, overlay and seed, forge-init `--recover` against the drill Forgejo, source switch, restore, recovery verify). Image pulls resolve `git.drill.<domain>` to the drill's own Traefik (CD-21 `/etc/hosts` mechanism). Record each step's wall time from the console (also in the journal).
3. **Data checks** (the drill's verify adds them; confirm the output):
   - observed RPO per database from the restored `dr-marker-heartbeat` rows ≤ 900 s relative to the drill start (plan 45 §14.2 step 9.3);
   - Forgejo `main` in the drill equals the source's GitHub `main` at drill start;
   - `forgejo-data` and `hermes-data` restore durations recorded;
   - a sample of 3 project repositories cloned from the drill Forgejo (port-forward) at their bundle heads.
4. Run `--step forge-reconnect` and `--step finalize` in drill mode: the finalize PR is opened in the drill Forgejo; mirror verification is skipped (`expectMirror` stays false in drill mode) but the forced restore tests run against the drill buckets. **Expected:** the drill report ends with `CONTROL TRANSFER COMPLETE (RECOVERY)`.
5. **Isolation checks:** the source's `pg-backups/<cluster>/` contains no object written after the drill start (`aws s3api list-objects-v2 … --query "Contents[?LastModified>='<drill start>'].Key"` lists only the source's own WAL, written by the source cluster); GitHub refs unchanged by the drill; the source's tunnel still has its connectors; no request from the drill reached the source Forgejo (drill Forgejo access log only).
6. Evidence: `./bootstrap exec -- sc-bootstrap drill record --type blackstart-drill --from-journal` writes `rt-blackstart-drill/<ts>.json` with `outcome`, `measurements.step_durations`, `measurements.per_subject.<cluster>.rpo_observed_seconds`, `measurements.per_subject.<volume>.restore_duration_seconds`, and the RTO measurements from `decidedAt`: `measurements.rto_data_availability_seconds` (end of restore), `measurements.rto_sr1_seconds` (Forgejo serving `main`, Flux reconciling) and `measurements.rto_sr2_seconds` (the drill finalize step's `resume-ready`; the emergency stop stays `all` in a drill).
7. Teardown: `./bootstrap destroy --env-file bootstrap.drill.env` on the drill installation (plan 49; its report must say `COMPUTE DESTROYED — DURABLE RECOVERY DATA RETAINED`), then remove the drill's own buckets, state, journal, tunnel and `drill.<domain>` DNS records per `safe-destruction.md` §Drill (plan 49). Never run destroy with the source's `bootstrap.env`.
8. **Emergency-stop timing (G7, BUD-B9).** On the drill installation (or, for A0, with the drill's own throwaway model key) time the two independent stop paths: **A0** (provider console key revoke, `budget-emergency-stop.md` §A0) from alert to "provider shows requests rejected", and **A3** (Kubernetes ConfigMap, §A3) from start to the Runner reporting level `workers` and the Gateway denying a test request. Record both durations in the evidence object (`measurements.estop_a0_seconds`, `measurements.estop_a3_seconds`); a time above 10 min for either is a finding to fix in that procedure, not a drill failure. Never revoke a production key for this timing.
9. At H04 additionally run the plan 45 §20.8 list (HA drills with plan 47) and record them with `sc-bootstrap drill record --type ha-drill`.

## Verification

- RD2: evidence object exists with `outcome: passed`; new attestation present; `rt-drill-freshness` Job succeeded after the drill.
- RD3: evidence object exists with `outcome: passed`; data-availability RTO ≤ 4 h, SR-1 ≤ 4 h and SR-2 ≤ 6 h (plan 45 §7.1, CD-32; any miss makes the drill `failed`); every observed RPO ≤ 900 s; isolation checks all true; drill installation destroyed (provider console shows no drill instances).

## Rollback / abort

- RD2 is read-only except the attestation and the break-glass login timestamps; abort at any step.
- RD3: abort with Ctrl-C, then `./bootstrap destroy --env-file bootstrap.drill.env` on the drill installation and the `safe-destruction.md` §Drill cleanup.

## Evidence to record

The evidence objects above (automatic), plus a short Forgejo issue (`drill` label) summarizing results, any step that exceeded its plan 45 §14.3 budget, and follow-up actions. A failed drill is recorded with `outcome: failed` (it counts as evidence that the drill ran, but `rt-drill-freshness` accepts only `passed`).

## Follow-ups

- Any failed check becomes an issue against the owning plan with the drill evidence attached.
- If RD3 missed any RTO target, update plan 45 §25 Q-45-11 with the measured numbers.
- From H04, one RD3 per year runs as the `--from-capsule` variant with GitHub sinkholed (plan 45 45-AC-20; `github-black-start.md` §E).
- Rotate any credential that was exposed on the drill machine beyond the tmpfs prompt.
