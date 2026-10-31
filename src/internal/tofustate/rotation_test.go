package tofustate_test

import (
	"sort"
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"

	"strange.company/sc/internal/tofustate"
)

// Stable key provider names (plan 06 sections 7.3 and 20.3 as edited in bba6ad9, 06-AC-6): the provider and method of a
// passphrase are named p<N> after the passphrase's generation, never renamed. During a rotation TF_ENCRYPTION carries
// p<N> and p<N-1>, with a fallback to p<N-1> in both the state and the plan block. A Generation of 0 keeps the
// single-passphrase form of the S4 golden file (name `current`).

func parseEncryption(t *testing.T, src string) *hclsyntax.Body {
	t.Helper()
	f, diags := hclsyntax.ParseConfig([]byte(src), "TF_ENCRYPTION", hcl.InitialPos)
	if diags.HasErrors() {
		t.Fatalf("the rendered TF_ENCRYPTION does not parse: %v\n%s", diags, src)
	}
	return f.Body.(*hclsyntax.Body)
}

func exprText(src string, a *hclsyntax.Attribute) string {
	r := a.Expr.Range()
	return strings.TrimSpace(src[r.Start.Byte:r.End.Byte])
}

func blocksOf(b *hclsyntax.Body, typ string) []*hclsyntax.Block {
	var out []*hclsyntax.Block
	for _, bl := range b.Blocks {
		if bl.Type == typ {
			out = append(out, bl)
		}
	}
	return out
}

// labelsOf is "type/name" of every block of a type, sorted.
func labelsOf(b *hclsyntax.Body, typ string) []string {
	var out []string
	for _, bl := range blocksOf(b, typ) {
		out = append(out, strings.Join(bl.Labels, "/"))
	}
	sort.Strings(out)
	return out
}

func blockWith(t *testing.T, b *hclsyntax.Body, typ string, labels ...string) *hclsyntax.Block {
	t.Helper()
	for _, bl := range blocksOf(b, typ) {
		if strings.Join(bl.Labels, "/") == strings.Join(labels, "/") {
			return bl
		}
	}
	t.Fatalf("no %s block %v in %v", typ, labels, labelsOf(b, typ))
	return nil
}

func attr(t *testing.T, src string, bl *hclsyntax.Block, name string) string {
	t.Helper()
	a, ok := bl.Body.Attributes[name]
	if !ok {
		t.Fatalf("%s %v has no attribute %q", bl.Type, bl.Labels, name)
	}
	return exprText(src, a)
}

func quoted(s string) string { return `"` + s + `"` }

func eqStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// 06-AC-6 (render): a single passphrase of generation N is the provider and method p<N>, enforced for state and plan, with
// no fallback and no `current`/`previous` names.
func TestRenderEncryption_NamedByGeneration_06AC6(t *testing.T) {
	for _, gen := range []int{1, 2, 7} {
		p := passphrase(byte(20 + gen))
		src, err := tofustate.RenderEncryption(tofustate.Encryption{Current: p, Generation: gen})
		if err != nil {
			t.Fatalf("generation %d: RenderEncryption: %v", gen, err)
		}
		name := "p" + string(rune('0'+gen))
		body := parseEncryption(t, src)
		if got := labelsOf(body, "key_provider"); !eqStrings(got, []string{"pbkdf2/" + name}) {
			t.Errorf("generation %d: key providers %v, want [pbkdf2/%s]", gen, got, name)
		}
		if got := labelsOf(body, "method"); !eqStrings(got, []string{"aes_gcm/" + name}) {
			t.Errorf("generation %d: methods %v, want [aes_gcm/%s]", gen, got, name)
		}
		kp := blockWith(t, body, "key_provider", "pbkdf2", name)
		if got := attr(t, src, kp, "passphrase"); got != quoted(p) {
			t.Errorf("generation %d: passphrase attribute %s, want the generation's passphrase", gen, got)
		}
		m := blockWith(t, body, "method", "aes_gcm", name)
		if got := attr(t, src, m, "keys"); got != "key_provider.pbkdf2."+name {
			t.Errorf("generation %d: method keys = %s", gen, got)
		}
		for _, blk := range []string{"state", "plan"} {
			bl := blocksOf(body, blk)
			if len(bl) != 1 {
				t.Fatalf("generation %d: %d %s blocks, want 1", gen, len(bl), blk)
			}
			if got := attr(t, src, bl[0], "method"); got != "method.aes_gcm."+name {
				t.Errorf("generation %d: %s method = %s, want method.aes_gcm.%s", gen, blk, got, name)
			}
			if got := attr(t, src, bl[0], "enforced"); got != "true" {
				t.Errorf("generation %d: %s enforced = %s, want true", gen, blk, got)
			}
			if len(bl[0].Body.Blocks) != 0 {
				t.Errorf("generation %d: %s block has nested blocks (a fallback) without a previous passphrase", gen, blk)
			}
		}
		for _, bad := range []string{`"current"`, `"previous"`, "fallback"} {
			if strings.Contains(src, bad) {
				t.Errorf("generation %d: the rendered encryption contains %s:\n%s", gen, bad, src)
			}
		}
	}
}

