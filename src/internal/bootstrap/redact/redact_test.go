package redact_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"strange.company/sc/internal/bootstrap/redact"
)

// Plan 50 section 10.2 rule 3: a redacted value appears as «redacted:SC-CLASS» (not scrub's [REDACTED:kind]).
func TestRedactor_MarkerFormat_50AC8(t *testing.T) {
	r := redact.New()
	secret := "fixture-" + "s3-secret-key-0001"
	r.Register("SC-OBJECT-STORAGE", secret)
	got := r.String("before " + secret + " after")
	if got != "before «redacted:SC-OBJECT-STORAGE» after" {
		t.Errorf("String = %q, want the plan 50 §10.2 marker around the surrounding text", got)
	}
	if r.String("nothing secret here") != "nothing secret here" {
		t.Errorf("text without a secret changed")
	}
}

func TestRedactor_ClassPerValueAndLongestFirst_50AC8(t *testing.T) {
	r := redact.New()
	short, long := "abcdefgh-ijklmnop", "abcdefgh-ijklmnop-QRSTUVWX"
	r.Register("SC-A", short)
	r.Register("SC-B", long)
	if got := r.String("x " + long + " y"); got != "x «redacted:SC-B» y" {
		t.Errorf("String = %q, want the longer secret redacted whole as SC-B", got)
	}
	if got := r.String(short); got != "«redacted:SC-A»" {
		t.Errorf("String = %q, want SC-A", got)
	}
}

// The JSON-escaped form of a value is redacted too: a structured log encodes quotes and backslashes.
func TestRedactor_JSONEscapedFormRedacted_50AC8(t *testing.T) {
	r := redact.New()
	secret := `pa"ss\w<or>d&-12345`
	r.Register("SC-DNS-API", secret)
	b, _ := json.Marshal(map[string]string{"msg": "token " + secret})
	got := r.String(string(b))
	enc, _ := json.Marshal(secret)
	if strings.Contains(got, string(enc[1:len(enc)-1])) || strings.Contains(got, secret) {
		t.Errorf("escaped secret survived: %s", got)
	}
	if !strings.Contains(got, "«redacted:SC-DNS-API»") {
		t.Errorf("no marker in %s", got)
	}
}

// A secret split across Write calls is redacted; Close flushes the tail; the sink gets the marker.
func TestRedactor_WriterRedactsSecretSplitAcrossWrites_50AC8(t *testing.T) {
	r := redact.New()
	secret := "fixture-" + "bundle-passphrase-0042"
	r.Register("SC-RECOVERY-PASSPHRASE", secret)
	for cut := 1; cut < len(secret); cut++ {
		var sink bytes.Buffer
		w := r.Writer(&sink)
		if w == nil {
			t.Fatal("Writer returned nil")
		}
		in := "pre " + secret + " post\n"
		split := 4 + cut
		w.Write([]byte(in[:split]))
		w.Write([]byte(in[split:]))
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		if sink.String() != "pre «redacted:SC-RECOVERY-PASSPHRASE» post\n" {
			t.Fatalf("cut %d: sink = %q", cut, sink.String())
		}
	}
}

func TestRedactor_WriterPassesPlainTextAndFlushesOnClose_50AC8(t *testing.T) {
	r := redact.New()
	r.Register("SC-X", "fixture-"+"value-0009")
	var sink bytes.Buffer
	w := r.Writer(&sink)
	if w == nil {
		t.Fatal("Writer returned nil")
	}
	w.Write([]byte("no newline at the end"))
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if sink.String() != "no newline at the end" {
		t.Errorf("sink = %q, want the text unchanged after Close", sink.String())
	}
}
