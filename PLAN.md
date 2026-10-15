# PLAN — Implementation sequence

**Status:** Stage-0 output. Implementation has not started.
**Generated from:** `plans/dag.yaml` plus the `### GATE-<node>` sections of every subsystem plan. Regenerate after any gate changes; do not hand-edit the per-node tables.
**Authority:** subordinate to `SPEC.md` and `plans/00-master-plan.md`.

## How to execute a node

Every node is a bounded engineering task whose success criteria already exist (SPEC §132). For each node:

1. Read `SPEC.md`, `plans/00-master-plan.md`, `plans/reviews/consolidation-decisions.md`, then the node's owning plans and the contracts and ADRs they reference.
2. **Verification first (SPEC §1.2, §4.4).** Turn the node's gate criteria into executable tests or checks. Commit them **first** and show the tests failing for the right reason. Phase-1 nodes do this under the bootstrap discipline of plan 52; from P08 on, Woodpecker; from the threshold on, the Strange Company TDD workflow itself.
3. Implement until the gate's checks pass in trusted CI (interim GitHub Actions until P08, then Woodpecker; tier-E e2e lane per ADR-0303).
4. Record gate evidence (CI run links, check outputs) in `STATUS.md` under the node. A node is complete when its evidence passes, not when its files exist (SPEC §124).
5. A node may start only when every node in its `deps` has passed its gate. Nodes whose deps are satisfied may proceed in parallel.
6. Re-verify every `(verify)`-marked third-party assumption the node relies on. These are written as acceptance criteria in the owning plans. If an assumption is false, stop and amend the plan, recording it in `plans/spec-issues.md` if it touches SPEC.

Version-sensitive facts (chart values, API endpoints, CLI flags) are pinned and recorded in the version locks of plan 01 when the node is implemented.

## Milestones

- **BOOTSTRAP-DOD** = gate of node P12 (§130)
- **ORCHESTRATION-DOD** = gate of node C10 (§103, §106)
- **SELF-HOSTING-THRESHOLD** = gate of node R02 (§117, §118)
- **SMALL-HA** = gate of node H04 (§12, §119 PHASE 6)


## Phase 1 — Foundations (no paid infrastructure; interim CI on GitHub Actions, ADR-0018)

### F01 — repo-skeleton

- **Depends on:** —
- **Owning plans:** `plans/01-repository.md`, `plans/52-ci-strategy.md`
- **Gate `GATE-F01`:** 22 acceptance criteria
  - `plans/01-repository.md`: 01-AC-1, 01-AC-2, 01-AC-3, 01-AC-4, 01-AC-5, 01-AC-6, 01-AC-7, 01-AC-8, 01-AC-9, 01-AC-10, 01-AC-11, 01-AC-12, 01-AC-13, 01-AC-14
  - `plans/52-ci-strategy.md`: 52-AC-1, 52-AC-2, 52-AC-3, 52-AC-4, 52-AC-5, 52-AC-6, 52-AC-7, 52-AC-8

### F02 — contract-tooling

- **Depends on:** F01
- **Owning plans:** `plans/01-repository.md`, `plans/23-supply-chain.md`, `plans/52-ci-strategy.md`
- **Gate `GATE-F02`:** 8 acceptance criteria
  - `plans/01-repository.md`: 01-AC-20, 01-AC-21, 01-AC-22, 01-AC-23, 01-AC-24
  - `plans/23-supply-chain.md`: 23-AC-28
  - `plans/52-ci-strategy.md`: 52-AC-9, 52-AC-10

### F03 — toolbox-image

- **Depends on:** F01
- **Owning plans:** `plans/02-bootstrap-toolbox.md`
- **Gate `GATE-F03`:** 13 acceptance criteria
  - `plans/02-bootstrap-toolbox.md`: 02-AC-1, 02-AC-2, 02-AC-3, 02-AC-4, 02-AC-5, 02-AC-6, 02-AC-7, 02-AC-8, 02-AC-9, 02-AC-10, 02-AC-11, 02-AC-12, 02-AC-13

### F04 — policy-lib

- **Depends on:** F02
- **Owning plans:** `plans/22-verification-policy.md`, `plans/26-agent-runner.md`
- **Gate `GATE-F04`:** 8 acceptance criteria
  - `plans/22-verification-policy.md`: 22-AC-1, 22-AC-2, 26-AC-4
  - `plans/26-agent-runner.md`: 26-AC-1, 26-AC-2, 26-AC-3, 26-AC-4, 26-AC-5

### F05 — state-machine-lib

- **Depends on:** F02
- **Owning plans:** `plans/21-tdd-workflow.md`, `plans/29-task-state-machine.md`
- **Gate `GATE-F05`:** 12 acceptance criteria
  - `plans/21-tdd-workflow.md`: 21-AC-20
  - `plans/29-task-state-machine.md`: 29-AC-1, 29-AC-2, 29-AC-3, 29-AC-4, 29-AC-5, 29-AC-6, 29-AC-7, 29-AC-8, 29-AC-9, 29-AC-10, 29-AC-11

### F06 — verifier-lib

- **Depends on:** F02, F04
- **Owning plans:** `plans/22-verification-policy.md`
- **Gate `GATE-F06`:** 20 acceptance criteria
  - `plans/22-verification-policy.md`: 22-AC-3, 22-AC-4, 22-AC-5, 22-AC-6, 22-AC-7, 22-AC-8, 22-AC-9, 22-AC-10, 22-AC-11, 22-AC-22, 22-AC-23, 22-AC-24, 22-AC-25, 22-AC-26, 22-AC-27, 22-AC-29, 22-AC-30, 22-AC-31, 22-AC-32, 22-AC-33

### F07 — worker-sdk-and-fake-harness

- **Depends on:** F02
- **Owning plans:** `plans/27-worker-contract.md`, `plans/28-harnesses.md`
- **Gate `GATE-F07`:** 25 acceptance criteria
  - `plans/27-worker-contract.md`: 27-AC-1, 27-AC-2, 27-AC-3, 27-AC-4, 27-AC-5, 27-AC-6, 27-AC-7, 27-AC-8, 27-AC-9, 27-AC-10, 27-AC-11, 27-AC-12, 27-AC-13, 27-AC-14, 27-AC-15, 27-AC-16, 27-AC-17, 27-AC-24
  - `plans/28-harnesses.md`: 28-AC-1, 28-AC-2, 28-AC-3, 28-AC-4, 28-AC-5, 28-AC-6, 28-AC-7

