# ADR-0006: One CloudNativePG Cluster per state owner

**Status:** Accepted (Stage 0) · **Date:** 2026-10-13

## Context
Forgejo, Authentik, Woodpecker and Strange Company each need PostgreSQL (SPEC §25, §30, §39). A shared cluster saves memory (roughly 0.6–0.9 GiB at one node). But a point-in-time restore of one application would roll back the others, and a single noisy tenant or bad migration affects all of them.

## Decision
Four CNPG `Cluster`s: `forgejo-pg` (ns `forgejo`), `authentik-pg` (ns `authentik`), `woodpecker-pg` (ns `woodpecker`) and `sc-pg` (ns `strange-company`). Each has its own barman-cloud object-store path, its own scheduled base backups and continuous WAL archiving, and its own restore test. `instances: 1` at self-hosting-minimum and `3` at small-ha. PGDATA is on node-local storage (`local-path`), never on Longhorn (SPEC §25).

## Consequences
- Independent PITR and restore testing per application (SPEC §108–§109).
- More memory. Each instance is tuned small (plan 12), and the capacity gate (plan 51) measures it.
- Four backup streams to monitor. Alerts are templated per cluster.

## Alternatives rejected
- A single shared cluster: cheaper, but couples recovery points and blast radius.
- Embedded SQLite for Woodpecker: no replication path for small-ha, and it is inconsistent with SPEC §25.

## Verification
P10 gate: per-cluster PITR restore test to a timestamp; the restored data matches a pre-recorded marker row.
