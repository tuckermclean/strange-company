package cli_test

// 50-AC-13 (CD-14, CD-70(e)): CLI coverage. `TestCLIIndexCoverage` extracts every `./bootstrap ...` and
// `sc-bootstrap ...` invocation from plans/*.md and procedures/*.md and dry-parses each with the real command tree
// (cli.Parse); zero unknown commands or flags are allowed.
//
// Extraction rules (the extractor is exercised on synthetic text by TestCLIExtractor_*):
//   - Sources: fenced code blocks (every line is a candidate; a trailing backslash joins the next line, the invocation
//     keeps the first line's number) and inline code spans (`...`) outside fences. Prose outside code is never read.
//   - A candidate line is split into segments at the unquoted shell operators | || && ; and at the redirections
//     (> 2> &> << and a lone <). A `|` with no whitespace next to it is an alternation (full|state-only), not a pipe.
//   - A segment is an invocation when, after an optional "$ " prompt, sudo, and env-var prefixes (SC_TEST_MODE=1), its
//     first word is ./bootstrap or sc-bootstrap. A '#' comment is cut in fenced lines.
//   - `./bootstrap exec ... -- sc-bootstrap ARGS` is checked as `sc-bootstrap ARGS`; any other command after `--`
//     belongs to the toolbox and is not checked. `./bootstrap shell|exec` check only the wrapper flags of plan 50
//     section 7.1 (plan 02 section 7.1); the wrapper-global --engine and --toolbox-build are removed from ./bootstrap
//     lines. -h and --help are removed (the dry parse never runs the command).
//   - Placeholders: <name> and $VAR become the value "x"; "..." and the ellipsis character are dropped; [optional]
//     groups give one variant without any group and one variant per group alone; <a|b|c> of lowercase words and
//     word|word alternations expand to every alternative (at most 64 variants). A placeholder in a subcommand position
//     (`rotate <target>`) cannot be checked: a parse error `unknown subcommand "x"` on a line with a placeholder is
//     accepted, any other error is drift.
//   - Plan 50 section 7.1 table rows (first column) and the section 7.1.1 table (the leading command words of each
//     code span of the Subcommands column, outside parentheses) are parsed as rows, so a row whose command the parser
//     lacks fails even if no other document uses it (TestCLIIndexCoverage_EveryRowOfSection71ParsesAndIsDocumented).
//
// Why rows, not a mutated tree: the tree is package-private and the test lives in cli_test, so it cannot remove a
// command from a copy. Every row of the CLI authority is parsed on its own; deleting a command from the parser makes
// that row's parse fail. TestCLIIndexCoverage_CatchesAnUnknownCommand proves the checker reports a missing command.

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"strange.company/sc/internal/bootstrap/cli"
)

type invocation struct {
	file        string
	line        int
	raw         string
	words       []string // the arguments handed to cli.Parse (after the program word)
	prog        string
	placeholder bool
	elided      bool
	problem     string // set by the extractor for a wrapper misuse
}

var (
	envAssign   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)
	codeSpan    = regexp.MustCompile("`([^`\n]+)`")
	angleHolder = regexp.MustCompile(`<[^<>]*>`)
	lowerWord   = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	fenceStart  = regexp.MustCompile("^\\s*(?:(?:[0-9]+\\.|[-*+])\\s+)?(```|~~~)")
)

type tok struct {
	s      string
	quoted bool
}

// tokenize splits like a shell: quotes group, operators become their own tokens when unquoted.
func tokenize(line string) []tok {
	var out []tok
	var cur []rune
	has, quoted := false, false
	var q rune
	flush := func() {
		if has {
			out = append(out, tok{string(cur), quoted})
		}
		cur, has, quoted = nil, false, false
	}
	rs := []rune(line)
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch {
		case q != 0:
			if r == q {
				q = 0
			} else {
				cur = append(cur, r)
			}
		case r == '\'' || r == '"':
			q, has, quoted = r, true, true
		case r == ' ' || r == '\t':
			flush()
		case r == ';':
			flush()
			out = append(out, tok{";", false})
		case r == '&' && i+1 < len(rs) && rs[i+1] == '&':
			flush()
			out = append(out, tok{"&&", false})
			i++
		case r == '|':
			next := i+1 < len(rs) && rs[i+1] == '|'
			spaced := !has || i+1 >= len(rs) || rs[i+1] == ' ' || next
			if spaced {
				flush()
				if next {
					out = append(out, tok{"||", false})
					i++
				} else {
					out = append(out, tok{"|", false})
				}
			} else {
				cur, has = append(cur, r), true
			}
		default:
			cur, has = append(cur, r), true
		}
	}
	flush()
	return out
}