### F08 — bootstrap-orchestrator-core

- **Depends on:** F02, F03
- **Owning plans:** `plans/06-opentofu-state.md`, `plans/50-bootstrap-orchestrator.md`
- **Gate `GATE-F08`:** 36 acceptance criteria
  - `plans/06-opentofu-state.md`: 06-AC-1, 06-AC-2, 06-AC-3, 06-AC-4, 06-AC-5, 06-AC-6, 06-AC-8, 06-AC-9, 06-AC-13, 06-AC-14
  - `plans/50-bootstrap-orchestrator.md`: 50-AC-1, 50-AC-2, 50-AC-3, 50-AC-4, 50-AC-5, 50-AC-6, 50-AC-7, 50-AC-8, 50-AC-9, 50-AC-10, 50-AC-11, 50-AC-12, 50-AC-13, 50-AC-14, 50-AC-15, 50-AC-16, 50-AC-17, 50-AC-18, 50-AC-19, 50-AC-29, 50-AC-30, 50-AC-31, 50-AC-32, 50-AC-34, 50-AC-35, 50-AC-36

### F09 — providers-and-paid-guardrails

- **Depends on:** F08
- **Owning plans:** `plans/03-provider-interface.md`, `plans/04-ovh-provider.md`, `plans/05-existing-nodes.md`
- **Gate `GATE-F09`:** 26 acceptance criteria
  - `plans/03-provider-interface.md`: 03-AC-1, 03-AC-2, 03-AC-3, 03-AC-4, 03-AC-5, 03-AC-6, 03-AC-7, 03-AC-8, 03-AC-9, 03-AC-10, 03-AC-11, 03-AC-12
  - `plans/04-ovh-provider.md`: 04-AC-1, 04-AC-2, 04-AC-3, 04-AC-4, 04-AC-5, 04-AC-6
  - `plans/05-existing-nodes.md`: 05-AC-1, 05-AC-2, 05-AC-3, 05-AC-4, 05-AC-5, 05-AC-6, 05-AC-7, 05-AC-8

### F10 — node-baseline-and-k3s-roles

- **Depends on:** F03, F08, F09
- **Owning plans:** `plans/07-k3s.md`, `plans/08-node-security.md`, `plans/48-upgrade.md`
- **Gate `GATE-F10`:** 19 acceptance criteria
  - `plans/07-k3s.md`: 07-AC-1, 07-AC-2, 07-AC-3, 07-AC-4, 07-AC-5, 07-AC-6
  - `plans/08-node-security.md`: 08-AC-1, 08-AC-2, 08-AC-3, 08-AC-4, 08-AC-5, 08-AC-6, 08-AC-7, 08-AC-8, 08-AC-9, 08-AC-10
  - `plans/48-upgrade.md`: 07-AC-2, 48-AC-1, 48-AC-2

### F11 — gitops-tree-offline

- **Depends on:** F02
- **Owning plans:** `plans/18-flux.md`
- **Gate `GATE-F11`:** 8 acceptance criteria
  - `plans/18-flux.md`: 18-AC-1, 18-AC-2, 18-AC-3, 18-AC-4, 18-AC-5, 18-AC-6, 18-AC-7, 18-AC-8

### F12 — admission-and-netpol-bundle

- **Depends on:** F11
- **Owning plans:** `plans/34-security.md`, `plans/36-network-policy.md`, `plans/37-admission-policy.md`, `plans/51-capacity-and-scheduling.md`
- **Gate `GATE-F12`:** 22 acceptance criteria
  - `plans/34-security.md`: 34-AC-1, 34-AC-2
  - `plans/36-network-policy.md`: 36-AC-1, 36-AC-2, 36-AC-3, 36-AC-4, 36-AC-5, 36-AC-6
  - `plans/37-admission-policy.md`: 26-AC-20, 37-AC-1, 37-AC-2, 37-AC-3, 37-AC-4, 37-AC-5, 37-AC-6, 37-AC-7, 37-AC-8, 37-AC-9, 37-AC-10, 37-AC-30
  - `plans/51-capacity-and-scheduling.md`: 51-AC-1, 51-AC-2

### F13 — strange-company-chart-skeleton

- **Depends on:** F11, F12
- **Owning plans:** `plans/54-strange-company-chart.md`
- **Gate `GATE-F13`:** 9 acceptance criteria
  - `plans/54-strange-company-chart.md`: 54-AC-1, 54-AC-2, 54-AC-3, 54-AC-4, 54-AC-5, 54-AC-6, 54-AC-7, 54-AC-8, 54-AC-9


## Phase 2 — Platform on one node (ends at the Bootstrap Definition of Done, SPEC §130)

### P01 — object-storage-and-remote-state

- **Depends on:** F08
- **Owning plans:** `plans/06-opentofu-state.md`, `plans/14-object-storage.md`
- **Gate `GATE-P01`:** 14 acceptance criteria
  - `plans/06-opentofu-state.md`: 06-AC-1, 06-AC-7, 06-AC-10, 06-AC-15
  - `plans/14-object-storage.md`: 14-AC-1, 14-AC-2, 14-AC-3, 14-AC-4, 14-AC-5, 14-AC-6, 14-AC-7, 14-AC-8, 14-AC-9, 14-AC-10

### P02 — provision

- **Depends on:** F09, P01
- **Owning plans:** `plans/03-provider-interface.md`, `plans/04-ovh-provider.md`, `plans/05-existing-nodes.md`, `plans/49-destroy.md`
- **Gate `GATE-P02`:** 25 acceptance criteria
  - `plans/03-provider-interface.md`: 03-AC-20, 03-AC-21, 03-AC-22, 03-AC-23, 03-AC-24
  - `plans/04-ovh-provider.md`: 03-AC-22, 04-AC-20, 04-AC-21, 04-AC-22, 04-AC-23, 04-AC-24, 04-AC-25, 49-AC-20
  - `plans/05-existing-nodes.md`: 05-AC-20, 05-AC-21
  - `plans/49-destroy.md`: 49-AC-1, 49-AC-2, 49-AC-3, 49-AC-4, 49-AC-5, 49-AC-6, 49-AC-7, 49-AC-8, 49-AC-9, 49-AC-10

