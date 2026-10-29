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
		if _, serr := os.Stat(filepath.Join(s.boot, "journal.json")); os.IsNotExist(serr) {
			if !s.lazy.Attached() {
				// without the store LoadJournal cannot show that no remote journal exists, so nothing is moved
				return refuse(7, "ABANDON_REMOTE_UNREACHABLE", "the object store cannot be reached, so a remote journal cannot be ruled out; nothing was changed; retry when it is reachable (procedures/object-storage-outage.md)")
			}
			if moved, code := s.moveOrphans(); moved || code != 0 {
				return code
			}
		}
		return refuse(2, "ABANDON_NO_IDENTITY", "the journal holds no installation identity to abandon")
	}
	if len(uid) < 8 {
		return refuse(2, "ABANDON_INVALID_IDENTITY", "the journal's install_uid %q is not an installation identity (fewer than 8 characters)", uid)
	}
	if s.eng.ConfirmedSubject("paid-footprint") != "" || s.eng.ConfirmedSubject("existing-nodes") != "" {
		return refuse(4, "ABANDON_REFUSED_COMPUTE_CONFIRMED",
			"a compute plan was confirmed for installation %s; its resources may exist, so the identity cannot be abandoned (procedures/bootstrap-resume.md)", uid)
	}
	if rb := s.eng.RecoveryBundle(); !s.lazy.Attached() && (s.eng.StepSucceeded("plan.storage-apply") || rb.S3URI != "" || rb.S3VersionID != "") {
		// the remote journal exists (storage was applied or a bundle was uploaded) but cannot be reached: it would stay
		// behind and the next run would adopt it again, so nothing is changed
		return refuse(7, "ABANDON_REMOTE_UNREACHABLE", "the remote journal of installation %s cannot be reached; nothing was changed; retry when the object store is reachable (procedures/object-storage-outage.md)", uid)
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
				return refuse(7, "ABANDON_ARCHIVE_FAILED", "the journal cannot be archived: %v", perr)
			}
		}
		if derr := s.lazy.Delete(ctx, engine.JournalKey(s.cluster), ""); derr != nil && !store.Is(derr, store.NotFound) {
			return refuse(7, "ABANDON_ARCHIVE_FAILED", "the remote journal cannot be removed: %v", derr)
		}
	}
	// NB1: what belongs to the abandoned identity moves aside (never deleted), or the next run would adopt the bundle
	// again and fail its confirmation for ever.
	// The journal goes last: a crash before that leaves the identity in place and a rerun finishes the move, while a
	// journal removed first would strand bundles that no abandon-identity could find again.
	aside := s.abandonedDir(uid)
	if len(journal) > 0 {
		if werr := writeAtomic(filepath.Join(aside, "journal.json"), journal); werr != nil {
			return refuse(1, "ABANDON_MOVE_FAILED", "%v", werr)
		}
	}
	if code := s.moveLocalAside(aside); code != "" {
		return refuse(1, "ABANDON_MOVE_FAILED", "%s", code)
	}
	if rerr := os.Remove(localPath); rerr != nil && !os.IsNotExist(rerr) {
		return refuse(1, "ABANDON_MOVE_FAILED", "%v", rerr)
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

// moveLocalAside moves the bundle directory's files (LATEST.json included) and recovery-identity.txt below aside. It
// returns an error text, "" on success.
func (s *sess) moveLocalAside(aside string) string {
	if err := os.MkdirAll(filepath.Join(aside, "bundles"), 0o700); err != nil {
		return err.Error()
	}
	bundles, _ := filepath.Glob(filepath.Join(s.cdir(), "bundles", "*"))
	for _, f := range bundles {
		if err := os.Rename(f, filepath.Join(aside, "bundles", filepath.Base(f))); err != nil {
			return err.Error()
		}
	}
	if err := os.Rename(filepath.Join(s.cdir(), "recovery-identity.txt"), filepath.Join(aside, "recovery-identity.txt")); err != nil && !os.IsNotExist(err) {
		return err.Error()
	}
	return ""
}

// moveOrphans handles a host with no journal and no identity but leftover bundles (an abandon-identity that died
// between the steps): they move below abandoned/orphaned-<sha256 prefix> so the next run starts a new identity. No
// identity is named, so there is nothing to confirm. moved is false when nothing was left.
func (s *sess) moveOrphans() (moved bool, code int) {
	bundles, _ := filepath.Glob(filepath.Join(s.cdir(), "bundles", "*"))
	rid := filepath.Join(s.cdir(), "recovery-identity.txt")
	_, rerr := os.Stat(rid)
	if len(bundles) == 0 && rerr != nil {
		return false, 0
	}
	src := rid
	if len(bundles) > 0 {
		src = bundles[0]
	}
	data, _ := os.ReadFile(src)
	aside := filepath.Join(s.cdir(), "abandoned", "orphaned-"+strings.TrimPrefix(sha256Digest(data), "sha256:")[:12])
	if msg := s.moveLocalAside(aside); msg != "" {
		s.errorf("ABANDON_MOVE_FAILED: %s\n", msg)
		return true, 1
	}
	s.printf("No installation identity is left on this host; its leftover local files are in %s.\nThe next run generates a new identity over the same buckets.\n", aside)
	return true, 0
}
