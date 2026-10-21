package verify

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"

	"strange.company/sc/internal/policy"
)

// Discovery protection (plan 22 section 14.5): the whole files and semantic keys that decide which tests run and
// how. Whole files are the configuration globs of the policy, every file make reads for a check (the Makefile
// transitively through include, -include, sinclude, -f and literal $(wildcard ...), and every script a recipe or an
// argv names by repository path) and every check's configPaths. A file that does not exist but would be read when it
// appears is listed with a nil blob. Anything the derivation cannot resolve statically is an error (22-R-3).

type semanticEntry struct {
	Path   string
	Format string
	Keys   []string
}

type discoveryPolicy struct {
	wholeGlobs   []string
	semantic     []semanticEntry
	forbidNew    []string
	testDepPaths []string
}

func specMap(p policy.Platform) (map[string]any, error) {
	spec, ok := pathMap(p.Verification, "spec")
	if !ok {
		return nil, errors.New("the platform verification policy has no spec")
	}
	return spec, nil
}

func parseSemanticEntries(v any) ([]semanticEntry, error) {
	if v == nil {
		return nil, nil
	}
	l, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("semanticKeys is not a list")
	}
	var out []semanticEntry
	for _, e := range l {
		m, ok := e.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("semanticKeys entry is not an object")
		}
		p, _ := m["path"].(string)
		f, _ := m["format"].(string)
		keys, err := stringList(m["keys"])
		if err != nil || p == "" {
			return nil, fmt.Errorf("semanticKeys entry %v is malformed", m)
		}
		if f == "" {
			f = "json"
		}
		out = append(out, semanticEntry{Path: p, Format: f, Keys: keys})
	}
	return out, nil
}

// loadDiscoveryPolicy reads the discovery configuration: the effective (merged) lists when the repository set them,
// else the platform's.
func loadDiscoveryPolicy(p policy.Platform, eff policy.Effective) (discoveryPolicy, error) {
	spec, err := specMap(p)
	if err != nil {
		return discoveryPolicy{}, err
	}
	dc, _ := pathMap(spec, "testProtection", "discoveryConfig")
	if dc == nil {
		return discoveryPolicy{}, errors.New("the platform verification policy has no testProtection.discoveryConfig")
	}
	var d discoveryPolicy
	if d.wholeGlobs, err = stringList(dc["wholeFiles"]); err != nil {
		return d, fmt.Errorf("discoveryConfig.wholeFiles: %w", err)
	}
	if v, ok := eff.Values["/spec/discoveryConfig/wholeFiles"]; ok {
		if d.wholeGlobs, err = stringList(v); err != nil {
			return d, fmt.Errorf("effective discoveryConfig.wholeFiles: %w", err)
		}
	}
	semSrc := dc["semanticKeys"]
	if v, ok := eff.Values["/spec/discoveryConfig/semanticKeys"]; ok {
		semSrc = v
	}
	if d.semantic, err = parseSemanticEntries(semSrc); err != nil {
		return d, err
	}
	if d.forbidNew, err = stringList(dc["forbidNewFiles"]); err != nil {
		return d, fmt.Errorf("discoveryConfig.forbidNewFiles: %w", err)
	}
	if d.testDepPaths, err = stringList(dc["testDependencyPaths"]); err != nil {
		return d, fmt.Errorf("discoveryConfig.testDependencyPaths: %w", err)
	}
	return d, nil
}

func matchAny(globs []string, p string) bool {
	for _, g := range globs {
		if policy.MatchGlob(g, p) {
			return true
		}
	}
	return false
}

func hasGlobMeta(s string) bool { return strings.ContainsAny(s, "*?[") }

// DeriveDiscoveryProtection snapshots the discovery-protected files and semantic keys at a revision
// (plan 22 section 14.5). WholeFiles and SemanticKeys are sorted and never nil.
func DeriveDiscoveryProtection(in DiscoveryInput) (DiscoveryProtection, error) {
	dp, bad, err := deriveDiscovery(in)
	if err == nil && len(bad) > 0 {
		return DiscoveryProtection{}, bad[0]
	}
	return dp, err
}

