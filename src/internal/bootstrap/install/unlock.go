package install

import (
	"context"

	"strange.company/sc/internal/bootstrap/engine"
)

// cmdUnlock is `./bootstrap unlock [--run-id ID]` (plan 50 section 7.1 and 17.4): it shows the run lock and, once the
// operator types `unlock <cluster> <run_id>`, breaks it. The engine does the checks, the deletion and the journal entry.
func (s *sess) cmdUnlock(ctx context.Context) int {
	code, ok := s.stateSetup(ctx, "unlock")
	if !ok {
		return code
	}
	if s.oc.prefix != "" && !s.lazy.Attached() && !s.bucketAbsent {
		// the remote run lock object cannot be read, so whether one is held is unknown (plan 50 section 17.4);
		// only a tofu-state bucket confirmed absent rules a remote lock out
		s.errorf("RUN_LOCK_UNAVAILABLE: the object store cannot be reached, so the remote run lock cannot be read; nothing was changed; retry when it is reachable (procedures/object-storage-outage.md)\n")
		return 1
	}
	o := engine.UnlockOptions{RunID: s.inv.Flags["run-id"]}
	res := s.eng.Unlock(ctx, o)
	if res.Code == "UNLOCK_CONFIRMATION_REQUIRED" && !s.inv.NonInteractive {
		s.errorf("%s\n", res.Message)
		line, err := s.readLine(ctx, "> ", false)
		if err != nil {
			return s.abandonInputErr(err)
		}
		o.Confirm = line
		res = s.eng.Unlock(ctx, o)
	}
	if res.Exit != 0 {
		s.errorf("%s: %s\n", res.Code, res.Message)
		return res.Exit
	}
	s.printf("%s\n", res.Message)
	return 0
}
