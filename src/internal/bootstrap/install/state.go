package install

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"strange.company/sc/internal/bootstrap/engine"
	"strange.company/sc/internal/bootstrap/identity"
	"strange.company/sc/internal/bootstrap/store"
	"strange.company/sc/internal/s3client"
	"strange.company/sc/internal/tofustate"
	"strange.company/sc/internal/version"
)

// StateTofu is the OpenTofu seam of the state commands (plan 06 sections 14.5, 20.1 to 20.3). Production runs tofu in
// a scratch work dir with the encryption in the process environment; tests inject a fake.
type StateTofu interface {
	// Decrypt returns the plaintext tfstate of one stored version of a module, decrypted with e. It fails when neither e.Current
	// nor the fallback e.Previous opens it.
	Decrypt(ctx context.Context, module string, raw []byte, e tofustate.Encryption) ([]byte, error)
	// PlanCopy runs `tofu plan -refresh=false` against a local, encrypted copy of raw and returns the number of changes.
	PlanCopy(ctx context.Context, module string, raw []byte, e tofustate.Encryption) (changes int, err error)
	// RefreshOnly runs `tofu apply -refresh-only` for the module against the remote backend: the state is rewritten
	// and encrypted with e.Current (e.Previous is the fallback that still reads the old version).
	RefreshOnly(ctx context.Context, module string, remote RemoteState, e tofustate.Encryption) error
}

// --- the modules of an installation (plan 06 section 20.1)

// stateModules are the OpenTofu root modules this installation has: storage iff the buckets are created by sc-bootstrap,
// compute iff the provider uses OpenTofu (every provider but `existing`).
func (s *sess) stateModules() []string {
	var m []string
	if str(dig(s.doc, "spec", "provider", "name")) != "existing" {
		m = append(m, "compute")
	}
	if s.oc.bucketMode != "existing" {
		m = append(m, "storage")
	}
	return m
}

func (s *sess) stateStore() (tofustate.Store, error) {
	acct, err := s.account(s.stateCred())
	if err != nil {
		return nil, err
	}
	return acct.Bucket(s.bucketName("tofu-state")), nil
}

// stateSetup loads the configuration and prepares the session of a state command. It returns the exit code when the
// command cannot go on.
func (s *sess) stateSetup(ctx context.Context, command string) (int, bool) {
	if err := s.load(); err != nil {
		return s.configFailure(err), false
	}
	s.ensureDefaults()
	s.tryAttach(ctx)
	eng, err := engine.New(s.engineConfig(command))
	if err != nil {
		s.errorf("sc-bootstrap: %v\n", err)
		return 1, false
	}
	s.eng = eng
	return 0, true
}

// existingStateModules are the modules whose current state object exists.
func (s *sess) existingStateModules(ctx context.Context, st tofustate.Store) ([]string, error) {
	var out []string
	for _, m := range s.stateModules() {
		if _, err := st.Head(ctx, tofustate.StateKey(s.cluster, m)); err != nil {
			if store.Is(err, store.NotFound) {
				continue
			}
			return nil, err
		}
		out = append(out, m)
	}
	return out, nil
}

// --- state status|restore|verify

