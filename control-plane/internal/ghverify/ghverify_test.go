package ghverify_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/tuckermclean/strange-company/control-plane/internal/codingrun"
	"github.com/tuckermclean/strange-company/control-plane/internal/ghverify"
	"github.com/tuckermclean/strange-company/control-plane/internal/github"
	"github.com/tuckermclean/strange-company/control-plane/internal/redgate"
)

type fakeChecks struct {
	outcomes []redgate.RunOutcome
	errs     []error
	calls    int
	refs     []string
}

func (f *fakeChecks) ChecksFor(_ context.Context, _, ref string) (redgate.RunOutcome, error) {
	i := f.calls
	if i >= len(f.outcomes) {
		i = len(f.outcomes) - 1
	}
	f.calls++
	f.refs = append(f.refs, ref)
	if i < len(f.errs) && f.errs[i] != nil {
		return redgate.RunOutcome{}, f.errs[i]
	}
	return f.outcomes[i], nil
}

func req() codingrun.VerifyRequest {
	return codingrun.VerifyRequest{Repository: "example/repo", Ref: "agent/card-1"}
}

func green() redgate.RunOutcome  { return redgate.RunOutcome{Completed: true, ExitCode: 0} }
func red() redgate.RunOutcome    { return redgate.RunOutcome{Completed: true, ExitCode: 1} }
func running() redgate.RunOutcome { return redgate.RunOutcome{} }

func TestAGreenRefIsAPass(t *testing.T) {
	c := &fakeChecks{outcomes: []redgate.RunOutcome{green()}}

	got, err := ghverify.New(c, time.Millisecond, time.Second, nil).Verify(context.Background(), req())
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !got.Completed || got.ExitCode != 0 {
		t.Fatalf("outcome = %+v", got)
	}
	if c.refs[0] != "agent/card-1" {
		t.Fatalf("asked about ref %q", c.refs[0])
	}
}

// CI takes minutes. The verifier waits for the checks to finish rather than
// reading "still running" as a verdict -- redgate would call that inconclusive
// and the card would stall on every attempt.
func TestItWaitsForChecksToFinish(t *testing.T) {
	c := &fakeChecks{outcomes: []redgate.RunOutcome{running(), running(), red()}}

	got, err := ghverify.New(c, time.Millisecond, time.Second, nil).Verify(context.Background(), req())
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !got.Completed || got.ExitCode == 0 {
		t.Fatalf("outcome = %+v", got)
	}
	if c.calls < 3 {
		t.Fatalf("polled %d times; it did not wait", c.calls)
	}
}

// A ref whose checks never arrive is not a failure. Waiting forever would hold
// a card's lease; reporting a verdict would invent one.
func TestChecksThatNeverFinishStayIncomplete(t *testing.T) {
	c := &fakeChecks{outcomes: []redgate.RunOutcome{running()}}

	got, err := ghverify.New(c, time.Millisecond, 20*time.Millisecond, nil).Verify(context.Background(), req())
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if got.Completed {
		t.Fatalf("invented a verdict: %+v", got)
	}
}

// A workflow that does not trigger on agent/* branches produces no checks at
// all, which looks exactly like nothing failing. It must reach the operator as
// an error rather than a pass.
func TestNoChecksIsAnError(t *testing.T) {
	c := &fakeChecks{outcomes: []redgate.RunOutcome{{}}, errs: []error{github.ErrNoChecks}}

	_, err := ghverify.New(c, time.Millisecond, time.Second, nil).Verify(context.Background(), req())
	if !errors.Is(err, github.ErrNoChecks) {
		t.Fatalf("error = %v, want ErrNoChecks", err)
	}
}
