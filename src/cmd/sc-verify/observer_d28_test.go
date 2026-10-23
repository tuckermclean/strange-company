package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"strange.company/sc/internal/contracts"
)

// F06 S7, RED 2 (D28 of the orchestrator's review of attempt 1): the replay export and the base tooling do not
// depend on workspace .git state (B1), the suite cannot read the run process (B2), run refuses what the
// repo-validation schema rejects (N2), a NUL byte does not hide a stub marker (N3), emit prints the plan 20
// section 8.4 shape (N4) and the command builds for darwin (N5).

const fxD28Protected = "change\tunit::fx.TestStillRed\npreservation\tunit::fx.TestBaseline"

// fxCatBlob returns the raw bytes of a path at a revision.
func fxCatBlob(t *testing.T, r *fxRepo, rev, path string) []byte {
	t.Helper()
	cmd := exec.Command("git", "cat-file", "blob", rev+":"+path)
	cmd.Dir, cmd.Env = r.ws, fxGitEnv(r.home)
	b, err := cmd.Output()
	if err != nil {
		t.Fatalf("git cat-file blob %s:%s: %v", rev, path, err)
	}
	return b
}

// fxReplayStatus returns the red_replay status of every listed test of a GREEN observation.
func fxReplayStatus(t *testing.T, doc fxDoc) map[string]string {
	t.Helper()
	if doc.Observed.RedReplay == nil {
		t.Fatalf("the GREEN observation has no red_replay")
	}
	got := map[string]string{}
	for _, x := range doc.Observed.RedReplay.Tests {
		got[x.ID] = x.Status
	}
	return got
}

// fxCheckSrcHash reads the srchash that the base checker of the d28 fixtures recorded for a check.
func fxCheckSrcHash(t *testing.T, r *fxRepo, name string) (tool, hash string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(r.out, "check-"+name+".txt"))
	if err != nil {
		t.Fatalf("check %s recorded nothing (the base checker did not run): %v", name, err)
	}
	for _, f := range strings.Fields(string(b)) {
		k, v, _ := strings.Cut(f, "=")
		switch k {
		case "tool":
			tool = v
		case "srchash":
			hash = v
		}
	}
	return tool, hash
}

func fxRequirePlanted(t *testing.T, r *fxRepo, rel, needle string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(r.ws, rel))
	if err != nil || !strings.Contains(string(b), needle) {
		t.Fatalf("the head code did not plant %q into %s (%v); the fixture proves nothing", needle, rel, err)
	}
}

// D28 (B1): head code writes `red_test.go export-ignore` into .git/info/attributes. The replay export must not
// honour it: TestStillRed is still exported and still fails against the stub, as in the existing replay test.
func TestRun_ReplayIgnoresHeadWrittenGitAttributes_D28B1(t *testing.T) {
	r := fxNewRepo(t, fxConfig{Fixture: "d28_attr", Suites: []fxSuite{fxGoSuite(nil)}})
	res := r.run(fxRunOpts{Phase: fxPhaseGreen, Protected: fxD28Protected})
	if res.Exit != 0 || res.TimedOut {
		t.Fatalf("run exit %d (timed out %v); stderr %q", res.Exit, res.TimedOut, fxTrunc(res.Stderr))
	}
	fxRequirePlanted(t, r, ".git/info/attributes", "red_test.go export-ignore")
	doc, _ := r.emitDoc(fxPhaseGreen)
	got := fxReplayStatus(t, doc)
	if got["unit::fx.TestStillRed"] != "failed_assertion" {
		t.Errorf("red_replay = %v, want unit::fx.TestStillRed = failed_assertion (the attribute must not drop red_test.go from the export)", got)
	}
	if len(got) != 1 {
		t.Errorf("red_replay lists %v, want only the change-criterion test", got)
	}
	if x, ok := doc.test("unit::fx.TestStillRed"); !ok || x.StatusHead != "passed" {
		t.Errorf("head run: TestStillRed = %+v (%v), want passed", x, ok)
	}
}

