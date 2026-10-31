package config_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"strange.company/sc/internal/bootstrap/config"
)

func dropKey(example []byte, key string) []byte {
	re := regexp.MustCompile(`(^|[^A-Z0-9_])` + regexp.QuoteMeta(key) + `([^A-Z0-9_]|$)`)
	var keep []string
	for _, line := range strings.Split(string(example), "\n") {
		if !re.MatchString(line) {
			keep = append(keep, line)
		}
	}
	return []byte(strings.Join(keep, "\n"))
}

// 50-AC-12: the example generated from the table is consistent with the table.
func TestConfigDocs_GeneratedExampleIsConsistent_50AC12(t *testing.T) {
	ex := config.ExampleEnv()
	if len(ex) == 0 {
		t.Fatal("ExampleEnv() is empty")
	}
	if problems := config.CheckExample(ex); len(problems) != 0 {
		t.Errorf("the generated example fails its own check: %v", problems)
	}
	for _, r := range planRows(t) {
		if !strings.Contains(string(ex), r.first) {
			t.Errorf("generated example does not document %s", r.first)
		}
	}
}

// 50-AC-12: the committed bootstrap.env.example matches section 9.2 (what `make config-docs-check` runs).
func TestConfigDocs_CommittedExampleMatches_50AC12(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(repoRoot(t), "bootstrap.env.example"))
	if err != nil {
		t.Fatalf("bootstrap.env.example: %v", err)
	}
	if problems := config.CheckExample(b); len(problems) != 0 {
		t.Errorf("bootstrap.env.example drifts from plan 50 section 9.2: %v", problems)
	}
}

// 50-AC-12: dropping any key from the example makes the check fail; so does an unknown key.
func TestConfigDocs_DroppingAKeyFails_50AC12(t *testing.T) {
	ex := config.ExampleEnv()
	for _, r := range planRows(t) {
		t.Run(r.first, func(t *testing.T) {
			mut := dropKey(ex, r.first)
			if string(mut) == string(ex) {
				t.Fatalf("%s is not in the generated example; the mutation changed nothing", r.first)
			}
			if problems := config.CheckExample(mut); len(problems) == 0 {
				t.Errorf("CheckExample accepted an example without %s", r.first)
			}
		})
	}
	t.Run("unknown-key", func(t *testing.T) {
		if problems := config.CheckExample(append(append([]byte{}, ex...), []byte("\nBOGUS_KEY=\n")...)); len(problems) == 0 {
			t.Error("CheckExample accepted an example with an undocumented key")
		}
	})
}

// 50-AC-12: `make config-docs-check` exists.
func TestConfigDocs_MakeTargetWired_50AC12(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join(repoRoot(t), "mk", "*.mk"))
	found := false
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if regexp.MustCompile(`(?m)^config-docs-check:`).Match(b) {
			found = true
		}
	}
	if !found {
		t.Error("no mk/*.mk defines the config-docs-check target")
	}
}
