# Blind review: bootstrap seed -> Forgejo -> Flux control transfer

Reviewer area: **SEEDFLUX** (empty node to `CONTROL TRANSFER COMPLETE`). Blind, independent, adversarial. Only `plans/reviews/consolidation-decisions.md` was read under `plans/reviews/`.

## 1. Scope read

Read in full: SPEC §8, §32-§35 (and grep of §6); plan 19 (all); plan 18 §1-§8, §22-§23 (tree, layer graph, root/source indirection, HelmRelease and prune conventions, validate checks, ACs); plan 50 §1-§17, §20, §22 (CLI, phases, journal, resume, verify, report); plan 16 §4, §5, §7, §14-§18, selected ACs; ADR-0005, 0016, 0252, 0300, 0304; procedures `seed-flux-handoff`, `failed-flux-handoff`, `bootstrap-resume`; contracts `seed-manifest`, `control-transfer-report` (first half), `forge-init-result`, `bootstrap-journal` (facts and step names).
Sampled by grep or targeted reads only: plans 05 (lab-seed), 09 (Traefik pin), 13, 14 (seed-read), 15 (A0), 17 (PF-16), 35, 36 (forgejo-ssh rows), 45 (black-start seed), 48 §8.3 (upgrade gates), 52 §8.4, `dag.yaml`, `bootstrap-config.schema.yaml`. Not read in depth: plans 10, 12, 37, 51, `forge-configuration` contract, rest of `control-transfer-report`.
Third-party behaviour (Flux field-manager cleanup, Bucket `prefix` artifact layout, `StrictPostBuildSubstitutions`, `$${}` escaping) was taken as the plans state it (they flag it for re-verification); I could not check upstream source.

## 2. Verdict

**PASS WITH FIXES.**

The core mechanism is sound and unusually well argued: seeding through Flux itself, one set of layer objects, root-only source substitution, a tree-hash proof of the Bucket artifact, UID and Helm-history comparison, prune protection, a positive-control mutant test. I found no design-level blocker. I found several majors in the places where the design is most irreversible or least rehearsed: resume of a half-done switch, resume after machine loss, detection-only (not prevention) of recreation, one cumulative-counter gate that can wedge the seed, and unrehearsed full post-transfer waves.

## 3. Findings (by severity)

