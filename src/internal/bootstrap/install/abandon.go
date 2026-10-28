package install

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"strange.company/sc/internal/bootstrap/engine"
	"strange.company/sc/internal/bootstrap/store"
)

// abandon-identity (plan 50 section 7.1, BOOT-6, 50-AC-18): an installation identity that never reached a confirmed
// compute plan is set aside, so the next run generates a new one over the same buckets.

// abandonedDir holds what the identity <uid> left on this host: journal.json, bundles/, recovery-identity.txt.
func (s *sess) abandonedDir(uid string) string { return filepath.Join(s.cdir(), "abandoned", uid) }

// firstGeneration is the bundle version a new identity starts at and the sha256 (hex) of the bundle it follows: 1 and
// "" on a fresh host, else one more than the newest bundle of an abandoned identity. The recovery bucket keeps the
// abandoned identity's bundle objects (Object Lock), and a bundle version is immutable (plan 50 section 14.3), so the
// new identity must not reuse a version; continuing the numbering also keeps "the highest version is the newest identity".
func (s *sess) firstGeneration() (int, string) {
	prefix := "sc-recovery-" + s.cluster + "-v"
	files, _ := filepath.Glob(filepath.Join(s.cdir(), "abandoned", "*", "bundles", prefix+"*.tar.age"))
	best, bestV := "", 0
	for _, f := range files {
		v, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(filepath.Base(f), prefix), ".tar.age"))
		if err == nil && v > bestV {
			best, bestV = f, v
		}
	}
	if best == "" {
		return 1, ""
	}
	data, err := os.ReadFile(best)
	if err != nil {
		return bestV + 1, ""
	}
	return bestV + 1, strings.TrimPrefix(sha256Digest(data), "sha256:")
}

func journalSeq(b []byte) int {
	var j struct {
		Seq int `json:"journal_seq"`
	}
	_ = json.Unmarshal(b, &j)
	return j.Seq
}

func (s *sess) cmdAbandon(ctx context.Context) int {
	code, ok := s.stateSetup(ctx, "abandon-identity")
	if !ok {
		return code
	}
	if res := s.eng.Acquire(ctx); res.Exit != 0 {
		s.errorf("%s: %s\n", res.Code, res.Message)
		return res.Exit
	}
	defer s.eng.Release(ctx)
	if res := s.eng.LoadJournal(ctx); res.Exit != 0 {
		s.errorf("%s: %s\n", res.Code, res.Message)
		return res.Exit
	}
	refuse := func(exit int, code, format string, a ...any) int {
		s.errorf("%s: %s\n", code, sprintf(format, a...))
		return exit
	}
	uid := s.eng.InstallUID()
	if uid == "" {
		return refuse(2, "ABANDON_NO_IDENTITY", "the journal holds no installation identity to abandon")
	}
	if s.eng.ConfirmedSubject("paid-footprint") != "" || s.eng.ConfirmedSubject("existing-nodes") != "" {
		return refuse(4, "ABANDON_REFUSED_COMPUTE_CONFIRMED",
			"a compute plan was confirmed for installation %s; its resources may exist, so the identity cannot be abandoned (procedures/bootstrap-resume.md)", uid)
	}
	want := "abandon " + s.cluster + " " + uid[:8]
	line, err := s.readLine(ctx, "Type '"+want+"' to abandon the installation identity: ", false)
	if err != nil {
		return s.abandonInputErr(err)
	}
	if strings.TrimSpace(line) != want {
		return refuse(5, "ABANDON_CONFIRMATION_MISMATCH", "the typed confirmation does not match '%s'", want)
	}

	localPath := filepath.Join(s.boot, "journal.json")
	journal, _ := os.ReadFile(localPath)
	if s.lazy.Attached() {
		if o, gerr := s.lazy.Get(ctx, engine.JournalKey(s.cluster)); gerr == nil && journalSeq(o.Data) >= journalSeq(journal) {
			journal = o.Data
		}
		if len(journal) > 0 {
			if _, perr := s.lazy.PutIfAbsent(ctx, s.cluster+"/journal/abandoned-"+uid+".json", journal); perr != nil && !store.Is(perr, store.Exists) {
				return refuse(1, "ABANDON_ARCHIVE_FAILED", "the journal cannot be archived: %v", perr)
			}
		}
		if derr := s.lazy.Delete(ctx, engine.JournalKey(s.cluster), ""); derr != nil && !store.Is(derr, store.NotFound) {
			return refuse(1, "ABANDON_ARCHIVE_FAILED", "the remote journal cannot be removed: %v", derr)
		}
	}
	// NB1: what belongs to the abandoned identity moves aside (never deleted), or the next run would adopt the bundle
	// again and fail its confirmation for ever.
	aside := s.abandonedDir(uid)
	if len(journal) > 0 {
		if werr := writeAtomic(filepath.Join(aside, "journal.json"), journal); werr != nil {
			return refuse(1, "ABANDON_MOVE_FAILED", "%v", werr)
		}
	}
	if rerr := os.Remove(localPath); rerr != nil && !os.IsNotExist(rerr) {
		return refuse(1, "ABANDON_MOVE_FAILED", "%v", rerr)
	}
	if merr := os.MkdirAll(filepath.Join(aside, "bundles"), 0o700); merr != nil {
		return refuse(1, "ABANDON_MOVE_FAILED", "%v", merr)
	}
	bundles, _ := filepath.Glob(filepath.Join(s.cdir(), "bundles", "*"))
	for _, f := range bundles {
		if merr := os.Rename(f, filepath.Join(aside, "bundles", filepath.Base(f))); merr != nil {
			return refuse(1, "ABANDON_MOVE_FAILED", "%v", merr)
		}
	}
	if merr := os.Rename(filepath.Join(s.cdir(), "recovery-identity.txt"), filepath.Join(aside, "recovery-identity.txt")); merr != nil && !os.IsNotExist(merr) {
		return refuse(1, "ABANDON_MOVE_FAILED", "%v", merr)
	}
	s.printf("Installation identity %s abandoned; its journal is archived as %s/journal/abandoned-%s.json and its local files are in %s.\n"+
		"The next run generates a new identity over the same buckets.\n", uid, s.cluster, uid, aside)
	return 0
}

func (s *sess) abandonInputErr(err error) int {
	err = wrapErr(err)
	if ee, ok := err.(*engine.Error); ok {
		s.errorf("%s: %s\n", ee.Code, ee.Message)
		return ee.Exit
	}
	s.errorf("sc-bootstrap: %v\n", err)
	return 1
}