// D28 (B1): head code adds a smudge filter driver to .git/config and binds it to the stub and to the base checker.
// Neither the replay tree nor the base tooling tree may be touched by it. The base checker prints a hash of its
// own source; the test compares it with the blob committed at base.
func TestRun_ReplayAndBaseToolingIgnoreHeadWrittenFilterDrivers_D28B1(t *testing.T) {
	r := fxNewRepo(t, fxConfig{
		Fixture: "d28_filter", Shared: []string{"_d28check"}, Suites: []fxSuite{fxGoSuite(nil)},
		Checks: []fxCheck{{Name: "default-base", Argv: []string{"go", "run", "./tools/check", "default-base"}}},
	})
	res := r.run(fxRunOpts{Phase: fxPhaseGreen, Protected: fxD28Protected})
	if res.Exit != 0 || res.TimedOut {
		t.Fatalf("run exit %d (timed out %v); stderr %q", res.Exit, res.TimedOut, fxTrunc(res.Stderr))
	}
	fxRequirePlanted(t, r, ".git/config", `[filter "evil"]`)
	doc, _ := r.emitDoc(fxPhaseGreen)
	got := fxReplayStatus(t, doc)
	if got["unit::fx.TestStillRed"] != "failed_assertion" {
		t.Errorf("red_replay = %v, want unit::fx.TestStillRed = failed_assertion (a smudge filter must not turn the stub into the implementation)", got)
	}
	tool, hash := fxCheckSrcHash(t, r, "default-base")
	want := fxSHA256(fxCatBlob(t, r, r.base, "tools/check/main.go"))
	if tool != "base" || hash != want {
		t.Errorf("the base checker ran as %q with source hash %s, want the blob committed at base, %s (a filter driver changed the exported tooling)", tool, hash, want)
	}
	if len(doc.Observed.Checks) != 1 || doc.Observed.Checks[0].ExitCode != 0 {
		t.Errorf("checks = %+v, want the one base check to exit 0", doc.Observed.Checks)
	}
}

// D28 (B1): head code writes attributes that would drop the checker from `git archive` output. The base check
// result must not change: the checker still runs, from the exact base source.
func TestRun_BaseToolingIgnoresHeadWrittenGitAttributes_D28B1(t *testing.T) {
	r := fxNewRepo(t, fxConfig{
		Fixture: "d28_checkattr", Shared: []string{"_d28check"}, Suites: []fxSuite{fxGoSuite(nil)},
		Checks: []fxCheck{{Name: "default-base", Argv: []string{"go", "run", "./tools/check", "default-base"}}},
	})
	res := r.run(fxRunOpts{Phase: fxPhaseGreen, Protected: "preservation\tunit::fx.TestBaseline"})
	if res.Exit != 0 || res.TimedOut {
		t.Fatalf("run exit %d (timed out %v); stderr %q", res.Exit, res.TimedOut, fxTrunc(res.Stderr))
	}
	fxRequirePlanted(t, r, ".git/info/attributes", "tools/** export-ignore")
	doc, _ := r.emitDoc(fxPhaseGreen)
	tool, hash := fxCheckSrcHash(t, r, "default-base")
	want := fxSHA256(fxCatBlob(t, r, r.base, "tools/check/main.go"))
	if tool != "base" || hash != want {
		t.Errorf("the base checker ran as %q with source hash %s, want the blob committed at base, %s", tool, hash, want)
	}
	if len(doc.Observed.Checks) != 1 || doc.Observed.Checks[0].ExitCode != 0 {
		t.Errorf("checks = %+v, want the one base check to exit 0 (the planted attribute must not change the base check result)", doc.Observed.Checks)
	}
}

