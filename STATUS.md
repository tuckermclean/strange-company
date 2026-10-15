# STATUS

**Current stage:** Stage 0 (Complete Engineering Definition) is **complete**. Stage 1 implementation can begin at DAG node F01 (`PLAN.md`).
**Last updated:** 2026-10-15 (after the external review of PR #138 and the blind-review round)
**SPEC version:** 1.3. During Stage 0 the operator amended node disk sizing (v1.2). v1.3 is a terminology cleanup: the 12Gi supported minimum and the 8Gi constrained lower bound are now separate fields, and CI concurrency is counted in pipelines. See the Amendments note in `SPEC.md`.
**Implementation status:** not started, by design (SPEC §2, §125).

## What Stage 0 produced

| Artifact | Count | Location |
|---|---|---|
| Master plan (architecture, decisions, inventories, DAG, sequence, MVP boundary) | 1 | `plans/00-master-plan.md` |
| Subsystem plans (each with all 25 SPEC §121 sections) | 55 | `plans/01-*.md` … `plans/55-*.md` |
| Machine-readable DAG (43 nodes, acyclic) | 1 | `plans/dag.yaml` |
| Implementation sequence (generated from the DAG and the gate sections; 1,190 gate acceptance criteria) | 1 | `PLAN.md` |
| Executable Stage-0 validator (checks C01–C15) and its CI workflow (ADR-0021, CD-27) | 1 + 1 | `tools/stage0/validate.py`, `.github/workflows/stage0-validate.yaml` |
| Compatibility baseline: exact third-party versions the plans are reasoned against (ADR-0022, CD-29) | 61 components | `plans/compatibility-baseline.yaml` |
| Contracts (JSON Schema 2020-12, OpenAPI 3.1, data files) | 52 files (48 schemas validated) | `contracts/` |
| Operator procedures | 63 | `procedures/` |
| ADRs (22 global + subsystem ADRs) | 79 | `adr/` |
| SPEC issues register (59 entries, none blocking) | 1 | `plans/spec-issues.md` |
| Consolidation decisions (CD-01..CD-45) | 1 | `plans/reviews/consolidation-decisions.md` |
| Independent fresh-context reviews (SPEC §132 items 43–47), plus closure verification | 10 + 3 | `plans/reviews/review-*.md`, `plans/reviews/closure-*.md` |
| External adversarial reviews of PR #138 (two passes) and their disposition | 1 | `plans/reviews/external-review-pr138.md` |
| Blind reviews (no access to earlier review conclusions; CD-34) | 5 | `plans/reviews/blind-*.md` |
| Verified third-party lessons from the wiped implementation | 1 | `plans/reference/prior-implementation-lessons.md` |

## How Stage 0 was verified
1. **Deterministic validator, run in CI.** `python3 -I tools/stage0/validate.py` performs every structural check this file claims. The GitHub Actions workflow `stage0-validate` runs it on every push, installing hash-locked dependencies, and also checks that `PLAN.md` regenerates byte-identically. Result on the committed tree: **PASS on all 17 checks**:
   - C01 contracts, C02 openapi and C03 data-files: every schema is meta-valid, every `examples` entry validates, every `x-invalid-examples` entry fails, and each data file validates against its schema.
   - C04 state machine: 24 states and 94 transitions; every state is reachable and every non-terminal state can terminate in both modes.
   - C05 plan sections: all 25 template sections in each of the 55 plans.
   - C06 DAG and gates: 43 nodes, acyclic, and every node and every plan listed under it has a `GATE-<node>` section.
   - C07 AC ids: 1,190 gate acceptance criteria.
   - C08 references: no dangling procedure, contract or plan references.
   - C09 alerts: every alert name used resolves in the 278-alert catalogue.
   - C10 secrets: every secret-class token resolves in the 71-class register.
   - C11 inventories, C12 `PLAN.md` freshness, C13 no blocking open questions, C14 spec-issues register, C15 compatibility baseline.
   - C16 privileges: `plans/machine-accounts.yaml` agrees with the forge-configuration contract. Only `sc-keys` holds standing repository admin, and no machine account is in `sc-mergers`. No plan, ADR or procedure claims admin for any other account.
   - C17 residue: no question marked resolved still says "conflict recorded", "lead decides", "plans disagree", or anything similar.
2. **Independent reviews.** Ten fresh-context reviewers covered bootstrap, storage, security, Authentik/break-glass, TDD enforcement, Agent Runner, cost accounting, disaster recovery, seed→Flux handoff, and global consistency/DAG. They found 13 blockers and about 130 majors.
3. **Fix rounds.** Owners fixed their own files. Cross-group items were routed to their owners. Lead decisions are recorded as CD-xx.
4. **Closure verification.** Three further fresh-context verifiers checked every blocker and major against the current files: **all 13 blockers closed, 0 open**. The partially-closed items and new contradictions they found were fixed in a final targeted round.
5. **External adversarial review (PR #138, 2026-10-14).** It reported 2 blockers and 6 majors, dispositioned in CD-27..CD-34:
   - #1, no executable validator → validator and CI (CD-27).
   - #2, `maxCostUsd` was not a maximum → a hard ceiling on a provable worst case, with `BOUND_VIOLATION` and model quarantine (CD-28).
   - #3, moving third-party targets → compatibility baseline (CD-29).
   - #4, forgeable `X-SC-Actor` → per-operator admin credentials; the header is rejected (CD-30).
   - #5 → seed→Flux fault cases on a `lab-seed` VM with real Longhorn (CD-31, CD-36).
   - #6 → data-availability RTO split from service restoration SR-1/SR-2 (CD-32).
   - #7 → bootstrap capsule in the recovery bucket (CD-33).
   - #8 → blind reviews (CD-34).
6. **Blind reviews (CD-34).** Five reviewers covered security, TDD enforcement, disaster recovery, seed→Flux, and budgets. None had access to earlier review reports or conclusions. They found 1 blocker (SEC-B1: bot accounts could approve and merge) and about 30 majors. All were fixed or dispositioned in the owning plans; the new lead decisions are:
   - CD-35: an RD3 black-start drill is a hard gate of R02.
   - CD-36: a `lab-seed` runner, with CI-only evidence.
   - CD-37: the derived Traefik ClusterIP.
   - CD-38: merge and approval are human-only by construction.
   - CD-39: approval rules that scale with the number of human mergers, plus a single-operator cooling-off.
   - CD-40: the one admin bot, `sc-keys`, kept for deploy keys, with compensating controls.
   **Caveat:** the blind reviewers were Anthropic models (Sonnet), a different model from the authors but the same vendor. Vendor-independent scrutiny comes from the external review.
7. **Second external review pass (2026-10-15).** It confirmed that the first eight findings were genuinely addressed and found five consistency defects, all fixed under CD-41:
   - Plan 55 (and plans 34, ADR-0007, and a procedure) contradicted plan 16 about the Runner bot's privileges. Now `sc-runner` is write-only, and `sc-keys` is the only admin. The new privilege matrix and check C16 prevent a recurrence; C16 also caught a stale `sc-woodpecker` admin claim.
   - Q-51-09 carried stale conflict text. It now states the settled rule, and check C17 prevents a recurrence.
   - 8 GiB was called a "minimum". SPEC v1.3 now separates the 12Gi supported minimum from the 8Gi constrained lower bound.
   - `maximumConcurrentCIJobs` is renamed `maximumConcurrentCIPipelines`.
   - GitHub Actions are pinned to Node 24 releases.
8. **Operator decisions** (2026-10-14):
   - CD-23: 8 GiB nodes stay allowed as `constrained`.
   - CD-24: the OVH default stays VPS-2.
   - CD-25: SPEC v1.2 disk sizing, 100 GiB minimum and 200 GiB preferred.
   - CD-26: a Bitnami template-only library subchart vendored in an upstream chart is allowed.
9. **Operator decisions** (2026-10-15):
   - CD-42: the project is licensed AGPL-3.0-or-later (`LICENSE`). Linked Go dependencies must have AGPL-compatible licences, checked by the tier-V target `make licenses-check` (plan 23 §14.11, gate 23-AC-28 at F02).
   - CD-43: existing GitHub repositories (mostly Python, Rust, TypeScript/JavaScript) migrate into the Forgejo org `projects`, with optional per-repository push mirroring back to GitHub. Import goes through `forge.yaml` and `forge apply`. There are onboarding templates per language, a separate mirror credential (`SC-GITHUB-PROJECT-MIRROR`), the procedure `onboard-project-repository.md`, three C10 acceptance criteria, and a new Phase-5 node **S01** (first real project repository). The Self-Hosting Threshold is not widened.
   - CD-44: Phase-1 agent scaffolding in `.claude/` (`/sc-node` skill, tester/implementer/reviewer role agents, guard hook), so the build follows RED → GREEN → REVIEW from the first commit. Plan 52 now allows declared stubs in Phase-1 RED commits, which fixes a gap that made RED impossible for new Go symbols.
   - CD-45: F01 builds the seed `ci` image and the `./bootstrap exec` subset (plan 02 §14.9, 02-AC-14..17), so every Make target runs in one pinned container from the first commit. This fixes an ordering defect: GATE-F01 needed the image that F03 owned.

## SPEC §125 Stage-0 Definition of Done

| # | Item | Status | Evidence |
|---|---|---|---|
| 1 | SPEC.md has no known contradiction | ✅ | `plans/spec-issues.md`: 59 entries, all resolved or recorded interpretations. SI-01/12/31/32 were resolved by operator decision, and SI-32 by the v1.2 amendment. |
| 2 | Every subsystem has a plan | ✅ | master plan §11; 55 plans (01–55) |
| 3 | Every significant interface has a contract | ✅ | master plan §10; 52 contract files covering every SPEC §122 item. Remaining minor interfaces are justified in their plans (GC-19). |
| 4 | Provider abstraction is defined | ✅ | plans 03, 04, 05; `provider.schema.yaml` |
| 5 | Paid-resource guardrails are defined | ✅ | plan 03 (plan-JSON allowlist, cost ceiling, typed confirmation); plan 50 storage and budget confirmations (SI-40) |
| 6 | OpenTofu state lifecycle is defined | ✅ | plan 06; ADR-0100; `recover-opentofu-state.md`; SI-41 (separate state account in shared mode) |
| 7 | Node baseline is defined | ✅ | plan 08; ADR-0155, ADR-0157 |
| 8 | Networking/encryption is defined | ✅ | plans 09, 36; ADR-0011 (flannel wireguard-native), ADR-0150 (egress proxy) |
| 9 | TLS lifecycle is defined | ✅ | plan 10; ADR-0152; `certificate-failure.md` |
| 10 | Authentik identity architecture is defined | ✅ | plan 15; ADR-0250; AUTH-1 MFA hardening (closure-b) |
| 11 | Break-glass access is defined | ✅ | plan 15, ADR-0251, `break-glass-access.md`, `rotate-break-glass.md`; quarterly drill |
| 12 | Bootstrap sequence is fully described | ✅ | plan 50 (phases, journal); CD-19 (unattended to CONTROL TRANSFER COMPLETE) |
| 13 | Bootstrap resume/idempotency is described | ✅ | plan 50 §17; phases frozen after the switch (BOOT-2); crash-injection AC 50-AC-33 |
| 14 | Seed → Forgejo → Flux transfer is fully described | ✅ | plan 19; ADR-0005 (amended), ADR-0300; §34 integration test with mutation and crash cases |
| 15 | TDD state machine is fully defined | ✅ | plan 29; `contracts/state-machine.yaml` (revision 3) |
| 16 | Verification-first exemption policy is defined | ✅ | plan 22; ADR-0353; `verification.schema.yaml` |
| 17 | Acceptance-criteria schema is defined | ✅ | plan 21; `acceptance-criteria.schema.yaml` |
| 18 | Red-verification semantics are defined | ✅ | plan 22 (R0–R13); ADR-0351, ADR-0352; `red-result.schema.yaml` |
| 19 | Test-protection policy is defined | ✅ | plan 22; `test-manifest.schema.yaml`; TDD-1/2 closed |
| 20 | CI enforcement of RED/GREEN provenance is defined | ✅ | plans 20, 22, 55 (merge facts F1–F12); ADR-0008, ADR-0019 (amended), ADR-0302; merge and approval are human-only (CD-38, CD-39) |
| 21 | Self-hosting threshold is exactly defined | ✅ | plan 46; DAG node R02 |
| 22 | One-node → three-node transition is defined | ✅ | plan 47; `expand-to-small-ha.md`; H01–H04 |
| 23 | Durable-state ownership is defined | ✅ | plan 11 register; master plan §8 |
| 24 | Secret ownership and rotation are defined | ✅ | plan 35; `contracts/secret-classes.yaml` (71 classes); `rotate-secrets.md` |
| 25 | Network boundaries are defined | ✅ | plan 36 allow matrix; ADR-0150 |
| 26 | Admission/security boundaries are defined | ✅ | plans 34, 37 (VAP catalogue plus escape matrix); ADR-0014, ADR-0503 |
| 27 | Model/harness abstraction is defined | ✅ | plans 28, 31, 53; ADR-0004 (amended: allowlisted, token-priced surface) |
| 28 | Cost-record schema is defined | ✅ | plan 32; `cost-record.schema.yaml`, `pricing.schema.yaml` |
| 29 | Economic circuit breakers are defined | ✅ | plan 33; ADR-0450..0452 (three-source emergency stop); `maxCostUsd` is a hard ceiling (CD-28) |
| 30 | Observability architecture is defined | ✅ | plans 38–41; ADR-0550..0552 |
| 31 | Alerting/notification behavior is defined | ✅ | plan 42; `alert-catalog.yaml` (278 alerts); ADR-0009 (amended) |
| 32 | Retention/GC policy is defined | ✅ | plan 43 |
| 33 | Software supply-chain policy is defined | ✅ | plan 23; ADR-0501, ADR-0502; upstream keyless signing (NC-4) |
| 34 | Backup destinations are defined | ✅ | plan 14 (ten buckets, protection model); plan 44 catalogue |
| 35 | RPO/RTO implementation is defined | ✅ | plan 44 §9.6; plan 45 RPO/RTO table, with data-availability RTO and service restoration SR-1 ≤ 4 h / SR-2 ≤ 6 h (CD-32); `dr-marker-heartbeat` |
| 36 | Restore testing is defined | ✅ | plan 44 (RT-1..RT-7); `restore-test-result.schema.yaml` |
| 37 | Black-start recovery is defined | ✅ | plan 45; `github-black-start.md`, `total-cluster-loss.md`; two-stage recovery report (DR-1); RD3 drill is a hard gate of R02 (CD-35); bootstrap capsule at H04 (CD-33) |
| 38 | Destructive operations are defined | ✅ | plan 49; `safe-destruction.md` |
| 39 | All major failure modes have procedures | ✅ | all 39 SPEC §113 procedures plus 24 discovered ones (master plan §12) |
| 40 | Dependency DAG exists | ✅ | `plans/dag.yaml` (acyclic, 43 nodes); master plan §13; `make dag-check` (plan 01) |
| 41 | Every DAG node has acceptance criteria | ✅ | `PLAN.md`: every node has GATE ACs; 1,190 total; enforced by validator C06/C07 |
| 42 | Deterministic CI strategy exists | ✅ | plan 52; ADR-0018 (amended), ADR-0303; `lab-seed` lane (CD-36); Stage-0 validator in CI (ADR-0021) |
| 43 | MVP/post-MVP boundary is explicit | ✅ | master plan §15 |
| 44 | No unresolved issue blocks implementation | ✅ | No open question is marked blocking (validator C13). Versions are frozen in the compatibility baseline (CD-29); remaining version-sensitive facts are `(verify)` items with re-verification ACs inside the owning gates; the OVH ordering spike (plan 04) gates P02 only for `PROVIDER=ovh`, with the `existing` fallback. |

## Known residual risks (accepted, recorded)
See plan 34's residual-risk register. Highlights:
- Code under test can in principle forge its own output (SI-07, SI-49).
- Forgejo status contexts are not bound to identities; forgery is detected, not prevented (SI-21). Since CD-38 a forged status can no longer produce a merge without a human in `sc-mergers`.
- `sc-keys` holds repository admin for deploy keys; a stolen token could weaken branch protection within the 1-minute probe interval. This is detected, not prevented (CD-40, R-16-1).
- With one human operator, elevated-path changes rely on a 24-hour cooling-off instead of a second reviewer (CD-39, SI-59).
- Rogue-agent label claims are detected, not prevented (R-21).
- Upstream platform operators have cluster-wide Secret read (SI-54).
- Repository exfiltration through allowed egress (SI-53).

## Next step
Implementation is a separate task. Start at DAG node **F01** in `PLAN.md`, following its "How to execute a node" rules: verification first, RED before GREEN, gate evidence recorded here.