### P03 — configure-node-and-k3s

- **Depends on:** P02, F10
- **Owning plans:** `plans/05-existing-nodes.md`, `plans/07-k3s.md`, `plans/08-node-security.md`, `plans/09-networking.md`, `plans/36-network-policy.md`
- **Gate `GATE-P03`:** 44 acceptance criteria
  - `plans/05-existing-nodes.md`: 05-AC-22, 07-AC-2
  - `plans/07-k3s.md`: 07-AC-7, 07-AC-8, 07-AC-9, 07-AC-10, 07-AC-11, 07-AC-12, 07-AC-13, 07-AC-14, 07-AC-15, 07-AC-16, 07-AC-17, 07-AC-18, 07-AC-24, 07-AC-25, 07-AC-26, 07-AC-27, 07-AC-28
  - `plans/08-node-security.md`: 08-AC-11, 08-AC-12, 08-AC-13, 08-AC-14, 08-AC-15, 08-AC-16, 08-AC-17, 08-AC-18, 08-AC-19, 08-AC-20, 08-AC-21, 08-AC-22, 08-AC-23, 08-AC-24, 09-AC-17, 15-AC-19, 20-AC-10
  - `plans/09-networking.md`: 09-AC-1, 09-AC-2, 09-AC-3, 09-AC-4, 09-AC-5, 09-AC-6, 09-AC-7
  - `plans/36-network-policy.md`: 36-AC-7

### P04 — seed

- **Depends on:** P03, F11, F12
- **Owning plans:** `plans/09-networking.md`, `plans/10-tls-certificates.md`, `plans/11-storage.md`, `plans/12-cnpg.md`, `plans/13-longhorn.md`, `plans/16-forgejo.md`, `plans/19-seed-handoff.md`, `plans/36-network-policy.md`, `plans/48-upgrade.md`, `plans/51-capacity-and-scheduling.md`
- **Gate `GATE-P04`:** 67 acceptance criteria
  - `plans/09-networking.md`: 09-AC-8, 09-AC-9, 09-AC-10, 09-AC-11, 09-AC-12, 09-AC-13, 09-AC-14, 09-AC-15, 09-AC-16, 09-AC-18, 09-AC-19
  - `plans/10-tls-certificates.md`: 10-AC-1, 10-AC-2, 10-AC-3, 10-AC-4, 10-AC-5, 10-AC-6, 10-AC-7, 10-AC-8, 10-AC-9, 10-AC-10, 10-AC-11, 10-AC-12, 10-AC-13, 10-AC-14
  - `plans/11-storage.md`: 11-AC-1, 11-AC-2, 11-AC-3
  - `plans/12-cnpg.md`: 12-AC-1, 12-AC-2, 12-AC-3, 12-AC-4, 12-AC-5
  - `plans/13-longhorn.md`: 13-AC-1, 13-AC-2, 13-AC-3, 13-AC-4, 13-AC-5, 13-AC-6, 13-AC-7, 13-AC-23
  - `plans/16-forgejo.md`: 16-AC-1, 16-AC-2, 16-AC-3, 16-AC-4, 16-AC-5, 16-AC-6, 16-AC-7, 16-AC-8, 16-AC-9, 16-AC-21
  - `plans/19-seed-handoff.md`: 19-AC-1, 19-AC-2, 19-AC-3, 19-AC-4, 19-AC-5, 19-AC-6, 19-AC-7, 19-AC-19, 19-AC-20
  - `plans/36-network-policy.md`: 36-AC-8, 36-AC-9, 36-AC-10
  - `plans/48-upgrade.md`: 48-AC-8
  - `plans/51-capacity-and-scheduling.md`: 51-AC-3, 51-AC-4, 51-AC-5

### P05 — forge-init

- **Depends on:** P04
- **Owning plans:** `plans/16-forgejo.md`, `plans/17-github-mirror.md`
- **Gate `GATE-P05`:** 23 acceptance criteria
  - `plans/16-forgejo.md`: 16-AC-10, 16-AC-11, 16-AC-12, 16-AC-13, 16-AC-14, 16-AC-15, 16-AC-16, 16-AC-17, 16-AC-18, 16-AC-19, 16-AC-20, 16-AC-21, 16-AC-22, 16-AC-23, 16-AC-34, 16-AC-35, 16-AC-36, 16-AC-37, 16-AC-38, 16-AC-39, 16-AC-40, 16-AC-41
  - `plans/17-github-mirror.md`: 17-AC-1

### P06 — control-transfer-and-mirror

- **Depends on:** P05
- **Owning plans:** `plans/05-existing-nodes.md`, `plans/16-forgejo.md`, `plans/17-github-mirror.md`, `plans/18-flux.md`, `plans/19-seed-handoff.md`, `plans/45-recovery.md`
- **Gate `GATE-P06`:** 38 acceptance criteria
  - `plans/05-existing-nodes.md`: 05-AC-24, 19-AC-21
  - `plans/16-forgejo.md`: 16-AC-24, 16-AC-25, 17-AC-1
  - `plans/17-github-mirror.md`: 17-AC-2, 17-AC-3, 17-AC-4, 17-AC-4b, 17-AC-5, 17-AC-6, 17-AC-7, 17-AC-8, 17-AC-9, 17-AC-10, 17-AC-11
  - `plans/18-flux.md`: 18-AC-9, 18-AC-10, 18-AC-11, 18-AC-12, 18-AC-13, 18-AC-14, 18-AC-15, 18-AC-16, 19-AC-11
  - `plans/19-seed-handoff.md`: 18-AC-13, 19-AC-8, 19-AC-9, 19-AC-10, 19-AC-11, 19-AC-12, 19-AC-13, 19-AC-17, 19-AC-18, 19-AC-21
  - `plans/45-recovery.md`: 19-AC-14, 45-AC-3, 45-AC-11

### P07 — authentik-sso-break-glass

