package cli_test

import (
	"strings"
	"testing"

	"strange.company/sc/internal/bootstrap/cli"
)

// S9 review 1 N1 (50-AC-13): an optional-value flag is validated in the `--flag=value` form as well as in the
// `--flag value` form. A value outside the allowed set is rejected as an invalid value; an allowed one parses.
func TestParse_OptionalValueFlagEqualsFormIsValidated_N1(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		args    []string
		wantErr bool
	}{
		{[]string{"rotate", "secret", "SC-X", "--stage=bogus"}, true},
		{[]string{"rotate", "secret", "SC-X", "--stage=add"}, false},
		{[]string{"rotate", "secret", "SC-X", "--stage=switch"}, false},
		{[]string{"rotate", "secret", "SC-X", "--stage=retire"}, false},
		{[]string{"supply-chain", "rotate-signing-key", "--stage=switch"}, true},
		{[]string{"supply-chain", "rotate-signing-key", "--stage=add"}, false},
	} {
		_, err := cli.Parse(tc.args)
		switch {
		case tc.wantErr && err == nil:
			t.Errorf("%s: parsed, want an invalid-value error for --stage", strings.Join(tc.args, " "))
		case tc.wantErr && !strings.Contains(err.Error(), "stage"):
			t.Errorf("%s: error %q does not name the flag --stage", strings.Join(tc.args, " "), err)
		case !tc.wantErr && err != nil:
			t.Errorf("%s: %v, want it to parse", strings.Join(tc.args, " "), err)
		}
	}
}
