# 52 — Deterministic CI strategy

**Status:** Stage-0 plan (draft)
**DAG nodes:** F01 (co-owner with 01: interim CI wiring and bootstrap discipline), F02 (co-owner with 01: CI execution of contract tests), P08 (co-owner with 20: trusted-CI cutover), P12 (contributes: required checks and release gate). Schedules the test targets of every other gate.
**Related plans:** [01 repository](01-repository.md) (target names), [02 toolbox](02-bootstrap-toolbox.md) (`ci` image), [17 GitHub mirror](17-github-mirror.md), [18 Flux](18-flux.md), [19 seed handoff](19-seed-handoff.md), [20 Woodpecker](20-woodpecker.md), [21 TDD workflow](21-tdd-workflow.md), [22 verification policy](22-verification-policy.md), [23 supply chain](23-supply-chain.md), [26 Agent Runner](26-agent-runner.md), [28 harnesses](28-harnesses.md), [29 state machine](29-task-state-machine.md), [36 network policy](36-network-policy.md), [37 admission](37-admission-policy.md), [42 alerting](42-alerting-notifications.md), [53 Model Gateway](53-model-gateway.md), [54 chart](54-strange-company-chart.md), [55 forge workflow](55-forge-workflow.md)
**Contracts:** consumes every contract through `make contracts`; `red-result.schema.yaml`/`green-result.schema.yaml` (22) by name; `policy/ci.yaml` (`CIPolicy`, plan 20; this plan owns its `tddDiscipline` section); owns `contracts/test-quarantine.schema.yaml` (§9.3) and the `tddDiscipline` section of `contracts/ci-policy.schema.yaml`.
**ADRs:** relies on ADR-0001, 0002, 0008, 0013, 0018, 0301, 0302. Introduces [ADR-0303](../adr/0303-offcluster-privileged-e2e-lane.md) (off-cluster e2e lane).

> Every section below is mandatory (SPEC §121). Requirements carry SPEC trace tags.

## 1. Purpose

SPEC §125 requires that "a deterministic CI strategy exists". This plan defines:
- the **test pyramid** and what each level may depend on;
- **which Make target runs on which event and on which CI**: interim GitHub Actions until P08, then Woodpecker, plus the off-cluster e2e lane (ADR-0303);
- **required status checks** per repository and per period;
- **determinism rules** and how each one is enforced;
- the **flaky-test policy**;
- **CI performance** on the small node;
- **bootstrap discipline**: how Phase-1 work proves TDD before the Runner exists;
- where the TDD verification pipelines, the SPEC §116 chart gates, the §105 security tests, the §106 TDD tests and the contract tests run.

Plan 01 owns target **names** and contents. Plan 20 owns Woodpecker mechanics. This plan owns **when** and **where** targets run, and **what blocks a merge or a release**.

## 2. Requirements derived from SPEC.md

1. Verification precedes construction, including for Stage-1 work before Woodpecker exists [SPEC §1.2, §119].
2. Trusted CI (Woodpecker) determines whether required deterministic gates passed. Agent-local validation is advisory [SPEC §36].
3. Default project CI does not require paid model APIs. Orchestration, TDD policy, state transitions, security and acceptance gates are testable deterministically [SPEC §104]. Real-model smoke tests are separate [SPEC §104].
4. CI verifies the worker escape set [SPEC §105] and the organization's TDD behavior [SPEC §106].
5. Every chart release eventually passes lint, values schema, template, Kubernetes schema validation, unit tests, k3d install, upgrade, uninstall and reinstall [SPEC §116].
6. Seed-to-Flux control transfer has an integration test [SPEC §34].
7. Critical images and dependencies are pinned [SPEC §37, §116].
8. Every implementation milestone has deterministic exit criteria [SPEC §124]. "Deterministic CI strategy exists" [SPEC §125].
9. Retries are bounded and failures produce evidence [SPEC §4.8].
10. **52-R-1** Every CI step is a plan-01 catalogue target. No CI product carries test logic of its own (plan 01 §2 rule).
11. **52-R-2** A tier-V target gives the same result for the same commit on a laptop, on GitHub Actions and on Woodpecker (ADR-0018). The only allowed time input is the `HEAD` committer date (§9.4 D9).
12. **52-R-3** No gating job is retried automatically, and no acceptance test is ever quarantined (§16, §9.3).
13. **52-R-4** Every behavior-changing human PR carries a RED commit, proven to fail meaningfully by re-execution in CI (§8.3).
14. **52-R-5** No CI job, in any lane, holds a model provider credential. CI egress to provider API hosts is unnecessary and blocked where the lane allows it (§9.4 D6).

## 3. Inputs

| Input | Source |
|---|---|
| Make target catalogue, tiers V/E/O, execution model (`ci` image, `SC_IN_CONTAINER`) | plan 01 §7.1, §8.1 |
| `ci` image digest | plan 02 (`toolbox/image.lock`) |
| `.strange-company/validation.yaml` (RED/GREEN commands) | plan 22, plan 01 §7.3 |
| Woodpecker trust model, lanes, verify workflow shape | plan 20, ADR-0301, ADR-0302 |
| Mirrored refs and the GitHub repository settings | plan 17 |
| Release pipeline | plan 23 |
| Fake harness scenarios, fake provider fixtures | plans 28, 53 |

## 4. Outputs

1. CI definition files (all protected paths):
   - `.github/workflows/verify.yaml`: interim CI, jobs `mode`, `verify`, `tdd-discipline`;
   - `.github/workflows/e2e.yaml`: off-cluster lane, one job per tier-E target, plus `e2e-gate`;
   - `.github/workflows/nightly-interim.yaml`: Phase 1 only;
   - `.github/workflows/publish.yaml` (content owned by plan 23 §14.9): runs only on the **upstream project's** GitHub repository, on protected `v*` tags (`push: tags: ['v*']`) and on `release: published`. It builds and publishes to GHCR the images an installation needs before its own P11 (toolbox, `ci`, `sc-ci-config`, `sc-release`, `restore-tester`), and signs every published digest with **cosign keyless** (GitHub Actions OIDC, Fulcio and Rekor). It is the source whose identity (`security/cosign/trusted-keys.yaml` entry `upstream-github-publish`) V-26 (plan 50) and 23-AC-23 verify against. It is the only workflow with write permissions (`id-token: write`, `packages: write`); it is skipped on an installation's read-only mirror (`if: github.repository == '<upstream-owner>/strange-company'`), and it calls only catalogue targets (`make images`, `make toolbox`) plus the pinned cosign and login actions;
   - `.github/workflows/stage0-validate.yaml` (ADR-0021, CD-27): every PR and every push to `main`. Runs `python3 -I tools/stage0/validate.py` (= `make stage0-validate`) and checks that `PLAN.md` generation is deterministic. Permissions are read-only, it uses no secrets, and its actions are pinned by SHA. It is required on the PR that merges Stage 0 and on every later PR that touches `plans/`, `contracts/`, `procedures/`, `adr/`, `PLAN.md` or `STATUS.md`;
   - `.woodpecker/ci.yaml` and `.woodpecker/nightly.yaml` (plan 20 serves them; plan 23 owns `release.yaml`; plan 20/22 own `verify.yaml`).
2. The `tddDiscipline` section of `policy/ci.yaml` and the checker `sc-lint tdd-discipline` behind `make tdd-discipline-check` (§8.3).
3. `policy/test-quarantine.yaml` (kind `TestQuarantine`) and `make quarantine-check` (§9.3).
4. The event × target matrix (§8.1) and the required-check sets (§8.2), which plans 16, 17 and 55 consume.
5. The release-gate rule over the off-cluster lane (§8.5), which plan 23 implements.
6. Alert IDs `SCCINightlyFailing`, `SCCIOffclusterLaneFailing`, `SCCIOffclusterLaneStale` (§21).

## 5. Dependencies

**Hard (DAG):** F01 → F02 (contract tooling). P08 requires P06 (plan 20). P12 requires P08 and P11.

**Soft:**
- GitHub Actions availability (Phase 1, and the e2e lane afterwards);
- Woodpecker (P08+);
- plan 17's probe for off-cluster lane status;
- plan 23's release pipeline.