- **Depends on:** P06
- **Owning plans:** `plans/15-authentik.md`, `plans/16-forgejo.md`
- **Gate `GATE-P07`:** 32 acceptance criteria
  - `plans/15-authentik.md`: 15-AC-1, 15-AC-2, 15-AC-3, 15-AC-4, 15-AC-5, 15-AC-6, 15-AC-7, 15-AC-8, 15-AC-9, 15-AC-10, 15-AC-12, 15-AC-15, 15-AC-16, 15-AC-17, 15-AC-18, 15-AC-19, 15-AC-20, 15-AC-21, 15-AC-22, 15-AC-23, 15-AC-23b, 15-AC-24, 15-AC-26b, 15-AC-29, 15-AC-30, 15-AC-31, 15-AC-33
  - `plans/16-forgejo.md`: 15-AC-12, 15-AC-26b, 16-AC-26, 16-AC-27, 16-AC-31

### P08 — woodpecker-trusted-ci

- **Depends on:** P06
- **Owning plans:** `plans/20-woodpecker.md`, `plans/52-ci-strategy.md`
- **Gate `GATE-P08`:** 40 acceptance criteria
  - `plans/20-woodpecker.md`: 20-AC-1, 20-AC-2, 20-AC-4, 20-AC-5, 20-AC-6, 20-AC-7, 20-AC-8, 20-AC-9, 20-AC-10, 20-AC-11, 20-AC-12, 20-AC-13, 20-AC-14, 20-AC-15, 20-AC-16, 20-AC-17, 20-AC-18, 20-AC-19, 20-AC-20, 20-AC-21, 20-AC-22, 20-AC-23, 20-AC-24, 20-AC-25, 20-AC-26, 20-AC-27, 20-AC-35, 20-AC-38, 20-AC-39, 20-AC-40, 20-AC-41
  - `plans/52-ci-strategy.md`: 20-AC-20, 52-AC-11, 52-AC-12, 52-AC-13, 52-AC-14, 52-AC-15, 52-AC-16, 52-AC-17, 52-AC-18

### P09 — observability-and-notifications

- **Depends on:** P06, P07, P08
- **Owning plans:** `plans/13-longhorn.md`, `plans/15-authentik.md`, `plans/17-github-mirror.md`, `plans/38-prometheus.md`, `plans/39-grafana.md`, `plans/40-opentelemetry.md`, `plans/41-logging-tracing.md`, `plans/42-alerting-notifications.md`
- **Gate `GATE-P09`:** 63 acceptance criteria
  - `plans/13-longhorn.md`: 13-AC-23
  - `plans/15-authentik.md`: 15-AC-11, 15-AC-13, 15-AC-23, 15-AC-23b, 15-AC-25, 15-AC-26, 15-AC-26b, 39-AC-6
  - `plans/17-github-mirror.md`: 17-AC-12, 17-AC-13, 17-AC-14, 17-AC-15
  - `plans/38-prometheus.md`: 38-AC-1, 38-AC-2, 38-AC-3, 38-AC-4, 38-AC-5, 38-AC-6, 38-AC-7, 38-AC-8, 38-AC-9, 38-AC-10, 38-AC-11, 38-AC-12, 38-AC-13, 38-AC-19, 38-AC-21
  - `plans/39-grafana.md`: 15-AC-13, 39-AC-1, 39-AC-2, 39-AC-3, 39-AC-4, 39-AC-5, 39-AC-6, 39-AC-7, 39-AC-8, 39-AC-9, 39-AC-16
  - `plans/40-opentelemetry.md`: 40-AC-1, 40-AC-2, 40-AC-3, 40-AC-4, 40-AC-5, 40-AC-6, 40-AC-7, 40-AC-9, 40-AC-14
  - `plans/41-logging-tracing.md`: 41-AC-1, 41-AC-2
  - `plans/42-alerting-notifications.md`: 42-AC-1, 42-AC-2, 42-AC-3, 42-AC-4, 42-AC-5, 42-AC-6, 42-AC-7, 42-AC-8, 42-AC-9, 42-AC-10, 42-AC-11, 42-AC-22, 42-AC-23

### P10 — backups-and-restore-tests

- **Depends on:** P06, P09, P08
- **Owning plans:** `plans/06-opentofu-state.md`, `plans/11-storage.md`, `plans/12-cnpg.md`, `plans/13-longhorn.md`, `plans/14-object-storage.md`, `plans/15-authentik.md`, `plans/16-forgejo.md`, `plans/17-github-mirror.md`, `plans/35-secrets.md`, `plans/42-alerting-notifications.md`, `plans/43-retention-gc.md`, `plans/44-backup.md`, `plans/45-recovery.md`, `plans/48-upgrade.md`
- **Gate `GATE-P10`:** 103 acceptance criteria
  - `plans/06-opentofu-state.md`: 06-AC-12
  - `plans/11-storage.md`: 11-AC-4, 11-AC-5, 11-AC-6
  - `plans/12-cnpg.md`: 12-AC-6, 12-AC-7, 12-AC-8, 12-AC-9, 12-AC-10, 12-AC-11, 12-AC-12, 12-AC-13, 12-AC-14, 12-AC-19, 44-AC-4
  - `plans/13-longhorn.md`: 13-AC-8, 13-AC-9, 13-AC-10, 13-AC-11, 13-AC-12, 13-AC-13, 13-AC-14, 13-AC-24, 13-AC-25, 44-AC-6
  - `plans/14-object-storage.md`: 14-AC-11, 14-AC-12, 14-AC-13, 14-AC-14, 14-AC-15, 14-AC-16, 14-AC-17, 14-AC-18, 14-AC-19
  - `plans/15-authentik.md`: 15-AC-27, 15-AC-28, 15-AC-29, 15-AC-32, 15-AC-34
  - `plans/16-forgejo.md`: 16-AC-28, 16-AC-29, 16-AC-30
  - `plans/17-github-mirror.md`: 17-AC-16
  - `plans/35-secrets.md`: 35-AC-1, 35-AC-2, 35-AC-3, 35-AC-4, 35-AC-5, 35-AC-6, 35-AC-7, 35-AC-8, 35-AC-9, 35-AC-10, 35-AC-11, 35-AC-12, 35-AC-13, 35-AC-14, 35-AC-15, 35-AC-16, 35-AC-17, 35-AC-18
  - `plans/42-alerting-notifications.md`: 42-AC-12, 42-AC-13
  - `plans/43-retention-gc.md`: 14-AC-7, 43-AC-1, 43-AC-2, 43-AC-4, 43-AC-5, 43-AC-6
  - `plans/44-backup.md`: 06-AC-7, 11-AC-5, 16-AC-29, 44-AC-1, 44-AC-2, 44-AC-3, 44-AC-4, 44-AC-5, 44-AC-6, 44-AC-7, 44-AC-8, 44-AC-9, 44-AC-10, 44-AC-11, 44-AC-12, 44-AC-13, 44-AC-14, 44-AC-18, 44-AC-19, 44-AC-20, 44-AC-21, 44-AC-23
  - `plans/45-recovery.md`: 42-AC-12, 45-AC-3, 45-AC-4, 45-AC-7, 45-AC-9, 45-AC-12, 45-AC-13, 45-AC-14, 45-AC-15, 45-AC-16
  - `plans/48-upgrade.md`: 48-AC-3, 48-AC-4

