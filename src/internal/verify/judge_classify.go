package verify

import (
	"fmt"
	"sort"
	"strings"

	"strange.company/sc/internal/policy"
)

// Outcomes of the judge classifications.
const (
	outcomeJudgeValid   = "judge_valid"
	outcomeJudgeInvalid = "judge_invalid"
	outcomeJudgingPass  = "judging_pass"
	outcomeJudgingFail  = "judging_fail"
)

// judgeClass is the judge definition of an exemption class of the platform policy.
type judgeClass struct {
	name          string
	kind          string
	source        string
	artifactPaths []string
}

func loadJudgeClass(plat policy.Platform, name string) (judgeClass, error) {
	spec, err := specMap(plat)
	if err != nil {
		return judgeClass{}, err
	}
	raw, _ := spec["exemptionClasses"].([]any)
	for _, r := range raw {
		m, ok := r.(map[string]any)
		if !ok || m["name"] != name {
			continue
		}
		j, ok := m["judge"].(map[string]any)
		if !ok {
			return judgeClass{}, fmt.Errorf("exemption class %q has no judge", name)
		}
		c := judgeClass{name: name}
		c.kind, _ = j["kind"].(string)
		c.source, _ = j["source"].(string)
		if c.artifactPaths, err = stringList(j["artifactPaths"]); err != nil {
			return judgeClass{}, fmt.Errorf("class %s judge.artifactPaths: %w", name, err)
		}
		if c.kind == "" || (c.source != "artifact" && c.source != "acceptance-criteria") {
			return judgeClass{}, fmt.Errorf("exemption class %q has an unusable judge definition", name)
		}
		return c, nil
	}
	return judgeClass{}, fmt.Errorf("exemption class %q is not defined by the platform policy", name)
}

func judgeCheckMap(what string, checks []JudgeCheck) (map[string]string, error) {
	out := map[string]string{}
	for _, c := range checks {
		switch c.Status {
		case "pass", "fail", "error":
		default:
			return nil, infraErr("%s: judge check %q has status %q", what, c.ID, c.Status)
		}
		if _, dup := out[c.ID]; dup {
			return nil, infraErr("%s: judge check %q is reported twice", what, c.ID)
		}
		out[c.ID] = c.Status
	}
	return out, nil
}

// headRunsOf returns the head runs of a document and requires each to be for the head commit.
func headRunsOf(what, head string, runs []RunSummary) error {
	n := 0
	for _, r := range runs {
		switch r.Tree {
		case "base":
		case "head":
			if r.Commit != head {
				return infraErr("%s: head run of suite %q is for commit %q, not %q", what, r.Suite, r.Commit, head)
			}
			n++
		default:
			return infraErr("%s: run of suite %q has unknown tree %q", what, r.Suite, r.Tree)
		}
	}
	if n == 0 {
		return infraErr("%s: the document has no head run", what)
	}
	return nil
}

