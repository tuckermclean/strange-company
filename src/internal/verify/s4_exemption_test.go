package verify

import (
	"errors"
	"strings"
	"testing"

	"strange.company/sc/internal/policy"
)

// 22-AC-10 and the allowedExemptionClasses clause of 22-AC-11: ValidateExemption (plan 22 section 9.2). Real
// platform policy, real repository policy edited one field at a time, merged by the real loader.

const s4Revision = "sha256:4444444444444444444444444444444444444444444444444444444444444444"

const s4Justification = "docs only, no behaviour change"

func s4Request(class string) ExemptionRequest {
	return ExemptionRequest{Mode: "verification-first", Class: class, Justification: s4Justification}
}

func s4Validate(t testing.TB, plat policy.Platform, eff policy.Effective, repo string, req ExemptionRequest) (ExemptionDecision, error) {
	t.Helper()
	return ValidateExemption(ExemptionInput{Platform: plat, Effective: eff, Repository: repo, Request: req, PolicyRevision: s4Revision})
}

func s4WantNotAllowed(t testing.TB, label string, d ExemptionDecision, err error) {
	t.Helper()
	var ee *ExemptionError
	if !errors.As(err, &ee) {
		t.Errorf("%s: err = %v (decision %+v), want an *ExemptionError", label, err, d)
		return
	}
	if ee.Code != s4NotAllowedCode {
		t.Errorf("%s: error code = %q, want %s", label, ee.Code, s4NotAllowedCode)
	}
	if ee.Error() == "" || !strings.Contains(ee.Error(), s4NotAllowedCode) {
		t.Errorf("%s: Error() = %q, want it to carry %s", label, ee.Error(), s4NotAllowedCode)
	}
	if d != (ExemptionDecision{}) {
		t.Errorf("%s: a refusal returned the decision %+v", label, d)
	}
}

// 22-AC-10: documentation-only on a repository allowing it is granted/auto; with requireHumanApproval it is
// pending_approval/human; classes the platform marks human are pending_approval/human; every decision records the
// class and the policy revision.
func TestValidateExemption_Decisions_22AC10(t *testing.T) {
	plat, _ := s4Effective(t, s4PolicySpec{Name: "demo"})
	all := s4PlatformClasses(t, plat)
	spec := plat.Verification["spec"].(map[string]any)
	approval := map[string]string{}
	for _, c := range spec["exemptionClasses"].([]any) {
		m := c.(map[string]any)
		approval[m["name"].(string)] = m["approval"].(string)
	}
	type tc struct {
		name, class, status, approval string
		ps                            s4PolicySpec
	}
	cases := []tc{
		{"documentation-only_allowed", "documentation-only", "granted", "auto", s4PolicySpec{Name: "demo", Allowed: []string{"documentation-only"}}},
		{"documentation-only_require_human", "documentation-only", "pending_approval", "human", s4PolicySpec{Name: "demo", Allowed: []string{"documentation-only"}, RequireHuman: []string{"documentation-only"}}},
		{"require_human_for_another_class_only", "documentation-only", "granted", "auto", s4PolicySpec{Name: "demo", Allowed: []string{"documentation-only", "metadata"}, RequireHuman: []string{"metadata"}}},
		{"metadata_allowed", "metadata", "granted", "auto", s4PolicySpec{Name: "demo", Allowed: []string{"metadata"}}},
		{"metadata_require_human", "metadata", "pending_approval", "human", s4PolicySpec{Name: "demo", Allowed: []string{"metadata"}, RequireHuman: []string{"metadata"}}},
		{"research_allowed", "research", "granted", "auto", s4PolicySpec{Name: "demo", Allowed: []string{"research"}}},
	}
	for _, c := range all {
		if approval[c] == "human" {
			cc := c
			cases = append(cases, tc{"platform_human_" + cc, cc, "pending_approval", "human", s4PolicySpec{Name: "demo", Allowed: []string{cc}}})
		}
	}
	if len(cases) < 10 {
		t.Fatalf("setup: only %d cases; the platform must have human classes", len(cases))
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			plat, eff := s4Effective(t, c.ps)
			d, err := s4Validate(t, plat, eff, "o/demo", s4Request(c.class))
			if err != nil {
				t.Fatalf("ValidateExemption: %v", err)
			}
			want := ExemptionDecision{Class: c.class, Status: c.status, Approval: c.approval, PolicyRevision: s4Revision}
			if d != want {
				t.Errorf("decision = %+v, want %+v", d, want)
			}
		})
	}
}