**Interfaces assumed from other plans (they must honor these):**
- **Plan 01** adds these targets to its catalogue (proposed here, not yet listed there):
  - `quarantine-check` (V);
  - `test-flaky-scan` (nightly);
  - `fuzz-short` (nightly);
  - `e2e-woodpecker` (E, plan 20);
  - `e2e-mirror` (E, plan 17 names it);
  - `repo-size-check` (V, plan 17 names it).

  Plan 01 also folds `woodpecker-cli lint .woodpecker/`, `sc-ci-config lint --verify` and `actionlint` into `lint`. `tdd-discipline-check` applies to human PRs in **all** phases, not "Phase 1 only" (Q-52-02).
- **Plan 02:** the `ci` image contains:
  - the Go toolchain;
  - `setup-envtest` assets (kube-apiserver and etcd matching the pinned Kubernetes minor);
  - `helm`, the `helm-unittest` plugin, `kubeconform`, `promtool`, `woodpecker-cli`, `actionlint`, `k3d` and `kubectl`.

  It runs as non-root (uid 65532) and sets `SC_IN_CONTAINER=1`, `TZ=UTC` and `LC_ALL=C.UTF-8`.
- **Plan 17:**
  - `SC-GITHUB-PROBE` gains `Actions: read`;
  - the platform probe exports `sc_ci_offcluster_lane_last_success_timestamp_seconds` and `sc_ci_offcluster_lane_last_run_conclusion{conclusion}` for workflow `e2e` on `main` (§21).
- **Plan 23:** its release pipeline calls `sc-release gate --offcluster` implementing §8.5.
- **Plan 55:**
  - required contexts per repository as in §8.2;
  - `strange-company/tdd-provenance` is `failure` for any PR from an `agent/*` branch that is not bound to a task. That way the `agent/` exemption of §8.3 rule T0 cannot be used by a human to skip the discipline check.

## 6. Owned state

| State | Location | Writers |
|---|---|---|
| CI definitions | `.github/workflows/*.yaml`, `.woodpecker/ci.yaml`, `.woodpecker/nightly.yaml` | humans by PR (protected paths) |
| TDD-discipline configuration | `policy/ci.yaml` `spec.tddDiscipline` | humans by PR |
| Test quarantine list | `policy/test-quarantine.yaml` | humans by PR (protected; agents cannot quarantine) |

CI results are not durable state owned here. They live in GitHub check runs (interim and e2e lane), Woodpecker (`woodpecker-pg`, 90 days) and, for RED/GREEN, the Runner's evidence store (plan 20/22).

## 7. External interfaces

- **Status contexts** (consumed by branch protection, plans 16/17/55): see §8.2.
- **Report files:** every target writes `out/<target>/junit.xml` where applicable (plan 01). `make verify` writes `out/verify/summary.json`. `make tdd-discipline-check` writes `out/tdd-discipline/report.json`:

  ```json
  {"result":"pass","classification":"behavior-changing","merge_base":"<sha>","red_commits":["<sha>"],
   "red_target":"test","red_vars":{"GO_TEST_PKGS":"./src/internal/x/..."},
   "baseline":{"exit_code":0,"testcases":42},"red":{"exit_code":1,"failed":["TestX/AC-1"],"build_ok":true},
   "head":{"exit_code":0,"red_tests_passed":true},"exemption":null,"warnings":[]}
  ```
- **Release-gate rule** (plan 23 implements): §8.5.
- **Off-cluster lane result:** GitHub check runs of workflow `e2e` (job names = target names, plus `e2e-gate`).

## 8. Internal interfaces

### 8.1 Test pyramid and the event × target matrix

**Pyramid** (lower levels run more often and gate earlier):

| Level | What | Dependencies allowed | Targets | Tier |
|---|---|---|---|---|
| L1 Unit | Go unit tests, golden files, property and fuzz corpora replay, **envtest** admission/RBAC/PSA tests | in-process only; envtest kube-apiserver and etcd binaries from the `ci` image; no network | `test` (plus nightly `test-flaky-scan`, `fuzz-short`) | V |
| L2 Contract and static | schemas, examples and invalid examples, policy files, lock files, render and offline validation of GitOps and chart, lint, alert and metric catalogues | files only | `contracts`, `policy-check`, `locks-check`, `layout-check`, `secrets-scan`, `secrets-lint`, `lint`, `fmt-check`, `gitops-validate`, `gitops-flux-regen-check`, `gitops-diff`, `chart-test`, `tofu-validate`, `ansible-lint`, `test-ansible-k3s`, `profiles-check`, `config-docs-check`, `check-pricing`, `test-pricing-vectors`, `test-ledger-queries`, `lint-alerts`, `lint-metrics`, `toolbox-check`, `check-provider-evidence`, `quarantine-check`, `repo-size-check`, `commit-lint`, `build` | V |
| L3 Integration with fakes | multi-component flows with in-process fakes: fake Forgejo API, fake Woodpecker API (recorded JSON), fake S3 (in-process), **fake provider upstream** (plan 53 recorded fixtures), fake harness scenarios (plan 28) | in-process | part of `test` (build tag-free; packages `…/integration_test.go`) | V |
| L3b Container-backed integration | real PostgreSQL (CNPG major), SeaweedFS `s3-emulator` (ADR-0205, CD-12) | container runtime | `test-integration` | E |
| L4 k3d e2e | real Kubernetes with real CNI and admission | container runtime (privileged k3d) | `e2e-k3d`, `e2e-handoff`, `e2e-cnpg`, `e2e-forgejo`, `e2e-authentik`, `e2e-mirror`, `e2e-woodpecker`, `e2e-bootstrap-fake`, `e2e-c10` (from C10 on; §8.5a) | E |
| L5 Lab-VM bootstrap e2e | `existing` provider on QEMU VMs through configure and seed: `lab-small` (1 VM) and `lab-ha` (3 VMs, H01 rehearsal) (plan 05, ADR-0105) | `/dev/kvm`; `lab-ha` also ≥ 8 vCPU / 24 GiB | `e2e-lab` | E |
| L6 Real-infrastructure e2e (opt-in, paid) | fresh VPS install and destroy; real S3 conformance; GitHub live settings | operator credentials | `e2e-fresh-vps`, `e2e-s3-conformance`, `e2e-github-live`, `drill-cnpg-recovery-overlay`, `tofu-fixtures` | O |
| L7 Real-model smoke (opt-in, paid) | provider wire-format smoke | model API keys | `smoke-providers`, `record-provider-fixtures` | O |
| Live gates | DAG gate ACs on the real cluster, including C10 fake end-to-end TDD flows | the installation | gate scripts in each plan | — |

**Where and when.** "GH" = GitHub Actions. "WP" = Woodpecker. "OCL" = off-cluster lane (GH `e2e.yaml`, ADR-0303).

| Targets | Phase 1 (until P08): PR (GH) | Phase 1: push `main` (GH) | P08+: PR (WP `ci`) | P08+: push `main`/`release/*` (WP `ci`) | P08+: nightly 02:00 UTC (WP `nightly`) | OCL: daily 03:17 UTC if `main` changed; tags; dispatch | Release tag (WP `release`, plan 23) | Opt-in |
|---|---|---|---|---|---|---|---|---|
| `verify` (all tier-V, fixed order) | required | yes | required | yes | — | — | prerequisite: the tag commit has a successful WP `push/ci` | — |
| `tdd-discipline-check` | required (human PRs) | — | required (human PRs, inside `ci`) | — | — | — | — | — |
| `test-flaky-scan`, `fuzz-short`, `locks-verify-online`, `quarantine-check --wall-clock` | — | — (nightly-interim) | — | — | yes | — | — | — |
| `test-integration`, `e2e-k3d`, `e2e-handoff`, `e2e-cnpg`, `e2e-forgejo`, `e2e-authentik`, `e2e-mirror`, `e2e-woodpecker`, `e2e-bootstrap-fake`, `toolbox` | required via `e2e-gate` (path-filtered, §8.4) | yes (all) | — | — | — | yes (all) | gate: OCL success on the tag commit (§8.5) | — |
| `e2e-lab` | when `infra/ansible/**`, `infra/lab/**`, `src/internal/bootstrap/**` change | weekly | — | — | — | weekly (Sunday) and tags | gate (§8.5) | — |
| RED/GREEN verification (`verify.yaml`, plan 20) | n/a (no Runner) | — | — | — | — | — | — | — |
| L6, L7 | — | — | — | — | — | — | — | operator |

