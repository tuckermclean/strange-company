package installf09_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// 03-AC-10 (plan 03 14.5): the catalogue of a paid provider is that provider's: a catalogue file whose spec.provider is
// another provider than the one running is unusable (COST_INCOMPLETE, exit 4), not silently priced.
func TestCost_CatalogueOfAnotherProviderExits4_03AC10(t *testing.T) {
	t.Parallel()
	i := newInst(t)
	s5Profiles(t, i)
	i.setVars(map[string]string{"MAX_MONTHLY_COST": "25"})
	i.useScenario("paid")
	s5bCatalogue(t, i, func(spec, _ map[string]any) { spec["provider"] = "ovh" })
	code, out := s5Provision(t, i, nil)
	requireExit(t, code, 4, out)
	if !strings.Contains(out, "COST_INCOMPLETE") {
		t.Errorf("output lacks COST_INCOMPLETE:\n%s", out)
	}
	if got := i.createCount(); got != 0 {
		t.Errorf("create counter = %d after the refusal, want 0", got)
	}
}

// 03-AC-11 (plan 03 9.2, PF-03): `preflight --profile lab-small` with a paid provider fails PF-03
// LAB_SUBSTRATE_PAID_REFUSED, like the run does (exit 3).
func TestPreflight_PaidProviderRefusesLabSubstrate_03AC11(t *testing.T) {
	t.Parallel()
	i := newInst(t)
	s5Profiles(t, i)
	i.setVars(map[string]string{"MAX_MONTHLY_COST": "25"})
	i.useScenario("paid")
	s5Catalogue(t, i, 10)
	env := merge(i.unattendedEnv(), stopAfter("provision"))
	code, out := i.run("", env, i.envOnlyArgs("--non-interactive", "preflight", "--profile", "lab-small"))
	requireExit(t, code, 3, out)
	for _, w := range []string{"PF-03", "LAB_SUBSTRATE_PAID_REFUSED"} {
		if !strings.Contains(out, w) {
			t.Errorf("preflight output lacks %s:\n%s", w, out)
		}
	}
	if got := i.createCount(); got != 0 {
		t.Errorf("create counter = %d after preflight, want 0", got)
	}
}

// 03-AC-9 (plan 03 9.1, plan 50 14.2): the PF-03 row is printed even when the other preflight rows already failed (here
// the budget confirmation is missing), so one run shows every problem.
func TestPreflight_PF03RowPrintedAlongsideFailedRows_03AC9(t *testing.T) {
	t.Parallel()
	i := newInst(t)
	s5Profiles(t, i)
	args := []string{"--env-file", i.envFile, "--confirm-backstops", i.backstopFile(), "--non-interactive", "preflight", "--profile", "small-ha"}
	code, out := i.run("", merge(i.passphraseFile(), stopAfter("provision")), args)
	if code != 3 && code != 5 {
		t.Fatalf("exit %d, want 3 or 5 (a failed preflight):\n%s", code, out)
	}
	for _, w := range []string{"PF-03", "PROFILE_KIND_MISMATCH"} {
		if !strings.Contains(out, w) {
			t.Errorf("preflight output lacks %s although the other rows failed:\n%s", w, out)
		}
	}
}

// 03-AC-9 (plan 03 9.1; config's PROFILE pattern ^[a-z][a-z0-9-]{1,61}[a-z0-9]$): a profile name is a name, not a path.
// `expand --profile <path>` is refused with exit 2 or 3 before any file outside infra/profiles/<name>.yaml is read: the
// path "../profiles/small-ha" resolves to a valid ClusterProfile, so reading it would reach COMMAND_NOT_IMPLEMENTED.
func TestExpand_ProfileNameIsValidatedNotAPath_03AC9(t *testing.T) {
	t.Parallel()
	for name, profile := range map[string]string{
		"dotdot-resolving-to-a-profile": "../profiles/small-ha",
		"dotdot-outside":                "../x",
		"absolute":                      "/etc/passwd",
		"subdirectory":                  "sub/small-ha",
		"uppercase":                     "Small-HA",
		"trailing-dash":                 "small-ha-",
	} {
		t.Run("flag-"+name, func(t *testing.T) {
			t.Parallel()
			i := newInst(t)
			s5Profiles(t, i)
			code, out := i.run("", i.unattendedEnv(), i.envOnlyArgs("expand", "--profile", profile))
			if code != 2 && code != 3 {
				t.Errorf("exit %d, want 2 or 3 for --profile %q:\n%s", code, profile, out)
			}
			if strings.Contains(out, "COMMAND_NOT_IMPLEMENTED") {
				t.Errorf("--profile %q was accepted as a profile (reached COMMAND_NOT_IMPLEMENTED):\n%s", profile, out)
			}
		})
	}
	t.Run("env-file-PROFILE", func(t *testing.T) {
		t.Parallel()
		i := newInst(t)
		s5Profiles(t, i)
		i.setVars(map[string]string{"PROFILE": "../profiles/small-ha"})
		code, out := i.run("", i.unattendedEnv(), i.envOnlyArgs("expand"))
		if code != 2 && code != 3 {
			t.Errorf("exit %d, want 2 or 3 for PROFILE=../profiles/small-ha:\n%s", code, out)
		}
		if strings.Contains(out, "COMMAND_NOT_IMPLEMENTED") {
			t.Errorf("PROFILE=../profiles/small-ha was accepted as a profile:\n%s", out)
		}
	})
	t.Run("preflight-flag", func(t *testing.T) {
		t.Parallel()
		i := newInst(t)
		s5Profiles(t, i)
		// a substrate file reachable only through "..": reading it would make this preflight pass
		b := readProfileFile(t, i, "self-hosting-minimum")
		write(t, filepath.Join(i.repo, "infra", "decoy.yaml"), b, 0o644)
		s5Commit(i, "decoy")
		code, out := i.run("", merge(i.unattendedEnv(), stopAfter("provision")), i.envOnlyArgs("--non-interactive", "preflight", "--profile", "../decoy"))
		if code != 2 && code != 3 {
			t.Errorf("exit %d, want 2 or 3 for preflight --profile ../decoy:\n%s", code, out)
		}
	})
}

func readProfileFile(t testing.TB, i *inst, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(i.repo, "infra", "profiles", name+".yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	out, err := yaml.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}
