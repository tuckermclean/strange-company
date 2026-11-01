package ovh_test

import (
	"crypto/sha1" //nolint:gosec // OVH's documented signature algorithm is SHA-1
	"encoding/hex"
	"testing"

	"strange.company/sc/internal/provider/ovh"
)

// Known-answer vectors for the OVH request signature (plan 04 section 22). OVH documents the algorithm,
// "$1$" + hex(sha1(AS+"+"+CK+"+"+METHOD+"+"+QUERY+"+"+BODY+"+"+TSTAMP)), but publishes no worked digest with
// published keys. The digests below were therefore computed from the documented formula with an independent tool
// (printf '%s' "<AS>+<CK>+<METHOD>+<URL>+<BODY>+<TS>" | sha1sum); the keys are invented.
// See test/fixtures/ovh/README.md.
var signVectors = []struct {
	name, method, url, body string
	ts                      int64
	want                    string
}{
	{"get no body", "GET", "https://eu.api.ovh.com/1.0/me", "", 1700000000, "$1$3a5d4e5d89fdfafe4d08c509a6daaa752aeca376"},
	{"post json body", "POST", "https://eu.api.ovh.com/1.0/order/cart", `{"ovhSubsidiary":"FR"}`, 1700000100, "$1$2e30349ce7112698a9accb6cfeab713adbad64c4"},
	{"get with query", "GET", "https://eu.api.ovh.com/1.0/me/order?date.from=2026-11-01T09:00:00%2B01:00", "", 1700000200, "$1$2f1c8b2566c5da2ae8fe6525ac73146315460d5d"},
	{"timestamp 1", "GET", "https://eu.api.ovh.com/1.0/auth/time", "", 1, "$1$5514e8536c6690ffbeb1a60d13d6b7ae14f09329"},
}

// AS in the signature formula is the application secret; the application key is not part of it.
var testCreds = ovh.Credentials{ApplicationKey: "app-key-invented", ApplicationSecret: "EgBjgXkQ0aWoLh9q", ConsumerKey: "Pv3tR8nYc2WzKd6U"}

// hexSHA1 is the independent re-implementation of the documented formula used by the cross-check below.
func hexSHA1(s string) string {
	h := sha1.Sum([]byte(s)) //nolint:gosec
	return hex.EncodeToString(h[:])
}

// 04-AC-3, 04-AC-4, 04-AC-5 (plan 04 section 22): signing known-answer vectors.
func TestSign_KnownAnswerVectors_04AC3(t *testing.T) {
	for _, v := range signVectors {
		t.Run(v.name, func(t *testing.T) {
			if got := ovh.Sign(testCreds, v.method, v.url, v.body, v.ts); got != v.want {
				t.Errorf("Sign(%s %s ts=%d) = %q, want %q", v.method, v.url, v.ts, got, v.want)
			}
		})
	}
}

// The signature input is exactly the five documented parts joined with plus signs.
func TestSign_MatchesDocumentedFormula_04AC3(t *testing.T) {
	c := ovh.Credentials{ApplicationKey: "ak", ApplicationSecret: "EgBjgXkQ0aWoLh9q", ConsumerKey: "ck"}
	want := "$1$" + hexSHA1("EgBjgXkQ0aWoLh9q+ck+DELETE+https://eu.api.ovh.com/1.0/order/cart/abc++1700000300")
	if got := ovh.Sign(c, "DELETE", "https://eu.api.ovh.com/1.0/order/cart/abc", "", 1700000300); got != want {
		t.Errorf("Sign = %q, want %q", got, want)
	}
	if got := ovh.Sign(c, "DELETE", "https://eu.api.ovh.com/1.0/order/cart/abc", "", 1700000301); got == want {
		t.Errorf("Sign ignores the timestamp")
	}
}

// 04-AC-3: the four request headers of plan 04 section 8.
func TestSignedHeaders_FourHeaders_04AC3(t *testing.T) {
	c := ovh.Credentials{ApplicationKey: "ak-invented", ApplicationSecret: "as-invented", ConsumerKey: "ck-invented"}
	h := ovh.SignedHeaders(c, "GET", "https://eu.api.ovh.com/1.0/me", "", 1700000000)
	want := map[string]string{
		"X-Ovh-Application": "ak-invented",
		"X-Ovh-Consumer":    "ck-invented",
		"X-Ovh-Timestamp":   "1700000000",
		"X-Ovh-Signature":   "$1$" + hexSHA1("as-invented+ck-invented+GET+https://eu.api.ovh.com/1.0/me++1700000000"),
	}
	for k, w := range want {
		if got := h.Get(k); got != w {
			t.Errorf("header %s = %q, want %q", k, got, w)
		}
	}
	if h.Get("X-Ovh-Application") == "" {
		t.Errorf("no headers returned")
	}
}
