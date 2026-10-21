package verify

import (
	"errors"
	"fmt"
	"path"
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
	if err := checkRevision(in.Rev); err != nil {
		return DiscoveryProtection{}, err
	}
	if in.Validation == nil {
		return DiscoveryProtection{}, errors.New("no validation.yaml: the files a check reads cannot be derived")
	}
	dpol, err := loadDiscoveryPolicy(in.Platform, in.Effective)
	if err != nil {
		return DiscoveryProtection{}, err
	}
	tree, err := LsTree(in.Git, in.Rev)
	if err != nil {
		return DiscoveryProtection{}, err
	}
	d := &discovery{g: in.Git, tree: map[string]TreeEntry{}, whole: map[string]*string{}, paths: make([]string, 0, len(tree))}
	for _, e := range tree {
		d.tree[e.Path] = e
		d.paths = append(d.paths, e.Path)
	}
	if err := d.fromValidation(in.Validation); err != nil {
		return DiscoveryProtection{}, err
	}
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
				return DiscoveryProtection{}, err
			}
			for _, key := range ent.Keys {
				h, err := semanticValueHash(ent.Format, p, content, key)
				if err != nil {
					return DiscoveryProtection{}, fmt.Errorf("%s: %w", p, err)
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
	return out, nil
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

// makeFiles reports whether argv runs make and which makefiles it reads: its -f files, else the three default names.
func makeFiles(argv []string) ([]string, bool, error) {
	switch path.Base(argv[0]) {
	case "make", "gmake":
	default:
		return nil, false, nil
	}
	var files []string
	for i := 1; i < len(argv); i++ {
		a := argv[i]
		var f string
		switch {
		case a == "--":
			i = len(argv)
			continue
		case a == "-f" || a == "--file" || a == "--makefile":
			if i+1 >= len(argv) {
				return nil, true, fmt.Errorf("%s needs a file", a)
			}
			i++
			f = argv[i]
		case strings.HasPrefix(a, "--file="):
			f = strings.TrimPrefix(a, "--file=")
		case strings.HasPrefix(a, "--makefile="):
			f = strings.TrimPrefix(a, "--makefile=")
		case a == "-C" || strings.HasPrefix(a, "-C") && !strings.HasPrefix(a, "--") || a == "--directory" || strings.HasPrefix(a, "--directory="):
			return nil, true, fmt.Errorf("make argv %q changes directory: the files it reads cannot be derived", argv)
		case strings.HasPrefix(a, "-f") && len(a) > 2 && !strings.HasPrefix(a, "--"):
			f = a[2:]
		case strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && len(a) > 2 && strings.ContainsRune(letters(a[1:]), 'f'):
			return nil, true, fmt.Errorf("make flag cluster %q cannot be resolved", a)
		default:
			continue
		}
		p, ok := repoPath(f)
		if !ok {
			return nil, true, fmt.Errorf("make file %q is not a repository path", f)
		}
		files = append(files, p)
	}
	if len(files) == 0 {
		files = []string{"GNUmakefile", "makefile", "Makefile"}
	}
	return files, true, nil
}

// letters returns the leading run of letters of a short-flag cluster.
func letters(s string) string {
	for i := 0; i < len(s); i++ {
		if c := s[i]; !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z') {
			return s[:i]
		}
	}
	return s
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
			return fmt.Errorf("%s: %w", p, err)
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
		if strings.HasPrefix(line, "\t") {
			recipes = append(recipes, line[1:])
			continue
		}
		line = stripMakeComment(line)
		trim := strings.TrimSpace(line)
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