func isRedirect(s string) bool {
	return strings.HasPrefix(s, ">") || strings.HasPrefix(s, "2>") || strings.HasPrefix(s, "1>") ||
		strings.HasPrefix(s, "&>") || strings.HasPrefix(s, "<<") || s == "<"
}

func isOp(t tok) bool {
	return !t.quoted && (t.s == "|" || t.s == "||" || t.s == "&&" || t.s == ";")
}

// segments splits tokens at operators.
func segments(ts []tok) [][]tok {
	var out [][]tok
	var cur []tok
	for _, t := range ts {
		if isOp(t) {
			out = append(out, cur)
			cur = nil
			continue
		}
		cur = append(cur, t)
	}
	return append(out, cur)
}

func stripHead(seg []tok) []tok {
	for len(seg) > 0 {
		s := seg[0].s
		switch {
		case s == "$" || s == "sudo":
			seg = seg[1:]
		case !seg[0].quoted && envAssign.MatchString(s):
			seg = seg[1:]
		default:
			if strings.HasPrefix(s, "(") && len(s) > 1 {
				seg[0].s = s[1:]
				continue
			}
			return seg
		}
	}
	return seg
}

// optionalVariants returns the argument lists: one without any [group], and one per group with only that group
// present (in place).
func optionalVariants(args []string) [][]string {
	type item struct {
		words []string
		group bool
	}
	var items []item
	depth := 0
	var cur []string
	for _, a := range args {
		opens, closes := strings.Count(a, "["), strings.Count(a, "]")
		switch {
		case depth == 0 && strings.HasPrefix(a, "[") && opens > closes:
			depth = opens - closes
			cur = []string{strings.TrimPrefix(a, "[")}
		case depth > 0:
			depth += opens - closes
			if depth <= 0 {
				cur = append(cur, strings.TrimSuffix(a, "]"))
				items = append(items, item{cur, true})
				depth, cur = 0, nil
			} else {
				cur = append(cur, a)
			}
		case strings.HasPrefix(a, "[") && strings.HasSuffix(a, "]") && opens == closes:
			items = append(items, item{[]string{a[1 : len(a)-1]}, true})
		default:
			items = append(items, item{[]string{a}, false})
		}
	}
	build := func(only int) []string {
		var out []string
		g := -1
		for _, it := range items {
			if it.group {
				g++
				if g != only {
					continue
				}
			}
			out = append(out, it.words...)
		}
		return out
	}
	out := [][]string{build(-1)}
	groups := 0
	for _, it := range items {
		if it.group {
			groups++
		}
	}
	for g := 0; g < groups; g++ {
		out = append(out, build(g))
	}
	return out
}

func expandAlts(args []string) [][]string {
	res := [][]string{nil}
	for _, a := range args {
		alts := []string{a}
		switch {
		case strings.HasPrefix(a, "<") && strings.HasSuffix(a, ">") && strings.Contains(a, "|"):
			inner := strings.Split(a[1:len(a)-1], "|")
			ok := true
			for _, w := range inner {
				ok = ok && lowerWord.MatchString(w)
			}
			if ok {
				alts = inner
			}
		case strings.Contains(a, "|") && !strings.Contains(a, "<"):
			pre := ""
			body := a
			if i := strings.Index(a, "="); i >= 0 && strings.HasPrefix(a, "-") {
				pre, body = a[:i+1], a[i+1:]
			}
			alts = nil
			for _, w := range strings.Split(body, "|") {
				alts = append(alts, pre+w)
			}
		}
		var next [][]string
		for _, r := range res {
			for _, alt := range alts {
				next = append(next, append(append([]string{}, r...), alt))
			}
		}
		res = next
		if len(res) > 64 {
			res = res[:64]
		}
	}
	return res
}

var wrapperFlags = map[string]map[string]bool{ // flag -> takes a value
	"exec": {"image": true, "network": true, "unlock": false, "no-kube": false, "kvm": false, "api-node": true,
		"clean-room": false, "with-file": true},
	"shell": {"image": true, "network": true, "unlock": false, "api-node": true, "publish": true},
}

