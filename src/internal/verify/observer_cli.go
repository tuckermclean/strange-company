//go:build linux

package verify

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"strange.company/sc/internal/contracts"
	"strange.company/sc/internal/policy"
)

// Deps is what the sc-verify main package hands to the library: the clock (plan 52 D8), the process arguments and
// environment, and the output streams.
type Deps struct {
	Clock   Clock
	Args    []string // os.Args without the program name
	Environ []string
	Stdout  io.Writer
	Stderr  io.Writer
}

// defaultChildPath is the fixed toolchain PATH of the child environment when --child-path is not given.
const defaultChildPath = "/usr/local/go/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

// Exit codes of sc-verify (plan 22 section 7.1): 0 when an observation was written (run) or framed (emit), 2 when it
// could not observe or the usage is wrong. classify exits 0 after printing a classification.
const (
	exitOK     = 0
	exitCannot = 2
)

type usageError string

func (e usageError) Error() string { return string(e) }

// Main is sc-verify. It never panics out: an internal error is a "could not observe" (exit 2), never evidence.
func Main(d Deps) (code int) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(d.Stderr, "sc-verify: internal error: %v\n", r)
			code = exitCannot
		}
	}()
	if len(d.Args) == 0 {
		fmt.Fprintln(d.Stderr, "usage: sc-verify run|emit|classify ... (see plan 22 section 7.1)")
		return exitCannot
	}
	var err error
	switch d.Args[0] {
	case "run":
		err = cmdRun(d, d.Args[1:])
	case "emit":
		err = cmdEmit(d, d.Args[1:])
	case "classify":
		err = cmdClassify(d, d.Args[1:])
	default:
		err = usageError(fmt.Sprintf("unknown command %q", d.Args[0]))
	}
	if err != nil {
		fmt.Fprintf(d.Stderr, "sc-verify %s: %v\n", d.Args[0], err)
		return exitCannot
	}
	return exitOK
}

func newFlags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

func envMap(environ []string) map[string]string {
	m := map[string]string{}
	for _, e := range environ {
		k, v, _ := strings.Cut(e, "=")
		m[k] = v
	}
	return m
}

func cmdRun(d Deps, args []string) error {
	fs := newFlags("run")
	validation := fs.String("validation", "", "")
	record := fs.String("record", "", "")
	policyPath := fs.String("policy", "", "")
	childPath := fs.String("child-path", defaultChildPath, "")
	if err := fs.Parse(args); err != nil {
		return usageError(err.Error())
	}
	if fs.NArg() != 0 || *validation == "" || *record == "" || *policyPath == "" {
		return usageError("usage: run --validation <file> --record <dir> --policy <verification.yaml> [--child-path <PATH>]")
	}
	env := envMap(d.Environ)
	// The nonce and the intent id belong to emit alone (blind TDD-B1). If the launching environment carries them,
	// run replaces itself by a copy of itself without them, so that /proc/<pid>/environ of this process shows
	// neither; if that is impossible it refuses to run.
	if hasKey(env, "SC_EVIDENCE_NONCE") || hasKey(env, "SC_INTENT_ID") {
		var kept []string
		for _, e := range d.Environ {
			if k, _, _ := strings.Cut(e, "="); k != "SC_EVIDENCE_NONCE" && k != "SC_INTENT_ID" {
				kept = append(kept, e)
			}
		}
		argv := append([]string{os.Args[0]}, d.Args...)
		if err := syscall.Exec("/proc/self/exe", argv, kept); err != nil {
			return fmt.Errorf("the launching environment carries SC_EVIDENCE_NONCE or SC_INTENT_ID and run cannot re-execute without them: %v", err)
		}
	}
	id, err := obsIdentityFrom(env)
	if err != nil {
		return err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	if real, err := filepath.EvalSymlinks(cwd); err == nil {
		cwd = real
	}
	o := &observer{d: d, clock: d.Clock, cwd: cwd, git: GitRepo{Dir: cwd}, id: id, childPath: *childPath}
	if !filepath.IsAbs(o.childPath) && !strings.HasPrefix(o.childPath, "/") {
		return usageError("--child-path must be absolute directories")
	}
	return o.run(*validation, *record, *policyPath, env)
}

func hasKey(m map[string]string, k string) bool { _, ok := m[k]; return ok }

func readYAMLFile(path string) (map[string]any, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	v, err := contracts.ParseYAML(b)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s: not a mapping", path)
	}
	return m, nil
}

func (o *observer) gitOut(args ...string) (string, error) {
	r, err := runGit(o.git, false, args...)
	return strings.TrimSpace(string(r.out)), err
}

