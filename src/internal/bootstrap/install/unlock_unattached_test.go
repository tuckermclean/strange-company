package install_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"strange.company/sc/internal/bootstrap/install"
	"strange.company/sc/internal/bootstrap/store"
	"strange.company/sc/internal/s3client"
)

// failHead is an account whose HeadBucket fails with err; everything else is the fake world.
type failHead struct {
	install.Account
	err error
}

func (f failHead) HeadBucket(context.Context, string) error { return f.err }

// S9 review 1 N3 (plan 50 section 17.4): when the S3 journal store cannot be attached for a reason other than "the
// bucket does not exist" (a dial error, a 403), `unlock` cannot know whether a lock object is held. It must fail closed:
// exit 1 RUN_LOCK_UNAVAILABLE, never "no run lock is held" with exit 0. An absent bucket means no remote lock can exist.
func TestUnlock_StoreCannotBeAttached_FailsClosed_S9N3(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		err      error
		wantExit int
	}{
		"dial error":    {&s3client.Error{Kind: store.Unavailable, Message: "dial tcp: connection refused"}, 1},
		"access denied": {&s3client.Error{Status: 403, Code: "AccessDenied"}, 1},
		"bucket absent": {nil, 0},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			i := newInst(t)
			d := i.deps("")
			if tc.err != nil {
				d.NewS3 = func(cfg s3client.Config) (install.Account, error) {
					a, err := i.world.open(cfg)
					return failHead{a, tc.err}, err
				}
			}
			var out, errb bytes.Buffer
			code := install.Run(i.args("unlock", "--non-interactive"), i.env, &out, &errb, d)
			all := out.String() + errb.String()
			if code != tc.wantExit {
				t.Errorf("exit %d, want %d:\n%s", code, tc.wantExit, all)
			}
			if tc.err != nil {
				if !strings.Contains(all, "RUN_LOCK_UNAVAILABLE") {
					t.Errorf("output lacks RUN_LOCK_UNAVAILABLE:\n%s", all)
				}
				if strings.Contains(all, "no run lock is held") {
					t.Errorf("claims no run lock is held although the store could not be read:\n%s", all)
				}
			} else if !strings.Contains(all, "no run lock is held") {
				t.Errorf("absent bucket: output lacks \"no run lock is held\":\n%s", all)
			}
		})
	}
}