// stripWrapperGlobals removes the wrapper-global flags (plan 50 section 7.1: --engine docker|podman, --toolbox-build).
func stripWrapperGlobals(args []string) []string {
	var kept []string
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--engine":
			i++
		case strings.HasPrefix(args[i], "--engine=") || args[i] == "--toolbox-build":
		default:
			kept = append(kept, args[i])
		}
	}
	return kept
}

// mergeAngles joins the tokens of a placeholder that contains spaces (<window start>).
func mergeAngles(args []string) []string {
	var out []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		for strings.Count(a, "<") > strings.Count(a, ">") && i+1 < len(args) {
			i++
			a += " " + args[i]
		}
		out = append(out, a)
	}
	return out
}

// handle turns the words after ./bootstrap or sc-bootstrap into invocations.
func handle(file string, line int, raw, prog string, args []string, out *[]invocation) {
	var cut []string
	for _, a := range args {
		if isRedirect(a) {
			break
		}
		cut = append(cut, a)
	}
	args = cut
	var kept []string
	elided := false
	for _, a := range args {
		if a != "..." && a != "…" && (strings.HasSuffix(a, "...") || strings.HasSuffix(a, "…")) {
			a = strings.TrimSuffix(strings.TrimSuffix(a, "..."), "…")
			elided = true
		}
		if a == "..." || a == "…" {
			elided = true
		}
		if a != "..." && a != "…" && a != "-h" && a != "--help" {
			kept = append(kept, a)
		}
	}
	args = kept
	args = mergeAngles(args)
	for _, v := range optionalVariants(args) {
		for _, w := range expandAlts(v) {
			if prog == "./bootstrap" {
				w = stripWrapperGlobals(w)
			}
			hasHolder := false
			for i, a := range w {
				if angleHolder.MatchString(a) || strings.Contains(a, "$") {
					hasHolder = hasHolder || angleHolder.MatchString(a)
					for angleHolder.MatchString(a) {
						a = angleHolder.ReplaceAllString(a, "x")
					}
					w[i] = a
					if strings.Contains(w[i], "$") {
						w[i] = "x"
					}
				}
			}
			inv := invocation{file: file, line: line, raw: raw, prog: prog, words: w, placeholder: hasHolder, elided: elided}
			if prog == "./bootstrap" && len(w) > 0 && (w[0] == "exec" || w[0] == "shell") {
				wrapper(&inv, out)
				continue
			}
			*out = append(*out, inv)
		}
	}
}

func wrapper(inv *invocation, out *[]invocation) {
	verb, w := inv.words[0], inv.words[1:]
	flags := wrapperFlags[verb]
	for i := 0; i < len(w); i++ {
		a := w[i]
		if a == "--" {
			n := *inv
			n.prog, n.words = "./bootstrap exec (wrapper)", nil
			if verb == "exec" && i+1 < len(w) && w[i+1] == "sc-bootstrap" {
				n.prog, n.words = "sc-bootstrap", w[i+2:]
			}
			*out = append(*out, n)
			return
		}
		if !strings.HasPrefix(a, "-") {
			if verb == "exec" {
				inv.problem = fmt.Sprintf("exec: %q before `--` (the wrapper form is exec [flags] -- <cmd>)", a)
			} else {
				inv.prog, inv.words = "./bootstrap shell (wrapper)", nil
			}
			*out = append(*out, *inv)
			return
		}
		name, _, hasVal := strings.Cut(strings.TrimLeft(a, "-"), "=")
		takes, ok := flags[name]
		if !ok {
			inv.problem = fmt.Sprintf("%s: unknown wrapper flag --%s", verb, name)
			*out = append(*out, *inv)
			return
		}
		if takes && !hasVal {
			i++
		}
	}
	// exec or shell with flags only: a mention of the wrapper command, nothing more to check.
	inv.prog, inv.words = "./bootstrap "+verb+" (wrapper)", nil
	*out = append(*out, *inv)
}

