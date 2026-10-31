package identity

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filippo.io/age"

	"strange.company/sc/internal/bootstrap/engine"
	"strange.company/sc/internal/clock"
)

// F08 S7, CD-70(g): conditions on the in-process scrypt key cache of the recovery bundle (identity/scryptkdf.go).
// The kdf-level tests use work factor 10 so that they stay cheap; the cache and the checks do not depend on it.

const cdTestPass = "cd70g test passphrase one"

func cdResetCache(t *testing.T) {
	t.Helper()
	wipe := func() {
		kdfMu.Lock()
		kdfKeys = map[[32]byte][]byte{}
		kdfOrder = nil
		kdfMu.Unlock()
	}
	wipe()
	t.Cleanup(wipe)
}

func cdCacheLen() int {
	kdfMu.Lock()
	defer kdfMu.Unlock()
	return len(kdfKeys)
}

// cdWrap wraps a file key with the cached recipient at the given work factor and returns the stanza.
func cdWrap(t *testing.T, pass string, logN int) *age.Stanza {
	t.Helper()
	r, err := newKDFRecipient(pass, logN)
	if err != nil {
		t.Fatal(err)
	}
	st, err := r.Wrap(bytes.Repeat([]byte{7}, 16))
	if err != nil {
		t.Fatal(err)
	}
	return st[0]
}

// CD-70(g) condition 2: the cache key is an HMAC under a random per-process key, not a plain hash of passphrase and salt.
// No plain SHA-256 of the usual encodings of (passphrase, salt, work factor) may be a key of the cache.
func TestKDFCache_KeyIsNotAPlainHash_CD70g(t *testing.T) {
	cdResetCache(t)
	st := cdWrap(t, cdTestPass, 10)
	salt, err := base64.RawStdEncoding.DecodeString(st.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	if n := cdCacheLen(); n != 1 {
		t.Fatalf("after one wrap the cache holds %d entries, want 1", n)
	}
	pw := []byte(cdTestPass)
	be := func(v uint64) []byte { var b [8]byte; binary.BigEndian.PutUint64(b[:], v); return b[:] }
	plain := map[string][]byte{
		"pass|salt|logN":           bytes.Join([][]byte{pw, salt, {10}}, nil),
		"pass|salt":                bytes.Join([][]byte{pw, salt}, nil),
		"len-prefixed pass,salt,N": bytes.Join([][]byte{be(uint64(len(pw))), pw, be(uint64(len(salt))), salt, be(10)}, nil),
		"pass|label+salt":          bytes.Join([][]byte{pw, []byte("age-encryption.org/v1/scrypt"), salt}, nil),
		"pass|0|salt|logN":         bytes.Join([][]byte{pw, {0}, salt, be(10)}, nil),
	}
	kdfMu.Lock()
	defer kdfMu.Unlock()
	for name, in := range plain {
		sum := sha256.Sum256(in)
		if _, ok := kdfKeys[sum]; ok {
			t.Errorf("the cache key is the plain SHA-256 of %s: it must be an HMAC under a random per-process key", name)
		}
	}
}

// CD-70(g) condition 2, second half: a failed unwrap (wrong passphrase) is never cached.
func TestKDFCache_WrongPassphraseIsNeverCached_CD70g(t *testing.T) {
	cdResetCache(t)
	st := cdWrap(t, cdTestPass, 10)
	if n := cdCacheLen(); n != 1 {
		t.Fatalf("after one wrap the cache holds %d entries, want 1", n)
	}
	id, err := newKDFIdentity("not the passphrase of this stanza", 22)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := id.Unwrap([]*age.Stanza{st}); err == nil {
		t.Fatal("a wrong passphrase unwrapped the stanza")
	}
	if n := cdCacheLen(); n != 1 {
		t.Errorf("after a wrong-passphrase attempt the cache holds %d entries, want 1: a failed unwrap must not be cached", n)
	}
	// the right passphrase still opens it from the cache and adds nothing
	good, _ := newKDFIdentity(cdTestPass, 22)
	if _, err := good.Unwrap([]*age.Stanza{st}); err != nil {
		t.Fatalf("the right passphrase fails: %v", err)
	}
	if n := cdCacheLen(); n != 1 {
		t.Errorf("the cache holds %d entries after a repeated open, want 1", n)
	}
}

func cdStrictLast(salt string) string {
	const alpha = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	last := strings.IndexByte(alpha, salt[len(salt)-1])
	return salt[:len(salt)-1] + string(alpha[last|1]) // the 4 spare bits of the 22nd character become non-zero
}

// CD-70(g) condition 3: the salt and work-factor checks equal age's. For every variant of the stanza, the cached identity
// accepts it exactly when age's own ScryptIdentity does.
func TestKDFIdentity_SaltAndWorkFactorChecksEqualAges_CD70g(t *testing.T) {
	cdResetCache(t)
	base := cdWrap(t, cdTestPass, 10)
	salt := base.Args[0]
	if len(salt) != 22 {
		t.Fatalf("fixture: salt %q is not 22 characters", salt)
	}
	cases := []struct{ name, salt, wf string }{
		{"valid", salt, "10"},
		{"non-strict base64 (non-zero spare bits)", cdStrictLast(salt), "10"},
		{"LF inside the salt", salt[:8] + "\n" + salt[8:], "10"},
		{"CRLF inside the salt", salt[:8] + "\r\n" + salt[8:], "10"},
		{"trailing LF", salt + "\n", "10"},
		{"leading CR", "\r" + salt, "10"},
		{"padded salt", salt + "==", "10"},
		{"short salt", salt[:20], "10"},
		{"work factor with a leading zero", salt, "010"},
		{"work factor with a plus sign", salt, "+10"},
		{"work factor zero", salt, "0"},
		{"work factor negative", salt, "-1"},
		{"work factor above the maximum", salt, "23"},
		{"work factor not a number", salt, "ten"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st := &age.Stanza{Type: "scrypt", Args: []string{c.salt, c.wf}, Body: base.Body}
			ageID, err := age.NewScryptIdentity(cdTestPass)
			if err != nil {
				t.Fatal(err)
			}
			ageID.SetMaxWorkFactor(22)
			_, ageErr := ageID.Unwrap([]*age.Stanza{st})
			mine, _ := newKDFIdentity(cdTestPass, 22)
			_, myErr := mine.Unwrap([]*age.Stanza{st})
			if (ageErr == nil) != (myErr == nil) {
				t.Errorf("age's ScryptIdentity returns %v, the cached identity returns %v: the checks must be equal", ageErr, myErr)
			}
		})
	}
}

