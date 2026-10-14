# ADR-0015: Namespace refinement

**Status:** Accepted (Stage 0) · **Date:** 2026-10-13

## Context
SPEC §19 lists the expected namespaces and permits refinement. It also requires agent workloads to stay separated from platform infrastructure.

## Decision
Keep SPEC §19's namespaces and add the following:
- `hermes`: Hermes Agent, isolated from `strange-company` so that Hermes shares no namespace (and therefore no Secret mounting scope) with the Runner or Gateway.
- `ci-jobs`: Woodpecker pipeline pods, separate from the Woodpecker server.
- `renovate`: the dependency-update bot.
- `dr-tests`: restore-test jobs.
The authoritative list, with PSA levels and tier labels, is in `plans/00-master-plan.md` §5.

## Consequences
Clean RBAC, NetworkPolicy and PSA scoping for every trust level.

## Alternatives rejected
- Hermes in `strange-company`: anything able to create pods in that namespace could mount Runner secrets. Hermes cannot create pods, but blast radius is smaller when the namespaces are separate.

## Verification
F11 gate: the rendered manifests contain exactly the namespaces in the master plan, each with PSA and tier labels.
