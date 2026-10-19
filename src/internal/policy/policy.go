// Package policy loads and validates the platform policy tree (plans 22 and 26, F04).
package policy

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/santhosh-tekuri/jsonschema/v6/kind"
	"go.yaml.in/yaml/v3"
	"golang.org/x/text/language"
	"golang.org/x/text/message"

	"strange.company/sc/internal/contracts"
)

// Error codes printed by Check.
const (
	CodeSchemaInvalid     = "POLICY_SCHEMA_INVALID"
	CodeUnknownKind       = "POLICY_UNKNOWN_KIND"
	CodeDuplicateResource = "POLICY_DUPLICATE_RESOURCE"
	CodeFileTooLarge      = "POLICY_FILE_TOO_LARGE"
	CodeYAMLAnchor        = "POLICY_YAML_ANCHOR"
	CodeNotUTF8           = "POLICY_NOT_UTF8"
	CodeReservedFilename  = "POLICY_RESERVED_FILENAME"
	CodeKindMissing       = "POLICY_KIND_MISSING"
	CodeYAMLInvalid       = "POLICY_YAML_INVALID"
	CodeUnreadable        = "POLICY_UNREADABLE"
)

// MaxFileSize is the largest accepted policy file (plan 26 section 9.2 step 1).
const MaxFileSize = 1 << 20

// kindSchemas maps a policy kind to the stem of its contract (plan 26 section 9.2, D2).
var kindSchemas = map[string]string{
	"VerificationPolicy": "verification",
	"HarnessRegistry":    "harness",
	"ResourceProfiles":   "resource-profile",
	"ModelRouting":       "model-routing",
	"PricingTable":       "pricing",
	"BudgetPolicy":       "budget-policy",
	"RoleDefinition":     "role",
	"CIPolicy":           "ci-policy",
	"TestQuarantine":     "test-quarantine",
}

// runnerKinds are the kinds Runner mode requires.
var runnerKinds = []string{"VerificationPolicy", "HarnessRegistry", "ResourceProfiles", "ModelRouting", "PricingTable", "BudgetPolicy", "RoleDefinition"}

// Issue is one policy error, printed as `<CODE>[:<json-pointer>] <file>: <message>`.
type Issue struct {
	Code    string
	Pointer string
	File    string
	Message string
}

// String formats the issue.
func (i Issue) String() string {
	code := i.Code
	if i.Pointer != "" {
		code += ":" + i.Pointer
	}
	return code + " " + i.File + ": " + i.Message
}

// Options configures a check or a Loader. ContractsDir holds the *.schema.yaml files. Runner selects Runner
// mode, which additionally requires every Runner kind of plan 26 section 9.2.
type Options struct {
	ContractsDir string
	Runner       bool
}

// Snapshot is an accepted policy revision.
type Snapshot struct {
	Revision string
	// Resources are the validated documents, keyed by relative path.
	Resources map[string]Resource
}

// Resource is one validated policy document.
type Resource struct {
	Kind string
	Name string
	Doc  map[string]any
}

type file struct {
	rel  string // relative path as found on disk
	path string // path used for the digest and in issues
	data []byte
	// reserved is set when the name is not allowed: it contains "__" in a git-style tree, or its mapped path
	// is shared with another file.
	reserved bool
}

// readTree reads every regular file under fsys (plan 26 section 9.2 step 1). Names beginning with ".." are
// ConfigMap volume internals and are skipped; symlinks to regular files are followed.
func readTree(fsys fs.FS) ([]file, error) {
	var files []file
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == "." {
			return nil
		}
		if strings.HasPrefix(d.Name(), "..") {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			if d.Type()&fs.ModeSymlink == 0 {
				return nil
			}
			st, err := fs.Stat(fsys, p)
			if err != nil || !st.Mode().IsRegular() {
				return nil
			}
		}
		b, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		files = append(files, file{rel: p, path: p, data: b})
		return nil
	})
	// A tree with a real subdirectory is a git-style tree; only a flat tree is a ConfigMap mount whose
	// top-level keys `a__b.yaml` are read as `a/b.yaml`.
	nested := false
	for _, f := range files {
		if strings.Contains(f.rel, "/") {
			nested = true
		}
	}
	mapped := map[string]int{}
	for i := range files {
		m := mapKey(files[i].rel)
		mapped[m]++
		if !nested {
			files[i].path = m
		} else if strings.Contains(files[i].rel, "__") {
			files[i].reserved = true
		}
	}
	for i := range files {
		if mapped[mapKey(files[i].rel)] > 1 {
			files[i].reserved = true
		}
	}
	sort.Slice(files, func(i, j int) bool {
		if files[i].path != files[j].path {
			return files[i].path < files[j].path
		}
		return files[i].rel < files[j].rel
	})
	return files, err
}

