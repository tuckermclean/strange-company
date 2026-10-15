package lint_test

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	"strange.company/sc/internal/lint"
)

// Rule IDs from plan 52 section 9.3.
const (
	ruleQ1 = "Q1"
	ruleQ2 = "Q2"
)

// headDate is the committer date of the fixture repositories' HEAD (D9: the only wall-clock input).
const headDate = "2026-10-15T12:00:00+00:00"

func qEntry(id, added, expires string) string {
	return fmt.Sprintf("    - {id: %q, target: test, reason: \"fails intermittently on CI runners\", issue: \"strange-company/strange-company#9\", added: %q, expires: %q, owner: \"bob\"}\n", id, added, expires)
}

func qFile(entries ...string) string {
	body := "apiVersion: strange.company/v1alpha1\nkind: TestQuarantine\nmetadata: {name: strange-company}\nspec:\n  maxEntries: 10\n  maxDays: 14\n"
	if len(entries) == 0 {
		return body + "  entries: []\n"
	}
	return body + "  entries:\n" + strings.Join(entries, "")
}

// quarantineTree commits policy/test-quarantine.yaml in a fresh repository whose HEAD committer date is headDate.
func quarantineTree(t *testing.T, content string) string {
	t.Helper()
	d := t.TempDir()
	write(t, d, "policy/test-quarantine.yaml", content)
	for _, args := range [][]string{{"init", "-q"}, {"add", "-A"}, {"commit", "-q", "-m", "fixture"}} {
		cmd := exec.Command("git", append([]string{"-c", "user.email=t@example.invalid", "-c", "user.name=t"}, args...)...)
		cmd.Dir = d
		cmd.Env = append(os.Environ(), "GIT_COMMITTER_DATE="+headDate, "GIT_AUTHOR_DATE="+headDate)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return d
}

func mustQuarantine(t *testing.T, dir string) lint.Result {
	t.Helper()
	r, err := lint.QuarantineCheck(dir)
	if err != nil {
		t.Fatalf("QuarantineCheck: %v", err)
	}
	return r
}

// 52-AC-7: the empty list and a single in-policy entry pass; Q1 and Q2 were evaluated.
func TestQuarantineCheck_ValidPasses_52AC7(t *testing.T) {
	for name, content := range map[string]string{
		"empty list":     qFile(),
		"one good entry": qFile(qEntry("strange.company/sc/src/internal/runner.TestFlaky", "2026-10-13", "2026-10-20")),
	} {
		t.Run(name, func(t *testing.T) {
			r := mustQuarantine(t, quarantineTree(t, content))
			ranRules(t, r, ruleQ1, ruleQ2)
			if f := errorsOf(r); len(f) != 0 {
				t.Errorf("findings: %v", f)
			}
		})
	}
}

// 52-AC-7: 11 entries, a 15-day entry, an entry expired against the HEAD committer date (Q1) and a
// quarantined TestSpec106ImplementerBlockedBeforeRed (Q2) each fail with the rule ID printed.
func TestQuarantineCheck_Negatives_52AC7(t *testing.T) {
	var eleven []string
	for i := 0; i < 11; i++ {
		eleven = append(eleven, qEntry(fmt.Sprintf("strange.company/sc/src/internal/runner.TestFlaky%d", i), "2026-10-13", "2026-10-20"))
	}
	cases := []struct {
		name, rule, content string
	}{
		{"11 entries", ruleQ1, qFile(eleven...)},
		{"15-day entry", ruleQ1, qFile(qEntry("strange.company/sc/src/internal/runner.TestFlaky", "2026-10-13", "2026-10-28"))},
		{"expired relative to HEAD committer date", ruleQ1, qFile(qEntry("strange.company/sc/src/internal/runner.TestFlaky", "2026-10-05", "2026-10-10"))},
		{"TestSpec106ImplementerBlockedBeforeRed", ruleQ2, qFile(qEntry("strange.company/sc/src/internal/statemachine.TestSpec106ImplementerBlockedBeforeRed", "2026-10-13", "2026-10-20"))},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := quarantineTree(t, c.content)
			r := mustQuarantine(t, d)
			if !hasRule(r, c.rule) {
				t.Errorf("want rule %s, got findings %v", c.rule, r.Findings)
			}
			var out, errb bytes.Buffer
			code := lint.Main([]string{"quarantine", "--root", d}, &out, &errb)
			if code != 1 || !strings.Contains(out.String(), c.rule) {
				t.Errorf("sc-lint quarantine: exit %d (want 1), output must name %s:\n%s%s", code, c.rule, out.String(), errb.String())
			}
		})
	}
}

// 52-AC-7: expiry is judged against the HEAD committer date, not the wall clock (D9): the same file passes
// when HEAD is dated before the expiry.
func TestQuarantineCheck_UsesHeadCommitterDate_52AC7(t *testing.T) {
	r := mustQuarantine(t, quarantineTree(t, qFile(qEntry("strange.company/sc/src/internal/runner.TestFlaky", "2026-10-13", "2026-10-15"))))
	ranRules(t, r, ruleQ1)
	if f := errorsOf(r); len(f) != 0 {
		t.Errorf("entry expiring on the HEAD committer date must pass: %v", f)
	}
}

// 52-AC-7: `make quarantine-check` passes on the committed (empty) list.
func TestRealTree_MakeQuarantineCheck_52AC7(t *testing.T) {
	runMake(t, "quarantine-check")
}

func TestRealTree_QuarantineListIsEmpty_52AC7(t *testing.T) {
	body := readFile(t, repoRoot(t), "policy/test-quarantine.yaml")
	if !strings.Contains(body, "kind: TestQuarantine") || !strings.Contains(body, "entries: []") {
		t.Errorf("policy/test-quarantine.yaml must be a TestQuarantine with `entries: []`:\n%s", body)
	}
}
