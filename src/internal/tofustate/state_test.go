package tofustate_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"strange.company/sc/internal/bootstrap/store"
	"strange.company/sc/internal/tofustate"
)

// The header OpenTofu writes around an encrypted state (observed with OpenTofu 1.13.1; encrypted_data shortened).
const realEnvelope = `{"serial":1,"lineage":"40fdcdb0-c0dd-f9ae-4f04-5a60a25e2b84","meta":{"key_provider.pbkdf2.current":"eyJzYWx0IjoiRTlBNTlpWXFEN2RpWFhmaC81V1NYMVdlQVkzQzFDcy9aRzB2NEVTR200ST0iLCJpdGVyYXRpb25zIjo2MDAwMDAsImhhc2hfZnVuY3Rpb24iOiJzaGE1MTIiLCJrZXlfbGVuZ3RoIjozMn0="},"encrypted_data":"+c/ndSoud8XGFQYMkE9Yzcx5Ws2yv6VitV9OuxJLLdSBn4x7lL8tZwqmhlBBNmeehsv5afr4Yra4Y41DjIEF0dZ","encryption_version":"v0"}`

const plaintextState = `{"version":4,"terraform_version":"1.13.1","serial":3,"lineage":"40fdcdb0-c0dd-f9ae-4f04-5a60a25e2b84","outputs":{},"resources":[],"check_results":null}`

func stateErr(t *testing.T, err error) *tofustate.Error {
	t.Helper()
	var e *tofustate.Error
	if !errors.As(err, &e) {
		t.Fatalf("error = %#v, want *tofustate.Error", err)
	}
	return e
}

