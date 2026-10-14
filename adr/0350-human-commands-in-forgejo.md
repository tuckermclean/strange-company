# ADR-0350: Human-only task transitions are authenticated `/sc` commands in Forgejo comments (plus the loopback admin API)

**Status:** Accepted (Stage 0) · **Date:** 2026-10-13 · **Owning plan:** 29

## Context
Lesson L-4 and plan 29 make several transitions human-only: leaving `BLOCKED` and `NEEDS_HUMAN`, approving a corrected acceptance test, approving an exemption or judge, revising acceptance criteria, declaring a task done, re-running verification. The MCP surface (SPEC §60) is Hermes's channel and Hermes is not trusted for enforcement (ADR-0003), so it cannot carry human authority. The Runner's admin API (plan 26) is loopback-only behind `kubectl port-forward` and a break-glass token, which is too heavy for routine decisions. Humans already work in Forgejo issues and PRs (SPEC §101), authenticated by Authentik SSO.

## Decision
- A human requests a human-only transition by commenting `/sc <command>` (plus a reason and an optional YAML payload) on the task's tracking issue or PR. The Runner receives the `issue_comment` webhook (HMAC `SC-RUNNER-WEBHOOK`), and authorizes only if the sender is a member of a configured Forgejo team (`spec.humanAuthority.forgejoTeams`, default `Owners`, `engineers`), is not a machine account (`sc-runner`, `sc-woodpecker`, `sc-renovate`, `sc-backup`, `sc-admin`), the comment was created (not edited) on the linked issue/PR, and the rate limit holds.
- The Runner replies in the thread with the accepted transition or a rejection code, records `actor = human:<login>` and the payload, and posts the silent audit alert `SCHumanTaskDecision`.
- The loopback admin API `POST /v1/tasks/{id}/transitions` accepts the same commands as a break-glass path (`decision.command` required).

## Consequences
- Routine human decisions need no cluster credentials and leave a durable, visible record next to the work.
- The safety of the channel rests on Forgejo identity: Hermes holds no Forgejo account (CD-15), workers hold only deploy keys (ADR-0007), and commands are accepted only from human accounts (never `sc-*` machine accounts or bot users).
- Membership is checked live through the Forgejo API at command time (verify paths at C03).

## Alternatives rejected
- Hermes dashboard buttons: Hermes is untrusted; a prompt-injected Hermes could press them.
- Status labels edited by humans: labels are a Runner-owned projection, and label events carry no reason or payload.
- Admin API only: requires break-glass credentials for ordinary work.

## Verification
29-AC-30 and 29-AC-31 (GATE-C03): member command accepted; machine account, non-member and edited comments rejected; webhook payload fields and team-membership API re-verified on the pinned Forgejo version.