// D28 (B2): the suite finds the run process through /proc/<pid>/stat and tries to open its mem and environ files.
// Both must fail (EACCES or EPERM) or yield nothing: run is not dumpable.
func TestRun_SuiteCannotOpenRunProcMemOrEnviron_D28B2(t *testing.T) {
	r := fxNewRepo(t, fxConfig{Fixture: "d28_proc", Suites: []fxSuite{fxGoSuite(nil)}})
	res := r.run(fxRunOpts{Hostile: true})
	if res.Exit != 0 || res.TimedOut {
		t.Fatalf("run exit %d (timed out %v); stderr %q", res.Exit, res.TimedOut, fxTrunc(res.Stderr))
	}
	b, err := os.ReadFile(filepath.Join(r.out, "procspy.txt"))
	if err != nil {
		t.Fatalf("the spy recorded nothing: %v", err)
	}
	runs := 0
	for _, l := range strings.Split(string(b), "\n") {
		if !strings.HasPrefix(l, "RUN ") {
			continue
		}
		runs++
		f := map[string]string{}
		for _, kv := range strings.Fields(l) {
			k, v, _ := strings.Cut(kv, "=")
			f[k] = v
		}
		unreadable := func(status, n string) bool {
			return n == "0" && (status == "EACCES" || status == "EPERM" || strings.HasPrefix(status, "OPEN_OK_READ_"))
		}
		if !unreadable(f["mem"], f["memread"]) {
			t.Errorf("the suite could use /proc/<run>/mem: %s", l)
		}
		if !unreadable(f["environ"], f["environread"]) {
			t.Errorf("the suite could use /proc/<run>/environ: %s", l)
		}
	}
	if runs == 0 {
		t.Fatalf("the spy did not find the sc-verify process among its ancestors (by command line):\n%s", fxTrunc(string(b)))
	}
}