// discoveryFileError is a derivation failure that belongs to one file (unparsable, or an include that cannot be
// resolved). The derivation goes on with the other files, so that CheckIngestion can name every broken file.
type discoveryFileError struct {
	Path string
	Err  error
}

func (e *discoveryFileError) Error() string { return e.Path + ": " + e.Err.Error() }
func (e *discoveryFileError) Unwrap() error { return e.Err }

// deriveDiscovery is DeriveDiscoveryProtection that reports the per-file failures separately from the errors that
// stop the derivation altogether.
func deriveDiscovery(in DiscoveryInput) (DiscoveryProtection, []*discoveryFileError, error) {
	var bad []*discoveryFileError
	if err := checkRevision(in.Rev); err != nil {
		return DiscoveryProtection{}, nil, err
	}
	if in.Validation == nil {
		return DiscoveryProtection{}, nil, errors.New("no validation.yaml: the files a check reads cannot be derived")
	}
	dpol, err := loadDiscoveryPolicy(in.Platform, in.Effective)
	if err != nil {
		return DiscoveryProtection{}, nil, err
	}
	tree, err := LsTree(in.Git, in.Rev)
	if err != nil {
		return DiscoveryProtection{}, nil, err
	}
	d := &discovery{g: in.Git, tree: map[string]TreeEntry{}, whole: map[string]*string{}, paths: make([]string, 0, len(tree))}
	for _, e := range tree {
		d.tree[e.Path] = e
		d.paths = append(d.paths, e.Path)
	}
	if err := d.fromValidation(in.Validation); err != nil {
		return DiscoveryProtection{}, nil, err
	}
	bad = append(bad, d.bad...)
	for _, g := range dpol.wholeGlobs {
		if !hasGlobMeta(g) {
			d.protect(g)
			continue
		}
		for _, p := range d.paths {
			if policy.MatchGlob(g, p) {
				d.protect(p)
			}
		}
	}
	out := DiscoveryProtection{WholeFiles: []WholeFileProtection{}, SemanticKeys: []SemanticKeyProtection{}}
	var names []string
	for p := range d.whole {
		names = append(names, p)
	}
	sort.Strings(names)
	for _, p := range names {
		out.WholeFiles = append(out.WholeFiles, WholeFileProtection{Path: p, BlobSha: d.whole[p]})
	}
	for _, p := range d.paths {
		for _, ent := range dpol.semantic {
			if !policy.MatchGlob(ent.Path, p) {
				continue
			}
			content, err := catBlob(in.Git, d.tree[p].Blob)
			if err != nil {
				return DiscoveryProtection{}, nil, err
			}
			for _, key := range ent.Keys {
				h, err := semanticValueHash(ent.Format, p, content, key)
				if err != nil {
					bad = append(bad, &discoveryFileError{Path: p, Err: err})
					break
				}
				out.SemanticKeys = append(out.SemanticKeys, SemanticKeyProtection{Path: p, Key: key, ValueSha256: h})
			}
		}
	}
	sort.SliceStable(out.SemanticKeys, func(i, j int) bool {
		a, b := out.SemanticKeys[i], out.SemanticKeys[j]
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		return a.Key < b.Key
	})
	out.SemanticKeys = dedupeSemantic(out.SemanticKeys)
	return out, bad, nil
}

func dedupeSemantic(in []SemanticKeyProtection) []SemanticKeyProtection {
	out := in[:0]
	for i, k := range in {
		if i > 0 && in[i-1].Path == k.Path && in[i-1].Key == k.Key {
			continue
		}
		out = append(out, k)
	}
	return out
}

type discovery struct {
	g     GitRepo
	tree  map[string]TreeEntry
	paths []string
	whole map[string]*string
	bad   []*discoveryFileError
}

// protect records a repository path with its blob at the revision (nil when absent or not a regular file).
func (d *discovery) protect(p string) {
	if e, ok := d.tree[p]; ok && e.Mode != "160000" {
		b := e.Blob
		d.whole[p] = &b
		return
	}
	if _, ok := d.whole[p]; !ok {
		d.whole[p] = nil
	}
}

// repoPath normalises a relative repository path; ok is false for an absolute path or one leaving the repository.
func repoPath(p string) (string, bool) {
	p = strings.TrimPrefix(p, "./")
	if p == "" || strings.HasPrefix(p, "/") {
		return "", false
	}
	c := path.Clean(p)
	if c == "." || c == ".." || strings.HasPrefix(c, "../") {
		return "", false
	}
	return c, true
}

