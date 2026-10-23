package worker

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"strange.company/sc/internal/clock"
	"strange.company/sc/internal/scrub"
)

// Exit codes of plan 27 section 14.14.
const (
	ExitOK           = 0
	ExitEnv          = 2
	ExitPostFailed   = 3
	ExitRejected     = 4
	ExitNoTaskToken  = 5
	ExitPanic        = 70
	ExitSIGTERM      = 143
	defaultSecrets   = "/var/run/sc/secrets"
	defaultWorkspace = "/workspace"
)

// Config is the library entry configuration of sc-worker. DefaultConfig returns the production values (the
// plan 27 path constants); tests inject path roots, the clock and the output sinks. No environment variable
// relocates /var/sc/private or the harness-secrets directory.
type Config struct {
	SecretsDir        string // default: $SC_SECRETS_DIR, else /var/run/sc/secrets
	WorkspaceDir      string // default: $SC_WORKSPACE, else /workspace
	HarnessSecretsDir string // default: /var/run/sc/harness-secrets
	PrivateDir        string // default: /var/sc/private
	HomeDir           string // default: /home/worker
	TmpDir            string // default: /tmp
	SSHProgram        string // default: /usr/bin/ssh
	Clock             clock.Clock
	Stdout            io.Writer
	Stderr            io.Writer
	Getenv            func(string) string // nil means os.Getenv
	// PanicAt is a test-only seam: when it equals "after_packet", Run panics right after the packet was
	// accepted, so the top-level recovery path (exit 70, best-effort failed/internal post) can be exercised.
	PanicAt string
}

// DefaultConfig returns the production configuration: the plan 27 path constants, SC_SECRETS_DIR and
// SC_WORKSPACE from the environment, the real clock and the process streams.
func DefaultConfig() Config {
	cfg := Config{
		SecretsDir:        defaultSecrets,
		WorkspaceDir:      defaultWorkspace,
		HarnessSecretsDir: "/var/run/sc/harness-secrets",
		PrivateDir:        "/var/sc/private",
		HomeDir:           "/home/worker",
		TmpDir:            "/tmp",
		SSHProgram:        "/usr/bin/ssh",
		Clock:             clock.Real{},
		Stdout:            os.Stdout,
		Stderr:            os.Stderr,
		Getenv:            os.Getenv,
	}
	if v := os.Getenv("SC_SECRETS_DIR"); v != "" {
		cfg.SecretsDir = v
	}
	if v := os.Getenv("SC_WORKSPACE"); v != "" {
		cfg.WorkspaceDir = v
	}
	return cfg
}