| ID | Sev | Location | Summary |
|---|---|---|---|
| SEEDFLUX-B1 | major | 19 §8.5 T1/T2, §17; 50 §17.1 r5; contract journal `facts.transfer` | Crash between T2's SSA and the `switched` journal write makes resume re-snapshot post-switch state |
| SEEDFLUX-B2 | major | 19 §14.3, §16, §17 r3; 50 §22 crash injection | Crash or resume during rollback is undefined; §16 and §17 give contradictory resume rules; transfer can complete with post-transfer layers left suspended |
| SEEDFLUX-B3 | major | 19 §18/§20; 50 §20; 50 §17.2; bootstrap-resume `#new-machine` | Machine loss between `seed.complete` and the forge-init push cannot be resumed: seed commit `S` is unreachable and re-rendered SOPS commits differ |
| SEEDFLUX-B4 | major | 19 §8.5 T0, §8.6 G3; 18 §22 check 9 | Recreation is detected only after T2; no pre-switch dry-run of the Git render against the live cluster; root inventory identity is not statically checked |
| SEEDFLUX-B5 | major | 19 §8.1 seed-complete #6 | `restartCount > 3` is cumulative; first-install Longhorn/CSI restarts can wedge the seed gate permanently with no documented fix |
| SEEDFLUX-B6 | major | 19 §8.5 T2-T5, §22 (`ci-canary`), 18 §8.3, dag P06/P12, 50 §14.6 | The whole post-transfer wave (about 20 layers) starts at T2 inside the proof window; no test, lab or k3d, ever rehearses it before P12 |
| SEEDFLUX-B7 | major | 05 lab-seed vs 52 §8.4; 19-AC-12/19-AC-19/19-AC-21; 52 §14.5 | The CD-31 lab gate has contradictory runner specs; the fallback is operator-committed evidence |
| SEEDFLUX-B8 | major | 09 §9 (Traefik `clusterIP: 10.43.0.20`), `bootstrap-config` `serviceCidr`, 08/CD-21 | The Traefik ClusterIP is hard-coded but `SERVICE_CIDR` is operator-configurable; a non-default CIDR fails the seed after paid provisioning |
| SEEDFLUX-B9 | major | 18 §8.6a (capsule); 19 G5/T6; 50 V-04; 19-AC-9 | `Bucket/capsule-charts` survives T6 but G5, V-04 and 19-AC-9 demand no Bucket anywhere: black start from capsule can never print `CONTROL TRANSFER COMPLETE` |
| SEEDFLUX-B10 | minor | 19 R-2/AC-5/G7 vs 15 A0 | A third imperative Secret (`authentik/authentik-bootstrap`) is created before T2; the "exactly two" invariant is false as written |
| SEEDFLUX-B11 | minor | 19 §17 re-seed, procedures D4/A3 | Re-seed repoints the Bucket and root on a running root; the §8.3 integrity check is not re-applied |
| SEEDFLUX-B12 | minor | 19 §8.6 G6, §8.1 #7 | G6 passes if `kubectl` still co-owns fields; ownership check #7 lacks entries for the out-of-band Secrets and Helm release Secrets |
| SEEDFLUX-B13 | minor | failed-flux-handoff §E2/§Verification vs 19 T5, 19-AC-8 | §E2 calls recreated stateless objects acceptable; contradicts `changed: []`; no CRD guidance (CRD recreation cascades to all CRs) |
| SEEDFLUX-B14 | minor | 19 §14.3 step 4, N5/N5-lab | Rollback step 4 ("verify UIDs") cannot pass after the very failure it is rolling back; final result code undefined |
| SEEDFLUX-B15 | minor | 50 §14.6, §14.8, 19 §8.7, 16 §14.3 | C1-C8 are evaluated before the CNPG-window-close commit and `forge finalize`; post-finalize push and mirror state are never proven |
| SEEDFLUX-B16 | minor | 16 §14.3 (4) | `sc-admin` password retirement has no crash-safe ordering with the bundle re-seal |
| SEEDFLUX-B17 | minor | 50 §14.2 PF-16, 17 §14.1.3 | PF-16 hard-fails on GitHub unreachability for every mutating command, including `handoff-rollback` |
| SEEDFLUX-B18 | minor | 19 §10, 35 L540 | No pipeline step owns the post-transfer `seed-read` rotation |
| SEEDFLUX-B19 | minor | 19 §5 vs 50 §14.5 | forge-init outputs: "N >= 0 commits" vs "at most one" |
| SEEDFLUX-B20 | minor | 19 A9, G3; 19 §8.1 #5 | Several test/gate definitions are under-specified (audit exclusions, `flux diff` paths per layer, ACME-in-direct-mode vs SEED-21) |

### SEEDFLUX-B1 (major): crash inside T2 re-snapshots post-switch state

**Where.** Plan 19 §8.5 T1/T2 and §17 ("T1 is recorded only if `flux-bootstrap.switched` is absent from the journal"); plan 50 §17.1 rule 5; N6 fault hook `after:T2`.
**Problem.** `switched` is journaled after the `kubectl apply` of the root succeeds. There is no `switching` intent record, and resume keys T1 on the journal fact, not on the real root `sourceRef.kind`. Crash injection (50 §22) kills at step boundaries only; N6's `after:T2` is "after" the journal write, so the interior window is untested.
**Scenario.** `sc-bootstrap` is SIGKILLed (or the laptop sleeps, or the SSH tunnel dies and the process aborts) between the SSA call returning and the journal PUT. Resume sees no `switched`. T0 passes (GitRepository is Ready at `F`). T1 re-snapshots: the root inventory now includes the post-transfer layers, HelmRelease `history[0].version` and Secret counts may already include a change the switch caused, and `transfer-pre-<S>.json` is overwritten (same key). T5 then compares against already-switched state, so the sole proof of "nothing was recreated or reinstalled" is vacuous for exactly the case that mattered. Also, `--from-phase`/freeze logic still treats the phase as pre-switch, so `seed` could be re-run against a Git-sourced root.
**Fix.**
- Plan 19 §8.5: add step T1b "journal intent `flux-bootstrap.switching` (with `pre_snapshot` outputRef) and fsync to S3 **before** T2". Make `transfer-pre-<S>.json` write-once (conditional PUT `If-None-Match: *`; a second T1 with an existing object verifies sha256 and reuses it).
- Plan 19 §17 and plan 50 §17.1: the resume Check for `flux-bootstrap` reads reality first: if root `spec.sourceRef.kind == GitRepository` then treat as switched regardless of the journal, and never run T1.
- Plan 50 §22: add crash points *inside* T2 (between SSA and journal write) to `make e2e-handoff` and `e2e-lab`; N6 gains a second variant `during:T2`.

