package engine_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"strange.company/sc/internal/bootstrap/cli"
	"strange.company/sc/internal/bootstrap/engine"
	"strange.company/sc/internal/bootstrap/store"
	"strange.company/sc/internal/clock"
	"strange.company/sc/internal/contracts"
)

const (
	cluster    = "lab-one"
	installUID = "6f1c2d3e-4b5a-4c6d-8e7f-0a1b2c3d4e5f"
	lockKey    = cluster + "/locks/bootstrap.lock"
	journalKey = cluster + "/journal/journal.json"
)

var runIDRe = regexp.MustCompile(`^[0-9]{8}T[0-9]{6}Z-[0-9a-f]{4}$`)

func repoRoot(t testing.TB) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "plans", "50-bootstrap-orchestrator.md")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("repository root not found")
		}
		dir = parent
	}
}

// --- directory-backed fake S3: usable from a child process too, so a SIGKILLed run leaves its lock object behind.

type op struct{ Op, Key, IfMatch string }

type dirStore struct {
	root    string
	ifMatch bool // the store supports If-Match writes

	mu      sync.Mutex
	ops     []op
	failPut func(key string) error
	onPut   func(key string, data []byte)
}

func newDirStore(root string, ifMatch bool) *dirStore {
	_ = os.MkdirAll(filepath.Join(root, ".tmp"), 0o755)
	return &dirStore{root: root, ifMatch: ifMatch}
}

func etagOf(b []byte) string {
	s := sha256.Sum256(b)
	return `"` + hex.EncodeToString(s[:8]) + `"`
}

func (s *dirStore) path(key string) string { return filepath.Join(s.root, filepath.FromSlash(key)) }

func (s *dirStore) rec(o, key, ifm string) {
	s.mu.Lock()
	s.ops = append(s.ops, op{o, key, ifm})
	s.mu.Unlock()
}

func (s *dirStore) opsFor(key string) []op {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []op
	for _, o := range s.ops {
		if o.Key == key {
			out = append(out, o)
		}
	}
	return out
}

func (s *dirStore) Get(_ context.Context, key string) (store.Object, error) {
	b, err := os.ReadFile(s.path(key))
	if errors.Is(err, fs.ErrNotExist) {
		return store.Object{}, &store.Error{Kind: store.NotFound, Msg: key}
	}
	if err != nil {
		return store.Object{}, &store.Error{Kind: store.Unavailable, Msg: err.Error()}
	}
	return store.Object{Data: b, ETag: etagOf(b)}, nil
}

func (s *dirStore) write(key string, data []byte, excl bool) error {
	tmp, err := os.CreateTemp(filepath.Join(s.root, ".tmp"), "put")
	if err != nil {
		return &store.Error{Kind: store.Unavailable, Msg: err.Error()}
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		return &store.Error{Kind: store.Unavailable, Msg: err.Error()}
	}
	tmp.Close()
	if err := os.MkdirAll(filepath.Dir(s.path(key)), 0o755); err != nil {
		return &store.Error{Kind: store.Unavailable, Msg: err.Error()}
	}
	if excl {
		if err := os.Link(tmp.Name(), s.path(key)); err != nil {
			if errors.Is(err, fs.ErrExist) {
				return &store.Error{Kind: store.Exists, Msg: key}
			}
			return &store.Error{Kind: store.Unavailable, Msg: err.Error()}
		}
		return nil
	}
	if err := os.Rename(tmp.Name(), s.path(key)); err != nil {
		return &store.Error{Kind: store.Unavailable, Msg: err.Error()}
	}
	return nil
}

func (s *dirStore) PutIfAbsent(_ context.Context, key string, data []byte) (string, error) {
	s.rec("put-if-absent", key, "")
	if s.failPut != nil {
		if err := s.failPut(key); err != nil {
			return "", err
		}
	}
	if s.onPut != nil {
		s.onPut(key, data)
	}
	if err := s.write(key, data, true); err != nil {
		return "", err
	}
	return etagOf(data), nil
}

func (s *dirStore) Put(_ context.Context, key string, data []byte, ifMatch string) (string, error) {
	s.rec("put", key, ifMatch)
	if ifMatch != "" && !s.ifMatch {
		return "", &store.Error{Kind: store.Unsupported, Msg: "If-Match"}
	}
	if s.failPut != nil {
		if err := s.failPut(key); err != nil {
			return "", err
		}
	}
	if ifMatch != "" {
		cur, err := os.ReadFile(s.path(key))
		if err != nil || etagOf(cur) != ifMatch {
			return "", &store.Error{Kind: store.PreconditionFailed, Msg: key}
		}
	}
	if s.onPut != nil {
		s.onPut(key, data)
	}
	if err := s.write(key, data, false); err != nil {
		return "", err
	}
	return etagOf(data), nil
}

func (s *dirStore) List(_ context.Context, prefix string) ([]string, error) {
	var out []string
	_ = filepath.WalkDir(s.root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(s.root, p)
		rel = filepath.ToSlash(rel)
		if !strings.HasPrefix(rel, ".tmp/") && strings.HasPrefix(rel, prefix) {
			out = append(out, rel)
		}
		return nil
	})
	return out, nil
}