// CD-70(g) condition 4a: what the package seals opens with stock age's ScryptIdentity, and the other way round.
func TestKDF_InteropWithStockAge_CD70g(t *testing.T) {
	cdResetCache(t)
	plain := []byte("interop payload")
	var mine bytes.Buffer
	rcp, err := newKDFRecipient(cdTestPass, 10)
	if err != nil {
		t.Fatal(err)
	}
	w, err := age.Encrypt(&mine, rcp)
	if err != nil {
		t.Fatal(err)
	}
	w.Write(plain)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	stock, _ := age.NewScryptIdentity(cdTestPass)
	r, err := age.Decrypt(bytes.NewReader(mine.Bytes()), stock)
	if err != nil {
		t.Fatalf("stock age cannot open what the package sealed: %v", err)
	}
	var got bytes.Buffer
	got.ReadFrom(r)
	if !bytes.Equal(got.Bytes(), plain) {
		t.Errorf("stock age read %q", got.Bytes())
	}

	srcp, _ := age.NewScryptRecipient(cdTestPass)
	srcp.SetWorkFactor(10)
	var theirs bytes.Buffer
	w, err = age.Encrypt(&theirs, srcp)
	if err != nil {
		t.Fatal(err)
	}
	w.Write(plain)
	w.Close()
	id, _ := newKDFIdentity(cdTestPass, 22)
	r, err = age.Decrypt(bytes.NewReader(theirs.Bytes()), id)
	if err != nil {
		t.Fatalf("the package cannot open what stock age sealed: %v", err)
	}
	got.Reset()
	got.ReadFrom(r)
	if !bytes.Equal(got.Bytes(), plain) {
		t.Errorf("the package read %q", got.Bytes())
	}
}

