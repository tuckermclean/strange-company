package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
)

// nextAction computes the operator's next step from the journal (plan 50 section 7.3).
func nextAction(j *journalDoc) string {
	for _, pl := range j.Pipelines {
		for _, ph := range pl.Phases {
			if ph.Status == stFailed && ph.Error != nil {
				msg := fmt.Sprintf("phase %s failed with %s: remove the cause, then rerun ./bootstrap", ph.Name, ph.Error.Code)
				if ph.Error.Procedure != "" {
					msg += " (see " + ph.Error.Procedure + ")"
				}
				if ph.Attempt >= retryCeiling {
					msg += fmt.Sprintf("; after %d attempts it is refused until its inputs change or you pass --retry-phase %s", ph.Attempt, ph.Name)
				}
				return msg
			}
		}
	}
	for _, pl := range j.Pipelines {
		for _, ph := range pl.Phases {
			if ph.Status == stPending || ph.Status == stRunning || ph.Status == stInterrupted {
				return fmt.Sprintf("run ./bootstrap to continue with phase %s", ph.Name)
			}
		}
	}
	switch j.Installation.State {
	case "transferred":
		return "the installation is transferred; ./bootstrap verify checks it again"
	case "new":
		return "run ./bootstrap preflight, then ./bootstrap to start the installation"
	}
	return "run ./bootstrap to continue the installation (state " + j.Installation.State + ")"
}

// Status implements `sc-bootstrap status [--json]`: it prints the journal summary (or the journal plus next_action
// as JSON) and returns 0, or 7 on a journal conflict after writing both copies under
// runs/<run-id>/journal-conflict/. It neither takes the run lock nor writes the journal.
func Status(ctx context.Context, cfg Config, jsonOut bool, stdout, stderr io.Writer) int {
	e, err := New(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "status: %v\n", err)
		return 1
	}
	lr, fail := e.load(ctx)
	if fail != nil {
		fmt.Fprintf(stderr, "%s: %s\n", fail.Code, e.red.String(fail.Message))
		return fail.Exit
	}
	if lr.conflict {
		res := e.conflictResult(lr)
		fmt.Fprintf(stderr, "%s: %s\n", res.Code, res.Message)
		return res.Exit
	}
	j := lr.doc
	na := e.red.String(nextAction(j))
	if jsonOut {
		b, err := json.Marshal(j)
		if err != nil {
			fmt.Fprintf(stderr, "status: %v\n", err)
			return 1
		}
		dec := json.NewDecoder(bytes.NewReader(b))
		dec.UseNumber()
		var m map[string]any
		if err := dec.Decode(&m); err != nil {
			fmt.Fprintf(stderr, "status: %v\n", err)
			return 1
		}
		m["next_action"] = na
		out, err := json.MarshalIndent(m, "", "  ")
		if err != nil {
			fmt.Fprintf(stderr, "status: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "%s\n", e.red.Bytes(out))
		return 0
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "cluster %s  install %s  state %s  journal_seq %d\n", j.Cluster, j.InstallUID, j.Installation.State, j.JournalSeq)
	tw := tabwriter.NewWriter(&sb, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "PIPELINE\tPHASE\tSTATUS\tATTEMPT\tLAST ERROR")
	for _, pl := range j.Pipelines {
		for _, ph := range pl.Phases {
			last := "-"
			if ph.Error != nil {
				last = ph.Error.Code
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\n", pl.Pipeline, ph.Name, ph.Status, ph.Attempt, last)
		}
	}
	_ = tw.Flush()
	if n := len(j.Runs); n > 0 {
		r := j.Runs[n-1]
		code := "-"
		if r.ExitCode != nil {
			code = fmt.Sprint(*r.ExitCode)
		}
		fmt.Fprintf(&sb, "last run %s  %s  %s  exit %s\n", r.RunID, r.Command, r.Outcome, code)
	}
	fmt.Fprintf(&sb, "next_action: %s\n", na)
	fmt.Fprint(stdout, e.red.String(sb.String()))
	return 0
}