// ClassifyJudge classifies the judge validation of a verification-first task (plan 22 section 14.8, "Judge
// validation"): the RED of a judge. It is pure and deterministic. Identity failure and unusable or inconsistent
// facts are errors carrying a *NotClassifiedError (verification_infra_failed), never a classification. An artifact
// that does not validate or does not belong to the task is judge_invalid, not an error.
func ClassifyJudge(in JudgeInput) (RedClassification, error) {
	if !in.Identity.Passed {
		return RedClassification{}, &NotClassifiedError{Code: "verification_infra_failed"}
	}
	obs := in.Observed
	if obs.Phase != "red" || obs.Subject != "judge" {
		return RedClassification{}, infraErr("observed document is phase %q subject %q, want red/judge", obs.Phase, obs.Subject)
	}
	if !objectIDRE.MatchString(obs.HeadSha) || !objectIDRE.MatchString(obs.BaseSha) {
		return RedClassification{}, infraErr("observed head_sha %q or base_sha %q is not a commit id", obs.HeadSha, obs.BaseSha)
	}
	if err := headRunsOf("observed", obs.HeadSha, obs.Runs); err != nil {
		return RedClassification{}, err
	}
	cls, err := loadJudgeClass(in.Platform, in.TaskClass)
	if err != nil {
		return RedClassification{}, infraErr("%v", err)
	}
	if cls.source != "artifact" {
		return RedClassification{}, infraErr("class %s is judged on its acceptance criteria, it has no judge to validate", cls.name)
	}
	observed, err := judgeCheckMap("observed", obs.JudgeChecks)
	if err != nil {
		return RedClassification{}, err
	}
	seenAC := map[string]bool{}
	for _, c := range in.Criteria {
		if seenAC[c.ID] || (c.Kind != "change" && c.Kind != "preservation") {
			return RedClassification{}, infraErr("criterion %q is duplicated or has kind %q", c.ID, c.Kind)
		}
		seenAC[c.ID] = true
	}
	characterization := cls.kind == "characterization"
	var baseChecks map[string]string
	if characterization {
		b := in.BaseObserved
		if b == nil {
			return RedClassification{}, infraErr("a characterization judge needs the checks run at base")
		}
		if b.Phase != "red" || b.Subject != "judge" || b.TaskID != obs.TaskID || b.Repository != obs.Repository {
			return RedClassification{}, infraErr("base observation is %s/%s phase %q subject %q", b.Repository, b.TaskID, b.Phase, b.Subject)
		}
		if b.ValidationBlob == "" || b.ValidationBlob != obs.ValidationBlob || b.IntentID == "" || b.IntentID != obs.IntentID {
			return RedClassification{}, infraErr("base observation is not bound to the judge observation (validation blob or intent id differs)")
		}
		if b.HeadSha != obs.BaseSha {
			return RedClassification{}, infraErr("base observation is for commit %q, not the base %q", b.HeadSha, obs.BaseSha)
		}
		if err := headRunsOf("base observation", b.HeadSha, b.Runs); err != nil {
			return RedClassification{}, err
		}
		if baseChecks, err = judgeCheckMap("base observation", b.JudgeChecks); err != nil {
			return RedClassification{}, err
		}
	}

	var invalid []string
	if obs.ValidationBlob == "" || obs.ValidationBlob != in.Identity.BaseValidationBlob {
		invalid = append(invalid, "validation_config_mismatch")
	}
	// Scope: the judge commit changes only files under the class's artifact paths, none of them protected.
	protected, err := protectedPaths(RedInput{Platform: in.Platform, Effective: in.Effective})
	if err != nil {
		return RedClassification{}, infraErr("%v", err)
	}
	inScope := matchAny(cls.artifactPaths, in.Judge.Path) && !matchAny(protected, in.Judge.Path)
	for _, f := range in.Scope.ChangedFiles {
		if !matchAny(cls.artifactPaths, f.Path) || matchAny(protected, f.Path) {
			inScope = false
		}
	}
	if !inScope {
		invalid = append(invalid, "scope_violation")
	}

	doc, perr := parseJudgeArtifact(in.Judge.Content)
	artifactOK := perr == nil && doc.Task == obs.TaskID && doc.Class == cls.name && doc.Kind == cls.kind
	if !artifactOK {
		// Schema, task, class and kind: the judge cannot be run as the task's judge.
		invalid = append(invalid, "judge_check_error")
	}
	var acResults []ACResult
	var newTests []string
	if artifactOK {
		defined := map[string]bool{}
		for _, c := range doc.Checks {
			defined[c.ID] = true
		}
		for _, id := range sortedStatusKeys(observed) {
			if !defined[id] {
				return RedClassification{}, infraErr("observed judge check %q is not in the judge artifact", id)
			}
		}
		for _, id := range sortedStatusKeys(baseChecks) {
			if !defined[id] {
				return RedClassification{}, infraErr("base judge check %q is not in the judge artifact", id)
			}
		}
		covers := map[string][]string{}
		for _, c := range doc.Checks {
			for _, ac := range c.ACIDs {
				covers[ac] = append(covers[ac], c.ID)
				if !seenAC[ac] {
					invalid = appendUnique(invalid, "ac_not_covered")
				}
			}
		}
		for _, c := range in.Criteria {
			if len(covers[c.ID]) == 0 {
				invalid = appendUnique(invalid, "ac_not_covered")
			}
		}
		// Every check ran: observed and not in error (a rubric judge has no CI evaluation).
		if doc.Kind != "rubric" {
			for _, c := range doc.Checks {
				if st, ok := observed[c.ID]; !ok || st == "error" {
					invalid = appendUnique(invalid, "judge_check_error")
				}
			}
		}
		if characterization {
			for _, c := range doc.Checks {
				if observed[c.ID] != "pass" || baseChecks[c.ID] != "pass" {
					invalid = appendUnique(invalid, "judge_check_error")
				}
			}
		}
		for _, c := range doc.Checks {
			newTests = append(newTests, "judge::"+c.ID)
		}
		sort.Strings(newTests)
		allPass, nonePass := true, true
		for _, c := range in.Criteria {
			r := ACResult{ACID: c.ID, Kind: c.Kind, Tests: []string{}, Outcome: "passing"}
			for _, id := range covers[c.ID] {
				r.Tests = append(r.Tests, "judge::"+id)
				if observed[id] != "pass" {
					r.Outcome = "failing_assertion"
				}
			}
			if len(r.Tests) == 0 {
				r.Outcome = "missing"
			}
			if r.Outcome == "passing" {
				nonePass = false
			} else {
				allPass = false
			}
			acResults = append(acResults, r)
		}
		if len(invalid) == 0 && !characterization && doc.Kind != "rubric" && len(in.Criteria) > 0 {
			switch {
			case allPass:
				return judgeResult(in, outcomeAlreadySatisfied, []string{"judge_passes_on_base"}, acResults, newTests), nil
			case !nonePass:
				return judgeResult(in, outcomePartiallySatisfied, []string{"judge_passes_on_base"}, acResults, newTests), nil
			}
		}
	}
	if len(invalid) > 0 {
		return judgeResult(in, outcomeJudgeInvalid, orderReasons(invalid, []string{"validation_config_mismatch", "scope_violation", "judge_check_error", "ac_not_covered"}), acResults, newTests), nil
	}
	return judgeResult(in, outcomeJudgeValid, []string{}, acResults, newTests), nil
}

