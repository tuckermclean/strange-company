// This file is derived from filippo.io/age v1.3.2 (scrypt.go) and carries age's licence:
//
// Copyright 2019 The age Authors
// Copyright 2019 Google LLC
// Copyright 2022 Filippo Valsorda
//
// Redistribution and use in source and binary forms, with or without
// modification, are permitted provided that the following conditions are
// met:
//
//    * Redistributions of source code must retain the above copyright
// notice, this list of conditions and the following disclaimer.
//    * Redistributions in binary form must reproduce the above
// copyright notice, this list of conditions and the following disclaimer
// in the documentation and/or other materials provided with the
// distribution.
//    * Neither the name of the age project nor the names of its
// contributors may be used to endorse or promote products derived from
// this software without specific prior written permission.
//
// THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS
// "AS IS" AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT
// LIMITED TO, THE IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR
// A PARTICULAR PURPOSE ARE DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT
// OWNER OR CONTRIBUTORS BE LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL,
// SPECIAL, EXEMPLARY, OR CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT
// LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR SERVICES; LOSS OF USE,
// DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER CAUSED AND ON ANY
// THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY, OR TORT
// (INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE
// OF THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.

package identity

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"filippo.io/age"
	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/scrypt"
)

// The recovery bundle uses the age passphrase format (stanza "scrypt"). These are age's own ScryptRecipient and
// ScryptIdentity with one difference: the derived wrapping key is remembered for the life of the process, per
// (passphrase, salt, work factor). A bundle this process sealed is then opened again (the upload confirmation, the next
// run step, a rotation that reads it) without repeating the memory-hard derivation, which is the dominant cost of every
// seal and open. The salt is random per seal, so no derivation is ever reused for a second file; the file format and the
// security of the stored bundle are those of age.

const (
	kdfLabel     = "age-encryption.org/v1/scrypt"
	kdfSaltSize  = 16
	kdfCacheSize = 16
)

var (
	kdfMu    sync.Mutex
	kdfKeys  = map[[32]byte][]byte{}
	kdfOrder [][32]byte
	kdfDigit = regexp.MustCompile(`^[1-9][0-9]*$`)
)

// kdfMacKey is the random per-process key of the cache index: the index is an HMAC-SHA-256 of the inputs, never a plain
// hash of passphrase and salt.
var kdfMacKey = func() []byte {
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		panic("identity: no randomness for the key cache: " + err.Error())
	}
	return k
}()

func kdfCacheID(password, salt []byte, logN int) [32]byte {
	h := hmac.New(sha256.New, kdfMacKey)
	var n [8]byte
	for _, part := range [][]byte{password, salt} {
		binary.BigEndian.PutUint64(n[:], uint64(len(part)))
		h.Write(n[:])
		h.Write(part)
	}
	binary.BigEndian.PutUint64(n[:], uint64(logN))
	h.Write(n[:])
	var id [32]byte
	copy(id[:], h.Sum(nil))
	return id
}

func kdfDerive(password, salt []byte, logN int) ([]byte, error) {
	k, err := scrypt.Key(password, append([]byte(kdfLabel), salt...), 1<<logN, 8, 1, chacha20poly1305.KeySize)
	if err != nil {
		return nil, fmt.Errorf("failed to generate scrypt hash: %v", err)
	}
	return k, nil
}

func kdfLookup(id [32]byte) ([]byte, bool) {
	kdfMu.Lock()
	defer kdfMu.Unlock()
	k, ok := kdfKeys[id]
	return k, ok
}

func kdfStore(id [32]byte, k []byte) {
	kdfMu.Lock()
	defer kdfMu.Unlock()
	if _, dup := kdfKeys[id]; dup {
		return
	}
	if len(kdfOrder) >= kdfCacheSize {
		delete(kdfKeys, kdfOrder[0])
		kdfOrder = kdfOrder[1:]
	}
	kdfKeys[id] = k
	kdfOrder = append(kdfOrder, id)
}

// kdfKey returns the wrapping key from the cache, deriving it on a miss (used when sealing, where the key is correct
// by construction).
func kdfKey(password, salt []byte, logN int) ([]byte, error) {
	id := kdfCacheID(password, salt, logN)
	if k, ok := kdfLookup(id); ok {
		return k, nil
	}
	k, err := kdfDerive(password, salt, logN)
	if err != nil {
		return nil, err
	}
	kdfStore(id, k)
	return k, nil
}

