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
| Consolidation decisions (CD-01..CD-59) | 1 | `plans/reviews/consolidation-decisions.md` |
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
   - CD-46: agent PRs are opened by the machine account `sc-agent-bot`; `main` requires 1 approval, so only the operator can approve and merge; a session-wide guard hook blocks merge, approve and settings changes through `gh`.
   - CD-47: an automated review check replaces the manual approval: after the reviewer agent approves, `sc-agent-bot` posts the commit status `sc-review` on the PR head. `main` needs 0 approvals, and a "Restrict updates" ruleset lets only the operator merge (plan 52 §8.2a).
   - CD-48: `toolbox-check` is a plan 52 red target, so RED commits for `./bootstrap` and `toolbox/**` can name the wrapper tests (found during F01 slice S1).
   - CD-49: rootless Podman runs with `--userns=keep-id --user $(id -u):$(id -g)`, because the image `USER` overrides keep-id (plan 02 §14.4; found in the F01 slice S1 review).
   - CD-50: RED stub files may also hold type declarations (plan 52 §8.3a); `make test` was added before the F01 S2 RED so that RED re-runs by its declared target (found in the F01 slice S2 review).
   - CD-51: `go.yaml.in/yaml/v3` is the Go YAML library and the module's first third-party dependency (found in F01 slice S3).
   - CD-52: the scheduled e2e skip uses a cache marker instead of `gh run list` (only `contents: read`); every workflow job declares its own permissions; the `toolbox` e2e filter also covers `bootstrap` and `test/e2e/wrapper/**` (found in the F01 slice S3 review).
   - CD-53: with `SC_FORBID_PAID=1` the Gateway accepts only fake upstream hosts (loopback, `localhost`, `*.invalid`, `*.test`) and fails closed on everything else (found in the F01 slice S4 review).
   - CD-54: the TDD checker reads `TDD-Red-Target:` from anywhere in the message and adapts wrapper-harness output into testcases; the guard hook also blocks role-agent shell writes into owned paths (found in the F01 slice S6 review).
   - CD-55: `reservedBranchPrefixes` is added to the repo-policy schema, as plan 01 describes.
   - CD-56: a red target missing at the base contributes an empty baseline; `redTargetFallbacks` covers commits made before the Make target existed; the wrapper-output adapter and the T4 line rules are specified precisely (decided before F01 slice S5).
   - CD-57: legitimately superseded RED tests are declared with a `TDD-Superseded:` line on the PR head commit; `cloneImage` and `verify.installerImage` are optional in the ci-policy schema until their images exist (found in the F01 slice S5 dry run).
   - CD-58: the TDD checker trusts the base revision's policy and checker (head only for the F01 bootstrap); `agent/*` is excluded only when provenance is bound; red-target values are validated (found in the F01 slice S5 review).
   - CD-59: a declared stub may be a marked field added to an existing struct; the operator granted F01 slice S5 one extra implementer attempt, beyond SPEC §74's 3, for this rule only.
   - CD-60: `sc-contracts` validates with `github.com/santhosh-tekuri/jsonschema/v6`; OpenAPI 3.1 is validated against the embedded official OAS 3.1 schema with the same library, with no OpenAPI dependency (decided before F02); its regex engine is `github.com/dlclark/regexp2`, because the contracts use lookaround (F02 S1).
   - CD-61: the operator granted F02 slice S1 one extra implementer attempt, beyond SPEC §74's 3, to fix the `responses.default` `$ref` regression only.

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

## Implementation progress

### F01 — repo-skeleton

Branch `node/F01-repo-skeleton`, built with `/sc-node F01` (CD-44). Base `main` 0d4e721. Five slices: S1 seed `ci` image and `./bootstrap exec`; S2 layout-check; S3 locks, secrets scan, quarantine and CI workflows; S4 `make verify`, lint and determinism; S5 TDD discipline checker. **Status: all seven slices approved (S7 fixed Docker CI); node-level evidence from PR #144.**

#### S1 — seed `ci` image, `./bootstrap exec`, Makefile re-exec

| Round | RED | GREEN | Review |
|---|---|---|---|
| 1 | 76836b4 (55 checks failing, assertion-class) | 3b3169c, a3b7c27, cd3e356, f6bce80 | `test_spec_conflict`: plan 02 §14.4 podman-rootless row (resolved by CD-49, af2a52c); also CD-48 (dbe178d), `toolbox-check` as a red target |
| 2 | 9a10846 (2 SIGTERM checks failing) | aefc4c0 | `request_changes`: 02-AC-14 literal command untested; unpinned-FROM guard used an unplanned code |
| 3 | 45b2fea (25 checks failing) | 15ab3b3 | **`approve`** at 15ab3b3 |

Red target: `sh test/e2e/wrapper/run.sh` (`TDD-Red-Target: toolbox-check`). Implementer attempts used: 3 of 3. CI: none for S1. No workflow ran on branch pushes before `verify.yaml` (S3).

| Criterion | Test(s) | Result at 15ab3b3 |
|---|---|---|
| 02-AC-14 | `test/e2e/wrapper/real-engine.sh` (engine `build --no-cache`, then literal `--toolbox-build … fetch-pinned --check`, GOVERSION, `SC_IN_CONTAINER`); `stub-unpinned-from.sh` | pass, rootless Podman 5.4.2; Docker rootful open (S3 CI) |
| 02-AC-15 | `real-engine.sh` (`TOOLBOX_CHECKSUM_MISMATCH:shfmt`, no tag left); `check-pins.sh` (`FROM golang:1.27`); `stub-exec-contract.sh` | pass on host and in `ci` image; Docker open (S3 CI) |
| 02-AC-16 | `real-engine.sh`; `stub-exec-contract.sh` (per-mode flags, TTY, `--network none`, SIGTERM) | pass, rootless Podman; Docker rootful open (S3 CI) |
| 02-AC-17 | `stub-image-reuse.sh` (17 checks) | pass |
| 01-AC-5 | `makefile-reexec.sh` (9 checks) | pass; full tier-V order asserted in S4 |

Deviations and open items:
- Docker-rootful runs of `real-engine.sh` and `check-pins.sh` must run in S3's `verify.yaml`. 02-AC-14..16 stay open until they pass.
- The wrapper passes `build --cgroup-manager=cgroupfs` when `$XDG_RUNTIME_DIR/bus` is absent (hosts without a user session bus). This is not in plan 02.
- Reviewer low findings, not fixed in S1 (retry budget spent; carry into a later slice that touches `bootstrap`): L1, a missing or unreadable `toolbox/Containerfile` makes the build path exit 2 with no message; L2, stage names are compared case-sensitively, and BuildKit is case-insensitive.
- Accepted residual risk: an image built from an unpinned Containerfile by older wrapper code can still be reused, because the 02-R-1 guard runs only before a build.

#### S2 — layout-check and repository hygiene

The branch was rewritten after S1 with operator approval (CD-50). Round 1 (RED dc09d49 + 9ceb865, GREEN 80e12fa) got `request_changes`: the RED's declared target `make test` did not exist at the RED commit. The new history puts a `make test` commit before the RED.

| Commit | Role |
|---|---|
| 08548d0 | `make test` with `go test -json` and the JUnit converter (`src/internal/testreport`, `sc-lint junit`). Bootstrapping exception, no RED; converter tests are S4 work |
| 0d80999 | RED: `src/internal/lint` tests, fixtures, declared stubs and types (CD-50). At R, `make test GO_TEST_PKGS=./src/internal/lint/...` exits 2 with 21 top-level failures and 0 build errors |
| 361fbd3 | RED: 01-AC-11 instance validation (operator: the Stage-0 venv `jsonschema` at F01; F02 replaces it with `sc-contracts`) |
| f324222 | docs CD-50 |
| df735bf | RED: executable-files exactly per plan 01 §6.1, lettered AC ids, `make` mentions inside backtick spans; clears the x-bit on `test/e2e/wrapper/*.sh`. 28 top-level failures |
| 9745770 | GREEN (attempt 2 of 3): **`approve`** at 9745770 |

Red target: `test GO_TEST_PKGS=./src/internal/lint/...`. CI: none yet (S3).

| Criterion | Test(s) | Result at 9745770 |
|---|---|---|
| 01-AC-1 | `TestLayoutCheck_{ValidFixturePasses,RealTreePasses,Negatives,SizeBoundary,EachGitignoreEntryRequired,ExecutableFilesFollowPlan}`, `TestRealTree_{GitignoreHasAllEntries,NoStrayExecutables}`, `TestMakeWiring` | pass; `make layout-check` 0 |
| 01-AC-2 | `TestRealTree_CheckIgnore_01AC2` | pass; already satisfied by S1's `.gitignore` |
| 01-AC-10 | `TestLayoutCheck_AgentsMd{Size,PolicyMarkers,MarkerKeyFromVerificationPolicy}`, `TestRealTree_AgentsMd` | pass |
| 01-AC-11 | `TestRealTree_{PolicyProtectedPathsEqualPlan,StrangeCompanyFilesKinds,ContractsTarget,MakeContractsValidatesInstances}`, `TestMakeContracts_{RejectsSchemaViolation,RejectsWrongType,UnknownKindFails}` | pass; `make contracts` 0 |
| 01-AC-12 | `TestLayoutCheck_{MakeTargetsKnown,MakeTargetInsideSpan}`, `TestRealTree_PlannedPathsFileExists` | pass |
| 01-AC-14 | `TestDagCheck_{ValidFixturePasses,RealTreePasses,Negatives,UsesNonUpstreamIsWarning,WritesReport,LetteredACIds}` | pass; `make dag-check` 0 |

Deviations and open items:
- **Before F01 completes (assigned to S4):** the host re-exec passes `$(MAKEOVERRIDES)` unquoted (`Makefile:14`). So `make test GO_TEST_RUN='TestA$'` from the host selects zero tests and exits 0, and `'^(A|B)$'` is a shell syntax error. In-container runs are correct. Also, `src/internal/lint/hygiene_test.go` fails `gofmt -l`; it needs a whitespace-only `test(F01):` commit, which raises a T8 warning.
- **Deferred, no GATE-F01 criterion:**
  - the plan 01 §7.1 / 01-R-1 rule that every backticked plan path exists or is listed in `planned-paths.txt`; `planned-paths.txt` holds only `make` targets;
  - `SC_TARGET_TREE` support (plan 01 §7.3), which nothing consumes before plan 22.
- **Plan/contract conflict:** plan 01 §6.4 and §7.3 put `reservedBranchPrefixes` in `policy.yaml`, but `contracts/repo-policy.schema.yaml` has no such field and sets `additionalProperties: false`. This needs an operator decision.
- **Implementation choices the plan leaves open:**
  - most layout rule IDs;
  - `planned-paths.txt` format: one entry per line, `make <target>` for targets;
  - `DAG_USES_UPSTREAM` warning ID;
  - a duplicate node id reports under `DAG_UNKNOWN_NODE`;
  - the `make` mention scan skips the `<make target>` placeholder.
- **Low reviewer findings:**
  - a `###` subheading ends the "Enforced rules" marker scan;
  - the marker key lookup is textual;
  - `make -n verify`-style mentions are not checked.

#### S3 — locks, secrets scan, quarantine, interim CI workflows

| Round | RED | GREEN | Review |
|---|---|---|---|
| 1 | 9937c8c (23 top-level failures) | 2eecac8; workflows 483d87a (implementer-written, orchestrator-committed with operator approval) | `request_changes`: one-line PEM, fail-open e2e filter, schedule vs permissions, placeholder leniency, `maxEntries: 0`, map-order output, Q2 packages, job-level permissions |
| 2 | a331135 (9 failures) | orchestrator workflows 0fd1752 (CD-52); 8ee2196 | `request_changes`: first match per line only; repeated-character allowance vs plan 35's closed allowlist; legacy encrypted PEM; cache post step |
| 3 | f108607 (3 failures; adds `test/fixtures/secrets-scan/allowlist.yaml`) | orchestrator e81af85; 3715111 | **`approve`** at 3715111 |

Red target: `test GO_TEST_PKGS=./src/internal/lint/...`. Implementer attempts used: 3 of 3. Operator decisions: CD-51 (`go.yaml.in/yaml/v3`), CD-52 (e2e schedule with a cache marker, job-level permissions, `toolbox` filter), and a closed two-entry secrets allowlist. CI: not run yet (no PR).

| Criterion | Test(s) | Result at 3715111 |
|---|---|---|
| 01-AC-7 | `TestLocksCheck_{ValidFixturePasses,Mutations,SchemaViolation,Deterministic}`, `TestRealTree_{MakeLocksCheck,LocksCheckPasses}` | pass; `make locks-check` 0 |
| 01-AC-8 | `TestSecretsScan_{ValidFixturePasses,Negatives,PEMOneLine,Placeholders,Deterministic,MultipleMatchesPerLine,AllowlistSemantics,LegacyEncryptedPEM}`, `TestRealTree_{MakeSecretsScan,SecretsScanPasses,SecretsAllowlistExact}`, `TestVerifyRunsSecretsScanAndQuarantine` | pass; `make secrets-scan` 0 |
| 52-AC-1 | `TestWorkflow_{VerifyJobs,ModeGating,VerifyShape,Permissions,NoSecretsAndPinnedUses,E2EGate,E2ESchedule,E2EPathFilter}`, `TestLint_RunsActionlint` | pass; `make lint` 0; first Actions run pending |
| 52-AC-7 | `TestQuarantineCheck_{ValidPasses,Negatives,UsesHeadCommitterDate,MaxEntriesZero,ProtectedPackages}`, `TestRealTree_{MakeQuarantineCheck,QuarantineListIsEmpty}` | pass; `make quarantine-check` 0 |
| S1 obligation (02-AC-14..16 on Docker) | `TestWorkflow_RunsRealEngineChecksOnDocker` (e2e.yaml `toolbox` job) | wired; needs an Actions run |

Deviations and open items:
- **Secrets-scan follow-ups (to be scheduled before F01 completes):**
  - F1: the scan skips the whole allowlist file, so a real key placed in it goes unreported;
  - F2: BEGIN, a blank line, then a base64 body is not flagged;
  - F3: the trailing-`EXAMPLE` exemption applies to every pattern, not only AWS;
  - F4: the stale-entry message repeats the path.
- **Deferred:**
  - the full gitleaks `rules.toml` (plan 01 §9); the scanner's patterns are hard-coded in Go;
  - Q2 bullets beyond the name pattern and the three protected packages (test manifests, DAG-gate e2e tests, contract examples), tracked against F04–F06 and plan 22;
  - `TestWorkflow_E2EPathFilter` would still pass if `|| true` were added to the diff step.
- **Implementation choices the plan leaves open:**
  - rule IDs for locks and secrets;
  - lock and quarantine schema validation runs through the Stage-0 venv;
  - cross-file lock rules have nothing to compare until `versions/` lock files exist.
- **Process:** the S3 implementer wrote `.github/**` through the shell, although the guard bars role agents from that path; the guard's edit check does not cover shell writes. The operator approved committing the files after the orchestrator read them. The guard fix is pending an operator decision.

#### S4 — `make verify`, lint and determinism, build, no-paid

| Round | RED | GREEN | Review |
|---|---|---|---|
| 1 | cbe7816 (11 failures); 6b96b5c `style`: gofmt of an S2 RED file | d916776 | `request_changes`: `GO_TEST_SKIP` knob, Go test cache, ci env defaults, gateway too narrow, D7/D8 scoping, host re-exec quoting |
| 2 | ee0bce2 (wrapper, 6 failures); 9c8d6b1 (Go, 7 failures, all new). The nested-verify Go test moved to `test/e2e/verify/run` (operator-approved; it failed at every RED). A first attempt, 644848c, was discarded unpushed | orchestrator 998ee2c (`verify-e2e` job); 1d0134a | `request_changes`: verify e2e failed on a clean checkout (container Go caches outside `.cache`); `verify.yaml` not offline; filter breadth; `SC_IN_CONTAINER` forwarding; userinfo |
| 3 | 339dde4 (gateway, 1 failure); b9e1846 (wrapper, 5 failures) | orchestrator 851d60b (offline verify job, wider filters); 4b6de68 | **`approve`** at 4b6de68 |

Red targets: `test GO_TEST_PKGS=./src/internal/lint/... ./src/internal/gateway/...` and `toolbox-check`. Implementer attempts used: 3 of 3. Operator decision: CD-53. CI: not run yet (no PR).

| Criterion | Test(s) | Result at 4b6de68 |
|---|---|---|
| 01-AC-3 | `test/e2e/verify/run`; `TestFmtCheck_FailsOnDiff`; host `make verify` (rootless Podman, no Go/tofu/kubectl): 12 of 12 targets pass | pass on Podman; Docker run via the `verify` and `verify-e2e` jobs (pending) |
| 01-AC-5 (order) | `TestVerify_DryRunSequenceOrder_01AC5`; `test/e2e/wrapper/makefile-reexec.sh` (19 checks) | pass |
| 01-AC-6 | `test/e2e/verify/run` in a fresh clone with no `.cache` (warm-up, then two `--network none` runs pass); `cache-env.sh`; `TestGitopsCache_OfflineNoop`; `TestTestTarget_{NoSelectionKnob,NoTestCache}` | pass |
| 01-AC-9 | `TestBuild_{Reproducible,RecipeFlags,DescribeAbbrev}`; two fresh clones produce an identical `sc-lint` sha256 | pass |
| 52-AC-5 | `test/e2e/verify/run` (c): summaries identical after deleting `duration_ms` and `started_at` | pass |
| 52-AC-6 | `TestGolangciLint_{ForbidigoFixtures,ForbidigoScoping}`, `TestNetcheck_*`, `TestLint_*` | pass |
| 52-AC-8 | `forbidpaid_test.go` (6 tests, CD-53 allowlist), `TestContainerfile_CIEnvDefaults`, `verify/run` with `SC_FORBID_PAID=1` | pass |