func (s *sess) cmdState(ctx context.Context) int {
	sub := s.inv.Command[1]
	code, ok := s.stateSetup(ctx, "state")
	if !ok {
		return code
	}
	switch sub {
	case "status":
		return s.stateStatus(ctx)
	case "restore":
		return s.stateRestore(ctx)
	}
	return s.stateVerify(ctx)
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

type moduleStatus struct {
	Module     string `json:"module"`
	Present    bool   `json:"present"`
	VersionID  string `json:"version_id,omitempty"`
	Modified   string `json:"modified,omitempty"`
	Serial     *int64 `json:"serial,omitempty"`
	Lineage    string `json:"lineage,omitempty"`
	Invalid    string `json:"invalid,omitempty"`
	InvalidMsg string `json:"invalid_message,omitempty"`
	Lock       *struct {
		ID        string `json:"id"`
		Who       string `json:"who"`
		Operation string `json:"operation"`
		Created   string `json:"created"`
	} `json:"lock,omitempty"`
}

// moduleStatuses reads, per module, the current version and its plaintext envelope fields (no decryption).
func (s *sess) moduleStatuses(ctx context.Context, st tofustate.Store, modules []string) ([]moduleStatus, error) {
	var out []moduleStatus
	for _, m := range modules {
		ms := moduleStatus{Module: m}
		vs, err := tofustate.ListVersions(ctx, st, s.cluster, m, 0)
		if err != nil {
			return nil, err
		}
		if len(vs) > 0 && vs[0].Current {
			ms.Present, ms.VersionID, ms.Modified = true, vs[0].ID, vs[0].Modified.UTC().Format(time.RFC3339)
			o, err := st.Get(ctx, tofustate.StateKey(s.cluster, m))
			if err != nil {
				return nil, err
			}
			if env, err := tofustate.ParseEnvelope(o.Data); err != nil {
				ms.Invalid, ms.InvalidMsg = "TOFU_STATE_INVALID", err.Error()
			} else {
				ms.Serial, ms.Lineage = &env.Serial, env.Lineage
			}
		}
		lk, err := tofustate.ReadLock(ctx, st, s.cluster, m)
		if err != nil {
			return nil, err
		}
		if lk.Present {
			ms.Lock = &struct {
				ID        string `json:"id"`
				Who       string `json:"who"`
				Operation string `json:"operation"`
				Created   string `json:"created"`
			}{lk.Info.ID, lk.Info.Who, lk.Info.Operation, lk.Info.Created.UTC().Format(time.RFC3339)}
		}
		out = append(out, ms)
	}
	return out, nil
}

func (s *sess) stateStatus(ctx context.Context) int {
	st, err := s.stateStore()
	if err != nil {
		s.errorf("sc-bootstrap: %v\n", err)
		return 1
	}
	modules := s.stateModules()
	if m := s.inv.Flags["module"]; m != "" {
		if !contains(modules, m) {
			s.errorf("sc-bootstrap: this installation has no module %q (modules: %s)\n", m, strings.Join(modules, ", "))
			return 2
		}
		modules = []string{m}
	}
	list, err := s.moduleStatuses(ctx, st, modules)
	if err != nil {
		s.errorf("sc-bootstrap: %v\n", wrapErr(err))
		return 1
	}
	if s.inv.Flags["json"] == "true" {
		b, _ := json.MarshalIndent(map[string]any{"cluster": s.cluster, "modules": list}, "", "  ")
		s.printf("%s\n", b)
		return 0
	}
	if len(list) == 0 {
		s.printf("This installation has no OpenTofu module.\n")
		return 0
	}
	for _, m := range list {
		switch {
		case !m.Present:
			s.printf("%s: no state object\n", m.Module)
		case m.Invalid != "":
			s.printf("%s: version %s modified %s %s: %s\n", m.Module, m.VersionID, m.Modified, m.Invalid, m.InvalidMsg)
		default:
			s.printf("%s: version %s modified %s serial %d lineage %s\n", m.Module, m.VersionID, m.Modified, *m.Serial, m.Lineage)
		}
		if m.Lock != nil {
			s.printf("  lock: held by %s (id %s, %s, since %s)\n", m.Lock.Who, m.Lock.ID, m.Lock.Operation, m.Lock.Created)
		} else {
			s.printf("  lock: none\n")
		}
	}
	return 0
}

func contains(l []string, v string) bool {
	for _, x := range l {
		if x == v {
			return true
		}
	}
	return false
}

// stateProblem is a damaged current state object `status` reports.
type stateProblem struct {
	Module  string `json:"module"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// stateProblems reports the modules whose current state object is not an OpenTofu encryption envelope. It needs no
// passphrase and never fails the command: an unreachable store reports nothing.
func (s *sess) stateProblems(ctx context.Context) []stateProblem {
	if !s.lazy.Attached() {
		return nil
	}
	st, err := s.stateStore()
	if err != nil {
		return nil
	}
	list, err := s.moduleStatuses(ctx, st, s.stateModules())
	if err != nil {
		return nil
	}
	var out []stateProblem
	for _, m := range list {
		if m.Invalid != "" {
			out = append(out, stateProblem{Module: m.Module, Code: m.Invalid, Message: "the current " + m.Module +
				" state object is not an OpenTofu encryption envelope; restore a previous version: ./bootstrap state restore --module " + m.Module + " --version-id <id>"})
		}
	}
	return out
}

// --- state restore (plan 06 sections 7.2 and 20.2)

func (s *sess) stateRestore(ctx context.Context) int {
	module, vid := s.inv.Flags["module"], s.inv.Flags["version-id"]
	if module == "" || vid == "" {
		s.errorf("sc-bootstrap: state restore needs --module and --version-id\n")
		return 2
	}
	if !contains(s.stateModules(), module) {
		s.errorf("sc-bootstrap: this installation has no module %q (modules: %s)\n", module, strings.Join(s.stateModules(), ", "))
		return 2
	}
	want := fmt.Sprintf("restore %s %s %s", s.cluster, module, shortID(vid))
	if s.inv.NonInteractive {
		s.errorf("STATE_RESTORE_CONFIRMATION_MISSING: restoring replaces the current %s state; run interactively and type '%s'\n", module, want)
		return 5
	}
	line, err := s.readLine(ctx, fmt.Sprintf("Type '%s' to make version %s the current %s state: ", want, vid, module), false)
	if err != nil {
		e := wrapErr(err)
		var ee *engine.Error
		if asEngineErr(e, &ee) {
			s.errorf("%s: %s\n", ee.Code, ee.Message)
			return orOne(ee.Exit)
		}
		s.errorf("sc-bootstrap: %v\n", err)
		return 1
	}
	if strings.TrimSpace(line) != want {
		s.errorf("STATE_RESTORE_CONFIRMATION_MISMATCH: the typed text is not '%s'; nothing was changed\n", want)
		return 5
	}
	var res tofustate.RestoreResult
	r := s.eng.ExecuteOp(ctx, func(ctx context.Context) error {
		st, err := s.stateStore()
		if err != nil {
			return wrapErr(err)
		}
		lk, err := tofustate.ReadLock(ctx, st, s.cluster, module)
		if err != nil {
			return wrapErr(err)
		}
		if lk.Present {
			return fail("TOFU_STATE_LOCKED", 1, procState, "the %s state is locked by %s (lock id %s, %s); wait for it, or inspect it with ./bootstrap state status",
				module, lk.Info.Who, lk.Info.ID, lk.Info.Operation)
		}
		enc, err := s.tofuEncryption(ctx)
		if err != nil {
			return wrapErr(err)
		}
		validate := func(raw []byte) error {
			if _, err := tofustate.ParseEnvelope(raw); err != nil {
				return err
			}
			_, err := s.deps.State.Decrypt(ctx, module, raw, enc)
			return err
		}
		res, err = tofustate.Restore(ctx, st, s.cluster, module, vid, validate)
		return wrapErr(err)
	})
	if r.Exit != 0 {
		s.errorf("%s: %s\n", r.Code, r.Message)
		return r.Exit
	}
	if res.NoOp {
		s.printf("Version %s is already the current %s state; nothing to do.\n", vid, module)
		return 0
	}
	s.printf("Version %s of the %s state is now the current version (new version %s); the history is kept. Run tofu plan to see the drift.\n", vid, module, res.NewVersionID)
	return 0
}

func orOne(n int) int {
	if n == 0 {
		return 1
	}
	return n
}

func asEngineErr(err error, target **engine.Error) bool {
	ee, ok := err.(*engine.Error)
	if ok {
		*target = ee
	}
	return ok
}

// --- state verify (plan 06 section 20.1)

type check struct {
	ID       string `json:"id"`
	Outcome  string `json:"outcome"`
	Observed string `json:"observed,omitempty"`
}

type verifyFailure struct{ Code, Message string }

func (s *sess) stateVerify(ctx context.Context) int {
	started := s.clk.Now().UTC()
	modules := s.stateModules()
	// The run lock keeps a rotation or restore in flight from making this look like a failure (plan 50 section 7.1).
	if r := s.eng.Acquire(ctx); r.Exit != 0 {
		s.errorf("%s: %s\n", r.Code, r.Message)
		return r.Exit
	}
	defer s.eng.Release(ctx)
	if r := s.eng.LoadJournal(ctx); r.Exit != 0 {
		s.errorf("%s: %s\n", r.Code, r.Message)
		return r.Exit
	}
	var checks []check
	var fails []verifyFailure
	if len(modules) == 0 {
		checks = []check{{ID: "state-not-applicable", Outcome: "passed", Observed: "no OpenTofu module in this installation"}}
	} else {
		enc, err := s.tofuEncryption(ctx)
		if err != nil {
			e := wrapErr(err)
			var ee *engine.Error
			if asEngineErr(e, &ee) {
				s.errorf("%s: %s\n", ee.Code, ee.Message)
				return orOne(ee.Exit)
			}
			s.errorf("sc-bootstrap: %v\n", err)
			return 1
		}
		st, err := s.stateStore()
		if err != nil {
			s.errorf("sc-bootstrap: %v\n", err)
			return 1
		}
		for _, m := range modules {
			c, f := s.verifyModule(ctx, st, m, enc)
			checks, fails = append(checks, c...), append(fails, f...)
		}
	}
	finished := s.clk.Now().UTC()
	outcome := "passed"
	if len(fails) > 0 {
		outcome = "failed"
	}
	doc := map[string]any{
		"schema_version":   "v1alpha1",
		"result_id":        resultID(s.cluster, started),
		"test_id":          "rt-tofu-state",
		"test_kind":        "opentofu-state",
		"subject":          "opentofu-state",
		"installation":     map[string]any{"cluster": s.cluster},
		"runner":           map[string]any{"kind": "bootstrap", "tool_version": orDefaultStr(version.Version, "dev")},
		"started_at":       started.Format(time.RFC3339),
		"finished_at":      finished.Format(time.RFC3339),
		"duration_seconds": finished.Sub(started).Seconds(),
		"outcome":          outcome,
		"modules":          append([]string{}, modules...),
		"checks":           checks,
	}
	if uid := s.eng.InstallUID(); len(uid) >= 8 {
		doc["installation"] = map[string]any{"cluster": s.cluster, "install_uid": uid}
	}
	if len(fails) > 0 {
		doc["failure"] = map[string]any{"code": fails[0].Code, "message": fails[0].Message}
	}
	body, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		s.errorf("sc-bootstrap: %v\n", err)
		return 1
	}
	body = s.red.Bytes(body)
	st, err := s.stateStore()
	if err != nil {
		s.errorf("sc-bootstrap: %v\n", err)
		return 1
	}
	key := s.cluster + "/restore-tests/" + started.Format("20060102T150405Z") + ".json"
	if _, err := st.PutObject(ctx, key, append(body, '\n'), s3client.PutOptions{}); err != nil {
		s.errorf("S3_REQUEST_FAILED: the restore test result cannot be written: %v\n", err)
		return 1
	}
	if len(fails) > 0 {
		for _, f := range fails {
			s.errorf("%s: %s\n", f.Code, f.Message)
		}
		s.errorf("state verify failed (result s3://%s/%s)\n", s.bucketName("tofu-state"), key)
		return 1
	}
	s.printf("state verify passed for %s (result s3://%s/%s)\n", moduleList(modules), s.bucketName("tofu-state"), key)
	return 0
}

func moduleList(m []string) string {
	if len(m) == 0 {
		return "no module"
	}
	return strings.Join(m, ", ")
}

func orDefaultStr(v, d string) string {
	if v == "" {
		return d
	}
	return v
}

// resultID is a UUID derived from the cluster and the start time, so the same invocation time gives the same document id.
func resultID(cluster string, t time.Time) string {
	h := sha256.Sum256([]byte("rt-tofu-state\x00" + cluster + "\x00" + t.Format(time.RFC3339Nano)))
	b := h[:16]
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

type plainState struct {
	Serial  int64  `json:"serial"`
	Lineage string `json:"lineage"`
}

func (s *sess) brief(err error) string {
	m := s.red.String(err.Error())
	if len(m) > 400 {
		m = m[:400]
	}
	return m
}

// verifyModule runs steps 1 to 4 of plan 06 section 20.1 for one module.
func (s *sess) verifyModule(ctx context.Context, st tofustate.Store, module string, enc tofustate.Encryption) ([]check, []verifyFailure) {
	var checks []check
	var fails []verifyFailure
	bad := func(id, code, msg string) {
		checks = append(checks, check{ID: module + "-" + id, Outcome: "failed", Observed: msg})
		fails = append(fails, verifyFailure{Code: code, Message: module + ": " + msg})
	}
	good := func(id, observed string) {
		checks = append(checks, check{ID: module + "-" + id, Outcome: "passed", Observed: observed})
	}
	key := tofustate.StateKey(s.cluster, module)
	vs, err := tofustate.ListVersions(ctx, st, s.cluster, module, 0)
	if err != nil {
		bad("versions", "RT_STATE_UNREADABLE", "the versions cannot be listed: "+s.brief(err))
		return checks, fails
	}
	if len(vs) == 0 || !vs[0].Current {
		bad("state-present", "RT_STATE_MISSING", "no current state object")
		return checks, fails
	}
	good("state-present", "version "+vs[0].ID)
	cur, err := st.GetVersion(ctx, key, vs[0].ID)
	if err != nil {
		bad("state-present", "RT_STATE_UNREADABLE", "the current version cannot be read: "+s.brief(err))
		return checks, fails
	}
	if _, err := tofustate.ParseEnvelope(cur.Data); err != nil {
		bad("envelope", "RT_STATE_INVALID", "the current version is not an OpenTofu encryption envelope: "+s.brief(err))
		return checks, fails
	}
	good("envelope", "encrypted envelope")
	plain, err := s.deps.State.Decrypt(ctx, module, cur.Data, enc)
	if err != nil {
		bad("decrypt-current", "RT_STATE_DECRYPT_FAILED", "the current version does not decrypt: "+s.brief(err))
		return checks, fails
	}
	var cp plainState
	if err := json.Unmarshal(plain, &cp); err != nil {
		bad("decrypt-current", "RT_STATE_INVALID", "the decrypted current version is not a tfstate")
		return checks, fails
	}
	good("decrypt-current", fmt.Sprintf("serial %d", cp.Serial))
	// the newest noncurrent version that is a state object at all (a garbage upload that was restored over stays in the history)
	var prev *plainState
	for _, v := range vs[1:] {
		o, err := st.GetVersion(ctx, key, v.ID)
		if err != nil {
			continue
		}
		if _, err := tofustate.ParseEnvelope(o.Data); err != nil {
			continue
		}
		pl, err := s.deps.State.Decrypt(ctx, module, o.Data, enc)
		if err != nil {
			bad("decrypt-previous", "RT_STATE_DECRYPT_FAILED", "version "+v.ID+" does not decrypt with the bundle's passphrases: "+s.brief(err))
			break
		}
		var pp plainState
		if json.Unmarshal(pl, &pp) != nil {
			bad("decrypt-previous", "RT_STATE_INVALID", "version "+v.ID+" is not a tfstate after decryption")
			break
		}
		good("decrypt-previous", fmt.Sprintf("version %s serial %d", v.ID, pp.Serial))
		prev = &pp
		break
	}
	if prev == nil {
		checks = append(checks, check{ID: module + "-serial-monotonic", Outcome: "skipped", Observed: "no earlier version to compare with"})
	} else {
		if prev.Lineage != cp.Lineage {
			bad("lineage-matches", "RT_STATE_LINEAGE_MISMATCH", "the lineage differs between the current and the previous version")
		} else {
			good("lineage-matches", "lineage "+cp.Lineage)
		}
		if cp.Serial < prev.Serial {
			bad("serial-monotonic", "RT_STATE_SERIAL_REGRESSION", fmt.Sprintf("the current serial %d is lower than the previous version's %d", cp.Serial, prev.Serial))
		} else {
			good("serial-monotonic", fmt.Sprintf("serial %d after %d", cp.Serial, prev.Serial))
		}
	}
	n, err := s.deps.State.PlanCopy(ctx, module, cur.Data, enc)
	if err != nil {
		bad("plan-refresh-false", "RT_STATE_PLAN_FAILED", "tofu plan -refresh=false on a copy of the current state failed: "+s.brief(err))
	} else {
		good("plan-refresh-false", fmt.Sprintf("%d changes against the configuration", n))
	}
	return checks, fails
}

// --- rotate tofu-passphrase (plan 06 section 20.3)

func (s *sess) cmdRotate(ctx context.Context) int {
	code, ok := s.stateSetup(ctx, "rotate")
	if !ok {
		return code
	}
	r := s.eng.ExecuteOp(ctx, s.rotateTofuPassphrase)
	if r.Exit != 0 {
		s.errorf("%s: %s\n", r.Code, r.Message)
		return r.Exit
	}
	return 0
}

func (s *sess) rotateTofuPassphrase(ctx context.Context) error {
	enc1, err := s.tofuEncryption(ctx)
	if err != nil {
		return wrapErr(err)
	}
	st, err := s.stateStore()
	if err != nil {
		return wrapErr(err)
	}
	modules, err := s.existingStateModules(ctx, st)
	if err != nil {
		return wrapErr(err)
	}
	rawOf := func(module string) ([]byte, error) {
		o, err := st.Get(ctx, tofustate.StateKey(s.cluster, module))
		return o.Data, err
	}
	// A rotation that died after sealing its bundle is finished first: the bundle is in the recovery bucket and the journal
	// before any state is rewritten with the passphrase it carries.
	settled, enc1, err := s.settleRotationBundle(ctx, enc1)
	if err != nil {
		return wrapErr(err)
	}
	alone := func(e tofustate.Encryption) tofustate.Encryption {
		return tofustate.Encryption{Current: e.Current, Generation: e.Generation}
	}
	rot := enc1
	// A bundle that already lists current and previous may come from a rotation that stopped before every module was
	// rewritten: finish that one instead of starting another.
	resume := settled
	if enc1.Previous != "" && !resume {
		for _, m := range modules {
			raw, err := rawOf(m)
			if err != nil {
				return wrapErr(err)
			}
			if _, err := s.deps.State.Decrypt(ctx, m, raw, alone(enc1)); err != nil {
				resume = true
			}
		}
	}
	if !resume {
		p2, err := identity.NewTofuPassphrase()
		if err != nil {
			return wrapErr(err)
		}
		s.red.Register("SC-TOFU-ENCRYPTION", p2)
		rot = tofustate.Encryption{Current: p2, Generation: enc1.Generation + 1, Previous: enc1.Current}
		// The bundle holds P2 before any state is rewritten with it.
		if err := s.resealWithRotation(ctx, rot); err != nil {
			return wrapErr(err)
		}
	}
	for _, m := range modules {
		raw, err := rawOf(m)
		if err != nil {
			return wrapErr(err)
		}
		if _, err := s.deps.State.Decrypt(ctx, m, raw, alone(rot)); err == nil {
			s.printf("%s: already encrypted with the new passphrase; skipped\n", m)
			continue
		}
		if err := s.deps.State.RefreshOnly(ctx, m, s.remoteStateOf(m), rot); err != nil {
			return fail("TOFU_ROTATION_FAILED", 1, procState, "the refresh-only apply of the %s module failed: %v; rerun ./bootstrap rotate tofu-passphrase to finish the rotation", m, s.brief(err))
		}
	}
	for _, m := range modules {
		raw, err := rawOf(m)
		if err != nil {
			return wrapErr(err)
		}
		if _, err := s.deps.State.Decrypt(ctx, m, raw, alone(rot)); err != nil {
			return fail("TOFU_ROTATION_FAILED", 1, procState, "the %s state does not open with the new passphrase alone: %v", m, s.brief(err))
		}
	}
	s.printf("OpenTofu state passphrase rotated to generation %d for %s; recovery bundle version %d lists the previous passphrase as SC-TOFU-ENCRYPTION/previous.\n",
		rot.Generation, moduleList(modules), s.eng.RecoveryBundle().Generation)
	return nil
}

// settleRotationBundle handles the two crash windows of a rotation (plan 06 section 20.3, plan 50 section 17). It reports
// whether it found a sealed rotation bundle and made sure it is uploaded and recorded.
//   - The journal says the bundle (already holding the rotation pair) is not uploaded: upload it and checkpoint.
//   - A local bundle v+1 exists that the journal does not know: adopt it when it lists (new, old current) at generations
//     N+1 and N, else fail with TOFU_ROTATION_BUNDLE_CONFLICT without touching anything.
func (s *sess) settleRotationBundle(ctx context.Context, enc tofustate.Encryption) (bool, tofustate.Encryption, error) {
	rb := s.eng.RecoveryBundle()
	if enc.Previous != "" && rb.Status != "uploaded" && rb.Status != "confirmed" && rb.LocalPath != "" {
		if _, err := s.uploadBundle(ctx); err != nil {
			return false, enc, err
		}
		return true, enc, s.eng.Checkpoint(ctx)
	}
	if rb.LocalPath == "" {
		return false, enc, nil
	}
	next := filepath.Join(filepath.Dir(rb.LocalPath), fmt.Sprintf("sc-recovery-%s-v%d.tar.age", s.cluster, rb.Generation+1))
	data, err := os.ReadFile(next)
	if err != nil {
		if os.IsNotExist(err) {
			return false, enc, nil
		}
		return false, enc, err
	}
	conflict := func(why string) error {
		return fail("TOFU_ROTATION_BUNDLE_CONFLICT", 1, procState,
			"%s exists but is not the sealed rotation of this install (%s); inspect it, and move it away only if it is not needed", next, why)
	}
	reg, err := s.register()
	if err != nil {
		return false, enc, err
	}
	pass, err := s.existingPassphrase(ctx)
	if err != nil {
		return false, enc, err
	}
	o, err := identity.Open(data, pass)
	if err != nil {
		return false, enc, conflict("it does not open with the install's recovery passphrase")
	}
	set, err := identity.SetFromBundle(reg, s.eng.InstallUID(), o)
	if err != nil {
		return false, enc, conflict("it is not a recovery bundle of this install")
	}
	ne, err := tofuEncryptionOf(set)
	if err != nil || ne.Previous != enc.Current || ne.Generation != enc.Generation+1 || ne.Current == enc.Current {
		return false, enc, conflict("its SC-TOFU-ENCRYPTION current/previous are not the new and the old passphrase at the next generations")
	}
	identity.RegisterSecrets(s.red, set, pass)
	s.set = set
	s.tofuGen, s.tofuPrev = ne.Generation, ne.Previous
	s.eng.SetRecoveryBundle(engine.RecoveryBundleState{Status: "created", Generation: rb.Generation + 1, Sha256: sha256Digest(data),
		LocalPath: next, CreatedAt: s.clk.Now().UTC().Format(time.RFC3339)})
	if err := s.eng.Checkpoint(ctx); err != nil {
		return false, enc, err
	}
	if _, err := s.uploadBundle(ctx); err != nil {
		return false, enc, err
	}
	return true, ne, s.eng.Checkpoint(ctx)
}

func (s *sess) remoteStateOf(module string) RemoteState {
	r := s.remoteState()
	r.Key = tofustate.StateKey(s.cluster, module)
	return r
}

// resealWithRotation seals the bundle with SC-TOFU-ENCRYPTION as the instances current (generation N+1) and previous
// (generation N), writes it next to the older versions, records it in the journal and uploads it.
func (s *sess) resealWithRotation(ctx context.Context, rot tofustate.Encryption) error {
	old := s.set
	ns := &identity.Set{InstallUID: old.InstallUID, Register: old.Register, ClusterRecipient: old.ClusterRecipient, RecoveryRecipient: old.RecoveryRecipient}
	for _, x := range old.Secrets {
		if x.Class != "SC-TOFU-ENCRYPTION" {
			ns.Secrets = append(ns.Secrets, x)
		}
	}
	ns.Secrets = append(ns.Secrets,
		identity.Secret{Class: "SC-TOFU-ENCRYPTION", Instance: "current", Format: "text", Value: []byte(rot.Current), InBundle: true, Generation: rot.Generation},
		identity.Secret{Class: "SC-TOFU-ENCRYPTION", Instance: "previous", Format: "text", Value: []byte(rot.Previous), InBundle: true, Generation: rot.Generation - 1},
	)
	cur := s.eng.RecoveryBundle()
	sealed, err := s.sealAs(ns, cur.Generation+1, strings.TrimPrefix(cur.Sha256, "sha256:"), "rotation")
	if err != nil {
		return err
	}
	path, err := s.writeBundleFiles(ns, cur.Generation+1, sealed)
	if err != nil {
		return err
	}
	s.eng.SetRecoveryBundle(engine.RecoveryBundleState{Status: "created", Generation: cur.Generation + 1, Sha256: "sha256:" + sealed.SHA256,
		LocalPath: path, CreatedAt: s.clk.Now().UTC().Format(time.RFC3339)})
	if err := s.eng.Checkpoint(ctx); err != nil {
		return err
	}
	if _, err := s.uploadBundle(ctx); err != nil {
		return err
	}
	s.set = ns
	s.tofuGen, s.tofuPrev = rot.Generation, rot.Previous
	return s.eng.Checkpoint(ctx)
}

// --- the production StateTofu

// ExecStateTofu is the production StateTofu: it runs the tofu binary in scratch work dirs with the encryption in the
// process environment only (plan 06 section 14.5).
type ExecStateTofu struct {
	// Repo is the repository root that holds infra/tofu.
	Repo string
	// Env is the process environment of sc-bootstrap (PATH, HOME, SC_TEST_MODE, proxy and plugin-cache settings).
	Env map[string]string
	// Vars returns the TF_VAR_* entries a module needs.
	Vars func(module string) []string
	// Bin is the tofu binary (default "tofu").
	Bin string
}

var _ StateTofu = (*ExecStateTofu)(nil)

func (t *ExecStateTofu) bin() string {
	if t.Bin != "" {
		return t.Bin
	}
	return "tofu"
}

// moduleDir is the module's configuration: infra/tofu/<module>, and for compute in test mode the fake module.
func (t *ExecStateTofu) moduleDir(module string) string {
	d := filepath.Join(t.Repo, "infra", "tofu", module)
	if module == "compute" && t.Env["SC_TEST_MODE"] == "1" {
		d = filepath.Join(d, "fake")
	}
	return d
}

func (t *ExecStateTofu) workDir(module string, withModule bool) (string, error) {
	dir, err := os.MkdirTemp("", "sc-state-")
	if err != nil {
		return "", err
	}
	if !withModule {
		return dir, nil
	}
	src := t.moduleDir(module)
	entries, err := os.ReadDir(src)
	if err != nil {
		os.RemoveAll(dir)
		return "", fmt.Errorf("the %s module is missing: %w", module, err)
	}
	for _, e := range entries {
		if e.IsDir() || (!strings.HasSuffix(e.Name(), ".tf") && e.Name() != ".terraform.lock.hcl") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(src, e.Name()))
		if err != nil {
			os.RemoveAll(dir)
			return "", err
		}
		if err := os.WriteFile(filepath.Join(dir, e.Name()), b, 0o600); err != nil {
			os.RemoveAll(dir)
			return "", err
		}
	}
	return dir, nil
}

func (t *ExecStateTofu) env(module string, s3 TofuS3, e tofustate.Encryption) []string {
	env := tofuEnv(t.Env, s3, e)
	if t.Vars != nil {
		env = append(env, t.Vars(module)...)
	}
	return env
}

func (t *ExecStateTofu) run(ctx context.Context, dir string, env []string, args ...string) (stdout string, exit int, err error) {
	cmd := exec.CommandContext(ctx, t.bin(), append([]string{"-chdir=" + dir}, args...)...)
	cmd.Env = env
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if rerr := cmd.Run(); rerr != nil {
		msg := strings.TrimSpace(errb.String())
		if len(msg) > 1500 {
			msg = msg[len(msg)-1500:]
		}
		if ee, ok := rerr.(*exec.ExitError); ok {
			return out.String(), ee.ExitCode(), fmt.Errorf("tofu %s: %w: %s", args[0], rerr, msg)
		}
		return out.String(), -1, fmt.Errorf("tofu %s: %w", args[0], rerr)
	}
	return out.String(), 0, nil
}

func (t *ExecStateTofu) localCopy(dir string, raw []byte) error {
	hcl, err := tofustate.RenderBackendLocal("terraform.tfstate")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "_backend.tf"), []byte(hcl), 0o600); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "terraform.tfstate"), raw, 0o600)
}

// Decrypt pulls the state of a local copy of raw in an empty work dir: reading a state needs no provider.
func (t *ExecStateTofu) Decrypt(ctx context.Context, module string, raw []byte, e tofustate.Encryption) ([]byte, error) {
	dir, err := t.workDir(module, false)
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	if err := t.localCopy(dir, raw); err != nil {
		return nil, err
	}
	env := t.env(module, TofuS3{}, e)
	if _, _, err := t.run(ctx, dir, env, "init", "-input=false", "-no-color"); err != nil {
		return nil, err
	}
	out, _, err := t.run(ctx, dir, env, "state", "pull")
	if err != nil {
		return nil, err
	}
	return []byte(out), nil
}

var planLineRe = regexp.MustCompile(`Plan: (\d+) to add, (\d+) to change, (\d+) to destroy`)

// PlanCopy runs `tofu plan -refresh=false` against a local, encrypted copy of the state.
func (t *ExecStateTofu) PlanCopy(ctx context.Context, module string, raw []byte, e tofustate.Encryption) (int, error) {
	dir, err := t.workDir(module, true)
	if err != nil {
		return 0, err
	}
	defer os.RemoveAll(dir)
	if err := t.localCopy(dir, raw); err != nil {
		return 0, err
	}
	env := t.env(module, TofuS3{}, e)
	if _, _, err := t.run(ctx, dir, env, "init", "-input=false", "-no-color"); err != nil {
		return 0, err
	}
	out, exit, err := t.run(ctx, dir, env, "plan", "-refresh=false", "-lock=false", "-detailed-exitcode", "-input=false", "-no-color")
	switch {
	case err == nil:
		return 0, nil
	case exit == 2:
		n := 1
		if m := planLineRe.FindStringSubmatch(out); m != nil {
			n = 0
			for _, g := range m[1:] {
				v, _ := strconv.Atoi(g)
				n += v
			}
		}
		return n, nil
	}
	return 0, err
}

// RefreshOnly rewrites the module's remote state under e.Current.
func (t *ExecStateTofu) RefreshOnly(ctx context.Context, module string, remote RemoteState, e tofustate.Encryption) error {
	dir, err := t.workDir(module, true)
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	hcl, err := remoteBackend(remote)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "_backend.tf"), []byte(hcl), 0o600); err != nil {
		return err
	}
	env := t.env(module, remote.S3, e)
	if _, _, err := t.run(ctx, dir, env, "init", "-input=false", "-no-color"); err != nil {
		return err
	}
	_, _, err = t.run(ctx, dir, env, "apply", "-refresh-only", "-auto-approve", "-input=false", "-no-color", "-lock-timeout=60s")
	return err
}

// stateVars are the TF_VAR_* entries of a module for the production StateTofu.
func (s *sess) stateVars(module string) []string {
	switch module {
	case "storage":
		return varsEnv(s.storageSpec(""))
	case "compute":
		return []string{"TF_VAR_cluster=" + s.cluster, "TF_VAR_sc_test_mode=" + s.env["SC_TEST_MODE"]}
	}
	return nil
}