// Run is the whole of `sc-worker run` (plan 27 section 14) and returns the process exit code. It never calls
// os.Exit; a panic is recovered here, reported best-effort as failed/internal, and ends in exit 70.
func Run(cfg Config) (code int) {
	w := newWorker(cfg)

	sigc := make(chan os.Signal, 2)
	signal.Notify(sigc, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(sigc)
	sigCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.sig = sigCtx
	go func() {
		select {
		case s := <-sigc:
			w.log("info", "signal_received", map[string]any{"signal": s.String()})
			cancel()
		case <-sigCtx.Done():
		}
	}()

	defer func() {
		if r := recover(); r != nil {
			code = w.onPanic(r)
		}
	}()
	return w.run()
}

func (w *worker) run() int {
	cfg := w.cfg
	w.started = w.clk.Now()

	// 14.1 step 2: environment.
	w.runnerURL = strings.TrimRight(w.getenv("SC_RUNNER_URL"), "/")
	w.taskID = w.getenv("SC_TASK_ID")
	w.execID = w.getenv("SC_EXECUTION_ID")
	var missing []string
	for _, kv := range [][2]string{{"SC_RUNNER_URL", w.runnerURL}, {"SC_TASK_ID", w.taskID}, {"SC_EXECUTION_ID", w.execID}} {
		if kv[1] == "" {
			missing = append(missing, kv[0])
		}
	}
	if len(missing) > 0 {
		w.log("error", "env_missing", map[string]any{"names": missing})
		return w.exit(ExitEnv, "environment variable missing: "+strings.Join(missing, ", "))
	}

	// Step 3: task token; without it nothing can be reported (L-4), so no network call is made.
	tok := strings.TrimSpace(w.readSecret("task-token"))
	if tok == "" {
		w.log("error", "task_token_missing", nil)
		return w.exit(ExitNoTaskToken, "task-token missing or empty")
	}
	w.token = tok
	w.scrub = scrub.New(tok, w.readSecret("gateway-token"), w.readSecret("deploy-key"))

	// Step 4: workspace and private layout.
	for _, d := range []string{"repo", "context", "out", ".sc-shim"} {
		if err := os.MkdirAll(filepath.Join(cfg.WorkspaceDir, d), 0o755); err != nil {
			return w.exit(ExitEnv, "workspace: "+err.Error())
		}
	}
	if err := os.MkdirAll(cfg.PrivateDir, 0o700); err != nil {
		return w.exit(ExitEnv, "private dir: "+err.Error())
	}
	_ = os.Chmod(cfg.PrivateDir, 0o700)
	for _, d := range []string{"home", "tmp"} {
		if err := os.MkdirAll(filepath.Join(cfg.PrivateDir, d), 0o700); err != nil {
			return w.exit(ExitEnv, "private dir: "+err.Error())
		}
	}

	// 14.2 steps 1-2: packet.
	raw, code, ok := w.fetchPacket()
	if !ok {
		return code
	}
	w.pktRaw = raw
	bad := w.acceptPacket(raw)
	if w.sig.Err() != nil {
		return w.terminate()
	}
	if bad != "" {
		w.log("error", "packet_invalid", map[string]any{"detail": bad})
		if !w.haveIdentity {
			return w.exit(ExitEnv, "packet unusable: "+bad)
		}
		doc := w.failedResult("packet_invalid", bad)
		if err := w.selfValidate(doc); err != nil {
			return w.exit(ExitEnv, "packet unusable: "+bad)
		}
		return w.report(doc, modeNormal)
	}
	defer w.stopHeartbeat()
	w.startHeartbeat()

	if cfg.PanicAt == "after_packet" {
		panic("injected panic after the packet was accepted")
	}

	// Step 3: clock check.
	if dl, ok := w.deadline(); ok && !w.clk.Now().Before(dl.Add(-120*time.Second)) {
		return w.report(w.failedResult("harness_timeout", "no_time_left"), modeNormal)
	}
	if w.sig.Err() != nil {
		return w.terminate()
	}

	// Step 5: the remaining credential files.
	var absent []string
	for _, name := range []string{"deploy-key", "known_hosts", "gateway-token"} {
		if strings.TrimSpace(w.readSecret(name)) == "" {
			absent = append(absent, name)
		}
	}
	if len(absent) > 0 {
		return w.report(w.failedResult("credentials_missing", "missing or empty: "+strings.Join(absent, ", ")), modeNormal)
	}

	// The pipeline after the credential check (clone, checkout, context, harness, validation, commit, push;
	// plan 27 sections 14.4 to 14.11) belongs to the later slices of F07. Until they land the worker reports
	// that it stopped here, so no execution is silently dropped.
	w.setPhase("reporting")
	return w.report(w.failedResult("internal", "pipeline_not_implemented"), modeNormal)
}

// exit logs the reason on stderr and returns the code.
func (w *worker) exit(code int, msg string) int {
	fmt.Fprintf(w.errw, "sc-worker: exit %d: %s\n", code, msg)
	return code
}

func (w *worker) readSecret(name string) string {
	b, err := os.ReadFile(filepath.Join(w.cfg.SecretsDir, name))
	if err != nil {
		return ""
	}
	return string(b)
}