// D28 (N2): run refuses (exit 2) every validation.yaml that contracts/repo-validation.schema.yaml rejects. The
// cases are checked against the compiled real schema, so that the list cannot drift from it, and the schema's
// valid twins must still be observed (exit 0).
//
// Known contract defect N1 (excluded here): `report.path: "-"` matches both branches of the schema's `oneOf`, so
// the schema rejects every go-test-json suite on stdout. The schema verdict is therefore taken on a copy of the
// document whose report path is a plain repository path; `run` gets the real document with "-". Cases that are
// stricter in `run` than in the schema by design (SC_ environment names, `{}` in report paths, environment values
// counted in bytes) are not listed.
func TestRun_RefusesWhatTheRepoValidationSchemaRejects_D28N2(t *testing.T) {
	schemas, err := contracts.CompileSchemas(filepath.Join(fxRoot(t), "contracts"))
	if err != nil {
		t.Fatal(err)
	}
	schema := schemas["repo-validation"]
	if schema == nil {
		t.Fatal("no repo-validation schema")
	}
	spec := func(d map[string]any) map[string]any { return d["spec"].(map[string]any) }
	ver := func(d map[string]any) map[string]any { return spec(d)["verification"].(map[string]any) }
	check := func(extra map[string]any) []any {
		c := map[string]any{"name": "c", "argv": []any{"x"}, "timeoutSeconds": 10}
		for k, v := range extra {
			c[k] = v
		}
		return []any{c}
	}
	many := func(n int, v any) []any {
		l := make([]any, n)
		for i := range l {
			l[i] = v
		}
		return l
	}
	type tc struct {
		name   string
		accept bool // the schema accepts the document (a valid twin)
		mut    func(d map[string]any)
	}
	cases := []tc{
		{"valid-baseline", true, func(d map[string]any) {}},
		// metadata
		{"metadata-missing", false, func(d map[string]any) { delete(d, "metadata") }},
		{"metadata-null", false, func(d map[string]any) { d["metadata"] = nil }},
		{"metadata-not-a-mapping", false, func(d map[string]any) { d["metadata"] = "fx" }},
		{"metadata-name-missing", false, func(d map[string]any) { d["metadata"] = map[string]any{} }},
		{"metadata-name-space", false, func(d map[string]any) { d["metadata"] = map[string]any{"name": "a b"} }},
		{"metadata-name-101-chars", false, func(d map[string]any) { d["metadata"] = map[string]any{"name": strings.Repeat("a", 101)} }},
		{"metadata-name-not-a-string", false, func(d map[string]any) { d["metadata"] = map[string]any{"name": 7} }},
		{"metadata-extra-key", false, func(d map[string]any) { d["metadata"] = map[string]any{"name": "fx", "labels": "x"} }},
		{"metadata-valid-name-charset", true, func(d map[string]any) { d["metadata"] = map[string]any{"name": "Fx_1.2-b"} }},
		// spec.local
		{"local-garbage-string", false, func(d map[string]any) { spec(d)["local"] = "x" }},
		{"local-null", false, func(d map[string]any) { spec(d)["local"] = nil }},
		{"local-entry-empty", false, func(d map[string]any) { spec(d)["local"] = []any{map[string]any{}} }},
		{"local-entry-bad-name", false, func(d map[string]any) {
			spec(d)["local"] = []any{map[string]any{"name": "Lint", "argv": []any{"x"}, "timeoutSeconds": 5}}
		}},
		{"local-entry-timeout-0", false, func(d map[string]any) {
			spec(d)["local"] = []any{map[string]any{"name": "lint", "argv": []any{"x"}, "timeoutSeconds": 0}}
		}},
		{"local-11-entries", false, func(d map[string]any) {
			spec(d)["local"] = many(11, map[string]any{"name": "lint", "argv": []any{"x"}, "timeoutSeconds": 5})
		}},
		{"local-valid", true, func(d map[string]any) {
			spec(d)["local"] = []any{map[string]any{"name": "lint", "argv": []any{"x"}, "timeoutSeconds": 5}}
		}},
		// protectedTestIds
		{"protectedTestIds-bad-id", false, func(d map[string]any) { ver(d)["protectedTestIds"] = []any{"bad id"} }},
		{"protectedTestIds-empty-name", false, func(d map[string]any) { ver(d)["protectedTestIds"] = []any{"unit::"} }},
		{"protectedTestIds-not-an-array", false, func(d map[string]any) { ver(d)["protectedTestIds"] = "unit::x" }},
		{"protectedTestIds-null", false, func(d map[string]any) { ver(d)["protectedTestIds"] = nil }},
		{"protectedTestIds-not-a-string", false, func(d map[string]any) { ver(d)["protectedTestIds"] = []any{7} }},
		{"protectedTestIds-201", false, func(d map[string]any) { ver(d)["protectedTestIds"] = many(201, "unit::fx.TestBaseline") }},
		{"protectedTestIds-valid", true, func(d map[string]any) { ver(d)["protectedTestIds"] = []any{"unit::fx.TestBaseline"} }},
		// checks
		{"checks-null", false, func(d map[string]any) { ver(d)["checks"] = nil }},
		{"checks-not-an-array", false, func(d map[string]any) { ver(d)["checks"] = "x" }},
		{"checks-11", false, func(d map[string]any) { ver(d)["checks"] = many(11, check(nil)[0]) }},
		{"check-timeout-9", false, func(d map[string]any) { ver(d)["checks"] = check(map[string]any{"timeoutSeconds": 9}) }},
		{"check-toolingFrom-other", false, func(d map[string]any) { ver(d)["checks"] = check(map[string]any{"toolingFrom": "other"}) }},
		{"check-valid", true, func(d map[string]any) { ver(d)["checks"] = check(nil) }},
		{"check-valid-head-tooling", true, func(d map[string]any) { ver(d)["checks"] = check(map[string]any{"toolingFrom": "head"}) }},
		// check.configPaths
		{"configPaths-absolute", false, func(d map[string]any) { ver(d)["checks"] = check(map[string]any{"configPaths": []any{"/etc/passwd"}}) }},
		{"configPaths-dotdot", false, func(d map[string]any) { ver(d)["checks"] = check(map[string]any{"configPaths": []any{"a/../b"}}) }},
		{"configPaths-space", false, func(d map[string]any) { ver(d)["checks"] = check(map[string]any{"configPaths": []any{"a b"}}) }},
		{"configPaths-empty-entry", false, func(d map[string]any) { ver(d)["checks"] = check(map[string]any{"configPaths": []any{""}}) }},
		{"configPaths-not-an-array", false, func(d map[string]any) { ver(d)["checks"] = check(map[string]any{"configPaths": ".golangci.yml"}) }},
		{"configPaths-null", false, func(d map[string]any) { ver(d)["checks"] = check(map[string]any{"configPaths": nil}) }},
		{"configPaths-51", false, func(d map[string]any) { ver(d)["checks"] = check(map[string]any{"configPaths": many(51, "a.yml")}) }},
		{"configPaths-valid", true, func(d map[string]any) {
			ver(d)["checks"] = check(map[string]any{"configPaths": []any{".golangci.yml", "mk/lint.mk"}})
		}},
		// the rest of the top level
		{"spec-extra-key", false, func(d map[string]any) { spec(d)["extra"] = 1 }},
		{"top-extra-key", false, func(d map[string]any) { d["extra"] = 1 }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := fxSmokeRepo(t)
			var doc map[string]any
			if err := json.Unmarshal(r.validationDoc(), &doc); err != nil {
				t.Fatal(err)
			}
			c.mut(doc)
			b, err := json.MarshalIndent(doc, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			// The schema verdict, with the N1 workaround (see above).
			var forSchema map[string]any
			if err := json.Unmarshal(b, &forSchema); err != nil {
				t.Fatal(err)
			}
			if v, ok := forSchema["spec"].(map[string]any); ok {
				if vv, ok := v["verification"].(map[string]any); ok {
					if ss, ok := vv["suites"].([]any); ok && len(ss) > 0 {
						ss[0].(map[string]any)["report"] = map[string]any{"path": "out/n1-workaround.json"}
					}
				}
			}
			schemaErr := schema.Validate(forSchema)
			if c.accept && schemaErr != nil {
				t.Fatalf("fixture: the schema rejects the case that is meant to be valid: %v", schemaErr)
			}
			if !c.accept && schemaErr == nil {
				t.Fatalf("fixture: the schema accepts the case that is meant to be invalid")
			}
			if err := os.WriteFile(filepath.Join(r.ws, ".strange-company", "validation.yaml"), append(b, '\n'), 0o644); err != nil {
				t.Fatal(err)
			}
			fxGit(t, r.home, r.ws, "add", ".strange-company/validation.yaml")
			fxGit(t, r.home, r.ws, "commit", "-q", "--allow-empty", "-m", "chore: validation case")
			head := fxGit(t, r.home, r.ws, "rev-parse", "HEAD")
			res := r.run(fxRunOpts{Expected: head})
			if res.TimedOut {
				t.Fatalf("run timed out")
			}
			if c.accept && res.Exit != 0 {
				t.Errorf("run exit %d for a document the schema accepts; stderr %q", res.Exit, fxTrunc(res.Stderr))
			}
			if !c.accept {
				if res.Exit != 2 {
					t.Errorf("run exit %d for a document the schema rejects (%v), want 2; stderr %q", res.Exit, schemaErr, fxTrunc(res.Stderr))
				}
				if fxExists(filepath.Join(r.record, "observed.json")) {
					t.Errorf("run observed a document the schema rejects")
				}
			}
		})
	}
}