// mapKey reads a top-level ConfigMap key `roles__tester.yaml` as `roles/tester.yaml`.
func mapKey(p string) string {
	if strings.Contains(p, "/") {
		return p
	}
	return strings.ReplaceAll(p, "__", "/")
}

func digestOf(files []file) string {
	h := sha256.New()
	for _, f := range files {
		h.Write([]byte(f.path))
		h.Write([]byte{0})
		h.Write(f.data)
		h.Write([]byte{0})
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

// Digest is the revision of the policy tree in dir: sha256 over (relative path, NUL, bytes, NUL) sorted by
// path (plan 26 section 9.2 step 4).
func Digest(dir string) (string, error) { return DigestFS(os.DirFS(dir)) }

// DigestFS is Digest over a file system.
func DigestFS(fsys fs.FS) (string, error) {
	files, err := readTree(fsys)
	if err != nil {
		return "", err
	}
	return digestOf(files), nil
}

// Check validates the policy tree in dir and returns its revision and every issue found.
func Check(dir string, opts Options) (string, []Issue) {
	rev, _, issues := check(dir, opts)
	return rev, issues
}

func check(dir string, opts Options) (string, map[string]Resource, []Issue) {
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		msg := "not a directory"
		if err != nil {
			msg = err.Error()
		}
		return "", nil, []Issue{{Code: CodeUnreadable, File: dir, Message: msg}}
	}
	files, err := readTree(os.DirFS(dir))
	if err != nil {
		return "", nil, []Issue{{Code: CodeUnreadable, File: dir, Message: err.Error()}}
	}
	rev := digestOf(files)
	schemas, err := contracts.CompileSchemas(opts.ContractsDir)
	if err != nil {
		return rev, nil, []Issue{{Code: CodeUnreadable, File: opts.ContractsDir, Message: err.Error()}}
	}

	var issues []Issue
	add := func(code, ptr, f, msg string) { issues = append(issues, Issue{code, ptr, f, msg}) }
	resources := map[string]Resource{}
	seen := map[string]string{} // kind/name -> first file
	present := map[string]bool{}
	names := map[string]map[string]bool{} // kind -> names seen, including documents rejected by their schema
	repoDocs := map[string]map[string]any{}

	for _, f := range files {
		if _, isRepo := repoSide(f.path); isRepo && !isRepoDocument(f.path) {
			continue // other files under .strange-company/ belong to other plans (repo.yaml, scripts)
		}
		if f.reserved {
			add(CodeReservedFilename, "", f.rel, `a policy file name may not contain "__" (reserved for ConfigMap keys) or share a path with another file`)
			continue
		}
		if len(f.data) > MaxFileSize {
			add(CodeFileTooLarge, "", f.path, fmt.Sprintf("%d bytes exceeds the %d byte limit", len(f.data), MaxFileSize))
			continue
		}
		if !utf8.Valid(f.data) {
			add(CodeNotUTF8, "", f.path, "file is not valid UTF-8")
			continue
		}
		if msg, bad := findAnchor(f.data); bad {
			add(CodeYAMLAnchor, "", f.path, msg)
			continue
		}
		doc, err := contracts.ParseYAML(f.data)
		if err != nil {
			add(CodeYAMLInvalid, "", f.path, oneLine(err.Error()))
			continue
		}
		if rstem, isRepo := repoSide(f.path); isRepo {
			if errs := validate(schemas[rstem], doc); len(errs) == 0 {
				repoDocs[f.path], _ = doc.(map[string]any)
			} else {
				for _, e := range errs {
					add(CodeSchemaInvalid, e[0], f.path, e[1])
				}
			}
			continue
		}
		m, _ := doc.(map[string]any)
		kind, _ := m["kind"].(string)
		stem, known := kindSchemas[kind]
		if !known {
			add(CodeUnknownKind, "/kind", f.path, fmt.Sprintf("unknown kind %q", kind))
			continue
		}
		present[kind] = true
		bad := false
		if nm, _ := mapAt(m, "metadata")["name"].(string); nm != "" {
			if names[kind] == nil {
				names[kind] = map[string]bool{}
			}
			names[kind][nm] = true
		}
		if verr := validate(schemas[stem], doc); len(verr) > 0 {
			bad = true
			for _, e := range verr {
				add(CodeSchemaInvalid, e[0], f.path, e[1])
			}
		}
		meta, _ := m["metadata"].(map[string]any)
		name, _ := meta["name"].(string)
		key := kind + "/" + name
		if first, dup := seen[key]; dup && name != "" {
			add(CodeDuplicateResource, "/metadata/name", f.path, fmt.Sprintf("%s %q is already defined in %s", kind, name, first))
			continue
		}
		seen[key] = f.path
		if !bad {
			resources[f.path] = Resource{Kind: kind, Name: name, Doc: m}
		}
	}
	if opts.Runner {
		for _, k := range runnerKinds {
			if !present[k] {
				add(CodeKindMissing, "", dir, fmt.Sprintf("no %s document found (required in Runner mode)", k))
			}
		}
	}
	issues = append(issues, crossFileInvariants(resources, names)...)
	var protected []string
	paths := make([]string, 0, len(resources))
	for p := range resources {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		if r := resources[p]; r.Kind == "VerificationPolicy" {
			issues = append(issues, verificationInvariants(p, mapAt(r.Doc, "spec"))...)
			protected = append(protected, strList(r.Doc, "spec", "protectedPaths")...)
		}
	}
	issues = append(issues, repoSideInvariants(protected, repoDocs[repoPolicyFile], repoDocs[repoValidationFile])...)
	sortIssues(issues)
	return rev, resources, issues
}

// repoSide reports whether p is under the repository-side directory and, if it is one of the two documents
// this package validates, the stem of its schema.
func repoSide(p string) (string, bool) {
	if !strings.HasPrefix(p, repoSideDir+"/") {
		return "", false
	}
	switch p {
	case repoPolicyFile:
		return repoPolicySchemaStem, true
	case repoValidationFile:
		return repoValidationSchemaStem, true
	}
	return "", true
}

func isRepoDocument(p string) bool { s, _ := repoSide(p); return s != "" }

// sortIssues orders issues by (file, pointer, code, message). Exact duplicates are already removed by validate.
func sortIssues(issues []Issue) []Issue {
	sort.SliceStable(issues, func(a, b int) bool {
		x, y := issues[a], issues[b]
		if x.File != y.File {
			return x.File < y.File
		}
		if x.Pointer != y.Pointer {
			return x.Pointer < y.Pointer
		}
		if x.Code != y.Code {
			return x.Code < y.Code
		}
		return x.Message < y.Message
	})
	return issues
}

// errMessage renders one leaf error deterministically. The library builds the property list of an
// additionalProperties error by ranging over a map, so that list is sorted before it is formatted.
func errMessage(k jsonschema.ErrorKind, p *message.Printer) string {
	if ap, ok := k.(*kind.AdditionalProperties); ok {
		c := *ap
		c.Properties = append([]string(nil), ap.Properties...)
		sort.Strings(c.Properties)
		return c.LocalizedString(p)
	}
	return k.LocalizedString(p)
}

// propNamesCount counts the propertyNames errors raised by the schema schemaURL for key at depth that validating
// doc produces.
func propNamesCount(s *jsonschema.Schema, doc any, schemaURL string, depth int, key string) int {
	var ve *jsonschema.ValidationError
	if err := s.Validate(doc); err == nil || !errors.As(err, &ve) {
		return 0
	}
	n := 0
	var rec func(e *jsonschema.ValidationError)
	rec = func(e *jsonschema.ValidationError) {
		if k, ok := e.ErrorKind.(*kind.PropertyNames); ok {
			if k.Property == key && len(e.InstanceLocation) == depth && e.SchemaURL == schemaURL {
				n++
			}
			return
		}
		for _, c := range e.Causes {
			rec(c)
		}
	}
	rec(ve)
	return n
}

// maxExperimentCandidates bounds the key-removal experiment of propertyIssues.
const maxExperimentCandidates = 32

// propertyIssues locates the objects whose key violates a propertyNames constraint. The library stores a shared,
// later-mutated slice as the location of such an error (its length, the depth, is right but its tokens are not),
// so the offending objects are found by experiment: among the objects at that depth holding the key, those
// whose removal of the key lowers the number of such errors. When no object can be singled out, the result is
// the deepest common pointer of the candidates, without the key, and the message names the candidates.
func propertyIssues(s *jsonschema.Schema, doc any, schemaURL string, depth int, key, msg string) [][2]string {
	cands := propertyCandidates(doc, depth, key)
	if len(cands) == 0 {
		return [][2]string{{"", msg}}
	}
	// The experiment costs one validation per candidate; above maxExperimentCandidates the candidates are
	// reported by their common prefix instead.
	if len(cands) <= maxExperimentCandidates {
		base := propNamesCount(s, doc, schemaURL, depth, key)
		var out [][2]string
		for _, c := range cands {
			obj := lookupTokens(doc, c)
			v := obj[key]
			delete(obj, key)
			after := propNamesCount(s, doc, schemaURL, depth, key)
			obj[key] = v
			if after < base {
				out = append(out, [2]string{pointer(append(append([]string{}, c...), key)), msg})
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	pre := cands[0]
	names := make([]string, 0, len(cands))
	for _, c := range cands {
		n := 0
		for n < len(pre) && n < len(c) && pre[n] == c[n] {
			n++
		}
		pre = pre[:n]
		names = append(names, pointer(append(append([]string{}, c...), key)))
	}
	return [][2]string{{pointer(pre), msg + " (candidates: " + strings.Join(names, ", ") + ")"}}
}

func lookupTokens(doc any, toks []string) map[string]any {
	cur := doc
	for _, t := range toks {
		switch x := cur.(type) {
		case map[string]any:
			cur = x[t]
		case []any:
			i, _ := strconv.Atoi(t)
			cur = x[i]
		}
	}
	m, _ := cur.(map[string]any)
	return m
}

// validate returns (pointer, message) for every leaf schema error. It walks ValidationError.Causes instead of
// BasicOutput (see propertyIssues for the propertyNames locations).
func validate(s *jsonschema.Schema, doc any) [][2]string {
	err := s.Validate(doc)
	if err == nil {
		return nil
	}
	var ve *jsonschema.ValidationError
	if !errors.As(err, &ve) {
		return [][2]string{{"", oneLine(err.Error())}}
	}
	var out [][2]string
	seen := map[string]bool{}
	type propKey struct {
		url   string
		depth int
		key   string
	}
	memo := map[propKey][][2]string{} // one experiment per (schema, depth, key) in this call
	emit := func(ptr, msg string) {
		e := [2]string{ptr, oneLine(msg)}
		if !seen[e[0]+"\x00"+e[1]] {
			seen[e[0]+"\x00"+e[1]] = true
			out = append(out, e)
		}
	}
	p := message.NewPrinter(language.English)
	var walk func(e *jsonschema.ValidationError)
	walk = func(e *jsonschema.ValidationError) {
		if k, ok := e.ErrorKind.(*kind.PropertyNames); ok {
			msg := k.LocalizedString(p)
			for _, d := range validateLeaves(e.Causes, p) {
				msg += "; " + d[1]
			}
			pk := propKey{e.SchemaURL, len(e.InstanceLocation), k.Property}
			res, done := memo[pk]
			if !done {
				res = propertyIssues(s, doc, pk.url, pk.depth, pk.key, oneLine(msg))
				memo[pk] = res
			}
			for _, r := range res {
				emit(r[0], r[1])
			}
			return
		}
		if len(e.Causes) == 0 {
			emit(pointer(e.InstanceLocation), errMessage(e.ErrorKind, p))
			return
		}
		for _, c := range e.Causes {
			walk(c)
		}
	}
	walk(ve)
	if len(out) == 0 {
		out = append(out, [2]string{"", oneLine(err.Error())})
	}
	return out
}

// validateLeaves returns the leaf messages of errs (used for the detail of a propertyNames error, whose causes
// are about the property name string itself).
func validateLeaves(errs []*jsonschema.ValidationError, p *message.Printer) [][2]string {
	var out [][2]string
	for _, e := range errs {
		if len(e.Causes) == 0 {
			out = append(out, [2]string{"", oneLine(errMessage(e.ErrorKind, p))})
			continue
		}
		out = append(out, validateLeaves(e.Causes, p)...)
	}
	return out
}

// propertyCandidates returns the tokens of every object at depth holding key, in sorted order.
func propertyCandidates(doc any, depth int, key string) [][]string {
	var cands [][]string
	var rec func(v any, at []string)
	rec = func(v any, at []string) {
		switch t := v.(type) {
		case map[string]any:
			if len(at) == depth {
				if _, ok := t[key]; ok {
					cands = append(cands, append([]string{}, at...))
				}
				return
			}
			ks := make([]string, 0, len(t))
			for k := range t {
				ks = append(ks, k)
			}
			sort.Strings(ks)
			for _, k := range ks {
				rec(t[k], append(at[:len(at):len(at)], k))
			}
		case []any:
			for i, c := range t {
				rec(c, append(at[:len(at):len(at)], strconv.Itoa(i)))
			}
		}
	}
	rec(doc, nil)
	return cands
}

func pointer(toks []string) string {
	var sb strings.Builder
	for _, t := range toks {
		sb.WriteByte('/')
		sb.WriteString(strings.ReplaceAll(strings.ReplaceAll(t, "~", "~0"), "/", "~1"))
	}
	return sb.String()
}

// findAnchor reports a YAML anchor or alias anywhere in the document.
func findAnchor(b []byte) (string, bool) {
	dec := yaml.NewDecoder(bytes.NewReader(b))
	for {
		var n yaml.Node
		if err := dec.Decode(&n); err != nil {
			return "", false // syntax errors are reported by the parser
		}
		if msg, bad := walkAnchor(&n); bad {
			return msg, true
		}
	}
}

func walkAnchor(n *yaml.Node) (string, bool) {
	if n.Kind == yaml.AliasNode {
		return fmt.Sprintf("line %d: YAML aliases are not allowed", n.Line), true
	}
	if n.Anchor != "" {
		return fmt.Sprintf("line %d: YAML anchors are not allowed (&%s)", n.Line, n.Anchor), true
	}
	for _, c := range n.Content {
		if msg, bad := walkAnchor(c); bad {
			return msg, true
		}
	}
	return "", false
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// Loader keeps the last-known-good snapshot (plan 26 section 9.2 steps 6 and 7, in memory only).
type Loader struct {
	opts Options
	mu   sync.RWMutex
	snap *Snapshot
	ok   bool
	err  string
}

// NewLoader returns a Loader with no snapshot and dispatch disabled.
func NewLoader(opts Options) *Loader { return &Loader{opts: opts} }

// Load validates the policy tree in dir. A valid revision atomically replaces the snapshot and enables
// dispatch; an invalid one keeps the last-known-good snapshot, disables dispatch and records the error.
func (l *Loader) Load(dir string) []Issue {
	rev, res, issues := check(dir, l.opts)
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(issues) == 0 {
		l.snap = &Snapshot{Revision: rev, Resources: res}
		l.ok, l.err = true, ""
		return nil
	}
	l.ok = false
	parts := make([]string, len(issues))
	for i, is := range issues {
		parts[i] = is.String()
	}
	l.err = strings.Join(parts, "\n")
	return issues
}

// Snapshot returns the last accepted revision, or nil.
func (l *Loader) Snapshot() *Snapshot {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.snap
}

// DispatchEnabled is true when the latest load was valid.
func (l *Loader) DispatchEnabled() bool {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.ok
}

// LastError is the error of the latest load, empty when it was valid.
func (l *Loader) LastError() string {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.err
}