type kdfRecipient struct {
	password   []byte
	workFactor int
}

func newKDFRecipient(password string, workFactor int) (*kdfRecipient, error) {
	if len(password) == 0 {
		return nil, errors.New("passphrase can't be empty")
	}
	return &kdfRecipient{password: []byte(password), workFactor: workFactor}, nil
}

func (r *kdfRecipient) Wrap(fileKey []byte) ([]*age.Stanza, error) {
	salt := make([]byte, kdfSaltSize)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	k, err := kdfKey(r.password, salt, r.workFactor)
	if err != nil {
		return nil, err
	}
	aead, err := chacha20poly1305.New(k)
	if err != nil {
		return nil, err
	}
	return []*age.Stanza{{
		Type: "scrypt",
		Args: []string{base64.RawStdEncoding.EncodeToString(salt), strconv.Itoa(r.workFactor)},
		Body: aead.Seal(nil, make([]byte, chacha20poly1305.NonceSize), fileKey, nil),
	}}, nil
}

// WrapWithLabels returns a random label so the recipient cannot be mixed with others, as age's ScryptRecipient does.
func (r *kdfRecipient) WrapWithLabels(fileKey []byte) ([]*age.Stanza, []string, error) {
	stanzas, err := r.Wrap(fileKey)
	if err != nil {
		return nil, nil, err
	}
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return nil, nil, err
	}
	return stanzas, []string{hex.EncodeToString(random)}, nil
}

type kdfIdentity struct {
	password      []byte
	maxWorkFactor int
}

func newKDFIdentity(password string, maxWorkFactor int) (*kdfIdentity, error) {
	if len(password) == 0 {
		return nil, errors.New("passphrase can't be empty")
	}
	return &kdfIdentity{password: []byte(password), maxWorkFactor: maxWorkFactor}, nil
}

func (i *kdfIdentity) Unwrap(stanzas []*age.Stanza) ([]byte, error) {
	for _, s := range stanzas {
		if s.Type == "scrypt" && len(stanzas) != 1 {
			return nil, errors.New("an scrypt recipient must be the only one")
		}
	}
	for _, s := range stanzas {
		if s.Type == "scrypt" {
			return i.unwrap(s)
		}
	}
	return nil, fmt.Errorf("%w: file is not passphrase-encrypted", age.ErrIncorrectIdentity)
}

func (i *kdfIdentity) unwrap(block *age.Stanza) ([]byte, error) {
	if len(block.Args) != 2 {
		return nil, errors.New("invalid scrypt recipient block")
	}
	if strings.ContainsAny(block.Args[0], "\r\n") {
		return nil, errors.New("failed to parse scrypt salt: invalid base64")
	}
	salt, err := base64.RawStdEncoding.Strict().DecodeString(block.Args[0])
	if err != nil {
		return nil, fmt.Errorf("failed to parse scrypt salt: %v", err)
	}
	if len(salt) != kdfSaltSize {
		return nil, errors.New("invalid scrypt recipient block")
	}
	if !kdfDigit.MatchString(block.Args[1]) {
		return nil, fmt.Errorf("scrypt work factor encoding invalid: %q", block.Args[1])
	}
	logN, err := strconv.Atoi(block.Args[1])
	if err != nil {
		return nil, fmt.Errorf("failed to parse scrypt work factor: %v", err)
	}
	if logN > i.maxWorkFactor {
		return nil, fmt.Errorf("scrypt work factor too large: %v", logN)
	}
	if logN <= 0 {
		return nil, fmt.Errorf("invalid scrypt work factor: %v", logN)
	}
	id := kdfCacheID(i.password, salt, logN)
	k, cached := kdfLookup(id)
	if !cached {
		if k, err = kdfDerive(i.password, salt, logN); err != nil {
			return nil, err
		}
	}
	aead, err := chacha20poly1305.New(k)
	if err != nil {
		return nil, err
	}
	if len(block.Body) != 16+aead.Overhead() {
		return nil, errors.New("invalid scrypt recipient block: incorrect file key size")
	}
	fileKey, err := aead.Open(nil, make([]byte, chacha20poly1305.NonceSize), block.Body, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: incorrect passphrase", age.ErrIncorrectIdentity)
	}
	if !cached {
		kdfStore(id, k) // only a derivation that opened the stanza is remembered
	}
	return fileKey, nil
}