### P11 — supply-chain

- **Depends on:** P08, P12
- **Owning plans:** `plans/17-github-mirror.md`, `plans/23-supply-chain.md`, `plans/43-retention-gc.md`, `plans/48-upgrade.md`, `plans/52-ci-strategy.md`
- **Gate `GATE-P11`:** 39 acceptance criteria
  - `plans/17-github-mirror.md`: 16-AC-18, 17-AC-4, 17-AC-4b
  - `plans/23-supply-chain.md`: 17-AC-4, 20-AC-12, 20-AC-38, 23-AC-1, 23-AC-2, 23-AC-3, 23-AC-4, 23-AC-5, 23-AC-6, 23-AC-7, 23-AC-8, 23-AC-9, 23-AC-10, 23-AC-11, 23-AC-12, 23-AC-13, 23-AC-14, 23-AC-15, 23-AC-16, 23-AC-17, 23-AC-18, 23-AC-19, 23-AC-20, 23-AC-21, 23-AC-22, 23-AC-23, 23-AC-25, 23-AC-26, 23-AC-27, 34-AC-10, 52-AC-20
  - `plans/43-retention-gc.md`: 43-AC-3
  - `plans/48-upgrade.md`: 48-AC-5, 48-AC-6
  - `plans/52-ci-strategy.md`: 52-AC-20, 52-AC-22

### P12 — bootstrap-verify-control-transfer-complete

- **Depends on:** P07, P08, P09, P10
- **Owning plans:** `plans/06-opentofu-state.md`, `plans/11-storage.md`, `plans/16-forgejo.md`, `plans/17-github-mirror.md`, `plans/19-seed-handoff.md`, `plans/20-woodpecker.md`, `plans/34-security.md`, `plans/38-prometheus.md`, `plans/39-grafana.md`, `plans/42-alerting-notifications.md`, `plans/45-recovery.md`, `plans/49-destroy.md`, `plans/50-bootstrap-orchestrator.md`, `plans/51-capacity-and-scheduling.md`, `plans/52-ci-strategy.md`
- **Gate `GATE-P12`:** 57 acceptance criteria
  - `plans/06-opentofu-state.md`: 06-AC-11
  - `plans/11-storage.md`: 11-AC-9
  - `plans/16-forgejo.md`: 16-AC-1, 16-AC-2, 16-AC-3, 16-AC-4, 16-AC-10, 16-AC-11, 16-AC-12, 16-AC-19, 16-AC-22, 16-AC-26, 16-AC-27, 16-AC-28, 16-AC-29, 16-AC-30, 16-AC-32, 17-AC-1, 55-AC-10
  - `plans/17-github-mirror.md`: 17-AC-17
  - `plans/19-seed-handoff.md`: 19-AC-15, 19-AC-16
  - `plans/20-woodpecker.md`: 16-AC-32, 20-AC-3, 20-AC-35, 20-AC-36, 20-AC-37
  - `plans/34-security.md`: 34-AC-6, 34-AC-7, 34-AC-8
  - `plans/38-prometheus.md`: 38-AC-18
  - `plans/39-grafana.md`: 39-AC-16
  - `plans/39-grafana.md`: 39-AC-15
  - `plans/42-alerting-notifications.md`: 42-AC-21
  - `plans/45-recovery.md`: 45-AC-10, 45-AC-12
  - `plans/49-destroy.md`: 49-AC-20, 49-AC-21, 49-AC-22
  - `plans/50-bootstrap-orchestrator.md`: 50-AC-20, 50-AC-21, 50-AC-22, 50-AC-23, 50-AC-24, 50-AC-25, 50-AC-26, 50-AC-27, 50-AC-28, 50-AC-33
  - `plans/51-capacity-and-scheduling.md`: 51-AC-6, 51-AC-7, 51-AC-8, 51-AC-9
  - `plans/52-ci-strategy.md`: 16-AC-32, 20-AC-36, 52-AC-19, 52-AC-21


## Phase 3 — Control plane (ends at the orchestration DoD: all fake TDD flows on the live cluster)

### C01 — runner-core

- **Depends on:** F04, F05, F13, P11
- **Owning plans:** `plans/09-networking.md`, `plans/26-agent-runner.md`, `plans/29-task-state-machine.md`, `plans/54-strange-company-chart.md`
- **Gate `GATE-C01`:** 29 acceptance criteria
  - `plans/09-networking.md`: 09-AC-17
  - `plans/26-agent-runner.md`: 26-AC-10, 26-AC-11, 26-AC-12, 26-AC-13, 26-AC-14, 26-AC-15, 26-AC-16, 26-AC-17, 26-AC-18, 26-AC-19, 26-AC-20, 26-AC-33, 26-AC-34, 26-AC-36, 26-AC-44, 26-AC-50
  - `plans/29-task-state-machine.md`: 29-AC-20, 29-AC-21, 29-AC-22, 29-AC-23, 29-AC-24, 29-AC-25, 29-AC-26, 29-AC-27
  - `plans/54-strange-company-chart.md`: 54-AC-20, 54-AC-21, 54-AC-22, 54-AC-23