// D28 (N3): a red commit holds a file with a NUL byte and a stub marker. The replay export cannot strip the marker
// from it, so run must refuse (exit 2) instead of exporting the marker. The twin has the same marker in a text
// file and is stripped as before.
func TestRun_ReplayRefusesNulFileWithStubMarker_D28N3(t *testing.T) {
	r := fxNewRepo(t, fxConfig{Fixture: "d28_nul", Suites: []fxSuite{fxGoSuite(nil)}})
	blob := fxCatBlob(t, r, r.red, "testdata/blob.bin")
	if !bytes.Contains(blob, []byte{0}) || !bytes.Contains(blob, []byte("SC-STUB"+"(")) {
		t.Fatalf("fixture: testdata/blob.bin lacks the NUL byte or the marker")
	}
	res := r.run(fxRunOpts{Phase: fxPhaseGreen, Protected: fxD28Protected})
	if res.TimedOut {
		t.Fatalf("run timed out")
	}
	if res.Exit != 2 {
		t.Errorf("run exit %d, want 2 (a stub marker in a NUL file survives the export); stderr %q", res.Exit, fxTrunc(res.Stderr))
	}
	if fxExists(filepath.Join(r.record, "observed.json")) {
		t.Errorf("run wrote an observation although its replay export keeps a stub marker")
	}
}

