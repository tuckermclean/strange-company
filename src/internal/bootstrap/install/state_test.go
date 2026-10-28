package install_test

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"

	"filippo.io/age"

	"strange.company/sc/internal/bootstrap/identity"
	"strange.company/sc/internal/bootstrap/install"
	"strange.company/sc/internal/contracts"
	"strange.company/sc/internal/s3client"
	"strange.company/sc/internal/tofustate"
)

// S7 of F08: the state commands (plan 50 section 7.1: `state status|restore|verify`, `rotate tofu-passphrase`, `status`;
// plan 06 sections 7.2, 20.1 to 20.3; 06-AC-6, 06-AC-8, 06-AC-14). The OpenTofu side is the fake StateTofu seam, which
// plays an OpenTofu that encrypts with the passphrase it is given: a stored state is the real envelope shape
// (serial, lineage, meta naming the key provider, encrypted_data) around a payload that only the passphrase and the
// provider name of the generation open. Real OpenTofu, real SeaweedFS and the real fake compute module run in tier E
// (tofustate/integration_state_test.go, `make test-integration`).
//
// Decisions the plans leave open (recorded in the handoff):
//   - `state status` needs neither the passphrase nor a prompt: serial and lineage are plaintext envelope fields;
//   - `state restore` asks for the typed text `restore <cluster> <module> <V[0:8]>` on stdin; --non-interactive without
//     it exits 5; a held state lock refuses with TOFU_STATE_LOCKED (exit 1);
//   - `state verify` writes <cluster>/restore-tests/<ts>.json to the tofu-state bucket, exit 0 passed / 1 otherwise;
//   - an install with no OpenTofu module needs neither bundle nor passphrase for `state verify`;
//   - `rotate tofu-passphrase` stores the new passphrase as the bundle entry SC-TOFU-ENCRYPTION/current (generation N+1)
//     and the old one as SC-TOFU-ENCRYPTION/previous, as a new bundle version that is also uploaded.

const stRestoreTests = clusterName + "/restore-tests/"

var (
	stModules   = []string{"compute", "storage"}
	stTagRe     = regexp.MustCompile(`^fake:([0-9a-f]{16}):(.*)$`)
	stMetaKeyRe = regexp.MustCompile(`^key_provider\.pbkdf2\.p([0-9]+)$`)
)

type stCall struct {
	Op, Module string
	Enc        tofustate.Encryption
}

// stFake is the fake OpenTofu of the state commands.
type stFake struct {
	mu    sync.Mutex
	w     *s3World
	calls []stCall
}

var _ install.StateTofu = (*stFake)(nil)

func stTag(pass string) string {
	s := sha256.Sum256([]byte("fake-tf-enc\x00" + pass))
	return hex.EncodeToString(s[:8])
}

// stEnvelope is a stored state in the shape OpenTofu writes: plaintext serial and lineage, meta naming the key provider
// p<gen>, and the encrypted payload.
func stEnvelope(serial int64, lineage, pass string, gen int, addrs []string) []byte {
	plain, _ := json.Marshal(map[string]any{"version": 4, "serial": serial, "lineage": lineage, "resources": addrs})
	env := map[string]any{
		"serial": serial, "lineage": lineage,
		"meta":               map[string]string{fmt.Sprintf("key_provider.pbkdf2.p%d", gen): "eyJzYWx0IjoiYSJ9"},
		"encrypted_data":     "fake:" + stTag(pass) + ":" + base64.StdEncoding.EncodeToString(plain),
		"encryption_version": "v0",
	}
	b, _ := json.Marshal(env)
	return b
}

type stPlain struct {
	Serial    int64    `json:"serial"`
	Lineage   string   `json:"lineage"`
	Resources []string `json:"resources"`
}