### C02 — k8s-adapter

- **Depends on:** C01, F07, F12, C03, C07
- **Owning plans:** `plans/26-agent-runner.md`, `plans/27-worker-contract.md`, `plans/28-harnesses.md`, `plans/29-task-state-machine.md`, `plans/30-handoffs.md`, `plans/34-security.md`, `plans/36-network-policy.md`, `plans/37-admission-policy.md`, `plans/51-capacity-and-scheduling.md`
- **Gate `GATE-C02`:** 53 acceptance criteria
  - `plans/26-agent-runner.md`: 26-AC-21, 26-AC-22, 26-AC-23, 26-AC-24, 26-AC-25, 26-AC-26, 26-AC-27, 26-AC-28, 26-AC-29, 26-AC-35, 26-AC-38, 26-AC-39, 26-AC-45, 26-AC-46, 26-AC-49, 26-AC-51, 26-AC-53
  - `plans/27-worker-contract.md`: 27-AC-20, 27-AC-21, 27-AC-22, 27-AC-23
  - `plans/28-harnesses.md`: 26-AC-26, 28-AC-10, 28-AC-11, 28-AC-12
  - `plans/29-task-state-machine.md`: 29-AC-28, 29-AC-29
  - `plans/30-handoffs.md`: 30-AC-1, 30-AC-2, 30-AC-3, 30-AC-4
  - `plans/34-security.md`: 26-AC-25, 34-AC-3, 34-AC-4
  - `plans/36-network-policy.md`: 36-AC-11, 36-AC-12, 36-AC-13, 36-AC-14, 36-AC-15, 36-AC-16
  - `plans/37-admission-policy.md`: 26-AC-22, 26-AC-28, 37-AC-20, 37-AC-21, 37-AC-22, 37-AC-23, 37-AC-41, 37-AC-43
  - `plans/51-capacity-and-scheduling.md`: 51-AC-10, 51-AC-11, 51-AC-12, 51-AC-13, 51-AC-14

### C03 — forgejo-adapter

- **Depends on:** C01, P05
- **Owning plans:** `plans/26-agent-runner.md`, `plans/29-task-state-machine.md`, `plans/55-forge-workflow.md`
- **Gate `GATE-C03`:** 18 acceptance criteria
  - `plans/26-agent-runner.md`: 26-AC-11, 26-AC-12, 26-AC-33, 26-AC-34, 26-AC-40, 26-AC-41, 26-AC-42, 26-AC-43, 26-AC-44, 26-AC-48, 26-AC-52, 26-AC-54
  - `plans/29-task-state-machine.md`: 29-AC-30, 29-AC-31
  - `plans/55-forge-workflow.md`: 55-AC-1, 55-AC-2, 55-AC-3, 55-AC-5

### C04 — woodpecker-adapter-red-green-evidence

- **Depends on:** C01, P08, F06, C02, C03
- **Owning plans:** `plans/20-woodpecker.md`, `plans/21-tdd-workflow.md`, `plans/22-verification-policy.md`
- **Gate `GATE-C04`:** 16 acceptance criteria
  - `plans/20-woodpecker.md`: 20-AC-28, 20-AC-29, 20-AC-30, 20-AC-31, 20-AC-32, 20-AC-33, 20-AC-34
  - `plans/21-tdd-workflow.md`: 20-AC-28, 21-AC-1, 21-AC-2, 21-AC-3
  - `plans/22-verification-policy.md`: 22-AC-12, 22-AC-13, 22-AC-14, 22-AC-15, 22-AC-16

### C05 — model-gateway-ledger-pricing

- **Depends on:** C01
- **Owning plans:** `plans/31-model-routing.md`, `plans/32-cost-accounting.md`, `plans/53-model-gateway.md`
- **Gate `GATE-C05`:** 57 acceptance criteria
  - `plans/31-model-routing.md`: 31-AC-1, 31-AC-2, 31-AC-3, 31-AC-9, 31-AC-10
  - `plans/32-cost-accounting.md`: 32-AC-1, 32-AC-2, 32-AC-3, 32-AC-4, 32-AC-5, 32-AC-6, 32-AC-7, 32-AC-8, 32-AC-9, 32-AC-10, 32-AC-11, 32-AC-12, 32-AC-13, 32-AC-14, 32-AC-15
  - `plans/53-model-gateway.md`: 53-AC-1, 53-AC-2, 53-AC-3, 53-AC-4, 53-AC-5, 53-AC-6, 53-AC-7, 53-AC-8, 53-AC-9, 53-AC-10, 53-AC-11, 53-AC-12, 53-AC-13, 53-AC-14, 53-AC-15, 53-AC-16, 53-AC-17, 53-AC-18, 53-AC-19, 53-AC-20, 53-AC-21, 53-AC-22, 53-AC-25, 53-AC-26, 53-AC-27, 53-AC-28, 53-AC-30, 53-AC-31, 53-AC-32, 53-AC-34, 53-AC-35, 53-AC-37, 53-AC-38, 53-AC-39, 53-AC-40, 53-AC-41, 53-AC-42

### C06 — budgets-emergency-stop-notifications

- **Depends on:** C05, P09, C02, C04, C08
- **Owning plans:** `plans/33-budget-enforcement.md`, `plans/42-alerting-notifications.md`
- **Gate `GATE-C06`:** 27 acceptance criteria
  - `plans/33-budget-enforcement.md`: 32-AC-15, 33-AC-1, 33-AC-2, 33-AC-3, 33-AC-4, 33-AC-6, 33-AC-10, 33-AC-12, 33-AC-13, 33-AC-14, 33-AC-15, 33-AC-17, 33-AC-18, 33-AC-19, 33-AC-20, 33-AC-21, 33-AC-22, 33-AC-23, 33-AC-24, 33-AC-25, 33-AC-28, 33-AC-29, 33-AC-30, 53-AC-36
  - `plans/42-alerting-notifications.md`: 42-AC-14, 42-AC-15, 42-AC-16

### C07 — context-compiler

