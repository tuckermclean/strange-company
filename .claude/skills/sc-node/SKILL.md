---
name: sc-node
description: Build one Strange Company DAG node (PLAN.md) with the approved process. Brief, then tester RED, verified RED, implementer GREEN, independent review, and finally gate evidence in STATUS.md. Use when asked to implement, build or start a node such as F01.
argument-hint: "<node-id, e.g. F01> [AC ids to limit the slice]"
disable-model-invocation: true
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

Push, wait for CI, and record the run URL and result. CI failures go back to a new implementer with the log excerpt. Retries are bounded: **at most 3 implementer attempts per slice** (SPEC §74). After that, go to step 7.

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
When all slices are done, push and give the user a ready-to-paste PR title and body: summary, criteria table, RED → GREEN → review evidence, and deviations. End the body with the attribution line. The user opens the PR and merges it; you never merge.

## 7. Stop and ask the user
Stop for:
- a false `(verify)` assumption;
- an ambiguous or contradictory criterion;
- a `TEST_SPEC_CONFLICT`;
- an exhausted retry budget;
- a needed change to SPEC, a plan, an ADR or the DAG.

Propose the amendment and record a spec issue if it touches SPEC (`plans/spec-issues.md`). Do not continue on a guess.

## Cost discipline
- Hand agents file paths and section pointers, not pasted documents.
- One fresh agent per role per attempt. Never resume long transcripts.
- Run at most 2–3 agents at a time, and only for independent slices.
