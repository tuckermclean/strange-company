package identity_test

import (
	"bytes"
	"regexp"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"strange.company/sc/internal/bootstrap/identity"
)

// rawStorage is a storage row read straight from contracts/secret-classes.yaml, independent of identity.LoadRegister.
type rawStorage struct {
	Kind string `yaml:"kind"`
	Path string `yaml:"path"`
}

type rawInstClass struct {
	ID        string       `yaml:"id"`
	Storage   []rawStorage `yaml:"storage"`
	Instances []struct {
		Name    string       `yaml:"name"`
		Storage []rawStorage `yaml:"storage"`
	} `yaml:"instances"`
}

func rawInstClasses(t testing.TB) map[string]rawInstClass {
	t.Helper()
	var doc struct {
		Spec struct {
			Classes []rawInstClass `yaml:"classes"`
		} `yaml:"spec"`
	}
	if err := yaml.Unmarshal(registerBytes(t), &doc); err != nil {
		t.Fatal(err)
	}
	out := map[string]rawInstClass{}
	for _, c := range doc.Spec.Classes {
		out[c.ID] = c
	}
	return out
}

type secretDoc struct {
	Kind     string `yaml:"kind"`
	Metadata struct {
		Name      string `yaml:"name"`
		Namespace string `yaml:"namespace"`
	} `yaml:"metadata"`
}

// renderedDoc finds the Secret manifest the encryptor was handed for one output path. The fake encryptor returns a
// digest only, so the test maps plaintext by the order RenderSOPSReport encrypts (path order) via the file list.
func renderedDocs(t *testing.T, set *identity.Set) (*identity.RenderReport, map[string]secretDoc) {
	t.Helper()
	var calls []encCall
	rep, err := identity.RenderSOPSReport(cluster, set, rcp(set), fakeEnc(&calls))
	if err != nil || rep == nil {
		t.Fatalf("RenderSOPSReport: %v", err)
	}
	if len(calls) != len(rep.Files) {
		t.Fatalf("%d encryptor calls for %d files", len(calls), len(rep.Files))
	}
	docs := map[string]secretDoc{}
	for i, f := range rep.Files {
		var d secretDoc
		if err := yaml.Unmarshal(calls[i].plain, &d); err != nil {
			t.Fatalf("%s: plaintext is not YAML: %v", f.Path, err)
		}
		docs[f.Path] = d
	}
	return rep, docs
}

// Plan 35 register: an instance's own storage rows replace the class rows for that instance.
func TestRenderSOPS_InstanceStorageReplacesClassRows_50AC10(t *testing.T) {
	set := genSet(t)
	if set.Find("SC-PG-APP", "forgejo-pg-ro") == nil || set.Find("SC-PG-APP", "sc-pg-grafana-ro") == nil {
		t.Skip("the generated set has no SC-PG-APP read-only instances")
	}
	rep, docs := renderedDocs(t, set)
	base := "gitops/clusters/" + cluster + "/secrets/"
	for path, want := range map[string]struct{ name, ns string }{
		base + "forgejo/forgejo-pg-ro.sops.yaml":            {"forgejo-pg-ro", "forgejo"},
		base + "strange-company/sc-pg-grafana-ro.sops.yaml": {"sc-pg-grafana-ro", "strange-company"},
		base + "monitoring/sc-pg-grafana-ro.sops.yaml":      {"sc-pg-grafana-ro", "monitoring"},
	} {
		d, ok := docs[path]
		if !ok {
			t.Errorf("%s not rendered", path)
			continue
		}
		if d.Kind != "Secret" || d.Metadata.Name != want.name || d.Metadata.Namespace != want.ns {
			t.Errorf("%s is %s %s/%s, want Secret %s/%s", path, d.Kind, d.Metadata.Namespace, d.Metadata.Name, want.ns, want.name)
		}
	}
	for _, f := range rep.Files {
		if strings.Contains(f.Path, "-ro-app.sops.yaml") || strings.Contains(f.Path, "grafana-ro-app") {
			t.Errorf("%s: a class row was applied to an instance that has its own storage", f.Path)
		}
	}
}