func (d *discovery) fromValidation(v map[string]any) error {
	ver, ok := pathMap(v, "spec", "verification")
	if !ok {
		return errors.New("validation.yaml has no spec.verification")
	}
	var argvs [][]string
	var roots []string
	collect := func(item any, where string, withConfig bool) error {
		m, ok := item.(map[string]any)
		if !ok {
			return fmt.Errorf("%s is not an object", where)
		}
		argv, err := stringList(m["argv"])
		if err != nil || len(argv) == 0 {
			return fmt.Errorf("%s has no usable argv", where)
		}
		argvs = append(argvs, argv)
		if env, ok := m["env"].(map[string]any); ok {
			for name := range env {
				if name == "MAKEFILES" {
					return fmt.Errorf("%s sets MAKEFILES: the files make reads cannot be derived", where)
				}
			}
		}
		if withConfig {
			cp, err := stringList(m["configPaths"])
			if err != nil {
				return fmt.Errorf("%s configPaths: %w", where, err)
			}
			for _, c := range cp {
				p, ok := repoPath(c)
				if !ok {
					return fmt.Errorf("%s configPaths entry %q is not a repository path", where, c)
				}
				d.protect(p)
			}
		}
		return nil
	}
	for _, key := range []string{"suites", "checks"} {
		l, _ := ver[key].([]any)
		for i, item := range l {
			if err := collect(item, fmt.Sprintf("%s[%d]", key, i), key == "checks"); err != nil {
				return err
			}
		}
	}
	for _, argv := range argvs {
		files, isMake, err := makeFiles(argv)
		if err != nil {
			return err
		}
		if isMake {
			roots = append(roots, files...)
			continue
		}
		d.protectArgv(argv)
	}
	return d.walkMakefiles(roots)
}

// makeLongOptions are the long options that change which makefiles make reads; getopt accepts any unambiguous
// prefix of a long option, so a prefix of one of these is treated as that option (fail closed).
var (
	makeFileOptions = []string{"file", "makefile"}
	makeDenyOptions = []string{"directory", "include-dir", "eval"}
)

func isOptionPrefix(name string, full []string) bool {
	if name == "" {
		return false
	}
	for _, f := range full {
		if strings.HasPrefix(f, name) {
			return true
		}
	}
	return false
}

// makeFiles reports whether argv runs make and which makefiles it reads: its -f files, else the three default names.
// An option that changes the directory, adds an include directory or evaluates a statement, and a MAKEFILES
// variable, cannot be resolved statically and is an error.
func makeFiles(argv []string) ([]string, bool, error) {
	switch path.Base(argv[0]) {
	case "make", "gmake":
	default:
		return nil, false, nil
	}
	var files []string
	addFile := func(f string) error {
		p, ok := repoPath(f)
		if !ok {
			return fmt.Errorf("make file %q is not a repository path", f)
		}
		files = append(files, p)
		return nil
	}
	unresolvable := func(a string) error {
		return fmt.Errorf("make argv %q: option %q changes which makefiles are read and cannot be resolved statically", argv, a)
	}
	for i := 1; i < len(argv); i++ {
		a := argv[i]
		switch {
		case a == "--":
			i = len(argv)
		case strings.HasPrefix(a, "--"):
			name, val, hasVal := strings.Cut(a[2:], "=")
			switch {
			case isOptionPrefix(name, makeDenyOptions):
				return nil, true, unresolvable(a)
			case isOptionPrefix(name, makeFileOptions):
				if name != "file" && name != "makefile" {
					return nil, true, fmt.Errorf("make argv %q: the abbreviated option %q cannot be resolved; spell --file out", argv, a)
				}
				if !hasVal {
					if i+1 >= len(argv) {
						return nil, true, fmt.Errorf("%s needs a file", a)
					}
					i++
					val = argv[i]
				}
				if err := addFile(val); err != nil {
					return nil, true, err
				}
			}
		case strings.HasPrefix(a, "-") && len(a) > 1:
			// A cluster of short flags: the first flag that takes an argument ends it (the argument is the rest of
			// the cluster, or the next word).
			for j := 1; j < len(a); j++ {
				c := a[j]
				switch c {
				case 'C', 'I', 'E':
					return nil, true, unresolvable(a)
				case 'f':
					f := a[j+1:]
					if f == "" {
						if i+1 >= len(argv) {
							return nil, true, fmt.Errorf("%s needs a file", a)
						}
						i++
						f = argv[i]
					}
					if err := addFile(f); err != nil {
						return nil, true, err
					}
					j = len(a)
				case 'o', 'W', 'l', 'j', 'O':
					if c == 'o' || c == 'W' {
						if a[j+1:] == "" {
							i++
						}
					}
					j = len(a)
				}
			}
		case strings.HasPrefix(a, "MAKEFILES="):
			return nil, true, unresolvable(a)
		}
	}
	if len(files) == 0 {
		files = []string{"GNUmakefile", "makefile", "Makefile"}
	}
	return files, true, nil
}

