package lab_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// F09 S9b (plan 05 section 14.6 "Execution", 05-AC-6, 05-AC-8): test/e2e/lab/run is the harness of `make e2e-lab`. These
// tests run it with fake sc-lab, sc-bootstrap and pgrep on PATH (real jq, mktemp, sed): the call order, the trap that
// always runs `sc-lab down`, the assertions on provider-output.json and the qemu-system check.

const fakeScLab = `#!/bin/sh
echo "sc-lab $*" >>"$FAKE_LOG"
case $1 in
up)
	if [ -n "${FAKE_UP_FAIL:-}" ]; then echo "sc-lab: LAB_NO_KVM: /dev/kvm is not usable" >&2; exit 1; fi
	[ -n "${FAKE_NO_KVM_LINE:-}" ] || echo "sc-lab: kvm ok"
	echo /run/sc/lab/x/bootstrap.env ;;
env) printf 'CLUSTER_NAME=lab-t1\nPROVIDER=existing\n' ;;
esac
exit 0
`

const fakeScBootstrap = `#!/bin/sh
echo "sc-bootstrap $* | MODE=$SC_TEST_MODE STOP=$SC_TEST_STOP_AFTER PASSFILE=$([ -f "$SC_RECOVERY_PASSPHRASE_FILE" ] && echo yes || echo no) STDIN=$(cat)" >>"$FAKE_LOG"
[ -z "${FAKE_BOOT_FAIL:-}" ] || exit 4
mkdir -p .bootstrap/runs/r1 && cp "$FAKE_PO" .bootstrap/runs/r1/provider-output.json
`

const fakePgrep = `#!/bin/sh
echo "${FAKE_QEMU:-0}"
[ "${FAKE_QEMU:-0}" != 0 ]
`

