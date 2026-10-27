package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"strange.company/sc/internal/bootstrap/store"
)

const (
	journalVersion = "v1alpha1"
	zeroUID        = "00000000-0000-4000-8000-000000000000"
	zeroDigest     = "sha256:0000000000000000000000000000000000000000000000000000000000000000"
	zeroSHA1       = "0000000000000000000000000000000000000000"
	maxRuns        = 100
	maxErrMessage  = 2000
)

// The journal as the contract bootstrap-journal.schema.yaml defines it. Parts the engine does not interpret
// (recovery_bundle, facts) stay raw so another tool's entries survive a rewrite.

type outputRefJ struct {
	Kind   string `json:"kind"`
	Ref    string `json:"ref"`
	Sha256 string `json:"sha256,omitempty"`
}

type errJ struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
	Procedure string `json:"procedure,omitempty"`
}

// unitJ is a phase (phase true) or a step. A phase always carries every key; a step omits the optional ones.
type unitJ struct {
	Name        string       `json:"name"`
	Status      string       `json:"status"`
	Attempt     int          `json:"attempt"`
	InputDigest *string      `json:"input_digest"`
	StartedAt   *string      `json:"started_at"`
	FinishedAt  *string      `json:"finished_at"`
	LastRunID   *string      `json:"last_run_id"`
	OutputRefs  []outputRefJ `json:"output_refs"`
	Steps       []*unitJ     `json:"steps"`
	Error       *errJ        `json:"error"`

	phase bool
}

// MarshalJSON writes the phase or step shape of the contract.
func (u *unitJ) MarshalJSON() ([]byte, error) {
	refs := u.OutputRefs
	if refs == nil {
		refs = []outputRefJ{}
	}
	if u.phase {
		steps := u.Steps
		if steps == nil {
			steps = []*unitJ{}
		}
		return json.Marshal(struct {
			Name        string       `json:"name"`
			Status      string       `json:"status"`
			Attempt     int          `json:"attempt"`
			InputDigest *string      `json:"input_digest"`
			StartedAt   *string      `json:"started_at"`
			FinishedAt  *string      `json:"finished_at"`
			LastRunID   *string      `json:"last_run_id"`
			OutputRefs  []outputRefJ `json:"output_refs"`
			Steps       []*unitJ     `json:"steps"`
			Error       *errJ        `json:"error"`
		}{u.Name, u.Status, u.Attempt, u.InputDigest, u.StartedAt, u.FinishedAt, u.LastRunID, refs, steps, u.Error})
	}
	return json.Marshal(struct {
		Name        string       `json:"name"`
		Status      string       `json:"status"`
		Attempt     int          `json:"attempt"`
		InputDigest *string      `json:"input_digest,omitempty"`
		StartedAt   *string      `json:"started_at,omitempty"`
		FinishedAt  *string      `json:"finished_at,omitempty"`
		OutputRefs  []outputRefJ `json:"output_refs"`
		Error       *errJ        `json:"error"`
	}{u.Name, u.Status, u.Attempt, u.InputDigest, u.StartedAt, u.FinishedAt, refs, u.Error})
}

type pipelineJ struct {
	Pipeline      string   `json:"pipeline"`
	Generation    int      `json:"generation"`
	TargetProfile string   `json:"target_profile,omitempty"`
	Status        string   `json:"status"`
	StartedAt     *string  `json:"started_at"`
	FinishedAt    *string  `json:"finished_at"`
	Phases        []*unitJ `json:"phases"`
}

type operatorJ struct {
	User string `json:"user"`
	Host string `json:"host"`
}

type toolboxJ struct {
	ImageDigest string `json:"image_digest"`
	Source      string `json:"source"`
}

type runJ struct {
	RunID          string    `json:"run_id"`
	Command        string    `json:"command"`
	StartedAt      string    `json:"started_at"`
	FinishedAt     *string   `json:"finished_at"`
	Outcome        string    `json:"outcome"`
	ExitCode       *int      `json:"exit_code"`
	Operator       operatorJ `json:"operator"`
	SCBootstrapVer string    `json:"sc_bootstrap_version"`
	Toolbox        toolboxJ  `json:"toolbox"`
	GitHead        string    `json:"git_head"`
}

type confirmationJ struct {
	Kind          string `json:"kind"`
	SubjectDigest string `json:"subject_digest"`
	ConfirmedAt   string `json:"confirmed_at"`
	RunID         string `json:"run_id"`
	Method        string `json:"method"`
}

type installationJ struct {
	State              string  `json:"state"`
	Provider           string  `json:"provider"`
	Profile            string  `json:"profile"`
	ConfigDigest       string  `json:"config_digest"`
	TransferredAt      *string `json:"transferred_at"`
	ComputeDestroyedAt *string `json:"compute_destroyed_at"`
}