func orderReasons(got, order []string) []string {
	var out []string
	for _, r := range order {
		if containsString(got, r) {
			out = append(out, r)
		}
	}
	return out
}

func sortedBoolKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedStatusKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func judgeResult(in JudgeInput, outcome string, reasons []string, acs []ACResult, tests []string) RedClassification {
	if acs == nil {
		acs = []ACResult{}
	}
	if tests == nil {
		tests = []string{}
	}
	return RedClassification{Outcome: outcome, Reasons: reasons, ACResults: acs, NewTests: tests,
		ComputedBy: "runner", ComputedAt: in.ComputedAt, PolicyRevision: in.PolicyRevision, VerifierLibVersion: verifierLibVersion}
}

// ClassifyJudging classifies the judging of a produced head (plan 22 section 14.8, "Judging"): the GREEN of a
// judge. It is pure and deterministic. Rules in order: validation blob, descent from the judge commit, judge
// artifact intact (tamper_detected), producer diff within the class paths (CheckScope), every judge check passes,
// required checks pass. The first failing rule decides the outcome, every failing rule adds its reasons. Facts the
// library can recompute (blob equality of the artifact files) are recomputed, never trusted.
func ClassifyJudging(in JudgingInput) (GreenClassification, error) {
	if !in.Identity.Passed {
		return GreenClassification{}, &NotClassifiedError{Code: "verification_infra_failed"}
	}
	obs := in.Observed
	if obs.Phase != "green" || obs.Subject != "judge" {
		return GreenClassification{}, infraErr("observed document is phase %q subject %q, want green/judge", obs.Phase, obs.Subject)
	}
	if !objectIDRE.MatchString(obs.HeadSha) || !objectIDRE.MatchString(obs.BaseSha) {
		return GreenClassification{}, infraErr("observed head_sha %q or base_sha %q is not a commit id", obs.HeadSha, obs.BaseSha)
	}
	if err := headRunsOf("observed", obs.HeadSha, obs.Runs); err != nil {
		return GreenClassification{}, err
	}
	if in.Exemption == nil {
		return GreenClassification{}, infraErr("no exemption decision is recorded on the task")
	}
	cls, err := loadJudgeClass(in.Platform, in.Exemption.Class)
	if err != nil {
		return GreenClassification{}, infraErr("%v", err)
	}
	violations, err := CheckScope(ScopeInput{Role: "producer", Entries: in.Diff, Exemption: in.Exemption, Platform: in.Platform, Effective: in.Effective})
	if err != nil {
		return GreenClassification{}, infraErr("%v", err)
	}
	observed, err := judgeCheckMap("observed", obs.JudgeChecks)
	if err != nil {
		return GreenClassification{}, err
	}

	var groups []reasonGroup
	add := func(outcome string, reasons ...string) {
		if len(reasons) > 0 {
			groups = append(groups, reasonGroup{outcome: outcome, reasons: reasons})
		}
	}
	if obs.ValidationBlob == "" || obs.ValidationBlob != in.Identity.BaseValidationBlob {
		add(outcomeJudgingFail, "validation_config_mismatch")
	}

	var judgeFail bool
	if cls.source == "acceptance-criteria" {
		// The research class: no artifact, no manifest; the report is the evidence.
		if len(in.Criteria) == 0 {
			return GreenClassification{}, infraErr("class %s has no frozen acceptance criteria to judge the report on", cls.name)
		}
		seenAC := map[string]bool{}
		for _, c := range in.Criteria {
			if c.ID == "" || seenAC[c.ID] || (c.Kind != "change" && c.Kind != "preservation") {
				return GreenClassification{}, infraErr("criterion %q is empty, duplicated or has kind %q", c.ID, c.Kind)
			}
			seenAC[c.ID] = true
		}
		if in.Report == nil {
			return GreenClassification{}, infraErr("class %s is judged on a report and none was given", cls.name)
		}
		if len(observed) > 0 {
			return GreenClassification{}, infraErr("class %s has no judge checks, the observation reports %d", cls.name, len(observed))
		}
		judgeFail = !reportAddresses(in.Report.Content, in.Criteria)
	} else {
		m := in.Manifest
		if m == nil {
			return GreenClassification{}, infraErr("class %s needs the judge manifest", cls.name)
		}
		if m.Kind != "judge" || m.TaskID != obs.TaskID || m.Repository != obs.Repository || m.BaseCommit != obs.BaseSha {
			return GreenClassification{}, infraErr("manifest is %s %s/%s base %q, the observation %s/%s base %q", m.Kind, m.Repository, m.TaskID, m.BaseCommit, obs.Repository, obs.TaskID, obs.BaseSha)
		}
		if d, err := manifestDigest(*m); err != nil || d != m.Digest {
			return GreenClassification{}, infraErr("manifest digest %q does not match its content", m.Digest)
		}
		if in.Integrity.RedCommit != m.TestCommit || in.Integrity.ManifestDigest != m.Digest || in.Integrity.Generation != m.Generation {
			return GreenClassification{}, infraErr("integrity facts are for another manifest (generation %d, judge commit %q, digest %q)", in.Integrity.Generation, in.Integrity.RedCommit, in.Integrity.ManifestDigest)
		}
		if len(m.Tests) == 0 || len(m.Files) == 0 {
			return GreenClassification{}, infraErr("the judge manifest lists %d tests and %d files, it is unusable", len(m.Tests), len(m.Files))
		}
		want := map[string]bool{}
		var ids []string
		for _, t := range m.Tests {
			id := strings.TrimPrefix(t.ID, "judge::")
			if id == t.ID || want[id] {
				return GreenClassification{}, infraErr("manifest test %q is not a judge check or is listed twice", t.ID)
			}
			want[id] = true
			ids = append(ids, id)
		}
		for _, id := range sortedStatusKeys(observed) {
			if !want[id] {
				return GreenClassification{}, infraErr("observed judge check %q is not in the judge manifest", id)
			}
		}
		if !in.Integrity.HeadDescendsFromRed {
			add(outcomeTamper, "not_descendant_of_red")
		}
		// The artifact files are byte-identical to the manifest: recomputed from the blobs, status not trusted.
		facts := map[string]IntegrityFile{}
		for _, f := range in.Integrity.ManifestFiles {
			facts[f.Path] = f
		}
		for _, mf := range m.Files {
			f, ok := facts[mf.Path]
			if !ok || f.Status != "intact" || f.ActualBlob == nil || *f.ActualBlob != mf.BlobSha || f.ExpectedBlob != mf.BlobSha {
				add(outcomeTamper, "judge_artifact_changed")
				break
			}
		}
		judgeFail = false
		for _, id := range ids {
			if observed[id] != "pass" {
				judgeFail = true
			}
		}
	}
	// The integrity facts of G2 that do not depend on the manifest: foreign commits, violation-severity static findings,
	// discovery changes (reasons as ClassifyGreen gives them); warning-only findings are recorded as warnings.
	var warnings []string
	got := map[string]bool{}
	if err := g2Facts(in.Integrity, got, &warnings); err != nil {
		return GreenClassification{}, err
	}
	add(outcomeTamper, orderReasons(sortedBoolKeys(got), g2Order)...)
	if len(violations) > 0 {
		// The producer diff leaves the class paths: the plan names no green reason, the nearest is a changed pipeline input.
		add(outcomeJudgingFail, "required_check_failed")
	}
	if judgeFail {
		add(outcomeJudgingFail, "judge_check_failed")
	}
	required := map[string]bool{}
	for _, n := range in.RequiredChecks {
		required[n] = true
	}
	ran := map[string]CheckRun{}
	for _, c := range obs.Checks {
		if _, dup := ran[c.Name]; dup {
			return GreenClassification{}, infraErr("check %q is reported twice", c.Name)
		}
		ran[c.Name] = c
		if c.Required {
			required[c.Name] = true
		}
	}
	for _, n := range sortedKeys(required) {
		if c, ok := ran[n]; !ok || c.ExitCode != 0 || c.TimedOut {
			add(outcomeJudgingFail, "required_check_failed")
			break
		}
	}

	cl := GreenClassification{
		Outcome: outcomeJudgingPass, Reasons: []string{},
		ComputedBy: "runner", ComputedAt: in.ComputedAt, PolicyRevision: in.PolicyRevision, VerifierLibVersion: verifierLibVersion,
		Warnings: warnings,
	}
	if len(groups) > 0 {
		cl.Outcome = groups[0].outcome
		for _, g := range groups {
			cl.Reasons = appendUnique(cl.Reasons, g.reasons...)
		}
	}
	return cl, nil
}
