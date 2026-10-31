package crash_test

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"

	"strange.company/sc/internal/contracts"
)

// 50-AC-4 and 50-AC-9 (plan 50 section 22, BOOT-5): the crash matrix of the fake install. For every step boundary of the
// plan phase and of the stubbed later phases the engine runs as a subprocess with SC_TEST_MODE=1 and
// SC_TEST_CRASH_AT=<unit>:<before|after>, dies by SIGKILL (no deferred cleanup, the run lock is not released), and a
// plain rerun on the same host converges.
//
// Names the plans leave open and these tests fix:
//   - the crash points are the units the engine runs, each with :before and :after: the phases preflight, plan,
//     provision, seed-prep, configure, seed, forge-init, flux-bootstrap, sso-init, ci-init, verify and control-transfer,
//     and the ten steps of the plan phase (22 units, 44 points); the engine has no finer boundary in this build, and
//     the seed, forge-init (S00-S19) and transfer (T0-T8) sub-steps belong to the nodes that implement those phases;
//   - "the final journal with timestamps and run ids stripped equals the reference" is read as equality of the journal's
//     structure: installation state, provider and profile, the recovery bundle's status and generation, every pipeline
//     with its phases and steps and their statuses, and the kinds of the confirmations. The fields that differ between
//     two honest runs of the same install are not compared: timestamps, run ids, the runs list, journal_seq, install_uid
//     and the digests and references derived from it (input and config digests, bundle hashes and paths, version ids),
//     the attempt counters (a step the crash interrupted ran twice), and the stale-lock-takeover confirmation that only
//     the rerun after a crash has; a unit the rerun found done ("skipped") counts as succeeded;
//   - the status --json of the journal after the crash and after the rerun is validated when a local journal exists
//     (a crash before the first journal write leaves nothing to report).
//
// A rerun that finds the run lock object of the dead run takes it over and journals it exactly once; a crash before the
// lock object existed has no takeover.

var units = func() []string {
	u := []string{"preflight", "plan"}
	for _, s := range []string{"install-identity", "storage-confirm", "storage-apply", "bundle-upload", "storage-credentials",
		"state-conformance", "state-migrate", "bundle-confirm", "compute-plan", "confirm"} {
		u = append(u, "plan."+s)
	}
	u = append(u, "provision")
	return append(u, "seed-prep", "configure", "seed", "forge-init", "flux-bootstrap", "sso-init", "ci-init", "verify", "control-transfer")
}()

func crashPoints() []string {
	var out []string
	for _, u := range units {
		out = append(out, u+":before", u+":after")
	}
	return out
}

// --- the schema (50-AC-9)

var (
	schemaOnce sync.Once
	schemaErr  error
	journalSch interface{ Validate(any) error }
)

