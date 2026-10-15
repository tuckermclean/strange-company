# ADR-0001: Stage-0 document and contract conventions

**Status:** Accepted (Stage 0) · **Date:** 2026-10-13

## Context
SPEC §120–§122 require plans, procedures and machine-readable contracts. Several authors (human and agent) will produce them in parallel, so the conventions must be fixed up front.

## Decision
- Contracts use JSON Schema 2020-12 written in YAML (`*.schema.yaml`). HTTP APIs use OpenAPI 3.1. Each schema carries `$id`, at least one valid `examples` entry and, where meaningful, `x-invalid-examples` with a `reason`.
- Config manifests use `apiVersion: strange.company/v1alpha1` with camelCase fields. API, record and evidence payloads use snake_case.
- Plans follow `plans/_template.md` (SPEC §121). Procedures follow `procedures/_template.md`.
- Acceptance criteria IDs are `<plan>-AC-<n>`, grouped under `GATE-<dag-node>`.
- Third-party versions are chosen by a stated *selection rule* in Stage 0 and pinned at implementation time.

## Consequences
CI node F02 can validate every contract and every example mechanically. Gates can be traced from `plans/dag.yaml` to plan text by ID.

## Alternatives rejected
- Protobuf or CUE as the contract language: CUE is attractive, but JSON Schema has wider tool support (Go, Python, editors, OpenAPI reuse).
- Pinning exact third-party versions in Stage 0: they would be stale before implementation, and a selection rule plus a re-verification test is more honest.

## Verification
F02 gate: meta-validation of all schemas, all `examples` validate, all `x-invalid-examples` fail.