// extractLine extracts the invocations of one candidate line (shell text).
func extractLine(file string, line int, text string, fenced bool) []invocation {
	if fenced {
		if i := strings.Index(text, " #"); i >= 0 {
			text = text[:i]
		}
		if strings.HasPrefix(strings.TrimSpace(text), "#") {
			return nil
		}
	}
	var out []invocation
	for _, seg := range segments(tokenize(text)) {
		seg = stripHead(seg)
		if len(seg) == 0 {
			continue
		}
		prog := seg[0].s
		if prog != "./bootstrap" && prog != "sc-bootstrap" {
			continue
		}
		var args []string
		for _, t := range seg[1:] {
			args = append(args, t.s)
		}
		if n := len(args); n > 0 && strings.HasSuffix(args[n-1], ")") && !strings.Contains(args[n-1], "(") {
			args[n-1] = strings.TrimSuffix(args[n-1], ")")
		}
		handle(file, line, strings.TrimSpace(text), prog, args, &out)
	}
	return out
}

// extractInvocations walks one markdown document.
func extractInvocations(file, text string) []invocation {
	var out []invocation
	lines := strings.Split(text, "\n")
	fenced := false
	for i := 0; i < len(lines); i++ {
		l := lines[i]
		if fenceStart.MatchString(l) {
			fenced = !fenced
			continue
		}
		if fenced {
			start, joined := i+1, l
			for strings.HasSuffix(strings.TrimRight(joined, " "), "\\") && i+1 < len(lines) {
				joined = strings.TrimSuffix(strings.TrimRight(joined, " "), "\\") + " " + lines[i+1]
				i++
			}
			out = append(out, extractLine(file, start, joined, true)...)
			continue
		}
		for _, m := range codeSpan.FindAllStringSubmatch(l, -1) {
			span := m[1]
			if strings.HasPrefix(strings.TrimSpace(l), "|") {
				span = strings.ReplaceAll(span, `\|`, "|")
			}
			out = append(out, extractLine(file, i+1, span, false)...)
		}
	}
	return out
}

// check dry-parses one invocation; "" means it parses.
func check(inv invocation) string {
	if inv.problem != "" {
		return inv.problem
	}
	_, err := cli.Parse(inv.words)
	if err == nil {
		return ""
	}
	e := err.Error()
	if inv.placeholder && (strings.Contains(e, `unknown subcommand "x"`) || strings.Contains(e, `unknown command "x"`)) {
		return ""
	}
	// A bare group name (`sc-bootstrap forge`, `./bootstrap state`) is a reference to the group, not a command line.
	if strings.Contains(e, "needs a subcommand") {
		return ""
	}
	if inv.elided && strings.Contains(e, "needs a value") {
		return ""
	}
	return err.Error()
}

func repoFile(t testing.TB, rel string) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "plans", "50-bootstrap-orchestrator.md")); err == nil {
			return filepath.Join(dir, rel)
		}
		if p := filepath.Dir(dir); p != dir {
			dir = p
			continue
		}
		t.Fatal("repository root not found")
	}
}

func docFiles(t testing.TB) []string {
	t.Helper()
	var all []string
	for _, g := range []string{"plans/*.md", "procedures/*.md"} {
		m, err := filepath.Glob(repoFile(t, g))
		if err != nil || len(m) == 0 {
			t.Fatalf("no files match %s: %v", g, err)
		}
		all = append(all, m...)
	}
	sort.Strings(all)
	return all
}

func relTo(t testing.TB, p string) string {
	root := repoFile(t, "")
	r, _ := filepath.Rel(root, p)
	return r
}

// 50-AC-13: every documented invocation parses with the real tree; zero unknown commands or flags.
func TestCLIIndexCoverage(t *testing.T) {
	t.Parallel()
	total := 0
	var bad []string
	for _, f := range docFiles(t) {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, inv := range extractInvocations(relTo(t, f), string(b)) {
			total++
			if msg := check(inv); msg != "" {
				bad = append(bad, fmt.Sprintf("%s:%d: %s %s: %s", inv.file, inv.line, inv.prog, strings.Join(inv.words, " "), msg))
			}
		}
	}
	if total < 800 {
		t.Errorf("only %d invocations extracted from plans/*.md and procedures/*.md, want at least 800 (about 1,500 are documented): the extractor lost its input", total)
	}
	sort.Strings(bad)
	if len(bad) > 0 {
		t.Errorf("50-AC-13: %d of %d documented invocations do not parse with the command tree:\n%s", len(bad), total, strings.Join(bad, "\n"))
	}
}