var scriptExts = map[string]bool{".sh": true, ".bash": true, ".py": true, ".js": true, ".mjs": true, ".ts": true, ".rb": true, ".pl": true, ".mk": true}

// protectArgv protects the script an argv runs: argv[0] when it is a repository path, and any other element that
// names an existing file.
func (d *discovery) protectArgv(argv []string) {
	if strings.Contains(argv[0], "/") && !strings.Contains(argv[0], "{") {
		if p, ok := repoPath(argv[0]); ok {
			d.protect(p)
		}
	}
	for _, a := range argv[1:] {
		d.protectIfScript(a)
	}
}

// protectIfScript protects a token that names an existing tracked file by path or by script extension.
func (d *discovery) protectIfScript(tok string) {
	tok = strings.Trim(tok, "\"';")
	if tok == "" || strings.ContainsAny(tok, "${}*?[") || strings.HasPrefix(tok, "-") {
		return
	}
	p, ok := repoPath(tok)
	if !ok {
		return
	}
	if _, exists := d.tree[p]; !exists {
		return
	}
	if strings.Contains(p, "/") || scriptExts[path.Ext(p)] {
		d.protect(p)
	}
}

// walkMakefiles protects every given makefile and everything it includes, and the scripts its recipes run.
func (d *discovery) walkMakefiles(roots []string) error {
	seen := map[string]bool{}
	queue := append([]string(nil), roots...)
	for len(queue) > 0 {
		p := queue[0]
		queue = queue[1:]
		if seen[p] {
			continue
		}
		seen[p] = true
		d.protect(p)
		e, ok := d.tree[p]
		if !ok || e.Mode == "160000" {
			continue
		}
		content, err := catBlob(d.g, e.Blob)
		if err != nil {
			return err
		}
		incs, recipes, err := parseMakefile(string(content), d.paths)
		if err != nil {
			d.bad = append(d.bad, &discoveryFileError{Path: p, Err: err})
			continue
		}
		queue = append(queue, incs...)
		for _, r := range recipes {
			d.protectRecipe(r)
		}
	}
	return nil
}

// protectRecipe protects the script a recipe line runs: its first word when it is a relative path (also when the
// file does not exist yet) and any word that names an existing file.
func (d *discovery) protectRecipe(line string) {
	words := strings.Fields(line)
	if len(words) == 0 {
		return
	}
	first := strings.TrimLeft(words[0], "@-+")
	if first == "" && len(words) > 1 {
		words, first = words[1:], strings.TrimLeft(words[1], "@-+")
	}
	if strings.Contains(first, "/") && !strings.ContainsAny(first, "${}*?[") {
		if p, ok := repoPath(strings.Trim(first, "\"';")); ok {
			d.protect(p)
		}
	}
	for _, w := range words {
		d.protectIfScript(strings.TrimLeft(w, "@-+"))
	}
}

