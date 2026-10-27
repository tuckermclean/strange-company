package engine_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"strange.company/sc/internal/bootstrap/engine"
)

func statusJSON(t *testing.T, v *tenv) map[string]any {
	t.Helper()
	var out, errb bytes.Buffer
	if exit := engine.Status(context.Background(), v.cfg(), true, &out, &errb); exit != 0 {
		t.Fatalf("status --json exit %d\nstdout: %s\nstderr: %s", exit, out.String(), errb.String())
	}
	return readJSON(t, out.Bytes(), "status --json stdout")
}

// 50-AC-9 (unit level): `status --json` prints the journal plus a computed next_action. The journal part validates
// against bootstrap-journal.schema.yaml for every journal the engine produces. (The contract has
// additionalProperties: false, so next_action is validated separately as a non-empty string.)
func TestStatusJSON_ValidatesAgainstJournalSchema_50AC9(t *testing.T) {
	cases := map[string]func(v *tenv){
		"succeeded run": func(v *tenv) {
			v.exec(pipe(newUnit("preflight"), newUnit("plan"), newUnit("provision")), engine.Options{})
		},
		"failed phase with a retryable error": func(v *tenv) {
			bad := newUnit("provision")
			bad.runFn = fail("PROVIDER_DELIVERY_TIMEOUT", true)
			bad.retryable = func(error) bool { return false } // do not retry in-run, keep the test small
			v.exec(pipe(newUnit("preflight"), newUnit("plan"), bad, newUnit("configure")), engine.Options{})
		},
		"phase with steps": func(v *tenv) {
			p := newUnit("plan", newUnit("plan.install-identity"), newUnit("plan.storage-apply"))
			v.exec(pipe(newUnit("preflight"), p), engine.Options{})
		},
		"confirmation recorded": func(v *tenv) {
			e := v.eng()
			e.RecordConfirmation(testConfirmation("storage-footprint"))
			e.Execute(context.Background(), pipe(newUnit("preflight")), engine.Options{})
		},
	}
	for name, mk := range cases {
		t.Run(name, func(t *testing.T) {
			v := newEnv(t)
			mk(v)
			got := statusJSON(t, v)
			na, ok := got["next_action"].(string)
			if !ok || strings.TrimSpace(na) == "" {
				t.Errorf("next_action = %v, want a non-empty string", got["next_action"])
			}
			delete(got, "next_action")
			journalSchemaValidate(t, got)
			if want := seqOf(v.remote()); seqOf(got) != want {
				t.Errorf("status journal_seq %d, want the stored %d", seqOf(got), want)
			}
			journalSchemaValidate(t, v.local())
			journalSchemaValidate(t, v.remote())
		})
	}
}

// Plan 50 section 21: status shows pipeline, phase, status, attempt, last error and next_action.
func TestStatusText_ShowsPhasesAndLastError_50AC9(t *testing.T) {
	v := newEnv(t)
	bad := newUnit("provision")
	bad.runFn = fail("PROVIDER_DELIVERY_TIMEOUT", false)
	v.exec(pipe(newUnit("preflight"), bad), engine.Options{})
	var out, errb bytes.Buffer
	if exit := engine.Status(context.Background(), v.cfg(), false, &out, &errb); exit != 0 {
		t.Fatalf("status exit %d: %s", exit, errb.String())
	}
	for _, want := range []string{"install", "preflight", "succeeded", "provision", "failed", "PROVIDER_DELIVERY_TIMEOUT", "next_action"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("status output lacks %q:\n%s", want, out.String())
		}
	}
}

// status does not take the run lock and does not write the journal.
func TestStatus_ReadOnlyAndLockFree_50AC9(t *testing.T) {
	v := newEnv(t)
	v.exec(pipe(newUnit("preflight")), engine.Options{})
	holder := v.eng()
	if res := holder.Acquire(context.Background()); res.Exit != 0 {
		t.Fatalf("Acquire exit %d", res.Exit)
	}
	before, _ := os.ReadFile(v.localPath())
	var out, errb bytes.Buffer
	if exit := engine.Status(context.Background(), v.cfg(), true, &out, &errb); exit != 0 {
		t.Fatalf("status exit %d while another run holds the lock: %s", exit, errb.String())
	}
	if after, _ := os.ReadFile(v.localPath()); !bytes.Equal(before, after) {
		t.Errorf("status modified the journal")
	}
	var m map[string]any
	if err := json.Unmarshal(out.Bytes(), &m); err != nil {
		t.Errorf("status --json is not JSON: %v", err)
	}
}
