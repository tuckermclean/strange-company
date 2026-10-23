package scrub_test

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"strange.company/sc/internal/scrub"
)

// Samples are assembled at run time so the repository secret scan does not trip over this file.
func rep(s string, n int) string { return strings.Repeat(s, n) }

// pemKey builds a PEM private key block with deterministic, seed-unique 64-character body lines.
func pemKey(kind, seed string, lines int) (block string, body []string) {
	var raw []byte
	for i := 0; len(raw) < lines*48; i++ {
		h := sha256.Sum256([]byte(seed + "/" + string(rune('a'+i))))
		raw = append(raw, h[:]...)
	}
	enc := base64.StdEncoding.EncodeToString(raw)
	for i := 0; i < lines; i++ {
		body = append(body, enc[i*64:(i+1)*64])
	}
	name := "PRIVATE KEY"
	if kind != "" {
		name = kind + " PRIVATE KEY"
	}
	return "-----BEGIN " + name + "-----\n" + strings.Join(body, "\n") + "\n-----END " + name + "-----\n", body
}

func jsonEscaped(t *testing.T, s string) string {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(b[1 : len(b)-1])
}

func noBodyLine(t *testing.T, out string, body []string) {
	t.Helper()
	for i, l := range body {
		if strings.Contains(out, l) {
			t.Errorf("body line %d of the private key survived the scrubber:\n%s", i, out)
			return
		}
	}
}

var kinds = []string{"OPENSSH", "RSA", "EC", ""}

// 27-AC-3 (13.4): with the deploy key loaded, a different PEM private key in output is redacted in full; the
// armor lines of the loaded key must not make the pattern miss the other key's body (B1).
func TestScrub_DifferentPEMKeyFullyRedacted_B1(t *testing.T) {
	for _, kind := range kinds {
		t.Run("kind="+kind, func(t *testing.T) {
			deploy, _ := pemKey(kind, "deploy", 6)
			other, body := pemKey(kind, "other", 6)
			sc := scrub.New(deploy)

			raw := sc.String("before\n" + other + "after")
			noBodyLine(t, raw, body)
			if !strings.Contains(raw, "[REDACTED:") {
				t.Errorf("no redaction marker in %q", raw)
			}

			esc := sc.String(`{"stderr":"` + jsonEscaped(t, other) + `"}`)
			noBodyLine(t, esc, body)
		})
	}
}

// 27-AC-3 (13.4): the loaded deploy key itself is redacted in every form: whole, JSON-escaped, indented per
// line, and line by line as a harness stream delivers it. Armor lines are not secret and need not be redacted.
func TestScrub_LoadedDeployKeyRedacted_B1(t *testing.T) {
	deploy, body := pemKey("OPENSSH", "deploy", 6)
	sc := scrub.New(deploy)
	noBodyLine(t, sc.String(deploy), body)
	noBodyLine(t, sc.String("x "+strings.TrimSpace(deploy)+" y"), body)
	noBodyLine(t, sc.String(jsonEscaped(t, deploy)), body)
	var indented []string
	for _, l := range strings.Split(strings.TrimSpace(deploy), "\n") {
		indented = append(indented, "> "+l)
	}
	noBodyLine(t, sc.String(strings.Join(indented, "\n")), body)
	for _, l := range strings.Split(deploy, "\n") {
		noBodyLine(t, sc.String("[stderr] "+l), body)
	}
	if sc.Redactions() == 0 {
		t.Errorf("Redactions() = 0 after scrubbing the deploy key")
	}
}

// 27-AC-3 (13.4): PEM private keys are redacted by pattern with no exact set, also with JSON-escaped newlines.
func TestScrub_PEMPatternWithoutExactSet_13_4(t *testing.T) {
	for _, kind := range kinds {
		other, body := pemKey(kind, "solo", 4)
		sc := scrub.New()
		noBodyLine(t, sc.String("a\n"+other+"b"), body)
		noBodyLine(t, sc.String(jsonEscaped(t, other)), body)
	}
}

var redacted = regexp.MustCompile(`\[REDACTED:[A-Za-z0-9_-]+\]`)

