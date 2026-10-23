package adapters_test

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"

	"strange.company/sc/internal/worker/adapters"
)

// S2-FU2: extra_args cannot smuggle in a flag the adapter itself emits under its kebab-case alias either:
// --allowed-tools / --disallowed-tools are the same options as --allowedTools / --disallowedTools (alone, as
// --flag=value, or with a variadic value list). The adapter's own argv stays exactly as without extra_args.
func TestPrepare_ClaudeCodeExtraArgsKebabAliases_S2FU2(t *testing.T) {
	packet := readFile(t, filepath.Join("testdata", "claude-code"), "packet-tester-extra-args.json")
	withExtra := func(extra []string) []string {
		var m map[string]any
		if err := json.Unmarshal(packet, &m); err != nil {
			t.Fatal(err)
		}
		m["harness"].(map[string]any)["extra_args"] = extra
		b, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		got, err := adapters.Prepare(b)
		if err != nil {
			t.Fatalf("Prepare: %v", err)
		}
		return got.Argv
	}
	base := withExtra([]string{})
	cases := map[string][]string{
		"allowed-tools-value":     {"--allowed-tools", "Edit,Write"},
		"allowed-tools-equals":    {"--allowed-tools=Edit,Write"},
		"allowed-tools-variadic":  {"--allowed-tools", "Read", "Edit"},
		"disallowed-tools-value":  {"--disallowed-tools", "Read"},
		"disallowed-tools-equals": {"--disallowed-tools=Read"},
		"disallowed-variadic":     {"--disallowed-tools", "Read", "Edit"},
	}
	for name, extra := range cases {
		t.Run(name, func(t *testing.T) {
			if got := withExtra(extra); !reflect.DeepEqual(got, base) {
				t.Errorf("argv changed by extra_args %q:\n got %q\nwant %q", extra, got, base)
			}
		})
	}
	t.Run("unrelated-kept-after-alias", func(t *testing.T) {
		got := withExtra([]string{"--allowed-tools", "Edit", "--unrelated-flag"})
		want := append(append([]string{}, base...), "--unrelated-flag")
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %q want %q", got, want)
		}
	})
}
