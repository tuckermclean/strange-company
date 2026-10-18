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
	"path"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"go.yaml.in/yaml/v3"

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
	path string // path with ConfigMap keys mapped to nested paths
	data []byte
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
		files = append(files, file{rel: p, path: mapKey(p), data: b})
		return nil
	})
	sort.Slice(files, func(i, j int) bool { return files[i].path < files[j].path })
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

	for _, f := range files {
		if strings.Contains(path.Base(f.rel), "__") && strings.Contains(f.rel, "/") || hasReservedDir(f.rel) {
			add(CodeReservedFilename, "", f.path, `a policy file name may not contain "__" (reserved for ConfigMap keys)`)
			continue
		}
		ext := strings.ToLower(path.Ext(f.path))
		if ext != ".yaml" && ext != ".yml" {
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
		m, _ := doc.(map[string]any)
		kind, _ := m["kind"].(string)
		stem, known := kindSchemas[kind]
		if !known {
			add(CodeUnknownKind, "/kind", f.path, fmt.Sprintf("unknown kind %q", kind))
			continue
		}
		present[kind] = true
		bad := false
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
	return rev, resources, issues
}

func hasReservedDir(rel string) bool {
	d := path.Dir(rel)
	return d != "." && strings.Contains(d, "__")
}

// validate returns (pointer, message) for every schema error.
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
	for _, u := range ve.BasicOutput().Errors {
		if u.Error == nil {
			continue
		}
		e := [2]string{u.InstanceLocation, oneLine(u.Error.String())}
		if !seen[e[0]+"\x00"+e[1]] {
			seen[e[0]+"\x00"+e[1]] = true
			out = append(out, e)
		}
	}
	if len(out) == 0 {
		out = append(out, [2]string{"", oneLine(err.Error())})
	}
	return out
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