// 27-AC-3 (13.4): each pattern of the shared set replaces a match with [REDACTED:<kind>] and counts it once.
func TestScrub_PatternCorpus_13_4(t *testing.T) {
	samples := map[string]string{
		"sk-ant":     "sk-ant-api03-" + rep("Ab1_-", 6),
		"sk":         "sk-" + rep("Ab1", 10),
		"sk-proj":    "sk-proj-" + rep("Ab1_", 8),
		"jws":        "eyJ" + rep("a", 10) + ".eyJ" + rep("b", 10) + "." + rep("c", 12),
		"ghp":        "ghp_" + rep("A1", 20),
		"gho":        "gho_" + rep("A1", 20),
		"ghu":        "ghu_" + rep("A1", 20),
		"ghs":        "ghs_" + rep("A1", 20),
		"ghr":        "ghr_" + rep("A1", 20),
		"github_pat": "github_pat_" + rep("a1_", 15),
		"AKIA":       "AKIA" + rep("A1", 8),
		"xoxb":       "xoxb-" + "1234567890-abcdefghij",
		"xoxa":       "xoxa-" + "1234567890-abcdefghij",
		"xoxp":       "xoxp-" + "1234567890-abcdefghij",
		"xoxr":       "xoxr-" + "1234567890-abcdefghij",
		"xoxs":       "xoxs-" + "1234567890-abcdefghij",
	}
	for name, secret := range samples {
		t.Run(name, func(t *testing.T) {
			sc := scrub.New()
			out := sc.String(`line: token="` + secret + `" end`)
			if strings.Contains(out, secret) || strings.Contains(out, secret[len(secret)/2:]) {
				t.Errorf("secret survived: %q", out)
			}
			if got := len(redacted.FindAllString(out, -1)); got != 1 {
				t.Errorf("%d [REDACTED:<kind>] markers in %q, want exactly 1", got, out)
			}
			if !strings.HasPrefix(out, `line: token="[REDACTED:`) || !strings.HasSuffix(out, `]" end`) {
				t.Errorf("surrounding text altered: %q", out)
			}
			if n := sc.Redactions(); n != 1 {
				t.Errorf("Redactions() = %d, want 1", n)
			}
		})
	}
}

// 27-AC-3 (13.4): exact-match values are the full file contents (trimmed); every occurrence is replaced and counted.
func TestScrub_ExactValuesAndCount_13_4(t *testing.T) {
	gw := "gw-CANARY-0002-gateway-token-value"
	sc := scrub.New(gw+"\n", "tt-CANARY-0001-task-token-value")
	out := sc.String("Authorization: Bearer " + gw + " and " + gw + "; also tt-CANARY-0001-task-token-value")
	if strings.Contains(out, "CANARY") {
		t.Errorf("exact value survived: %q", out)
	}
	if got := len(redacted.FindAllString(out, -1)); got != 3 {
		t.Errorf("%d markers, want 3: %q", got, out)
	}
	if n := sc.Redactions(); n != 3 {
		t.Errorf("Redactions() = %d, want 3", n)
	}
	sc = sc.AddSecret("env-secret-value-0003")
	if out := sc.String("x env-secret-value-0003"); strings.Contains(out, "env-secret") {
		t.Errorf("AddSecret value survived: %q", out)
	}
	if n := sc.Redactions(); n != 4 {
		t.Errorf("Redactions() after AddSecret = %d, want 4 (count carries over)", n)
	}
}

// 27-AC-3 (13.4), N1: sk-(proj-)?[...]{20,} needs a non-alphanumeric left boundary; ordinary words that end in
// "sk" are not secrets (every redaction becomes SECRET_DETECTED at the Runner).
func TestScrub_SkPatternLeftBoundary_N1(t *testing.T) {
	for _, word := range []string{
		"task-token-missing-detail-abc",
		"risk-assessment-for-the-feature",
		"desk-reference-handbook-volume",
	} {
		t.Run("not_redacted/"+word, func(t *testing.T) {
			sc := scrub.New()
			if out := sc.String("see " + word + " for details"); out != "see "+word+" for details" {
				t.Errorf("ordinary text altered: %q", out)
			}
			if n := sc.Redactions(); n != 0 {
				t.Errorf("Redactions() = %d, want 0", n)
			}
		})
	}
	secret := "sk-" + rep("Ab1", 10)
	for name, text := range map[string]string{
		"start":  secret + " tail",
		"space":  "key " + secret,
		"quote":  `"` + secret + `"`,
		"equals": "OPENAI_KEY=" + secret,
		"colon":  "key:" + secret,
	} {
		t.Run("redacted/"+name, func(t *testing.T) {
			sc := scrub.New()
			out := sc.String(text)
			if strings.Contains(out, secret) || sc.Redactions() != 1 {
				t.Errorf("secret not redacted exactly once: %q (count %d)", out, sc.Redactions())
			}
		})
	}
}

// 27-R-3, N2: Value scrubs strings inside typed containers too, not only string, []any and map[string]any.
func TestScrub_ValueScrubsTypedContainers_N2(t *testing.T) {
	tok := "tt-CANARY-0001-task-token-value"
	type inner struct {
		Detail string   `json:"detail"`
		Tags   []string `json:"tags"`
	}
	type doc struct {
		Title string `json:"title"`
		Inner inner  `json:"inner"`
	}
	cases := map[string]any{
		"[]string":            []string{"a", "leak " + tok},
		"map[string]string":   map[string]string{"k": "leak " + tok},
		"map[string][]string": map[string][]string{"k": {"leak " + tok}},
		"struct":              doc{Title: "t", Inner: inner{Detail: "leak " + tok, Tags: []string{tok}}},
		"*struct":             &doc{Title: tok},
		"[]any with typed":    []any{[]string{tok}, map[string]string{"k": tok}},
	}
	for name, v := range cases {
		t.Run(name, func(t *testing.T) {
			sc := scrub.New(tok)
			got := sc.Value(v)
			b, err := json.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(b), "CANARY") {
				t.Errorf("secret survived in %s", b)
			}
			if sc.Redactions() == 0 {
				t.Errorf("Redactions() = 0")
			}
		})
	}
}