type journalDoc struct {
	SchemaVersion  string          `json:"schema_version"`
	RecordType     string          `json:"record_type"`
	JournalSeq     int             `json:"journal_seq"`
	Cluster        string          `json:"cluster"`
	InstallUID     string          `json:"install_uid"`
	CreatedAt      string          `json:"created_at"`
	UpdatedAt      string          `json:"updated_at"`
	Installation   installationJ   `json:"installation"`
	RecoveryBundle json.RawMessage `json:"recovery_bundle"`
	Pipelines      []*pipelineJ    `json:"pipelines"`
	Runs           []*runJ         `json:"runs"`
	Facts          json.RawMessage `json:"facts,omitempty"`
	Confirmations  []confirmationJ `json:"confirmations"`
}

// normalize makes the nil slices empty and marks the phase records.
func (j *journalDoc) normalize() {
	if j.Pipelines == nil {
		j.Pipelines = []*pipelineJ{}
	}
	if j.Runs == nil {
		j.Runs = []*runJ{}
	}
	if j.Confirmations == nil {
		j.Confirmations = []confirmationJ{}
	}
	if len(j.RecoveryBundle) == 0 || string(j.RecoveryBundle) == "null" {
		j.RecoveryBundle = json.RawMessage(`{"status":"absent","generation":0}`)
	}
	for _, p := range j.Pipelines {
		if p.Phases == nil {
			p.Phases = []*unitJ{}
		}
		for _, ph := range p.Phases {
			ph.phase = true
			if ph.Steps == nil {
				ph.Steps = []*unitJ{}
			}
		}
	}
}

func parseJournal(b []byte) (*journalDoc, error) {
	var j journalDoc
	if err := json.Unmarshal(b, &j); err != nil {
		return nil, fmt.Errorf("not a journal: %w", err)
	}
	if j.RecordType != "bootstrap_journal" {
		return nil, fmt.Errorf("record_type %q is not bootstrap_journal", j.RecordType)
	}
	j.normalize()
	return &j, nil
}

// semanticEqual compares two journal copies by value, not by formatting.
func semanticEqual(a, b []byte) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return bytes.Equal(a, b)
	}
	return reflect.DeepEqual(x, y)
}

func (e *Engine) journalKey() string   { return e.cfg.Cluster + "/journal/journal.json" }
func (e *Engine) localJournal() string { return filepath.Join(e.cfg.Dir, "journal.json") }

func (e *Engine) now() time.Time { return e.clk.Now().UTC() }
func (e *Engine) stamp() string  { return e.now().Format(time.RFC3339) }

func ptr(s string) *string { return &s }

// newJournal is the journal of an installation that has none yet.
func (e *Engine) newJournal() *journalDoc {
	j := &journalDoc{
		SchemaVersion: journalVersion, RecordType: "bootstrap_journal", Cluster: e.cfg.Cluster, InstallUID: e.uid(),
		CreatedAt: e.stamp(), UpdatedAt: e.stamp(),
		Installation: installationJ{
			State: "new", Provider: e.cfg.Provider, Profile: e.cfg.Profile, ConfigDigest: orDefault(e.cfg.ConfigDigest, zeroDigest),
		},
	}
	j.normalize()
	return j
}

func orDefault(v, d string) string {
	if v == "" {
		return d
	}
	return v
}

func (e *Engine) uid() string { return orDefault(e.cfg.InstallUID, zeroUID) }

// loadResult is the outcome of reading both journal copies.
type loadResult struct {
	doc        *journalDoc
	fresh      bool
	conflict   bool
	local      []byte
	remote     []byte
	remoteETag string
}

// load reads the local and the S3 copy and applies the plan 50 section 17.3 rules: the higher journal_seq wins, equal
// seq with different content is a conflict. A non-nil Result is the failure to return.
func (e *Engine) load(ctx context.Context) (*loadResult, *Result) {
	lr := &loadResult{}
	if b, err := os.ReadFile(e.localJournal()); err == nil {
		lr.local = b
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, &Result{Exit: 7, Code: "JOURNAL_INVALID", Message: "the local journal cannot be read: " + err.Error()}
	}
	if e.cfg.Store != nil {
		obj, err := e.cfg.Store.Get(ctx, e.journalKey())
		switch {
		case err == nil:
			lr.remote, lr.remoteETag = obj.Data, obj.ETag
		case store.Is(err, store.NotFound):
		default:
			return nil, &Result{Exit: 7, Code: "JOURNAL_REMOTE_READ_FAILED", Message: "the S3 journal cannot be read: " + err.Error()}
		}
	}
	var ld, rd *journalDoc
	var err error
	if lr.local != nil {
		if ld, err = parseJournal(lr.local); err != nil {
			return nil, &Result{Exit: 7, Code: "JOURNAL_INVALID", Message: "the local journal is unusable: " + err.Error()}
		}
	}
	if lr.remote != nil {
		if rd, err = parseJournal(lr.remote); err != nil {
			return nil, &Result{Exit: 7, Code: "JOURNAL_INVALID", Message: "the S3 journal is unusable: " + err.Error()}
		}
	}
	for _, d := range []*journalDoc{ld, rd} {
		if d != nil && d.SchemaVersion != journalVersion {
			return nil, &Result{Exit: 7, Code: "JOURNAL_FROM_NEWER_VERSION",
				Message: fmt.Sprintf("the journal has schema_version %q; this sc-bootstrap reads %s", d.SchemaVersion, journalVersion)}
		}
	}
	switch {
	case ld == nil && rd == nil:
		lr.doc, lr.fresh = e.newJournal(), true
	case rd == nil:
		lr.doc = ld
	case ld == nil:
		lr.doc = rd
	case rd.JournalSeq > ld.JournalSeq:
		lr.doc = rd
	case ld.JournalSeq > rd.JournalSeq:
		lr.doc = ld
	case semanticEqual(lr.local, lr.remote):
		lr.doc = ld
	default:
		lr.conflict = true
	}
	return lr, nil
}

