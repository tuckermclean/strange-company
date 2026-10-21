//go:build linux

package verify

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
)

// The record directory (plan 22 section 8, plan 20 section 8.4): out/<suite>-<tree>.log, sealed/<report copies>,
// observed.json and record.json, which lists the sha256 of every other file. run writes it after the last
// descendant of the last suite is dead; emit verifies it before framing anything.

const (
	recordFormat     = "sc-verify-record/v1"
	recordName       = "record.json"
	observedName     = "observed.json"
	maxRecordMeta    = 1 << 20
	resultSchemaVers = "v1alpha1"
)

var (
	emitNonceRE  = regexp.MustCompile(`^[0-9a-f]{32}$`)
	emitIntentRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,63}$`)
)

type recordMeta struct {
	Format  string            `json:"format"`
	Phase   string            `json:"phase"`
	HeadSha string            `json:"head_sha"`
	Files   map[string]string `json:"files"`
}

type resultEnvelope struct {
	SchemaVersion string          `json:"schema_version"`
	Document      string          `json:"document"`
	Stage         string          `json:"stage"`
	Observed      json.RawMessage `json:"observed"`
}

type redDoc struct {
	SchemaVersion string      `json:"schema_version"`
	Document      string      `json:"document"`
	Stage         string      `json:"stage"`
	Observed      Observation `json:"observed"`
}

type greenDoc struct {
	SchemaVersion string           `json:"schema_version"`
	Document      string           `json:"document"`
	Stage         string           `json:"stage"`
	Observed      GreenObservation `json:"observed"`
}

// shrinkRuns drops the output tails, the first thing to go when the document is too large.
func shrinkRuns(runs []RunSummary) {
	for i := range runs {
		runs[i].OutputTail = ""
	}
}

// fitDoc marshals a result document and, when it exceeds the 256 KiB frame limit, drops the output tails and then
// cuts the (priority ordered) test list, marking the observation truncated.
func fitDoc(marshal func() ([]byte, error), dropTails func(), cut func() bool) ([]byte, error) {
	b, err := marshal()
	if err != nil {
		return nil, err
	}
	if len(b) <= obsMaxDocBytes {
		return b, nil
	}
	dropTails()
	for {
		if b, err = marshal(); err != nil {
			return nil, err
		}
		if len(b) <= obsMaxDocBytes {
			return b, nil
		}
		if !cut() {
			return nil, errors.New("the observation is larger than 262144 bytes")
		}
	}
}

func buildRedDoc(obs Observation) ([]byte, error) {
	doc := redDoc{SchemaVersion: resultSchemaVers, Document: "red-result", Stage: "observed", Observed: obs}
	return fitDoc(
		func() ([]byte, error) { return json.Marshal(&doc) },
		func() { shrinkRuns(doc.Observed.Runs) },
		func() bool {
			if len(doc.Observed.Tests) == 0 {
				return false
			}
			doc.Observed.Tests = doc.Observed.Tests[:len(doc.Observed.Tests)*3/4]
			doc.Observed.TestsTruncated = true
			return true
		})
}

func buildGreenDoc(obs GreenObservation) ([]byte, error) {
	doc := greenDoc{SchemaVersion: resultSchemaVers, Document: "green-result", Stage: "observed", Observed: obs}
	return fitDoc(
		func() ([]byte, error) { return json.Marshal(&doc) },
		func() {
			shrinkRuns(doc.Observed.Runs)
			for i := range doc.Observed.Checks {
				doc.Observed.Checks[i].OutputTail = ""
			}
		},
		func() bool {
			if len(doc.Observed.Tests) == 0 {
				return false
			}
			doc.Observed.Tests = doc.Observed.Tests[:len(doc.Observed.Tests)*3/4]
			doc.Observed.TestsTruncated = true
			return true
		})
}

// writeRecord replaces the record's contents. Whatever the suites left at these names (including symlinks they
// planted) is removed first without being followed; every file is created exclusively, without following links,
// and read-only.
func writeRecord(record string, phase, head string, doc []byte, files []recFile) error {
	fi, err := os.Lstat(record)
	if err != nil || !fi.IsDir() {
		return fmt.Errorf("record directory %q is not a directory", record)
	}
	for _, n := range []string{"sealed", "out", observedName, recordName} {
		if err := forceRemove(filepath.Join(record, n)); err != nil {
			return err
		}
	}
	for _, n := range []string{"sealed", "out"} {
		if err := os.Mkdir(filepath.Join(record, n), 0o755); err != nil {
			return err
		}
	}
	meta := recordMeta{Format: recordFormat, Phase: phase, HeadSha: head, Files: map[string]string{}}
	put := func(name string, data []byte) error {
		f, err := os.OpenFile(filepath.Join(record, filepath.FromSlash(name)), os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o444)
		if err != nil {
			return err
		}
		if _, err := f.Write(data); err != nil {
			f.Close()
			return err
		}
		return f.Close()
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	for _, f := range files {
		if !strings.HasPrefix(f.Name, "sealed/") && !strings.HasPrefix(f.Name, "out/") || strings.Contains(f.Name[strings.Index(f.Name, "/")+1:], "/") {
			return fmt.Errorf("internal error: record file name %q", f.Name)
		}
		if _, dup := meta.Files[f.Name]; dup {
			return fmt.Errorf("internal error: duplicate record file %q", f.Name)
		}
		if err := put(f.Name, f.Data); err != nil {
			return err
		}
		meta.Files[f.Name] = sha256Hex(f.Data)
	}
	if err := put(observedName, doc); err != nil {
		return err
	}
	meta.Files[observedName] = sha256Hex(doc)
	mb, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	return put(recordName, append(mb, '\n'))
}

// readRecordFile reads a regular file of the record (never a symlink).
func readRecordFile(record, name string, limit int64) ([]byte, error) {
	p := filepath.Join(record, filepath.FromSlash(name))
	f, err := os.OpenFile(p, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("record file %s: %w", name, err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("record file %s is not a regular file", name)
	}
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(b)) > limit {
		return nil, fmt.Errorf("record file %s cannot be read or is too large", name)
	}
	return b, nil
}

// verifyRecord checks every recorded sha256 and that the record holds nothing else, and returns the observation
// document.
func verifyRecord(record string) (recordMeta, []byte, error) {
	var meta recordMeta
	mb, err := readRecordFile(record, recordName, maxRecordMeta)
	if err != nil {
		return meta, nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(mb))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&meta); err != nil || meta.Format != recordFormat || meta.Files == nil {
		return meta, nil, errors.New("record.json is malformed")
	}
	var doc []byte
	for name, want := range meta.Files {
		data, err := readRecordFile(record, name, obsMaxReportSize+maxCapture)
		if err != nil {
			return meta, nil, err
		}
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != want {
			return meta, nil, fmt.Errorf("record file %s changed since run", name)
		}
		if name == observedName {
			doc = data
		}
	}
	if doc == nil {
		return meta, nil, errors.New("record has no observed.json")
	}
	for _, dir := range []string{"sealed", "out"} {
		ents, err := os.ReadDir(filepath.Join(record, dir))
		if err != nil {
			return meta, nil, fmt.Errorf("record directory %s: %w", dir, err)
		}
		for _, e := range ents {
			if _, ok := meta.Files[dir+"/"+e.Name()]; !ok {
				return meta, nil, fmt.Errorf("record holds an unrecorded file %s/%s", dir, e.Name())
			}
		}
	}
	return meta, doc, nil
}

// frameEvidence builds the framed evidence block of plan 20 section 8.5 around the observation, whose intent id
// it sets. The nonce appears in the begin marker only.
func frameEvidence(meta recordMeta, doc []byte, nonce, intent string) (string, error) {
	var env resultEnvelope
	if err := json.Unmarshal(doc, &env); err != nil {
		return "", fmt.Errorf("observed.json: %w", err)
	}
	if env.SchemaVersion != resultSchemaVers || env.Stage != "observed" {
		return "", errors.New("observed.json is not an observed result document")
	}
	var out []byte
	var phase, head string
	decode := func(dst any) error {
		d := json.NewDecoder(bytes.NewReader(doc))
		d.DisallowUnknownFields()
		return d.Decode(dst)
	}
	switch env.Document {
	case "red-result":
		var rd redDoc
		if err := decode(&rd); err != nil {
			return "", fmt.Errorf("observed.json: %w", err)
		}
		rd.Observed.IntentID = intent
		phase, head = rd.Observed.Phase, rd.Observed.HeadSha
		out, _ = json.Marshal(&rd)
	case "green-result":
		var gd greenDoc
		if err := decode(&gd); err != nil {
			return "", fmt.Errorf("observed.json: %w", err)
		}
		gd.Observed.IntentID = intent
		phase, head = gd.Observed.Phase, gd.Observed.HeadSha
		out, _ = json.Marshal(&gd)
	default:
		return "", errors.New("observed.json is neither a red-result nor a green-result")
	}
	if phase != meta.Phase || head != meta.HeadSha || !objectIDRE.MatchString(head) || (phase != "red" && phase != "green") {
		return "", errors.New("observed.json disagrees with record.json")
	}
	if len(out) > obsMaxDocBytes {
		return "", errors.New("the observation is larger than 262144 bytes")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "::sc-evidence-begin v1 intent=%s nonce=%s phase=%s sha=%s bytes=%d sha256=%s::\n", intent, nonce, phase, head, len(out), sha256Hex(out))
	enc := base64.StdEncoding.EncodeToString(out)
	for len(enc) > 0 {
		n := 76
		if len(enc) < n {
			n = len(enc)
		}
		b.WriteString(enc[:n])
		b.WriteByte('\n')
		enc = enc[n:]
	}
	fmt.Fprintf(&b, "::sc-evidence-end v1 intent=%s::\n", intent)
	return b.String(), nil
}
