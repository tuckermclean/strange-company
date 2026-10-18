package toolbox_test

import (
	"strings"
	"testing"
)

// F03 S3 review round 2, N3: the Go parser trims exactly the characters the wrapper's sed trims in the C locale,
// " \t\r\n\v\f" (CR only as the line ending). Unicode spaces (U+00A0, U+0085, U+2003) are part of the value.
// Observed through `env s3`.

func TestEnvFile_TrimsOnlyASCIIWhitespace_Plan02S14_2_N3(t *testing.T) {
	for _, tc := range []struct{ name, sp string }{
		{"NBSP", " "},
		{"NEL", "\u0085"},
		{"EM SPACE", " "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, d := s3Run(t, "S3_ENDPOINT="+tc.sp+"http://x.invalid"+tc.sp+"\n"+s3Base)
			if r.code != 0 {
				t.Fatalf("exit %d: %q", r.code, r.all())
			}
			want := "endpoint_url = " + tc.sp + "http://x.invalid" + tc.sp + "\n"
			if cfg := readAws(t, d, "config"); !strings.Contains(cfg, want) {
				t.Fatalf("a %s around the value is part of it; want %q in %q", tc.name, want, cfg)
			}
		})
	}
}

// The ASCII set is still trimmed on both sides: space, tab, VT, FF.
func TestEnvFile_TrimsASCIIWhitespaceBothSides_Plan02S14_2_N3(t *testing.T) {
	r, d := s3Run(t, "S3_ENDPOINT= \t\v\fhttp://x.invalid \t\v\f\n"+s3Base)
	if r.code != 0 {
		t.Fatalf("exit %d: %q", r.code, r.all())
	}
	if cfg := readAws(t, d, "config"); !strings.Contains(cfg, "endpoint_url = http://x.invalid\n") {
		t.Fatalf("ASCII whitespace must be trimmed on both sides: %q", cfg)
	}
}