// 06-AC-4 (library): only the OpenTofu encryption envelope is accepted as a state object.
func TestParseEnvelope_AcceptsOnlyEncryptedState_06AC4(t *testing.T) {
	env, err := tofustate.ParseEnvelope([]byte(realEnvelope))
	if err != nil {
		t.Fatalf("ParseEnvelope(real envelope): %v", err)
	}
	if env.Serial != 1 || env.Lineage != "40fdcdb0-c0dd-f9ae-4f04-5a60a25e2b84" || env.EncryptionVersion != "v0" {
		t.Errorf("Envelope = %+v", env)
	}
	for name, raw := range map[string]string{
		"plaintext state (accidental replacement)": plaintextState,
		"garbage":                         "this is not json at all\x00\x01",
		"empty":                           "",
		"json array":                      `[1,2,3]`,
		"encrypted_data is not a string":  `{"serial":1,"lineage":"l","encrypted_data":5}`,
		"encrypted_data is empty":         `{"serial":1,"lineage":"l","encrypted_data":"","encryption_version":"v0"}`,
		"no encrypted_data":               `{"serial":1,"lineage":"l","meta":{}}`,
		"envelope that also has resource": `{"serial":1,"lineage":"l","encrypted_data":"abc","resources":[]}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := tofustate.ParseEnvelope([]byte(raw))
			if err == nil {
				t.Fatal("ParseEnvelope accepted it")
			}
			if e := stateErr(t, err); e.Code != "TOFU_STATE_INVALID" {
				t.Errorf("Code = %q, want TOFU_STATE_INVALID", e.Code)
			}
		})
	}
}

func TestKeys_06AC3(t *testing.T) {
	if got, want := tofustate.StateKey("lab-one", "compute"), "lab-one/compute/terraform.tfstate"; got != want {
		t.Errorf("StateKey = %q, want %q", got, want)
	}
	if got, want := tofustate.LockKey("lab-one", "compute"), "lab-one/compute/terraform.tfstate.tflock"; got != want {
		t.Errorf("LockKey = %q, want %q", got, want)
	}
}

// OpenTofu 1.13.1's lock object, as observed on the emulator.
const realLock = `{"ID":"8ba6b987-d51d-6895-a9cb-081b09bed248","Operation":"OperationTypeApply","Info":"","Who":"debian@sc-toolbox","Version":"1.13.1","Created":"2026-10-27T07:51:44.194693873Z","Path":"lab-tofu-state/lab/compute/terraform.tfstate"}`

// 06-AC-3 (library): the lock holder is readable from the lock object.
func TestReadLock_06AC3(t *testing.T) {
	ctx := context.Background()
	m := newMemS3()
	st, err := tofustate.ReadLock(ctx, m, "lab", "compute")
	if err != nil || st.Present {
		t.Fatalf("ReadLock with no lock = %+v, %v; want Present=false", st, err)
	}
	m.seed("lab/compute/terraform.tfstate.tflock", realLock)
	st, err = tofustate.ReadLock(ctx, m, "lab", "compute")
	if err != nil || !st.Present {
		t.Fatalf("ReadLock = %+v, %v; want Present=true", st, err)
	}
	if st.Info.ID != "8ba6b987-d51d-6895-a9cb-081b09bed248" || st.Info.Operation != "OperationTypeApply" || st.Info.Who != "debian@sc-toolbox" || st.Info.Version != "1.13.1" {
		t.Errorf("Info = %+v", st.Info)
	}
	if want := time.Date(2026, 10, 27, 7, 51, 44, 194693873, time.UTC); !st.Info.Created.Equal(want) {
		t.Errorf("Created = %v, want %v", st.Info.Created, want)
	}
	if other, err := tofustate.ReadLock(ctx, m, "lab", "storage"); err != nil || other.Present {
		t.Errorf("the compute lock leaked into the storage module: %+v, %v", other, err)
	}
	for _, bad := range []string{"../x", "a/b", "", "Compute", "a b"} {
		if _, err := tofustate.ReadLock(ctx, m, "lab", bad); err == nil {
			t.Errorf("ReadLock accepted the module name %q", bad)
		}
	}
}

// 06-AC-3: what tofu prints when the lock is held (observed with OpenTofu 1.13.1, colors off).
const lockStderr = `
Error: Error acquiring the state lock

Error message: operation error S3: PutObject, https response error StatusCode:
412, RequestID: 18E25397CDCACCC07C182141, HostID: , api error PreconditionFailed:
At least one of the pre-conditions you specified did not hold
Lock Info:
  ID:        8ba6b987-d51d-6895-a9cb-081b09bed248
  Path:      lab-tofu-state/lab/compute/terraform.tfstate
  Operation: OperationTypeApply
  Who:       debian@sc-toolbox
`

const lockStderrBoxed = "╷\n│ Error: Error acquiring the state lock\n│ \n│ Lock Info:\n│   ID:        8ba6b987-d51d-6895-a9cb-081b09bed248\n│   Path:      lab-tofu-state/lab/compute/terraform.tfstate\n╵\n"

func TestLockStderr_06AC3(t *testing.T) {
	for name, s := range map[string]string{"plain": lockStderr, "boxed": lockStderrBoxed} {
		if !tofustate.IsLockError(s) {
			t.Errorf("%s: IsLockError = false for a state lock error", name)
		}
		if got := tofustate.LockIDFromStderr(s); got != "8ba6b987-d51d-6895-a9cb-081b09bed248" {
			t.Errorf("%s: LockIDFromStderr = %q", name, got)
		}
	}
	other := "Error: Failed to load state: AccessDenied\n"
	if tofustate.IsLockError(other) || tofustate.LockIDFromStderr(other) != "" {
		t.Error("an unrelated error is classified as a lock error")
	}
}

func stateKey() string { return tofustate.StateKey("lab", "compute") }

// versioned state fixture: s1 (older), s2, then garbage as the current version; the lock object has its own history.
func seedHistory(m *memS3) {
	m.seed(stateKey(), realEnvelope)
	m.seed(stateKey(), strings.Replace(realEnvelope, `"serial":1`, `"serial":2`, 1))
	m.seed(stateKey()+".tflock", realLock)
	m.seed(stateKey(), "GARBAGE-NOT-A-STATE")
}

// 06-AC-8 (library): newest first, limited, only the state object (not its lock), current marked.
func TestListVersions_06AC8(t *testing.T) {
	ctx := context.Background()
	m := newMemS3()
	seedHistory(m)
	vs, err := tofustate.ListVersions(ctx, m, "lab", "compute", 0)
	if err != nil {
		t.Fatalf("ListVersions: %v", err)
	}
	if len(vs) != 3 {
		t.Fatalf("ListVersions returned %d versions, want 3 (the lock object's version must not appear): %+v", len(vs), vs)
	}
	if !vs[0].Current || vs[1].Current || vs[2].Current {
		t.Errorf("Current flags = %v %v %v, want only the newest", vs[0].Current, vs[1].Current, vs[2].Current)
	}
	if !(vs[0].Modified.After(vs[1].Modified) && vs[1].Modified.After(vs[2].Modified)) {
		t.Errorf("versions are not newest first: %+v", vs)
	}
	if vs[0].Size != int64(len("GARBAGE-NOT-A-STATE")) {
		t.Errorf("Size = %d", vs[0].Size)
	}
	if lim, err := tofustate.ListVersions(ctx, m, "lab", "compute", 2); err != nil || len(lim) != 2 || lim[0].ID != vs[0].ID {
		t.Errorf("ListVersions(limit 2) = %+v, %v", lim, err)
	}
	if none, err := tofustate.ListVersions(ctx, m, "lab", "storage", 0); err != nil || len(none) != 0 {
		t.Errorf("ListVersions for a module without state = %+v, %v; want none", none, err)
	}
}

// Section 20.2: the newest non-current version that validates.
func TestSelectRestoreVersion_06AC8(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	vs := []tofustate.Version{
		{ID: "v4", Modified: now.Add(3 * time.Hour), Current: true},
		{ID: "v3", Modified: now.Add(2 * time.Hour)},
		{ID: "v2", Modified: now.Add(time.Hour)},
		{ID: "v1", Modified: now},
	}
	bad := errors.New("does not decrypt")
	got, err := tofustate.SelectRestoreVersion(vs, func(v tofustate.Version) error {
		if v.ID == "v3" {
			return bad
		}
		return nil
	})
	if err != nil || got.ID != "v2" {
		t.Errorf("SelectRestoreVersion = %+v, %v; want v2 (v4 is current, v3 invalid)", got, err)
	}
	var asked []string
	got, err = tofustate.SelectRestoreVersion(vs, func(v tofustate.Version) error { asked = append(asked, v.ID); return nil })
	if err != nil || got.ID != "v3" {
		t.Errorf("SelectRestoreVersion = %+v, %v; want v3", got, err)
	}
	for _, id := range asked {
		if id == "v4" {
			t.Error("the current version was offered for restoring")
		}
	}
	if _, err := tofustate.SelectRestoreVersion(vs, func(tofustate.Version) error { return bad }); err == nil {
		t.Error("no valid version: SelectRestoreVersion returned no error")
	}
	if _, err := tofustate.SelectRestoreVersion(vs[:1], func(tofustate.Version) error { return nil }); err == nil {
		t.Error("only the current version exists: SelectRestoreVersion returned no error")
	}
}

func versionIDs(t *testing.T, m *memS3) []string {
	t.Helper()
	vs, err := tofustate.ListVersions(context.Background(), m, "lab", "compute", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 3 {
		t.Fatalf("ListVersions returned %d versions, want 3: %+v", len(vs), vs)
	}
	ids := make([]string, len(vs))
	for i, v := range vs {
		ids[i] = v.ID
	}
	return ids
}

// 06-AC-8 (library): restore validates the old version, copies it onto the key as a new version, keeps history.
func TestRestore_06AC8(t *testing.T) {
	ctx := context.Background()
	m := newMemS3()
	seedHistory(m)
	ids := versionIDs(t, m) // newest first: garbage, s2, s1
	before := m.versionCount(stateKey())
	var validated []string
	res, err := tofustate.Restore(ctx, m, "lab", "compute", ids[1], func(raw []byte) error {
		validated = append(validated, string(raw))
		_, err := tofustate.ParseEnvelope(raw)
		return err
	})
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if res.NoOp || res.NewVersionID == "" {
		t.Errorf("RestoreResult = %+v, want a new version", res)
	}
	if len(validated) != 1 || !strings.Contains(validated[0], `"serial":2`) {
		t.Errorf("validate saw %q, want the restored version's bytes", validated)
	}
	if got := m.versionCount(stateKey()); got != before+1 {
		t.Errorf("version count = %d, want %d (history preserved, plus one)", got, before+1)
	}
	if !strings.Contains(m.content(stateKey()), `"serial":2`) {
		t.Errorf("current content is %q, want the restored version", m.content(stateKey()))
	}
	for _, id := range ids {
		if id == res.NewVersionID {
			t.Errorf("NewVersionID %s is an old version id", id)
		}
	}
	if m.content(stateKey()+".tflock") != realLock {
		t.Error("restore touched the lock object")
	}
}

func TestRestore_InvalidVersionChangesNothing_06AC8(t *testing.T) {
	ctx := context.Background()
	m := newMemS3()
	seedHistory(m)
	ids := versionIDs(t, m)
	before := m.versionCount(stateKey())
	// ids[0] is the garbage current version: validation must refuse it.
	_, err := tofustate.Restore(ctx, m, "lab", "compute", ids[2], func([]byte) error { return errors.New("wrong lineage") })
	if err == nil {
		t.Fatal("Restore accepted a version that failed validation")
	}
	if got := m.versionCount(stateKey()); got != before {
		t.Errorf("version count = %d after a refused restore, want %d", got, before)
	}
}

func TestRestore_NoOpWhenContentIsAlreadyCurrent_06AC8(t *testing.T) {
	ctx := context.Background()
	m := newMemS3()
	m.seed(stateKey(), realEnvelope)
	m.seed(stateKey(), "other")
	m.seed(stateKey(), realEnvelope) // current has the same bytes as version 1
	ids := versionIDs(t, m)          // newest first: cur, other, v1
	before := m.versionCount(stateKey())
	for name, id := range map[string]string{"the current version itself": ids[0], "an older version with the current content": ids[2]} {
		res, err := tofustate.Restore(ctx, m, "lab", "compute", id, func([]byte) error { return nil })
		if err != nil || !res.NoOp {
			t.Errorf("%s: Restore = %+v, %v; want NoOp", name, res, err)
		}
	}
	if got := m.versionCount(stateKey()); got != before {
		t.Errorf("version count = %d after no-op restores, want %d", got, before)
	}
}

func TestRestore_UnknownVersion_06AC8(t *testing.T) {
	m := newMemS3()
	seedHistory(m)
	_, err := tofustate.Restore(context.Background(), m, "lab", "compute", "v-does-not-exist", func([]byte) error { return nil })
	if err == nil {
		t.Fatal("Restore of an unknown version returned no error")
	}
	if e := stateErr(t, err); e.Code != "TOFU_VERSION_NOT_FOUND" {
		t.Errorf("Code = %q, want TOFU_VERSION_NOT_FOUND", e.Code)
	}
	if store.Is(err, store.Unavailable) {
		t.Error("an unknown version is not an outage")
	}
}