// conflictResult writes both copies below runs/<run-id>/journal-conflict/ and returns exit 7.
func (e *Engine) conflictResult(lr *loadResult) Result {
	dir := filepath.Join(e.runDir(), "journal-conflict")
	msg := fmt.Sprintf("the local and the S3 journal both have journal_seq %d but differ", seqOf(lr.local))
	if err := os.MkdirAll(dir, 0o700); err == nil {
		werr1 := os.WriteFile(filepath.Join(dir, "local.json"), lr.local, 0o600)
		werr2 := os.WriteFile(filepath.Join(dir, "remote.json"), lr.remote, 0o600)
		if werr1 != nil || werr2 != nil {
			msg += " (and the copies could not be written)"
		} else {
			msg += "; both copies are in " + dir
		}
	}
	return Result{Exit: 7, Code: "JOURNAL_CONFLICT", Message: e.red.String(msg + " (procedures/bootstrap-resume.md#journal-conflict)")}
}

func seqOf(b []byte) int {
	var s struct {
		Seq int `json:"journal_seq"`
	}
	_ = json.Unmarshal(b, &s)
	return s.Seq
}

func (e *Engine) runDir() string { return filepath.Join(e.cfg.Dir, "runs", e.runID) }

// flushPending moves the recorded confirmations into the journal.
func (e *Engine) flushPending() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.jr != nil && len(e.pending) > 0 {
		e.jr.Confirmations = append(e.jr.Confirmations, e.pending...)
		e.pending = nil
	}
}

// save writes the journal: local atomic write first, then the S3 PUT with retries (plan 50 section 17.3).
func (e *Engine) save(ctx context.Context) *Result {
	ctx = context.WithoutCancel(ctx)
	e.flushPending()
	j := e.jr
	j.JournalSeq++
	j.UpdatedAt = e.stamp()
	b, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		return &Result{Exit: 1, Code: "JOURNAL_ENCODE_FAILED", Message: err.Error()}
	}
	b = append(e.red.Bytes(b), '\n')
	if err := writeFileAtomic(e.localJournal(), b); err != nil {
		return &Result{Exit: 1, Code: "JOURNAL_LOCAL_WRITE_FAILED", Message: e.red.String(err.Error())}
	}
	if e.cfg.Store == nil {
		return nil
	}
	var last error
	for try := 1; try <= 5; try++ {
		ifm := ""
		if e.ifMatch {
			ifm = e.remoteETag
		}
		etag, perr := e.cfg.Store.Put(ctx, e.journalKey(), b, ifm)
		if perr == nil {
			e.remoteETag = etag
			return nil
		}
		last = perr
		if store.Is(perr, store.Unsupported) && ifm != "" {
			e.ifMatch = false
			try--
			continue
		}
		if store.Is(perr, store.PreconditionFailed) {
			return &Result{Exit: 7, Code: "JOURNAL_CONFLICT", Message: "the S3 journal changed during this run (another writer)"}
		}
		if try < 5 {
			_ = e.clk.Sleep(ctx, time.Duration(1<<(try-1))*time.Second)
		}
	}
	return &Result{Exit: 7, Code: "JOURNAL_REMOTE_WRITE_FAILED",
		Message: e.red.String(fmt.Sprintf("the S3 journal write failed after 5 attempts: %v (procedures/object-storage-outage.md)", last))}
}

// writeFileAtomic writes tmp, fsyncs, renames and syncs the directory.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-")
	if err != nil {
		return err
	}
	name := f.Name()
	cleanup := func() { _ = os.Remove(name) }
	if _, err := f.Write(data); err != nil {
		f.Close()
		cleanup()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		cleanup()
		return err
	}
	if err := f.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Chmod(name, 0o600); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(name, path); err != nil {
		cleanup()
		return err
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		d.Close()
	}
	return nil
}

// truncateMessage keeps an error message within the contract's 2000 characters.
func truncateMessage(s string) string {
	r := []rune(s)
	if len(r) <= maxErrMessage {
		return s
	}
	return strings.TrimSpace(string(r[:maxErrMessage-1])) + "…"
}