- **Depends on:** C01, F04
- **Owning plans:** `plans/25-context.md`
- **Gate `GATE-C07`:** 13 acceptance criteria
  - `plans/25-context.md`: 25-AC-1, 25-AC-2, 25-AC-3, 25-AC-4, 25-AC-5, 25-AC-6, 25-AC-7, 25-AC-8, 25-AC-9, 25-AC-10, 25-AC-11, 25-AC-12, 46-AC-7

### C08 — mcp-surface-and-hermes

- **Depends on:** C01, C05, P07
- **Owning plans:** `plans/13-longhorn.md`, `plans/15-authentik.md`, `plans/24-hermes.md`, `plans/26-agent-runner.md`, `plans/44-backup.md`
- **Gate `GATE-C08`:** 30 acceptance criteria
  - `plans/13-longhorn.md`: 13-AC-9, 13-AC-24
  - `plans/15-authentik.md`: 15-AC-14
  - `plans/24-hermes.md`: 24-AC-1, 24-AC-2, 24-AC-3, 24-AC-4, 24-AC-5, 24-AC-6, 24-AC-7, 24-AC-8, 24-AC-9, 24-AC-10, 24-AC-11, 24-AC-12, 24-AC-13, 24-AC-14, 24-AC-15, 24-AC-16, 24-AC-17, 24-AC-18, 24-AC-19, 24-AC-20, 46-AC-6, 46-AC-17
  - `plans/26-agent-runner.md`: 26-AC-30, 26-AC-31, 26-AC-32, 26-AC-37
  - `plans/44-backup.md`: 44-AC-22

### C09 — merge-policy-enforcement

- **Depends on:** C03, C04, C02
- **Owning plans:** `plans/22-verification-policy.md`, `plans/34-security.md`, `plans/55-forge-workflow.md`
- **Gate `GATE-C09`:** 17 acceptance criteria
  - `plans/22-verification-policy.md`: 22-AC-28
  - `plans/34-security.md`: 34-AC-5, 34-AC-10
  - `plans/55-forge-workflow.md`: 23-AC-21, 55-AC-10, 55-AC-11, 55-AC-15, 55-AC-16, 55-AC-19, 55-AC-20, 55-AC-21, 55-AC-22, 55-AC-24, 55-AC-25, 55-AC-26, 55-AC-27, 55-AC-30

### C10 — fake-end-to-end-tdd-flows

- **Depends on:** C02, C03, C04, C05, C06, C07, C08, C09
- **Owning plans:** `plans/21-tdd-workflow.md`, `plans/22-verification-policy.md`, `plans/30-handoffs.md`, `plans/31-model-routing.md`, `plans/33-budget-enforcement.md`, `plans/37-admission-policy.md`, `plans/46-self-hosting-threshold.md`, `plans/48-upgrade.md`, `plans/55-forge-workflow.md`
- **Gate `GATE-C10`:** 47 acceptance criteria
  - `plans/21-tdd-workflow.md`: 21-AC-4, 21-AC-10, 21-AC-11, 21-AC-12, 21-AC-13, 21-AC-14
  - `plans/22-verification-policy.md`: 22-AC-20, 22-AC-21, 22-AC-34, 22-AC-35, 22-AC-36
  - `plans/30-handoffs.md`: 30-AC-5
  - `plans/31-model-routing.md`: 31-AC-4, 31-AC-5, 31-AC-6, 31-AC-7, 31-AC-8, 31-AC-11, 31-AC-14
  - `plans/33-budget-enforcement.md`: 33-AC-5, 33-AC-7, 33-AC-8, 33-AC-9, 33-AC-11, 33-AC-16, 33-AC-26, 33-AC-27, 33-AC-31
  - `plans/37-admission-policy.md`: 37-AC-42
  - `plans/46-self-hosting-threshold.md`: 24-AC-8, 24-AC-12, 25-AC-10, 46-AC-1, 46-AC-2, 46-AC-3, 46-AC-4, 46-AC-5, 46-AC-6, 46-AC-7
  - `plans/48-upgrade.md`: 48-AC-7
  - `plans/55-forge-workflow.md`: 55-AC-4, 55-AC-12, 55-AC-13, 55-AC-14, 55-AC-17, 55-AC-18, 55-AC-23

### C11 — sc-dashboards-and-alerts

- **Depends on:** C06, P09, C10
- **Owning plans:** `plans/34-security.md`, `plans/38-prometheus.md`, `plans/39-grafana.md`, `plans/40-opentelemetry.md`, `plans/41-logging-tracing.md`, `plans/42-alerting-notifications.md`, `plans/55-forge-workflow.md`
- **Gate `GATE-C11`:** 20 acceptance criteria
  - `plans/34-security.md`: 34-AC-5, 34-AC-9
  - `plans/38-prometheus.md`: 38-AC-14, 38-AC-15, 38-AC-16, 38-AC-17
  - `plans/39-grafana.md`: 39-AC-4, 39-AC-10, 39-AC-11, 39-AC-12, 39-AC-13
  - `plans/40-opentelemetry.md`: 40-AC-11, 40-AC-12
  - `plans/41-logging-tracing.md`: 41-AC-3
  - `plans/42-alerting-notifications.md`: 42-AC-15, 42-AC-17, 42-AC-18
  - `plans/55-forge-workflow.md`: 55-AC-14, 55-AC-15, 55-AC-30


## Phase 4 — Real harness and the Self-Hosting Threshold (SPEC §117–§118)

### R01 — real-harness

- **Depends on:** C10
- **Owning plans:** `plans/28-harnesses.md`, `plans/40-opentelemetry.md`
- **Gate `GATE-R01`:** 13 acceptance criteria
  - `plans/28-harnesses.md`: 28-AC-20, 28-AC-21, 28-AC-22, 28-AC-23, 28-AC-24, 28-AC-25, 28-AC-26, 28-AC-27, 28-AC-28, 28-AC-29, 31-AC-12, 40-AC-10
  - `plans/40-opentelemetry.md`: 40-AC-10

### R02 — self-hosting-threshold

