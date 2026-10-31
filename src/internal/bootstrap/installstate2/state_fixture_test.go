package installstate2_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"sync"
	"testing"

	"strange.company/sc/internal/bootstrap/identity"
	"strange.company/sc/internal/bootstrap/install"
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