RED/GREEN verification pipelines are Runner-triggered (`manual` event, plan 20 §14.5) from C04 on. They run the base revision's `.strange-company/validation.yaml` command (plan 22) and are independent of this matrix.

### 8.2 Required status checks

| Repository / period | Branch protection | Required checks |
|---|---|---|
| GitHub `strange-company`, Phase 1 → P06 | `main`: PR required, 1 approval, no force push | `verify`, `tdd-discipline`, `e2e-gate` (GH job names) |
| Forgejo `strange-company`, P05 → P12 (protection stage `bootstrap`, plan 16) | push-whitelist `sc-admin`, 1 approval | none. Only bootstrap commits land in this window (plan 20 §18) |
| Forgejo `strange-company`, P12 → C09 (stage `steady`) | as plan 16 | `ci/woodpecker/pr/ci` |
| Forgejo `strange-company`, C09+ | as plan 16/55 | `ci/woodpecker/pr/ci`, `strange-company/tdd-provenance`, `strange-company/review` (ADR-0019) |
| Forgejo `projects/*` with `.woodpecker/ci.yaml` | per plan 55 template | `ci/woodpecker/pr/ci`, `strange-company/tdd-provenance`, `strange-company/review` |
| Forgejo `projects/*` without CI | per plan 55 | Runner contexts only. Plan 22 decides whether test-first tasks are allowed (they need `validation.yaml`) |
| GitHub mirror, P06+ | read-only (plan 17) | none. GH workflows are advisory, except the OCL as release evidence |

### 8.3 Bootstrap discipline: `make tdd-discipline-check` (`sc-lint tdd-discipline`)

Applies to every PR into a protected branch of `strange-company` (and of `projects/*` that adopt it) whose source branch is not exempt by T0. Inputs:
- `B = git merge-base origin/<target> HEAD`;
- commits `C1..Cn = git rev-list --reverse --no-merges B..HEAD`.

A merge commit in `B..HEAD` fails with `TDD_MERGE_COMMIT`. PRs are linear; rebase instead.