func (s *dirStore) Delete(_ context.Context, key string, ifMatch string) error {
	s.rec("delete", key, ifMatch)
	if ifMatch != "" && !s.ifMatch {
		return &store.Error{Kind: store.Unsupported, Msg: "If-Match"}
	}
	if ifMatch != "" {
		cur, err := os.ReadFile(s.path(key))
		if err != nil || etagOf(cur) != ifMatch {
			return &store.Error{Kind: store.PreconditionFailed, Msg: key}
		}
	}
	if err := os.Remove(s.path(key)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return &store.Error{Kind: store.Unavailable, Msg: err.Error()}
	}
	return nil
}

// raw returns the stored bytes of key, or nil.
func (s *dirStore) raw(key string) []byte {
	b, err := os.ReadFile(s.path(key))
	if err != nil {
		return nil
	}
	return b
}

// --- run ids: a counter feeds the Rand of every engine, so ids differ and the test needs no wall clock.

type ctrRand struct {
	mu sync.Mutex
	n  byte
}

func (r *ctrRand) Read(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for k := range p {
		r.n++
		p[k] = r.n
	}
	return len(p), nil
}

// --- the scratch installation

type tenv struct {
	t   *testing.T
	dir string // the .bootstrap directory
	st  *dirStore
	clk *clock.Fake
	rnd *ctrRand
}

func newEnv(t *testing.T) *tenv {
	t.Helper()
	root := t.TempDir()
	return &tenv{
		t: t, dir: filepath.Join(root, ".bootstrap"), st: newDirStore(filepath.Join(root, "s3"), false),
		clk: clock.NewFake(time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)), rnd: &ctrRand{},
	}
}

func (v *tenv) cfg() engine.Config {
	return engine.Config{
		Dir: v.dir, Store: v.st, IfMatch: v.st.ifMatch, Clock: v.clk, Rand: v.rnd,
		Cluster: cluster, InstallUID: installUID, Provider: "fake", Profile: "self-hosting-minimum",
		ConfigDigest: "sha256:aa11bb22cc33dd44ee55ff6600112233445566778899aabbccddeeff00112233",
		Command:      "run", User: "alice", Host: "host-a", Version: "v0.1.0",
		GitHead:       "4b825dc642cb6eb9a060e54bf8d69288fbee4904",
		ToolboxDigest: "sha256:5555555555555555555555555555555555555555555555555555555555555555", ToolboxSource: "pulled",
		Env: map[string]string{},
	}
}

func (v *tenv) eng(mod ...func(*engine.Config)) *engine.Engine {
	v.t.Helper()
	c := v.cfg()
	for _, m := range mod {
		m(&c)
	}
	e, err := engine.New(c)
	if err != nil {
		v.t.Fatal(err)
	}
	return e
}

func (v *tenv) exec(p engine.Pipeline, o engine.Options, mod ...func(*engine.Config)) engine.Result {
	v.t.Helper()
	return v.eng(mod...).Execute(context.Background(), p, o)
}

func (v *tenv) localPath() string { return filepath.Join(v.dir, "journal.json") }

func readJSON(t testing.TB, b []byte, what string) map[string]any {
	t.Helper()
	if len(b) == 0 {
		t.Fatalf("%s: empty or missing", what)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("%s: not JSON: %v\n%s", what, err, b)
	}
	return m
}

func (v *tenv) local() map[string]any {
	v.t.Helper()
	b, _ := os.ReadFile(v.localPath())
	return readJSON(v.t, b, "local journal")
}

func (v *tenv) remote() map[string]any {
	v.t.Helper()
	return readJSON(v.t, v.st.raw(journalKey), "remote journal")
}

func (v *tenv) writeBoth(j map[string]any) (local, remote []byte) {
	v.t.Helper()
	b, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		v.t.Fatal(err)
	}
	if err := os.WriteFile(v.localPath(), b, 0o600); err != nil {
		v.t.Fatal(err)
	}
	if _, err := v.st.Put(context.Background(), journalKey, b, ""); err != nil {
		v.t.Fatal(err)
	}
	return b, b
}

func seqOf(j map[string]any) int { f, _ := j["journal_seq"].(float64); return int(f) }

func dig(m any, path ...string) any {
	for _, p := range path {
		mm, ok := m.(map[string]any)
		if !ok {
			return nil
		}
		m = mm[p]
	}
	return m
}

func str(v any) string { s, _ := v.(string); return s }

func phaseJ(t testing.TB, j map[string]any, name string) map[string]any {
	t.Helper()
	pls, _ := j["pipelines"].([]any)
	for _, pl := range pls {
		phs, _ := dig(pl, "phases").([]any)
		for _, ph := range phs {
			if str(dig(ph, "name")) == name {
				return ph.(map[string]any)
			}
		}
	}
	t.Fatalf("journal has no phase %q: %v", name, j["pipelines"])
	return nil
}

