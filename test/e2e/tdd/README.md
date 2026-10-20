# TDD trajectories

One YAML file per trajectory (plan 21 section 22.3): an event sequence, and the state, transition and reason the
task must show after each event. The file is consumed by two runners, so the library and the cluster are held to
the same trajectory:

* the **library runner** (F05; `make test-tdd-library`, Go tests `TestTrajectories_*` in
  `src/internal/statemachine`) replays the events against `contracts/state-machine.yaml` through `Apply` with a
  table-driven fake `Facts`, compares each step with the file, and validates the task record serialized after every
  applied step against `contracts/task.schema.yaml`;
* the **live runner** (C10, `sc-e2e run --suite c10`) drives the same events on the cluster with fake harnesses
  and compares the observed trajectory with `expected_*`. Fields under `live:` (top level or per event) belong to
  it; the library runner ignores them.

`src/internal/statemachine/trajectory` parses and validates the files (strict: an unknown key, a missing field or
unequal list lengths is an error, as is a directory without any trajectory file, a `*.yml` file in the directory,
or a file with more than one YAML document).

## Format

```yaml
trajectory: tdd-happy-path          # required; equals the file stem
title: ...                          # optional
refs: ["plan 21 22.2 ...", "29-AC-11"]   # plan row or acceptance criterion the file specifies
mode: test-first                    # test-first (default) | verification-first
policy: {resolutionApproval: human} # optional: spec.testSpecConflict.resolutionApproval, human (default) | adjudicator
task: {...}                         # optional overrides of the record fields the library does not own
events:                             # required, in order
  - event: dispatch_implementer     # required: an event of the table
    actor: hermes                   # required: runner | ci | hermes | human
    role: implementer               # dispatch events: the role being dispatched (Event.Role)
    at: "2026-10-13T12:05:00Z"      # required: RFC 3339, the fake clock; never before the previous event
    facts: {G_ATTEMPTS_REMAIN: true, G_VERIFICATION_OUTCOME: red_valid}
    dispatch: {harness: ..., model: ..., profile: ..., start_commit: ..., ac_version: 1, notes: ...}
    failure_code: JOB_LOST          # execution_failed: the terminal failure code
    failure_signature: TestX        # merit/policy failures: the failure signature
    verdict: test_invalid           # adjudication_recorded: the adjudicator verdict (StepInput.Verdict)
    reason: free text               # history reason of the record (default: event and transition id)
    pipeline: 412                   # events that start a verification: the pipeline number (claimed_by)
    live: {...}                     # ignored by the library runner
expected_states: [...]              # required, one per event: the task state after the event
expected_transitions: [...]         # required, one per event: the transition id, or "rejected"
expected_reasons: [...]             # required, one per event, "" when none
expected_failed_guards: [...]       # optional, one list per event (null = unchecked): the failed guards of a rejection
expected_attempts: {...}            # optional: final counters, dotted names
live: {...}                         # ignored by the library runner (fake scenarios, cluster assertions, evidence reasons)
```

* **Policy.** The library runner applies `policy` (`resolutionApproval`, default `human`) to every step.
  `tdd-adjudication-tighten-only` runs under `adjudicator`, the policy under which a lone `test_invalid` adjudication
  would itself approve the test change, so that TDD-20 (a later verdict never loosens an earlier one) is proved.
* **Failure codes.** `failure_code` is a code of plan 29 section 15.2 (for example `JOB_LOST`, `TIMED_OUT`,
  `RESULT_SCHEMA_INVALID` infrastructure; `TEST_TAMPER`, `PROTECTED_PATH_VIOLATION` policy).
* **Facts.** For every step the fake facts answer every base boolean guard `true` and no valued guard; `facts`
  overrides. A key is a guard id of the table (`G_X: false`, or `G_VERIFICATION_OUTCOME: red_valid` for the valued
  guards) or `<role>/<guard>` for a per-role boolean. Facts are per step; they never carry over. The counter and
  replay guards (attempts, reviews, auxiliary, identical replay, infrastructure requeue, failure code) are answered
  by the library from the task, not by `facts`. So are the task-state guards (D14): `G_NO_IMPLEMENTER_DISPATCHED_IN_GENERATION`,
  `G_STRENGTHEN_AUTHORIZED`, `G_TEST_CHANGE_APPROVED`, `G_CONFLICT_REJECTED`; a file must not supply them. Unknown guards fail the file.
