package config_test

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"strange.company/sc/internal/bootstrap/config"
	"strange.company/sc/internal/contracts"
)

func loadEnv(t *testing.T, path string) map[string]string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	env, err := config.ParseEnv(b)
	if err != nil {
		t.Fatalf("ParseEnv(%s): %v", path, err)
	}
	if len(env) == 0 {
		t.Fatalf("ParseEnv(%s) returned no variables", path)
	}
	return env
}

func configSchema(t *testing.T) interface{ Validate(any) error } {
	t.Helper()
	schemas, err := contracts.CompileSchemas(filepath.Join(repoRoot(t), "contracts"))
	if err != nil {
		t.Fatal(err)
	}
	s := schemas["bootstrap-config"]
	if s == nil {
		t.Fatal("no bootstrap-config schema")
	}
	return s
}

// 50-AC-3: every valid golden env file normalizes to output that validates against the schema and contains none
// of the fixture secret values (grep -F semantics).
func TestValidFixtures_SchemaAndNoSecrets_50AC3(t *testing.T) {
	files, err := filepath.Glob(filepath.Join(repoRoot(t), "test", "fixtures", "bootstrap", "valid", "*.env"))
	if err != nil || len(files) < 3 {
		t.Fatalf("want at least 3 valid fixtures, got %v (err %v)", files, err)
	}
	schema := configSchema(t)
	for _, f := range files {
		t.Run(filepath.Base(f), func(t *testing.T) {
			env := loadEnv(t, f)
			res := mustNormalize(t, env, "")
			doc, err := contracts.ParseYAML(res.YAML)
			if err != nil {
				t.Fatalf("normalized output is not YAML: %v", err)
			}
			if err := schema.Validate(doc); err != nil {
				t.Errorf("normalized output violates contracts/bootstrap-config.schema.yaml: %v", err)
			}
			secrets := 0
			for k, v := range env {
				if !strings.HasPrefix(v, "fixture-secret-") {
					continue
				}
				secrets++
				if strings.Contains(string(res.YAML), v) {
					t.Errorf("normalized output contains the fixture secret value of %s", k)
				}
			}
			if secrets == 0 {
				t.Errorf("fixture %s carries no fixture-secret-* values; the grep -F check would be vacuous", f)
			}
		})
	}
}

// 50-AC-3: the generated metadata, which the fixtures do not supply.
func TestNormalize_Metadata_50AC3(t *testing.T) {
	res := mustNormalize(t, baseExisting(), "")
	if got := get(res.Doc, "/apiVersion"); got != "strange.company/v1alpha1" {
		t.Errorf("apiVersion = %q", got)
	}
	if got := get(res.Doc, "/kind"); got != "BootstrapConfig" {
		t.Errorf("kind = %q", got)
	}
	if got := get(res.Doc, "/metadata/installUid"); got != fixtureUID {
		t.Errorf("installUid = %q, want the Options value", got)
	}
	if !regexp.MustCompile(`^sha256:[0-9a-f]{64}$`).MatchString(get(res.Doc, "/metadata/envDigest")) {
		t.Errorf("envDigest = %q", get(res.Doc, "/metadata/envDigest"))
	}
	if got := get(res.Doc, "/metadata/generatedAt"); got != "2026-10-26T12:00:00Z" {
		t.Errorf("generatedAt = %q, want the injected clock's instant", got)
	}
}

// 50-AC-3: the digest covers the non-secret set only (plan 50 envDigest): a secret change leaves it alone, a
// non-secret change moves it.
func TestNormalize_EnvDigestExcludesSecrets_50AC3(t *testing.T) {
	a := mustNormalize(t, baseExisting(), "")
	b := mustNormalize(t, merge(baseExisting(), m{"S3_SECRET_ACCESS_KEY": "fixture-secret-changed"}, nil), "")
	c := mustNormalize(t, merge(baseExisting(), m{"S3_REGION": "eu-west-3"}, nil), "")
	da, db, dc := get(a.Doc, "/metadata/envDigest"), get(b.Doc, "/metadata/envDigest"), get(c.Doc, "/metadata/envDigest")
	if da != db {
		t.Errorf("a secret value changed envDigest: %s vs %s", da, db)
	}
	if da == dc {
		t.Errorf("a non-secret change left envDigest at %s", da)
	}
}

// 50-AC-7: SC_TEST_CRASH_AT together with provider ovh is refused with exit 2 TEST_MODE_REFUSED_PAID.
func TestNormalize_CrashAtWithOVH_Refused_50AC7(t *testing.T) {
	env := loadEnv(t, filepath.Join(repoRoot(t), "test", "fixtures", "bootstrap", "invalid", "ovh-with-crash-at.env"))
	if env["PROVIDER"] != "ovh" || env["SC_TEST_CRASH_AT"] == "" {
		t.Fatalf("fixture must set PROVIDER=ovh and SC_TEST_CRASH_AT, got %q %q", env["PROVIDER"], env["SC_TEST_CRASH_AT"])
	}
	res, err := config.Normalize(env, opts(""))
	var ce *config.Error
	if !errors.As(err, &ce) {
		t.Fatalf("want a *config.Error, got res=%v err=%v", res, err)
	}
	if ce.ExitCode != 2 || ce.Code != "TEST_MODE_REFUSED_PAID" {
		t.Errorf("got exit %d code %q, want exit 2 TEST_MODE_REFUSED_PAID", ce.ExitCode, ce.Code)
	}
}

// 50-AC-7: SC_TEST_CRASH_AT alone (no SC_TEST_MODE) is also refused for ovh; plan 50 section 9.2 refuses every
// test-only runtime variable when the provider is paid.
func TestNormalize_TestOnlyVariablesWithOVH_Refused_50AC7(t *testing.T) {
	for _, v := range []string{"SC_TEST_CRASH_AT", "SC_TEST_MODE", "SC_TEST_STOP_AFTER", "SC_LAB_S3_INCLUSTER_ENDPOINT", "SC_LAB_S3_CA_FILE"} {
		t.Run(v, func(t *testing.T) {
			res, err := config.Normalize(merge(baseOVH(), m{v: "1"}, nil), opts(""))
			var ce *config.Error
			if !errors.As(err, &ce) {
				t.Fatalf("want a *config.Error, got res=%v err=%v", res, err)
			}
			if ce.ExitCode != 2 || ce.Code != "TEST_MODE_REFUSED_PAID" {
				t.Errorf("got exit %d code %q, want exit 2 TEST_MODE_REFUSED_PAID", ce.ExitCode, ce.Code)
			}
		})
	}
}

// 50-AC-7: the refusal is specific to paid providers; an existing-node install may use the crash hook.
func TestNormalize_CrashAtWithExisting_Allowed_50AC7(t *testing.T) {
	mustNormalize(t, merge(baseExisting(), m{"SC_TEST_MODE": "1", "SC_TEST_CRASH_AT": "plan.storage-apply:after"}, nil), "")
}

// 50-AC-1: the invalid fixture with missing required variables is a configuration error (exit 2).
func TestNormalize_MissingRequiredFixture_50AC1(t *testing.T) {
	env := loadEnv(t, filepath.Join(repoRoot(t), "test", "fixtures", "bootstrap", "invalid", "missing-required.env"))
	res, err := config.Normalize(env, opts(""))
	var ce *config.Error
	if !errors.As(err, &ce) {
		t.Fatalf("want a *config.Error, got res=%v err=%v", res, err)
	}
	if ce.ExitCode != 2 {
		t.Errorf("exit code %d, want 2", ce.ExitCode)
	}
}
