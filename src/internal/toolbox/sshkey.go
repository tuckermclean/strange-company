package toolbox

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/binary"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"strings"
)

// sshKey is the public side of an unencrypted OpenSSH private key.
type sshKey struct {
	Type string // ssh-ed25519 | ssh-rsa
	Bits int
	Blob []byte // SSH wire-format public key
}

type sshReader struct{ b []byte }

func (r *sshReader) u32() (uint32, error) {
	if len(r.b) < 4 {
		return 0, errors.New("truncated key")
	}
	v := binary.BigEndian.Uint32(r.b)
	r.b = r.b[4:]
	return v, nil
}

func (r *sshReader) str() ([]byte, error) {
	n, err := r.u32()
	if err != nil {
		return nil, err
	}
	if uint64(n) > uint64(len(r.b)) {
		return nil, errors.New("truncated key")
	}
	s := r.b[:n]
	r.b = r.b[n:]
	return s, nil
}

func sshStr(b []byte) []byte {
	return append(binary.BigEndian.AppendUint32(nil, uint32(len(b))), b...)
}

// parseOpenSSHPrivate parses an unencrypted openssh-key-v1 ed25519 or RSA key (PF-06).
//
// With allowEncrypted (ssh-agent mode) a passphrase-protected key is accepted: type, size and public key are taken
// from the unencrypted public blob in the header.
func parseOpenSSHPrivate(data []byte, allowEncrypted bool) (*sshKey, error) {
	blk, _ := pem.Decode(data)
	if blk == nil || blk.Type != "OPENSSH PRIVATE KEY" {
		return nil, errors.New("not an OpenSSH private key (PEM \"OPENSSH PRIVATE KEY\" expected)")
	}
	const magic = "openssh-key-v1\x00"
	if !bytes.HasPrefix(blk.Bytes, []byte(magic)) {
		return nil, errors.New("bad openssh-key-v1 magic")
	}
	r := &sshReader{blk.Bytes[len(magic):]}
	cipher, err := r.str()
	if err != nil {
		return nil, err
	}
	if _, err = r.str(); err != nil { // kdfname
		return nil, err
	}
	if _, err = r.str(); err != nil { // kdfoptions
		return nil, err
	}
	encrypted := string(cipher) != "none"
	if encrypted && !allowEncrypted {
		return nil, errors.New("private key is passphrase-protected (cipher " + string(cipher) + "); an unencrypted key is required (or set SSH_USE_AGENT=true)")
	}
	n, err := r.u32()
	if err != nil || n != 1 {
		return nil, errors.New("expected exactly one key")
	}
	hdrBlob, err := r.str() // public blob in header
	if err != nil {
		return nil, err
	}
	if encrypted {
		return keyFromBlob(hdrBlob)
	}
	privBytes, err := r.str()
	if err != nil {
		return nil, err
	}
	p := &sshReader{privBytes}
	c1, err1 := p.u32()
	c2, err2 := p.u32()
	if err1 != nil || err2 != nil || c1 != c2 {
		return nil, errors.New("corrupt private section (check ints differ)")
	}
	kt, err := p.str()
	if err != nil {
		return nil, err
	}
	switch string(kt) {
	case "ssh-ed25519":
		pub, err := p.str()
		if err != nil {
			return nil, err
		}
		priv, err := p.str()
		if err != nil {
			return nil, err
		}
		if len(pub) != ed25519.PublicKeySize || len(priv) != ed25519.PrivateKeySize ||
			!bytes.Equal(ed25519.NewKeyFromSeed(priv[:ed25519.SeedSize]).Public().(ed25519.PublicKey), pub) ||
			!bytes.Equal(priv[ed25519.SeedSize:], pub) {
			return nil, errors.New("corrupt ed25519 key")
		}
		blob := append(sshStr([]byte("ssh-ed25519")), sshStr(pub)...)
		return &sshKey{Type: "ssh-ed25519", Bits: 256, Blob: blob}, nil
	case "ssh-rsa":
		nb, err := p.str()
		if err != nil {
			return nil, err
		}
		eb, err := p.str()
		if err != nil {
			return nil, err
		}
		mod := new(big.Int).SetBytes(nb)
		blob := append(sshStr([]byte("ssh-rsa")), sshStr(eb)...)
		blob = append(blob, sshStr(nb)...)
		return &sshKey{Type: "ssh-rsa", Bits: mod.BitLen(), Blob: blob}, nil
	}
	return nil, fmt.Errorf("unsupported key type %q (ed25519 or RSA >= 3072 required)", kt)
}

// parsePublicLine returns the wire blob of the first "<type> <base64> [comment]" line.
func parsePublicLine(data []byte) (string, []byte, error) {
	f := strings.Fields(string(data))
	if len(f) < 2 {
		return "", nil, errors.New("not an OpenSSH public key line")
	}
	b, err := base64.StdEncoding.DecodeString(f[1])
	if err != nil {
		return "", nil, err
	}
	return f[0], b, nil
}

// keyFromBlob reads type and size from an SSH wire-format public key blob.
func keyFromBlob(blob []byte) (*sshKey, error) {
	r := &sshReader{blob}
	kt, err := r.str()
	if err != nil {
		return nil, err
	}
	switch string(kt) {
	case "ssh-ed25519":
		pub, err := r.str()
		if err != nil || len(pub) != ed25519.PublicKeySize {
			return nil, errors.New("corrupt ed25519 public blob")
		}
		return &sshKey{Type: "ssh-ed25519", Bits: 256, Blob: blob}, nil
	case "ssh-rsa":
		if _, err := r.str(); err != nil {
			return nil, err
		}
		nb, err := r.str()
		if err != nil {
			return nil, err
		}
		return &sshKey{Type: "ssh-rsa", Bits: new(big.Int).SetBytes(nb).BitLen(), Blob: blob}, nil
	}
	return nil, fmt.Errorf("unsupported key type %q (ed25519 or RSA >= 3072 required)", kt)
}