Deviations and open items (follow-up slice S6, before S5):
- **Data loss:** `test/e2e/verify/run` runs `rm -rf .cache/go out` in the checkout it starts in. Run in the main checkout, it deleted `out/`, including the orchestrator brief, which was regenerated. It must run in a temporary clone.
- **Override whitespace:** the `$(strip …)` around the re-exec line collapses whitespace inside quoted override values, a regression from 1d0134a.
- **CI path:** `verify.yaml` runs `./bootstrap exec … -- make verify` directly, not host `make verify`. The two are equivalent under 01-AC-5, and host `make verify` was run on Podman.
- **Implementation choices:**
  - tier-V order follows plan 01 §7.1 catalogue order, with `gitops-cache` first;
  - `summary.json` has the shape `{"targets":[{target,result,started_at,duration_ms}]}`;
  - the gateway function is `gateway.ValidateUpstream`;
  - `GOFLAGS=-mod=readonly` in the image replaces `-buildvcs=false`, which `make build` passes explicitly;
  - the Go cache defaults are set in `sc-entrypoint`, and a caller's own values win.

#### S6 — carried follow-ups (run before S5)

| Round | RED / tests | GREEN | Review |
|---|---|---|---|
| 1 | 5503d4e (toolbox-check, 11 failures; also moves `test/e2e/verify/run` into a temporary clone); afad724 (lint, 4 failures); chore 18dc460 (JUnit converter tests, passing) | 2865990 | `request_changes`: the clone overlay was not faithful; RED target lines were not git trailers; wrapper REDs had no JUnit |
| 2 | orchestrator 943b918 (CD-54, CD-55, guard shell-write blocking, sc-tester trailer rule); chore bbf7d74 (faithful overlay plus a scratch-repo test); 4da04cb (CD-55, 7 failures); 3f245c9 (operator-approved correction of a self-contradicting CD-55 test, after the implementer's TEST_SPEC_CONFLICT) | 60f069e | **`approve`** at 60f069e |

Implementer attempts used: 3 of 3 (attempt 2 stopped with TEST_SPEC_CONFLICT). Red targets: `toolbox-check` and `test GO_TEST_PKGS=./src/internal/lint/...`.

| Follow-up | Test(s) | Result at 60f069e |
|---|---|---|
| `test/e2e/verify/run` never touches the checkout (it runs in a `git clone --no-local` under a temp dir) | `test/e2e/wrapper/verify-script.sh`, `test/e2e/wrapper/overlay.sh` (10 checks: staged rename, symlinks, non-ASCII name, deletions, failed copy) | pass; `sh test/e2e/verify/run` → 0, and `out/` and the brief stay untouched |
| Re-exec keeps override whitespace (01-AC-5) | `makefile-reexec.sh` | pass |
| Missing or unreadable Containerfile → exit 3; stage names case-insensitive (02-R-1, plan 02 §21) | `stub-unpinned-from.sh` | pass |
| Secrets scan: allowlist file scanned, `EXAMPLE` only for AWS, BEGIN + blank + body flagged, stale path named once (01-AC-8) | `secrets_followups_test.go` | pass |
| JUnit converter (CD-50) | `convert_cases_test.go` | pass |
| `reservedBranchPrefixes` (CD-55) | `reserved_prefixes_test.go` | pass; the field is optional in the schema, and this repository's exact list is enforced by test |

Follow-ups recorded (not blocking):
- `makefile-reexec.sh:17` still inherits `MAKEFLAGS`/`MAKEOVERRIDES` when run from a make given command-line variables. A one-line `chore` fix is pending.
- `test/e2e/verify/run` removes stale summaries only at the end, so an early exit leaves an older summary in place, and the comment at :126 is out of date.
- `bootstrap:125` hashes an unreadable input as the literal `unreadable` (implementation choice, not in plan 02 §14.2 step 5).
- `verify-script.sh` passed at its own RED (5503d4e): it is a static guard, evidence only.
- Plan 22 §9.4 needs merge semantics for `reservedBranchPrefixes` (union, like `protectedBranches`).
- **The guard hook is a heuristic.** Whole-directory operands are now caught (fixed by the orchestrator after the review). It still misses `&>`, `cd dir && …`, writes through a variable, `perl -pi` and writes inside `./bootstrap exec`. It also has false positives for copying from a plan file and for paths inside a `git commit -m` message.
- **T8 warnings S5 must report:**
  - 6b96b5c (S2 RED file, gofmt only);
  - 3f245c9 (CD-55 test correction);
  - bbf7d74 (changes `test/e2e/wrapper/makefile-reexec.sh`, `run.sh` and `test/e2e/verify/run`).
- 5503d4e's `TDD-Red-Target:` sits in the body; CD-54 covers it.

#### S5 — TDD discipline checker (`make tdd-discipline-check`, `sc-lint tdd-discipline`)

| Round | RED | GREEN | Review |
|---|---|---|---|
| 1 | 23dc13b (11 failures); orchestrator ee06039 (the `tdd-discipline` job checks out the PR head and passes base, branch and author) | 9b3cb29, 20e6826. A branch dry run then found superseded RED tests and placeholder digests (CD-57) | operator decisions CD-57 (ffaf284) |
| 2 | chore af24f57 (`make contracts` validates `policy/ci.yaml`); ef52fff (5 failures) | f1620bd | `request_changes`: B1 skipped tests passed as superseded; B2 non-test files in a RED. Escalated: E1 the PR supplied the policy and the checker, E2 `agent/*` skipped the check |
| 3 | orchestrator 09804d0 (CD-58); 4cce571 (10 failures) | 4f185f0. The dry run rejected 4cce571's own struct-field stubs → CD-59 (00eb09e), one extra attempt granted | — |
| extra | f54d076 (2 failures) | a310389 | **`approve`** at a310389 |

Implementer attempts used: 3 of 3, plus 1 granted by the operator (CD-59). Red target: `test GO_TEST_PKGS=./src/internal/lint/...`.

| Criterion / rule | Test(s) | Result at a310389 |
|---|---|---|
| 52-AC-4 (17 outcomes) | `TestDiscipline/*` (25 subtests) | pass |
| T4 line, multiple packages, safe values (CD-54/58) | `TestDiscipline_RedTargetLine`, `TestTrusted_RedTargetInjection_CD58c` | pass (gap FU1) |
| T5 empty baseline, fallbacks, wrapper adapter (CD-56) | `TestDiscipline_Baseline`, `TestDiscipline_WrapperAdapterAndFallback` | pass |
| Several REDs per PR, T8, `chore` commits | `TestDiscipline_MultipleRedCommits` | pass |
| T7 supersession (CD-57) | `TestDiscipline_Superseded*`, `…SkippedNotAbsent_B1` | pass |
| T3 a RED holds only tests and stubs; stub markers; field stubs (CD-50/59) | `…RedWithOtherFiles_B2`, `…StubMarker*`, `…FieldStubs_*` | pass (gap FU2) |
| Trusted inputs; `agent/*` (CD-58) | `TestTrusted_*`; hand test against a scratch base that has the checker (`policy_source` and `checker_source` both `base`, cleanup verified) | pass |
| `policy/ci.yaml` equals the plan block and validates | `TestRealTree_*` | pass |
| 52-AC-3, checker side | branch dry run before this commit: 22 RED commits, all `build_ok`; `red.failed` holds 375 ids, including S2's layout-check RED 0d80999; the only failures were the two superseded sets, which this commit's `TDD-Superseded:` lines declare | pending the CI run on the PR |

Follow-ups (not blocking; FU1 needs an operator CD):
- **FU1:** the CD-58 safe set still allows `${`, `|` and parentheses. `GO_TEST_RUN=${shell …}` runs through make, and `GO_TEST_PKGS` is unquoted in `mk/test.mk`. This grants nothing new, since CI already runs PR code, but values should be stricter.
- **FU2:** an embedded field is accepted as a field stub, although it adds promoted methods. It should be rejected.
- **FU3:** an added blank import (`import _ "…"`, which runs `init`) is allowed in a RED.
- **FU4:** `mk/tdd.mk` checks the `$TDD_BASE` tip, but the checker reports from the merge base. The recipe should use `git merge-base`.
- **FU5:** the base-worktree build path has no end-to-end test; it was verified by hand.
- **FU6:** `mk/tdd.mk` and `verify.yaml` come from the PR head, so human review of these protected paths is the control. Full enforcement needs a workflow the PR cannot edit (Forgejo lane).
- **FU7:** the checker's temporary directory is not removed on an error return.
- **Runtime:** about 17–20 min on this branch, within the 40-minute job timeout.

#### S7 — Docker CI fixes

The first Actions run of PR #144 (54f43f3) failed every container job on Docker rootful: `/tmp` was mounted `--tmpfs /tmp:rw,nosuid,nodev,size=1g`, and Docker's tmpfs default is `noexec`, so neither Go binaries nor the wrapper's stub engines could run there. Rootless Podman allows exec, so every local run had passed. `check-pins.sh`'s "got exit 0" was a hard-coded failure message, printed because its precondition failed for the same reason.

| Commit | Role |
|---|---|
| 200dcc3 | RED (`toolbox-check`): the `/tmp` tmpfs must have `exec,nosuid,nodev,size=1g` and not `noexec` in all four engine modes (4 failures); `real-engine.sh` adds a /tmp-exec check and a `go run` check |
| 64e104a | GREEN: `--tmpfs /tmp:rw,exec,nosuid,nodev,size=1g` |
| 4e21991 | chore: the real-engine /tmp-exec check compares stdout only (rootless Podman prints warnings on stderr) |

Review: **`approve`** at 4e21991 (attempt 1 of 3). Security: no regression under plan 02 §13, since `nosuid` and `nodev` stay and the repository mount is already rw and exec. Follow-ups: name `exec,nosuid,nodev,size=1g` in plan 02 §14.3's mount table; give `check-pins.sh` a real failure message.

### F01 — node-level evidence

| Criterion | Evidence | Status |
|---|---|---|
| 01-AC-4 | Actions run [38978817161](https://github.com/tuckermclean/strange-company/actions/runs/38978817161) `verify` job (Docker rootful) on 4e21991: `summary.json` lists 12 of 12 targets `pass`; local host `make verify` on 4e21991 (rootless Podman) gives the identical target set and results | **pass** |
| 02-AC-14..16 on Docker rootful | run [38978817153](https://github.com/tuckermclean/strange-company/actions/runs/38978817153) `toolbox` job: `real-engine.sh` and `check-pins.sh` with `SC_CONTAINER_ENGINE=docker` | **pass** |
| 52-AC-5, 01-AC-6, 52-AC-8 on Docker | run 38978817153 `verify-e2e` job: two `--network none`, `SC_FORBID_PAID=1` runs of `make verify`, both 12 of 12 pass; summaries identical after removing timing fields | **pass** |
| 01-AC-3 (Docker host without Go) | the `verify` job above (the runner's Go is never used: everything runs in the ci image) | **pass** |
| 01-AC-13 / 52-AC-3 | the `tdd-discipline` job on this commit (the PR head): `report.json` with `red.failed` non-empty and `red.build_ok: true`, S2 layout-check RED 0d80999 included. On 4e21991 it failed only on the two superseded sets, which this commit declares | see the PR's run on this head |
| 52-AC-2 | operator adds `verify`, `tdd-discipline` and `e2e-gate` (source: GitHub Actions) to the `main-gates` ruleset after merge (plan 52 §8.2a) | operator action |

### F02 — contract-tooling

Branch `node/F02-contract-tooling`, built with `/sc-node F02`. Base `main` 56c280b (after operator PRs #145, the licence policy, and #146, the `go.yaml.in/yaml/v3` exception). Four slices: S1 `sc-contracts` schema checks, `report.json` and `junit.xml`; S2 instance validation for every plan 01 §8.3 glob; S3 the Python crosscheck; S4 `make licenses-check`. Decisions: CD-60 (validator library, OAS 3.1 approach, regexp2), CD-61 (one extra S1 implementer attempt). **Status: all four slices approved; gate evidence below; PR CI pending.** SHAs are those after the rebase onto 56c280b.

#### S1 — `sc-contracts` schema checks, report, junit (01-AC-20, 01-AC-21, 01-AC-24, 52-AC-10)

| Commit | Role |
|---|---|
| d43da0f | docs: CD-60 |
| 193e1a7 | RED: report/junit, six negative fixtures, version locks (11 tests fail on assertions) |
| 9154c22 | GREEN attempt 1: `src/cmd/sc-contracts`, `src/internal/contracts`, embedded OAS 3.1 schema, `make contracts` |
| 98ecdff | docs: CD-60 names regexp2 (the contracts use lookaround) |
| e0470b4 → bd09452 | review 1 request_changes → RED (7 fail) → fix attempt 2 (example index in details, value-less invalid examples fail, all OpenAPI schema examples, second YAML document rejected, `toolbox/image.lock`) |
| 46debd1, 5c7d0ee → 64f5c4d | review 2 request_changes → chore (per-check-ID accounting test) → RED (5 fail) → fix attempt 3 (`CONTRACT_KIND_AMBIGUOUS`, wrong-shaped containers fail, keyword-named properties, `x-` extensions skipped) |
| 6238b92 | ci: `verify.yaml` runs `make contracts` alone after an earlier failure, so `junit.xml` is always uploaded (52-AC-10; operator) |
| d1e2eb6, 2b0b9cf → b31aec9 | review 3 request_changes (regression: `responses.default` not walked) → CD-61 → RED (2 fail) → fix attempt 4 (one line) |
| — | review 4 **approve** at b31aec9 |

#### S2 — instance validation (01-AC-23)

| Commit | Role |
|---|---|
| 1d70aa3 | chore: three tests already satisfied by S1 (lock-glob violations, `kind: HelmLock` → `CONTRACT_KIND_UNKNOWN`, kind-less `versions/` file) |
| b114a23 → 9c4689c | RED (11 fail) → GREEN: every §8.3 glob (kind-selected, fixed, registers), `**` walker, pass lines naming file and schema, `make contracts` Go-only; S1 follow-ups (`x-` under operation `responses`, escaped `$ref` pointers, empty `x-defs-examples` list) |
| 5ee22fa → 9ab3941 | review 1 approve with non-blockers, fixed before S3: RED (4 fail) → fix (kind rows accept only their row's schema files; fixed rows validate the whole document; only operation `responses` skip `x-` keys) |
| — | review 2 **approve** at 9ab3941 |

#### S3 — Go/Python crosscheck (01-AC-22)

| Commit | Role |
|---|---|
| d15a407 → a7934e6 | RED (2 fail; deletes the unused `test/contracts/validate_instances.py`) → GREEN: `test/contracts/crosscheck.py`, `make contracts` runs both with a combined exit status |
| 3110156 → f5ff16b | review 1 request_changes (OpenAPI examples never paired; silent drops and zero pairs passed; OAS-structure and meta-validation verdicts unpaired) → RED (5 fail) → fix: every verdict row carries a structured `pair`; the crosscheck evaluates exactly those rows, fails on any mismatch, unevaluable pair or zero pairs; its YAML loader follows go.yaml.in/yaml/v3. Written by the implementer, committed by the orchestrator because the role guard blocks implementer edits to `test/contracts/crosscheck.py` (operator) |
| — | review 2 **approve** at f5ff16b |

#### S4 — licence compliance (23-AC-28)

| Commit | Role |
|---|---|
| e720971 | chore: `LICENSE` sha256 and policy-shape tests (already true) |
| 43149ec | RED: `sc-lint licenses` over fixture module caches, real tree, `make verify` composition (12 fail) |
| — | the implementer found plan 23 §14.11's `(verify)` false: licensecheck v0.3.1 classifies `go.yaml.in/yaml/v3` v3.0.5 (MIT plus the Apache-2.0 notice) at 85 % coverage → `NOASSERTION`. Per §14.11 the operator added a version-pinned exception (PR #146) |
| 05264da | GREEN: `sc-lint licenses`, `make licenses-check`, `make verify` runs it between `secrets-scan` and `contracts`; `github.com/google/licensecheck` v0.3.1 (committed by the orchestrator after the rebase) |
| 0965af9 | chore: three tests that assumed an empty exception list now accept the §14.11 exception (operator); `tdd-discipline` reports `TDD_RED_TEST_MODIFIED` for `licenses_test.go` as a warning |
| — | review **approve** at 0965af9 |

#### F02 — gate evidence

| Criterion | Test(s) / evidence | Status |
|---|---|---|
| 01-AC-20 | `TestMakeContracts_ReportHasRowPerCheck_01AC20`, `_PerCheckAccounting_01AC20`, `TestReview_InvalidExampleWithoutValue_01AC20`; real tree `sc-contracts: 942 checks, 0 failed` | pass |
| 01-AC-21 | `TestMakeContracts_NegativeFixtures_01AC21` (wrong `$id`, missing examples, passing invalid example, `date-time` violation, duplicate key, unresolved OpenAPI `$ref`), `TestReview*_01AC21`, `TestReview2*_01AC21`, `TestReview3*_01AC21`, `TestInstances*_01AC21` | pass |
| 01-AC-22 | `TestMakeContracts_Crosscheck*_01AC22`, `TestCrosscheck2_*_01AC22`; real tree `crosscheck: 399 pairs compared, 0 mismatches, 0 unevaluable` | pass |
| 01-AC-23 | `TestInstances_*_01AC23`, `TestInstances2_*_01AC23` (every §8.3 glob; `memory: 8GB` fails naming `substrate.schema.yaml`; `kind: HelmLock` → `CONTRACT_KIND_UNKNOWN`) | pass |
| 01-AC-24 | `TestMakeContracts_VersionLocks_01AC24`, `TestReview_*_01AC24`, `TestReview2_KindAmbiguous*_01AC24` | pass |
| 23-AC-28 | `TestLicenseFile_Sha256_23AC28`, `TestLicensePolicy_Shape_23AC28`, `TestLicenses_*_23AC28`, `TestVerifyRunsLicensesCheck_23AC28`; real tree: 5 modules, 4 classified and allowed, `go.yaml.in/yaml/v3` v3.0.5 by exception | pass |
| 52-AC-9 | demonstration PR #147 (base = this branch, closed unmerged): run [39056889365](https://github.com/tuckermclean/strange-company/actions/runs/39056889365/job/117466594871) on 1a20e01 failed `verify` at target `contracts`, `report.json` names the demo schema `demo-52ac9` entry `x-invalid-examples[0]` (`CONTRACT_INVALID_EXAMPLE_PASSES`); run [39057039213](https://github.com/tuckermclean/strange-company/actions/runs/39057039213/job/117467036761) on the fix 8f73e65 passed | pass |
| 52-AC-10 | `TestMakeContracts_JUnitMatchesReport_52AC10`; run 39056889365 artifact `verify-out`: `out/contracts/junit.xml` 952 testcases = 952 `report.json` rows; 6238b92 uploads it even when `verify` fails earlier | pass |
| TDD discipline | local `make tdd-discipline-check` on 0965af9 (checker and policy from base 56c280b): `pass`, 9 RED commits, 76 RED failures, `build_ok`, 1 warning (`TDD_RED_TEST_MODIFIED`, above) | pass; PR run pending |

Follow-ups (not blocking GATE-F02):
- Plan 01 §8.3: the `policy/**` row lists plans 22, 25, 26, 28, 31, 32, 33, 52, but `policy/ci.yaml` is plan 20's `CIPolicy`; correct the list before restricting that row's schemas in `sc-contracts`. A `CONTRACT_KIND_UNKNOWN` detail for a kind outside a row's allowed set is misleading.
- `test/contracts/validate_schema.py` (behind `sc-lint locks` and `quarantine`) still says F02 replaces it with `sc-contracts`; no F02 criterion covers it.
- Crosscheck: the ci image's Python `jsonschema` asserts no `date-time` or `uri` format (no `rfc3339-validator`, `rfc3987` in `tools/stage0/requirements.txt`), so Go/Python leniency on those formats is invisible; the Python loader differs from go.yaml.in on overflow floats (`1e400`) and `.1_5`; a Go-only metaschema rejection carries no `pair`; an empty `pair` object is skipped.
- Licences: licensecheck v0.3.1 has no `-or-later` ids, so any GPL/LGPL/AGPL module needs an exception; versions are not case-escaped when locating the module cache (prerelease tags) and `go list` `Replace` is ignored; excepted rows are not marked in `report.json`; separate licence files combine as OR; the missing-module message is misleading; the `sc-lint` usage string omits `licenses`.
- Role guard: `test/contracts/crosscheck.py` is check logic under a test path, so implementers cannot edit it after it exists; the orchestrator commits such edits.

### F03 — toolbox-image

Branch `node/F03-toolbox-image`, built with `/sc-node F03`. It is based on `main` 665a3a9, after PR #150 (issue #149, CI authority from the PR base) and PR #151 (CD-63, a fourth implementer attempt escalates to Opus). Four slices:
- S1: `sc-bootstrap` toolbox commands, preflight PF-01/PF-06, the env-file parser.
- S2: the `toolbox` and `ci` images.
- S3: the `./bootstrap` wrapper.
- S4: pull/build equivalence, the engine matrix and k3d.

Decisions:
- D1–D4 (orchestrator scoping, below).
- CD-64: `make toolbox` runs on the host.
- CD-65: cmctl gets its own baseline entry.
- CD-66: plan 02 records the S2 interface details.
- CD-67: operator inputs go only to the toolbox image, and `SC_TOOLBOX_REQUIRE_PULL` accepts only a pulled image.

**Status: all four slices approved; every GATE-F03 criterion has CI evidence below; PR CI pending.** SHAs are those after the final rebase onto 665a3a9; e2e runs ran on the pre-rebase SHAs. The rebased tree equals the reviewed tree plus `main`'s CD-63 commit. The pre-rebase heads are 988973b (S1), 4b45983 (S2), 7c6197b (S3) and 6d9ec16 (S4).

Orchestrator scoping (brief, D1–D4):
- **D1:** `sc-bootstrap preflight` runs only PF-01 and PF-06; plan 50's framework and the other checks come with F08.
- **D2:** the exec scopes are the env file, SSH and `s3`. The `kube` tunnel and `unlock` scopes need F08/F10 inputs and are left out.
- **D3:** a local OCI registry stands in for GHCR in 02-AC-4. Re-run it at the first real release (plan 23).
- **D4:** the 02-AC-5 SELinux leg runs in a Fedora Cloud VM under QEMU/KVM on the hosted runner.

#### S1 — `sc-bootstrap` toolbox fetch/selfcheck/init, preflight PF-01/PF-06, env s3 (02-AC-3 fetch side, 02-AC-10, 02-AC-11 PF-06 side)

| Commit | Role |
|---|---|
| d5b64f7 → c9c37db | RED (22 fail) → GREEN attempt 1 (`src/cmd/sc-bootstrap`, `src/internal/toolbox`) |
| 08217dd → 5f64a64 | review 1 request_changes. Blocker: `install.extract` lacked `{version}`/`{arch}` expansion. Non-blockers: PF-06 key handling, version-mismatch codes, env grammar, a sleeper that evaded forbidigo. RED (13 fail) → attempt 2 |
| c09709e → 690b7df | review 2 request_changes. Blocker: the mounted key was used only when the test-only `SC_INPUT_DIR` was set. RED (6 fail) → attempt 3 (`ProductionEnv`, default `~/.ssh/id_ed25519`, agent socket dialled, fixed 5 s/15 s waits) |
| — | review 3 **approve** at 690b7df. The reviewer also ran the binary in the ci image on the production path |

#### S2 — toolbox and ci images (02-AC-1, 02-AC-2, 02-AC-3 build side, 02-AC-8, 02-AC-9)

| Commit | Role |
|---|---|
| b0bec4c | RED (69 of 81 static checks fail; tier-E `toolbox-image.sh`) |
| 6a10d77 | docs: CD-64 |
| 96936fd, 122dc27, 401c3de, cfd94fa | GREEN: full lock (every §9.4 tool, sha256 per arch verified against vendor checksum files, AWS CLI by GPG signature), hash-locked ansible closure, the five §14.1 stages, `./bootstrap build`, `--image toolbox`, host-run `make toolbox`, gcc for `-race`, gitleaks image config |
| e35b52e, 0c32321 | the implementer found plan 02's `(verify)` "cmctl version matching cert-manager" false: cmctl is versioned separately (v2.x). CD-65 pins cmctl v2.6.1, the release built against cert-manager v1.21.2; chore corrects the test mapping and the selfcheck row key (`.tool`) |
| c9c8242, 78dbf15 | ci: e2e `toolbox` job runs `toolbox-image.sh`, frees disk first, is selected by every image input |
| 589eb70 | docs: CD-66 (review non-blockers) |
| — | review **approve** at c9c8242 (supply chain re-verified independently) |

#### S3 — wrapper (02-AC-5 Docker/Podman legs, 02-AC-6, 02-AC-7, 02-AC-10, 02-AC-11, 02-AC-13)

| Commit | Role |
|---|---|
| a95a7f9, 50f8885 | RED Go (4 fail) and wrapper (176 fail); tier-E `wrapper-real.sh` |
| 7ffd8e1 | chore: removes the two F01 assertions that `exec` without `--image` exits 2 ("until F03", plan 02 §14.9). **This modifies base tests; it needs explicit human sign-off.** Its `TDD-Superseded` lines name base tests, so they are inert and are not repeated on the head |
| 873f4e9, 55067b0 | GREEN attempt 1: env file without evaluation, §14.3 mounts, default toolbox image, `image.lock` branch (b) with retries/fallback/REQUIRE_PULL, OCI archive, `shell`, entrypoint dispatch |
| c367b33 | chore: test correction. An empty digest may be written `""`, because the protected ToolboxImageLock schema types it as string |
| 0972efc | ci: e2e runs `wrapper-real.sh` and the literal default-image commands |
| 3f74189, c712ef2, 97defd7 → c76549a | review 1 request_changes. **Blocker:** shellrc put env-file `CLUSTER_NAME` into `PS1`, so bash prompt expansion executed `$(…)`. CD-67, RED (5 Go + 22 wrapper fail) → attempt 2 (sanitised prompt, sh/Go parser parity, CD-67) |
| — | review 2 **approve** at c76549a (11 hostile prompt values probed in a real `./bootstrap shell`, none executed) |

#### S4 — pull/build equivalence, engine matrix, k3d (02-AC-4, 02-AC-5 matrix, 02-AC-12)

| Commit | Role |
|---|---|
| 27c9211, 5c5930d, 9276fc9 | RED Go (N3), lint workflow shape, wrapper (13 + 4 fail); tier-E `pull-equivalence.sh`, `ac5-probe.sh`, `test/e2e/selinux/run`, `test/e2e/k3d/run` |
| bb285e8, a66e7d9 | ci: jobs `toolbox-podman`, `toolbox-selinux`, `toolbox-pull-equivalence`, `e2e-k3d`, awaited by `e2e-gate`; plan 52 §8.4 table |
| 7f9d2d7 | GREEN attempt 1: `image.lock` accepts registry `host:port`, `LC_ALL=C` env reading, ci env-file warning, shellrc `CLUSTER_NAME` grammar |
| 2f47b67, 485b8d5, e6a99ed → 5dc3c48 | review 1 request_changes. Blocker: `stub-shellrc.sh` depended on cwd length. Chore test corrections: cwd-independent, built sha via `--toolbox-build`, SELinux snapshot before the first run, k3d-tools pinned by digest. RED (locale) → attempt 2 (`LC_ALL=C` filters) |
| — | review 2 **approve** at 5dc3c48 |

#### F03 — gate evidence

CI runs on GitHub-hosted ubuntu-24.04 (`e2e.yaml`, `workflow_dispatch` on the branch):
- [39095330782](https://github.com/tuckermclean/strange-company/actions/runs/39095330782) on 6d9ec16 (= 5dc3c48): every job succeeded.
- [39092416709](https://github.com/tuckermclean/strange-company/actions/runs/39092416709) on 9c30b7e and [39087906768](https://github.com/tuckermclean/strange-company/actions/runs/39087906768) on 7c6197b: every job succeeded.

| Criterion | Test(s) / evidence | Status |
|---|---|---|
| 02-AC-1 | `toolbox-static.sh` (stages, `make toolbox` shape, final `selfcheck --embedded-only`), `toolbox-image.sh` (`make toolbox` `--no-cache` both targets; the literal `selfcheck --json \| jq -e 'all(.tools[]; .ok)'`); toolbox 17 rows, ci 28 rows ok | pass (Docker CI 26/0; rootless Podman) |
| 02-AC-2 | `toolbox-static.py` inventory; `toolbox-image.sh` literal `command -v` loop plus ci tools, `helm unittest`, `$KUBEBUILDER_ASSETS` | pass |
| 02-AC-3 | `TestFetch_*` (mismatch → `TOOLBOX_CHECKSUM_MISMATCH:<tool>`, no install, no retry); `toolbox-image.sh` fixture lock (`TOOLBOX_CHECKSUM_MISMATCH:jq`, no tag); `stub-s3-*` build-failure path | pass |
| 02-AC-4 | `pull-equivalence.sh`: an independent `--no-cache` build pushed to a local `registry:3.0.0` (pinned by digest), pulled with `SC_TOOLBOX_REQUIRE_PULL=1` (`SC_TOOLBOX_SOURCE=pulled`); selfcheck rows and `sha256sum /opt/sc/bin/sc-bootstrap` identical to a `--toolbox-build` image | pass (D3 stand-in; re-run at first release) |
| 02-AC-5 | `wrapper-real.sh` on Docker rootful (job `toolbox`); `wrapper-real.sh` + `ac5-probe.sh` on rootless Podman, Ubuntu (`toolbox-podman`); `ac5-probe.sh` in a Fedora 44 VM, `getenforce=Enforcing`, rootless Podman, `ls -Z` of `$REPO` and `~/.ssh` unchanged, no `container_file_t` (`toolbox-selinux`, 10/0) | pass |
| 02-AC-6 | `wrapper-real.sh` (canary env incl. the s3 scope: none in inspect, engine argument log or `env`; `AWS_CONFIG_FILE` set), `stub-s3-wrapper.sh` | pass |
| 02-AC-7 | `wrapper-real.sh`, `stub-s3-wrapper.sh`, `stub-s3-entrypoint.sh` | pass |
| 02-AC-8 | `toolbox-image.sh`: toolbox 1,603,785,562 B (1.49 GiB ≤ 1.6), ci 2,711,337,121 B (2.53 GiB ≤ 3.5) on Docker | pass |
| 02-AC-9 | `toolbox-image.sh`: `gitleaks dir` over the exported toolbox filesystem with the explicit `--config toolbox/gitleaks-image.toml` (allowlists limited to vendored AWS CLI/botocore/community.general files), no `/.gitleaks.toml` in the image; image `Env` clean | pass |
| 02-AC-10 | `TestSelfcheck_Stale*`, `TestPreflight_StaleToolboxExits3`; `wrapper-real.sh` (image built from a different lock → `./bootstrap preflight` exit 3 `PF-01 TOOLBOX_STALE`) | pass |
| 02-AC-11 | `TestPreflight_EnvFileValueIsNotEvaluated`, `TestPF06_*`, `stub-s3-wrapper.sh`, `stub-shellrc*.sh`, `wrapper-real.sh` (literal path used, PF-06 fails, `/tmp/pwned` absent on the host) | pass |
| 02-AC-12 | `test/e2e/k3d/run`: k3s v1.36.5+k3s1 by digest, namespace PSA `restricted` enforced (privileged control rejected), Pod `runAsNonRoot`, `readOnlyRootFilesystem`, drop ALL, `RuntimeDefault`, emptyDir `/tmp` and `/run/sc`, `args: [sc-bootstrap, version]` → Succeeded, exit 0 (`e2e-k3d`, 15/0) | pass |
| 02-AC-13 | `wrapper-real.sh`, `stub-s3-wrapper.sh` | pass |
| F01 02-AC-14..17 (regression) | `real-engine.sh` 22/0 on Docker; `stub-exec-contract.sh`, `stub-image-reuse.sh`, `stub-unpinned-from.sh` | pass |
| TDD discipline | local `make tdd-discipline-check` on b5dcdcb (checker and policy from base 665a3a9): `pass`, 12 RED commits, 342 RED failures, `build_ok`, 14 warnings (`TDD_RED_TEST_MODIFIED`, the post-RED test corrections listed below), 0 superseded | pass; PR run pending |

Deviations and disclosures:
- 7ffd8e1 removes two base F01 assertions (plan 02 §14.9 limited that behaviour to "until F03"). It needs human sign-off.
- The following edit this PR's own RED tests after RED; check names are unchanged and no assertion is loosened. The checker warns `TDD_RED_TEST_MODIFIED` for them.
  - c367b33 and 0c32321;
  - 485b8d5 (cwd-independent shellrc test, built sha via `--toolbox-build`, SELinux snapshot before the first run, k3d-tools pin);
  - wrapper-real.sh fixture additions in 97defd7 and 9276fc9.
- Interface details not in the plans: the S1 PF-06/PF-01 failure codes (`SSH_KEY_*`, `SSH_PUBKEY_MISMATCH`, `SSH_AGENT_UNAVAILABLE`, `TOOLBOX_TOOL_MISMATCH`) and the wrapper codes `WRAPPER_BLACKSTART_UNSAFE`, `WRAPPER_LOCK_INVALID`. `toolbox/image.lock` names `ghcr.io/tuckermclean/…`.
- The `toolbox-selinux` job installs qemu with unpinned apt, and `toolbox-podman` relaxes the AppArmor userns sysctl. Both run on ephemeral hosted runners.

Follow-ups (not blocking GATE-F03):
- **F08:**
  - plan 50 §7.1 verbs (`configure`, `node`, `upgrade`, `storage`, `forge-init`, …), bare `./bootstrap` = `run`, and the global flags `--non-interactive`, `--log-format`, `-v`;
  - the kube and unlock scopes and `SC_RECOVERY_PASSPHRASE_FILE` (D2);
  - PF-06's agent check should list identities like `ssh-add -l`;
  - `./bootstrap version` should print the toolbox ref, source and selfcheck table (§21).
- **Procedures:** `procedures/bootstrap-preflight-failure.md` §D needs the new PF-06/PF-01 and `WRAPPER_*` codes and the §15 subuid/SELinux hints.
- **Wrapper:** §11 old OCI archives are not pruned. `HTTP_PROXY` credentials appear in run arguments and inspect output (the plan requires the passthrough; document it). A `:` in a key path breaks `-v` (use `--mount type=bind,readonly`).
- **Tests:**
  - the k3d check should also require the `<none>` tag column, to prove `K3D_IMAGE_TOOLS` was honoured;
  - `stub-shellrc-locale.sh`'s shim check assumes `sed`;
  - the `TestMainBinary_UsesProductionEnv` source-text check could become a behavioural one;
  - the `toolbox` image has about 110 MiB of headroom against its 1.6 GiB budget.
- **Release:** re-run 02-AC-4 against GHCR at the first release (D3); the Fedora 44 image moves to `archives.fedoraproject.org` at EOL.

### F04 — policy-lib

Branch `node/F04-policy-lib`, built with `/sc-node F04` from `main` 61d38fa. Library `src/internal/policy` (loader, revision digest, invariants, exact glob algebra, `MergePolicy`), CLI `src/cmd/sc-policy`, `make policy-check` (`mk/policy.mk`, in `VERIFY_TARGETS`) and the committed `policy/verification.yaml`. Three slices:
- S1: loader core (schema by kind, plan 26 §9.2 step 1, digest, last-known-good, `sc-policy check`).
- S2: cross-file invariants (plan 26 §9.2 step 3) and verification-policy invariants (plan 22 §9.5).
- S3: `MergePolicy` (plan 22 §9.4).

**Status: all three slices approved; every GATE-F04 criterion has passing evidence below.**

SHAs are those after one operator-approved rewrite. The first RED's stubs failed `TDD_STUB_INVALID`: `main` was unmarked and had a body, and `NewLoader` returned non-zero. The rewrite amended only that commit; the head tree is byte-identical to the reviewed pre-rewrite head 651633e (tag `f04-pre-rewrite`). CI runs and reviews ran on the pre-rewrite SHAs.

Pre-rewrite → rewritten: 83187d5→f27788c, 49e362b→e94f6bc, 6b27092→0772a43, 20f1921→208c18c, b41bef5→130f707, 4692d86→c3b079e, 2934c6a→1667cae, ab5d5b7→6502520, 93ce467→7609d0e, a8a0ff5→0921d00, 9270b25→f1590e2, d660fc2→c3a2497, 7342130→2e43575, 4d41398→daed229, 5c6daa0→d8b7171, bfe677e→247015c, 7a2f663→ee39d43, e1334f3→c44be44, ad721a3→90d6f70, f7827ba→d0bf8d8, 3f8b785→3fdf43e, bc9c181→a961949, 6e734a3→a73cb23, 5172d7d→6f57506, 09a5e9b→15a3b4b, 059dbcd→c2ba522, 2e2e1ab→076a5a3, 651633e→031b031.

Decisions:
- **D1:** F04 commits only `policy/verification.yaml`. The other Runner kinds ship with their owning nodes (F07/C02, C05, C06, C07). `make policy-check` (CI mode) checks the kinds present; the five-roles rule applies once any `RoleDefinition` exists. Runner mode (`Options.Runner`) also requires every Runner kind (`POLICY_KIND_MISSING`). `testdata/valid/` is a complete Runner-mode tree.
- **D2:** `CIPolicy` and `TestQuarantine` are known kinds, validated by schema only.
- **D3:** the loader covers plan 26 §9.2 steps 1–4 and the in-memory parts of 6–7. Commit resolution, `policy_revisions`, metrics, alerts and step 8 are C01. `MANAGED_REPOSITORY_NOT_IN_FORGE` (22-AC-36) and `ENFORCEMENT_PLANE_NOT_ELEVATED` (22-AC-33) belong to their own gates.
- **D4:** `src/policy/testdata/…` in the criteria means `src/internal/policy/testdata/…` (plan 01 OQ-01-4).
- **D5:** `sc-policy check [--root R] [--dir D]` prints `<CODE>[:<pointer>] <file>: <message>`. `make policy-check POLICY_DIR=<dir>` checks another tree or fixture.
- **CD-68** (operator-approved; three parts):
  - `documentation-only` also denies `gitops/**`, `deploy/**`, `ansible/**`, `tofu/**` and `**/migrations/**`. The shipped `examples[0]` overlapped human classes under exact glob semantics.
  - §9.5 path checks are exact language intersection and inclusion over the checker's glob semantics. A repository definition glob is rejected only when it lies entirely inside the protected set.
  - A newline in a path is an ordinary character for `*`, `?` and `**`.
- **D6 — `maxTier` ceiling:** the platform ceiling for routing `maxTier`/`roleMaxTier` is the highest `ModelRouting` tier with candidates. Plan 31 §14.3 names no platform cap other than "frontier when absent". `roleMaxTier` is checked against that global ceiling, not the role's allowed tiers.
- **D7 — `MergePolicy` signature:** the signature is `MergePolicy(opts Options, platform Platform, repo map[string]any) (Effective, []Issue)`, not plan 22 §7.2's `(platform, repo) (Effective, error)`. It reads the contract schemas through a per-directory cache. F06 may wrap it.
- **D8 (operator):** `TestCheck_ManyPropertyNamesViolationsAreBounded` runs without a wall clock (forbidigo D8). Its time bound is `go test -timeout 20m`. A separate test pins the >32-candidate fallback shape.

#### S1 — loader core (26-AC-2, 26-AC-3, 22-AC-1 committed file, 26-AC-1 schema by kind and step 1)

| Commit | Role |
|---|---|
| f27788c → e94f6bc | RED (165 fail) → GREEN attempt 1. CI [39154759893](https://github.com/tuckermclean/strange-company/actions/runs/39154759893) success |
| 0772a43, 208c18c → 130f707..1667cae | review 1 request_changes. Blockers: schema-error pointers varied between runs (jsonschema v6.0.3 shares the `InstanceLocation` slice); a top-level `__` file overwrote a nested path; non-YAML files were not validated. RED (46 fail) + chore → attempt 2 (leaf walk, schema cache, collisions, every file is a document). CI [39165586740](https://github.com/tuckermclean/strange-company/actions/runs/39165586740) success |
| 6502520 → 7609d0e | review 2 request_changes. Blocker: additionalProperties messages listed keys in map order. RED (6 fail) → attempt 3 (sorted lists; propertyNames locations found by a key-removal experiment). CI [39168461414](https://github.com/tuckermclean/strange-company/actions/runs/39168461414) success |
| — | review 3 **approve** at 7609d0e |

#### S2 — invariants (26-AC-1 §9.2 step 3, 26-AC-5, 22-AC-1 §9.5)

| Commit | Role |
|---|---|
| 0921d00 | RED (64 fail; cross-file, §9.5 and repository-side fixtures; review-3 items) |
| f1590e2, c3a2497, 2e43575 | docs CD-68 (documentation-only denies, exact semantics); chore aligns 39 testdata copies; RED: exact intersection and inclusion (87 fail in total) |
| daed229, d8b7171, 247015c | GREEN attempt 1 (`invariants.go`, `glob.go` product-automaton algebra); docs CD-68 repository side; chore D8. CI [39177055694](https://github.com/tuckermclean/strange-company/actions/runs/39177055694) success |
| ee39d43, c44be44 → (attempt 2: test-spec conflict, no commit) → 90d6f70, d0bf8d8 → 3fdf43e | review 1 request_changes. Blocker: `**/` behaved like `**`, so deny globs over-matched and overlaps were missed (fail-open). Chore (fallback shape) + RED (8 fail, including an exhaustive brute-force cross-check against the reference regex). Attempt 2 found the newline contradiction; operator decided CD-68 newline, test corrected → attempt 3. CI [39189093759](https://github.com/tuckermclean/strange-company/actions/runs/39189093759) success |
| — | review 2 **approve** at 3fdf43e (two 30,000-case brute-force runs against the dotall reference: 0 mismatches) |

#### S3 — `MergePolicy` (22-AC-2, 26-AC-4)

| Commit | Role |
|---|---|
| a961949 → a73cb23 | RED (21 fail) → GREEN attempt 1. CI [39193457747](https://github.com/tuckermclean/strange-company/actions/runs/39193457747) success |
| 6f57506, 15a3b4b → c2ba522 | review 1 request_changes. Blocker: Go-typed lists (`[]string`) bypassed every set row (fail-open). Non-blockers: missing required platform values accepted the repository value; non-RE2 staticChecks patterns were accepted. Chore (schema-valid `frontier`) + RED (16 fail) → attempt 2. CI [39198487843](https://github.com/tuckermclean/strange-company/actions/runs/39198487843) success |
| — | review 2 **approve** at c2ba522; non-blocker N1': a nil or un-marshalable platform document was used as given |
| 076a5a3 → 031b031 | RED (7 fail, 5 cases) → attempt 3 (`normalize` fails closed with `PLATFORM_POLICY_INVALID`). CI [39202440947](https://github.com/tuckermclean/strange-company/actions/runs/39202440947) success |
| — | final review **approve** at 031b031 (pre-STATUS head; reviewed as 651633e), including a whole-node pass; every GATE-F04 criterion passes |

#### F04 — gate evidence

| Criterion | Test(s) / evidence | Status |
|---|---|---|
| 22-AC-1 | `TestCommittedVerificationPolicyEqualsExample_22AC1` (no local overrides), `sc-policy` `TestCheck_CommittedPolicyExitsZero_26AC1`, `make policy-check` exit 0; `TestVerificationInvariantFixtures_22AC1` over `invalid/verification/` (one per §9.5 invariant, including the repository side), `TestExactGlob*_22AC1` (exact overlap, dead entry, brute force), `TestCheckPrintsInvariantCodes_*` (CLI prints each code) | pass |
| 22-AC-2 | `TestMergePolicy_Rows_22AC2` (each §9.4 row: tightening fixture with merged value, loosening fixture with `POLICY_LOOSENING:<pointer>`), `_OrderedEnumsStricterWins`, `_EqualToPlatformIsNotLoosening`, `_UnionKeepsPlatformEntries`, `_DoesNotMutatePlatform`, `_SharedLoosenFixture_22AC2`, `merge_review1_test.go` | pass |
| 26-AC-1 | `TestCheck_SchemaInvalidExamples_26AC1` (41 fixtures, one per policy-kind `x-invalid-examples`), `TestCheck_Step1Rejections_26AC1`, `TestCheck_EveryInvalidFixtureIsRejected_26AC1`, `TestInvariantFixtures_26AC1` (18 step-3 fixtures), `sc-policy` `TestCheck_InvalidFixturesPrintExpectedCode_26AC1`, determinism and pointer tests | pass |
| 26-AC-2 | `TestDigest_ValidTreeEqualsRecorded` (`valid.digest` computed independently), `_RenameOrByteChangeChangesIt`, `_DirectoryOrderDoesNotMatter`, `_ConfigMapKeysMapToPaths` | pass |
| 26-AC-3 | `TestLoader_InvalidRevisionKeepsLastKnownGood_26AC3`, `TestLoader_FirstRevisionInvalid_26AC3` | pass |
| 26-AC-4 | `TestMergePolicy_RejectsLooseningFixture_26AC4`, `TestMergePolicy_SharedLoosenFixture_22AC2` (`repo-policy-loosen.yaml` → `POLICY_LOOSENING:/spec/retry/maxAutonomousAttempts`) | pass |
| 26-AC-5 | `TestFourNamedRunnerFixturesGiveFourDistinctCodes_26AC5`: `ROLE_HARNESS_ROLE_UNSUPPORTED`, `ROLE_PROFILE_MISSING`, `HARNESS_REPO_CONFIG_NOT_IGNORED`, `HARNESS_WORKER_TIMEOUT_NOT_TEST_ONLY` | pass |
| TDD discipline | local `make tdd-discipline-check` on 031b031 (checker and policy from base `origin/main` 61d38fa): `pass`, 9 RED commits, 354 RED failures, `build_ok`, 42 warnings (`TDD_RED_TEST_MODIFIED`, the post-RED corrections listed below), 0 superseded | pass; PR run pending |

Error codes not named by the plans:
- loader: `POLICY_SCHEMA_INVALID`, `POLICY_UNKNOWN_KIND`, `POLICY_DUPLICATE_RESOURCE`, `POLICY_FILE_TOO_LARGE`, `POLICY_YAML_ANCHOR`, `POLICY_NOT_UTF8`, `POLICY_RESERVED_FILENAME`, `POLICY_KIND_MISSING`, `POLICY_YAML_INVALID`, `POLICY_UNREADABLE`;
- invariants: the plan 26 §9.2 step-3 and plan 22 §9.5 codes listed in 0921d00, plus `POLICY_GLOB_TOO_COMPLEX` (fails closed beyond 200,000 automaton states);
- merge: `REPO_POLICY_INVALID`, `PLATFORM_POLICY_INVALID`.

Deviations and disclosures:
- Plan and contract changes (CD-68) touch protected paths: `contracts/verification.schema.yaml` `examples[0]`, `policy/verification.yaml`, plan 22 §9.2, §9.4 and §9.5. Operator-approved; they need human review in the PR.
- The following correct this PR's own RED tests after RED; test names are unchanged and no assertion is loosened beyond the operator decisions. The checker warns `TDD_RED_TEST_MODIFIED`:
  - c3a2497: CD-68 testdata;
  - 247015c: D8;
  - d0bf8d8: CD-68 newline;
  - 6f57506: schema-valid `frontier`.
- `src/internal/contracts/compile.go` exports the shared schema compiler (`newCompiler`) and a cache for `sc-policy`.
- `golang.org/x/text` moved from indirect to direct (BSD-3-Clause, `licenses-check` pass).

Follow-ups (not blocking GATE-F04):
- **Glob matcher:** `globRegexp` in `src/internal/lint/tdd/gitutil.go` lets `**` miss a newline (Go's `.`), so `.github/x\ny` escapes `.github/**` in the tdd-discipline checker (CD-68). This needs a separate fix with tests.
- **Contract:** `contracts/verification.schema.yaml` `x-invalid-examples[3]` still carries the pre-CD-68 `documentation-only` denies. It is invalid for another reason (catch-all `auto`); align it, together with `testdata/invalid/schema-verification-3`, in a human PR.
- **Loader:**
  - the flat-vs-nested `__` heuristic accepts a real `x__y.yaml` in a tree without subdirectories; a ConfigMap's `..data` entry would be a sturdier signal;
  - the propertyNames key-removal experiment could misattribute if a schema made one object's propertyNames depend on a sibling key (not reachable with today's schemas).
- **`MergePolicy` hardening (before F06 wires it; final-review non-blockers):**
  - validate `Platform.Verification` against `verification.schema.yaml` and fail closed with `PLATFORM_POLICY_INVALID`. Today `Verification: {}` or a platform without `spec.protectedPaths` judges definitions against an empty protected set. Loader-built platforms are schema-checked, so only a Go caller passing an unchecked document reaches this;
  - iterate `Platform.Roles` in sorted order so the `PLATFORM_POLICY_INVALID` message is byte-deterministic when several roles are bad.
- **Later gates:**
  - the per-kind invariants of plans 25/28/31/33 (ladder anchoring, image-digest uniqueness, budget invariants, profile quota against the chart);
  - `make policy-check` validating this repository's `.strange-company/` through `sc-verify lint-policy` (F06/C04).

### F05 — state-machine-lib

Branch `node/F05-state-machine-lib`, built with `/sc-node F05` from `main` b69e280. Library `src/internal/statemachine` (`Load` with the plan 29 §8.2 invariants and per-mode reachability report, `Fire`, `Project`, `NextAllowedRoles`, guard and effect registries, the retry-accounting layer `AccountingFacts`/`Advance`/`Apply`/`Replay`, `Fingerprint`, the task-record serializer `Record`), the trajectory loader `src/internal/statemachine/trajectory`, CLI `src/cmd/sc-statemachine check`, `mk/statemachine.mk` (`state-machine-check` in `VERIFY_TARGETS`, `state-machine-coverage`, `test-tdd-library`) and the trajectory files `test/e2e/tdd/*.yaml` with their format in `test/e2e/tdd/README.md`. Four slices:
- S1: loader (29-AC-1, 29-AC-2).
- S2: evaluation (29-AC-3, 29-AC-5, 29-AC-6, 29-AC-9).
- S3: retries and properties (29-AC-7, 29-AC-8, 29-AC-4).
- S4: trajectories (21-AC-20, 29-AC-11, 29-AC-10).

**Status: all four slices approved; a final whole-node review approved 79df7c2 (= ccf86e6 after the rewrite); every GATE-F05 criterion has passing evidence below.**

SHAs are those after one history rewrite (D19). Four RED commits had added `const` declarations (error codes) to five new declared-stub files, which plan 52 §8.3a does not allow (`TDD_STUB_INVALID`; the orchestrator's brief had wrongly said consts were fine). The rewrite moved those const blocks into companion `*_codes_test.go` files in the RED commits up to the next GREEN, which restores its original tree. Messages, authors and dates are unchanged, and the head tree is byte-identical to the reviewed head 79df7c2 (tag `f05-pre-rewrite`). CI runs and reviews ran on the pre-rewrite SHAs.

Pre-rewrite → rewritten: 7bcbaad→8ab80f5, d9e2a3a→cac67cd, 01fbb50→b43c7db, a602ffc→7b2cd56, 16a51e8→00e5dc7, 8330afb→ed4b289, 6218437→7648025, 4fbc01b→8b0939d, 1127ff5→f68a820, ebe821d→3a93c60, ea47ed9→02c292d, 70ab6e3→08ceda1, ea073c0→f6d30e4, 1de2b0a→0cb9d5c, 899b4b5→274b7b5, b178d68→3ddaba6, d915d5a→26cfc9a, 580dd35→46e7c3c, ac0866a→f849aa2, 9892daf→347335a, 9cfc621→ef11fa7, 79df7c2→ccf86e6.

Decisions (orchestrator, under the operator's standing delegation unless marked operator):
- **D1:** the package is `src/internal/statemachine` (plan 01 §6.1 layout and every elevated-path list), not plan 29's `src/runner/statemachine`. 29-AC-3's command reads `go test ./src/internal/statemachine/... -run TestTransitionTable`; the invalid fixtures are in `src/internal/statemachine/testdata/invalid/`.
- **D2:** every RED uses the red target `test GO_TEST_PKGS=./src/internal/statemachine/... ./src/cmd/sc-statemachine/...`. The new Make targets are not in `policy/ci.yaml` `redTargets`.
- **D3:** `TestRandomWalks_29AC4` takes `-seed N` or `-seed A..B`; the default is `1..10000` per mode, so plain `make test` runs the full 29-AC-4 range. Use the bare package path: `go test ./src/internal/statemachine -run TestRandomWalks -seed 1..10000` (the `/...` form also builds the trajectory package, which has no `-seed` flag).
- **D4:** the trajectory files (plan 21 §22.3) are written in F05; cluster-only assertions are optional `live:` fields ignored by the library runner. Excluded from the library set, with reasons in the README: `tdd-second-pr-other-branch` (a Forgejo status, no Runner event) and `tdd-foreign-pipeline-ignored` (no event).
- **D5:** plan 29 §8.2's "events listed in the state's mapping table (§7.2)" is enforced as membership of the table's `spec.events` catalogue (`SM_EVENT_UNDECLARED`); §7.2 maps input sources, not states, to events.
- **D6–D8 (loader, reviews):** verification states need candidates for their phase event in every mode, completeness per actor class, exactly one outcome or failure-class guard per phase or `execution_failed` transition and none elsewhere, valid guard arguments, stall transitions per mode with actor `runner`, `$none` only to `initialState` in every mode, `$resume` only from held states, no mode leaks, phase events only from their phase state, `execution_failed` only from `activeExecution: required` states. All hold on `contracts/state-machine.yaml`.
- **D9–D13 (evaluation and accounting, reviews):** actor-class filtering before guards; `ATTEMPTS_EXHAUSTED` with `details.allowed=[identical_replay]`; each fact asked once per `Fire`; dispatch roles checked; identical replay fails closed on an empty or mismatched role; `Advance` re-derives target, effects, deltas, actor, the guards it can answer and lower-id shadowing, so `Replay` rejects forged histories; an unknown failure code is `""`.
- **D12 (operator):** one row of `TestFire_AppliedDeltas_29AC7` was corrected to plan 29 §16.1 / CD-09: a T102 worker merit failure in IMPLEMENTING counts `by_class.merit` only.
- **D14, D17 (task-state guards):** the library derives `G_NO_ACTIVE_EXECUTION`, `G_NO_IMPLEMENTER_DISPATCHED_IN_GENERATION`, `G_NO_PRODUCER_DISPATCHED_IN_GENERATION`, `G_NO_PRODUCTION_IN_GENERATION`, `G_TEST_CHANGE_APPROVED`, `G_CONFLICT_REJECTED` and `G_STRENGTHEN_AUTHORIZED` from the task. It keeps a conflict record with TDD-20 semantics (first adjudication decisive, later ones only tighten, disagreement needs a human, a human approval is authoritative). An adjudication needs a running reviewer auxiliary execution, an open conflict and a known verdict. `adjudication_recorded` ends the adjudicator's execution.
- **D15 (operator):** RED 1560057 was amended to d915d5a (now 26cfc9a) before it was pushed (a blank line before each field stub made `TDD_STUB_INVALID`).
- **D16:** `TestRandomWalks_29AC4` requires attempt refusals in test-first only. D14 makes verification-first refusals unreachable in unsteered walks; `TestRandomWalksSteered_29AC4` asserts them in both modes. The final review confirmed the change hides no defect.
- **D18:** `d17Forged` passes the valued fact `G_JUDGE_SOURCE:artifact` its T112 setup needs (setup-only). `Apply` returns `Rejected{GATE_NOT_SATISFIED}` on failed adjudication preconditions; `Advance` returns `SM_ADVANCE_NOT_APPLIED` for a forged step.
- **D19:** the history rewrite above.

#### S1 — loader (29-AC-1, 29-AC-2)

| Commit | Role |
|---|---|
| 8ab80f5, cac67cd → b43c7db | RED (55 fail) + RED fixup (three fixtures were schema-invalid: implementer TEST_SPEC_CONFLICT) → GREEN attempt 1. CI [39272795981](https://github.com/tuckermclean/strange-company/actions/runs/39272795981) success |
| 7b2cd56 → 00e5dc7 | review 1 request_changes. Blockers: a verification state with no classification transitions, the stall check ignoring modes, `$none` skipping every rule. RED (D6, 28 fail) → attempt 2. CI [39282317395](https://github.com/tuckermclean/strange-company/actions/runs/39282317395) success |
| ed4b289 → 7648025 | review 2 request_changes. Blocker: an unguarded classification transition fired first for every outcome. RED (D7, 7 fail) → attempt 3. CI [39290652066](https://github.com/tuckermclean/strange-company/actions/runs/39290652066) success |
| — | review 3 **approve** at 6218437 (= 7648025) |

#### S2 — evaluation (29-AC-3, 29-AC-5, 29-AC-6, 29-AC-9)

| Commit | Role |
|---|---|
| 8b0939d → f68a820 | RED (22 top-level, 642 leaf failures; D8 loader fixtures) → GREEN attempt 1. CI [39302020569](https://github.com/tuckermclean/strange-company/actions/runs/39302020569) success |
| — | review **approve** at 1127ff5 (= f68a820): random-oracle sweep of 175,500 `Fire` calls, 0 mismatches |

#### S3 — retries and properties (29-AC-7, 29-AC-8, 29-AC-4)

| Commit | Role |
|---|---|
| 3a93c60 → 02c292d | RED (21 fail; D9) → attempt 1. CI [39314839403](https://github.com/tuckermclean/strange-company/actions/runs/39314839403) success |
| 08ceda1 → f6d30e4 | review 1 request_changes. Blockers: identical replay bypassed with an empty role; `Advance` trusted forged decisions; random-walk property 4 vacuous. RED (D11, 7 fail) → attempt 2. CI [39346235947](https://github.com/tuckermclean/strange-company/actions/runs/39346235947) success |
| 0cb9d5c → 274b7b5 | review 2 request_changes. Blockers: `Advance` actor check, guard re-evaluation and shadowing, unknown-code refund; test-spec conflict on one row (D12, operator). RED (7 fail) → attempt 3. CI [39353780966](https://github.com/tuckermclean/strange-company/actions/runs/39353780966) success |
| — | review 3 **approve** at 899b4b5 (= 274b7b5): 16 of 16 mutations caught |

#### S4 — trajectories (21-AC-20, 29-AC-11, 29-AC-10)

| Commit | Role |
|---|---|
| 3ddaba6, 26cfc9a, 46e7c3c → f849aa2 | RED (36 trajectory files, runner, `Record`, D13) + RED (D14: library-derived task-state guards, fake facts removed) + RED (D16) → attempt 1 (stopped once on the D16 test-spec conflict). CI [39366505939](https://github.com/tuckermclean/strange-company/actions/runs/39366505939) success |
| — | review 1 **approve** at ac0866a (= f849aa2) with non-blockers N1–N6 (fail-open library gaps, fabricated record values, trajectory fidelity) → D17 |
| 347335a, ef11fa7 → ccf86e6 | RED (D17, 16 fail) + RED (D18 setup fix) → attempt 2 (stopped once on the D18 setup bug). CI [39377342840](https://github.com/tuckermclean/strange-company/actions/runs/39377342840) success |
| — | final review **approve** at 79df7c2 (= ccf86e6), including a whole-node pass: `make test` 1,894 results, 0 failures |

#### F05 — gate evidence

| Criterion | Test(s) / evidence | Status |
|---|---|---|
| 21-AC-20 | `TestTrajectories_Suite_21AC20`, `TestTrajectories_Replay_21AC20` (37 files through `Apply`, records validated after every step), `trajectory` `TestParse_*`/`TestLoad_*_21AC20`, `TestMakeTarget_TestTddLibrary_21AC20`; `make test-tdd-library` exit 0 (37 trajectories), exit 2 on an empty or malformed directory | pass |
| 29-AC-1 | `TestLoad_AcceptsContract_29AC1`, `TestReport_NoUnreachableNoNonTerminating_29AC1`, `TestRevision_29AC1`, `TestCheck_*_29AC1`, `sc-statemachine` `main_test.go`; `make state-machine-check`: "revision 4: schema valid, invariants hold", test-first 18/18 reachable, verification-first 19/19, 0 unreachable, 0 non-terminating | pass |
| 29-AC-2 | `TestInvalidFixtures_29AC2` (88 fixtures, one or more per invariant incl. the four named), `TestInvalidFixtures_D6/D7/D8Coverage_29AC2`, `TestLoad_FailsClosed*_29AC2` | pass |
| 29-AC-3 | `TestTransitionTable` (a fixture per transition id and per guard id), `TestTransitionCoverage_29AC3`; `make state-machine-coverage`: "transitions 94/94 guards 66/66"; path per D1 | pass |
| 29-AC-4 | `TestRandomWalks_29AC4` (seeds 1..10000 per mode, the five §22 properties), `TestRandomWalksSteered_29AC4`, `TestSeedFlag_29AC4`; command per D3 | pass |
| 29-AC-5 | `TestFire_DispatchImplementerGate_29AC5` (every state × mode × actor class), `TestFire_DispatchImplementerPositiveAndGates_29AC5`, `TestFire_DispatchRejectionCodes_29AC5` | pass |
| 29-AC-6 | `TestFire_HeldStatesRefuseNonHuman_29AC6`, `TestFire_NonHumanNeverLeavesHeld_29AC6` | pass |
| 29-AC-7 | `TestRetryAccounting_GreenFailTimesFive_29AC7`, `_InfrastructureRefunds_29AC7`, `_ConsecutiveTimeouts_29AC7`, `_EmergencyStop_29AC7` (plus `_ConsecutiveInfrastructureCap`, `_TesterAttempts`), `TestFire_AppliedDeltas_29AC7` | pass |
| 29-AC-8 | `TestIdenticalReplay_SameFingerprintRejected_29AC8` (and a different tier accepted), `_EqualSignaturesSameSettings_29AC8`, `_OnlyMeritOrPolicyFailuresCount`, `_InfrastructureFailureIsNoReplay`, `TestFingerprint_CanonicalJSON_29AC8` | pass |
| 29-AC-9 | `TestProject_AllStates_29AC9` (24 states, every mode and variant, never `Backlog`), `TestProject_FollowsTableAndFailsClosed_29AC9` | pass |
| 29-AC-10 | `TestTrajectories_HappyPathRecords_29AC10`, `TestRecord_*_29AC10`, the record validation after every replayed step, `TestTaskSchema_InvalidExamplesFail_29AC10` (every `x-invalid-examples` entry fails) | pass |
| 29-AC-11 | `TestTrajectories_BypassAssertions_29AC11` (the five cases), `TestTrajectories_NoTaskStateFacts_29AC11_D14`, `TestTrajectories_D17_29AC11`, the D14/D17 accounting and conflict-record tests | pass |
| TDD discipline | local `make tdd-discipline-check` on ccf86e6 (checker from base `origin/main` b69e280): `pass`, 13 RED commits, 839 RED failures, `build_ok`, 30 warnings (`TDD_RED_TEST_MODIFIED`, the post-RED changes listed below), 0 superseded | pass; PR run pending |

Deviations and disclosures:
- Earlier-slice tests changed after their RED (the checker warns `TDD_RED_TEST_MODIFIED`): the D12 row (operator), the D16 condition, the D18 helper, cac67cd (three fixtures schema-valid), the D14/D17 edits to this PR's own trajectory files and runner, and D19's companion `*_codes_test.go` files, which the following GREEN deletes. Test names are unchanged and no assertion is loosened except D16.
- `mk/statemachine.mk` and the `VERIFY_TARGETS` line in `mk/verify.mk` are protected paths and need human review.
- `test/e2e/tdd/**` is an elevated path of this repository.

Follow-ups (not blocking GATE-F05):
- **Record replay of external facts:** a history records neither the failure class nor the external counters (verification outcome, approvals, review completeness, retrigger/conflict/blocked-result budgets), so `Replay` cannot verify them; recording them in the step would close this (S3 review 3).
- **Library hardening (final review):**
  - an execution built without `Dispatch` counts as a non-producer (no library path creates one);
  - `G_NO_ACTIVE_EXECUTION` ignores the "no verification pipeline pending" part of its contract text, which the Runner must check;
  - a rejected adjudication carries no reason;
  - `Record` does not check generation rows for mutual consistency;
  - the trajectory loader silently skips `*.YML`, `*.Yaml` and `*~` files.
- **Plan 29 / contract:** an auxiliary execution can outlive T003 or T067 into states where T091 is not accepted. This fails closed, but a lost `auxiliary_result_recorded` then blocks dispatch until a human cancels. The 20 fallback-covered guards have no sibling-fails fixture. Reject an empty `Event.Role` on dispatch events once the Runner always sets it. Not specified in F05: `blocked_results` and `conflicts_raised` increments, the plan 31 escalation ladder, §16.4 failure-signature normalisation, and `returnTargets` per mode.
- **Plans:** plan 29 §22 and 29-AC-3 should name `src/internal/statemachine`; 29-AC-4 should name the bare-package command (D3).

### F06 — verifier-lib

Branch `node/F06-verifier-lib`, built with `/sc-node F06` from `main` b1098ba (owning plan `plans/22-verification-policy.md`). Library `src/internal/verify`:
- adapters and normalisation (go-test-json, junit-xml, pytest, jest, vitest and nextest JUnit);
- the declared-stub check (Go AST; canonical-layout allowlist for Python, TS/JS and Rust);
- `ClassifyRed` R0–R11 and `ClassifyRedGen` (R1b, R11b, R12–R14);
- `BuildManifest`;
- Git diff derivation (`DeriveDiff`, `AddedLines`, `CrossCheckCompare`, `RevList`, `IsAncestor`, `LsTree`);
- `CheckIngestion` and `DeriveDiscoveryProtection` with fail-closed go.mod, TOML, INI and JSON recognisers;
- `ValidateExemption` and `CheckScope`;
- `ClassifyGreen` G0–G9 with `DeriveIntegrity` and `DeriveIsolationFiles`;
- `ClassifyJudge` and `ClassifyJudging`;
- the observer behind `src/cmd/sc-verify` (`run`, `emit`, `classify`).

In `src/internal/policy` it adds `Effective.Name`, `policy.MatchGlob` and the `ENFORCEMENT_PLANE_NOT_ELEVATED` invariant, enforced in `MergePolicy` and `Check`.

Slices:
- S1: red-core (22-AC-4..7).
- S2: red-generations (22-AC-26, 22-AC-30, 22-AC-9, 22-AC-24 RED part, 22-AC-32 `manifest_too_large`).
- S3: ingestion (22-AC-8, 22-AC-22, 22-AC-31, 22-AC-32 discovery).
- S4: policy-scope (22-AC-10, 22-AC-11, 22-AC-33).
- S5: green (22-AC-23, 22-AC-25, 22-AC-27, 22-AC-24 GREEN part).
- S6: judges and completeness (22-AC-3). S6 was re-sliced after its budget ran out: S6a holds judges, completeness and determinism; S6b holds the research-report recogniser.
- S7: observer (22-AC-29, 22-AC-24 child env and replay export, 22-AC-32 `toolingFrom`).

**Status: every slice approved by its last review; every GATE-F06 criterion has passing evidence below.**

Decisions (orchestrator, under the operator's standing delegation unless marked operator). The full text is in the node brief `out/sc-node/F06/brief.md`.
- **D1–D7 (brief):**
  - Paths are `src/internal/verify` and `src/cmd/sc-verify`; 22-AC-3 reads `go test ./src/internal/verify/...`.
  - The bare red target is `test GO_TEST_PKGS=…`.
  - The enforcement plane reads plan 22 §9.3's `jobbuilder` as `src/internal/jobs/**`. The invariant lives in the F04 loader, applies to repository `strange-company` only, and uses glob inclusion.
  - For strange-company, `test/e2e/**` is protected and is not a test path (D4).
  - pytest, jest, vitest and nextest fixtures are hand-recorded (D5; re-recording is 22-AC-12 at C04).
  - Classification is pure; Git derivation is tested on real scratch repositories, and the observer on real fixture modules (D6).
- **D8–D13 (S1):**
  - interface choices (D8);
  - non-function stub declarations and the protected-path check in R1 (D9);
  - a fail-closed stub check and R10/R11 reason order (D10);
  - the canonical-layout allowlist for line languages, with foreign line terminators an error (D11, D12);
  - no panic on open stubs; existing Go TypeSpecs gain no lines except a marked, untagged named field; fresh Rust/TS type names; Python annotation spaces (D13).
- **D14–D16 (S2):**
  - S2 RED audit answers; 22-AC-9's "digest equal" is read against the placeholder digest in `examples[0]` (D14).
  - One structural primitive for R1b/R12/R13, keyed by `(file, func)` (D15).
  - cgo and import doc comments; fail-closed generation facts; manifest limits that agree with the real schema validator (D16).
- **D17–D19 (S3):**
  - S3 RED audit answers (D17).
  - Semantic-key and discovery parsers are fail-closed recognisers of a canonical subset; Git ignores grafts (D18).
  - Odd line terminators and NUL are errors; gitlinks are skipped in semantic loops; the TOML and go.mod subsets are tightened (D19).
- **D20 (operator):** `ENFORCEMENT_PLANE_NOT_ELEVATED` is also reported by `Check` (`make policy-check`). The six merged F04 fixtures named `strange-company` received the real `merge.elevatedPaths` block. No other key and no `.expect` file changed.
- **D21 (S4):** the `refactor` (characterization) class denies the repository's test, fixture and test-infra paths. `CheckScope` reports `GITLINK_DENIED` for every role and refuses inconsistent decisions. Auto for a repository-human class keeps the stale-auto escalation.
- **D22 (S5):** a manifest file is modified on a blob or mode change, or when CheckIngestion flags it. Truncated reports whose failing counts exceed the reported failures fail G5. Count and exit-code inconsistencies, an unbound baseline and warning downgrades of §14.6 codes are `verification_infra_failed`.
- **D23–D25 (S6):**
  - The JudgeArtifact check agrees with the real schema, backed by a differential test; the report recogniser fails closed; G2 facts apply in judging; the characterization base observation is bound; unusable inputs are refused (D23).
  - Anchors and aliases are refused; container-nested fences and HTML fail closed; inline-hidden ids do not count; the base SHA is bound (D24).
  - AC ids are matched only before the first `<`, `![`, `](` or `][` (D25).
- **Re-slice (operator instruction "if that attempt fails, refactor your slicing for this node and try again").** S6's four attempts were spent; S6a was closed as approved and S6b opened with a fresh budget.
- **D26–D27 (S6b):** outside an open top-level fence, any indented fence or `<` opener fails a research report closed; `<` rules do not apply inside a top-level fence. A recorded CommonMark oracle corpus guards this (`testdata/report_oracle/`, markdown-it-py 3.0.0 in commonmark mode, seed 27, 3252 entries).
- **D28 (S7):**
  - The replay and base-tooling trees are snapshotted from Git blobs before any workspace code runs and extracted into private directories.
  - `run` is non-dumpable.
  - The repo-validation schema is mirrored, with a differential test.
  - A NUL file carrying a stub marker refuses the export.
  - `emit` follows plan 20 §8.4.
  - Non-linux builds compile.
- **Operator:**
  - one extra S1 attempt beyond CD-63's four (attempt 5, Opus);
  - re-slicing S6;
  - D20.

#### S1 — red-core (22-AC-4, 22-AC-5, 22-AC-6, 22-AC-7)

| Commit | Role |
|---|---|
| 58e0ea5, 2f506e2 → 3e30d95 | RED (97 failures) + RED fix (escaped jest fixture, gofmt) → attempt 1. CI [39439498894](https://github.com/tuckermclean/strange-company/actions/runs/39439498894) success |
| 8c45999, 2e9108b → 7d34d2e | review 1 request_changes (stubs ignore non-function declarations; R1 had no protected-path check) → RED (D9) + data fix → attempt 2. CI [39452969621](https://github.com/tuckermclean/strange-company/actions/runs/39452969621) success |
| bfe9691 → 4a2916b, 3e9f19e | review 2 request_changes (stub check must fail closed; R10/R11 order) → RED (D10) → attempt 3. CI [39463742752](https://github.com/tuckermclean/strange-company/actions/runs/39463742752) success |
| 179c74f → bb8b7fb | review 3 request_changes (still fails open in six ways) → RED (D11/D12) → attempt 4 (Opus, CD-63). CI [39473865028](https://github.com/tuckermclean/strange-company/actions/runs/39473865028) success |
| 39911e6 → 7743b39 | review 4 request_changes (panic on an open brace; existing Go type gains lines) → RED (D13) → attempt 5 (Opus, operator-approved exception). CI [39490032191](https://github.com/tuckermclean/strange-company/actions/runs/39490032191) success |
| — | review 5 **approve** at 7743b39 (240k-run fuzz with the recover bypassed: 0 panics) |

#### S2 — red-generations (22-AC-26, 22-AC-30, 22-AC-9, 22-AC-24 RED, 22-AC-32 `manifest_too_large`)

| Commit | Role |
|---|---|
| e498d77 → 0a956e4 | RED → attempt 1. CI [39502038250](https://github.com/tuckermclean/strange-company/actions/runs/39502038250) success |
| 73539ff, d9e1510 → b321307 | review 1 request_changes (bare-name keying; non-function edits ignored) → RED (D15) → attempt 2 test_spec_conflict → test fix d9e1510 → attempt 3. CI [39507619874](https://github.com/tuckermclean/strange-company/actions/runs/39507619874) success |
| f26d2ed, 5e25f97 → 0040df3, 8a5cc38 | review 2 request_changes (cgo preamble; inconsistent facts skipped) → RED (D16) → attempt 4 (Opus) test_spec_conflict → test fix 5e25f97 → attempt 4's patch + chore drift test. CI [39516182552](https://github.com/tuckermclean/strange-company/actions/runs/39516182552) success |
| — | review 3 **approve** at 8a5cc38 |

#### S3 — ingestion (22-AC-8, 22-AC-22, 22-AC-31, 22-AC-32 discovery)

| Commit | Role |
|---|---|
| 70e17ca → a6aa64f | RED (215 subtests, 125 fixtures) → attempt 1. CI [39528114551](https://github.com/tuckermclean/strange-company/actions/runs/39528114551) success |
| 5c09054, 3c33367 → 428044e | review 1 request_changes (four parser bypasses) → RED (D18; amended by the orchestrator before push) → attempt 2 test_spec_conflict → test fix 3c33367 → attempt 2's patch. CI [39531964379](https://github.com/tuckermclean/strange-company/actions/runs/39531964379) success |
| 3e56986 → dba7b22 | review 2 request_changes (INI section hidden behind odd line terminators) → RED (D19) → attempt 3. CI [39538561806](https://github.com/tuckermclean/strange-company/actions/runs/39538561806) success |
| — | review 3 **approve** at dba7b22 (158-case parser oracle; 33 scenarios × Validation on/off) |

#### S4 — policy-scope (22-AC-10, 22-AC-11, 22-AC-33)

| Commit | Role |
|---|---|
| 57b19c0 → 92a147c | RED → attempt 1. CI [39549613378](https://github.com/tuckermclean/strange-company/actions/runs/39549613378) success |
| 08a7331 → 6b81d23 | RED (D20, operator: Check-level invariant + six F04 fixtures) → GREEN 2. CI [39555357012](https://github.com/tuckermclean/strange-company/actions/runs/39555357012) success |
| e8d8d53 → 9a8d7fa | review 1 request_changes (refactor producer could touch test-classified paths) → RED (D21) → attempt 3. CI [39565021054](https://github.com/tuckermclean/strange-company/actions/runs/39565021054) success |
| — | review 2 **approve** at 9a8d7fa |

#### S5 — green (22-AC-23, 22-AC-25, 22-AC-27, 22-AC-24 GREEN)

| Commit | Role |
|---|---|
| 1734f10 → bdaa8ad | RED (141 leaves, 96 fixtures) → attempt 1. CI [39574643830](https://github.com/tuckermclean/strange-company/actions/runs/39574643830) success |
| 7305b73, dae914a → d531b7b | review 1 request_changes (manifest file judged by blob only; failures dropped by truncation) → RED (D22) → attempt 2 test_spec_conflict → setup-only helper fix dae914a → attempt 2's patch. CI [39579996521](https://github.com/tuckermclean/strange-company/actions/runs/39579996521) success |
| — | review 2 **approve** at d531b7b |

#### S6a — judges, completeness, determinism (22-AC-3)

| Commit | Role |
|---|---|
| ca1df36 → f71955d | RED (98 judge fixtures; completeness and 100× tests) → attempt 1. CI [39591942751](https://github.com/tuckermclean/strange-company/actions/runs/39591942751) success |
| 2718f3a → e882c41 | review 1 request_changes (JudgeArtifact looser than the schema; report counted unrendered headings) → RED (D23) → attempt 2. CI [39602732660](https://github.com/tuckermclean/strange-company/actions/runs/39602732660) success |
| e090e2a, 83ae24a → a7b502c | review 2 request_changes (alias-bomb hang; container fences and HTML) → RED (D24) + bounded-bomb test fix 83ae24a (RED 3 had been OOM-killed) → attempt 3. CI [39619548175](https://github.com/tuckermclean/strange-company/actions/runs/39619548175) success |
| c8d3a28 → e803da5 | review 3 request_changes (partial tag/link stripping) → RED (D25) → attempt 4 (Opus). CI [39631783334](https://github.com/tuckermclean/strange-company/actions/runs/39631783334) success |
| — | review 4 request_changes on the recogniser only (list-item fences); everything else approved → re-slice |

#### S6b — research-report recogniser (22-AC-3 judge rule)

| Commit | Role |
|---|---|
| 9d5b078 → aa238f3 | RED (D26: six repros + CommonMark oracle corpus, 39 fail-opens at head) → attempt 1. CI [39643029415](https://github.com/tuckermclean/strange-company/actions/runs/39643029415) success |
| ff9ee2f → 02f71b1 | review 1 request_changes (indented HTML openers in list continuation) → RED (D27; corpus re-recorded, seed 27) → attempt 2. CI [39654185436](https://github.com/tuckermclean/strange-company/actions/runs/39654185436) success |
| — | review 2 **approve** at 02f71b1 (240k random reports from three generators: 0 fail-opens) |

#### S7 — observer (22-AC-29, 22-AC-24 child env and replay export, 22-AC-32 `toolingFrom`)

| Commit | Role |
|---|---|
| 83f2268 → 30688e7 | RED (17 functions; satisfiability with 22 mutations caught) → attempt 1. CI [39672027912](https://github.com/tuckermclean/strange-company/actions/runs/39672027912) success |
| e9beca9 → c2ab364 | review 1 request_changes (replay and base tooling read the workspace `.git` after head ran; `run` dumpable) → RED (D28; identification-only helper edits) → attempt 2. CI [39680925312](https://github.com/tuckermclean/strange-company/actions/runs/39680925312) success |
| — | review 2 **approve** at c2ab364 (all-state `.git` poisoning, `/proc` surface, 10/10 runs and runs under load) |

#### F06 — gate evidence

| Criterion | Test(s) / evidence | Status |
|---|---|---|
| 22-AC-3 | `TestFixtureCompleteness_22AC3` (inventory derived from the plan 22 §14.3/§14.6/§14.7/§14.8 tables and the red/green-result enums), `TestClassification_100xByteIdentical_22AC3`, `TestJudgeFixtures_100xByteIdentical_22AC3`, `TestGoBuiltHomes_100xByteIdentical_D23_22AC3`, `TestClassifyJudge_*`/`TestClassifyJudging_*` (98 fixtures + D23–D27), `TestReportRecogniser_NeverAddressesWhatCommonMarkHides_D26_22AC3`; `go test ./src/internal/verify/...` passes | pass |
| 22-AC-4 | `TestAnalyzeStubs_*`, `TestClassifyRed_StubFixtures_22AC4`, D9–D13 suites (`d9`/`d10`/`d11`/`d13_test.go`) | pass |
| 22-AC-5 | `TestClassifyRed_ErrorClass*`, `TestNormalizeReport_*`, `TestLoadAdapters_*` | pass |
| 22-AC-6 | `TestClassifyRed_AlreadySatisfied*`, R10 order tests | pass |
| 22-AC-7 | `TestClassifyRed_Baseline*`, `UnusableFacts_FailClosed` | pass |
| 22-AC-8 | `TestCheckIngestion_Fixtures_22AC8` (119 fixtures), `_Roles`, `_DeterministicAndSorted`, `_StartAfterRed`, `_AddedLineParsing`, `_HostileGitConfig`, `_BinaryFiles`, `TestAddedLines_*`, D18/D19 recogniser suites | pass |
| 22-AC-9 | `TestBuildManifest_Sc184MatchesSchemaExample_22AC9` (digest per D14), `_DigestCoversTheDocument`, `_OnlyForRedValid`, `_CumulativeGeneration`, D15/D16 schema and determinism tests, `manifest_limits_test.go` drift test | pass |
| 22-AC-10 | `TestValidateExemption_Decisions`, `_NotAllowed`, `_RepositoryGlobs`, `_ModeAndJustification`, `_PureAndDeterministic` (all `_22AC10`) | pass |
| 22-AC-11 | `TestCheckScope_DocumentationOnlyProducer`, `_MetadataProducerEscalation`, `_ClassesByPathClass`, `_InvalidInputsFailClosed`, `_DeterministicOrder`, `TestValidateExemption_AbsentOrEmptyAllowList…`, D21 tests; F04 `TestCheck_AutoClassOverlappingHumanClassFails_22AC11` | pass |
| 22-AC-22 | `TestCheckIngestion_Fixtures_22AC22`, `TestCheckIngestion_GoMod_D18_22AC22`, `_GoModSeparators_D19`, `_GoModCanonicalSubset_D19` | pass |
| 22-AC-23 | `TestClassifyGreen_IsolationRun_G4b`, `_ImplementerMocksOnly_TamperDetected`, `TestDeriveIsolationFiles_*`, D22 N1/N2 tests | pass |
| 22-AC-24 | RED: `TestClassifyRedGen_R11bNondeterministic`, `_R11bMarkerConditional`, `_R11bRepeatFacts_FailClosed`; GREEN: `TestClassifyGreen_RedReplay_G9_22AC24`; observer: `TestRun_ChildEnvironmentIsBuiltFromScratch`, `_SuiteEnvCannotReintroduceSCVariables`, `_ReplayExportHasNoGitNoSCVariablesNoStubMarkers`, D28 B1/N3 tests | pass |
| 22-AC-25 | `TestClassifyGreen_FullSuiteComparison_G5b`, `_PreexistingTestRenamedOutOfDiscovery`, `_TruncatedHidesFailure_D22`; the RED clause in `testdata/red/R8` | pass |
| 22-AC-26 | `TestClassifyRedGen_R12DropsManifestTest`, `_R12Twins`, `_R13UnapprovedTestChange`, `_R13Twins`, `_TesterDeletion`, D15/D16 R13 tests | pass |
| 22-AC-27 | `TestClassifyGreen_Integrity_G2`, `TestDeriveIntegrity_*` (foreign commits incl. adversaries, head descent, manifest files from Git, stub markers, ingestion violations), D22 B1/N3 tests | pass |
| 22-AC-29 | `TestRunEmit_RedObservation`, `TestRun_SealsReportBeforeParsing`, `_KillsDetachedDescendantsBeforeSealing` (setsid, setpgid, double fork, sleeper), `_ReportSymlinkOutsideWorkspaceIsNotFollowed`, `_PlantedRecordSymlinksAreNotFollowed`, `TestEmit_RefusesTamperedRecord`, `TestRun_ProcEnvironOfRunHoldsNoNonce`, `_NonceInLaunchEnvironmentNeverReachesTheSuite`, `_SuiteCannotOpenRunProcMemOrEnviron_D28B2`, `TestRunEmit_ForgedPassLinesLeaveProtectedStatusUnchanged` | pass |
| 22-AC-30 | `TestClassifyRedGen_R1bExistingTestWeakened`, `_R1bTwins`, `_R1bUnparsableGo_FailClosed`, `_R12EarlyReturn`, `_R12BodyEdits`, D15/D16 R1b/R12 tests; `G_STRENGTHEN_AUTHORIZED` by F05 (`accounting_d14_test.go`, trajectory `already-satisfied-needs-human`) | pass |
| 22-AC-31 | `TestDeriveDiff_*`, `TestCrossCheckCompare`, `TestRevListAndIsAncestor` (+ `_IgnoreGrafts_D18`), `TestLsTree`, `TestCheckIngestion_ProtectedPaths*`, `_Gitlinks`, `_DiffTooLargeBeforeAnyRule`, `_MergeCommitResolution`, `TestCheckIngestionManyFiles_EveryViolationSeen` | pass |
| 22-AC-32 | `TestClassifyRedGen_R14ManifestTooLarge_22AC32` (61 KiB), D15/D16 per-item limits, `TestManifestLimits_MatchSchema_D16`; discovery: `TestDeriveDiscoveryProtection_*`, `TestCheckIngestion_IncludedMakefileAndConfigPaths_22AC32`, D18/D19 Make suites; `toolingFrom`: `TestRun_CheckWithoutToolingFromRunsBaseTooling_22AC32`, `_RedRunExecutesNoCheck`, D28 base-hash tests | pass |
| 22-AC-33 | policy `TestMergePolicy_*_22AC33`, `TestCheck_Plane*_D20_22AC33`, `TestSCPolicyCheckCommand_PlaneGlobOmitted_D20_22AC33`; verify `TestCheckScope_E2EProtectedForStrangeCompany`, `TestClassifyRed_E2E…`, `TestCheckIngestion_E2E…`, `TestCheckScope_TesterAndImplementerProtectedPaths`, `TestCheckScope_GitlinkDeniedForEveryRole_D21_22AC33` | pass |
| TDD discipline | local `make tdd-discipline-check TDD_BASE=b1098ba` on the evidence head (checker and policy from base): pass: 31 RED commits, 1,617 distinct RED failures, `build_ok`, 0 failures, 18 `TDD_RED_TEST_MODIFIED` warnings (the post-RED test changes listed below), 2 superseded (the bomb cases) | pass |

Deviations and disclosures:
- **History rewrite (orchestrator, operator-instructed):** the first discipline dry run (on b44b19d) failed `TDD_RED_TESTS_NOT_GREEN` on two REDs: 0160fbb (the superseded bomb cases below) and 84e8ce4. In 84e8ce4, two subtest names of `TestDeriveDiff_OnlyObjectIDs_22AC31` embedded a `t.TempDir()` path, so their ids changed on every replay, and neither a later commit nor a `TDD-Superseded` line could match them.
  - A tester's two-line test-only fix (the name uses `PWN` for the temp path; the revision passed to `DeriveDiff` is unchanged) was squashed into that RED, which is now 70e17ca.
  - Commits before it keep their SHAs. Every commit from S3 on is rewritten; the SHAs above are the rewritten ones. Reviews and CI ran on the pre-rewrite SHAs (tag `f06-pre-rewrite` = b44b19d, head of the reviewed branch); the map is `git log --reverse --format=%h b1098ba..f06-pre-rewrite` against the same range of c2ab364, line by line. A fresh reviewer approved the rewrite (range-diff, trees, superseded lines). CI on the rewritten head: [39717986310](https://github.com/tuckermclean/strange-company/actions/runs/39717986310) success.
  - `git range-diff b1098ba f06-pre-rewrite c2ab364` shows one changed patch (70e17ca, those two lines); every other commit is identical, and c2ab364's tree is b44b19d's plus those two lines.
- **Superseded RED tests:** `TestClassifyJudge_AliasBombAndAnchors_D24_22AC3/invalid/bomb_8_levels` and `…/bomb_10_levels` (RED e090e2a) were replaced by the bounded 5- and 6-level cases in RED 83ae24a. The evidence head commit carries the two `TDD-Superseded` lines.
- **Changes to this node's own RED tests and fixtures after their RED** (each is a one-purpose commit or an orchestrator-recorded decision; no assertion was loosened except where a test contradicted the decided rule):
  - 2f506e2: escaped jest fixture.
  - 2e9108b: duplicate ids.
  - d9e1510: the R14 per-item cases drop a red-result schema assertion their input made impossible.
  - 5e25f97: a U+2028 case becomes `\n`, after the RED 3 audit chose to agree with the validator.
  - 3c33367: one TOML subcase becomes a twin, because both spellings decode equal.
  - dae914a: setup-only scenario baseline binding.
  - 83ae24a: the alias-bomb cases are bounded, so RED fails on assertions rather than OOM; `bomb_8_levels` and `bomb_10_levels` were removed.
  - ff9ee2f: the oracle corpus is re-recorded (D27(c)).
  - e9beca9: helpers identify `run` by cmdline.
  - 5c09054: amended by the orchestrator before push (the guard hook reserves rewrites).
- **Changes to merged F04 tests:** the six policy fixtures under D20 (operator-approved).
- **Attempt counts:** S1 used five implementer attempts (attempt 5 an operator-approved exception). S6 used four and was re-sliced (S6b used two). Several attempts stopped on a test_spec_conflict; the orchestrator then committed the implementer's unchanged patch after a tester's test fix.
- **Protected and elevated paths:** `src/internal/verify/**` and `src/cmd/sc-verify/**` are elevated (enforcement plane). `src/internal/policy/**` is elevated. No protected path changed, except the six F04 test fixtures under D20.
- **Dependencies:** `verify` imports `regexp2` and `jsonschema/v6`, already pinned for `src/internal/contracts`; `go.mod` is unchanged.

Follow-ups (not blocking GATE-F06):
- **Contracts (human PR, protected):**
  - `repo-validation.schema.yaml` `Suite.report.path` uses `oneOf [const "-", RepoPath]`, and "-" matches both, so the real schema rejects every stdout-report suite. Use `anyOf`, or exclude "-" from RepoPath.
  - `test-manifest.schema.yaml` `examples[0]` has a placeholder digest and a Go `stub_unimplemented` test.
  - The red-result reasons have no schema reason for an invalid judge; the green-result reasons have no producer-scope reason.
  - The platform semantic-key lists miss package.json `pretest`/`posttest`, `overrides`/`resolutions`, Cargo `[target.*.dev-dependencies]`, `[replace]`, `.cargo/config` and tsconfig `extends` (`policy/verification.yaml`).
- **Plan 22 wording:**
  - §9.3 `jobbuilder` → `jobs`.
  - `test/e2e` is protected vs elevated.
  - §14.5 `.eslintrc*` vs the RepoPath pattern.
  - 22-AC-3's path.
  - Plain `make policy-check` never reads `.strange-company/`; caught by `make test` and `MergePolicy` today (`mk/policy.mk` human PR).
- **C04, together with 22-AC-12 re-recording:**
  - real pytest JUnit (failure `type`/prefix);
  - Rust/jest panic text only in the JUnit body;
  - the isolation run (G4b) and repeats (R11b) production in `sc-verify`;
  - `redScope` (ignored today: the head run is unscoped, fail closed);
  - Rust/Python/JS observation;
  - the judge subject in `sc-verify run`;
  - the `install` and `lint-policy` subcommands;
  - `--policy` vs the plan 20 command;
  - GOTOOLCHAIN/GOWORK in the child env;
  - line-language method stubs;
  - non-Go R1b;
  - TS overloads;
  - preservation-criterion judging;
  - artifactPaths scoped to the task;
  - fenced code in list items in research reports, and column-0 `<` lines, both failing closed;
  - re-recording the oracle against GFM/goldmark.
- **Plan gaps (recorded, fail closed or unreachable):**
  - new declarations in an existing file (`init()`, `TestMain`, methods, `//go:linkname`, `//go:embed`) can affect existing tests;
  - a new file with `#cgo CFLAGS`;
  - a marked Go field can shadow a promoted method;
  - Go method stubs matching well-known interfaces;
  - Rust prelude names;
  - Make `$(eval $(X))` evasion in base content;
  - `.git/shallow`;
  - `contracts.ParseYAML` alias expansion is unbounded for every caller;
  - CheckScope accepts a forged `granted/auto` decision for a class the repository does not allow (§9.2 checks at task creation);
  - the replay is detectable (design residual of G9);
  - a fake `--- PASS` for a non-existent test is observable (SI-07 reviewer-packet statement).
- **Test-only:** `TestBuildManifest_CumulativeGeneration_22AC9` aliases a slice (harmless; needs operator approval to fix).

### F07 — worker-sdk-and-fake-harness

Branch `node/F07-worker-sdk-and-fake-harness`, built with `/sc-node F07` from `main` e62ef45 (owning plans `plans/27-worker-contract.md` and `plans/28-harnesses.md`). It adds:
- `src/internal/worker/**` (bootstrap, Runner client, git operations with a private `GIT_DIR`, context tree, pre-checks and secret scan, shim protocol, framer, result assembly, adapters `claude-code` and `fake`, self-test) and `src/cmd/sc-worker` (`run`, `shim`, `selftest`);
- `src/internal/scrub/**`, `src/internal/clock` (D5), `src/internal/fakeharness/**` and `src/cmd/sc-fake-harness` (all 51 scenarios of plan 28 §14.4);
- `policy/harnesses.yaml` (`claude` disabled + one `fake-<scenario>` entry per enum value) and the F04 loader codes for it;
- the `harness-fake` image (`harnesses/fake/`), the host-run tier-E targets `harness-image-test` and `test-worker-component` (`mk/worker.mk`, Makefile host-only branch), the Layer-2 suite `test/e2e/worker/run`, the fixture `test/e2e/fake-target/`, and the e2e job `worker-component` (Docker and rootless Podman).

Test layers (brief D3): Layer 1 is `make test` (tier V, offline, no engine; every RED target). Layer 2 is `make harness-image-test IMAGE=harness-fake` and `make test-worker-component` (tier E, never a red target).

Slices: S1 registry and embedded schemas; S2 scrub, result, adapters, exit codes; S3 git operations and pre-checks; S4 shim, framer, timeout, stdin; S5a private GIT_DIR; S5b scrubbing in the stream path; S5c symlink safety; S6a scenario table, gateway counts, env isolation, self-test; S6b skip_fold/hostile knobs; S6c secret scan of path names and the 16 MiB cap; S6d Layer 2 (image, targets, suite, CI job); S6e passwd entry. S5 and S6 were re-sliced by the orchestrator as findings arrived; S6e was opened by the operator after S6d spent its four attempts.

**Status: every slice approved by its last review; every GATE-F07 criterion has passing evidence below.**

Decisions (orchestrator under the operator's standing delegation unless marked operator; full text in `out/sc-node/F07/brief.md` and `progress.md`):
- **D1–D9 (brief):** paths under plan 01 §6.2 (`src/internal/worker/**`; 27-AC-1 reads `./src/internal/worker/... ./src/internal/scrub/... ./src/cmd/sc-worker/... ./src/cmd/sc-fake-harness/...`); fake image in `harnesses/fake/` (D2; plan 28 §4 says `images/harness-fake/`); two test layers with injectable path roots and a fake-ssh seam (D3); `test-worker-component` is tier E (D4; plan 01 §8 lists no tier); `clock.Clock` everywhere (D5); digest-pinned registry placeholders until plan 23 (D6); fixture files matching protected globs are written at build time from Go constants (D7); hostile knobs implemented, Runner-side effects are C02 (D8); 27-AC-13's literal 400 KiB only at unit level because HarnessClaim `maxLength` is 65536 (D9).
- **OD1, OD2:** own S1 RED corrected (own `HARNESS_*` code required, generic `POLICY_SCHEMA_INVALID` may accompany); `sk-` needs a non-alphanumeric left boundary (report to plan 34).
- **Operator (2026-10-23): private GIT_DIR.** All post-harness git runs against config/HEAD/refs/index in `/var/sc/private`, objects through alternates to the workspace store; restore and `git_internals_restored` stay as defence in depth. Deviation from plan 27 §13.3/§11 (amendment proposed in the PR).
- **OD-S5a:** an unreadable or corrupt workspace branch ref means "no harness commits"; `head_not_descendant` only for a readable non-descendant sha.
- **OD-S5b:** known_hosts content is in the exact-match scrub set (27-AC-4; plan 27 §13.4 omits it).
- **OD-S6a:** the 27-AC-5 dump is unconditional (`out/env.txt`, `out/files.txt`); `selftest` takes an injectable `SelfTestEnv` (read-only dirs `/ /etc /var /opt /bin /home /usr`); `disable_cancel` ignores a heartbeat cancel while SIGTERM still exits 143; `malformed-result`/`malformed-handoff` → 422 → exit 4; `push-no-result` → exit 0 and no POST; the shim's descendant sweep runs before `res-<n>` is published. OD-S6a-2/-3: two own-RED corrections (a36273a: the brief miscounted 52 scenarios, the enum has 51; 87f3577: a round-1 test superseded by serialised shim jobs).
- **OD-S6b-1:** `settleBound` 3.1 s is a heuristic; a D-state leader or WaitDelay residual can exceed it, which can only misreport advisory validation.
- **OD-S6d-1:** the Layer-2 build passes `--build-arg SC_INPUTS_ID12=<first 12>`; the Containerfile stamps both the binary and the `strange.company/sc-worker-version` label from it, because buildah 1.33.2 (ubuntu-24.04) rejects every `%`/`#` substitution. The own static test was corrected to accept that form (da9ad7a); the runtime label check stays authoritative.
- **Operator (2026-10-23): the e2e job is added in this PR.** The orchestrator wrote it (09db760; role agents may not edit `.github/**`).
- **Operator (2026-10-24): S6e** opened with a fresh budget after S6d's four implementer attempts.
- **Implementer decisions accepted by review:** any 4xx except 429 on the result POST → exit 4; packet backoff of 8 delays; exact-match scrub ignores secrets under 8 characters; a real-time git transport watchdog (`clock.Real{}`, 10 s without a live child) plus `WaitDelay` 3 s; a 50 ms forged-result poll; serialised shim jobs (lowest-numbered request first, foreign requests absorbed and flagged); `permission_denials` caps 100/120/300; `over-budget` cap 10000; fake scenario file contents; test seams `SC_FAKE_FORGE_RESULT_URL` and `SC_FAKE_FORGE_PUSH_REMOTE`; pre-planted res/stream/cancel/tmp checks; `ProcPids` excludes pids ≤ 1; `writeRelReplace` (tmp + renameat, ≤ 8 EEXIST retries); `.sc-shim` mode 0770 check; `push_extra_refs` pushes `main` first and logs `fake_push_main_accepted`; `parent_override` via `reset --soft` in the private GIT_DIR with post-commit checks against `base_commit`; `skip_fold` scans every published commit and fails closed on merges or more than 100 commits; path names are scanned from the `diff --git`, `rename to`, `copy to` and `+++` headers.

#### S1 — registry and embedded schemas (28-AC-1, 28-AC-2, 27-AC-14)

| Commit | Role |
|---|---|
| dd8bfd9, 21ee990 → fe437c3 | RED (20 fail) + RED correction (OD1) → attempt 1 test_spec_conflict, attempt 2. CI [39888497514](https://github.com/tuckermclean/strange-company/actions/runs/39888497514) success |
| — | review 1 request_changes (fake-deadline-exceeded lacks `disableWorkerTimeout`; credential heuristic) → folded into S2 |

#### S2 — scrub, result, adapters, exit codes (27-AC-13, 28-AC-5, 28-AC-6, 27-AC-2, 27-AC-17)

| Commit | Role |
|---|---|
| bc43532 → af45d59, 013167d | RED (incl. S1 findings) → attempt 1. CI [39893946147](https://github.com/tuckermclean/strange-company/actions/runs/39893946147) success |
| 5315d96 → eb89131, 37edf66 | review 1 request_changes (PEM armor lines; `sk-` boundary, OD2) → RED → attempt 2 + own-test chore (`time.After` → context). CI [39898934252](https://github.com/tuckermclean/strange-company/actions/runs/39898934252) success |
| 0c29847 → 4fdbe4a, 1f99e07 | review 2 request_changes (JSON-escaped `sk-`) → RED → attempt 3 (+ gofmt chore; CI [39900583356](https://github.com/tuckermclean/strange-company/actions/runs/39900583356) failed on fmt). CI [39901155129](https://github.com/tuckermclean/strange-company/actions/runs/39901155129) success |
| — | review 3 **approve** at 4fdbe4a |

#### S3 — git operations and pre-checks (27-AC-6..10, 27-AC-12)

| Commit | Role |
|---|---|
| 91c5fe6 → da7c47b | RED (19 fail) → attempt 1. CI [39906216377](https://github.com/tuckermclean/strange-company/actions/runs/39906216377) success |
| ed5f6ea → a43c203 | review 1 request_changes (`.git/commondir` bypass; missing HEAD walks to parent repo; `++ ` line evades the scan) → RED → attempt 2. CI [39909079390](https://github.com/tuckermclean/strange-company/actions/runs/39909079390) success |
| — | review 2 **approve** at a43c203 |

#### S4 — shim, framer, timeout, stdin (27-AC-3, 27-AC-11, 27-AC-15, 27-AC-16)

| Commit | Role |
|---|---|
| b960402 → 884501d | RED → attempt 1. CI [39915880741](https://github.com/tuckermclean/strange-company/actions/runs/39915880741) success |
| 8512bb4 → 0155724 | review 1 request_changes (pod-deletion SIGTERM: shim kept the full grace, END lost); operator decision private GIT_DIR (S5a) → RED → attempt 2. CI [39968687741](https://github.com/tuckermclean/strange-company/actions/runs/39968687741) success |
| — | review 2 **approve** at 0155724 |

#### S5a — private GIT_DIR (operator decision; 27-AC-9, 27-AC-10, 27-AC-12)

| Commit | Role |
|---|---|
| de2e5fd → fb644e4 | RED (forged-result race, corrupt workspace state, lying commit-graph; OD-S5a) → attempt 1. CI [39974762819](https://github.com/tuckermclean/strange-company/actions/runs/39974762819) success |
| ba4297e → 61138f3 | review 1 request_changes (`--text` on the partial diff broke §14.13 binary-free) → RED → attempt 2. CI [39982271176](https://github.com/tuckermclean/strange-company/actions/runs/39982271176) success |
| — | review 2 **approve** at 61138f3 |

#### S5b — scrubbing in the stream path (27-AC-4)

| Commit | Role |
|---|---|
| fcf73d3 → cf7ca12 | RED (8 fail; OD-S5b) → attempt 1. CI [39988616765](https://github.com/tuckermclean/strange-company/actions/runs/39988616765) failure |
| bc492ef → 6984a9f | review 1 request_changes (unbounded drain stalls sc-worker) → RED → attempt 2. CI [39997005268](https://github.com/tuckermclean/strange-company/actions/runs/39997005268) success |
| — | review 2 **approve** at 6984a9f |

#### S5c — symlink safety (27-AC-24)

| Commit | Role |
|---|---|
| e42f05c → c75e21e | RED (18 fail; forged loose start commit found) → attempt 1. CI [40006344088](https://github.com/tuckermclean/strange-company/actions/runs/40006344088) success |
| b673a0a → a69c01b | review 1 request_changes (flaky own test; `/workspace/repo` path-based) → RED → attempt 2. CI [40015860397](https://github.com/tuckermclean/strange-company/actions/runs/40015860397) success |
| dff50d1 → 35ab25a | review 2 request_changes (own-test no-op op; arm64 build broken by `SYS_NEWFSTATAT`) → RED → attempt 3. CI [40021646527](https://github.com/tuckermclean/strange-company/actions/runs/40021646527) success |
| — | review 3 **approve** at 35ab25a |

#### S6a — scenario table, gateway counts, env isolation, self-test (28-AC-4, 28-AC-7, 27-AC-5 Layer 1, 28-AC-3 self-test)

| Commit | Role |
|---|---|
| 8872d1f, a36273a → dd5db51 | RED (63 fail; OD-S6a) + RED correction (51 scenarios) → attempt 1 (test_spec_conflict, re-applied after the correction). CI [40043554068](https://github.com/tuckermclean/strange-company/actions/runs/40043554068) success |
| 362408c → 243d606 | review 1 request_changes (forged concurrent shim job survives the sweep) → RED → attempt 2. CI [40051183071](https://github.com/tuckermclean/strange-company/actions/runs/40051183071) failure (`restore config: file exists`) |
| 4cb1b7f, 87f3577 → edc82de | review 2 request_changes (request planted at harness exit runs after the sweep) + the CI EEXIST race → RED + own-test supersession (OD-S6a-3) → attempt 3. CI [40060552533](https://github.com/tuckermclean/strange-company/actions/runs/40060552533) success |
| — | review 3 **approve** at edc82de |

#### S6b — hostile knobs and live-forged results (28 §14.4 knobs, S6a-r3 F1–F3)

| Commit | Role |
|---|---|
| fd6bf99 → aea8454 | RED (8 fail) → attempt 1. CI [40068641162](https://github.com/tuckermclean/strange-company/actions/runs/40068641162) success |
| 7dd1ba1 → 61da515 | review 1 request_changes (`skip_fold` skipped secret scan and pre-checks) → RED → attempt 2. CI [40076180942](https://github.com/tuckermclean/strange-company/actions/runs/40076180942) success |
| 5150c5c → 9a92b15 | review 2 request_changes (`skip_fold` published unscanned intermediate commits, merges, messages) → RED → attempt 3. CI [40083710635](https://github.com/tuckermclean/strange-company/actions/runs/40083710635) success |
| — | review 3 **approve** at 9a92b15 |

#### S6c — secret in path names, 16 MiB git output cap (27-AC-12)

| Commit | Role |
|---|---|
| 2800f37 → 16f7566 | RED (5 fail) → attempt 1 (after the Galaxy outage). CI [40101852411](https://github.com/tuckermclean/strange-company/actions/runs/40101852411) success |
| 3867610, c255c09 → 6faa528 | review 1 request_changes (path line after a header-only entry skipped) → RED + passing chore (commit-object cap) → attempt 2. CI [40108009654](https://github.com/tuckermclean/strange-company/actions/runs/40108009654) success |
| — | review 2 **approve** at 6faa528 |

#### S6d — Layer 2: harness-fake image, host-run targets, suite, e2e job (28-AC-3, 27-AC-1, 27-AC-5, 27-AC-16, 27-AC-24, 27-AC-8 over real ssh, plan 27 Q3)

| Commit | Role |
|---|---|
| 113bc62 → 57d432f, 09db760 | RED (static image/target rules; suite) → attempt 1 + orchestrator e2e job. verify CI [40113994996](https://github.com/tuckermclean/strange-company/actions/runs/40113994996) success; e2e CI [40113996806](https://github.com/tuckermclean/strange-company/actions/runs/40113996806) failure (cold module cache) |
| 5f0a297 → 36751d2 | review 1 request_changes (cold module cache; strace split lines fail open) → RED (suite fixes + Go toolchain, run-alone, version label) → attempt 2. verify CI [40117413053](https://github.com/tuckermclean/strange-company/actions/runs/40117413053) success; e2e CI [40117414905](https://github.com/tuckermclean/strange-company/actions/runs/40117414905) failure (Docker tmpfs `noexec`; old buildah rejects the label expansion) |
| 05b4648, da9ad7a → c2acfd5 | test-infra chores (tmpfs `exec`; OD-S6d-1 label form) → attempt 3 blocked (buildah 1.33.2) → attempt 4 (Opus). verify CI [40120385325](https://github.com/tuckermclean/strange-company/actions/runs/40120385325) success; e2e CI [40120387289](https://github.com/tuckermclean/strange-company/actions/runs/40120387289) failure (Docker: `No user exists for uid 65532`) |
| — | budget spent (4 attempts) → operator opened S6e |

#### S6e — passwd/group entry for uid 65532 (28-AC-3; Docker, containerd, kubelet)

| Commit | Role |
|---|---|
| 6efbb43 → 73c50b4 | RED (static entry check; podman `--passwd=false`; getpwuid/ssh image check) → attempt 1. verify [40128142876](https://github.com/tuckermclean/strange-company/actions/runs/40128142876) success; e2e [40128144364](https://github.com/tuckermclean/strange-company/actions/runs/40128144364) success (one transient k3d image-import failure, re-run); `worker-component` Docker and Podman: image-test 22/0, component 64/0, no GAP |
| — | review **approve** (S6d round 2 + S6e) at 73c50b4 |

#### F07 — gate evidence

| Criterion | Test(s) / evidence | Status |
|---|---|---|
| 27-AC-1 | `test/e2e/worker/run component`: `go test` with `--network none` in the ci image over `./src/internal/worker/... ./src/internal/scrub/... ./src/cmd/sc-worker/... ./src/cmd/sc-fake-harness/...` after a networked `go mod download` (cold cache verified); CI `worker-component` Docker and Podman | pass |
| 27-AC-2 | `TestWorkerRun_ExitCodes_27AC2` (7 cases incl. 70 via injected panic), `TestWorkerRun_Exit143_OnSIGTERM_27AC2`, `TestWorkerRun_SIGTERMInResultRetryWindowPostsOnce_27AC2_N5` | pass |
| 27-AC-3 | `TestShim_TimeoutScenario_27AC11_27AC3`, `TestFramer_LogBudget_27AC3`, `TestFramer_LongLineTruncatedAt64KiB_27AC3`, `TestDrain_HarnessCannotStallSCWorker_27AC3`, `TestDrain_SIGTERMWhileStreamIsHuge_27AC3` and the 12 `_27AC3` tests (BEGIN/END per scenario, SIGKILL mid-harness) | pass |
| 27-AC-4 | `TestScrub_CanariesNeverReachStdoutOrResult_27AC4` (four secret files, PEM block, `sk-ant-`; `redactions` ≥ 6) + S2/S5b scrub suites | pass |
| 27-AC-5 | Layer 1: `TestEnvIsolation_HarnessEnvironmentDump_27AC5`, `TestRun_DumpsEnvironmentAndFileList_27AC5`; Layer 2: real `/var/run`, `/var/sc` dump in the harness container (exactly the two harness-secret files, gateway token, `HTTPS_PROXY` with the egress token, none of the forbidden values); CI both engines | pass (flat mounts, see disclosures) |
| 27-AC-6 | `TestPipeline_TesterSuccess_ContextOutsideTree_27AC6`; Layer 2 real-ssh success run (`git show --name-only agent/sc-1`, context mode 0444) | pass |
| 27-AC-7 | `TestTopology_27AC7` (`branch_exists`, `branch_moved`, readonly ref digest), `TestImplementerContinue_PushesOnTopOfRed_27AC7_27AC8` | pass |
| 27-AC-8 | `TestCommitFormat_27AC8`, `TestImplementerContinue_PushesOnTopOfRed_27AC7_27AC8`; Layer 2 real-ssh push (subject, trailers, author, parent); plan 27 Q3: a 0440 key owned by 65532 is rejected (`UNPROTECTED PRIVATE KEY FILE`), the 0600 copy is accepted | pass |
| 27-AC-9 | `TestFold_ThreeHarnessCommits_27AC9`, `TestHarnessHead_PackedRefsFolded_27AC9`, `TestPrivateGitDir_FoldCountSurvivesWorkspaceDamage_27AC9` | pass |
| 27-AC-10 | `TestHardening_HostileFixture_27AC10`, `TestHardening_CommondirRedirect_27AC10`, `TestHardening_BrokenGitDirFallsBackToParent_27AC10`, `TestPrivateGitDir_*_27AC10` (incl. the forged-result race and the config-rewriter stress) | pass |
| 27-AC-11 | `TestShim_TimeoutScenario_27AC11_27AC3` (result within 60 + grace + 15 s, `/proc` scan finds no survivor) | pass |
| 27-AC-12 | `TestPrechecks_OneFixturePerRule_27AC12`, `TestPartialDiff_BinaryFree_27AC12`, `TestPrechecks_LyingCommitGraphCannotHideChange_27AC12`, `TestHarnessHead_NotDescendant_27AC12`, S6b/S6c tests (`skip_fold` per-commit scan, secret in path names incl. after header-only entries, 16 MiB cap fails closed, `max_diff_bytes` named ahead of the cap) | pass |
| 27-AC-13 | `TestFit_OversizedReportIsTruncated_27AC13`, `TestFit_ResultThatFitsIsNotTruncated_27AC13`, `TestResultFit_OversizedReportEndToEnd_27AC13` (D9) | pass |
| 27-AC-14 | `TestEmbeddedSchemas_MatchContracts_27AC14` | pass |
| 27-AC-15 | `TestShim_StdinIsClosed_27AC15` | pass |
| 27-AC-16 | `TestShim_IgnoresSIGTERM_KilledWithinGrace_27AC16`, `TestShim_ValidationCommandRunsInShim_27AC16`, `TestShim_HarnessOutputOnlyThroughFramer_27AC16`, `TestShimSubcommand_ExitsZeroOnDone_27AC16`; Layer 2: `/proc/self/cgroup` of the validation command differs from sc-worker's (CI both engines; rootless local run prints a GAP and checks hostname and mount namespace), SIGTERM-ignoring harness gone in 5.2 s (< 7 s) | pass |
| 27-AC-17 | `checkFramed`/`checkFramedResult` in every result-posting component test (`TestWorkerRun_ExitCodes_27AC2`, `TestScenarioTable_PostedResults_28AC4`, git and shim component tests): exactly one block, sha256 equals the posted body | pass |
| 27-AC-24 | `TestSymlinkSafety_ClaimJSONSymlinkIsClaimAbsent_27AC24`, `TestSymlinkSafety_GitHooksSymlink_WorkspaceTampered_27AC24`, `TestSymlinkSafety_NotesSymlinks_27AC24` + S5c suites; Layer 2: strace of sc-worker (split-line join, 7-case self-test) shows no open resolving a planted link and one `openat2(RESOLVE_NO_SYMLINKS|RESOLVE_BENEATH) = -1 ELOOP` per plant; `/var/sc/private` hash unchanged; CI both engines | pass |
| 28-AC-1 | `TestHarnessRegistry_CommittedRegistryAccepted_28AC1`, `TestHarnessRegistry_FixturesRejectedWithDistinctCodes_28AC1`, `TestCheck_CommittedHarnessRegistryAccepted_28AC1`, `TestCheck_HarnessRegistryFixtures_28AC1` (7 fixtures, 7 codes) | pass |
| 28-AC-2 | `TestHarnessRegistry_FakeEntriesMatchScenarioEnum_28AC2` (51 scenarios, one digest, `stage: test-only`) | pass |
| 28-AC-3 | static: `TestHarnessFakeImage_ContainerfileRules_28AC3`, `_AllowedPackages_28AC3`, `_HasGoToolchain_28AC3`, `_WorkerVersionLabel_28AC3`, `_UserHasPasswdAndGroupEntry_28AC3`, `TestMakeTargets_WorkerLayer2_*_28AC3`, checker guards; `make harness-image-test IMAGE=harness-fake`: uid 65532, `--read-only`, no setuid/setgid (find as root), packages within `harnesses/fake/allowed-packages.txt`, no package manager, `sc-worker selftest` ok, Go toolchain, labels, passwd entry; CI both engines 22/0 | pass (credential scan by listing only) |
| 28-AC-4 | `TestScenarioTable_PostedResults_28AC4`, `TestScenarioTable_CoversEverySpecifiedScenario_28AC4`, `TestScenarioTable_IgnoreCancel_28AC4`, `TestScenarioTable_IgnoreCancelKnobIgnoresHeartbeatCancel_28AC4` | pass |
| 28-AC-5 | `TestPrepare_ClaudeCodeGoldens_28AC5`, `TestPrepare_ClaudeCodeExtraArgsNotDuplicated_28AC5_N4` | pass |
| 28-AC-6 | `TestFakeKnobs_InertForRealAdapters_28AC6` | pass |
| 28-AC-7 | `TestRun_EveryScenarioCallsTheGateway_28AC7` (51 scenarios; `over-budget` until 403); Layer 2 gateway count | pass |
| TDD discipline | local `make tdd-discipline-check TDD_BASE=b7a7afc` on the rebased evidence head (checker and policy from base b7a7afc, the gh-159 batched/parallel checker): `tdd-discipline: pass`, 29 RED commits, 37 TDD_RED_TEST_MODIFIED warnings, all for this node's own RED tests and disclosed below, 73 min. The old checker earlier gave the same result and the same warning lines on pre-rebase e6b1aad (base e62ef45, about 5 h) | pass |

Deviations and disclosures:
- **Plan departures:** private GIT_DIR (operator; plan 27 §13.3/§11); `harnesses/fake/` instead of `images/harness-fake/` (D2); `harness-image-test` and `test-worker-component` run on the host like `make toolbox` (plan 01 §8.1 and CD-64 name only `toolbox`; amendment needed); the secret scan also covers path names (plan 27 line 385 says "added lines"); binary files count toward `max_diff_bytes` and a fold without `reset --soft` drops force-added ignored files (§14.10); known_hosts in the exact-match set; N9: the fake hard-codes `fake-model` (§9.2 has no model channel); `symlink_refused` events carry a `kind` field; the base directory is opened per call, not once at start-up (§13.3).
- **27-AC-5 file list** holds for flat secret mounts; a kubelet secret volume also exposes `..data` and `..<timestamp>/` entries, so plan 26's Job builder needs `subPath` mounts or the AC wording an amendment.
- **28-AC-3** checks credentials by file listing only (`trivy`, `skopeo`, `cosign` are not in the ci image). The image deletes the Go standard library's test `*.pem`/`*.key` fixtures to pass the credential look-alike check. Label values `cli-version=fake-0.1.0` and `source=https://strange.company/sc` are placeholders. Image size 465 MB uncompressed (plan 28 §12: ≤ 600 MiB).
- **27-AC-16** on rootless Podman without cgroup delegation prints a GAP and falls back to hostname and mount-namespace checks; on the GitHub runners (both engines) the real cgroup check ran. The "no bypass line" part is BEGIN/END counts at Layer 2; Layer 1 covers it fully.
- **Changes to this node's own RED tests after their RED:** 21ee990, 37edf66, bc492ef (runtime-built PEM), b673a0a, dff50d1, a36273a, 87f3577, da9ad7a; additive helper edits in `component_git_support_test.go` and `component_shim_test.go`; Layer-2 test-infrastructure fixes 5f0a297, 05b4648, 6efbb43.
- **Attempt counts:** S6d used all four implementer attempts (attempt 3 blocked by the buildah limitation, attempt 4 on Opus); the remaining Docker defect became S6e (operator). S2, S5c, S6a and S6b each used three. S1 attempt 1 and S6a attempt 1 stopped on test_spec_conflict.
- **External outage:** S6c GREEN waited about 100 minutes for galaxy.ansible.com (HTTP 500 on a locked tarball needed by every ci-image rebuild); no workaround was used.
- **History rewrite:** the first `tdd-discipline-check` on the final head failed with TDD_RED_TARGET_INVALID because two RED trailers quoted their package list (`GO_TEST_PKGS="…"`). The orchestrator reworded only those two lines (no file changed; every commit keeps its tree, author and dates) and force-pushed; the CI runs ran on the pre-rewrite commits with identical trees (old head 7036eb6, local tag `F07-pre-reword`). After #160 (gh-159, the faster checker) merged, the branch was rebased onto `main` b7a7afc without conflicts (local tag `F07-pre-rebase`). Every SHA in this section is post-rebase. The rebased tree differs from the pre-rebase one only by #160's files.
- **Incident:** the S3 reviewer wrapped `git` in a shell function after the role guard blocked it, to run probes in its scratchpad (repository untouched). Reported to the operator; every later prompt forbids working around a guard block.
- **Protected and elevated paths changed (human review):** `policy/harnesses.yaml` (+ F04 loader codes), `harnesses/fake/**`, `mk/worker.mk`, `Makefile`, `.github/workflows/e2e.yaml`, `test/e2e/**`. Elevated: `src/internal/worker/**`, `src/cmd/sc-worker/**`, `src/internal/scrub/**`, `src/internal/policy/**`.
- **Residual risks (threat notes):** a forged result while the harness is alive (shared uid 65532); glued `sk-` keys (`Bearersk-…`); re-added alternates are data only; a PEM armor line with an invalid byte is dropped and the unknown key body survives; TOCTOU on git's own path use; ctime granularity before kernel 6.13; a `skip_fold` intermediate AGENTS.md rewrite (Runner `MULTIPLE_COMMITS` covers it); UTF-16 secrets evade the byte scrubber; drain-skipped data is lost silently; END bytes semantics changed; quoted path names (`core.quotePath`) can hide a secret in a path from the scan; podman without `--passwd` would mask a missing passwd entry again.

Follow-ups (not blocking GATE-F07): S2-FU3 (`\\[A-Za-z]` boundary over-redacts); S4 N6 (res.ID / stale files), N7 (T<30→30, UTF-8 cut, lookPath fallback), NB-c; S5a N3 (64 Mi private dir ENOSPC; plan §11 row), N8 (SHA-1 vs 64-hex); S5b N4, N6, N-b; S5c N4, N5, N6, r3 F5; S6a-r3 F4; S6c N2 (`-z` name-status path scan); unused `forge-job` op and a near-duplicate test; 28-R-5 pipeline-level inertness untested at Layer 1; the empty `oom` file some worker test leaves in the repository root; S6d/S6e review: `test/e2e/worker/run` label regex accepts `in-unknown` for external images, nothing checks that `SC_INPUTS_ID12` is a prefix of `SC_INPUTS_ID` (plan 23 builder), `/usr/local/go` is outside the dpkg SBOM allowlist, and the `IMAGE=%` filter-out in `Makefile` is a no-op.
