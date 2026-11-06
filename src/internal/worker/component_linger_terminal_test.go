package worker_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// 27-AC-10 (plan 27 section 7.4, section 14 steps 2, 5, 6): a lingering harness must forge its result only after its
// terminal line is in the stream. The shim's copier is delayed by 60 ms, so a double that forges at once makes
// sc-worker see "process ended" without a result event (harness_stream_incomplete).
func TestLingerForgesOnlyAfterTerminalLineStreamed_27AC10(t *testing.T) {
	fx := buildFixture(t, false)
	stop := filepath.Join(t.TempDir(), "stop-writer")
	ops := []harnessOp{opWrite("calc/add_ac_test.go", addACTestSrc), {Op: "linger", Path: stop, Content: "tight"}}
	gr := runGit(t, gitSpec{fx: fx, mode: "ops", ops: ops, pkt: gitPacket(t, fx, pktOpts{}), streamDelay: 60 * time.Millisecond})
	go func() {
		<-gr.proc.done
		_ = os.WriteFile(stop, nil, 0o644)
	}()
	t.Cleanup(func() { _ = os.WriteFile(stop, nil, 0o644) })
	res := gr.finish(t)
	wantNoCanary(t, gr)
	if res["state"] != "completed" || res["pushed"] != true {
		t.Fatalf("state = %v, pushed = %v, want completed and pushed; failure %v", res["state"], res["pushed"], res["failure"])
	}
	wantPushedOnly(t, fx, res, "calc/add_ac_test.go", addACTestSrc)
}