func (o *observer) run(validation, record, policyPath string, env map[string]string) error {
	if err := setSubreaper(); err != nil {
		return err
	}
	head, err := o.gitOut("rev-parse", "HEAD")
	if err != nil {
		return err
	}
	if head != o.id.Expected {
		return fmt.Errorf("HEAD is %s, SC_EXPECTED_SHA is %s", head, o.id.Expected)
	}
	if _, err := o.gitOut("rev-parse", "--verify", o.id.Base+"^{commit}"); err != nil {
		return fmt.Errorf("SC_BASE_SHA is not a commit of this repository")
	}
	if o.id.Phase == "green" {
		if _, err := o.gitOut("rev-parse", "--verify", o.id.Red+"^{commit}"); err != nil {
			return fmt.Errorf("SC_RED_SHA is not a commit of this repository")
		}
	}
	pol, err := readYAMLFile(policyPath)
	if err != nil {
		return err
	}
	if o.adapters, err = LoadAdapters(pol); err != nil {
		return err
	}
	// validation.yaml is read from the checked-out commit's Git object, not from the work tree (plan 22 section 13).
	rel := filepath.ToSlash(filepath.Clean(validation))
	if filepath.IsAbs(validation) || rel == ".." || strings.HasPrefix(rel, "../") {
		return usageError("--validation must be a path inside the repository")
	}
	if o.valBlob, err = o.gitOut("rev-parse", "--verify", "HEAD:"+rel); err != nil {
		return fmt.Errorf("cannot find %s at HEAD", rel)
	}
	r, err := runGit(o.git, false, "cat-file", "blob", o.valBlob)
	if err != nil {
		return err
	}
	if o.cfg, err = parseValidation(r.out, o.adapters); err != nil {
		return err
	}
	if len(o.suitesFor(o.id.Phase)) == 0 {
		return fmt.Errorf("validation.yaml has no suite for phase %s", o.id.Phase)
	}
	recAbs, err := filepath.Abs(record)
	if err != nil {
		return err
	}
	if fi, err := os.Lstat(recAbs); err != nil || !fi.IsDir() {
		return fmt.Errorf("--record %s is not an existing directory", record)
	}
	if real, err := filepath.EvalSymlinks(recAbs); err != nil || within(o.cwd, real) {
		return fmt.Errorf("--record must be a directory outside the workspace")
	}
	ver, err := o.verifier()
	if err != nil {
		return err
	}
	if err := o.makeWorkRoot([]string{os.TempDir(), "/tmp", "/var/tmp"}); err != nil {
		return err
	}
	defer forceRemove(o.work)
	ctx := context.Background()
	var doc []byte
	if o.id.Phase == "red" {
		var base *treeRun
		if o.id.Baseline == "run" {
			tree, cleanup, err := o.addBase()
			if err != nil {
				return err
			}
			tr, err := o.runTree(ctx, tree, "base", o.id.Base)
			cleanup()
			if err != nil {
				return err
			}
			base = &tr
		}
		hr, err := o.runTree(ctx, o.cwd, "head", head)
		if err != nil {
			return err
		}
		doc, err = buildRedDoc(o.redObservation(head, base, hr, ver))
		if err != nil {
			return err
		}
	} else {
		hr, err := o.runTree(ctx, o.cwd, "head", head)
		if err != nil {
			return err
		}
		replay, err := o.redReplay(ctx)
		if err != nil {
			return err
		}
		checks, err := o.runChecks(ctx)
		if err != nil {
			return err
		}
		doc, err = buildGreenDoc(o.greenObservation(head, hr, checks, replay, ver))
		if err != nil {
			return err
		}
	}
	// Every descendant of every suite is dead and reaped here; only now is anything written to the record.
	files := append(append([]recFile(nil), o.sealed...), o.logs...)
	if err := writeRecord(recAbs, o.id.Phase, head, doc, files); err != nil {
		return err
	}
	fmt.Fprintf(o.d.Stdout, "sc-verify run: phase=%s head=%s record=%s\n", o.id.Phase, head, recAbs)
	return nil
}

