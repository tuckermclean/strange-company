package config_test

import (
	"errors"
	"strings"
	"testing"

	"strange.company/sc/internal/bootstrap/config"
)

// 05-AC-1 (S7 review, plan 05 section 9): a host or user that starts with "-" would be passed to ssh and ssh-keyscan as an
// option (-oProxyCommand=... runs a command on the operator's machine). The EXISTING_NODES parser rejects it with a
// field-level error naming EXISTING_NODES (exit 2 at preflight), so it never reaches a transport.
func TestExistingEnv_HostStartingWithDashIsRejected_05AC1(t *testing.T) {
	for _, nodes := range []string{
		"node-01=debian@-oProxyCommand=x",
		"node-01=debian@-oProxyCommand=x:22",
		"node-01=debian@-v/10.77.0.11",
		"node-01=debian@203.0.113.10,node-02=debian@-F/dev/null",
	} {
		t.Run(nodes, func(t *testing.T) {
			env := merge(baseExisting(), map[string]string{"EXISTING_NODES": nodes}, nil)
			if strings.Contains(nodes, "node-02") {
				env["EXPECTED_NODE_COUNT"] = "2"
			}
			_, err := config.Normalize(env, opts(""))
			var ce *config.Error
			if err == nil || !errors.As(err, &ce) {
				t.Fatalf("a host starting with '-' was accepted (err=%v)", err)
			}
			if ce.ExitCode != 2 || !strings.Contains(ce.Error(), "EXISTING_NODES") {
				t.Errorf("exit %d, error %q: want exit 2 naming EXISTING_NODES", ce.ExitCode, ce.Error())
			}
		})
	}
}