func validateJournal(t testing.TB, raw []byte) {
	t.Helper()
	schemaOnce.Do(func() {
		schemas, err := contracts.CompileSchemas(filepath.Join(repoRoot(t), "contracts"))
		if err != nil {
			schemaErr = err
			return
		}
		if sc := schemas["bootstrap-journal"]; sc != nil {
			journalSch = sc
		} else {
			schemaErr = fmt.Errorf("contracts/bootstrap-journal.schema.yaml is not among the compiled schemas")
		}
	})
	if schemaErr != nil {
		t.Fatalf("50-AC-9: %v", schemaErr)
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	var inst any
	if err := dec.Decode(&inst); err != nil {
		t.Errorf("50-AC-9: status --json does not print JSON: %v\n%s", err, tail(string(raw), 1500))
		return
	}
	if err := journalSch.Validate(inst); err != nil {
		t.Errorf("50-AC-9: status --json does not validate against bootstrap-journal.schema.yaml: %v", err)
	}
}

// statusValidates runs `status --json` and validates its output. A missing local journal has nothing to validate.
func (s *scenario) statusValidates(when string) {
	s.t.Helper()
	if _, ok := s.journalBytes(); !ok {
		return
	}
	res := s.run("", s.unattendedEnv(), s.runArgs("status", "--json"))
	if res.exit != 0 || res.signal != 0 {
		s.t.Errorf("50-AC-9: status --json %s: exit %d (signal %v), want 0:\n%s", when, res.exit, res.signal, tail(res.out, 1500))
		return
	}
	// status --json prints the journal plus the computed next_action (plan 50 section 7.1); the contract has
	// additionalProperties: false, so next_action is checked on its own and the rest is validated.
	var doc map[string]any
	if err := json.Unmarshal([]byte(res.stdout), &doc); err != nil {
		s.t.Errorf("50-AC-9: status --json %s does not print a JSON object: %v\n%s", when, err, tail(res.stdout, 1500))
		return
	}
	if na, _ := doc["next_action"].(string); strings.TrimSpace(na) == "" {
		s.t.Errorf("50-AC-9: status --json %s has no next_action string", when)
	}
	delete(doc, "next_action")
	raw, _ := json.Marshal(doc)
	validateJournal(s.t, raw)
}

// --- the projection (50-AC-4)

// done folds "skipped" into "succeeded": a rerun reports the work an earlier run did as skipped (plan 50 section 17.1,
// 50-AC-16), and the crash-free reference reports it as succeeded.
func done(status any) any {
	if status == "skipped" {
		return "succeeded"
	}
	return status
}

// projection is the part of the journal two honest runs of the same install have in common (see above).
func projection(j map[string]any) map[string]any {
	out := map[string]any{}
	if in, ok := j["installation"].(map[string]any); ok {
		out["installation"] = map[string]any{"state": in["state"], "provider": in["provider"], "profile": in["profile"]}
	}
	if rb, ok := j["recovery_bundle"].(map[string]any); ok {
		out["recovery_bundle"] = map[string]any{"status": rb["status"], "generation": rb["generation"]}
	}
	var pls []any
	pipes, _ := j["pipelines"].([]any)
	for _, p := range pipes {
		pm, _ := p.(map[string]any)
		var phases []any
		ph, _ := pm["phases"].([]any)
		for _, x := range ph {
			xm, _ := x.(map[string]any)
			var steps []any
			st, _ := xm["steps"].([]any)
			for _, y := range st {
				ym, _ := y.(map[string]any)
				steps = append(steps, map[string]any{"name": ym["name"], "status": done(ym["status"])})
			}
			phases = append(phases, map[string]any{"name": xm["name"], "status": done(xm["status"]), "steps": steps})
		}
		pls = append(pls, map[string]any{"pipeline": pm["pipeline"], "generation": pm["generation"], "status": pm["status"], "phases": phases})
	}
	out["pipelines"] = pls
	var kinds []string
	cs, _ := j["confirmations"].([]any)
	for _, c := range cs {
		if cm, ok := c.(map[string]any); ok {
			if k, _ := cm["kind"].(string); k != "stale-lock-takeover" {
				kinds = append(kinds, k)
			}
		}
	}
	sort.Strings(kinds)
	out["confirmations"] = kinds
	return out
}

func takeovers(j map[string]any) int {
	n := 0
	cs, _ := j["confirmations"].([]any)
	for _, c := range cs {
		if cm, ok := c.(map[string]any); ok && cm["kind"] == "stale-lock-takeover" {
			n++
		}
	}
	return n
}

// --- the crash-free reference

type reference struct {
	proj     map[string]any
	subjects []string
	err      string
}

var (
	refOnce sync.Once
	refVal  reference
)

// theReference is one crash-free run of the fake install, made once and shared by every crash point.
func theReference(t testing.TB) reference {
	t.Helper()
	refOnce.Do(func() {
		s := newScenario(t)
		res := s.run("", s.unattendedEnv(), s.runArgs("--non-interactive"))
		if res.exit != 0 || res.signal != 0 {
			refVal.err = fmt.Sprintf("the crash-free reference run: exit %d (signal %v):\n%s", res.exit, res.signal, tail(res.out, 3000))
			return
		}
		j := s.journal()
		refVal.proj = projection(j)
		refVal.subjects = commitSubjects(t, s.repo)
		sort.Strings(refVal.subjects)
		if in, _ := j["installation"].(map[string]any); in["state"] != "transferred" {
			refVal.err = fmt.Sprintf("the crash-free reference run ended in installation state %v, want transferred", in["state"])
		}
	})
	if refVal.err != "" {
		t.Fatal(refVal.err)
	}
	return refVal
}

func TestCrashMatrix_HasAtLeastThirtyPoints_50AC4(t *testing.T) {
	t.Parallel()
	if n := len(crashPoints()); n < 30 {
		t.Fatalf("50-AC-4: the matrix has %d crash points, want at least 30", n)
	}
}

// The reference run itself: exit 0, the install finished, one installation config commit, create counter 1, ten
// buckets created once each, and a status --json that validates.
func TestCrashMatrix_CrashFreeReferenceRun_50AC4_50AC9(t *testing.T) {
	t.Parallel()
	ref := theReference(t)
	s := newScenario(t)
	res := s.run("", s.unattendedEnv(), s.runArgs("--non-interactive"))
	s.requireExit(res, 0, "crash-free run")
	s.statusValidates("after a crash-free run")
	if !reflect.DeepEqual(projection(s.journal()), ref.proj) {
		t.Errorf("two crash-free runs of the same install have different journals:\n%v\n%v", projection(s.journal()), ref.proj)
	}
	s.assertConverged("crash-free run")
}

// assertConverged checks the effects every converged install has, whatever happened on the way.
func (s *scenario) assertConverged(what string) {
	s.t.Helper()
	subjects := commitSubjects(s.t, s.repo)
	if n := countSubject(subjects, s.installSubject()); n != 1 {
		s.t.Errorf("%s: git log has %d %q commits, want exactly one:\n%s", what, n, s.installSubject(), strings.Join(subjects, "\n"))
	}
	if n := s.createCount(); n != 1 {
		s.t.Errorf("%s: fake provider create counter = %d, want 1", what, n)
	}
	for _, p := range bucketPurposes {
		if n := s.createsOf(s.bucket(p)); n != 1 {
			s.t.Errorf("%s: CreateBucket for %s was requested %d times, want exactly once", what, s.bucket(p), n)
		}
	}
	if in, _ := s.journal()["installation"].(map[string]any); in["state"] != "transferred" {
		s.t.Errorf("%s: installation state %v, want transferred", what, in["state"])
	}
}

func TestCrashMatrix_RerunConvergesToTheReference_StatusValidates_50AC4_50AC9(t *testing.T) {
	t.Parallel()
	ref := theReference(t)
	for _, point := range crashPoints() {
		t.Run(strings.NewReplacer(":", "_").Replace(point), func(t *testing.T) {
			t.Parallel()
			s := newScenario(t)
			env := s.unattendedEnv()
			args := s.runArgs("--non-interactive")

			crashed := s.run("", merge(env, map[string]string{"SC_TEST_CRASH_AT": point}), args)
			if !crashed.killedByKill() {
				t.Fatalf("50-AC-4: SC_TEST_CRASH_AT=%s: the engine ended with exit %d (signal %v), want death by SIGKILL:\n%s",
					point, crashed.exit, crashed.signal, tail(crashed.out, 2000))
			}
			if strings.Contains(crashed.out, "panic:") || strings.Contains(crashed.out, "goroutine ") {
				t.Errorf("50-AC-4: SC_TEST_CRASH_AT=%s: the engine printed a Go panic instead of dying silently:\n%s", point, tail(crashed.out, 1500))
			}
			_, lockHeld := s.lockObject()
			s.statusValidates("after the crash at " + point)

			rerun := s.run("", env, args)
			if rerun.exit != 0 || rerun.signal != 0 {
				t.Fatalf("50-AC-4: rerun after %s: exit %d (signal %v), want 0:\n%s", point, rerun.exit, rerun.signal, tail(rerun.out, 3000))
			}
			s.statusValidates("after the rerun that followed " + point)

			j := s.journal()
			if got := projection(j); !reflect.DeepEqual(got, ref.proj) {
				gb, _ := json.MarshalIndent(got, "", " ")
				rb, _ := json.MarshalIndent(ref.proj, "", " ")
				t.Errorf("50-AC-4: the journal after %s and the rerun differs from the crash-free reference.\ngot:\n%s\nwant:\n%s", point, gb, rb)
			}
			s.assertConverged("after " + point)
			subjects := commitSubjects(t, s.repo)
			sort.Strings(subjects)
			if !reflect.DeepEqual(subjects, ref.subjects) {
				t.Errorf("50-AC-4: git log subjects after %s and the rerun:\n%s\nwant (the reference's):\n%s", point, strings.Join(subjects, "\n"), strings.Join(ref.subjects, "\n"))
			}
			wantTakeover := 0
			if lockHeld {
				wantTakeover = 1
			}
			if n := takeovers(j); n != wantTakeover {
				t.Errorf("50-AC-4: %d stale-lock-takeover confirmations after %s (the dead run's lock object was present: %v), want %d", n, point, lockHeld, wantTakeover)
			}
			if _, ok := s.lockObject(); ok {
				t.Errorf("50-AC-4: the run lock object remains after the rerun that followed %s", point)
			}
		})
	}
}

func merge(ms ...map[string]string) map[string]string {
	out := map[string]string{}
	for _, m := range ms {
		for k, v := range m {
			out[k] = v
		}
	}
	return out
}