// rowsOf71 returns the command spans of the plan 50 section 7.1 table (the first column) and the leading command
// words of the section 7.1.1 table, each as an invocation to parse.
func rowsOf71(t testing.TB) (rows71, rows711 []invocation) {
	t.Helper()
	b, err := os.ReadFile(repoFile(t, "plans/50-bootstrap-orchestrator.md"))
	if err != nil {
		t.Fatal(err)
	}
	section := ""
	for i, l := range strings.Split(string(b), "\n") {
		switch {
		case strings.HasPrefix(l, "### 7.1 "):
			section = "7.1"
		case strings.HasPrefix(l, "#### 7.1.1"):
			section = "7.1.1"
		case strings.HasPrefix(l, "### 7.2"):
			section = ""
		}
		if !strings.HasPrefix(l, "|") {
			continue
		}
		switch section {
		case "7.1":
			if m := codeSpan.FindStringSubmatch(l); m != nil && strings.HasPrefix(m[1], "./bootstrap") {
				got := extractLine("plans/50-bootstrap-orchestrator.md", i+1, strings.ReplaceAll(m[1], `\|`, "|"), false)
				if len(got) == 0 {
					t.Errorf("plans/50-bootstrap-orchestrator.md:%d: the row `%s` yields no invocation", i+1, m[1])
				}
				rows71 = append(rows71, got...)
			}
		case "7.1.1":
			cells := splitCells(l)
			if len(cells) < 3 || strings.HasPrefix(cells[1], "---") || cells[1] == "Subcommands" {
				continue
			}
			for _, sp := range spansOutsideParens(cells[1]) {
				if sp == "sc-env" { // an alias of `env` for the toolbox shell, not a command word
					continue
				}
				if w := leadingWords(sp); len(w) > 0 {
					for _, alt := range w {
						rows711 = append(rows711, invocation{file: "plans/50-bootstrap-orchestrator.md", line: i + 1, raw: sp,
							prog: "sc-bootstrap", words: alt})
					}
				}
			}
		}
	}
	return rows71, rows711
}

func splitCells(row string) []string {
	var cells []string
	var cur strings.Builder
	rs := []rune(row)
	for i := 0; i < len(rs); i++ {
		if rs[i] == '\\' && i+1 < len(rs) && rs[i+1] == '|' {
			cur.WriteString(`\|`)
			i++
			continue
		}
		if rs[i] == '|' {
			cells = append(cells, strings.TrimSpace(cur.String()))
			cur.Reset()
			continue
		}
		cur.WriteRune(rs[i])
	}
	if len(cells) > 0 {
		cells = cells[1:]
	}
	return cells
}

func spansOutsideParens(cell string) []string {
	var out []string
	depth, last := 0, 0
	for _, m := range codeSpan.FindAllStringSubmatchIndex(cell, -1) {
		pre := cell[last:m[0]]
		depth += strings.Count(pre, "(") - strings.Count(pre, ")")
		last = m[1]
		if depth <= 0 {
			out = append(out, strings.ReplaceAll(cell[m[2]:m[3]], `\|`, "|"))
		}
	}
	return out
}

// leadingWords returns the command-word sequences at the start of a span: lowercase words, with word|word
// alternations expanded, up to the first flag, bracket, placeholder or other token.
func leadingWords(span string) [][]string {
	res := [][]string{nil}
	for _, w := range strings.Fields(span) {
		alts := strings.Split(w, "|")
		ok := true
		for _, a := range alts {
			ok = ok && lowerWord.MatchString(a)
		}
		if !ok {
			break
		}
		var next [][]string
		for _, r := range res {
			for _, a := range alts {
				next = append(next, append(append([]string{}, r...), a))
			}
		}
		res = next
	}
	if len(res[0]) == 0 {
		return nil
	}
	return res
}

// 50-AC-13, "removing a row's command from the parser makes the test fail": every row of plan 50 section 7.1 and
// every command of section 7.1.1 is parsed on its own, and each row of section 7.1 is also used by at least one
// documented invocation (its own row counts, so the floor is the row itself).
func TestCLIIndexCoverage_EveryRowOfSection71ParsesAndIsDocumented(t *testing.T) {
	t.Parallel()
	rows71, rows711 := rowsOf71(t)
	if len(rows71) < 30 || len(rows711) < 60 {
		t.Fatalf("extracted %d rows of section 7.1 and %d command spans of section 7.1.1, want at least 30 and 60", len(rows71), len(rows711))
	}
	var bad []string
	seen := map[string]bool{}
	for _, inv := range append(rows71, rows711...) {
		key := inv.prog + " " + strings.Join(inv.words, " ")
		if seen[key] {
			continue
		}
		seen[key] = true
		if msg := check(inv); msg != "" {
			bad = append(bad, fmt.Sprintf("%s:%d: %s: %s", inv.file, inv.line, key, msg))
		}
	}
	sort.Strings(bad)
	if len(bad) > 0 {
		t.Errorf("50-AC-13: %d rows of plan 50 section 7.1/7.1.1 do not parse (a command was removed from the tree, or the row disagrees with it):\n%s", len(bad), strings.Join(bad, "\n"))
	}
}

