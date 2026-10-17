package lint_test

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"go.yaml.in/yaml/v3"
)

// 23-AC-28: LICENSE is the verbatim GNU AGPL v3 text (gnu.org copy).
func TestLicenseFile_Sha256_23AC28(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(repoRoot(t), "LICENSE"))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	const want = "0d96a4ff68ad6d4b6f1f30f713b18d5184912ba8dd389f86aa7710db079abcb0"
	if got := hex.EncodeToString(sum[:]); got != want {
		t.Errorf("LICENSE sha256 = %s, want %s", got, want)
	}
}

// 23-AC-28 policy-file shape test (plan 23 section 9 row, section 22): projectLicense and the exact lists.
func TestLicensePolicy_Shape_23AC28(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(repoRoot(t), "security", "license-policy.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		APIVersion string `yaml:"apiVersion"`
		Kind       string `yaml:"kind"`
		Spec       struct {
			ProjectLicense string   `yaml:"projectLicense"`
			Allow          []string `yaml:"allow"`
			Deny           []string `yaml:"deny"`
			Exceptions     []any    `yaml:"exceptions"`
		} `yaml:"spec"`
	}
	if err := yaml.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.APIVersion != "strange.company/v1alpha1" || doc.Kind != "LicensePolicy" {
		t.Errorf("apiVersion/kind = %q/%q", doc.APIVersion, doc.Kind)
	}
	if doc.Spec.ProjectLicense != "AGPL-3.0-or-later" {
		t.Errorf("projectLicense = %q", doc.Spec.ProjectLicense)
	}
	allow := []string{"MIT", "BSD-2-Clause", "BSD-3-Clause", "ISC", "0BSD", "Zlib", "Apache-2.0", "MPL-2.0", "CC0-1.0", "Unlicense",
		"LGPL-2.1-or-later", "LGPL-3.0-or-later", "GPL-2.0-or-later", "GPL-3.0-or-later", "AGPL-3.0-or-later"}
	deny := []string{"GPL-2.0-only", "LGPL-2.1-only", "SSPL-1.0", "BUSL-1.1", "Elastic-2.0", "Commons-Clause", "CC-BY-NC-4.0", "CC-BY-NC-SA-4.0"}
	if !reflect.DeepEqual(doc.Spec.Allow, allow) {
		t.Errorf("allow = %v, want %v", doc.Spec.Allow, allow)
	}
	if !reflect.DeepEqual(doc.Spec.Deny, deny) {
		t.Errorf("deny = %v, want %v", doc.Spec.Deny, deny)
	}
	if len(doc.Spec.Exceptions) != 0 {
		t.Errorf("exceptions = %v, want none", doc.Spec.Exceptions)
	}
}