// 22-AC-10: infrastructure-config on a repository whose allowedExemptionClasses omits it, and a class the platform
// does not define, are EXEMPTION_NOT_ALLOWED; so are spelling variants of an allowed class (no wildcard, case
// folding or trimming), a class that is only listed in requireHumanApproval, and the human classes of a
// repository that allows only auto ones.
func TestValidateExemption_NotAllowed_22AC10(t *testing.T) {
	plat, eff := s4Effective(t, s4PolicySpec{Name: "demo", Allowed: []string{"documentation-only", "metadata"}, RequireHuman: []string{"infrastructure-config"}})
	cases := []struct{ name, class string }{
		{"omitted_class", "infrastructure-config"},
		{"omitted_human_class", "migration"},
		{"omitted_catch_all_class", "benchmark"},
		{"omitted_refactor", "refactor"},
		{"unknown_class", "no-such-class"},
		{"unknown_class_similar", "documentation-only2"},
		{"wildcard_star", "*"},
		{"wildcard_double_star", "**"},
		{"wildcard_prefix", "documentation-*"},
		{"wildcard_question", "documentation-onl?"},
		{"upper_case", "Documentation-Only"},
		{"leading_space", " documentation-only"},
		{"trailing_space", "documentation-only "},
		{"trailing_newline", "documentation-only\n"},
		{"nul_suffix", "documentation-only\x00"},
		{"empty", ""},
		{"path_like", "../documentation-only"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d, err := s4Validate(t, plat, eff, "o/demo", s4Request(c.class))
			s4WantNotAllowed(t, c.class, d, err)
		})
	}
	// Twins: the allowed classes still validate in the same repository.
	for _, class := range []string{"documentation-only", "metadata"} {
		if d, err := s4Validate(t, plat, eff, "o/demo", s4Request(class)); err != nil || d.Status != "granted" {
			t.Errorf("twin %s: decision %+v, err %v, want granted", class, d, err)
		}
	}
}

// 22-AC-11 (TDD-12): a repository policy without allowedExemptionClasses refuses every class, and so does an empty
// list; an unknown class is refused in a repository that allows every platform class.
func TestValidateExemption_AbsentOrEmptyAllowListRefusesEveryClass_22AC11(t *testing.T) {
	plat, _ := s4Effective(t, s4PolicySpec{Name: "demo"})
	all := s4PlatformClasses(t, plat)
	for name, ps := range map[string]s4PolicySpec{
		"absent":                 {Name: "demo"},
		"empty_list":             {Name: "demo", AllowedEmpty: true},
		"absent_with_human_only": {Name: "demo", RequireHuman: []string{"documentation-only"}},
		"strange_company_file":   {Name: "strange-company"},
	} {
		plat, eff := s4Effective(t, ps)
		for _, class := range all {
			d, err := s4Validate(t, plat, eff, "o/demo", s4Request(class))
			s4WantNotAllowed(t, name+"/"+class, d, err)
		}
	}
	plat, eff := s4Effective(t, s4PolicySpec{Name: "demo", Allowed: all})
	for _, class := range all {
		if d, err := s4Validate(t, plat, eff, "o/demo", s4Request(class)); err != nil || d.Class != class {
			t.Errorf("twin: repository allowing every class: %s -> %+v, %v", class, d, err)
		}
	}
	d, err := s4Validate(t, plat, eff, "o/demo", s4Request("no-such-class"))
	s4WantNotAllowed(t, "unknown class in a permissive repository", d, err)
}