// The checker itself: an unknown command or flag in a document is reported (so removing a command from the tree, which
// makes a documented invocation unknown, fails TestCLIIndexCoverage).
func TestCLIIndexCoverage_CatchesAnUnknownCommand(t *testing.T) {
	t.Parallel()
	doc := "```sh\n./bootstrap frobnicate --now\n./bootstrap status --nope\n```\nand `./bootstrap status --json`\n"
	var failed int
	for _, inv := range extractInvocations("x.md", doc) {
		if check(inv) != "" {
			failed++
		}
	}
	if failed != 2 {
		t.Errorf("%d invocations flagged, want 2 (frobnicate and --nope)", failed)
	}
}

func words(inv []invocation) []string {
	var out []string
	for _, i := range inv {
		if strings.Contains(i.prog, "(wrapper)") {
			continue
		}
		out = append(out, i.prog+" "+strings.Join(i.words, " "))
	}
	return out
}

func TestCLIExtractor_Rules(t *testing.T) {
	t.Parallel()
	doc := strings.Join([]string{
		"prose ./bootstrap status is not code",
		"```sh",
		"$ SC_TEST_MODE=1 SC_TEST_CRASH_AT=plan:after ./bootstrap run --non-interactive \\",
		"    --from-phase seed",
		"./bootstrap status --json | jq .phase   # comment ./bootstrap nope",
		"cd x && ./bootstrap exec -- sc-bootstrap state status --module compute > out.txt",
		"./bootstrap exec --image ci -- make verify",
		"```",
		"`./bootstrap recover [--scope full|state-only] [--confirm-plan D8]`",
		"`./bootstrap state <status|versions>` and `./bootstrap rotate <target>`",
		"| `./bootstrap node add\\|remove <name>` | x |",
		"`./bootstrap --engine podman -v version`",
		"`./bootstrap --help` and `sc-bootstrap forge init --only <step>...`",
	}, "\n")
	got := words(extractInvocations("d.md", doc))
	want := []string{
		"./bootstrap run --non-interactive --from-phase seed",
		"./bootstrap status --json",
		"sc-bootstrap state status --module compute",
		"./bootstrap recover",
		"./bootstrap recover --scope full",
		"./bootstrap recover --scope state-only",
		"./bootstrap recover --confirm-plan D8",
		"./bootstrap state status",
		"./bootstrap state versions",
		"./bootstrap rotate x",
		"./bootstrap node add x",
		"./bootstrap node remove x",
		"./bootstrap -v version",
		"./bootstrap ",
		"sc-bootstrap forge init --only x",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("extracted\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	for _, inv := range extractInvocations("d.md", doc) {
		if inv.line == 5 && inv.prog == "./bootstrap" && len(inv.words) > 0 && inv.words[0] == "nope" {
			t.Errorf("a comment was read as an invocation")
		}
	}
}

func TestCLIExtractor_WrapperMisuseIsReported(t *testing.T) {
	t.Parallel()
	for doc, wantProblem := range map[string]string{
		"`./bootstrap exec kubectl get pods`":              "before `--`",
		"`./bootstrap exec --bogus -- kubectl get pods`":   "unknown wrapper flag",
		"`./bootstrap shell --image ci --network host`":    "",
		"`./bootstrap exec --image ci --unlock -- true`":   "",
		"`./bootstrap shell --nope`":                       "unknown wrapper flag",
		"`./bootstrap exec --image ci -- sc-bootstrap x1`": "unknown command",
	} {
		invs := extractInvocations("d.md", doc)
		var msg string
		for _, i := range invs {
			if m := check(i); m != "" {
				msg = m
			}
		}
		if wantProblem == "" && msg != "" || wantProblem != "" && !strings.Contains(msg, wantProblem) {
			t.Errorf("%s: problem %q, want %q", doc, msg, wantProblem)
		}
	}
}
