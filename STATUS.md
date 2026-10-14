# STATUS

**Current stage:** Stage 0 (Complete Engineering Definition) is **complete**. Stage 1 implementation can begin at DAG node F01 (`PLAN.md`).
**Last updated:** 2026-10-14
**SPEC version:** 1.2. The operator amended node disk sizing during Stage 0; see the Amendments note in `SPEC.md`.
**Implementation status:** not started, by design (SPEC §2, §125).

## What Stage 0 produced

| Artifact | Count | Location |
|---|---|---|
| Master plan (architecture, decisions, inventories, DAG, sequence, MVP boundary) | 1 | `plans/00-master-plan.md` |
| Subsystem plans (each with all 25 SPEC §121 sections) | 55 | `plans/01-*.md` … `plans/55-*.md` |
| Machine-readable DAG (42 nodes, acyclic) | 1 | `plans/dag.yaml` |
| Implementation sequence (generated from the DAG and the gate sections; 1,138 gate acceptance criteria) | 1 | `PLAN.md` |
| Contracts (JSON Schema 2020-12, OpenAPI 3.1, data files) | 51 files (47 schemas validated) | `contracts/` |
| Operator procedures | 63 | `procedures/` |
| ADRs (20 global + subsystem ADRs) | ~100 | `adr/` |
| SPEC issues register (58 entries, none blocking) | 1 | `plans/spec-issues.md` |
| Consolidation decisions (CD-01..CD-26) | 1 | `plans/reviews/consolidation-decisions.md` |
| Independent fresh-context reviews (SPEC §132 items 43–47), plus closure verification | 10 + 3 | `plans/reviews/review-*.md`, `plans/reviews/closure-*.md` |
| Verified third-party lessons from the wiped implementation | 1 | `plans/reference/prior-implementation-lessons.md` |

## How Stage 0 was verified
1. **Structural checks, all passing.**
   - Every schema is meta-valid; every `examples` entry validates; every `x-invalid-examples` entry fails.
   - The data files validate against their schemas: the state machine (24 states, 94 transitions, every state reachable and every non-terminal state able to terminate in both modes), the secret register (67 classes) and the alert catalogue (268 alerts).
   - Every plan has all 25 template sections.
   - Every DAG node has `GATE-<node>` acceptance criteria.
   - There are no dangling procedure or contract references.
   - Every alert name used resolves in the catalogue, and every secret-class token resolves in the register.
2. **Independent reviews.** Ten fresh-context reviewers covered bootstrap, storage, security, Authentik/break-glass, TDD enforcement, Agent Runner, cost accounting, disaster recovery, seed→Flux handoff, and global consistency/DAG. They found 13 blockers and about 130 majors.
3. **Fix rounds.** Owners fixed their own files. Cross-group items were routed to their owners. Lead decisions are recorded as CD-xx.
4. **Closure verification.** Three further fresh-context verifiers checked every blocker and major against the current files: **all 13 blockers closed, 0 open**. The partially-closed items and new contradictions they found were fixed in a final targeted round.
5. **Operator decisions** (2026-10-14):
   - CD-23: 8 GiB nodes stay allowed as `constrained`.
   - CD-24: the OVH default stays VPS-2.
   - CD-25: SPEC v1.2 disk sizing, 100 GiB minimum and 200 GiB preferred.
   - CD-26: a Bitnami template-only library subchart vendored in an upstream chart is allowed.

## SPEC §125 Stage-0 Definition of Done