// parseMakefile returns the included files (literal paths and expanded literal wildcards) and the recipe lines of a
// makefile. An include it cannot resolve statically is an error.
func parseMakefile(text string, tree []string) (includes, recipes []string, err error) {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	lines := strings.Split(text, "\n")
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		for strings.HasSuffix(line, "\\") && i+1 < len(lines) {
			i++
			line = strings.TrimSuffix(line, "\\") + " " + strings.TrimLeft(lines[i], " \t")
		}
		if err := checkMakeLine(line); err != nil {
			return nil, nil, err
		}
		trim := strings.TrimSpace(stripMakeComment(line))
		if strings.HasPrefix(line, "\t") {
			// A recipe line, but make reads a tab-led line outside a rule (for example inside a conditional) as a
			// directive, so an include-like line is also resolved as an include (a safe over-approximation).
			recipes = append(recipes, line[1:])
		}
		var rest string
		switch {
		case hasDirective(trim, "include"):
			rest = trim[len("include"):]
		case hasDirective(trim, "-include"):
			rest = trim[len("-include"):]
		case hasDirective(trim, "sinclude"):
			rest = trim[len("sinclude"):]
		default:
			continue
		}
		words, err := splitIncludeWords(rest)
		if err != nil {
			return nil, nil, err
		}
		for _, w := range words {
			if pat, ok := strings.CutPrefix(w, "$(wildcard "); ok {
				pat = strings.TrimSuffix(pat, ")")
				for _, one := range strings.Fields(pat) {
					if strings.Contains(one, "$") || strings.ContainsAny(one, "()") {
						return nil, nil, fmt.Errorf("include %q cannot be resolved statically", w)
					}
					cp, ok := repoPath(one)
					if !ok {
						return nil, nil, fmt.Errorf("include pattern %q is not a repository path", one)
					}
					for _, t := range tree {
						if m, _ := path.Match(cp, t); m {
							includes = append(includes, t)
						}
					}
				}
				continue
			}
			if strings.ContainsAny(w, "$()") {
				return nil, nil, fmt.Errorf("include %q cannot be resolved statically", w)
			}
			cp, ok := repoPath(w)
			if !ok {
				return nil, nil, fmt.Errorf("include %q is not a repository path", w)
			}
			includes = append(includes, cp)
		}
	}
	return includes, recipes, nil
}

var (
	makeEvalRE = regexp.MustCompile(`\$[({]\s*eval\b`)
	makeWordRE = regexp.MustCompile(`\b(include|sinclude|load)\b`)
)

// checkMakeLine rejects what the derivation cannot follow: a `load` directive (a loadable object runs code and can
// define new directives) and a `$(eval ...)` that mentions include or load.
func checkMakeLine(line string) error {
	trim := strings.TrimSpace(stripMakeComment(line))
	if hasDirective(trim, "load") || hasDirective(trim, "-load") {
		return fmt.Errorf("a load directive cannot be resolved statically: %q", trim)
	}
	if makeEvalRE.MatchString(line) && makeWordRE.MatchString(line) {
		return fmt.Errorf("$(eval) with include or load cannot be resolved statically: %q", trim)
	}
	return nil
}

// hasDirective reports whether the line is the directive followed by a space (and not an assignment to a
// variable of that name).
func hasDirective(line, name string) bool {
	rest, ok := strings.CutPrefix(line, name)
	if !ok || rest == "" || (rest[0] != ' ' && rest[0] != '\t') {
		return false
	}
	rest = strings.TrimSpace(rest)
	for _, op := range []string{":=", "::=", "=", "?=", "+=", "!=", ":"} {
		if strings.HasPrefix(rest, op) {
			return false
		}
	}
	return true
}

func stripMakeComment(line string) string {
	for i := 0; i < len(line); i++ {
		switch line[i] {
		case '\\':
			i++
		case '#':
			return line[:i]
		}
	}
	return line
}

// splitIncludeWords splits the argument of include into words, keeping a `$(wildcard ...)` call whole.
func splitIncludeWords(s string) ([]string, error) {
	var words []string
	i := 0
	for i < len(s) {
		if s[i] == ' ' || s[i] == '\t' {
			i++
			continue
		}
		start, depth := i, 0
		for i < len(s) && (depth > 0 || (s[i] != ' ' && s[i] != '\t')) {
			switch s[i] {
			case '(':
				depth++
			case ')':
				depth--
			}
			i++
		}
		if depth != 0 {
			return nil, fmt.Errorf("unbalanced parentheses in include %q", s)
		}
		words = append(words, s[start:i])
	}
	if len(words) == 0 {
		return nil, errors.New("include without a file")
	}
	return words, nil
}
