package runner

import (
	"strings"
	"testing"
	"time"
)

func TestOpenCodeCommandIsHeadlessAndPinsTheModel(t *testing.T) {
	argv := OpenCodeAdapter{}.Command(Request{
		Task:  "implement the health endpoint",
		Model: "deepseek/deepseek-v4-pro",
	})

	if argv[0] != "opencode" || argv[1] != "run" {
		t.Fatalf("argv = %v", argv)
	}
	joined := strings.Join(argv, " ")
	for _, want := range []string{"--format json", "--model deepseek/deepseek-v4-pro"} {
		if !strings.Contains(joined, want) {
			t.Errorf("argv is missing %q: %v", want, argv)
		}
	}
	// The task is the final positional argument, never interpolated into a
	// flag or a shell string.
	if argv[len(argv)-1] != "implement the health endpoint" {
		t.Errorf("the task is not the final argument: %v", argv)
	}
}

// The Adapter contract forbids a permission-bypass flag. opencode's --auto
// "auto-approves permissions that are not explicitly denied", which is exactly
// that: permissions must come from the allowlist and the sandbox the adapter
// selects, per §13, §14 and §24.
func TestOpenCodeNeverAutoApprovesPermissions(t *testing.T) {
	argv := OpenCodeAdapter{}.Command(Request{Task: "t", Model: "m"})

	for _, flag := range []string{"--auto", "--dangerously", "--yolo"} {
		for _, arg := range argv {
			if strings.Contains(arg, flag) {
				t.Fatalf("argv carries the permission bypass %q: %v", flag, argv)
			}
		}
	}
}

const openCodeStream = `{"type":"step_start","sessionID":"ses_1"}
{"type":"text","text":"Adding the handler"}
{"type":"step_finish","tokens":{"input":120,"output":340,"reasoning":10,"cache":{"read":5,"write":2}},"cost":0.0021}
`

func TestOpenCodeParsesTokensAndCost(t *testing.T) {
	got, err := OpenCodeAdapter{}.Parse([]byte(openCodeStream), 0, 2*time.Second)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got.Status != StatusCompleted {
		t.Fatalf("status = %q", got.Status)
	}
	if got.Usage.InputTokens != 120 || got.Usage.OutputTokens != 340 {
		t.Fatalf("usage = %+v", got.Usage)
	}
	if got.CostUSD == nil || *got.CostUSD != 0.0021 {
		t.Fatalf("cost = %v", got.CostUSD)
	}
	if !strings.Contains(got.Summary, "Adding the handler") {
		t.Errorf("summary = %q", got.Summary)
	}
}

// opencode is documented to exit before emitting step_finish, and to drop
// text and step-finish events specifically in containerised environments --
// which is exactly where this runs. A run whose work succeeded must not be
// reported as an infrastructure failure just because the terminal event never
// arrived, or the escalation ladder would never move.
//
// See github.com/anomalyco/opencode issues 26855 and 31435.
func TestAMissingStepFinishIsStillACompletedRun(t *testing.T) {
	stream := `{"type":"step_start","sessionID":"ses_1"}
{"type":"text","text":"Added the handler and its test"}
`
	got, err := OpenCodeAdapter{}.Parse([]byte(stream), 0, time.Second)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got.Status != StatusCompleted {
		t.Fatalf("status = %q", got.Status)
	}
	// Cost is unknown, not zero. A ledger that silently records 0 is worse
	// than one that honestly records nothing.
	if got.CostUSD != nil {
		t.Errorf("cost = %v; it was never reported", *got.CostUSD)
	}
}

func TestANonZeroExitIsAFailedRun(t *testing.T) {
	got, err := OpenCodeAdapter{}.Parse([]byte(openCodeStream), 1, time.Second)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got.Status != StatusFailed {
		t.Fatalf("status = %q", got.Status)
	}
}

// Nothing parseable at all is genuinely unreadable, and the contract says that
// is an error rather than a silent zero-value result.
func TestUnreadableOutputIsAnError(t *testing.T) {
	for _, in := range []string{"", "   ", "not json at all\nnor this\n"} {
		if _, err := (OpenCodeAdapter{}).Parse([]byte(in), 0, time.Second); err == nil {
			t.Errorf("Parse(%q) returned no error", in)
		}
	}
}

// The raw stream is kept for audit (§21) and so a parsing mistake here can be
// diagnosed against what the harness actually said.
func TestTheRawStreamIsKept(t *testing.T) {
	got, err := OpenCodeAdapter{}.Parse([]byte(openCodeStream), 0, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Raw) == 0 {
		t.Fatal("the raw stream was discarded")
	}
}

func TestOpenCodeIdentifiesItself(t *testing.T) {
	if (OpenCodeAdapter{}).Name() != "opencode" {
		t.Fatalf("name = %q", (OpenCodeAdapter{}).Name())
	}
}