* **Dispatch.** A dispatch event without `dispatch` gets a spec that differs from every earlier one (a new model
  and start commit), so none is an identical replay; give `dispatch` to test replay.
* **Rejected events.** `expected_transitions` is `rejected`, `expected_states` is the unchanged state (`""` when the
  task does not exist yet) and `expected_reasons` is the MCP code (`GATE_NOT_SATISFIED`, `ALREADY_RUNNING`,
  `ATTEMPTS_EXHAUSTED`). A rejected event is not recorded in the task history.
* **Reasons of applied events.** The NEEDS_HUMAN reason code of the transition (`E_NOTIFY_HUMAN`), the BLOCKED
  reason code (`emergency_stop`, `budget_window_exhausted`, `human_hold`) when the step parks the task, else `""`.
* **expected_attempts keys.** `autonomous_used`, `tester_used`, `reviews_used`, `auxiliary_used`,
  `consecutive_infrastructure`, `consecutive_timeouts`, `by_class.<class>`, `merit_by_generation.<role>:g<n>`.
* **Evidence sub-reasons** (`build_failed`, `existing_test_modified`, ...) belong to the F06 classification and the
  live runner; they appear under `live.expected_evidence_reason`.

## Files and plan 21 section 22.2

| File | Plan row |
|---|---|
| `tdd-gate-implementer-before-red` | implementer cannot start before RED_VERIFIED |
| `tdd-red-broken-test`, `tdd-red-compile-error-then-stub` | broken tests are not valid red evidence |
| `tdd-already-satisfied` | already-passing test produces investigation state |
| `tdd-tamper-delete` | implementation cannot silently delete acceptance tests |
| `tdd-tamper-weaken`, `-golden`, `-helper`, `-monkeypatch`, `-nested-module`, `tdd-phase-conditional`, `tdd-tamper-skip`, `-discovery`, `-env-detect` | weakened acceptance tests are detected (each: `tamper_detected` at GREEN, T036) |
| `tdd-happy-path` | valid RED, implementation, GREEN path (T001 T010 T014 T020 T030 T032 T033 T040 T041 T050) |
| `tdd-test-spec-conflict`, `tdd-conflict-rejected` | TEST_SPEC_CONFLICT is supported (and its variant) |
| `tdd-provenance-survives-retry` | TDD provenance survives retry |
| `tdd-exemption-not-in-policy`, `-auto-docs`, `-human-approval`, `-no-self` | exemption requires approved policy (`-human-approval`: T117, `G_APPROVAL:human`; `-no-self`: `RESULT_SCHEMA_INVALID`, infrastructure, T100) |
| `tdd-strengthen-after-blocked-refused`, `tdd-already-satisfied-needs-human`, `tdd-forged-trailer-commit`, `tdd-unapproved-test-change`, `tdd-protected-path-edit`, `tdd-pipeline-config-edit`, `tdd-merge-without-provenance`, `tdd-human-command-authz`, `tdd-exhaustion` | additional organizational tests |
| `tdd-review-truncated-input` (TDD-13), `tdd-adjudication-tighten-only` (TDD-20), `tdd-external-commit-hermes-refused` (TDD-9) | 29-AC-11 bypass trajectories without a row of their own |
| `tdd-retry-infra-refunds`, `tdd-retry-consecutive-timeouts`, `tdd-retry-emergency-stop` | 29-AC-7 golden retry sequences |

Not expressible as a library trajectory (no state-machine event sequence; the live runner covers them):

* `tdd-second-pr-other-branch`: the `tdd-provenance = failure` commit status of a second PR is a Forgejo status, the
  Runner emits no event.
* `tdd-foreign-pipeline-ignored`: "no transition" for a human-created pipeline; the Runner never produces an event.

Cluster-only assertions inside the included files (kubectl, Forgejo, S3 tags, alerts, the `fake-*` scenarios, the
evidence sub-reasons) are kept under `live:` and are not checked by the library runner.
