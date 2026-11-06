package lab

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"

	"strange.company/sc/internal/clock"
)

// S3Prep is what the lab needs to start the s3-emulator over TLS: the complete `weed server` arguments, the run-local
// certificate material and the S3Info (https endpoint, CA file) it yields.
type S3Prep struct {
	Args     []string
	CertFile string
	KeyFile  string
	Info     S3Info
}

// s3CertLifetime bounds the run-local certificates; a lab run lasts minutes.
const s3CertLifetime = 7 * 24 * time.Hour

// PrepareS3 creates <runDir>/s3 and a run-local CA with a server certificate for the s3-emulator (SANs IP:127.0.0.1,
// IP:10.0.2.2, DNS:s3.lab.internal) and returns the arguments of `weed server`.
func PrepareS3(runDir string) (S3Prep, error) {
	data := filepath.Join(runDir, "s3")
	if err := os.MkdirAll(data, 0o755); err != nil {
		return S3Prep{}, err
	}
	now := clock.Real{}.Now()
	notBefore, notAfter := now.Add(-5*time.Minute), now.Add(s3CertLifetime)

	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return S3Prep{}, err
	}
	caSerial, err := serial()
	if err != nil {
		return S3Prep{}, err
	}
	caTmpl := &x509.Certificate{
		SerialNumber:          caSerial,
		Subject:               pkix.Name{CommonName: "sc-lab run-local CA"},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            0,
		MaxPathLenZero:        true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		return S3Prep{}, err
	}
	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		return S3Prep{}, err
	}

	srvKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return S3Prep{}, err
	}
	srvSerial, err := serial()
	if err != nil {
		return S3Prep{}, err
	}
	srvTmpl := &x509.Certificate{
		SerialNumber: srvSerial,
		Subject:      pkix.Name{CommonName: "s3.lab.internal"},
		NotBefore:    notBefore,
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("10.0.2.2")},
		DNSNames:     []string{"s3.lab.internal"},
	}
	srvDER, err := x509.CreateCertificate(rand.Reader, srvTmpl, ca, &srvKey.PublicKey, caKey)
	if err != nil {
		return S3Prep{}, err
	}
	keyDER, err := x509.MarshalECPrivateKey(srvKey)
	if err != nil {
		return S3Prep{}, err
	}

	p := S3Prep{
		CertFile: filepath.Join(runDir, "s3-server.crt"),
		KeyFile:  filepath.Join(runDir, "s3-server.key"),
		Info:     S3Info{Endpoint: "https://127.0.0.1:9000", CAFile: filepath.Join(runDir, "s3-ca.crt")},
	}
	if err := os.WriteFile(p.Info.CAFile, pemBlock("CERTIFICATE", caDER), 0o644); err != nil {
		return S3Prep{}, err
	}
	if err := os.WriteFile(p.CertFile, pemBlock("CERTIFICATE", srvDER), 0o644); err != nil {
		return S3Prep{}, err
	}
	if err := os.WriteFile(p.KeyFile, pemBlock("EC PRIVATE KEY", keyDER), 0o600); err != nil {
		return S3Prep{}, err
	}
	p.Args = append(S3EmulatorArgs(data), "-s3.cert.file="+p.CertFile, "-s3.key.file="+p.KeyFile)
	return p, nil
}

func serial() (*big.Int, error) {
	return rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
}

func pemBlock(typ string, der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der})
}