// A run that dies before emitting an event has to explain itself. The first
// real end-to-end attempt produced no events and no reason, and diagnosing it
// cost a round trip: opencode's logs go to stderr and were simply never asked
// for.
func TestOpenCodeIsAskedForItsLogs(t *testing.T) {
	argv := OpenCodeAdapter{}.Command(Request{Task: "t", Model: "m"})

	joined := strings.Join(argv, " ")
	if !strings.Contains(joined, "--print-logs") {
		t.Errorf("argv does not ask for logs, so a silent failure stays silent: %v", argv)
	}
	// Still JSON on stdout: Parse skips what it cannot read, so the logs
	// interleaving in a pod log costs nothing.
	if !strings.Contains(joined, "--format json") {
		t.Errorf("argv = %v", argv)
	}
}

// opencode nests an event's content under "part". This adapter read "tokens"
// and "cost" from the top level, where they are never present -- so every run
// was recorded with no usage and no narrative, and both symptoms were blamed
// on an upstream bug that drops events in containers. The events were in the
// stream all along, one level down.
//
// The shape here is from a real 0.10.2 coding run on the operator's cluster.
func TestUsageIsReadFromTheNestedPart(t *testing.T) {
	stream := `{"type":"step_finish","part":{"tokens":{"total":9832,"input":7699,"output":164,"reasoning":49,"cache":{"write":0,"read":1920}},"cost":0}}
`
	res, err := OpenCodeAdapter{}.Parse([]byte(stream), 0, time.Second)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if res.Usage.InputTokens != 7699 || res.Usage.OutputTokens != 164 {
		t.Errorf("usage = %+v, want the tokens opencode actually reported", res.Usage)
	}
	if res.Usage.CachedInputTokens != 1920 {
		t.Errorf("cached input = %d, want 1920", res.Usage.CachedInputTokens)
	}
	if res.Usage.ReasoningTokens != 49 {
		t.Errorf("reasoning = %d, want 49", res.Usage.ReasoningTokens)
	}
	// opencode reports 0 for a provider models.dev cannot price. Reported
	// zero is not the same as unreported, and the ledger needs to tell them
	// apart to know whether to price it itself.
	if res.CostUSD == nil || *res.CostUSD != 0 {
		t.Errorf("cost = %v, want a reported zero", res.CostUSD)
	}
}

// A run makes many steps and each reports its own usage. Keeping only the last
// one charged a whole card for its final turn.
func TestUsageAccumulatesAcrossSteps(t *testing.T) {
	stream := `{"type":"step_finish","part":{"tokens":{"input":100,"output":10,"cache":{"read":5,"write":1}},"cost":0.01}}
{"type":"step_finish","part":{"tokens":{"input":200,"output":20,"cache":{"read":7,"write":2}},"cost":0.02}}
`
	res, err := OpenCodeAdapter{}.Parse([]byte(stream), 0, time.Second)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if res.Usage.InputTokens != 300 || res.Usage.OutputTokens != 30 {
		t.Errorf("usage = %+v, want both steps summed", res.Usage)
	}
	if res.Usage.CachedInputTokens != 12 || res.Usage.CacheCreationTokens != 3 {
		t.Errorf("cache = read %d write %d, want 12 and 3",
			res.Usage.CachedInputTokens, res.Usage.CacheCreationTokens)
	}
	if res.CostUSD == nil || *res.CostUSD < 0.0299 || *res.CostUSD > 0.0301 {
		t.Errorf("cost = %v, want the steps summed to 0.03", res.CostUSD)
	}
}

// The other half of the same bug: a run's narrative is nested too, which is
// why every summary read "opencode exited 0 with no narrative output".
func TestNarrativeIsReadFromTheNestedPart(t *testing.T) {
	stream := `{"type":"text","part":{"text":"wrote src/tictactoe.js and its tests"}}
{"type":"step_finish","part":{"tokens":{"input":1,"output":1,"cache":{}},"cost":0}}
`
	res, err := OpenCodeAdapter{}.Parse([]byte(stream), 0, time.Second)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !strings.Contains(res.Summary, "wrote src/tictactoe.js") {
		t.Errorf("summary = %q, want the model's own account", res.Summary)
	}
}

// Kept because the evidence for the nested shape is one real run: if opencode
// ever emits the flat shape, this still reads it.
func TestTheFlatShapeStillParses(t *testing.T) {
	stream := `{"type":"text","text":"done"}
{"type":"step_finish","tokens":{"input":5,"output":2,"cache":{}},"cost":0.5}
`
	res, err := OpenCodeAdapter{}.Parse([]byte(stream), 0, time.Second)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if res.Usage.InputTokens != 5 || res.Summary != "done" {
		t.Errorf("usage = %+v summary = %q", res.Usage, res.Summary)
	}
}
