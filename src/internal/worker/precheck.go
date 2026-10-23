package worker

import (
	"fmt"
	"strings"

	"strange.company/sc/internal/policy"
	"strange.company/sc/internal/scrub"
)

// change is one entry of `git diff --cached --name-status -M -z`.
type change struct {
	status  byte
	path    string
	oldPath string // renames and copies
}

// parseNameStatus parses the -z output of `git diff --name-status -M`.
func parseNameStatus(b []byte) []change {
	parts := strings.Split(string(b), "\x00")
	var out []change
	for i := 0; i < len(parts); {
		st := parts[i]
		if st == "" {
			i++
			continue
		}
		c := change{status: st[0]}
		if (st[0] == 'R' || st[0] == 'C') && i+2 < len(parts) {
			c.oldPath, c.path = parts[i+1], parts[i+2]
			i += 3
		} else if i+1 < len(parts) {
			c.path = parts[i+1]
			i += 2
		} else {
			break
		}
		out = append(out, c)
	}
	return out
}

// violation is one failed pre-check rule; rule is the packet policy key (or no_changes).
type violation struct {
	rule  string
	paths []string
	note  string
}

func (v violation) String() string {
	s := v.rule
	if v.note != "" {
		s += " (" + v.note + ")"
	}
	if len(v.paths) > 0 {
		p := v.paths
		more := ""
		if len(p) > 20 {
			more = fmt.Sprintf(" and %d more", len(p)-20)
			p = p[:20]
		}
		s += ": " + strings.Join(p, ", ") + more
	}
	return s
}

func matchAny(globs []string, path string) bool {
	for _, g := range globs {
		if policy.MatchGlob(g, path) {
			return true
		}
	}
	return false
}

// runPrechecks mirrors the advisory pre-checks of plan 27 section 14.10 step 4.
func runPrechecks(pol fullPolicy, changes []change, patchBytes int, required bool) []violation {
	var out []violation
	touched := func(c change) []string {
		if c.oldPath != "" {
			return []string{c.path, c.oldPath}
		}
		return []string{c.path}
	}
	var protected []string
	for _, c := range changes {
		for _, p := range touched(c) {
			if matchAny(pol.ProtectedPaths, p) {
				protected = append(protected, p)
			}
		}
	}
	if len(protected) > 0 {
		out = append(out, violation{rule: "protected_paths", paths: protected})
	}
	switch pol.DiffMode {
	case "none":
		if len(changes) > 0 {
			var ps []string
			for _, c := range changes {
				ps = append(ps, c.path)
			}
			out = append(out, violation{rule: "diff_mode", note: "no changes allowed", paths: ps})
		}
	case "allowlist":
		var miss []string
		for _, c := range changes {
			for _, p := range touched(c) {
				if !matchAny(pol.DiffAllow, p) {
					miss = append(miss, p)
				}
			}
		}
		if len(miss) > 0 {
			out = append(out, violation{rule: "diff_allow", paths: miss})
		}
		fallthrough
	case "denylist":
		var hit []string
		for _, c := range changes {
			for _, p := range touched(c) {
				if matchAny(pol.DiffDeny, p) {
					hit = append(hit, p)
				}
			}
		}
		if len(hit) > 0 {
			out = append(out, violation{rule: "diff_deny", paths: hit})
		}
	}
	if !pol.AllowDeletions {
		var del []string
		for _, c := range changes {
			if c.status == 'D' {
				del = append(del, c.path)
			}
		}
		if len(del) > 0 {
			out = append(out, violation{rule: "allow_deletions", paths: del})
		}
	}
	if len(changes) > pol.MaxFilesChanged {
		var ps []string
		for _, c := range changes {
			ps = append(ps, c.path)
		}
		out = append(out, violation{rule: "max_files_changed", note: fmt.Sprintf("%d > %d", len(changes), pol.MaxFilesChanged), paths: ps})
	}
	if patchBytes > pol.MaxDiffBytes {
		out = append(out, violation{rule: "max_diff_bytes", note: fmt.Sprintf("%d > %d", patchBytes, pol.MaxDiffBytes)})
	}
	if len(changes) == 0 && required {
		out = append(out, violation{rule: "no_changes"})
	}
	return out
}

// secretPaths returns the files whose added lines match the scrubber sets (plan 27 sections 13.4 and 14.10).
// The patch is a plain `git diff --cached`; only added lines are examined (header lines are recognised only
// between a `diff --git` line and the first hunk, so an added line that looks like a header is still scanned)
// and no matched value is returned.
func secretPaths(secrets []string, patch string) []string {
	s := scrub.New(secrets...)
	added := map[string]*strings.Builder{}
	var order []string
	cur := ""
	inHeader := false
	for _, l := range strings.Split(patch, "\n") {
		switch {
		case !inHeader && strings.HasPrefix(l, "diff --git "):
			inHeader = true
			cur = ""
		case inHeader && strings.HasPrefix(l, "@@"):
			inHeader = false
		case inHeader && strings.HasPrefix(l, "+++ "):
			cur = strings.TrimPrefix(strings.TrimPrefix(l, "+++ "), "b/")
			if l == "+++ /dev/null" {
				cur = ""
			}
			if cur != "" && added[cur] == nil {
				added[cur] = &strings.Builder{}
				order = append(order, cur)
			}
		case !inHeader && strings.HasPrefix(l, "+") && cur != "":
			added[cur].WriteString(l[1:])
			added[cur].WriteByte('\n')
		}
	}
	var hits []string
	for _, p := range order {
		t := added[p].String()
		if s.String(t) != t {
			hits = append(hits, p)
		}
	}
	return hits
}
