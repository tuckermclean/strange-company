package verify

import "strange.company/sc/internal/policy"

// JudgeCheck mirrors the judge_checks items of the red-result and green-result observations.
type JudgeCheck struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

// JudgeFile is the judge artifact as committed (YAML text, format JudgeArtifact of verification.schema.yaml).
type JudgeFile struct {
	Path    string
	Content []byte
}

// ResearchReport is the report of a class whose judge source is acceptance-criteria (the research class).
type ResearchReport struct {
	Path    string
	Content []byte
}

// JudgeInput is everything ClassifyJudge needs. Observed is the judge-validation observation at the judge commit
// (phase red, subject judge; the judge checks ran at its head). BaseObserved is the same checks run at the base
// revision (Observed.BaseSha); it is required for a characterization judge and ignored otherwise. TaskClass is the
// exemption class granted to the task, Judge the artifact at the judge commit, Criteria the frozen acceptance
// criteria, Scope the Runner's changed files of the judge commit.
type JudgeInput struct {
	Observed       Observation
	BaseObserved   *Observation
	Identity       Identity
	TaskClass      string
	Judge          JudgeFile
	Criteria       []Criterion
	Scope          Scope
	Platform       policy.Platform
	Effective      policy.Effective
	ComputedAt     string
	PolicyRevision string
}

// JudgingInput is everything ClassifyJudging needs. Observed is the judging observation of the produced head (phase
// green, subject judge). Manifest is the judge manifest (kind judge: files are the judge artifact files, tests are
// `judge::<check id>`); it is nil only for a class whose judge source is acceptance-criteria, which is judged on
// Report. Integrity holds the Runner's facts about the manifest files, Diff the producer diff red..head (DeriveDiff
// entries), Exemption the decision recorded on the task.
type JudgingInput struct {
	Observed       GreenObservation
	Identity       Identity
	Manifest       *Manifest
	Integrity      Integrity
	Diff           []DiffEntry
	Exemption      *ExemptionDecision
	Criteria       []Criterion
	Report         *ResearchReport
	RequiredChecks []string
	Platform       policy.Platform
	Effective      policy.Effective
	ComputedAt     string
	PolicyRevision string
}