| # | Item | Status | Evidence |
|---|---|---|---|
| 1 | SPEC.md has no known contradiction | ✅ | `plans/spec-issues.md`: 58 entries, all resolved or recorded interpretations. SI-01/12/31/32 were resolved by operator decision, and SI-32 by the v1.2 amendment. |
| 2 | Every subsystem has a plan | ✅ | master plan §11; 55 plans (01–55) |
| 3 | Every significant interface has a contract | ✅ | master plan §10; 51 contract files covering every SPEC §122 item. Remaining minor interfaces are justified in their plans (GC-19). |
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
| 20 | CI enforcement of RED/GREEN provenance is defined | ✅ | plans 20, 22, 55 (merge facts F1–F12); ADR-0008, ADR-0019 (amended), ADR-0302 |
| 21 | Self-hosting threshold is exactly defined | ✅ | plan 46; DAG node R02 |
| 22 | One-node → three-node transition is defined | ✅ | plan 47; `expand-to-small-ha.md`; H01–H04 |
| 23 | Durable-state ownership is defined | ✅ | plan 11 register; master plan §8 |
| 24 | Secret ownership and rotation are defined | ✅ | plan 35; `contracts/secret-classes.yaml` (67 classes); `rotate-secrets.md` |
| 25 | Network boundaries are defined | ✅ | plan 36 allow matrix; ADR-0150 |
| 26 | Admission/security boundaries are defined | ✅ | plans 34, 37 (VAP catalogue plus escape matrix); ADR-0014, ADR-0503 |
| 27 | Model/harness abstraction is defined | ✅ | plans 28, 31, 53; ADR-0004 (amended: allowlisted, token-priced surface) |
| 28 | Cost-record schema is defined | ✅ | plan 32; `cost-record.schema.yaml`, `pricing.schema.yaml` |
| 29 | Economic circuit breakers are defined | ✅ | plan 33; ADR-0450..0452 (three-source emergency stop) |
| 30 | Observability architecture is defined | ✅ | plans 38–41; ADR-0550..0552 |
| 31 | Alerting/notification behavior is defined | ✅ | plan 42; `alert-catalog.yaml` (268 alerts); ADR-0009 (amended) |
| 32 | Retention/GC policy is defined | ✅ | plan 43 |
| 33 | Software supply-chain policy is defined | ✅ | plan 23; ADR-0501, ADR-0502; upstream keyless signing (NC-4) |
| 34 | Backup destinations are defined | ✅ | plan 14 (ten buckets, protection model); plan 44 catalogue |
| 35 | RPO/RTO implementation is defined | ✅ | plan 44 §9.6; plan 45 RPO/RTO table; `dr-marker-heartbeat` |
| 36 | Restore testing is defined | ✅ | plan 44 (RT-1..RT-7); `restore-test-result.schema.yaml` |
| 37 | Black-start recovery is defined | ✅ | plan 45; `github-black-start.md`, `total-cluster-loss.md`; two-stage recovery report (DR-1) |
| 38 | Destructive operations are defined | ✅ | plan 49; `safe-destruction.md` |
| 39 | All major failure modes have procedures | ✅ | all 39 SPEC §113 procedures plus 24 discovered ones (master plan §12) |
| 40 | Dependency DAG exists | ✅ | `plans/dag.yaml` (acyclic, 42 nodes); master plan §13; `make dag-check` (plan 01) |
| 41 | Every DAG node has acceptance criteria | ✅ | `PLAN.md`: every node has GATE ACs; 1,138 total |
| 42 | Deterministic CI strategy exists | ✅ | plan 52; ADR-0018 (amended), ADR-0303 |
| 43 | MVP/post-MVP boundary is explicit | ✅ | master plan §15 |
| 44 | No unresolved issue blocks implementation | ✅ | No open question is marked blocking. Version-sensitive third-party facts are `(verify)` items with re-verification ACs inside the owning gates; the OVH ordering spike (plan 04) gates P02 only for `PROVIDER=ovh`, with the `existing` fallback. |

## Known residual risks (accepted, recorded)
See plan 34's residual-risk register. Highlights:
- Code under test can in principle forge its own output (SI-07, SI-49).
- Forgejo status contexts are not bound to identities; forgery is detected, not prevented (SI-21).
- Rogue-agent label claims are detected, not prevented (R-21).
- Upstream platform operators have cluster-wide Secret read (SI-54).
- Repository exfiltration through allowed egress (SI-53).

## Next step
Implementation is a separate task. Start at DAG node **F01** in `PLAN.md`, following its "How to execute a node" rules: verification first, RED before GREEN, gate evidence recorded here.