### SEEDFLUX-B2 (major): rollback is not a resumable state; §16 and §17 contradict

**Where.** Plan 19 §14.3 (rollback steps 1-4), §16 ("`./bootstrap` resume re-runs T0 from scratch"), §17 rule 3 ("resume inside `flux-bootstrap` between T2 and T7 continues from the recorded step"); plan 50 §22.
**Problem.** Rollback consists of four sub-steps (suspend non-seed layers, re-apply seed root with `prune:false`, reconcile, verify UIDs) and only its end is journaled (`TRANSFER_ROLLED_BACK`). After `switched`, rule 3 says resume continues from the recorded step; §16 says it restarts at T0. These differ, and neither handles "interrupted mid-rollback".
**Scenario.** T4 times out (memory pressure, see B6). Auto-rollback suspends the 20 post-transfer layers (step 1) and the operator laptop loses power before step 2. Resume: `switched` is present, so rule 3 applies and the engine continues at T3. T3 passes (root is still on Git), T4 passes (T4 ignores post-transfer layers; the transient has cleared), T5 passes, T6 sees no Bucket reference and **deletes the Bucket**, T7 reports pass. The post-transfer layers are still `suspend: true` with the `handoff-rollback` label. T0(f), the only code that resumes them, is never re-entered. `verify` (V-05: no suspended object) then fails in a frozen pipeline; the operator must discover and patch 20 objects by hand, and rollback to the Bucket is gone. Variant: crash after step 2 but before the journal write: resume waits at T3 for a Git revision on a Bucket-sourced root, times out, and rolls back a second time.
**Fix.**
- Plan 19 §14.3/§17: journal each rollback sub-step (`rollback.suspend`, `rollback.root`, `rollback.reconcile`, `rollback.verify`) with an `in-rollback` state; Check() reads reality (any Kustomization labeled `suspended-by: handoff-rollback`, or root `sourceRef.kind == Bucket` with `prune:false`) and, if present, resumes the rollback before anything else.
- Resolve §16 vs §17: state one rule: resume after `switched` always re-runs the *cheap, read-only* T0 subset (d)(e) and continues at the recorded step; it never reaches T6 while any object carries the rollback label (add that as a T6 precondition `TRANSFER_PRECONDITION:rollback-residue`).
- Plan 50 §22 / plan 19 §22: crash-injection cases at each rollback sub-step (N4 variants).

### SEEDFLUX-B3 (major): machine loss between seed and forge-init cannot be resumed