// open decrypts like OpenTofu with the given encryption: the enforced provider p<Generation>, or the fallback
// p<Generation-1> when a previous passphrase is given. The provider name stored in the state must be one of them.
func (f *stFake) open(raw []byte, e tofustate.Encryption) (stPlain, int, error) {
	var env struct {
		Meta map[string]string `json:"meta"`
		Data string            `json:"encrypted_data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil || env.Data == "" {
		return stPlain{}, 0, errors.New("fake tofu: not an encrypted state")
	}
	m := stTagRe.FindStringSubmatch(env.Data)
	if m == nil {
		return stPlain{}, 0, errors.New("fake tofu: the encrypted payload is damaged")
	}
	gen := 0
	for k := range env.Meta {
		if g := stMetaKeyRe.FindStringSubmatch(k); g != nil {
			fmt.Sscanf(g[1], "%d", &gen)
		}
	}
	ok := (m[1] == stTag(e.Current) && gen == e.Generation && e.Generation >= 1) ||
		(e.Previous != "" && m[1] == stTag(e.Previous) && gen == e.Generation-1)
	if !ok {
		return stPlain{}, gen, errors.New("fake tofu: decryption failed: neither the enforced nor the fallback key provider opens this state")
	}
	b, err := base64.StdEncoding.DecodeString(m[2])
	if err != nil {
		return stPlain{}, gen, err
	}
	var p stPlain
	if err := json.Unmarshal(b, &p); err != nil {
		return stPlain{}, gen, err
	}
	return p, gen, nil
}

func (f *stFake) record(op, module string, e tofustate.Encryption) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, stCall{Op: op, Module: module, Enc: e})
}

func (f *stFake) snapshot() []stCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]stCall(nil), f.calls...)
}

func (f *stFake) count(op, module string) int {
	n := 0
	for _, c := range f.snapshot() {
		if c.Op == op && (module == "" || c.Module == module) {
			n++
		}
	}
	return n
}

func (f *stFake) Decrypt(_ context.Context, module string, raw []byte, e tofustate.Encryption) ([]byte, error) {
	f.record("decrypt", module, e)
	p, _, err := f.open(raw, e)
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{"version": 4, "serial": p.Serial, "lineage": p.Lineage, "resources": p.Resources})
}

func (f *stFake) PlanCopy(_ context.Context, module string, raw []byte, e tofustate.Encryption) (int, error) {
	f.record("plan", module, e)
	if _, _, err := f.open(raw, e); err != nil {
		return 0, err
	}
	return 0, nil
}

func (f *stFake) RefreshOnly(_ context.Context, module string, remote install.RemoteState, e tofustate.Encryption) error {
	f.record("refresh", module, e)
	raw, ok := f.w.read(remote.Bucket, remote.Key)
	if !ok {
		return fmt.Errorf("fake tofu: no state at %s/%s", remote.Bucket, remote.Key)
	}
	p, _, err := f.open(raw, e)
	if err != nil {
		return err
	}
	return f.w.write(remote.S3.AccessKey, remote.Bucket, remote.Key, stEnvelope(p.Serial+1, p.Lineage, e.Current, e.Generation, p.Resources))
}

// --- the fixture: a fake install that reached `provisioned`, with state objects in the tofu-state bucket

type stFix struct {
	t       *testing.T
	i       *inst
	fake    *stFake
	pass1   string
	lineage map[string]string
}

func stAddrs(module string) []string {
	if module == "compute" {
		return []string{`terraform_data.node["node-01"]`}
	}
	var out []string
	for _, b := range allBucketNames() {
		out = append(out, addrOf(b)...)
	}
	sort.Strings(out)
	return out
}

func newStateFix(t *testing.T) *stFix {
	t.Helper()
	i := newInst(t)
	code, out := i.run("", merge(i.unattendedEnv(), stopAfter("provision")), i.envOnlyArgs("--non-interactive"))
	requireExit(t, code, 0, out)
	f := &stFix{t: t, i: i, fake: &stFake{w: i.world}, lineage: map[string]string{
		"compute": "40fdcdb0-c0dd-f9ae-4f04-5a60a25e2b84", "storage": "7a1b2c3d-0000-4000-8000-00000000f008",
	}}
	f.pass1 = f.bundleSecret(i.journal().RecoveryBundle.LocalPath, "SC-TOFU-ENCRYPTION", "")
	f.put("storage", 2, f.pass1, 1)
	f.put("storage", 3, f.pass1, 1)
	f.put("compute", 1, f.pass1, 1)
	f.put("compute", 2, f.pass1, 1)
	return f
}

// bundleSecret reads one secret out of a bundle file ("" instance: whichever instance is there).
func (f *stFix) bundleSecret(path, class, instance string) string {
	f.t.Helper()
	m, files := f.openBundle(path)
	for _, e := range m.Entries {
		if e.Class == class && (instance == "" || e.Instance == instance) {
			return string(files[e.Path])
		}
	}
	f.t.Fatalf("bundle %s has no %s/%s entry", path, class, instance)
	return ""
}

type stManifest struct {
	BundleVersion        int     `json:"bundle_version"`
	InstallID            string  `json:"install_id"`
	InstallUID           string  `json:"install_uid"`
	PreviousBundleSha256 *string `json:"previous_bundle_sha256"`
	Entries              []struct {
		Class      string `json:"class"`
		Instance   string `json:"instance"`
		Path       string `json:"path"`
		Generation int    `json:"generation"`
	} `json:"entries"`
}

func (f *stFix) openBundle(path string) (stManifest, map[string][]byte) {
	f.t.Helper()
	return stOpenBundle(f.t, path, goodPass)
}

func stOpenBundle(t testing.TB, path, pass string) (stManifest, map[string][]byte) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the bundle: %v", err)
	}
	o, err := identity.Open(data, pass)
	if err != nil || o == nil {
		t.Fatalf("opening %s: %v", path, err)
	}
	var m stManifest
	if err := json.Unmarshal(o.Manifest, &m); err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{}
	for _, af := range o.Files {
		files[af.Path] = af.Data
	}
	return m, files
}

func (f *stFix) store() tofustate.Store {
	acct, err := f.i.world.open(s3client.Config{AccessKey: stateKeyID})
	if err != nil {
		f.t.Fatal(err)
	}
	return acct.Bucket(bucketName("tofu-state"))
}

func stKey(module string) string { return tofustate.StateKey(clusterName, module) }

// put appends a state version encrypted with pass as provider p<gen>.
func (f *stFix) put(module string, serial int64, pass string, gen int) {
	f.t.Helper()
	if err := f.i.world.write(stateKeyID, bucketName("tofu-state"), stKey(module), stEnvelope(serial, f.lineage[module], pass, gen, stAddrs(module))); err != nil {
		f.t.Fatal(err)
	}
}

func (f *stFix) corrupt(module string) {
	f.t.Helper()
	if _, err := f.store().PutObject(context.Background(), stKey(module), []byte("GARBAGE"), s3client.PutOptions{}); err != nil {
		f.t.Fatal(err)
	}
}

func (f *stFix) versions(module string) []tofustate.Version {
	f.t.Helper()
	vs, err := tofustate.ListVersions(context.Background(), f.store(), clusterName, module, 0)
	if err != nil {
		f.t.Fatalf("ListVersions(%s): %v", module, err)
	}
	return vs
}

func (f *stFix) current(module string) []byte {
	f.t.Helper()
	b, ok := f.i.world.read(bucketName("tofu-state"), stKey(module))
	if !ok {
		f.t.Fatalf("no %s state object", module)
	}
	return b
}

func (f *stFix) lock(module, who string) {
	f.t.Helper()
	body := `{"ID":"11111111-2222-3333-4444-555555555555","Operation":"OperationTypeApply","Info":"","Who":"` + who + `","Version":"1.13.1","Created":"2026-10-27T09:00:00.000000001Z","Path":"` + bucketName("tofu-state") + `/` + stKey(module) + `"}`
	if _, err := f.store().PutIfAbsent(context.Background(), tofustate.LockKey(clusterName, module), []byte(body)); err != nil {
		f.t.Fatal(err)
	}
}

// runWith executes a state-command line with the fake OpenTofu injected and exactly the given process environment.
func (f *stFix) runWith(stdin string, env map[string]string, args ...string) (int, string) {
	f.t.Helper()
	d := f.i.deps(stdin)
	d.State = f.fake
	var out, errb bytes.Buffer
	code := install.Run(append([]string{"--env-file", f.i.envFile}, args...), env, &out, &errb, d)
	return code, out.String() + errb.String()
}

// run is runWith with the runtime confirmations and the passphrase file in the environment; extra overrides them.
func (f *stFix) run(stdin string, extra map[string]string, args ...string) (int, string) {
	f.t.Helper()
	return f.runWith(stdin, merge(f.i.env, f.i.unattendedEnv(), extra), args...)
}

func short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func (f *stFix) versionIDs(module string) map[string]bool {
	ids := map[string]bool{}
	for _, v := range f.versions(module) {
		ids[v.ID] = true
	}
	return ids
}

// --- state status

// 06-AC-8: `state status` lists, per module, the current version id, serial and lineage, and the holder of a state lock
// object. It needs no passphrase.
func TestStateStatus_ListsModulesVersionsAndLocks_06AC8(t *testing.T) {
	f := newStateFix(t)
	f.put("compute", 41, f.pass1, 1)
	f.put("storage", 57, f.pass1, 1)
	f.lock("compute", "other@host")
	bare := merge(f.i.env, map[string]string{"SC_CONFIRM_BUDGET": budgetMonthly, "SC_CONFIRM_BACKSTOPS": f.i.backstopFile()})
	code, out := f.runWith("", bare, "--non-interactive", "state", "status")
	requireExit(t, code, 0, out)
	for m, serial := range map[string]string{"compute": "41", "storage": "57"} {
		if !strings.Contains(out, m) {
			t.Errorf("state status does not mention the module %s:\n%s", m, out)
		}
		cur := f.versions(m)[0]
		if !cur.Current || !strings.Contains(out, cur.ID) {
			t.Errorf("state status does not show the current version id %s of %s:\n%s", cur.ID, m, out)
		}
		if !strings.Contains(out, f.lineage[m]) {
			t.Errorf("state status does not show the lineage of %s:\n%s", m, out)
		}
		if !strings.Contains(out, serial) {
			t.Errorf("state status does not show the serial %s of %s:\n%s", serial, m, out)
		}
	}
	if !strings.Contains(out, "other@host") || !strings.Contains(out, "11111111-2222-3333-4444-555555555555") {
		t.Errorf("state status does not show the holder and id of the compute state lock:\n%s", out)
	}
	if n := f.fake.count("decrypt", ""); n != 0 {
		t.Errorf("state status decrypted %d times; serial and lineage are plaintext", n)
	}
	// --module narrows it
	code, out = f.run("", nil, "--non-interactive", "state", "status", "--module", "compute")
	requireExit(t, code, 0, out)
	if strings.Contains(out, f.lineage["storage"]) || !strings.Contains(out, f.lineage["compute"]) {
		t.Errorf("state status --module compute shows the wrong modules:\n%s", out)
	}
}

// 06-AC-8: after a garbage upload as a new version, `./bootstrap status` reports TOFU_STATE_INVALID and names the module;
// before it, it does not.
func TestStatus_ReportsInvalidCurrentState_06AC8(t *testing.T) {
	f := newStateFix(t)
	code, out := f.run("", nil, "status")
	requireExit(t, code, 0, out)
	if strings.Contains(out, "TOFU_STATE_INVALID") {
		t.Fatalf("status reports TOFU_STATE_INVALID on healthy states:\n%s", out)
	}
	f.corrupt("compute")
	_, out = f.run("", nil, "status")
	if !strings.Contains(out, "TOFU_STATE_INVALID") {
		t.Fatalf("status does not report TOFU_STATE_INVALID after the compute state was replaced by garbage:\n%s", out)
	}
	if !strings.Contains(out, "compute") {
		t.Errorf("status does not name the module:\n%s", out)
	}
	if strings.Contains(out, "GARBAGE") {
		t.Errorf("status echoes the damaged object:\n%s", out)
	}
	_, out = f.run("", nil, "status", "--json")
	if !strings.Contains(out, "TOFU_STATE_INVALID") {
		t.Errorf("status --json does not report TOFU_STATE_INVALID:\n%s", out)
	}
}

// --- state restore

// 06-AC-8, plan 06 section 20.2: restoring the version before the garbage copies it onto the key as a new version; the history
// keeps growing by exactly one version, the content is the restored version's, and the state is valid again.
func TestStateRestore_CopiesPreviousVersionAsNewVersion_06AC8(t *testing.T) {
	f := newStateFix(t)
	storageBefore := len(f.versions("storage"))
	f.corrupt("compute")
	before := f.versions("compute")
	if len(before) != 3 {
		t.Fatalf("fixture: %d compute versions, want 3", len(before))
	}
	prev := before[1]
	want, err := f.store().GetVersion(context.Background(), stKey("compute"), prev.ID)
	if err != nil {
		t.Fatal(err)
	}
	oldIDs := f.versionIDs("compute")

	code, out := f.run("restore "+clusterName+" compute "+short(prev.ID)+"\n", nil, "state", "restore", "--module", "compute", "--version-id", prev.ID)
	requireExit(t, code, 0, out)

	after := f.versions("compute")
	if len(after) != len(before)+1 {
		t.Fatalf("compute version count %d -> %d, want +1 (history preserved)", len(before), len(after))
	}
	for id := range oldIDs {
		if !f.versionIDs("compute")[id] {
			t.Errorf("version %s disappeared: restore must never delete history", id)
		}
	}
	if !after[0].Current || after[0].ID == prev.ID || oldIDs[after[0].ID] {
		t.Errorf("the new current version %+v is not a new version", after[0])
	}
	if !bytes.Equal(f.current("compute"), want.Data) {
		t.Errorf("the restored current object differs from version %s", prev.ID)
	}
	if f.fake.count("decrypt", "compute") == 0 {
		t.Error("the version was restored without being decrypted and validated first (section 20.2)")
	}
	for _, c := range f.fake.snapshot() {
		if c.Op == "decrypt" && (c.Enc.Current != f.pass1 || c.Enc.Generation != 1 || c.Enc.Previous != "") {
			t.Errorf("decrypt call used %+v, want the bundle's passphrase as generation 1 and no fallback", c.Enc)
		}
	}
	_, out = f.run("", nil, "status")
	if strings.Contains(out, "TOFU_STATE_INVALID") {
		t.Errorf("status still reports TOFU_STATE_INVALID after the restore:\n%s", out)
	}
	// the other module is untouched
	if n := len(f.versions("storage")); n != storageBefore {
		t.Errorf("storage has %d versions, want %d", n, storageBefore)
	}
}

// 06-AC-8, plan 06 section 7.2: the restore needs the typed confirmation `restore <cluster> <module> <V[0:8]>`; a wrong text and
// --non-interactive without one exit 5 and change nothing.
func TestStateRestore_RequiresTypedConfirmation_06AC8(t *testing.T) {
	f := newStateFix(t)
	f.corrupt("compute")
	before := f.versions("compute")
	prev := before[1]
	args := []string{"state", "restore", "--module", "compute", "--version-id", prev.ID}

	code, out := f.run("restore "+clusterName+" compute wrong\n", nil, args...)
	requireExit(t, code, 5, out)
	code, out = f.run("", nil, append([]string{"--non-interactive"}, args...)...)
	requireExit(t, code, 5, out)
	code, out = f.run("restore "+clusterName+" storage "+short(prev.ID)+"\n", nil, args...)
	requireExit(t, code, 5, out)
	if got := f.versions("compute"); len(got) != len(before) {
		t.Errorf("compute has %d versions after refused restores, want %d", len(got), len(before))
	}
}

// 06-AC-8, plan 06 section 20.2: a version that does not validate (the garbage itself) is refused with TOFU_STATE_INVALID, and an
// unknown version id is TOFU_VERSION_NOT_FOUND. Nothing is written.
func TestStateRestore_RefusesUnreadableOrUnknownVersion_06AC8(t *testing.T) {
	f := newStateFix(t)
	f.corrupt("compute")
	vs := f.versions("compute")
	garbage := vs[0]
	n := len(vs)
	for name, tc := range map[string]struct {
		id   string
		code string
	}{
		"the garbage version": {garbage.ID, "TOFU_STATE_INVALID"},
		"an unknown version":  {"v99999", "TOFU_VERSION_NOT_FOUND"},
	} {
		code, out := f.run("restore "+clusterName+" compute "+short(tc.id)+"\n", nil, "state", "restore", "--module", "compute", "--version-id", tc.id)
		if code == 0 {
			t.Errorf("%s: restore exited 0:\n%s", name, out)
		}
		if !strings.Contains(out, tc.code) {
			t.Errorf("%s: output lacks %s:\n%s", name, tc.code, out)
		}
		if got := len(f.versions("compute")); got != n {
			t.Errorf("%s: %d versions, want %d (nothing may be written)", name, got, n)
		}
	}
}

// Plan 06 section 17: restoring the version that is already current is a no-op.
func TestStateRestore_CurrentVersionIsNoOp_06AC8(t *testing.T) {
	f := newStateFix(t)
	cur := f.versions("compute")[0]
	n := len(f.versions("compute"))
	code, out := f.run("restore "+clusterName+" compute "+short(cur.ID)+"\n", nil, "state", "restore", "--module", "compute", "--version-id", cur.ID)
	requireExit(t, code, 0, out)
	if got := len(f.versions("compute")); got != n {
		t.Errorf("restoring the current version created a version (%d -> %d)", n, got)
	}
}

// Plan 06 sections 9 and 15: a state held by someone else's lock is not restored over; the refusal is TOFU_STATE_LOCKED.
func TestStateRestore_RefusesWhileStateIsLocked_06AC3(t *testing.T) {
	f := newStateFix(t)
	f.corrupt("compute")
	before := f.versions("compute")
	f.lock("compute", "other@host")
	code, out := f.run("restore "+clusterName+" compute "+short(before[1].ID)+"\n", nil, "state", "restore", "--module", "compute", "--version-id", before[1].ID)
	if code == 0 || !strings.Contains(out, "TOFU_STATE_LOCKED") {
		t.Errorf("restore under a held lock: exit %d, want non-zero with TOFU_STATE_LOCKED:\n%s", code, out)
	}
	if got := len(f.versions("compute")); got != len(before) {
		t.Errorf("restore under a held lock wrote a version (%d -> %d)", len(before), got)
	}
}

// --- state verify (plan 06 section 20.1)

type stResult struct {
	TestID       string   `json:"test_id"`
	TestKind     string   `json:"test_kind"`
	Subject      string   `json:"subject"`
	Outcome      string   `json:"outcome"`
	Modules      []string `json:"modules"`
	Installation struct {
		Cluster string `json:"cluster"`
	} `json:"installation"`
	Failure *struct {
		Code string `json:"code"`
	} `json:"failure"`
	Checks []struct {
		ID      string `json:"id"`
		Outcome string `json:"outcome"`
	} `json:"checks"`
}

// stValidateResult checks a restore-test-result document against the real contract.
func stValidateResult(t testing.TB, raw []byte) {
	t.Helper()
	schemas, err := contracts.CompileSchemas(filepath.Join(repoRoot(t), "contracts"))
	if err != nil {
		t.Fatal(err)
	}
	sc := schemas["restore-test-result"]
	if sc == nil {
		t.Fatal("no restore-test-result schema")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var inst any
	if err := dec.Decode(&inst); err != nil {
		t.Fatalf("the result is not JSON: %v\n%s", err, raw)
	}
	if err := sc.Validate(inst); err != nil {
		t.Errorf("the result does not validate against restore-test-result.schema.yaml: %v\n%s", err, raw)
	}
}

// stResultObject finds the single restore-tests object in the tofu-state bucket.
func stResultObject(t testing.TB, w *s3World) (stResult, []byte) {
	t.Helper()
	keys := w.objects(bucketName("tofu-state"), stRestoreTests)
	if len(keys) != 1 {
		t.Fatalf("restore-tests objects = %v, want exactly one", keys)
	}
	if !strings.HasSuffix(keys[0], ".json") {
		t.Errorf("result key %s does not end in .json", keys[0])
	}
	raw, _ := w.read(bucketName("tofu-state"), keys[0])
	var r stResult
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatalf("result is not JSON: %v", err)
	}
	return r, raw
}

// 06-AC-8, plan 06 section 20.1: `state verify` on healthy states exits 0, decrypts and plans every module, and writes a valid
// restore-test-result with the module set, outcome passed and no secret.
func TestStateVerify_WritesValidResult_06AC14(t *testing.T) {
	f := newStateFix(t)
	code, out := f.run("", nil, "--non-interactive", "state", "verify")
	requireExit(t, code, 0, out)
	r, raw := stResultObject(t, f.i.world)
	stValidateResult(t, raw)
	if r.TestID != "rt-tofu-state" || r.TestKind != "opentofu-state" || r.Subject != "opentofu-state" || r.Outcome != "passed" {
		t.Errorf("result header %+v, want rt-tofu-state / opentofu-state / opentofu-state / passed", r)
	}
	if r.Installation.Cluster != clusterName {
		t.Errorf("result installation.cluster = %q", r.Installation.Cluster)
	}
	if strings.Join(r.Modules, ",") != "compute,storage" {
		t.Errorf("result modules = %v, want [compute storage]", r.Modules)
	}
	if len(r.Checks) == 0 {
		t.Error("the result has no checks")
	}
	for _, c := range r.Checks {
		if c.Outcome != "passed" {
			t.Errorf("check %s is %s on healthy states", c.ID, c.Outcome)
		}
	}
	for _, m := range stModules {
		if f.fake.count("decrypt", m) == 0 {
			t.Errorf("verify never decrypted the %s state", m)
		}
		if f.fake.count("plan", m) == 0 {
			t.Errorf("verify never ran the refresh-free plan on a copy of the %s state (step 4)", m)
		}
	}
	if bytes.Contains(raw, []byte(f.pass1)) || strings.Contains(out, f.pass1) {
		t.Error("the passphrase appears in the result or the output")
	}
	if n := len(f.versions("compute")); n != 2 {
		t.Errorf("verify changed the compute history (%d versions): it is read-only apart from its result", n)
	}
}

// 06-AC-8, plan 06 section 20.1 step 3: an unreadable current state makes the verification fail: exit 1, and the failed
// result is written (valid against the contract, with a failure and a failed check).
func TestStateVerify_FailsOnDamagedState_06AC8(t *testing.T) {
	f := newStateFix(t)
	f.corrupt("compute")
	code, out := f.run("", nil, "--non-interactive", "state", "verify")
	requireExit(t, code, 1, out)
	r, raw := stResultObject(t, f.i.world)
	stValidateResult(t, raw)
	if r.Outcome != "failed" || r.Failure == nil || !strings.HasPrefix(r.Failure.Code, "RT_") {
		t.Errorf("result outcome %q failure %+v, want failed with an RT_ failure code", r.Outcome, r.Failure)
	}
	failed := 0
	for _, c := range r.Checks {
		if c.Outcome == "failed" {
			failed++
		}
	}
	if failed == 0 {
		t.Errorf("no failed check in %+v", r.Checks)
	}
}

// Plan 06 section 20.1 step 3: the serial must be monotonic between the current and the newest noncurrent version.
func TestStateVerify_FailsOnSerialRegression_06AC8(t *testing.T) {
	f := newStateFix(t)
	f.put("compute", 1, f.pass1, 1)
	code, out := f.run("", nil, "--non-interactive", "state", "verify")
	requireExit(t, code, 1, out)
	r, raw := stResultObject(t, f.i.world)
	stValidateResult(t, raw)
	if r.Outcome != "failed" {
		t.Errorf("result outcome %q, want failed (current serial 1 after serial 2)", r.Outcome)
	}
}

// 06-AC-14 (BOOT-4): the `existing` provider with bucket mode `existing` has no OpenTofu module: `state verify` exits 0 and writes
// a result with modules: [] and outcome passed, with the one check state-not-applicable. It needs no bundle and no passphrase.
func TestStateVerify_NoModuleInstall_06AC14(t *testing.T) {
	i := newInst(t)
	i.setVars(map[string]string{"PROVIDER": "existing", "S3_BUCKET_MODE": "existing", "EXISTING_NODES": "node-01=ops@192.0.2.10/10.0.0.11"})
	if err := i.world.createBucket(stateKeyID, bucketName("tofu-state")); err != nil {
		t.Fatal(err)
	}
	fake := &stFake{w: i.world}
	d := i.deps("")
	d.State = fake
	env := merge(i.env, map[string]string{"SC_CONFIRM_BUDGET": budgetMonthly, "SC_CONFIRM_BACKSTOPS": i.backstopFile()})
	var out, errb bytes.Buffer
	code := install.Run(i.envOnlyArgs("--non-interactive", "state", "verify"), env, &out, &errb, d)
	requireExit(t, code, 0, out.String()+errb.String())

	r, raw := stResultObject(t, i.world)
	stValidateResult(t, raw)
	if r.Outcome != "passed" || r.Modules == nil || len(r.Modules) != 0 {
		t.Errorf("result outcome %q modules %v, want passed with modules: []", r.Outcome, r.Modules)
	}
	if !bytes.Contains(raw, []byte(`"modules"`)) || !regexp.MustCompile(`"modules":\s*\[\s*\]`).Match(raw) {
		t.Errorf("the result lacks an explicit empty modules array:\n%s", raw)
	}
	n := 0
	for _, c := range r.Checks {
		if c.ID == "state-not-applicable" {
			n++
		}
	}
	if len(r.Checks) != 1 || n != 1 {
		t.Errorf("result checks %+v, want exactly one check state-not-applicable", r.Checks)
	}
	if len(fake.snapshot()) != 0 {
		t.Errorf("a no-module install ran OpenTofu: %+v", fake.snapshot())
	}
}

// --- rotate tofu-passphrase (plan 06 section 20.3, 06-AC-6)

var stPassRe = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

// 06-AC-6: after `rotate tofu-passphrase` every module opens with P2 alone (generation 2) and none with P1 alone; the
// refresh-only apply ran with P2 enforced and P1 as the fallback; the new bundle version lists SC-TOFU-ENCRYPTION as the
// instances current (P2, generation 2) and previous (P1, generation 1), is on disk and in S3, and no passphrase leaks.
func TestRotateTofuPassphrase_06AC6(t *testing.T) {
	f := newStateFix(t)
	jb := f.i.journal().RecoveryBundle
	oldPath := jb.LocalPath
	oldBundle, err := os.ReadFile(oldPath)
	if err != nil {
		t.Fatal(err)
	}

	code, out := f.run("", nil, "--non-interactive", "rotate", "tofu-passphrase")
	requireExit(t, code, 0, out)

	nj := f.i.journal().RecoveryBundle
	if nj.Generation != jb.Generation+1 || nj.LocalPath == "" || nj.LocalPath == oldPath {
		t.Fatalf("journal bundle %+v after rotation, want generation %d at a new path (was %+v)", nj, jb.Generation+1, jb)
	}
	m, files := f.openBundle(nj.LocalPath)
	if m.BundleVersion != jb.Generation+1 {
		t.Errorf("manifest bundle_version = %d, want %d", m.BundleVersion, jb.Generation+1)
	}
	if m.PreviousBundleSha256 == nil || *m.PreviousBundleSha256 != sha256Hex(oldBundle) {
		t.Errorf("manifest previous_bundle_sha256 = %v, want the sha256 of the previous bundle %s", m.PreviousBundleSha256, sha256Hex(oldBundle))
	}
	got := map[string]string{}
	gens := map[string]int{}
	for _, e := range m.Entries {
		if e.Class == "SC-TOFU-ENCRYPTION" {
			got[e.Instance], gens[e.Instance] = string(files[e.Path]), e.Generation
		}
	}
	if len(got) != 2 || got["current"] == "" || got["previous"] == "" {
		t.Fatalf("manifest SC-TOFU-ENCRYPTION instances = %v, want exactly current and previous", got)
	}
	p1, p2 := got["previous"], got["current"]
	if p1 != f.pass1 {
		t.Error("the bundle's previous passphrase is not the passphrase the states were encrypted with")
	}
	if p2 == p1 || !stPassRe.MatchString(p2) {
		t.Errorf("the new passphrase is %d characters (want 43 of base64url) or equals the old one", len(p2))
	}
	if gens["current"] != 2 || gens["previous"] != 1 {
		t.Errorf("entry generations %v, want current 2 and previous 1", gens)
	}
	if m.InstallUID == "" || m.InstallUID != f.i.journal().InstallUID {
		t.Errorf("the rotated bundle's install_uid %q differs from the journal's", m.InstallUID)
	}

	if want := "s3://" + bucketName("recovery") + "/" + clusterName + "/bundles/sc-recovery-" + clusterName + fmt.Sprintf("-v%d.tar.age", nj.Generation); nj.S3URI != want {
		t.Errorf("journal s3_uri = %q, want %q", nj.S3URI, want)
	}
	up, ok := f.i.world.read(bucketName("recovery"), clusterName+"/bundles/sc-recovery-"+clusterName+fmt.Sprintf("-v%d.tar.age", nj.Generation))
	newBundle, _ := os.ReadFile(nj.LocalPath)
	if !ok || !bytes.Equal(up, newBundle) {
		t.Errorf("the rotated bundle is not in the recovery bucket (found %v)", ok)
	}

	for _, mod := range stModules {
		raw := f.current(mod)
		if _, _, err := f.fake.open(raw, tofustate.Encryption{Current: p2, Generation: 2}); err != nil {
			t.Errorf("%s: state pull with only P2 fails: %v", mod, err)
		}
		if _, _, err := f.fake.open(raw, tofustate.Encryption{Current: p1, Generation: 1}); err == nil {
			t.Errorf("%s: state pull with only P1 succeeds", mod)
		}
		refreshes, p2only := 0, false
		for _, c := range f.fake.snapshot() {
			if c.Module != mod {
				continue
			}
			switch {
			case c.Op == "refresh":
				refreshes++
				if c.Enc != (tofustate.Encryption{Current: p2, Generation: 2, Previous: p1}) {
					t.Errorf("%s: refresh-only apply ran with %+v, want P2 enforced as generation 2 and P1 as the fallback", mod, c.Enc)
				}
			case c.Op == "decrypt" && refreshes > 0 && c.Enc == (tofustate.Encryption{Current: p2, Generation: 2}):
				p2only = true
			}
		}
		if refreshes != 1 {
			t.Errorf("%s: %d refresh-only applies, want 1", mod, refreshes)
		}
		if !p2only {
			t.Errorf("%s: no verification with P2 alone (no fallback) after the refresh (section 20.3 step 4)", mod)
		}
	}

	if strings.Contains(out, p1) || strings.Contains(out, p2) {
		t.Error("a passphrase appears in the command output")
	}
	tree := treeText(t, f.i.repo)
	hist := f.i.git("log", "-p", "--all")
	for name, p := range map[string]string{"P1": p1, "P2": p2} {
		if strings.Contains(tree, p) {
			t.Errorf("%s appears in a file under the repository (.bootstrap included)", name)
		}
		if strings.Contains(hist, p) {
			t.Errorf("%s appears in the Git history", name)
		}
	}
}

// 06-AC-6 and plan 50 section 7.2: a wrong bundle passphrase exits 8 before any state is touched.
func TestRotateTofuPassphrase_WrongBundlePassphraseExits8_06AC6(t *testing.T) {
	f := newStateFix(t)
	jb := f.i.journal().RecoveryBundle
	bad := write(t, filepath.Join(f.i.dir, "wrong-passphrase"), "not the recovery passphrase at all\n", 0o600)
	code, out := f.run("", map[string]string{"SC_RECOVERY_PASSPHRASE_FILE": bad}, "--non-interactive", "rotate", "tofu-passphrase")
	requireExit(t, code, 8, out)
	if n := f.fake.count("refresh", ""); n != 0 {
		t.Errorf("%d refresh-only applies ran although the bundle did not open", n)
	}
	if nj := f.i.journal().RecoveryBundle; nj.Generation != jb.Generation || nj.LocalPath != jb.LocalPath {
		t.Errorf("the bundle changed: %+v -> %+v", jb, nj)
	}
	for _, mod := range stModules {
		if _, _, err := f.fake.open(f.current(mod), tofustate.Encryption{Current: f.pass1, Generation: 1}); err != nil {
			t.Errorf("%s no longer opens with the original passphrase: %v", mod, err)
		}
	}
}

// --- install_uid in the bundle manifest (operator decision 2026-10-27)

func deriveUID(key []byte) string {
	h := sha256.Sum256(append([]byte("sc-install-uid\x00"), key...))
	b := h[:16]
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// identityStop runs a fake install up to plan.install-identity and returns the local bundle path.
func identityStop(t *testing.T) (*inst, map[string]string, []string, string) {
	t.Helper()
	i := newInst(t)
	env := merge(i.passphraseFile(), stopAfter("plan.install-identity"))
	args := i.args("--non-interactive")
	code, out := i.run("", env, args)
	requireExit(t, code, 0, out)
	return i, env, args, i.journal().RecoveryBundle.LocalPath
}

// The writer: a fresh installation's bundle carries the journal's install_uid in its manifest, and that uid is not the key-derived
// one (the derivation is only the fallback for bundles without the field).
func TestInstallIdentity_BundleManifestCarriesInstallUID_InstallUID(t *testing.T) {
	i, _, _, path := identityStop(t)
	m, files := stOpenBundle(t, path, goodPass)
	uid := i.journal().InstallUID
	if uid == "" {
		t.Fatal("no install_uid in the journal")
	}
	if m.InstallUID != uid {
		t.Errorf("manifest install_uid = %q, want the journal's %q", m.InstallUID, uid)
	}
	if m.InstallID != clusterName {
		t.Errorf("manifest install_id = %q, want the cluster name", m.InstallID)
	}
	for _, e := range m.Entries {
		if e.Class == "SC-AGE-CLUSTER" {
			if uid == deriveUID(files[e.Path]) {
				t.Errorf("a fresh install's uid is the key-derived one; the derivation is only a fallback")
			}
		}
	}
}

// rewriteManifest re-seals the local bundle with a changed manifest (same files, same passphrase) and removes the journal and
// the installation commit, so the next run adopts the bundle (plan 50 section 14.3: the bundle is the source of truth).
func rewriteManifest(t *testing.T, i *inst, path string, mutate func(m map[string]any)) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	o, err := identity.Open(data, goodPass)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(o.Manifest, &m); err != nil {
		t.Fatal(err)
	}
	mutate(m)
	nm, _ := json.MarshalIndent(m, "", "  ")
	out := stReseal(t, o, nm)
	if err := os.WriteFile(path, out, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(i.repo, ".bootstrap", "journal.json")); err != nil {
		t.Fatal(err)
	}
	i.git("reset", "-q", "--hard", "HEAD~1")
	if len(i.installCommits()) != 0 {
		t.Fatal("fixture: the installation commit is still in the history")
	}
}

// Adoption (journal missing, bundle on disk): the uid comes from the manifest, and the installation commit carries it.
func TestInstallIdentity_AdoptionTakesUIDFromManifest_InstallUID(t *testing.T) {
	i, env, args, path := identityStop(t)
	const forced = "5f0c2b9e-7a41-4d8e-8b3a-9c1d2e3f4a5b"
	rewriteManifest(t, i, path, func(m map[string]any) { m["install_uid"] = forced })
	code, out := i.run("", env, args)
	requireExit(t, code, 0, out)
	if got := i.journal().InstallUID; got != forced {
		t.Errorf("install_uid after adoption = %q, want the manifest's %q", got, forced)
	}
	if got := i.git("log", "-1", "--format=%B", "--grep=SC-Install-Uid: "+forced); !strings.Contains(got, "SC-Install-Uid: "+forced) {
		t.Errorf("no commit carries SC-Install-Uid: %s", forced)
	}
}

// The fallback: a bundle without install_uid (older bundles) gets the uid derived from the SC-AGE-CLUSTER key.
func TestInstallIdentity_AdoptionFallsBackToDerivedUID_InstallUID(t *testing.T) {
	i, env, args, path := identityStop(t)
	m, files := stOpenBundle(t, path, goodPass)
	var key []byte
	for _, e := range m.Entries {
		if e.Class == "SC-AGE-CLUSTER" {
			key = files[e.Path]
		}
	}
	if len(key) == 0 {
		t.Fatal("no SC-AGE-CLUSTER entry in the bundle")
	}
	rewriteManifest(t, i, path, func(m map[string]any) { delete(m, "install_uid") })
	code, out := i.run("", env, args)
	requireExit(t, code, 0, out)
	if got, want := i.journal().InstallUID, deriveUID(key); got != want {
		t.Errorf("install_uid after adopting a bundle without the field = %q, want the key-derived %q", got, want)
	}
}

// stReseal builds a bundle archive from the opened bundle's files with another manifest and encrypts it with the test passphrase.
func stReseal(t testing.TB, o *identity.Opened, manifest []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	rcp, err := age.NewScryptRecipient(goodPass)
	if err != nil {
		t.Fatal(err)
	}
	rcp.SetWorkFactor(18)
	aw, err := age.Encrypt(&out, rcp)
	if err != nil {
		t.Fatal(err)
	}
	tw := tar.NewWriter(aw)
	for _, f := range o.Files {
		data := f.Data
		if f.Path == "manifest.json" {
			data = manifest
		}
		if err := tw.WriteHeader(&tar.Header{Name: f.Path, Mode: 0o600, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := aw.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}
