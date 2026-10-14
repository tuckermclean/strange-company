# External adversarial review of PR #138 (Stage 0)

**Received:** 2026-10-14, from the operator. **Disposition:** consolidation decisions CD-27..CD-34 (`consolidation-decisions.md`).

**Verdict:** two merge blockers, four substantial architectural issues, and two recovery or process concerns. PR #138 should not be merged unchanged, though it is substantially better than the usual AI-generated architecture output.

| # | Severity | Finding (summary) | Disposition |
|---|---|---|---|
| 1 | BLOCKER | `STATUS.md` claims structural checks pass, but the PR has no executable validator and no CI check, so the claims are prose. This contradicts "models propose, deterministic systems decide". Commit a validator that performs exactly the claimed checks, plus a GitHub Actions workflow. | CD-27 |
| 2 | BLOCKER | `perRequest.maxCostUsd` is not a maximum. Plans admit overrun beyond it (byte/token estimate), and shared scopes can overshoot by the sum of concurrent overruns. Either make it a real conservative ceiling with a provable token bound, or rename it and amend SPEC §81. | CD-28 (real ceiling) |
| 3 | MAJOR | Stage 0 is reasoned against moving third-party targets ("latest at implementation time"). Freeze a compatibility baseline now; an upgrade becomes an explicit plan-amendment task. | CD-29 |
| 4 | MAJOR | The Runner admin API trusts caller-supplied `X-SC-Actor`, so anyone with the shared admin bearer can claim any identity in the audit trail. Identity must come from the credential. | CD-30 |
| 5 | MAJOR | The seed→Flux rollback and fault cases (N4/N5/N6) run only on k3d with local-path in place of Longhorn. Add them to the disposable lab-VM gate with real Longhorn. | CD-31 |
| 6 | MAJOR | Two meanings of RTO: "GitHub makes the bytes readable" vs "Forgejo serving / factory working" (~2h10m / ~3h30m). Split into data-availability and service-restoration RTO. | CD-32 |
| 7 | MAJOR (acknowledged risk) | Black start depends on GitHub's Git service, GHCR, release assets and Pages. Eventually keep a bootstrap capsule in the recovery object-storage failure domain. | CD-33 |
| 8 | MAJOR (process) | Fresh-context reviews are not independent verification. Require the deterministic validator (#1), plus at least one review per dangerous area conducted without feeding in previous reviewers' conclusions. | CD-34 (blind reviews, different model family) |

**Reviewer's notes on strengths (kept for context):**
- The TDD machinery resisted attempts to find an "agent edits the tests until they pass" escape.
- The Woodpecker exclusive configuration extension goes well beyond "trust CI".
- The Model Gateway is stronger than the original idea. It denies provider-side state, file and URL sources, MCP, web search and n > 1; keys never reach workers; reservations are atomic; unknown usage is conservatively charged; and the Gateway makes no provider retries.

**Merge bar stated by the reviewer:** fix #1 and #2 before merge; fix #3 and #4 now while everything is still prose; amend gates and definitions for #5–#7.
