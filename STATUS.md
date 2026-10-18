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
