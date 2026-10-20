---
name: sc-node
description: Build one Strange Company DAG node (PLAN.md) with the approved process. Brief, then tester RED, verified RED, implementer GREEN, independent review, and finally gate evidence in STATUS.md. Use when asked to implement, build or start a node such as F01, or to continue with the next ready node when the operator has asked for consecutive nodes (step 8).
argument-hint: "<node-id, e.g. F01> [AC ids to limit the slice]"
---

# /sc-node $ARGUMENTS

You are the **orchestrator**: the Hermes analogue for Phase 1, before the platform exists. You never write tests or implementation yourself. You prepare context, dispatch fresh role agents (`sc-tester`, `sc-implementer`, `sc-reviewer`), check their evidence, push, and record results.

Authority: `SPEC.md`, then `plans/00-master-plan.md`, `plans/reviews/consolidation-decisions.md` and the owning plans. Follow PLAN.md's "How to execute a node". Phase-1 commits must satisfy plan 52 §8.3 (`tdd-discipline`) so that the check passes when it exists.

## 0. Preconditions
- The working tree is clean and `main` is current (`git pull`).
- The node exists in `plans/dag.yaml`. Every node in its `deps` has recorded gate evidence in `STATUS.md`. If not, stop and say which dependency is missing.
- Create the branch `node/<id>-<name>` from `main`. History stays linear: no merges, rebase onto `main` if needed.

## 1. Brief (keeps every agent's context small)
Write `out/sc-node/<id>/brief.md`. `out/` is gitignored from F01 on. Before F01 lands, add `out/` to `.git/info/exclude`. The brief contains:
- the node id, name, deps and owning plans, taken from PLAN.md;
- **every acceptance criterion in scope, copied verbatim** from the `### GATE-<id>` sections, with its plan path;
- for each owning plan, the section numbers that matter (outputs, interfaces, configuration, testing strategy), as `path §n` pointers rather than pasted text;
- referenced contracts, ADRs and CDs as pointers;
- the `(verify)` items the node relies on, plus their entries in `plans/compatibility-baseline.yaml`.

Split large nodes into **slices** of 3–8 related criteria. F01, for example, slices into the seed `ci` image and `./bootstrap exec` (first, because every Make target re-executes through it; plan 02 §14.9), layout-check, locks and CI workflow, lint and determinism, and TDD discipline. Each slice runs steps 2–5. Order the slices so earlier ones build what later tests need.

## 2. RED (per slice)
Dispatch a **new** `sc-tester` with the brief path and the slice's criterion IDs. Never resume an old agent: fresh contexts are the point, and they are cheaper.

Then verify the handoff yourself before accepting it:
- `git show --stat <red>` touches only test paths, plus `SC-STUB` additions under `src/`;
- run the red target at `<red>` and confirm a non-zero exit, assertion-class failures only, and only new or changed tests failing;
- the commit subject is `test(<id>): …` and it carries the `TDD-Red-Target:` trailer.

If the tester reports `already_satisfied`, record it and drop those criteria from the slice. If it reports `blocked`, go to step 7.

Push the branch with `git push -u origin HEAD` so the RED commit is addressable. If interim CI runs on the branch, record the failing run URL.

## 3. GREEN
Dispatch a **new** `sc-implementer` with the brief path, the RED SHA, the red target and the failing test IDs. Check its handoff:
- the red target passes at head;
- the full local suite passes, plus `make verify` once the Makefile exists;
- `python3 -I tools/stage0/validate.py` passes;
- `git diff <red>..HEAD -- <test paths>` shows no modified or deleted RED tests;
- `git grep -n 'SC-STUB('` finds nothing in the files the slice touched.

Push, wait for CI, and record the run URL and result. CI failures go back to a new implementer with the log excerpt. Retries are bounded (SPEC §74, CD-63): **at most 4 implementer attempts per slice**.
- Attempts 1–3 use the agent's default model (`model:` in `.claude/agents/sc-implementer.md`).
- Attempt 4 is an **escalation**: dispatch the new `sc-implementer` with the Agent tool's `model: "opus"` override. Give it the brief, the latest RED SHA and failing tests, and every review's findings and failed diff SHAs from attempts 1–3, so it does not replay identical work (SPEC §74). Record the escalation in `STATUS.md`.
- If attempt 4 is rejected or fails, go to step 7.

## 4. Review
Dispatch a **new** `sc-reviewer` with the brief path, the base, RED and head SHAs, and the evidence. On `request_changes`, give the findings to a new implementer; the retry budget still applies. On `test_spec_conflict`, go to step 7. Never edit tests to satisfy a reviewer: a wrong test means a new RED commit from a new tester, and the user approves that first.

## 5. Evidence
Append to `STATUS.md` under `## Implementation progress` → `### <id> — <name>`:
- the slices, each with its RED SHA, head SHA and CI run URLs;
- a table of criterion → test(s) → result;
- the review verdict;
- deviations and open questions.

A node is complete only when **every** gate criterion has passing evidence (SPEC §124). Commit this as a human-owned docs change: `docs(<id>): gate evidence`.

## 6. Pull request
When all slices are done, push and give the user a ready-to-paste PR title and body: summary, criteria table, RED → GREEN → review evidence, and deviations. End the body with the attribution line. Open it with `gh pr create` (it runs as `sc-agent-bot`, CD-46). If `gh` is unavailable, give the user the title and body to paste instead.

Then post the review status (plan 52 §8.2a, CD-47):
- Post it only if every slice's verdict is `approve`, and the last reviewed SHA equals the PR head or every later commit changes only `STATUS.md`. Otherwise dispatch a new reviewer for the head first.
- Add a PR comment with `gh pr comment` holding the verdicts, the reviewed SHAs and the criterion table.
- Post `gh api repos/<owner>/strange-company/statuses/<head-sha> -f state=success -f context=sc-review -f description='<summary>' -f target_url=<comment URL>`.
- Any later push to the PR needs a fresh review and a fresh status.

Give the user the PR URL. The user merges; you never approve or merge.

## 8. Next node (only when the operator asked for consecutive nodes)
Run this step only when the operator has explicitly asked you to keep going node after node (CD-69). Otherwise stop after step 6.
- Check the PR's CI first. If it fails, fix it with a new implementer (step 3's budget applies) before you leave the node.
- Pick the next node: the first node in PLAN.md order whose `deps` all have gate evidence in `STATUS.md` on `main`. A node that depends on an open, unmerged PR is not ready.
- If no node is ready, stop and report every open PR URL and what each waiting node needs.
- Otherwise invoke `/sc-node <next-id>` with the Skill tool and start again at step 0, from an updated `main`.
- Context is compacted automatically; the brief and evidence in `out/sc-node/<id>/` carry the state, so write them before you move on.
- Step 7's stop conditions still apply, and so does every rule above: you still never approve or merge.

## 7. Stop and ask the user
Stop for:
- a false `(verify)` assumption;
- an ambiguous or contradictory criterion;
- a `TEST_SPEC_CONFLICT`;
- an exhausted retry budget (after the attempt-4 escalation);
- a needed change to SPEC, a plan, an ADR or the DAG.

Propose the amendment and record a spec issue if it touches SPEC (`plans/spec-issues.md`). Do not continue on a guess.

## Cost discipline
- Hand agents file paths and section pointers, not pasted documents.
- One fresh agent per role per attempt. Never resume long transcripts.
- Run at most 2–3 agents at a time, and only for independent slices.