func TestRun_ReplayStripsMarkerFromTextFile_D28N3Twin(t *testing.T) {
	r := fxNewRepo(t, fxConfig{Fixture: "d28_marker", Suites: []fxSuite{fxGoSuite(nil)}})
	res := r.run(fxRunOpts{Phase: fxPhaseGreen, Protected: fxD28Protected})
	if res.Exit != 0 || res.TimedOut {
		t.Fatalf("run exit %d (timed out %v); stderr %q", res.Exit, res.TimedOut, fxTrunc(res.Stderr))
	}
	doc, _ := r.emitDoc(fxPhaseGreen)
	if got := fxReplayStatus(t, doc); got["unit::fx.TestStillRed"] != "failed_assertion" {
		t.Errorf("red_replay = %v, want TestStillRed = failed_assertion", got)
	}
}

// D28 (N4): emit prints what plan 20 section 8.4 says: a header, a scrubbed tail of at most 64 KiB and the framed
// evidence block (section 8.5), in that order, with the block last. The suite prints an early mark, 300 KiB of
// filler and a late mark: the tail ends with the log and does not reach back to its start.
func TestEmit_PrintsHeaderTailAndFramedBlock_D28N4(t *testing.T) {
	r := fxNewRepo(t, fxConfig{Fixture: "d28_tail", Suites: []fxSuite{fxGoSuite(nil)}})
	if res := r.run(fxRunOpts{}); res.Exit != 0 || res.TimedOut {
		t.Fatalf("run exit %d (timed out %v); stderr %q", res.Exit, res.TimedOut, fxTrunc(res.Stderr))
	}
	res := r.emit(r.nonce, r.intent)
	if res.Exit != 0 {
		t.Fatalf("emit exit %d; stderr %q", res.Exit, fxTrunc(res.Stderr))
	}
	if _, err := fxParseBlock(res.Stdout); err != nil {
		t.Fatalf("emit: %v", err)
	}
	lines := strings.Split(strings.TrimSuffix(res.Stdout, "\n"), "\n")
	begin := -1
	for i, l := range lines {
		if strings.HasPrefix(l, "::sc-evidence-begin ") {
			begin = i
			break
		}
	}
	if begin < 0 {
		t.Fatalf("no begin marker")
	}
	if !strings.HasPrefix(lines[len(lines)-1], "::sc-evidence-end ") {
		t.Errorf("the last line is %q, want the end marker (the framed block comes after the header and the tail)", fxTrunc(lines[len(lines)-1]))
	}
	pre := lines[:begin]
	if len(pre) < 2 {
		t.Fatalf("emit printed %d line(s) before the block, want a header line and the tail of the log:\n%s", len(pre), fxTrunc(res.Stdout))
	}
	header, tail := pre[0], strings.Join(pre[1:], "\n")
	if header == "" || len(header) > 1024 || strings.Contains(header, "-MARK") {
		t.Errorf("header line = %q, want one short non-empty line that is not log output", fxTrunc(header))
	}
	if len(tail) > 64*1024 {
		t.Errorf("the tail is %d bytes, want at most 65536", len(tail))
	}
	if !strings.Contains(tail, "LATE-MARK") {
		t.Errorf("the tail does not hold the end of the suite's log (no LATE-MARK)")
	}
	if strings.Contains(tail, "EARLY-MARK") {
		t.Errorf("the tail reaches back to the start of the log (EARLY-MARK): it is not limited to the last 64 KiB")
	}
	for _, secret := range []string{r.nonce} {
		if strings.Contains(strings.Join(pre, "\n"), secret) {
			t.Errorf("the header or tail shows %q", secret)
		}
	}
}

// D28 (N5): the command compiles for a non-linux platform (run then exits 2 with "unsupported platform").
func TestBuild_CompilesForDarwin_D28N5(t *testing.T) {
	out := filepath.Join(t.TempDir(), "sc-verify-darwin")
	cmd := exec.Command("go", "build", "-o", out, ".")
	cmd.Env = append(os.Environ(), "GOOS=darwin", "GOARCH=arm64", "CGO_ENABLED=0")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("GOOS=darwin go build ./cmd/sc-verify failed: %v\n%s", err, fmt.Sprint(fxTrunc(string(b))))
	}
}