**Where.** Plan 19 §20 ("Mid-seed loss of the operator machine ... re-run `./bootstrap`"); plan 50 §20 and §17.2; `bootstrap-resume.md#new-machine`; plan 50 `plan.install-identity` resume rule.
**Problem.** The seed commit `S` exists only in the lost clone and, as a `gitops/` subtree without history, in S3. The new-machine path re-renders `gitops/clusters/<cluster>/` from the bundle. SOPS re-encryption yields different ciphertext, hence a different tree and commit SHA; other install commits (inventory, edge, admin-transition) are re-derived likewise. Plan 50 §20 fetches Forgejo `main` only if `facts.forge_init.pushed_sha` is journaled, which is written at S19, after S08 and S16.
**Scenario A.** Seed completes (journal `seed.complete`, `seed.commit = S`), laptop dies before forge-init. New machine: HEAD is a re-rendered `S''` unrelated to `S`. Seed digest is `facts.seed.commit` (explicitly not HEAD), so `seed` is "done". Forge-init S08 pushes `S''`; later T0(b) fails `git merge-base --is-ancestor S F` (`S` is not even in the local object store) with `TRANSFER_PRECONDITION:forge-outputs-scope`. The documented fix (re-seed, B2/D4) is manual and the error message does not say so.
**Scenario B.** Laptop dies after S08 pushed `S` but before S19 recorded `pushed_sha`. New machine has no `pushed_sha`, so no sync-local; the re-rendered HEAD is pushed and S08 fails `FORGE_MAIN_DIVERGED` (fatal, "never force").
**Fix.**
- Define the seed identity by **`gitops_tree_sha` + manifest** (already in the snapshot), not by commit SHA. T0(b) then checks "tree of `gitops/` at `F` equals the snapshot tree plus only forge-init output paths", which is machine-independent.
- Plan 50 §20: before `forge-init` or `flux-bootstrap` on a clone that lacks `facts.seed.commit`, always (a) if the Forgejo repo exists, run sync-local first (trigger on repository existence, not on `pushed_sha`); (b) otherwise, automatically re-seed at the new HEAD (journal `seed.reseed` with reason `commit-lost`) before forge-init.
- Add a crash/new-machine case at "seed complete, forge-init not started" and "S08 done, S19 not" to the k3d/lab new-machine scenario (50 §22).

### SEEDFLUX-B4 (major): irreversible damage is only detected after T2