- **Depends on:** R01, C11, P12
- **Owning plans:** `plans/37-admission-policy.md`, `plans/43-retention-gc.md`, `plans/44-backup.md`, `plans/45-recovery.md`, `plans/46-self-hosting-threshold.md`, `plans/51-capacity-and-scheduling.md`
- **Gate `GATE-R02`:** 24 acceptance criteria
  - `plans/37-admission-policy.md`: 37-AC-40
  - `plans/43-retention-gc.md`: 11-AC-9, 43-AC-7, 43-AC-8, 43-AC-9
  - `plans/44-backup.md`: 44-AC-25, 44-AC-26, 44-AC-27, 44-AC-28
  - `plans/45-recovery.md`: 45-AC-21, 45-AC-22, 45-AC-23
  - `plans/46-self-hosting-threshold.md`: 24-AC-13, 46-AC-10, 46-AC-11, 46-AC-12, 46-AC-13, 46-AC-14, 46-AC-15, 46-AC-16, 46-AC-17, 46-AC-18, 46-AC-19
  - `plans/51-capacity-and-scheduling.md`: 51-AC-16


## Phase 5 — Strange Company works on other repositories (CD-43; after the threshold, does not widen it)

### S01 — first-project-repository

- **Depends on:** R02
- **Owning plans:** `plans/16-forgejo.md`, `plans/17-github-mirror.md`, `plans/22-verification-policy.md`, `plans/44-backup.md`
- **Gate `GATE-S01`:** 4 acceptance criteria
  - `plans/16-forgejo.md`: 16-AC-42
  - `plans/17-github-mirror.md`: 17-AC-18
  - `plans/22-verification-policy.md`: 22-AC-37
  - `plans/44-backup.md`: 44-AC-29


## Phase 6 — Small-HA (sequenced after the threshold; hard dependency only on P12)

### H01 — nodes-2-3-etcd-quorum

- **Depends on:** P12, R02
- **Owning plans:** `plans/05-existing-nodes.md`, `plans/07-k3s.md`, `plans/47-ha-expansion.md`
- **Gate `GATE-H01`:** 15 acceptance criteria
  - `plans/05-existing-nodes.md`: 05-AC-23
  - `plans/07-k3s.md`: 07-AC-19, 07-AC-20, 07-AC-21, 07-AC-22, 07-AC-23
  - `plans/47-ha-expansion.md`: 07-AC-19, 47-AC-1, 47-AC-2, 47-AC-3, 47-AC-4, 47-AC-5, 47-AC-6, 47-AC-7, 47-AC-8

### H02 — cnpg-3-longhorn-2

- **Depends on:** H01, C08
- **Owning plans:** `plans/11-storage.md`, `plans/12-cnpg.md`, `plans/13-longhorn.md`, `plans/44-backup.md`, `plans/47-ha-expansion.md`
- **Gate `GATE-H02`:** 22 acceptance criteria
  - `plans/11-storage.md`: 11-AC-7, 11-AC-8, 11-AC-10
  - `plans/12-cnpg.md`: 12-AC-15, 12-AC-16, 12-AC-17, 12-AC-18
  - `plans/13-longhorn.md`: 13-AC-15, 13-AC-16, 13-AC-17, 13-AC-18
  - `plans/44-backup.md`: 13-AC-17, 44-AC-15, 44-AC-16, 44-AC-17
  - `plans/47-ha-expansion.md`: 12-AC-15, 47-AC-9, 47-AC-10, 47-AC-11, 47-AC-12, 47-AC-13, 47-AC-17

### H03 — loki-tempo

- **Depends on:** H01, P09, R02
- **Owning plans:** `plans/39-grafana.md`, `plans/40-opentelemetry.md`, `plans/41-logging-tracing.md`, `plans/42-alerting-notifications.md`, `plans/43-retention-gc.md`
- **Gate `GATE-H03`:** 18 acceptance criteria
  - `plans/39-grafana.md`: 39-AC-14
  - `plans/40-opentelemetry.md`: 40-AC-11, 40-AC-13
  - `plans/41-logging-tracing.md`: 40-AC-13, 41-AC-4, 41-AC-5, 41-AC-5a, 41-AC-5b, 41-AC-5c, 41-AC-6, 41-AC-7, 41-AC-8, 41-AC-9, 41-AC-10, 42-AC-19
  - `plans/42-alerting-notifications.md`: 42-AC-7, 42-AC-19
  - `plans/43-retention-gc.md`: 43-AC-10

### H04 — ha-hardening-drills

- **Depends on:** H02, H03, R02
- **Owning plans:** `plans/19-seed-handoff.md`, `plans/23-supply-chain.md`, `plans/38-prometheus.md`, `plans/41-logging-tracing.md`, `plans/42-alerting-notifications.md`, `plans/44-backup.md`, `plans/45-recovery.md`, `plans/47-ha-expansion.md`, `plans/51-capacity-and-scheduling.md`
- **Gate `GATE-H04`:** 25 acceptance criteria
  - `plans/19-seed-handoff.md`: 19-AC-14
  - `plans/23-supply-chain.md`: 23-AC-24
  - `plans/38-prometheus.md`: 38-AC-20
  - `plans/41-logging-tracing.md`: 41-AC-11
  - `plans/42-alerting-notifications.md`: 42-AC-20
  - `plans/44-backup.md`: 44-AC-24
  - `plans/45-recovery.md`: 07-AC-22, 15-AC-23, 38-AC-20, 41-AC-11, 42-AC-20, 45-AC-1, 45-AC-2, 45-AC-5, 45-AC-6, 45-AC-8, 45-AC-17, 45-AC-18, 45-AC-19, 45-AC-20, 45-AC-23
  - `plans/47-ha-expansion.md`: 47-AC-14, 47-AC-15, 47-AC-16
  - `plans/51-capacity-and-scheduling.md`: 51-AC-15

## Critical path

F01 → F02 → F08 → P01 → P02 → P03 → P04 → P05 → P06 → P08 → P09 → P10 → P12 → P11 → C01 → (C03, C07) → C02 → C04 → C09 → C10 → R01 → R02. P07 runs beside P08. P11 (installation supply chain) runs after control transfer (CD-19).

## Operator decisions that affect sequencing
- **Node size (SI-01/SI-12).** It must be decided before P02 orders hardware. See `STATUS.md`.
- **OVH product line (plan 04 spike S-1..S-7).** It is decided inside F09/P02. The `existing` provider is always available as the fallback.

