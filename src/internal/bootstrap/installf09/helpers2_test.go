package installf09_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"strange.company/sc/internal/provider"
	"strange.company/sc/internal/provider/fake"
)

// Copies of install package test helpers (F09 install tests moved here for the CI time budget).

// stopAfter ends the pipeline successfully after a step (SC_TEST_STOP_AFTER, plan 50 section 9.2).
func stopAfter(step string) map[string]string { return map[string]string{"SC_TEST_STOP_AFTER": step} }

type pvRef struct {
	Kind string `json:"kind"`
	Ref  string `json:"ref"`
}

type pvPhase struct {
	Name       string  `json:"name"`
	Status     string  `json:"status"`
	Attempt    int     `json:"attempt"`
	OutputRefs []pvRef `json:"output_refs"`
	Steps      []jStep `json:"steps"`
}

type pvJournal struct {
	InstallUID   string `json:"install_uid"`
	Installation struct {
		State string `json:"state"`
	} `json:"installation"`
	Pipelines []struct {
		Pipeline string    `json:"pipeline"`
		Phases   []pvPhase `json:"phases"`
	} `json:"pipelines"`
	Confirmations []jConfirm `json:"confirmations"`
}

func pvRead(t testing.TB, i *inst) pvJournal {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(i.repo, ".bootstrap", "journal.json"))
	if err != nil {
		t.Fatalf("no local journal: %v", err)
	}
	var j pvJournal
	if err := json.Unmarshal(b, &j); err != nil {
		t.Fatalf("journal does not parse: %v", err)
	}
	return j
}

func (j pvJournal) phases() []pvPhase {
	for _, p := range j.Pipelines {
		if p.Pipeline == "install" {
			return p.Phases
		}
	}
	return nil
}

func (j pvJournal) phase(name string) (pvPhase, bool) {
	for _, p := range j.phases() {
		if p.Name == name {
			return p, true
		}
	}
	return pvPhase{}, false
}

func (j pvJournal) step(name string) (jStep, bool) {
	for _, p := range j.phases() {
		for _, s := range p.Steps {
			if s.Name == name {
				return s, true
			}
		}
	}
	return jStep{}, false
}

// pvPlanDigest is the digest of the fake provider's plan for the fixture installation: the subject of the plan
// confirmation (plan 03 section 14.7: the full digest; D8 is its first 8 hex characters).
func pvPlanDigest(t testing.TB) (full, d8 string) {
	t.Helper()
	p, err := fake.New(map[string]string{"SC_TEST_MODE": "1"}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	plan, err := p.Plan(context.Background(), provider.Desired{Cluster: clusterName, NodeCount: 1})
	if err != nil || plan == nil || !strings.HasPrefix(plan.Digest, "sha256:") || len(plan.Digest) < len("sha256:")+8 {
		t.Fatalf("fake provider plan = %+v, %v", plan, err)
	}
	return plan.Digest, strings.TrimPrefix(plan.Digest, "sha256:")[:8]
}

// unattendedEnv is the runtime-only environment of plan 50 section 9.2: every confirmation and the passphrase file.
// The provider-backstop attestation (PF-29) is by file, as the fixture policy enables a provider that needs it.
func (i *inst) unattendedEnv(extra ...map[string]string) map[string]string {
	i.t.Helper()
	_, d8 := pvPlanDigest(i.t)
	env := merge(i.passphraseFile(), map[string]string{
		"SC_CONFIRM_STORAGE": clusterName, "SC_CONFIRM_PLAN": d8, "SC_CONFIRM_BUDGET": budgetMonthly,
		"SC_CONFIRM_BACKSTOPS": i.backstopFile(),
	})
	return merge(append([]map[string]string{env}, extra...)...)
}

func (i *inst) envOnlyArgs(a ...string) []string {
	return append([]string{"--env-file", i.envFile}, a...)
}

// stepIn finds a step in the named pipeline entry (for example "recover"), where step only reads "install".
func (j pvJournal) stepIn(pipeline, name string) (jStep, bool) {
	for _, p := range j.Pipelines {
		if p.Pipeline != pipeline {
			continue
		}
		for _, ph := range p.Phases {
			for _, s := range ph.Steps {
				if s.Name == name {
					return s, true
				}
			}
		}
	}
	return jStep{}, false
}

func (j pvJournal) confirmation(kinds ...string) (jConfirm, bool) {
	for _, c := range j.Confirmations {
		for _, k := range kinds {
			if c.Kind == k {
				return c, true
			}
		}
	}
	return jConfirm{}, false
}