// Completeness per instance, computed from the contract: every sops-git and offcluster-sops row that applies to a
// generated secret (instance rows if the instance has any, else class rows) is rendered or reported as deferred.
func TestRenderSOPS_EveryApplicableRowRenderedOrDeferred_50AC10(t *testing.T) {
	set := genSet(t)
	rep, _ := renderedDocs(t, set)
	files := map[string]bool{}
	for _, f := range rep.Files {
		files[f.Path] = true
	}
	raw := rawInstClasses(t)
	for _, s := range set.Secrets {
		c, ok := raw[s.Class]
		if !ok {
			continue
		}
		rows := c.Storage
		for _, in := range c.Instances {
			if in.Name == s.Instance && len(in.Storage) > 0 {
				rows = in.Storage
			}
		}
		for _, row := range rows {
			if row.Kind != "sops-git" && row.Kind != "offcluster-sops" {
				continue
			}
			p := strings.ReplaceAll(strings.ReplaceAll(row.Path, "{cluster}", cluster), "<instance>", s.Instance)
			open := strings.ContainsAny(p, "<>")
			var re *regexp.Regexp
			if open {
				re = regexp.MustCompile("^" + regexp.MustCompile(`<[^>]*>`).ReplaceAllString(regexp.QuoteMeta(p), "[^/]+") + "$")
			}
			found := false
			for fp := range files {
				if (!open && fp == p) || (open && re.MatchString(fp)) {
					found = true
				}
			}
			for _, d := range rep.Deferred {
				if d.Class == s.Class && d.Instance == s.Instance && (open || d.Path == p || d.Path == row.Path) {
					found = true
				}
			}
			if !found {
				t.Errorf("%s/%s: row %s (%s) is neither rendered nor deferred", s.Class, s.Instance, row.Path, row.Kind)
			}
		}
	}
}

// Plan 35 section 14.2 reading: SOPS takes the first matching creation rule (Go regexp), so a new cluster rule must
// precede every existing rule that matches a path of the cluster, however that rule is spelled. A rule that does not
// match the cluster's paths is not moved: it keeps its position relative to the other existing rules.
func TestSOPSRules_NewRulesPrecedeAnyBroadMatchingRule_50AC10(t *testing.T) {
	set := genSet(t)
	narrow := `^secrets/ci/other/[a-z0-9.-]+\.sops\.yaml$`
	samples := []string{
		"gitops/clusters/" + cluster + "/secrets/forgejo/forgejo-pg-ro.sops.yaml",
		"secrets/offcluster/" + cluster + "/k3s-token.sops.yaml",
	}
	for _, broad := range []string{`^gitops/.*\.sops\.yaml$`, `^.+\.sops\.yaml$`} {
		existing := []byte("creation_rules:\n  - path_regex: '" + narrow + "'\n    age: age1aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n" +
			"  - path_regex: '" + broad + "'\n    age: age1bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\n" +
			"  - path_regex: '" + broad + "'\n    age: age1ccccccccccccccccccccccccccccccccccccccccccccccccccccccccc\n")
		existing = existing[:bytes.LastIndex(existing, []byte("  - path_regex"))]
		out, err := identity.SOPSRules(cluster, rcp(set), existing)
		if err != nil {
			t.Fatalf("%s: %v", broad, err)
		}
		rules := fullRules(t, out)
		idx := func(match func(fullRule) bool) int {
			for i, r := range rules {
				if match(r) {
					return i
				}
			}
			return -1
		}
		broadAt := idx(func(r fullRule) bool { return r.PathRegex == broad })
		narrowAt := idx(func(r fullRule) bool { return r.PathRegex == narrow })
		if broadAt < 0 || narrowAt < 0 {
			t.Fatalf("%s: an existing rule was lost:\n%s", broad, out)
		}
		for _, sample := range samples {
			// The first rule that matches the sample must be one of the cluster's own.
			for _, r := range rules {
				if regexp.MustCompile(r.PathRegex).MatchString(sample) {
					if r.PathRegex == broad || r.PathRegex == narrow {
						t.Errorf("%s: %s is matched first by the existing rule %s, not by a cluster rule:\n%s", broad, sample, r.PathRegex, out)
					}
					break
				}
			}
		}
		if narrowAt > broadAt {
			t.Errorf("%s: the unrelated narrow rule moved behind the broad rule:\n%s", broad, out)
		}
		again, err := identity.SOPSRules(cluster, rcp(set), out)
		if err != nil || !bytes.Equal(again, out) {
			t.Errorf("%s: a second application changed .sops.yaml (%v)", broad, err)
		}
	}
}
