package statemachine

import "sort"

// The guard and effect registries: the identifiers this library implements. They must equal the catalogues of
// contracts/state-machine.yaml; Load rejects a table whose catalogue names one the library lacks (SM_GUARD_UNKNOWN,
// SM_EFFECT_UNKNOWN), and TestRegistries_CoverExactlyTheCatalogues_29AC3 rejects one the table lacks.

var guardIDs = []string{
	"G_ACCEPTANCE_CRITERIA_VALID", "G_ACTOR_AUTHORIZED", "G_ACTOR_NOT_MACHINE_ACCOUNT", "G_APPROVAL",
	"G_ATTEMPTS_EXHAUSTED", "G_ATTEMPTS_REMAIN", "G_AUXILIARY_REMAIN", "G_BASE_COMMIT_RESOLVED",
	"G_BLOCKED_RESULTS_EXHAUSTED", "G_BLOCKED_RESULTS_REMAIN", "G_BRANCH_EXISTS", "G_CHILDREN_TERMINAL",
	"G_COMMIT_PUSHED", "G_CONFLICTS_EXHAUSTED", "G_CONFLICTS_REMAIN", "G_CONFLICT_REJECTED",
	"G_DESCENDS_FROM_RED", "G_DISPATCH_PREREQUISITES", "G_EVIDENCE_AUTHENTIC", "G_EXECUTION_ACCEPTED",
	"G_EXEMPTION_GRANTED", "G_FAILURE_CLASS", "G_FAILURE_CODE", "G_GENERATIONS_REMAIN",
	"G_GREEN_EVIDENCE_AT_HEAD", "G_HAS_MERGEABLE_CHANGE", "G_HEAD_UNCHANGED", "G_INFRA_REQUEUE_ALLOWED",
	"G_INFRA_REQUEUE_EXHAUSTED", "G_INFRA_RETRIGGERS_EXHAUSTED", "G_INFRA_RETRIGGERS_REMAIN",
	"G_JUDGE_APPROVED", "G_JUDGE_ARTIFACT_INTACT", "G_JUDGE_DETERMINISTIC", "G_JUDGE_NONDETERMINISTIC",
	"G_JUDGE_SCOPE_VALID", "G_JUDGE_SOURCE", "G_MERGE_PROVENANCE_FAILED", "G_MERGE_PROVENANCE_VERIFIED",
	"G_NOT_IDENTICAL_REPLAY", "G_NO_ACTIVE_EXECUTION", "G_NO_IMPLEMENTER_DISPATCHED_IN_GENERATION",
	"G_NO_MERGEABLE_CHANGE", "G_NO_PRODUCER_DISPATCHED_IN_GENERATION", "G_NO_PRODUCTION_IN_GENERATION",
	"G_PRODUCER_ROLE_ALLOWED", "G_PRODUCTION_SCOPE_VALID", "G_PROVENANCE_COMPLETE",
	"G_RED_CANDIDATE_UNIMPLEMENTED", "G_RED_EVIDENCE_VALID", "G_RED_SCOPE_VALID", "G_REFUSAL_CODE",
	"G_REPOSITORY_MANAGED", "G_REPO_POLICY_VALID", "G_RESUME_STATE_VALID", "G_REVIEWED_COMMIT_IS_HEAD",
	"G_REVIEWS_REMAIN", "G_REVIEW_INPUT_COMPLETE", "G_REVIEW_VERDICT", "G_ROLE_AUXILIARY_HERE",
	"G_START_DESCENDS_FROM_RED", "G_STRENGTHEN_AUTHORIZED", "G_TEST_CHANGE_APPROVED", "G_TEST_MANIFEST_INTACT",
	"G_VERIFICATION_MODE_ALLOWED", "G_VERIFICATION_OUTCOME",
}

