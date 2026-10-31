package identity_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"

	"strange.company/sc/internal/bootstrap/identity"
	"strange.company/sc/internal/clock"
)

const cluster = "lab-one"

var recipientRe = regexp.MustCompile(`^age1[02-9ac-hj-np-z]{58}$`)

func repoRoot(t testing.TB) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "plans", "50-bootstrap-orchestrator.md")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("repository root not found")
		}
		dir = parent
	}
}

func registerBytes(t testing.TB) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot(t), "contracts", "secret-classes.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// rawClass is the part of a register row the tests read themselves, independent of identity.LoadRegister.
type rawClass struct {
	ID               string `yaml:"id"`
	InRecoveryBundle bool   `yaml:"inRecoveryBundle"`
	Creator          struct {
		Actor string `yaml:"actor"`
	} `yaml:"creator"`
	Instances []struct {
		Name             string `yaml:"name"`
		InRecoveryBundle *bool  `yaml:"inRecoveryBundle"`
	} `yaml:"instances"`
}

func rawClasses(t testing.TB) []rawClass {
	t.Helper()
	var doc struct {
		Spec struct {
			Classes []rawClass `yaml:"classes"`
		} `yaml:"spec"`
	}
	if err := yaml.Unmarshal(registerBytes(t), &doc); err != nil {
		t.Fatal(err)
	}
	return doc.Spec.Classes
}

// generatedClasses is the set plan 35 assigns to plan.install-identity: the register rows whose creator actor
// names plan.install-identity or a pre-seed, plus SC-INTERNAL-CA (`secrets gen internal-ca`, when
// plan.install-identity).
func generatedClasses(t testing.TB) map[string]rawClass {
	t.Helper()
	out := map[string]rawClass{}
	for _, c := range rawClasses(t) {
		a := strings.ToLower(c.Creator.Actor)
		if strings.Contains(a, "plan.install-identity") || strings.Contains(a, "pre-seed") || c.ID == "SC-INTERNAL-CA" {
			out[c.ID] = c
		}
	}
	return out
}

func loadReg(t testing.TB) *identity.Register {
	t.Helper()
	reg, err := identity.LoadRegister(registerBytes(t))
	if err != nil {
		t.Fatalf("LoadRegister: %v", err)
	}
	if reg == nil {
		t.Fatal("LoadRegister returned no register")
	}
	return reg
}

func genSet(t testing.TB) *identity.Set {
	t.Helper()
	set, err := identity.Generate(loadReg(t), cluster)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if set == nil {
		t.Fatal("Generate returned no set")
	}
	return set
}

// A passphrase that satisfies the policy. It is a test value, not a secret.
const goodPass = "correct horse battery staple lab"

var (
	sealOnce  sync.Once
	sealedSet *identity.Set
	sealedBun *identity.Sealed
	sealErr   error
)

// sealedV1 seals one bundle for the whole package at work factor 18 (scrypt is slow; the contract minimum).
func sealedV1(t testing.TB) (*identity.Set, *identity.Sealed) {
	t.Helper()
	sealOnce.Do(func() {
		reg, err := identity.LoadRegister(registerBytes(t))
		if err != nil || reg == nil {
			sealErr = err
			return
		}
		set, err := identity.Generate(reg, cluster)
		if err != nil || set == nil {
			sealErr = err
			return
		}
		set.Secrets = append(set.Secrets, identity.Secret{
			Class: "SC-INFRA-PROVIDER", Format: "env", Value: []byte("OS_TOKEN=operator-supplied-value-0001\n"), InBundle: true,
		})
		b, err := identity.Seal(identity.SealInput{
			Cluster: cluster, Version: 1, Passphrase: goodPass, Set: set,
			Clock:       clock.NewFake(time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC)),
			ToolVersion: "0.1.0", ToolboxImage: "local-build:sha256:" + strings.Repeat("a", 64),
			SourceCommit: strings.Repeat("b", 40), Trigger: "initial-bootstrap", WorkFactor: 18,
		})
		sealedSet, sealedBun, sealErr = set, b, err
	})
	if sealErr != nil {
		t.Fatalf("Seal: %v", sealErr)
	}
	if sealedBun == nil || sealedSet == nil {
		t.Fatal("Seal returned no bundle")
	}
	return sealedSet, sealedBun
}