type harness struct {
	t       *testing.T
	root    string
	bin     string
	log     string
	po      string
	harness string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	if _, err := exec.LookPath("jq"); err != nil {
		t.Fatalf("the harness needs jq (the ci image has it): %v", err)
	}
	h := &harness{t: t, root: t.TempDir(), bin: t.TempDir()}
	h.log = filepath.Join(h.bin, "calls.log")
	h.po = filepath.Join(h.bin, "po.json")
	h.harness = filepath.Join(realRoot(t), "test", "e2e", "lab", "run")
	for name, body := range map[string]string{"sc-lab": fakeScLab, "sc-bootstrap": fakeScBootstrap, "pgrep": fakePgrep} {
		if err := os.WriteFile(filepath.Join(h.bin, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	h.writePO("cloud-init", "10.77.0.11", 2201)
	return h
}

func (h *harness) writePO(source, ip string, port int) {
	h.t.Helper()
	doc := map[string]any{
		"provider": map[string]any{"capabilities": map[string]any{"host_key_source": source}},
		"nodes": []any{map[string]any{"name": "node-01",
			"addresses": map[string]any{"internal_ip": ip},
			"ssh":       map[string]any{"host": "127.0.0.1", "port": port}}},
	}
	b, err := json.Marshal(doc)
	if err != nil {
		h.t.Fatal(err)
	}
	if err := os.WriteFile(h.po, b, 0o644); err != nil {
		h.t.Fatal(err)
	}
}

// run executes the harness with the given extra environment and returns its exit code, combined output and the call log.
func (h *harness) run(extra ...string) (int, string, []string) {
	h.t.Helper()
	cmd := exec.Command("sh", h.harness)
	cmd.Env = append(os.Environ(), "PATH="+h.bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"SC_REPO_ROOT="+h.root, "SC_LAB_RUN_BASE="+filepath.Join(h.root, "labruns"), "FAKE_LOG="+h.log, "FAKE_PO="+h.po,
		"LAB_SUBSTRATE=lab-small", "LAB_OS=debian-13", "LAB_STOP_AFTER=provision")
	cmd.Env = append(cmd.Env, extra...)
	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			h.t.Fatalf("running the harness: %v", err)
		}
		code = ee.ExitCode()
	}
	b, _ := os.ReadFile(h.log)
	return code, string(out), strings.Split(strings.TrimSpace(string(b)), "\n")
}

func (h *harness) idx(calls []string, prefix string) int {
	for i, c := range calls {
		if strings.HasPrefix(c, prefix) {
			return i
		}
	}
	return -1
}

// 05-AC-6, 05-AC-8: up, env, sc-bootstrap with the stop phase, down; the kvm line is printed; the artifacts are kept.
func TestE2ELabHarness_HappyPath_05AC6_05AC8(t *testing.T) {
	h := newHarness(t)
	code, out, calls := h.run()
	if code != 0 {
		t.Fatalf("harness exit %d:\n%s", code, out)
	}
	if !strings.Contains(out, "sc-lab: kvm ok") {
		t.Errorf("the output lacks 'sc-lab: kvm ok' (05-AC-8 reads it from the CI log):\n%s", out)
	}
	up, envi, boot, down := h.idx(calls, "sc-lab up"), h.idx(calls, "sc-lab env"), h.idx(calls, "sc-bootstrap"), h.idx(calls, "sc-lab down")
	if up != 0 || !(up < envi && envi < boot && boot < down) {
		t.Errorf("call order wrong (up %d env %d bootstrap %d down %d): %v", up, envi, boot, down, calls)
	}
	if up >= 0 && !regexp.MustCompile(`^sc-lab up --substrate lab-small --os debian-13 --run-id e2e-[0-9]+$`).MatchString(calls[up]) {
		t.Errorf("up call = %q", calls[up])
	}
	if down >= 0 && calls[down] != "sc-lab down --run-id "+strings.TrimPrefix(calls[up], "sc-lab up --substrate lab-small --os debian-13 --run-id ") {
		t.Errorf("down call %q does not use the run id of up %q", calls[down], calls[up])
	}
	if boot >= 0 {
		for _, want := range []string{"--env-file ", " run ", "MODE=1", "STOP=provision", "PASSFILE=yes", "STDIN=lab-t1"} {
			if !strings.Contains(calls[boot]+" ", want) {
				t.Errorf("sc-bootstrap call lacks %q: %s", want, calls[boot])
			}
		}
	}
	if _, err := os.Stat(filepath.Join(h.root, "out", "e2e-lab", "provider-output.json")); err != nil {
		t.Errorf("provider-output.json is not kept as an artifact: %v", err)
	}
}

// 05-AC-6: LAB_STOP_AFTER reaches sc-bootstrap as SC_TEST_STOP_AFTER.
func TestE2ELabHarness_StopAfterIsPassedThrough_05AC6(t *testing.T) {
	h := newHarness(t)
	code, out, calls := h.run("LAB_STOP_AFTER=configure")
	if code != 0 {
		t.Fatalf("harness exit %d:\n%s", code, out)
	}
	if i := h.idx(calls, "sc-bootstrap"); i < 0 || !strings.Contains(calls[i], "STOP=configure") {
		t.Errorf("sc-bootstrap did not get SC_TEST_STOP_AFTER=configure: %v", calls)
	}
}

// 05-AC-6: a provider-output.json with the wrong host_key_source, internal_ip or ssh.port fails the run; down still ran.
func TestE2ELabHarness_WrongProviderOutput_Fails_05AC6(t *testing.T) {
	for name, po := range map[string]struct {
		src, ip string
		port    int
	}{
		"host_key_source tofu": {"tofu", "10.77.0.11", 2201},
		"internal_ip":          {"cloud-init", "10.77.0.12", 2201},
		"ssh.port":             {"cloud-init", "10.77.0.11", 2202},
	} {
		t.Run(strconv.Quote(name), func(t *testing.T) {
			h := newHarness(t)
			h.writePO(po.src, po.ip, po.port)
			code, out, calls := h.run()
			if code == 0 {
				t.Errorf("harness exited 0 with %s wrong:\n%s", name, out)
			}
			if h.idx(calls, "sc-lab down") < 0 {
				t.Errorf("sc-lab down was not run: %v", calls)
			}
		})
	}
}

// 05-AC-6: a failing sc-bootstrap fails the run and the trap still runs sc-lab down.
func TestE2ELabHarness_BootstrapFails_StillDown_05AC6(t *testing.T) {
	h := newHarness(t)
	code, out, calls := h.run("FAKE_BOOT_FAIL=1")
	if code == 0 {
		t.Errorf("harness exited 0 although sc-bootstrap failed:\n%s", out)
	}
	if h.idx(calls, "sc-lab down") < 0 {
		t.Errorf("sc-lab down was not run: %v", calls)
	}
}

// 05-AC-6: a qemu-system process left after `sc-lab down` fails the run (`pgrep -c qemu-system` must be 0).
func TestE2ELabHarness_QemuLeftBehind_Fails_05AC6(t *testing.T) {
	h := newHarness(t)
	code, out, _ := h.run("FAKE_QEMU=1")
	if code == 0 {
		t.Errorf("harness exited 0 with a qemu-system process left:\n%s", out)
	}
	if !strings.Contains(out, "qemu-system") {
		t.Errorf("the failure does not name qemu-system:\n%s", out)
	}
}

// 05-AC-8: `sc-lab up` without the kvm line (no /dev/kvm open) never reaches sc-bootstrap, with or without a failing up.
func TestE2ELabHarness_NoKVM_FailsBeforeBootstrap_05AC8(t *testing.T) {
	for name, env := range map[string]string{"up fails LAB_NO_KVM": "FAKE_UP_FAIL=1", "up succeeds without the kvm line": "FAKE_NO_KVM_LINE=1"} {
		t.Run(strconv.Quote(name), func(t *testing.T) {
			h := newHarness(t)
			code, out, calls := h.run(env)
			if code == 0 {
				t.Errorf("harness exited 0:\n%s", out)
			}
			if h.idx(calls, "sc-bootstrap") >= 0 {
				t.Errorf("sc-bootstrap ran without a verified /dev/kvm: %v", calls)
			}
			if h.idx(calls, "sc-lab down") < 0 {
				t.Errorf("sc-lab down was not run: %v", calls)
			}
		})
	}
}