// CD-70(g) condition 4b: a file made by the age CLI (`age -p`, work factor 18, v1.3.2) with a fixed, obviously non-secret
// passphrase opens with Open and yields the expected archive.
func TestOpen_AgeCLIFixture_CD70g(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "agecli", "archive.age"))
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(filepath.Join("testdata", "agecli", "plaintext.tar"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(data, []byte("age-encryption.org/v1\n-> scrypt ")) {
		t.Fatal("fixture: not an age passphrase file")
	}
	o, err := Open(data, "interop-fixture-passphrase-0000")
	if err != nil {
		t.Fatalf("Open cannot decrypt the file made by the age CLI: %v", err)
	}
	if !strings.Contains(string(o.Manifest), `"age-cli-interop"`) {
		t.Errorf("manifest %q", o.Manifest)
	}
	if len(o.Files) != 2 || o.Files[1].Path != "secrets-0.txt" || string(o.Files[1].Data) != "plain test file\n" {
		t.Errorf("files %+v", o.Files)
	}
	if len(want) == 0 {
		t.Error("empty expected plaintext")
	}
	if _, err := Open(data, "interop-fixture-passphrase-0001"); err == nil {
		t.Error("a wrong passphrase opens the fixture")
	}
}

type cdJournal struct{ rb engine.RecoveryBundleState }

func (j *cdJournal) RecoveryBundle() engine.RecoveryBundleState     { return j.rb }
func (j *cdJournal) SetRecoveryBundle(s engine.RecoveryBundleState) { j.rb = s }

// CD-70(g) condition 1 / plan 50 section 14.3 step 9 / 50-AC-10: bundle-confirm opens the downloaded copy with age's own
// ScryptIdentity (a fresh derivation), never through the key cache. Observable: the cache holds a deliberately wrong key
// for this very bundle (as if a cached derivation were stale); a confirm that reads the cache fails to open the bundle, one
// that derives afresh confirms it. The cache must also gain no entry.
func TestBundleConfirm_DerivesFreshNeverFromCache_CD70g_50AC10(t *testing.T) {
	cdResetCache(t)
	pass := cdTestPass
	var buf bytes.Buffer
	rcp, err := newKDFRecipient(pass, 10) // populates the cache, as a seal in this process does
	if err != nil {
		t.Fatal(err)
	}
	aw, err := age.Encrypt(&buf, rcp)
	if err != nil {
		t.Fatal(err)
	}
	aw.Write(tarOf(t, map[string]string{"manifest.json": "{}"}))
	if err := aw.Close(); err != nil {
		t.Fatal(err)
	}
	data := buf.Bytes()
	if o, err := Open(data, pass); err != nil || o == nil {
		t.Fatalf("fixture: Open of the sealed copy fails: %v", err)
	}
	kdfMu.Lock()
	if len(kdfKeys) == 0 {
		kdfMu.Unlock()
		t.Fatal("fixture: the seal left no entry in the cache")
	}
	for id := range kdfKeys {
		kdfKeys[id] = bytes.Repeat([]byte{1}, 32)
	}
	n := len(kdfKeys)
	kdfMu.Unlock()

	sum := sha256.Sum256(data)
	j := &cdJournal{rb: engine.RecoveryBundleState{Status: "uploaded", Generation: 1, Sha256: "sha256:" + hex.EncodeToString(sum[:])}}
	u := NewBundleConfirm(BundleConfirmConfig{
		Journal:  j,
		Download: func(context.Context) ([]byte, error) { return data, nil },
		Prompt:   func(context.Context, int) (string, error) { return pass, nil },
		Clock:    clock.NewFake(time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)),
		Attempts: 1,
	})
	_, err = u.Run(context.Background())
	if err != nil || j.rb.Status != "confirmed" {
		t.Errorf("confirm err=%v status=%q: it must open the downloaded copy with a fresh derivation, not with the cached key", err, j.rb.Status)
	}
	if m := cdCacheLen(); m != n {
		t.Errorf("the cache holds %d entries after confirm, want %d: confirm must not use or fill the cache", m, n)
	}
}

func tarOf(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var b bytes.Buffer
	tw := tar.NewWriter(&b)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		tw.Write([]byte(body))
	}
	tw.Close()
	return b.Bytes()
}