// 06-AC-6 (render, section 20.3 step 2): during a rotation P2 (p<N+1>) is enforced and P1 (p<N>, name unchanged) is the
// fallback of state and plan.
func TestRenderEncryption_RotationFallsBackToPreviousName_06AC6(t *testing.T) {
	p1, p2 := passphrase(3), passphrase(11)
	src, err := tofustate.RenderEncryption(tofustate.Encryption{Current: p2, Generation: 2, Previous: p1})
	if err != nil {
		t.Fatalf("RenderEncryption: %v", err)
	}
	body := parseEncryption(t, src)
	if got := labelsOf(body, "key_provider"); !eqStrings(got, []string{"pbkdf2/p1", "pbkdf2/p2"}) {
		t.Errorf("key providers %v, want [pbkdf2/p1 pbkdf2/p2]", got)
	}
	if got := labelsOf(body, "method"); !eqStrings(got, []string{"aes_gcm/p1", "aes_gcm/p2"}) {
		t.Errorf("methods %v, want [aes_gcm/p1 aes_gcm/p2]", got)
	}
	if got := attr(t, src, blockWith(t, body, "key_provider", "pbkdf2", "p1"), "passphrase"); got != quoted(p1) {
		t.Errorf("p1 passphrase attribute %s, want the previous passphrase", got)
	}
	if got := attr(t, src, blockWith(t, body, "key_provider", "pbkdf2", "p2"), "passphrase"); got != quoted(p2) {
		t.Errorf("p2 passphrase attribute %s, want the current passphrase", got)
	}
	if got := attr(t, src, blockWith(t, body, "method", "aes_gcm", "p1"), "keys"); got != "key_provider.pbkdf2.p1" {
		t.Errorf("method p1 keys = %s", got)
	}
	if got := attr(t, src, blockWith(t, body, "method", "aes_gcm", "p2"), "keys"); got != "key_provider.pbkdf2.p2" {
		t.Errorf("method p2 keys = %s", got)
	}
	for _, blk := range []string{"state", "plan"} {
		bl := blocksOf(body, blk)
		if len(bl) != 1 {
			t.Fatalf("%d %s blocks, want 1", len(bl), blk)
		}
		if got := attr(t, src, bl[0], "method"); got != "method.aes_gcm.p2" {
			t.Errorf("%s method = %s, want method.aes_gcm.p2", blk, got)
		}
		if got := attr(t, src, bl[0], "enforced"); got != "true" {
			t.Errorf("%s enforced = %s, want true", blk, got)
		}
		fb := blocksOf(bl[0].Body, "fallback")
		if len(fb) != 1 {
			t.Fatalf("%s has %d fallback blocks, want 1", blk, len(fb))
		}
		if got := attr(t, src, fb[0], "method"); got != "method.aes_gcm.p1" {
			t.Errorf("%s fallback method = %s, want method.aes_gcm.p1", blk, got)
		}
	}
}

