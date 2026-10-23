package adapters_test

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"

	"strange.company/sc/internal/worker/adapters"
)

// 28-AC-5, N4: the worker-input schema example carries --bare, --strict-mcp-config and --no-session-persistence
// in harness.extra_args; the adapter already passes them, so each must still appear exactly once in argv.
func TestPrepare_ClaudeCodeExtraArgsNotDuplicated_28AC5_N4(t *testing.T) {
	dir := filepath.Join("testdata", "claude-code")
	packet := readFile(t, dir, "packet-tester-extra-args.json")
	validatePacket(t, packet)
	var in struct {
		Harness struct {
			ExtraArgs []string `json:"extra_args"`
		} `json:"harness"`
	}
	if err := json.Unmarshal(packet, &in); err != nil || len(in.Harness.ExtraArgs) == 0 {
		t.Fatalf("fixture must carry non-empty extra_args: %v", err)
	}

	got, err := adapters.Prepare(packet)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	for _, flag := range []string{"--bare", "--strict-mcp-config", "--no-session-persistence"} {
		n := 0
		for _, a := range got.Argv {
			if a == flag {
				n++
			}
		}
		if n != 1 {
			t.Errorf("%s appears %d times in argv, want exactly 1: %q", flag, n, got.Argv)
		}
	}

	argv := append([]string(nil), got.Argv...)
	for i := 0; i+1 < len(argv); i++ {
		switch argv[i] {
		case "-p":
			argv[i+1] = "<PROMPT>"
		case "--append-system-prompt":
			argv[i+1] = "<SYSTEM_ADDENDUM>"
		}
	}
	var want adapters.Prepared
	if err := json.Unmarshal(readFile(t, dir, "prepare-tester-extra-args.golden.json"), &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(argv, want.Argv) {
		t.Errorf("argv differs from the golden\n got: %q\nwant: %q", argv, want.Argv)
	}
}

func packetWithEnvironment(t testing.TB, adapter string, env map[string]string) []byte {
	t.Helper()
	var p map[string]any
	if err := json.Unmarshal(readFile(t, "testdata", "claude-code", "packet-tester.json"), &p); err != nil {
		t.Fatal(err)
	}
	h := p["harness"].(map[string]any)
	if adapter == "fake" {
		h = map[string]any{"adapter": "fake", "cli_version": "0.1.0", "fake_scenario": "happy-path"}
		p["harness"] = h
	}
	e := map[string]any{}
	if old, ok := h["environment"].(map[string]any); ok {
		e = old
	}
	for k, v := range env {
		e[k] = v
	}
	h["environment"] = e
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Plan 27 section 14.7, N4: the harness environment is built from scratch. Packet harness.environment cannot set SC_*
// names the registry does not name, nor PATH, HOME, TMPDIR or the *_PROXY variables the shim adds at run time.
func TestPrepare_PacketEnvironmentCannotOverrideBase_14_7_N4(t *testing.T) {
	hostile := map[string]string{
		"SC_FOO":      "evil",
		"PATH":        "/evil/bin",
		"HOME":        "/evil/home",
		"TMPDIR":      "/evil/tmp",
		"HTTPS_PROXY": "http://evil.example:3128",
		"HTTP_PROXY":  "http://evil.example:3128",
		"NO_PROXY":    "evil.example",
	}
	for _, adapter := range []string{"claude-code", "fake"} {
		t.Run(adapter, func(t *testing.T) {
			packet := packetWithEnvironment(t, adapter, hostile)
			validatePacket(t, packet)
			got, err := adapters.Prepare(packet)
			if err != nil {
				t.Fatalf("Prepare: %v", err)
			}
			for _, k := range []string{"SC_FOO", "HTTPS_PROXY", "HTTP_PROXY", "NO_PROXY"} {
				if v, ok := got.Env[k]; ok {
					t.Errorf("env %s=%q reached the prepared environment", k, v)
				}
			}
			want := map[string]string{"PATH": "/usr/local/bin:/usr/bin:/bin", "HOME": "/home/worker", "TMPDIR": "/tmp"}
			for k, v := range want {
				if got.Env[k] != v {
					t.Errorf("env %s = %q, want %q", k, got.Env[k], v)
				}
			}
		})
	}
}