func cmdEmit(d Deps, args []string) error {
	fs := newFlags("emit")
	record := fs.String("record", "", "")
	emit := fs.String("emit", "log", "")
	if err := fs.Parse(args); err != nil {
		return usageError(err.Error())
	}
	if fs.NArg() != 0 || *record == "" || *emit != "log" {
		return usageError("usage: emit --record <dir> --emit log")
	}
	env := envMap(d.Environ)
	nonce, intent := env["SC_EVIDENCE_NONCE"], env["SC_INTENT_ID"]
	if !emitNonceRE.MatchString(nonce) {
		return errors.New("SC_EVIDENCE_NONCE is missing or not 32 lowercase hex characters")
	}
	if !emitIntentRE.MatchString(intent) {
		return errors.New("SC_INTENT_ID is missing or malformed")
	}
	meta, doc, err := verifyRecord(*record)
	if err != nil {
		return err
	}
	block, err := frameEvidence(meta, doc, nonce, intent)
	if err != nil {
		return err
	}
	_, err = io.WriteString(d.Stdout, block)
	return err
}

// classifyFacts is the --facts document of `classify`: the inputs of ClassifyRed or ClassifyGreen that are not in
// the observed document.
type classifyFacts struct {
	Scope                Scope               `json:"scope"`
	Identity             Identity            `json:"identity"`
	Declared             map[string][]string `json:"declared"`
	Criteria             []Criterion         `json:"criteria"`
	SuiteLanguages       map[string]string   `json:"suite_languages"`
	HeadScope            map[string][]string `json:"head_scope"`
	Effective            policy.Effective    `json:"effective"`
	ComputedAt           string              `json:"computed_at"`
	PolicyRevision       string              `json:"policy_revision"`
	Integrity            Integrity           `json:"integrity"`
	Manifest             Manifest            `json:"manifest"`
	Baseline             Observation         `json:"baseline"`
	Isolation            IsolationFacts      `json:"isolation"`
	ProtectedTestIDs     []string            `json:"protected_test_ids"`
	RequiredChecks       []string            `json:"required_checks"`
	ApprovedRemovedTests []string            `json:"approved_removed_tests"`
}

func decodeStrict(b []byte, dst any) error {
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	if dec.More() {
		return errors.New("trailing data")
	}
	return nil
}

func cmdClassify(d Deps, args []string) error {
	fs := newFlags("classify")
	policyPath := fs.String("policy", "", "")
	docPath := fs.String("doc", "", "")
	factsPath := fs.String("facts", "", "")
	if err := fs.Parse(args); err != nil {
		return usageError(err.Error())
	}
	if fs.NArg() != 0 || *policyPath == "" || *docPath == "" || *factsPath == "" {
		return usageError("usage: classify --policy <verification.yaml> --doc <result.json> --facts <facts.json>")
	}
	pol, err := readYAMLFile(*policyPath)
	if err != nil {
		return err
	}
	docB, err := os.ReadFile(*docPath)
	if err != nil {
		return err
	}
	factsB, err := os.ReadFile(*factsPath)
	if err != nil {
		return err
	}
	var facts classifyFacts
	if err := decodeStrict(factsB, &facts); err != nil {
		return fmt.Errorf("%s: %w", *factsPath, err)
	}
	var env resultEnvelope
	if err := json.Unmarshal(docB, &env); err != nil {
		return fmt.Errorf("%s: %w", *docPath, err)
	}
	platform := policy.Platform{Verification: pol}
	var result any
	switch env.Document {
	case "red-result":
		var rd redDoc
		if err := decodeStrict(docB, &rd); err != nil {
			return fmt.Errorf("%s: %w", *docPath, err)
		}
		result, err = ClassifyRed(RedInput{
			Observed: rd.Observed, Scope: facts.Scope, Identity: facts.Identity, Declared: facts.Declared, Criteria: facts.Criteria,
			SuiteLanguages: facts.SuiteLanguages, HeadScope: facts.HeadScope, Platform: platform, Effective: facts.Effective,
			ComputedAt: facts.ComputedAt, PolicyRevision: facts.PolicyRevision,
		})
	case "green-result":
		var gd greenDoc
		if err := decodeStrict(docB, &gd); err != nil {
			return fmt.Errorf("%s: %w", *docPath, err)
		}
		result, err = ClassifyGreen(GreenInput{
			Observed: gd.Observed, Integrity: facts.Integrity, Manifest: facts.Manifest, Baseline: facts.Baseline, Identity: facts.Identity,
			Isolation: facts.Isolation, ProtectedTestIDs: facts.ProtectedTestIDs, RequiredChecks: facts.RequiredChecks,
			ApprovedRemovedTests: facts.ApprovedRemovedTests, Platform: platform, Effective: facts.Effective,
			ComputedAt: facts.ComputedAt, PolicyRevision: facts.PolicyRevision,
		})
	default:
		return fmt.Errorf("%s is neither a red-result nor a green-result document", *docPath)
	}
	if err != nil {
		return err
	}
	out, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(d.Stdout, "%s\n", out)
	return err
}