// 22-AC-10 (plan 22 section 9.2 step 2): the repository must match the class's repositories globs; a class without
// the key is usable in every repository. The platform is the real one with `repositories` added to two classes.
func TestValidateExemption_RepositoryGlobs_22AC10(t *testing.T) {
	plat, eff := s4Effective(t, s4PolicySpec{Name: "demo", Allowed: []string{"documentation-only", "metadata"}})
	plat.Verification = s4Clone(t, plat.Verification)
	for _, c := range plat.Verification["spec"].(map[string]any)["exemptionClasses"].([]any) {
		m := c.(map[string]any)
		if m["name"] == "documentation-only" {
			m["repositories"] = s4Strings("acme/*", "tools/de*")
		}
	}
	cases := []struct {
		repo    string
		class   string
		allowed bool
	}{
		{"acme/demo", "documentation-only", true},
		{"tools/demo", "documentation-only", true},
		{"tools/other", "documentation-only", false},
		{"other/demo", "documentation-only", false},
		{"Acme/demo", "documentation-only", false},
		{"acme/demo/extra", "documentation-only", false},
		{"acme", "documentation-only", false},
		{"", "documentation-only", false},
		{"other/demo", "metadata", true}, // no repositories key: every repository
		{"acme/demo", "metadata", true},
	}
	for _, c := range cases {
		d, err := s4Validate(t, plat, eff, c.repo, s4Request(c.class))
		if c.allowed {
			if err != nil || d.Status != "granted" {
				t.Errorf("%q / %s: decision %+v, err %v, want granted", c.repo, c.class, d, err)
			}
			continue
		}
		s4WantNotAllowed(t, c.repo+" / "+c.class, d, err)
	}
}

// 22-AC-10 (plan 22 section 9.2 step 1): the mode and the justification. A test-first task has no exemption; a
// verification-first request needs a justification of at least 20 characters.
func TestValidateExemption_ModeAndJustification_22AC10(t *testing.T) {
	plat, eff := s4Effective(t, s4PolicySpec{Name: "demo", Allowed: []string{"documentation-only"}})
	const nineteen = "abcdefghijklmnopqrs"
	const twenty = "abcdefghijklmnopqrst"
	if len(nineteen) != 19 || len(twenty) != 20 {
		t.Fatal("setup: boundary strings")
	}
	req := func(mode, just string) ExemptionRequest {
		return ExemptionRequest{Mode: mode, Class: "documentation-only", Justification: just}
	}
	for name, r := range map[string]ExemptionRequest{
		"justification_19":     req("verification-first", nineteen),
		"justification_empty":  req("verification-first", ""),
		"test_first_request":   req("test-first", twenty),
		"mode_empty":           req("", twenty),
		"mode_upper_case":      req("Verification-First", twenty),
		"mode_unknown":         req("exempt", twenty),
		"mode_with_whitespace": req("verification-first ", twenty),
	} {
		d, err := s4Validate(t, plat, eff, "o/demo", r)
		if err == nil || d != (ExemptionDecision{}) {
			t.Errorf("%s: decision %+v, err %v, want an error and no decision", name, d, err)
		}
	}
	d, err := s4Validate(t, plat, eff, "o/demo", req("verification-first", twenty))
	if err != nil || d.Status != "granted" || d.Approval != "auto" {
		t.Errorf("justification of exactly 20 characters: decision %+v, err %v, want granted/auto", d, err)
	}
}

// 22-AC-10: the validation is a pure function of its inputs; it modifies neither policy.
func TestValidateExemption_PureAndDeterministic_22AC10(t *testing.T) {
	plat, eff := s4Effective(t, s4PolicySpec{Name: "demo", Allowed: []string{"documentation-only", "migration"}, RequireHuman: []string{"documentation-only"}})
	before := jsonOf(plat) + jsonOf(eff)
	var first ExemptionDecision
	for i := 0; i < 5; i++ {
		d, err := s4Validate(t, plat, eff, "o/demo", s4Request("documentation-only"))
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = d
		} else if d != first {
			t.Fatalf("run %d: %+v differs from %+v", i, d, first)
		}
	}
	if first.Status != "pending_approval" {
		t.Errorf("decision = %+v, want pending_approval", first)
	}
	if after := jsonOf(plat) + jsonOf(eff); after != before {
		t.Error("ValidateExemption modified its inputs")
	}
}
