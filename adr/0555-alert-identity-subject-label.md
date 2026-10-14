# ADR-0555: Runner-posted alerts carry an opaque identity label `subject` (clarifies ADR-0009)

**Status:** Accepted (Stage 0) · **Date:** 2026-10-13 · **Owning plan:** 42

## Context
ADR-0009 says Runner-posted organizational alerts carry `task_id` "as an *annotation* rather than a label for grouping". Alertmanager identifies an alert by its **complete label set**. If two tasks entering NEEDS_HUMAN post identical label sets, Alertmanager sees *one* alert, overwrites its annotations, and does not notify for the second task, because notifications fire on group membership changes, not annotation changes. That would violate SPEC §97 ("Needs Human SHALL result in actual notifications").

## Decision
- Runner-posted alerts carry an identity label **`subject`** (`sc-<n>`, `test-<nonce>`, `bg-<target>-<ts>`). `subject` is **excluded from `group_by`** (schema-enforced in `alert-catalog.schema.yaml`), so grouping behaves as ADR-0009 intended. `task_id` remains an annotation for display and links.
- This identifier exists only in Alertmanager's in-memory alert set and notification payloads. It never becomes a Prometheus series, so SPEC §91's cardinality guidance is respected. The Prometheus-side NEEDS_HUMAN signal stays the task-free gauge `sc_runner_tasks{state="NEEDS_HUMAN"}` (plan 38 §21.2).

## Consequences
Each task's NEEDS_HUMAN entry triggers a notification, and a burst of tasks is batched into one grouped message per `group_interval`. Plan 26 posts according to `contracts/notification.schema.yaml`.

## Alternatives rejected
- One aggregate alert with a list annotation: misses notifications for second and later tasks.
- A hashed task ID label: equivalent cardinality, less readable.

## Verification
C06 gate (plan 42): posting NEEDS_HUMAN for `sc-1` and then `sc-2` 60 s apart produces two deliveries at the test receiver, and both appear in one group after `group_interval`.