// 06-AC-6 (render): the names follow the generation, not the position: generation 3 with a previous passphrase is p3 with
// fallback p2.
func TestRenderEncryption_NamesDoNotShift_06AC6(t *testing.T) {
	p2, p3 := passphrase(11), passphrase(17)
	src, err := tofustate.RenderEncryption(tofustate.Encryption{Current: p3, Generation: 3, Previous: p2})
	if err != nil {
		t.Fatalf("RenderEncryption: %v", err)
	}
	body := parseEncryption(t, src)
	if got := labelsOf(body, "key_provider"); !eqStrings(got, []string{"pbkdf2/p2", "pbkdf2/p3"}) {
		t.Errorf("key providers %v, want [pbkdf2/p2 pbkdf2/p3]", got)
	}
	for _, bad := range []string{`"p1"`, `"current"`, `"previous"`} {
		if strings.Contains(src, bad) {
			t.Errorf("the rendered encryption contains %s:\n%s", bad, src)
		}
	}
}

// 06-AC-6 (render): the environment form is TF_ENCRYPTION= followed by the same HCL, and neither passphrase leaks anywhere else.
func TestEncryptionEnv_RotationForm_06AC6(t *testing.T) {
	p1, p2 := passphrase(3), passphrase(11)
	e := tofustate.Encryption{Current: p2, Generation: 2, Previous: p1}
	env, err := tofustate.EncryptionEnv(e)
	if err != nil {
		t.Fatalf("EncryptionEnv: %v", err)
	}
	hclSrc, err := tofustate.RenderEncryption(e)
	if err != nil {
		t.Fatalf("RenderEncryption: %v", err)
	}
	if env != "TF_ENCRYPTION="+hclSrc {
		t.Errorf("EncryptionEnv = %q, want TF_ENCRYPTION= followed by the rendered HCL", env)
	}
	if strings.Count(env, p1) != 1 || strings.Count(env, p2) != 1 {
		t.Errorf("each passphrase must appear exactly once in the environment entry")
	}
}

// 06-AC-6 (render): inputs that cannot be a rotation fail closed with TOFU_RENDER_INVALID.
func TestRenderEncryption_RejectsBadRotationInputs_06AC6(t *testing.T) {
	good1, good2 := passphrase(3), passphrase(11)
	cases := map[string]tofustate.Encryption{
		"negative generation":                          {Current: good2, Generation: -1},
		"previous without a generation":                {Current: good2, Previous: good1},
		"previous of generation 1 (no generation 0)":   {Current: good2, Generation: 1, Previous: good1},
		"previous equal to current":                    {Current: good2, Generation: 2, Previous: good2},
		"previous too short":                           {Current: good2, Generation: 2, Previous: "short"},
		"previous with a quote":                        {Current: good2, Generation: 2, Previous: strings.Repeat("a", 20) + `"`},
		"previous with a template sequence":            {Current: good2, Generation: 2, Previous: strings.Repeat("a", 20) + "${x}"},
		"previous with a control character":            {Current: good2, Generation: 2, Previous: strings.Repeat("a", 20) + "\n"},
		"current too short in a rotation":              {Current: "short", Generation: 2, Previous: good1},
		"current with a template sequence in rotation": {Current: strings.Repeat("a", 20) + "%{x}", Generation: 2, Previous: good1},
	}
	for name, e := range cases {
		t.Run(name, func(t *testing.T) {
			out, err := tofustate.RenderEncryption(e)
			if err == nil {
				t.Fatalf("RenderEncryption accepted it:\n%s", out)
			}
			if got := stateErrCode(err); got != "TOFU_RENDER_INVALID" {
				t.Errorf("error code %q, want TOFU_RENDER_INVALID (%v)", got, err)
			}
			if _, err := tofustate.EncryptionEnv(e); err == nil {
				t.Error("EncryptionEnv accepted it")
			}
		})
	}
}

func stateErrCode(err error) string {
	if e, ok := err.(*tofustate.Error); ok {
		return e.Code
	}
	return ""
}
