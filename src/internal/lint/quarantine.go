package lint

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// Rule IDs of plan 52 section 9.3.
const (
	ruleQ1         = "Q1"
	ruleQ2         = "Q2"
	ruleQSchema    = "quarantine-schema"
	quarantineFile = "policy/test-quarantine.yaml"
)

// neverQuarantine is the Q2 pattern: protected acceptance tests (the schema carries the same pattern).
var neverQuarantine = regexp.MustCompile(`(^|[./])Test(Spec105|Spec106|AC[0-9]|Gate)`)

type quarantineDoc struct {
	Spec struct {
		MaxEntries int `yaml:"maxEntries"`
		MaxDays    int `yaml:"maxDays"`
		Entries    []struct {
			ID      string `yaml:"id"`
			Added   string `yaml:"added"`
			Expires string `yaml:"expires"`
		} `yaml:"entries"`
	} `yaml:"spec"`
}

// headCommitDate returns the committer date (YYYY-MM-DD, in the committer's zone) of HEAD: the only clock
// input of the check (plan 52 D9).
func headCommitDate(root string) (time.Time, error) {
	cmd := exec.Command("git", "-C", root, "log", "-1", "--format=%cI")
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return time.Time{}, fmt.Errorf("git log: %v: %s", err, strings.TrimSpace(errb.String()))
	}
	s := strings.TrimSpace(out.String())
	if len(s) < 10 {
		return time.Time{}, fmt.Errorf("no HEAD committer date (%q)", s)
	}
	return time.Parse("2006-01-02", s[:10])
}

// QuarantineCheck evaluates rules Q1 and Q2 of plan 52 section 9.3 on policy/test-quarantine.yaml under root,
// using the committer date of HEAD as "today".
func QuarantineCheck(root string) (Result, error) {
	var r Result
	r.ran(ruleQ1)
	r.ran(ruleQ2)
	r.ran(ruleQSchema)
	p := filepath.Join(root, quarantineFile)
	b, err := os.ReadFile(p)
	if err != nil {
		r.add(ruleQ1, quarantineFile, "cannot read: %v", err)
		return r, nil
	}
	vs, err := validateSchema(root, "test-quarantine.schema.yaml", "-", []string{p + ":TestQuarantine"})
	if err != nil {
		return r, err
	}
	for _, v := range vs {
		r.add(ruleQSchema, quarantineFile, "%s: %s", v.Path, v.Message)
	}
	var d quarantineDoc
	if err := yaml.Unmarshal(b, &d); err != nil {
		r.add(ruleQSchema, quarantineFile, "not parseable YAML: %v", err)
		return r, nil
	}
	today, err := headCommitDate(root)
	if err != nil {
		return r, err
	}
	max := d.Spec.MaxEntries
	if max > 10 || max == 0 && len(d.Spec.Entries) > 0 {
		max = 10
	}
	if len(d.Spec.Entries) > max {
		r.add(ruleQ1, quarantineFile, "%d entries exceed the limit of %d", len(d.Spec.Entries), max)
	}
	maxDays := d.Spec.MaxDays
	if maxDays > 14 || maxDays == 0 {
		maxDays = 14
	}
	for i, e := range d.Spec.Entries {
		where := fmt.Sprintf("entries[%d] %s", i, e.ID)
		if neverQuarantine.MatchString(e.ID) {
			r.add(ruleQ2, quarantineFile, "%s: a protected acceptance test must not be quarantined", where)
		}
		added, err1 := time.Parse("2006-01-02", e.Added)
		expires, err2 := time.Parse("2006-01-02", e.Expires)
		if err1 != nil || err2 != nil {
			r.add(ruleQ1, quarantineFile, "%s: added/expires must be YYYY-MM-DD dates", where)
			continue
		}
		if days := int(expires.Sub(added).Hours() / 24); days > maxDays || days < 0 {
			r.add(ruleQ1, quarantineFile, "%s: expires %d days after added (limit %d)", where, days, maxDays)
		}
		if expires.Before(today) {
			r.add(ruleQ1, quarantineFile, "%s: expired on %s (HEAD committer date %s)", where, e.Expires, today.Format("2006-01-02"))
		}
	}
	return r, nil
}