| Rule | Check | Failure code |
|---|---|---|
| T0 | The source branch matches `tddDiscipline.excludedBranches` → `not-applicable`, under these conditions only: `agent/*` (covered by the Runner's provenance; plan 55 posts `tdd-provenance = failure` for an unbound agent branch); `renovate/*` **only if** the PR author is `sc-renovate` **and** every changed file matches the `dependency-update` class paths (TDD-16). Any other `renovate/*` PR goes through T1–T8 | `TDD_EXCLUSION_INVALID` (a `renovate/*` PR not meeting both conditions is evaluated normally, and the report records the refused exclusion) |
| T1 | Classification: if no file changed in `B..HEAD` matches `behaviorPaths`, or every changed behavior-path file also matches `testPaths` → `not-applicable` | — |
| T2 | Exemption: the PR head commit has trailer `TDD-Exempt: <class>: <reason ≥ 20 chars>` with `<class>` in `exemptionClasses`, and the class's path constraint holds (below) → `exempt` (recorded in the report; the human merger sees it) | `TDD_EXEMPTION_INVALID` |
| T3 | There is ≥ 1 RED commit R: subject matches `^test(\([^)]+\))?: ` and every changed file matches `testPaths`. No commit before the first R changes a behavior-path file outside `testPaths` | `TDD_NO_RED_COMMIT`, `TDD_IMPLEMENTATION_BEFORE_RED` |
| T4 | Red target: R's trailer `TDD-Red-Target: <target> [GO_TEST_PKGS=… ] [GO_TEST_RUN=…]`, with `<target>` in `redTargets`. Default `test` with `GO_TEST_PKGS` = the packages containing `_test.go` files changed in R | `TDD_RED_TARGET_INVALID` |
| T5 | Baseline: run the red target at B in a clean `git worktree` and collect the set of JUnit testcase IDs `S_B` (`classname.name`). A package absent at B contributes nothing | `TDD_BASELINE_FAILED` (the target fails at B: the PR is based on a broken `main`) |
| T6 | RED run at R (clean worktree):
- exit code ≠ 0;
- JUnit present with ≥ 1 testcase;
- the build succeeded: no `go test -json` event with `Action: "fail"` on a package without a `Test` field caused by `[build failed]` or `[setup failed]`, and no JUnit `<error type="build">`;
- the failed set `F_R` is non-empty;
- every `f ∈ F_R` is either ∉ `S_B` (new) or a test whose function body changed in R (Go AST diff; for non-Go targets only "new" counts) | `TDD_RED_PASSED` (exit 0: the behavior already exists; for humans that is the `ALREADY_SATISFIED` analogue), `TDD_RED_BUILD_FAILURE`, `TDD_RED_PREEXISTING_FAILURE` |
| T7 | HEAD: the same target exits 0, and every `f ∈ F_R` is present in HEAD's JUnit with result `passed` (not skipped) | `TDD_RED_TESTS_NOT_GREEN` (catches deleted or skipped acceptance tests) |
| T8 | Warning only: a test file changed in R is modified again after R → `TDD_RED_TEST_MODIFIED` in `warnings[]`. The human reviewer must look (SPEC §53 analogue) | — |

Configuration (`policy/ci.yaml`):

```yaml
spec:
  tddDiscipline:
    excludedBranches: ["agent/*", "renovate/*"]
    behaviorPaths: ["src/**", "charts/**", "gitops/platform/**", "gitops/layers/**", "gitops/profiles/**", "infra/**",
                    "toolbox/**", "images/**", "harnesses/**", "policy/**", "contracts/**", "Makefile", "mk/**", "bootstrap"]
    testPaths: ["**/*_test.go", "**/testdata/**", "test/**", "charts/*/tests/**", "src/internal/contracts/testdata/**"]
    redTargets: [test, contracts, policy-check, gitops-validate, chart-test, test-ansible-k3s, tofu-validate,
                 lint-alerts, lint-metrics, profiles-check, locks-check, secrets-lint, layout-check]
    exemptionClasses:
      docs-only:                {paths: ["**/*.md", "adr/**", "plans/**", "procedures/**"]}
      ci-config:                {paths: [".github/**", ".woodpecker/**", "policy/ci.yaml"]}
      dependency-update:        {paths: ["src/go.mod", "src/go.sum", "versions/**", "toolbox/*.lock*", "toolbox/requirements.txt", "infra/tofu/**/.terraform.lock.hcl"]}
      generated-regeneration:   {paths: ["gitops/clusters/*/flux-system/gotk-components.yaml", "bootstrap.env.example"]}
      revert:                   {subjectRegex: '^Revert "'}
      contract-definition:      {paths: ["contracts/**"], requires: "x-invalid-examples added or changed"}
      policy-tuning:            {paths: ["policy/**"], requires: "policy-check passes; values only"}
```

Budget: T5 and T6 run only the selected target with scoped packages. The added CI time is ≤ 2 × the scoped `go test` time, typically < 3 min.

The check **re-executes** RED instead of looking up a recorded CI run on the test commit (plan 01's wording). GitHub and Woodpecker only run the PR head, so a recorded per-commit run would require pushing R separately. Re-execution is deterministic and self-contained.

### 8.4 Interim and off-cluster workflows (GitHub Actions)

- Every `uses:` is pinned to a 40-hex commit SHA (`locks-check`).
- Runner label `ubuntu-24.04` (the runner image version is recorded in the job summary).
- `permissions: {contents: read}`. No secrets referenced (`lint` fails on `secrets.` in workflows other than `GITHUB_TOKEN`). The single exception is `publish.yaml` (§4 item 1): `permissions: {contents: read, id-token: write, packages: write}`, upstream repository only, keyless signing, no stored signing key. `concurrency: {group: <workflow>-<ref>, cancel-in-progress: true}` for PRs only.
- Checkout uses `fetch-depth: 0`.
- `.cache/` is cached by `actions/cache` (pinned SHA), keyed by `src/go.sum`, `versions/platform.lock.yaml` and `infra/tofu/**/.terraform.lock.hcl`.
- **`mode` job:** it outputs `primary=forgejo` when the commit contains any `gitops/clusters/<c>/config/forge-init.yaml` with `<c>` not matching `ci-*`, otherwise `primary=github`.
  - `verify` and `tdd-discipline` run only when `primary == github`. After control transfer they no longer spend minutes on mirror pushes (ADR-0018: advisory).
  - `e2e.yaml` runs in both modes.
- **`e2e-gate`:** in Phase 1, `e2e.yaml` runs on PRs. A path filter (`dorny/paths-filter` pinned, or a `git diff --name-only` step) selects the tier-E jobs whose inputs changed. `e2e-gate` always runs and succeeds iff every selected job succeeded. This avoids required checks stuck in "expected".

  | Job | Paths that select it on PRs |
  |---|---|
  | `e2e-k3d` | `charts/**`, `gitops/**`, `src/**`, `policy/**` |
  | `e2e-handoff` | `gitops/profiles/*/seed/**`, `gitops/layers/**`, `src/internal/bootstrap/{seed,handoff}/**` (plan 19 §19) |
  | `e2e-cnpg` | `gitops/platform/cnpg*/**`, `gitops/platform/*-db/**` |
  | `e2e-forgejo` | `gitops/platform/forgejo/**`, `forge/**`, `src/internal/bootstrap/forge/**` |
  | `e2e-authentik` | `gitops/platform/authentik/**` |
  | `e2e-mirror` | `forge/**`, `src/internal/bootstrap/mirror/**` |
  | `e2e-woodpecker` | `gitops/platform/woodpecker/**`, `src/cmd/sc-ci-config/**`, `.woodpecker/**`, `policy/ci.yaml` |
  | `e2e-bootstrap-fake` | `src/internal/bootstrap/**`, `infra/**` |
  | `test-integration` | `src/**` |
  | `toolbox` | `toolbox/**` |
  | `e2e-c10` | not run on PRs (C10 exists only after Phase 1); on `main` pushes touching `src/**`, `charts/strange-company/**`, `policy/harnesses.yaml`, `images/**`, `harnesses/**`, and on tags |
  | `e2e-lab` | as in §8.1 |

  On push, schedule and tags every job runs.
- **Schedule:** daily `17 3 * * *`. The first step compares `main`'s SHA with the head SHA of the last successful scheduled run (`gh run list --workflow e2e --status success --limit 1`, using the `GITHUB_TOKEN` read scope). If equal, the run exits successfully without running the jobs (`skipped: unchanged`). `e2e-lab` runs only on Sundays and on tags.

- **Lab runners (plan 05):**
  - `e2e-lab LAB_SUBSTRATE=lab-seed` (one VM: 4 vCPU / 8 GiB / 40 GiB thin qcow2, plan 05) runs the plan 19 seed with real Longhorn, CNPG and Forgejo: the positive A-series (19-AC-19, required for P04) and the fault cases `LAB_SCENARIO=handoff-faults` N4-lab, N5-lab and N6-lab (19-AC-21, required for P06; CD-31). It runs in the off-cluster lane (ADR-0303) on a standard KVM-capable hosted runner (4 vCPU / 16 GiB; the free-disk headroom on the runner for Longhorn's backing files is verified at F10). It runs on `main` pushes touching the plan 19 §19 paths, weekly, and on release tags, never on the production node. The §14.5 operator-run fallback applies when KVM is unavailable.
  - `e2e-lab LAB_SUBSTRATE=lab-small` runs on a **KVM-capable** runner. The standard hosted `ubuntu-24.04` runner (4 vCPU / 16 GiB) qualifies when `/dev/kvm` is exposed; the job adds the documented udev rule (`KERNEL=="kvm", GROUP="kvm", MODE="0666"`) and fails with `LAB_NO_KVM` otherwise (verify at F10). The container is started with `./bootstrap exec --image ci --kvm --network host`.
  - `e2e-lab LAB_SUBSTRATE=lab-ha` needs a runner with **≥ 8 vCPU / 24 GiB** and KVM. That is a GitHub larger runner (paid per minute) or an operator-registered self-hosted lab host. Its label comes from the repository variable `SC_LAB_HA_RUNNER`. If the variable is unset, the job is skipped with `skipped: no-lab-ha-runner` and the §14.5 operator-run fallback applies. `lab-ha` runs weekly and on release tags; it gates H01 (plan 47), not P-phase releases.

### 8.5 Release gate over the off-cluster lane (rule; plan 23 implements)

For a release tag at commit `T`, the release pipeline (Woodpecker, protected mode, build lane) must verify all of the following before publishing:
1. Woodpecker `ci/woodpecker/push/ci` is `success` on `T`, or on the `main`/`release/*` commit that `T` points to.
2. A GitHub Actions run of workflow `e2e` with `head_sha == T` exists with `conclusion == success`. Every tier-E job ran (none `skipped`, except the `lab-ha` variant of `e2e-lab`, which gates H01 only) on a push, tag or dispatch event, not a schedule-skip. The pipeline polls `GET /repos/{o}/{r}/actions/runs?head_sha=<T>` every 60 s for up to `CIPolicy.spec.offclusterLane.maxWaitMinutes` (90), using `SC-GITHUB-PROBE` (read-only).
3. The run's `run_attempt` is reported in the release notes. A success on attempt > 1 is accepted but flagged `rerun: true` (a human re-ran it; §16).

Failure codes: `OFFCLUSTER_LANE_MISSING`, `OFFCLUSTER_LANE_FAILED[:<job>]`, `OFFCLUSTER_LANE_TIMEOUT`, `WOODPECKER_MAIN_NOT_GREEN`.

### 8.5a Upgrade-class checks from plan 48 (GC-07)

Plan 48 asked for two statuses, `strange-company/c10-regression` and `upgrade-freeze`. Decision (CD-22):

- **C10 regression is a release gate, not a merge gate.** The C10 fake end-to-end suite needs a cluster running the Strange Company images *built from the commit under test*. Woodpecker PR pipelines build no images (images are built only by the release lane, plan 23), and the privileged k3d environment exists only in the off-cluster lane, which never sees PR refs (plan 17 mirrors `main`, `release/*` and tags). A pre-merge C10 lane is therefore impossible without privileged CI on the production node (rejected by ADR-0303). The off-cluster lane gains job **`e2e-c10`** (target `make e2e-c10`, tier E, to be added to plan 01's catalogue): build the internal images locally at the commit (`make images`), load them into k3d, install the `strange-company` chart with the fake provider upstream and the fake harnesses, and run the plan 21/46 C10 suite (`sc-e2e run --suite c10`). It runs on every mirrored `main` push that touches the plan 48 sc-class paths (`src/**`, `charts/strange-company/**`, `policy/harnesses.yaml`, `images/**`, `harnesses/**`) and on every release tag. §8.5 item 2 includes it, so **no release is published unless C10 passed on that commit** (52-AC-22). Plan 48 §8.2 and 48-AC-7 must change "merge-blocking" to "release-blocking" (needs plan 48).
- **`upgrade-freeze` is not a status context (CD-22).** The freeze conditions of plan 48 are an operator-checked rule enforced by the `./bootstrap upgrade` preflight (plan 48 Q-48-04). No CI lane or required check implements them.


### 8.6 Where specific SPEC test families run

**SPEC §116 chart gates** (`charts/strange-company`, plan 54):

| §116 item | Target | Level / lane |
|---|---|---|
| helm lint, values schema validation, helm template, Kubernetes schema validation, unit tests (`helm unittest`) | `chart-test` | L2, every PR (GH then WP) |
| k3d installation, upgrade (from the chart at the previous `v*` tag to HEAD), uninstall, reinstall | `e2e-k3d` (`TestChartLifecycle`) | L4, OCL (Phase 1 PR-gating; then daily and the release gate) |

**SPEC §105 worker escape tests** (content: plans 37, 36, 34):

| Escape | Primary test | Additional |
|---|---|---|
| privileged, root, hostPath, SA token mount, arbitrary ServiceAccount, arbitrary Secrets, arbitrary images, exceeding resource profiles, entering infrastructure namespaces | envtest admission suite in `make test` (VAP + PSA + RBAC against a real kube-apiserver, no kubelet) | `e2e-k3d` (the same cases on k3s), live V-13 (plan 50) and C02 (plan 26/37) |
| bypassing network policy | `e2e-k3d` (real CNI; probe pods) | live V-12 |
| obtaining signing or deployment credentials | `secrets-lint` (the forbidden classes of `secret-classes.yaml` never appear in rendered `agent-jobs` manifests) + Runner Job builder golden tests (`make test`) | live C02 |

**SPEC §106 TDD tests** (content: plans 21, 22, 29):

| §106 item | Unit (L1/L3, `make test`) | Live (C10, fake harness) |
|---|---|---|
| implementer cannot start before `RED_VERIFIED` | state-machine library (F05) table tests; Runner gate tests (26-AC-15) | `fake-red-valid` then an implementer dispatch attempted early |
| broken tests are not valid RED | verifier library (F06) classification fixtures | `fake-red-broken-test` |
| already-passing test → investigation state | F06 + F05 | `fake-already-satisfied` |
| implementation cannot silently delete acceptance tests | F06 test-manifest comparison | `fake-test-tamper` |
| weakened acceptance tests detected where policy permits | F06 assertion-count/hash fixtures | `fake-test-tamper` (weakening variant) |
| valid RED → implementation → GREEN path | F05 path test | `fake-red-valid` + `fake-success` |
| `TEST_SPEC_CONFLICT` | F05 | `fake-test-spec-conflict` |
| TDD provenance survives retry | F05 + Runner DB tests | `fake-green-after-retry` |
| exemption requires approved policy | F04 policy loader fixtures | exemption request without a policy entry is rejected |

**SPEC §20 "No Bitnami dependencies" (CD-26, supersedes CD-20):** `gitops-validate` check `no-bitnami` (plan 18 §22 item 4) runs on every PR in both CIs: no Bitnami image in rendered manifests; no `HelmRepository`/`OCIRepository` or chart dependency pointing at a Bitnami repository or registry; a Bitnami subchart is allowed only when vendored inside an upstream chart package and of `type: library`.

**Contract tests (F02):** `make contracts` (L2) on every PR, with the Go validator and the Python crosscheck (plan 01 §8.3).

**TDD verification pipelines:** Runner-triggered `verify` workflow (plan 20 §8.4) running the base revision's validation command. RED for `strange-company`: `make test GO_TEST_PKGS=<manifest packages> JUNIT=…`. GREEN: `make verify` (plan 22 decides the exact commands). They are not part of the PR matrix. Their latency budget is plan 20 §14.5.

## 9. Configuration

### 9.1 Knobs

| Knob | Location | Default | Change by |
|---|---|---|---|
| Nightly schedule | `policy/ci.yaml` `crons` (plan 20) | `0 0 2 * * *` | PR |
| OCL schedule, path filters | `.github/workflows/e2e.yaml` | §8.4 | PR |
| TDD discipline | `policy/ci.yaml` `tddDiscipline` | §8.3 | PR |
| Quarantine | `policy/test-quarantine.yaml` | empty | PR |
| Flaky-scan repetitions | `test-flaky-scan` variable `FLAKY_COUNT` | 3 | PR |
| Release-gate wait | `CIPolicy.spec.offclusterLane.maxWaitMinutes` | 90 | PR |
| Test seed | `SC_TEST_SEED` (`ci` image env) | `20261013` | PR to plan 02 image |

### 9.2 CI step environment (both CI products; `ci` image defaults)
`SC_IN_CONTAINER=1`, `TZ=UTC`, `LC_ALL=C.UTF-8`, `SC_TEST_SEED=20261013`, `SC_FORBID_PAID=1`, `GOFLAGS=-mod=readonly`, `GOMAXPROCS=2` (Woodpecker ci lane), `GOCACHE`/`GOMODCACHE` under `.cache/` in the workspace (shared across the steps of one workflow).

### 9.3 Quarantine (`policy/test-quarantine.yaml`, kind `TestQuarantine`; contract `contracts/test-quarantine.schema.yaml`)

```yaml
apiVersion: strange.company/v1alpha1
kind: TestQuarantine
metadata: {name: strange-company}
spec:
  maxEntries: 10
  maxDays: 14
  entries:
    - id: "strange.company/sc/src/internal/runner/dispatch.TestDispatchRace"   # JUnit classname.name
      target: test
      reason: "race between informer resync and dispatcher tick; tracked"
      issue: "strange-company/strange-company#123"
      added: "2026-10-13"
      expires: "2026-10-27"
      owner: "<forgejo user>"
```

Rules (`make quarantine-check`, tier V):
- **Q1** At most `maxEntries` entries. `expires - added ≤ maxDays`. `expires` must be ≥ the **committer date of HEAD**; this is deterministic per commit (D9). With `--wall-clock` (nightly) the current date is used instead.
- **Q2** Never quarantinable (protected tests); entries matching these fail:
  - tests listed in any committed test manifest (plan 22 `test-manifest`);
  - names matching `^Test(Spec105|Spec106|AC[0-9]|Gate)`;
  - tests under `test/e2e/**` named by a DAG gate;
  - contract example and invalid-example checks;
  - every test of `src/internal/verify/**`, `src/internal/statemachine/**` and `src/internal/policy/**` (F04–F06).
- **Q3** The entry must reference an existing open issue (static format check only; the issue's existence is checked nightly).
- **Q4** Effect: a quarantined test still runs. Its failure is reported in `out/<target>/quarantine.json` and does not fail the target. Its pass is also recorded. Three consecutive nightly passes produce a `quarantine-release-candidate` line in the nightly summary.
- **Q5** `policy/test-quarantine.yaml` is a protected path (agents cannot quarantine their own failing tests). Plan 01 adds it to §13.2. Plan 22 adds it to the platform protected-path minimum.

### 9.4 Determinism rules and their enforcement

| ID | Rule | Enforcement |
|---|---|---|
| D1 | Pinned toolchain and images: CI runs only in the digest-pinned `ci` image; workflow images and actions are pinned | `locks-check`; `sc-ci-config` image rule (plan 20); GH `uses:` SHA |
| D2 | Pinned dependencies: `go.sum`, chart digests, tool checksums; no `latest` | `locks-check`, `lint` (`go mod verify`) |
| D3 | Registries: only those in `versions/platform.lock.yaml` and `CIPolicy.spec.imageRegistries` | `locks-check`; `sc-ci-config` |
| D4 | Offline tier V after cache warm-up | GH: `make verify` runs with `--network none` after `gitops-cache` and `go mod download` (01-AC-6). WP: NetworkPolicy denies everything but registries and Forgejo (plan 20 §13.3) |
| D5 | No real external services in L1–L3: the fake Forgejo, Woodpecker, S3, provider upstream and harnesses are in-process | `lint` forbids `http.DefaultClient` and non-loopback dial addresses in `_test.go` outside `test/e2e/**` (custom analyzer `sc-lint netcheck`) |
| D6 | No paid model calls: no provider keys exist in any CI lane; `SC_FORBID_PAID=1` makes the Gateway and harness adapters refuse non-fake upstream hosts | `secrets-lint` (no `SC-MODEL-API-KEYS` reference in CI files); unit test asserting refusal; GH `--network none`; WP egress cannot reach the Gateway (plan 20) |
| D7 | Fixed seeds: randomness through `testutil.Rand(t)` seeded from `SC_TEST_SEED` and the test name. `-shuffle` only in `test-flaky-scan`, which prints the seed | `lint` (`forbidigo`: `math/rand` global functions and `rand.Seed` outside `testutil`) |
| D8 | Time faking: all service code takes a `clock.Clock` | `lint` (`forbidigo`: `time.Now`, `time.Since`, `time.Sleep`, `time.After` outside `src/internal/clock` and `main` packages; tests use `clock.Fake`) |
| D9 | The only wall-clock input to a tier-V result is HEAD's committer date (quarantine expiry) | `quarantine-check` implementation; review |
| D10 | Golden files never updated in CI | target fails if `UPDATE_GOLDEN` is set while `CI=true` |
| D11 | No test-order dependence | `test-flaky-scan` (nightly shuffle × 3) |
| D12 | Fixed locale and timezone | `ci` image env (§9.2) |
| D13 | Reproducible builds | 01-AC-9 (`make build` twice, identical sha256) |

## 10. Secrets

| Class | Where in CI | Notes |
|---|---|---|
| none | GH `verify`, `tdd-discipline`, `e2e` jobs; WP `ci`, `nightly`, `verify` | `permissions: contents: read`; no Woodpecker secrets for these events (plan 20) |
| `SC-GITHUB-PROBE` (read-only, plan 17) | WP `release` gate only (plan 23, events `[tag]`, image filter `sc-release`) | for §8.5 |
| `SC-RELEASE-SIGNING`, `SC-REGISTRY-PUSH` | WP `release` only (plan 23) | |

No new secret classes. Plan 17 extends `SC-GITHUB-PROBE`'s permissions (`Actions: read`) and adds a Woodpecker secret instance of it for plan 23.

## 11. Storage

- GH: Actions cache ≤ 6 GiB (`.cache/`), artifacts (JUnit, `out/`) retained 14 days. Nothing sensitive is uploaded (`out/` excludes `.bootstrap/`).
- WP: workspace PVCs (plan 20, ADR-0305). Logs live in `woodpecker-pg` for 90 days.
- No CI artifact is authoritative. Release artifacts belong to plan 23. RED/GREEN evidence belongs to the Runner and S3.

## 12. Resource requirements

| Lane | Machine | Budget |
|---|---|---|
| GH `verify` | hosted `ubuntu-24.04` (4 vCPU / 16 GiB for public repositories; private repositories may get less, verify) | `make verify` ≤ 15 min warm, ≤ 25 min cold (plan 01 §12) |
| GH `e2e` | same | each job ≤ 30 min; the whole workflow ≤ 60 min (jobs run in parallel) |
| WP `ci` (self-hosting-minimum) | one step pod at a time: requests 500m/1Gi, memory limit 3Gi (2Gi on the constrained profile), `GOMAXPROCS=2` | PR `ci` p95 ≤ 25 min including queue wait when the queue is idle; hard limit 45 min (repository timeout) |
| WP `nightly` | same | ≤ 60 min |
| WP `verify` (RED/GREEN) | same | plan 20 §14.5 |

**Small-node performance levers, in the order they are applied when a P08+ measurement exceeds a budget** (each is a PR to this plan or plan 01):
1. Share the build cache within a workflow: `GOCACHE` lives on the workspace PVC (already the default).
2. Scope RED runs to manifest packages (plan 22; already the default).
3. Move `-race` from PR `test` to `main` pushes and nightly (plan 01 change). Race detection on the 4-vCPU node costs about 2–3× time and up to 5× memory.
4. Raise `CI_MAX_CONCURRENT` only at small-ha (plan 51).
5. A cluster-local Go module proxy cache (post-MVP).

**Throughput note:** with one CI job at a time (`strange.company/heavy-slot`, CD-05), one test-first task consumes RED (≈5–10 min), GREEN (≈15–20 min) and the PR `ci` run (≈15–20 min). The node therefore completes about one task per hour when CI-bound. SPEC §89 accepts "a slow worker". Plan 46 uses this for the threshold timing expectations.

## 13. Security boundary

- GH workflows run with read-only `GITHUB_TOKEN`, no secrets, and pinned actions. After P06 GitHub is a read-only mirror (plan 17), so a compromised workflow can neither push nor read platform secrets. Its only power is a false OCL result. That result gates releases but never merges, and plan 23 additionally requires Woodpecker's `ci` success.
- WP lanes follow plan 20's trust model. This plan adds no privilege.
- `tdd-discipline-check` executes PR code (tests at B, R and HEAD) inside the same untrusted step: no secrets, restricted pod.
- The quarantine list and CI definitions are protected paths, so agents cannot weaken CI (ADR-0008, plan 01 §13.2).

## 14. Happy path

### 14.1 Phase 1 (F01–F13; GitHub is origin and interim trusted CI)
1. A developer (human or external agent) pushes branch `feat/x` with commits `test(F05): …` (RED) and `feat(F05): …`, then opens a PR.
2. GH `verify` runs `make verify`. `tdd-discipline` runs T0–T8 and shows the RED failure set, which must be new tests. `e2e.yaml` runs the path-selected tier-E jobs, and `e2e-gate` aggregates them.
3. All three required checks pass, a human approves and merges. `main` push runs `verify` and all of `e2e`.

### 14.2 P08 cutover
`ci-init` W11 (plan 20) proves `make verify` green on Woodpecker for the current `main`. From the next commit, the `mode` job reports `primary=forgejo` (forge-init outputs exist since P05), GH `verify`/`tdd-discipline` stop running, and `e2e.yaml` keeps running on mirror pushes as the OCL.

### 14.3 Steady state (P12+)
PR on Forgejo → WP `ci` (`make verify` + `tdd-discipline-check` for human PRs) → `ci/woodpecker/pr/ci`. With Runner contexts from C09: merge → WP `push/ci` on `main` → mirror → OCL (daily-if-changed) → nightly WP. Tag → WP `release` (plan 23) → §8.5 gate → publish.

### 14.4 Agent task (C10+)
The Runner triggers RED and GREEN `verify` pipelines (plan 20). After GREEN it opens the PR (plan 55). The PR `ci` runs (tdd-discipline not applicable to `agent/*`; T0) and the Runner posts the provenance and review statuses.

### 14.5 `e2e-lab` fallback
If hosted runners lack `/dev/kvm` (verify at F10), `e2e-lab` runs on the operator's workstation: `./bootstrap exec --image ci -- make e2e-lab`. Its JUnit and `summary.json` are committed by PR under `evidence/e2e-lab/<sha>.json`. The release gate (§8.5) then accepts that record instead of the `e2e-lab` job when its `sha` equals the tag commit (plan 23 implements; `make check-e2e-lab-evidence` validates the record's format).

## 15. Failure modes

| Failure | Detection | Effect | Automated response | Procedure |
|---|---|---|---|---|
| Interim CI (GitHub Actions) outage in Phase 1 | GH status page; checks stuck | no merges | none | wait. Local `./bootstrap exec --image ci -- make verify` evidence is **not** a substitute for required checks |
| Woodpecker down (P08+) | plan 20 alerts | no merges, no verification | plan 20 | `woodpecker-failure.md` |
| Flaky test on a gating lane | intermittent failure; nightly `test-flaky-scan` reports it | PR blocked | none (no retries) | fix it or quarantine it by PR (§9.3) |
| Quarantine expired | `quarantine-check` fails on new commits; nightly `--wall-clock` fails → `SCCINightlyFailing` | PRs blocked until renewed or removed | none | PR to renew (≤ 14 days) or fix |
| OCL failing on `main` | `SCCIOffclusterLaneFailing` | releases blocked | none | investigate the failing job; fix forward by PR |
| OCL stale (no successful run for 8 days while `main` changed) | `SCCIOffclusterLaneStale` | releases risk timeout | none | check GitHub Actions minutes, the mirror (plan 17), workflow disablement |
| GH Actions minutes exhausted | jobs queued or blocked; stale alert | OCL stops; releases blocked | none | buy minutes, make the repository public, or use the optional in-house lane (ADR-0303 §5) |
| `tdd-discipline` false positive (non-Go RED) | PR blocked with code | — | none | use `TDD-Red-Target` or a valid exemption class; change `tddDiscipline` by PR if a class is missing |
| Nightly fails | `SCCINightlyFailing` | none immediate | none | triage by job; open an issue |
| Hosted runner image change breaks e2e | OCL failure on unchanged code | releases blocked | none | pin to the previous runner label (`ubuntu-24.04` is already explicit) and fix |

## 16. Retry behavior

- **Gating lanes (GH `verify`/`tdd-discipline`, WP `ci`/`verify`):** no automatic retries at any level. No `retry:` in Woodpecker, no retry actions in GH, no `--rerun-fails`, `go test -count=1`. A human may re-run a failed GH or WP run from the UI. The run's attempt number is visible (GH `run_attempt`, WP restart lineage). For acceptance tests (Q2 set), a re-run that passes is still a flake signal: the nightly `test-flaky-scan` must be clean before the next release (plan 23 gate item, recorded in the release notes).
- **Tier-E harnesses** may retry **environment provisioning** once (k3d cluster create, image pre-pull), with 30 s backoff, and log `env-retry: 1`. Assertions are never retried.
- **Cache warm-up** (`gitops-cache`, `go mod download`): 3 attempts (plan 01).
- **RED/GREEN verification:** retry budgets by failure class are plan 22's. Infrastructure failures may re-trigger, merit failures may not (lesson L-4).

## 17. Idempotency behavior

- Every target is re-runnable with identical results (52-R-2; 52-AC-5 proves it for `verify`).
- `tdd-discipline-check` uses throwaway worktrees under `out/tdd-discipline/wt-*`, removed at exit. Re-running gives the same report modulo durations.
- The scheduled OCL short-circuits when `main` is unchanged (§8.4).

## 18. Bootstrap behavior

- **F01** lands `.github/workflows/verify.yaml` and the first RED run (plan 01 §18: a failing `layout-check` fixture commit before the skeleton). The operator configures GitHub branch protection with the required checks of §8.2 row 1 (procedure step in `fresh-install.md`, plan 50: `gh api -X PUT repos/<owner>/strange-company/branches/main/protection` with the `required_status_checks.contexts` list).
- **F02** adds `make contracts` to `verify`. From then on, every contract change is validated on PRs.
- **P05–P08:** see §8.2 rows 2–3 and plan 20 §18.
- **P12:** `sc-bootstrap forge finalize` (plan 16) installs `ci/woodpecker/pr/ci` as required.
- **Black start:** CI definitions come back with Git. Woodpecker follows plan 20 §18. GH workflows keep running on the mirror throughout (the OCL remains available during a Forgejo outage, which also helps recovery validation).

## 19. Upgrade behavior

- CI tool upgrades (`ci` image, actions SHAs, Woodpecker, `woodpecker-cli`) arrive as Renovate PRs (plan 23). They must pass the current lanes. A `ci` image bump runs `make verify` in both the old and the new image in the same PR (GH job matrix in Phase 1; after P08, a second `ci` workflow step `verify-next` driven by `CI_IMAGE_NEXT` in `.woodpecker/ci.yaml` for the duration of the PR). Results must match (52-R-2).
- Changes to the Go toolchain minor require `test-flaky-scan` to pass on the PR branch (run manually via WP `manual` on `main` after merge if PR-time is impractical; a failure blocks the next release).
- Changes to this plan's matrix are PRs to `.github/**`/`.woodpecker/**` (protected; human review).

## 20. Recovery behavior

- Lost CI history is not a recovery concern. Gates re-run from Git.
- If Woodpecker is unrecoverable for longer than the RTO, merges stop. The documented emergency path is **not** to fall back to GitHub Actions as merge-gating (the mirror is read-only and PR refs are not mirrored). Recover Woodpecker instead (`woodpecker-failure.md` §L).

## 21. Observability

- **Dashboards (plan 39, "CI"):**
  - WP pipeline durations by workflow (p50/p95, from `woodpecker_pipeline_time`, verify labels);
  - queue wait (`woodpecker_waiting_steps`);
  - nightly result;
  - OCL last success age;
  - quarantine size (`sc_ci_quarantine_entries`, exported by the platform probe reading `policy/test-quarantine.yaml` at the deployed revision, plan 17/15 probe host).
- **Alerts** (new IDs for plan 42; runbook `procedures/woodpecker-failure.md#ci-lanes`):

  | alertname | Source | for | severity |
  |---|---|---|---|
  | `SCCINightlyFailing` | probe metric `sc_ci_nightly_last_conclusion{conclusion="failure"} == 1`. The probe reads the Forgejo commit statuses with context `ci/woodpecker/cron/nightly` on the last 20 `main` commits (sc-admin read token, plan 16 `platform-probes`) | 0m (fires on the first failed nightly) | warning |
  | `SCCIOffclusterLaneFailing` | `sc_ci_offcluster_lane_last_run_conclusion{conclusion="failure"} == 1` (plan 17 probe) | 0m | warning |
  | `SCCIOffclusterLaneStale` | `time() - sc_ci_offcluster_lane_last_success_timestamp_seconds > 8*86400` | 1h | warning |
- **Reports:** `out/verify/summary.json`, `out/tdd-discipline/report.json` and the JUnit files are uploaded as GH artifacts and printed into WP logs (bounded to 64 KiB per file).

## 22. Testing strategy

- **Checker unit tests** (`make test`): `sc-lint tdd-discipline` against synthetic Git repositories built in `t.TempDir()` with a tiny Go module. Each T-rule has positive and negative fixtures (52-AC-4). `quarantine-check` has fixtures for Q1–Q3.
- **`make dag-check` (GC-01, proposed tier-V target; plan 01 adds the name):** parses every plan's `### GATE-<node>` headings and checks that (a) `plans/dag.yaml` lists each such plan under that node, and (b) no AC references a component whose introducing node (a `components:` map the lead adds to `dag.yaml`) is not upstream of the gate's node. It runs on every PR that touches `plans/**` or `plans/dag.yaml`.
- **Workflow lint:** `actionlint` and `woodpecker-cli lint` in `lint`. A static check asserts that every `run:`/`commands:` line in CI files is `make <catalogue target> [VAR=…]` or a whitelisted plumbing command (`git`, `gh run list`, `echo`) (52-R-1).
- **Determinism tests:** 52-AC-5 (repeat run), 01-AC-6 (offline), D7/D8 lint fixtures.
- **Lane tests:** the P08 cross-CI parity check (52-AC-12).
- All of this needs no paid model APIs.

## 23. Acceptance criteria

### GATE-F01

- [ ] **52-AC-1** `.github/workflows/verify.yaml` exists with jobs `mode`, `verify` and `tdd-discipline`. `make locks-check` passes (every `uses:` is a 40-hex SHA). `make lint` passes `actionlint`. `grep -c 'secrets\.' .github/workflows/*.yaml` = 0 except `secrets.GITHUB_TOKEN`. Each job declares `permissions: {contents: read}`, except the jobs of `publish.yaml`, which declare exactly `{contents: read, id-token: write, packages: write}` and the upstream-repository `if:` guard.
- [ ] **52-AC-2** GitHub branch protection: `gh api repos/<owner>/strange-company/branches/main/protection --jq '.required_status_checks.contexts'` contains `verify`, `tdd-discipline` and `e2e-gate`.
- [ ] **52-AC-3** The F01 PR itself passes `tdd-discipline` with a RED commit (`test(F01): …`, which adds the `layout-check` fixture tests). The check's `report.json` shows `red.failed` non-empty and `red.build_ok: true`.
- [ ] **52-AC-4** `go test ./src/internal/lint/tdd/... -run TestDiscipline` passes, covering one synthetic repository per outcome:
  - not-applicable (docs only);
  - valid exemption;
  - invalid exemption class;
  - no RED commit;
  - implementation before RED;
  - RED that fails to compile (`TDD_RED_BUILD_FAILURE`);
  - RED failing only on a pre-existing test (`TDD_RED_PREEXISTING_FAILURE`);
  - RED that passes (`TDD_RED_PASSED`);
  - RED tests deleted at HEAD (`TDD_RED_TESTS_NOT_GREEN`);
  - RED tests skipped at HEAD (`TDD_RED_TESTS_NOT_GREEN`);
  - merge commit (`TDD_MERGE_COMMIT`);
  - `renovate/feature` branch authored by a human that changes `src/` without a RED commit (`TDD_NO_RED_COMMIT`, with `TDD_EXCLUSION_INVALID` recorded);
  - `renovate/go-x` authored by `sc-renovate` changing a file outside `dependency-update` paths (evaluated normally);
  - valid case.
  Each prints its exact failure code.
- [ ] **52-AC-5** Repeatability: two consecutive `./bootstrap exec --image ci -- make verify` runs on the same clean checkout produce `out/verify/summary.json` files that are identical after deleting `duration_ms` and timestamps (`jq 'del(..|.duration_ms?, .started_at?)'` + `diff`).
- [ ] **52-AC-6** D7/D8 enforcement: `make lint` fails on the fixtures `src/internal/lint/testdata/forbidigo/{time_now.go,rand_global.go,dial_external_test.go}` with the analyzer names in the output, and passes on the tree.
- [ ] **52-AC-7** `make quarantine-check` passes on the empty list. It fails on fixtures with 11 entries, a 15-day entry, an entry expired relative to the HEAD committer date, and an entry quarantining `TestSpec106ImplementerBlockedBeforeRed`. Each prints its rule ID (Q1/Q2).
- [ ] **52-AC-8** No paid calls (D6): `make verify` passes with `SC_FORBID_PAID=1` and `--network none`. `go test ./src/internal/gateway/... -run TestForbidPaidRefusesRealHosts` passes (the Gateway refuses `api.anthropic.com` and `api.openai.com` upstream URLs when `SC_FORBID_PAID=1`).

### GATE-F02

- [ ] **52-AC-9** A demonstration PR (or replayed fixture commit) adding a schema whose `x-invalid-examples` entry validates makes GH `verify` fail at target `contracts`, and `out/contracts/report.json` names the entry. The fixed commit passes.
- [ ] **52-AC-10** `out/contracts/junit.xml` is uploaded as an artifact on every PR run and has one testcase per (schema, example) pair (count equals `report.json` rows).

### GATE-P08 (co-owned with plan 20)

- [ ] **52-AC-11** `.woodpecker/ci.yaml` and `.woodpecker/nightly.yaml` pass `woodpecker-cli lint` and `sc-ci-config lint` (part of `make lint`). Every step `commands` entry is a catalogue Make target invocation (the static check of §22).
- [ ] **52-AC-12** Cross-CI parity (ADR-0018): for the cutover commit, the WP `ci` run's `out/verify/summary.json` (printed in the step log between `::sc-summary-begin::`/`::sc-summary-end::`) and the GH `verify` run's artifact for the same SHA list the same targets with the same `result` values.
- [ ] **52-AC-13** Discipline on Woodpecker:
  - a human PR on Forgejo that changes `src/` without a RED commit gets `ci/woodpecker/pr/ci` = `failure`, and the step log contains `TDD_NO_RED_COMMIT`;
  - the same change rebased with a valid RED commit gets `success`;
  - a PR from branch `agent/sc-999` (no such task) gets `tdd-discipline` `not-applicable`. The test then asserts that `strange-company/tdd-provenance` is `failure` or absent and that the PR cannot be merged (that part is checked at C09 by plan 55; recorded here as a dependency).
- [ ] **52-AC-14** After cutover, the GH `verify` workflow run for the next mirrored `main` commit shows job `mode` output `primary=forgejo` and jobs `verify` and `tdd-discipline` as `skipped`.
- [ ] **52-AC-15** The OCL ran on the cutover commit (push of `main` to the mirror) with every tier-E job `success` (`gh run list --workflow e2e --commit <sha> --json conclusion`).
- [ ] **52-AC-16** The nightly cron exists (`$WP GET /api/repos/{id}/cron` lists `nightly`, schedule `0 0 2 * * *`, branch `main`). The first nightly run is `success`, and its log shows `test-flaky-scan` with the printed shuffle seed.
- [ ] **52-AC-17** Performance baseline: over the first 10 PR `ci` runs after cutover, measured from WP pipeline `started`/`finished` (queue excluded) and recorded in the P08 evidence, p95 ≤ 25 min and max ≤ 45 min (pass/fail on these two numbers only; GC-22). A failure blocks the P08 gate; the §12 levers are the documented remediation, after which this AC is re-run on 10 new PR runs.
- [ ] **52-AC-18** No CI pod in `ci-jobs` has an environment variable whose name matches `(?i)(anthropic|openai|deepseek|api_key|gateway_token)` (`kubectl get pods -o json` poll during 52-AC-12). No Woodpecker secret is available to `pull_request`, `push` or `manual` events (20-AC-20).

### GATE-P12 (contributions)

- [ ] **52-AC-19** Forgejo `strange-company` `main` protection requires `ci/woodpecker/pr/ci` (20-AC-36, 16-AC-32). GitHub `main` protection no longer lists `verify`/`tdd-discipline`/`e2e-gate` as required, and allows only the mirror identity to push (plan 17 settings check `mirror_only`).
- [ ] **52-AC-21** Prometheus `GET /api/v1/rules` contains `SCCINightlyFailing`, `SCCIOffclusterLaneFailing` and `SCCIOffclusterLaneStale`. `sc_ci_offcluster_lane_last_success_timestamp_seconds` is less than 2 days old.

### GATE-P11 (contribution; release gate rule of §8.5)

- [ ] **52-AC-20** Release gate negative test (needs plan 23's `sc-release`, delivered at P11, which CD-19 moves after control transfer; GC-01): `sc-release gate --offcluster --dry-run <sha>` (plan 23) against a `main` commit with no `e2e` run exits non-zero with `OFFCLUSTER_LANE_MISSING`. Against the cutover commit it exits 0.
- [ ] **52-AC-22** C10 regression as a release gate (GC-07): `sc-release gate --offcluster --dry-run <sha>` on a commit whose OCL run lacks a successful `e2e-c10` job exits non-zero with `OFFCLUSTER_LANE_FAILED:e2e-c10`.

## 24. Explicit non-goals

- Paid model calls in any default CI lane [SPEC §104]. L7 is operator opt-in only.
- Merge gating by GitHub Actions after P08 (ADR-0018, ADR-0303).
- Privileged or tier-E tests on the production cluster (ADR-0303).
- Automatic retries of gating jobs, auto-quarantine, or flake auto-detection that changes gate outcomes.
- A CI-specific test language or framework. All tests are Go, or tools invoked by Make targets.
- Cross-pipeline build caches on the cluster (MVP).

## 25. Open questions

| # | Question | Blocking? | Proposed resolution | Owner |
|---|---|---|---|---|
| Q-52-01 | ADR-0303 makes the GitHub-hosted off-cluster lane release-gating after P08. ADR-0018 calls GitHub Actions "advisory" after P08. | no | Treat ADR-0303 as a scoped refinement of ADR-0018 (merges stay Woodpecker-gated; releases additionally need tier-E evidence). The lead amends ADR-0018's consequence line at consolidation. | lead |
| Q-52-02 | Plan 01 lists `tdd-discipline-check` as "Phase 1 only". This plan applies it to all human PRs in all phases (SPEC §4.4 applies to humans too). | no | Apply in all phases. Plan 01 updates the tier note. | 01 |
| Q-52-03 | GitHub Actions minutes for a private repository (about 1,400 min/month worst case against 2,000 on the free plan). | no | Daily-if-changed schedule plus tags. `SCCIOffclusterLaneStale` surfaces exhaustion. Operators may make the repository public (it contains no plaintext secrets; plan 17 decides visibility) or buy minutes. | 17 / operator |
| Q-52-04 | `/dev/kvm` on hosted runners. | no | Verify at F10. The fallback is §14.5 (committed operator-run evidence). | 05 / 52 |
| Q-52-05 | `-race` on the 4-vCPU node may exceed the 25-min PR budget or the 3 GiB step memory. | no | Measure at P08 (52-AC-17). Lever 3 in §12. | 52 / 01 |
| Q-52-06 | New Make targets proposed here (`quarantine-check`, `test-flaky-scan`, `fuzz-short`, `e2e-woodpecker`, `e2e-mirror`, `repo-size-check`) and the `lint` additions. | no | Plan 01 adds them to the catalogue. `layout-check`'s `make-targets-known` rule lists them as planned until then. | 01 |
| Q-52-07 | The quarantine and `tddDiscipline` contracts were outside the original file list. | no | **Resolved at consolidation:** `contracts/test-quarantine.schema.yaml` and `contracts/ci-policy.schema.yaml` (`spec.tddDiscipline`) written and validated. | — |
| Q-52-08 | Human PRs need a `strange-company/tdd-provenance` status from C09 on (ADR-0019 requires it on every PR). | no | Plan 55: for non-`agent/*` PRs the Runner posts `success` when the PR's `ci/woodpecker/pr/ci` is `success` (the discipline check ran inside it). For `agent/*` PRs without a task it posts `failure`. | 55 |
| Q-52-09 | GC-07 offered a per-PR on-cluster C10 lane. | no | **Rejected:** PR pipelines build no images, and privileged k3d is never run on the production node (ADR-0303), so a pre-merge C10 run of the PR's own build is impossible. C10 regression is release-blocking instead (§8.5a); plan 48 adjusts. | 48 / 52 |