**Where.** Plan 19 §8.5 T0-T5, §8.6 G3, §14.3; plan 18 §22 check 9 (`seed-steady-identity`), ADR-0300, ADR-0304.
**Problem.** The proof is entirely after the fact: T5/G-checks run after the root has switched, with `prune: true`. Rollback cannot restore a recreated UID (see B14). Prevention rests on (i) the static `seed-steady-identity` render (layers only; it compares each seed layer's content between modes, not the **root-level inventory** between seed and steady), (ii) T0(b) path scope, (iii) prune-protection annotations on a fixed list of kinds. `HelmRelease` (a stateful one), `Service`, `Deployment`, `Job`, ClusterRole, `Kustomization` objects and Flux's own Deployments are *not* protected. T0 has no check that the Git tree at `F` renders to what the cluster already holds. `flux diff` (the right tool) only appears as post-hoc G3.
**Scenario.** A template or `sc-gitops gen` bug makes the steady root omit one object the seed root had (a `vars` file, a seed-only patch, a `Namespace` with a drifted annotation, a Flux CRD). F11 passes because layers are identical. At T2 the new root prunes it; for a HelmRelease that is a Helm uninstall of Forgejo or Traefik with `MirrorPrune`; for a Flux CRD it is deletion of every CR of that kind. T5 reports `TRANSFER_UID_CHANGED`, rollback runs, and the operator is told to "stop for human" with a destroyed object that rollback cannot resurrect.
**Fix.**
- Plan 19 T0: add (g) **pre-switch dry-run**: from a checkout of Forgejo `main@F`, for every seed layer and for the root, run `flux diff kustomization ... --path` (or the library equivalent) and a **prune-candidate computation**: `inventory(root, steady render at F) ⊇ inventory(root, current status)`; any prune candidate or non-empty diff aborts with `TRANSFER_PRECONDITION:steady-diff`. This is cheap and changes nothing.
- Plan 18 §22: add F11 check `root-inventory-subset` (objects of the seed root render, minus the root Kustomization, are a subset of the steady root render, ids and content).
- Keep T5 as the independent second line.

### SEEDFLUX-B5 (major): cumulative `restartCount` can wedge the seed

**Where.** Plan 19 §8.1 "Seed complete" #6 (`restartCount > 3` fails `SEED_HEALTH_FAILED`); procedure `seed-flux-handoff.md` Diagnose.
**Problem.** `restartCount` is cumulative and never decreases. First installs of Longhorn (driver-deployer and CSI sidecars restarting until the manager is ready), CNPG webhooks, Traefik and Forgejo init containers routinely restart a few times on a 12 GiB node. A pod healthy for 40 minutes still fails the check, on every resume.
**Scenario.** `csi-attacher` restarts 5 times while Longhorn manager comes up. All layers are Ready, healthz is 200, and `./bootstrap` reports `SEED_HEALTH_FAILED:restarts`. Rerun reproduces it. The procedure has no entry; `--retry-phase` only resets the ceiling. Operator must delete the pod by hand (forbidden by "do not touch" guidance) or re-seed.
**Fix.** Plan 19 §8.1 #6: fail only on pods currently not Ready, `CrashLoopBackOff`/`ImagePullBackOff` now, or `restartCount` increase within the last N minutes (record a baseline at the first evaluation, require "no new restart for 10 min"). Report cumulative counts as information. Add the Longhorn first-install restart case to the lab A-series.

### SEEDFLUX-B6 (major): the full post-transfer wave is released at T2 and never rehearsed

**Where.** Plan 19 §8.5 T4 note, §22 (fixture "one cheap canary", lab VM 8 GiB); plan 18 §8.3 (profile includes all post-transfer layers); `dag.yaml` (P07-P10 after P06); plan 50 §14.6.
**Problem.** Once P07-P10 exist, T2 makes about 20 layers eligible at once (Authentik with `authentik-pg`, Woodpecker with `woodpecker-pg`, kube-prometheus-stack, Grafana, OTel, `sc-db`, backups, renovate, strange-company). Three more CNPG initdb/WAL archives, Helm installs, image pulls and a Longhorn replica sync run on a 12 GiB node (8 GiB allowed) *inside* the T3-T5 window, while Forgejo, the Flux source, is a participant. The k3d fixture deliberately has one canary layer, the lab VM cannot hold more, and DAG phasing means P06 is accepted before most of this exists. The first time the real wave meets the transfer window is the P12 run on the production node. V-30 is measured only after a 10 min settle and so cannot show peak concurrency.
**Scenario.** Forgejo is OOM-killed or evicted during the wave (Forgejo's `PriorityClass` is platform; Longhorn instance managers are not killable but compete). GitRepository goes NotReady, T4 times out at 20 min, automatic rollback fires and suspends the wave, whose pods keep running and keep consuming memory, so the retry starts worse than the first attempt.
**Fix.**
- Plan 19/18: release post-transfer layers **after** T6 rather than at T2. Mechanism: the steady root carries `spec.suspend: ${SC_POST_TRANSFER_SUSPEND:=false}`-style gating on non-seed layers is awkward; simpler is a second Git commit by `sc-bootstrap` (allowed pre-finalize, stage `bootstrap`) that adds the post-transfer layers to the profile after T7, with the wave then sequenced by `dependsOn`. Alternatively give every post-transfer layer a `dependsOn` on a no-op Kustomization `transfer-complete` that is created by that same commit.
- If the design stays as is, add a required pre-P12 rehearsal AC: full profile, production shape (12 GiB / 100 GiB), disposable VM, with N4 and N6 run against the real wave, plus measured peak working set during T2-T6.
- Rollback must also scale or at least report the wave workloads it leaves running.

### SEEDFLUX-B7 (major): the lab gate that CD-31 depends on is not satisfiable as specified

**Where.** Plan 05 (lab-seed: runner needs >= 8 vCPU, >= 16 GiB, KVM, >= 60 GiB free; `LAB_RUNNER_DISK_LOW` hard failure; 05-AC-24) vs plan 52 §8.4 ("standard KVM-capable hosted runner (4 vCPU / 16 GiB)"); 19-AC-12, 19-AC-19, 19-AC-21; plan 52 §14.5 fallback.
**Problem.** A standard hosted runner has neither 8 vCPU nor 60 GiB free (plan 52 defers "verify at F10"). If it cannot run, the fallback is "operator runs `make e2e-lab` on a workstation and commits the JUnit by PR". That evidence is produced and attested by the person shipping, and 19-AC-12 treats it as the *only* proof that rollback and storage preservation work, because rollback is forbidden on the production node. Also lab-seed is direct mode, 8 GiB, 40 GiB; production is tunnel mode on 12 GiB/100 GiB; the binding is to the `sc-bootstrap` digest only, not to the gitops tree, Flux version or profile.
**Fix.** Plan 52 §8.4 and plan 05: one runner spec, stated once; a decision at F10 (self-hosted lab host or paid larger runner) *before* P04 is scheduled; fallback evidence must be a machine-signed artifact (JUnit plus run log, hash recorded in the journal/report) from a pinned container, with the Flux version, profile and tree hash of the tested commit in the filename, and 19-AC-12 must also bind to `gitops` tree hash and Flux lock. Add tunnel-mode coverage or state the residual risk in 19 §22's divergence table.

### SEEDFLUX-B8 (major): Traefik ClusterIP is hard-coded, the service CIDR is configurable

**Where.** Plan 09 (Traefik `service.spec.clusterIP: 10.43.0.20`, F11-reserved), plan 08 (`/etc/hosts` managed block), CD-21; `bootstrap-config.schema.yaml` `network.serviceCidr` (pattern-only validation), plan 50 §9.2 `SERVICE_CIDR`, plan 07.
**Problem.** `SERVICE_CIDR` is an accepted input (e.g. to avoid a clash on `existing` nodes). Nothing derives or validates `10.43.0.20` against it. The apiserver rejects a Service with a ClusterIP outside the range.
**Scenario.** Operator sets `SERVICE_CIDR=10.96.0.0/16` on existing nodes. Configure succeeds (k3s starts), seed starts, `traefik` HelmRelease fails (`Service ... provided IP is not in the valid range`), `forgejo` waits on `traefik`, `SEED_LAYER_TIMEOUT:traefik` after 45 minutes. k3s service CIDR is immutable after install, so the fix is reprovisioning/reinstalling.
**Fix.** Plan 09/50: either derive `SC_TRAEFIK_CLUSTER_IP` as `serviceCidr base + 20` into `cluster-vars` (and use it in the values and hosts block), or add preflight `PF-29`: `serviceCidr` must be `10.43.0.0/16` unless the derived IP is used; update 18 `cluster-vars-schema` and the CoreDNS rewrite accordingly.

### SEEDFLUX-B9 (major, H04 scope): capsule Bucket contradicts the "no Bucket" completion checks

**Where.** Plan 18 §8.6a (`Bucket/capsule-charts` stays; "not deleted at T6"); plan 19 G5, T6, 19-AC-9, §8.7; plan 50 V-04.
**Problem.** G5 requires `kubectl get buckets.source.toolkit.fluxcd.io -A` to return none; V-04 requires "no `Bucket` in `flux-system`"; 19-AC-9 likewise. With `--from-capsule` the capsule component keeps a Bucket in `flux-system` until a later PR. C6 and C4 fail, so the black-start-without-GitHub path (CD-33) can never print `CONTROL TRANSFER COMPLETE`. Also seed-set HelmReleases are repointed to a Bucket chart source, which is exactly what plan 18's seed chart rule forbids for the `flux-system` source; removal later causes unprompted Helm upgrades of Forgejo (stateful, retries 0) and Longhorn (no downgrade) with only a "rendered output is identical" argument.
**Fix.** Plan 19 G5/T6, plan 50 V-04: replace "no Bucket" by "no Bucket named `flux-system` and none labeled `strange.company/seed=true`; in capsule mode exactly `capsule-charts` is permitted and listed in the report". Plan 18 §8.6a: require the removal PR to pass through the upgrade gates and `make gitops-diff` for each repointed release; add a capsule variant to 19-AC-14.

### SEEDFLUX-B10 (minor): the "exactly two imperative Secrets" invariant is false

Plan 15 A0 creates `authentik/authentik-bootstrap` (`kubectl create secret`, label `strange.company/ephemeral=bootstrap`) inside `flux-bootstrap` before T2, while plan 19 R-2, 19-AC-5, G7, the seed procedure and 50 §14.5 say exactly two (three with a CA) are created out-of-band and "nothing else". G7 uses a different label, so tests stay green, but the written invariant and the "Do not create Secrets other than the two" instruction are wrong. **Fix:** plan 19 R-2/§8.4: add the A0 Secret as a documented third class with its own label, lifetime and teardown (A10), and add it to G6's allow-list.

### SEEDFLUX-B11 (minor): re-seed bypasses the integrity proof

Plan 19 §17 "Re-seed" and procedures D4/A3 patch the Bucket prefix and then the root path on a root that is already unsuspended. 19-R-1 ("before any layer is applied, the artifact is proven byte-identical") is only enforced for the first seed. **Fix:** re-seed = suspend root, upload `S'`, patch Bucket, wait Ready at the new revision, run §8.3 against `S'`, patch root path/`SC_SOURCE_ROOT`, resume.

### SEEDFLUX-B12 (minor): ownership checks are weaker or stricter than intended

G6 requires `managedFields[].manager` to *contain* `kustomize-controller`/`helm-controller`; an object still co-owned by `kubectl` passes, so SEED-17's claim that no imperative field survives is not tested. Conversely seed-complete #7 would fail on `flux-system/sops-age`, `seed-bucket-credentials`, Helm `sh.helm.release.v1.*` Secrets and k3s-created `kube-system` objects unless the allowlist lists them; plan 19 assigns only Kubernetes-created `kube-root-ca.crt`/default SAs/Leases to itself. **Fix:** G6: for seeded objects assert that no `kubectl`/`sc-bootstrap` entries remain except the listed out-of-band Secrets; plan 19 §8.1 #7: add those Secrets and Helm release Secrets (`owner=helm` label) to the plan-19-owned allowlist entries; add a check that the allowlist contains no entry that matches a Flux-labeled object.

### SEEDFLUX-B13 (minor): failure procedure §E contradicts T5 and misses CRDs

`failed-flux-handoff.md` §E2 says recreated "stateless objects (Deployment, ConfigMap, Service, RBAC)" are acceptable and the Verification section accepts them in `changed[]`. Plan 19 T5 treats any UID change as failure and 19-AC-8 requires `changed: []`. T1 compares every CRD, but §E2 never says that a recreated CRD deletes all its custom resources (CNPG `Cluster`, HelmRelease, Kustomization). **Fix:** make §E2 consistent with T5 (all changes are failures; stateless ones may be accepted only by a signed-off defect record) and add a CRD branch: stop, assume CR loss, restore per plan 45.

### SEEDFLUX-B14 (minor): rollback's verification step cannot pass after a UID change

Plan 19 §14.3 step 4 verifies UIDs against `transfer-pre`. After `TRANSFER_UID_CHANGED` the recreated objects have permanent new UIDs, so rollback "fails" its own check; N5/N5-lab expect "followed by rollback" without stating the final result code or what 19 §16 ("rollback at most once") reports. **Fix:** define the outcome: rollback verify compares *only* what rollback can restore (source kind, layer revisions) and reports changed UIDs as residual in the report (`result: rolled_back`, `uid_comparison.changed` non-empty), with N5/N5-lab asserting that exact shape.

### SEEDFLUX-B15 (minor): C1-C8 are not evaluated on the final state

Plan 50 §14.6 runs `control-transfer.close-cnpg-window` (a new commit to Forgejo `main`) and `forge finalize` (steady protection, `sc-admin` password retired) *after* `verify`; plan 19 §8.7 demands C1-C8 "within one evaluation". The report asserts GitHub mirror `main == Forgejo main` and push capability, but the head moved and protection changed afterwards. **Fix:** order the phase as window-close, mirror catch-up wait, finalize, then the C1-C8 evaluation and the report; C3 probe runs under the post-finalize credential model.

### SEEDFLUX-B16 (minor): `forge finalize` credential rotation is not crash-safe

Plan 16 §14.3(4) sets a new `sc-admin` password through the API and then re-seals the bundle. A crash between the two leaves a Forgejo password that exists nowhere (the SOPS seed value no longer works; the bundle has the old value). Break-glass is the only way back. **Fix:** stage the new password in the bundle first (`bundle stage-rotation`), apply to Forgejo, then commit the stage; resume reads both.

### SEEDFLUX-B17 (minor): preflight makes local repair depend on GitHub

PF-16 a/b fail on GitHub errors and preflight runs before every mutating command. `./bootstrap handoff-rollback`, `verify`, `forge-init --recover` and routine resumes cannot start during a GitHub outage, although SPEC §32 says Forgejo operation (and by extension its repair) must not depend on GitHub. **Fix:** plan 50 §14.2: preflight checks are command-scoped; `handoff-rollback`, `status`, `exec`, cluster-only repairs run PF-01/02/22 only; PF-16 downgrades to `warn` when only reachability fails after `forge-initialized`.

### SEEDFLUX-B18 (minor): nobody owns `seed-read` rotation

Plan 19 §10 and plan 35 L540 say the key "is rotated after transfer", and V-38 checks snapshots, but no phase, step or report field does the rotation. **Fix:** add `control-transfer.rotate-seed-read` (plan 50 §14.6), recorded in the report.

### SEEDFLUX-B19 (minor): outputs-commit count

Plan 19 allows N >= 0 outputs commits (`F == S` possible); plan 16 S15 always writes `config/forge-init.yaml`; plan 50 §14.5 says "at most one"; the result contract requires `outputs_commit` on success. **Fix:** state "exactly one, `F != S`" everywhere, keep T0(b) tolerant.

### SEEDFLUX-B20 (minor): under-specified test/gate details

- A9 asserts "zero `create`/`delete` by kustomize-controller/helm-controller in seed namespaces"; Events and Leases are created by those controllers. Define the exclusion list.
- G3 command shows `--path .../platform/<c>/overlays/<profile>` for all layers; `cluster-config`, `namespaces`, `policies`, `apps` use other paths. Derive each from the layer `spec.path`.
- Seed-complete #5 requires a valid public certificate in direct mode, so ACME failure (rate limit during repeated fresh installs) blocks P04, contradicting the SEED-21 intent that ACME never gates the seed.
- Plan 19's table lists `longhorn-upgrade-gate` at 15m+5m while plan 18 sets 30m, and `seed.timeout` is 45m while individual gates allow 30m.

## 4. Scenarios tried that the design withstood

- **Duplicate resource ownership at the switch.** Layer objects are literally the same files in both modes (ADR-0300); the `seed` vs steady roots differ by one deleted object; `single-owner`, `seed-chart-source`, `source-ignore-parity`, `root-substitution-scope` close the obvious duplicate/escape paths. The pre-applied PriorityClasses and controllers are adopted by SSA with matching content.
- **Bucket artifact tampering or partial upload.** Manifest uploaded last, U4 conflict rule, U6 count check, root created suspended and released only after tarball digest plus git tree-hash equality; N2 asserts no Pods exist after a mismatch.
- **Seed-time deadlocks from default-deny.** Plan 36 rows for S3 egress, DNS upstream, upgrade-gate API access and Flux to Forgejo `2222`; A1 asserts the Bucket stays Ready after `policies`; A10 asserts the gates complete.
- **GitRepository authentication ordering.** Deploy key and host key are pre-generated, SOPS-delivered from the snapshot, and forge-init only registers/verifies; `FORGE_HOSTKEY_MISMATCH` and N3 are fail-closed before any change.
- **Wrong or moved Forgejo `main`.** T0(b)/(c) require ancestry, a scoped diff, and equality with the journaled final SHA; N1 proves refusal with the root unchanged.
- **Helm reinstall hiding behind revision 1.** HelmRelease CR UIDs are in the T1 set and release-Secret counts are compared, so uninstall/reinstall is caught even when numbering restarts.
- **Pruning stateful data on a Git mistake.** Protected-kind annotations, `gitops-destructive-diff`, and `stateful-app` remediation class are consistent across ADR-0304 and plan 18.
- **Upgrade-gate Jobs breaking first install or repeating.** Name embeds version, `ttlSecondsAfterFinished` unset, first-install branch fails closed on 403.
- **Authentik empty-database takeover at the switch.** A0 is journaled before T2, `verify` checks pod start time, blueprints deny `initial-setup`.
- **Comparator vacuity.** N5 mutant plus N5-lab with markers (repo commit, issue row) and PVC/PV/Volume/Cluster UIDs is a genuine positive control.
- **Machine loss after the switch.** Frozen phases, S3-held `transfer-pre`, `TRANSFER_PRE_SNAPSHOT_MISSING` instead of re-snapshot (this is what B1 narrows further), N6 from a fresh workspace.
- **Emergency source after Forgejo loss.** `prune: false` and suspend-first are specified and tested (N7); the cascade-uninstall trap is called out.
- **CD-19 unattended completion.** Credentials collected up front, restore tests primed by `verify.dr-prime`, P11 removed from the DoD with `dag.yaml` consistent.