func stepJ(t testing.TB, ph map[string]any, name string) map[string]any {
	t.Helper()
	ss, _ := ph["steps"].([]any)
	for _, s := range ss {
		if str(dig(s, "name")) == name {
			return s.(map[string]any)
		}
	}
	t.Fatalf("phase %v has no step %q", ph["name"], name)
	return nil
}

func statusOf(t testing.TB, j map[string]any, phase string) string {
	t.Helper()
	return str(phaseJ(t, j, phase)["status"])
}

func shaOf(b []byte) string {
	s := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(s[:])
}

func digestOf(inputs any) string {
	b, _ := json.Marshal(inputs)
	s := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(s[:])
}

func confirmations(j map[string]any, kind string) []map[string]any {
	var out []map[string]any
	cs, _ := j["confirmations"].([]any)
	for _, c := range cs {
		if m, ok := c.(map[string]any); ok && str(m["kind"]) == kind {
			out = append(out, m)
		}
	}
	return out
}

func journalSchemaValidate(t testing.TB, j map[string]any) {
	t.Helper()
	schemas, err := contracts.CompileSchemas(filepath.Join(repoRoot(t), "contracts"))
	if err != nil {
		t.Fatal(err)
	}
	sc := schemas["bootstrap-journal"]
	if sc == nil {
		t.Fatal("no bootstrap-journal schema")
	}
	b, _ := json.Marshal(j)
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var inst any
	if err := dec.Decode(&inst); err != nil {
		t.Fatal(err)
	}
	if err := sc.Validate(inst); err != nil {
		t.Errorf("journal does not validate against bootstrap-journal.schema.yaml: %v", err)
	}
}

// --- fake phases

// unit is a fake phase or step. The "world" is its done flag: Run sets it, Check reports it, so a rerun against the
// same unit behaves like a rerun against the same real world.
type unit struct {
	name      string
	inputs    any
	timeout   time.Duration
	steps     []engine.Unit
	retryable func(error) bool

	mu           sync.Mutex
	done         bool
	checks, runs int
	checkFn      func(u *unit) engine.Check
	runFn        func(u *unit) ([]engine.OutputRef, error)
}

func newUnit(name string, steps ...engine.Unit) *unit {
	return &unit{name: name, inputs: map[string]any{"phase": name, "v": 1}, timeout: 30 * time.Minute, steps: steps}
}

func (u *unit) Name() string                        { return u.name }
func (u *unit) Inputs(context.Context) (any, error) { return u.inputs, nil }
func (u *unit) Timeout() time.Duration              { return u.timeout }
func (u *unit) Steps() []engine.Unit                { return u.steps }
func (u *unit) Retryable(err error) bool {
	if u.retryable != nil {
		return u.retryable(err)
	}
	var e *engine.Error
	return errors.As(err, &e) && e.Retryable
}

func (u *unit) Check(context.Context) (engine.Check, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.checks++
	if u.checkFn != nil {
		return u.checkFn(u), nil
	}
	if u.done {
		return engine.Check{State: engine.Done}, nil
	}
	return engine.Check{State: engine.NotDone}, nil
}

func (u *unit) Run(context.Context) ([]engine.OutputRef, error) {
	u.mu.Lock()
	u.runs++
	fn := u.runFn
	u.mu.Unlock()
	if fn != nil {
		refs, err := fn(u)
		if err == nil {
			u.mu.Lock()
			u.done = true
			u.mu.Unlock()
		}
		return refs, err
	}
	u.mu.Lock()
	u.done = true
	u.mu.Unlock()
	return []engine.OutputRef{{Kind: "file", Ref: ".bootstrap/runs/x/" + u.name + ".json"}}, nil
}

func (u *unit) counts() (checks, runs int) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.checks, u.runs
}

func (u *unit) setInputs(v any) { u.mu.Lock(); u.inputs = v; u.mu.Unlock() }
func (u *unit) setDone(d bool)  { u.mu.Lock(); u.done = d; u.mu.Unlock() }

func fail(code string, retryable bool) func(*unit) ([]engine.OutputRef, error) {
	return func(*unit) ([]engine.OutputRef, error) {
		return nil, &engine.Error{Code: code, Message: code + " happened", Retryable: retryable}
	}
}

func pipe(phases ...*unit) engine.Pipeline {
	p := engine.Pipeline{Kind: "install"}
	for _, u := range phases {
		p.Phases = append(p.Phases, u)
	}
	return p
}

func runsOf(us ...*unit) []int {
	var out []int
	for _, u := range us {
		_, r := u.counts()
		out = append(out, r)
	}
	return out
}

func eqInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func testConfirmation(kind string) cli.Confirmation {
	return cli.Confirmation{Kind: kind, SubjectDigest: "sha256:6666666666666666666666666666666666666666666666666666666666666666", Method: "flag"}
}

func jsonMarshal(v any) ([]byte, error) { return json.MarshalIndent(v, "", "  ") }
