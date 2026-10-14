# ADR-0402: An SC-owned adapter (`sc-worker`) owns the worker lifecycle, Git operations and credentials; harnesses only edit files and write a claim

**Status:** Accepted (Stage 0) · **Date:** 2026-10-13 · **Owning plan:** 27 (consumer: 28)

## Context
SPEC §64 says no vendor's coding harness defines the abstract concept of an agent, and §67 defines one lifecycle for every harness (fetch task, credentials, clone, checkout, branch, context, role, validation, commit, push, result, exit). Coding CLIs differ in output formats, exit semantics, stdin handling (L-2), configuration loading (repository hooks and instruction files are an injection surface, L-2) and cost reporting (L-3). If each harness performed its own Git operations, the commit format, trailers, branch topology and push target would depend on model behavior.

## Decision
- Every harness image runs `sc-worker` (Go) as PID 1. It fetches the packet, handles credentials, clones only `fetch_ref`, enforces branch topology, materializes context outside the working tree, invokes the CLI through an adapter (stdin closed, framed output, timeout below the pod deadline, signal forwarding), validates the harness's structured claim (`HarnessClaim`), runs advisory validation, makes exactly **one** commit with fixed identity, subject prefix and trailers (`SC-Task`, `SC-Execution`, `SC-Role`, `SC-Base`), pushes `agent/<task-id>` with a lease, and posts the `WorkerResult`.
- Harnesses are configured to not commit or push (tool deny-lists where supported); commits they make anyway are folded into `sc-worker`'s commit; other refs they push are caught by the Runner's ref audit (ADR-0007).
- `sc-worker` neutralizes repository-controlled code in its own Git operations (hooks, filter drivers, fsmonitor, alternates are restored or removed before post-harness Git commands).
- `sc-worker`'s checks are advisory; the Runner re-derives every fact (plan 26 §14.4).

## Amendment (2026-10-14, review TDD-14 / RA-4)
The worker pod has **two containers** from the same digest-pinned image. `sc-worker run` (container `sc-worker`) alone mounts the deploy key and the Runner task token and performs all Git and Runner I/O. `sc-worker shim` (container `harness`) runs the harness CLI and every repository-controlled command (local validation) and mounts only the gateway token and an egress-proxy token (`contracts/task-token.schema.yaml`, `token_use: egress`). The containers exchange only files in the shared workspace emptyDir; `shareProcessNamespace` is off. This removes the earlier residual risk that a hostile harness, sharing the uid, could read the deploy key or task token: it can no longer push any ref, fetch other refs (SPEC §48) or forge a result. Plans 26 §13 and 27 §7.4/§10 define the details; plan 37 must admit exactly this container pair.

## Consequences
- One lifecycle, one result contract and one commit format across Claude Code, Codex, OpenCode and the fake.
- The harness shares a uid with `sc-worker` and can read the mounted credentials; this residual risk is documented (plan 27 §10) and contained by credential scope, Runner verification and revocation.
- Adapters must be maintained per CLI version; image self-tests catch flag drift.

## Alternatives rejected
- Harness-native Git (the CLI commits and pushes): non-deterministic history and formats; injection-driven pushes become normal behavior.
- A sidecar for Git operations: shared-volume coordination and process lifecycles add failure modes without removing the shared-uid exposure.

## Verification
27-AC-8 (commit format), 27-AC-9 (folding), 27-AC-10 (no repository code executed by sc-worker's Git), 28-AC-6 (fake-only knobs are inert for real adapters).