var effectIDs = []string{
	"E_APPROVE_TEST_CHANGE", "E_ARCHIVE_BRANCH", "E_ARCHIVE_GENERATION", "E_AUTHORIZE_STRENGTHEN",
	"E_CANCEL_ACTIVE_EXECUTION", "E_CANCEL_PENDING_VERIFICATION", "E_CLOSE_PR", "E_CLOSE_TRACKING",
	"E_COUNT_BYPASS", "E_COUNT_FAILURE", "E_CREATE_EXECUTION", "E_CREATE_TEST_MANIFEST",
	"E_DISCARD_UNVERIFIED_PUSH", "E_ENSURE_PR", "E_ENSURE_TRACKING_ISSUE", "E_FREEZE_ACCEPTANCE_CRITERIA",
	"E_FREEZE_BASE_COMMIT", "E_GRANT_ATTEMPTS", "E_NOTE_EVENT", "E_NOTIFY_HUMAN", "E_POST_STATUS",
	"E_QUARANTINE_HEAD", "E_RECORD_AC_VERSION", "E_RECORD_ADJUDICATION", "E_RECORD_AUXILIARY_OUTPUT",
	"E_RECORD_CANDIDATE_RED_COMMIT", "E_RECORD_CONFLICT", "E_RECORD_EVIDENCE", "E_RECORD_HUMAN_COMMITS",
	"E_RECORD_HUMAN_DECISION", "E_RECORD_IMPLEMENTATION_COMMIT", "E_RECORD_JUDGE_APPROVAL", "E_RECORD_MERGE",
	"E_RECORD_RED_COMMIT", "E_RECORD_RESUME_STATE", "E_RECORD_REVIEW", "E_RECORD_TEST_EXECUTION",
	"E_RECORD_VERIFICATION_DECISION", "E_REJECT_CONFLICT", "E_REQUEUE_EXECUTION", "E_RESUME", "E_SET_MODE",
	"E_START_GENERATION", "E_TRIGGER_VERIFICATION",
}

// valuedGuards are the guards whose fact is a value compared with the guard's comma-separated arguments.
var valuedGuards = map[string]bool{
	"G_VERIFICATION_OUTCOME": true, "G_FAILURE_CLASS": true, "G_FAILURE_CODE": true, "G_REFUSAL_CODE": true,
	"G_REVIEW_VERDICT": true, "G_JUDGE_SOURCE": true, "G_APPROVAL": true,
}

// derivedGuards maps a negation guard (catalogue: "Negation of <base>") to its base guard. Facts answer only the
// base guard; the negation is true exactly when the base fact is known and false, so a missing fact fails both.
var derivedGuards = map[string]string{
	"G_ATTEMPTS_EXHAUSTED":         "G_ATTEMPTS_REMAIN",
	"G_INFRA_RETRIGGERS_EXHAUSTED": "G_INFRA_RETRIGGERS_REMAIN",
	"G_CONFLICTS_EXHAUSTED":        "G_CONFLICTS_REMAIN",
	"G_MERGE_PROVENANCE_FAILED":    "G_MERGE_PROVENANCE_VERIFIED",
	"G_INFRA_REQUEUE_EXHAUSTED":    "G_INFRA_REQUEUE_ALLOWED",
	"G_BLOCKED_RESULTS_EXHAUSTED":  "G_BLOCKED_RESULTS_REMAIN",
	"G_NO_MERGEABLE_CHANGE":        "G_HAS_MERGEABLE_CHANGE",
	"G_JUDGE_NONDETERMINISTIC":     "G_JUDGE_DETERMINISTIC",
}

var guardSet, effectSet = toSet(guardIDs), toSet(effectIDs)

func toSet(l []string) map[string]bool {
	s := make(map[string]bool, len(l))
	for _, e := range l {
		s[e] = true
	}
	return s
}

// Guards is the library's guard implementation set; it implements GuardRegistry.
type Guards struct{}

// HasGuard reports whether id is an implemented guard identifier (without arguments).
func (Guards) HasGuard(id string) bool { return guardSet[id] }

// GuardIDs returns the implemented guard identifiers in ascending order.
func (Guards) GuardIDs() []string { return sorted(guardIDs) }

// Effects is the library's effect implementation set; it implements EffectRegistry.
type Effects struct{}

// HasEffect reports whether id is an implemented effect identifier (without arguments).
func (Effects) HasEffect(id string) bool { return effectSet[id] }

// EffectIDs returns the implemented effect identifiers in ascending order.
func (Effects) EffectIDs() []string { return sorted(effectIDs) }

func sorted(l []string) []string {
	out := append([]string(nil), l...)
	sort.Strings(out)
	return out
}
