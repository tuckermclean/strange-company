package lab_test

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/pem"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"strange.company/sc/internal/lab"
)

// F09 S9b (plan 05 section 14.6 "Lab S3", 05-AC-6): the s3-emulator serves TLS with a run-local CA; the certificate
// names IP:127.0.0.1 (toolbox side), IP:10.0.2.2 (QEMU's host alias, the in-cluster side) and DNS:s3.lab.internal; the
// generated bootstrap.env says S3_ENDPOINT=https://127.0.0.1:9000 (the config requires https) and `sc-lab env` carries
// SC_LAB_S3_INCLUSTER_ENDPOINT=https://10.0.2.2:9000 and SC_LAB_S3_CA_FILE.

func readCert(t *testing.T, path string) *x509.Certificate {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	blk, _ := pem.Decode(b)
	if blk == nil || blk.Type != "CERTIFICATE" {
		t.Fatalf("%s holds no PEM certificate", path)
	}
	c, err := x509.ParseCertificate(blk.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func prepared(t *testing.T) (string, lab.S3Prep) {
	t.Helper()
	runDir := t.TempDir()
	p, err := lab.PrepareS3(runDir)
	if err != nil {
		t.Fatalf("PrepareS3: %v", err)
	}
	return runDir, p
}

// 05-AC-6: the weed arguments enable TLS with files inside the run directory and stay bound to loopback.
func TestPrepareS3_ServesTLSFromRunLocalFiles_05AC6(t *testing.T) {
	runDir, p := prepared(t)
	for _, want := range []string{"-s3", "-s3.cert.file=" + p.CertFile, "-s3.key.file=" + p.KeyFile} {
		if !slices.Contains(p.Args, want) {
			t.Errorf("args lack %q: %v", want, p.Args)
		}
	}
	for _, f := range []string{p.CertFile, p.KeyFile, p.Info.CAFile} {
		if f == "" || !strings.HasPrefix(f, runDir+string(filepath.Separator)) {
			t.Errorf("file %q is not inside the run directory %s", f, runDir)
			continue
		}
		if _, err := os.Stat(f); err != nil {
			t.Errorf("%v", err)
		}
	}
	if st, err := os.Stat(p.KeyFile); err == nil && st.Mode().Perm()&0o077 != 0 {
		t.Errorf("private key mode = %v, want no group or other access", st.Mode().Perm())
	}
	for _, a := range p.Args {
		if strings.Contains(a, "0.0.0.0") {
			t.Errorf("argument %q binds beyond 127.0.0.1", a)
		}
	}
	if p.Info.Endpoint != "https://127.0.0.1:9000" {
		t.Errorf("Info.Endpoint = %q, want https://127.0.0.1:9000", p.Info.Endpoint)
	}
}

// 05-AC-6: the certificate carries the three SANs and chains to the run-local CA for each of them.
func TestPrepareS3_CertificateSANsAndChain_05AC6(t *testing.T) {
	_, p := prepared(t)
	leaf := readCert(t, p.CertFile)
	for _, ip := range []string{"127.0.0.1", "10.0.2.2"} {
		if !slices.ContainsFunc(leaf.IPAddresses, func(a net.IP) bool { return a.Equal(net.ParseIP(ip)) }) {
			t.Errorf("certificate lacks SAN IP:%s (has %v)", ip, leaf.IPAddresses)
		}
	}
	if !slices.Contains(leaf.DNSNames, "s3.lab.internal") {
		t.Errorf("certificate lacks SAN DNS:s3.lab.internal (has %v)", leaf.DNSNames)
	}
	caPEM, err := os.ReadFile(p.Info.CAFile)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		t.Fatal("the CA file holds no certificate")
	}
	opts := x509.VerifyOptions{Roots: pool, CurrentTime: leaf.NotBefore.AddDate(0, 0, 1)}
	if _, err := leaf.Verify(opts); err != nil {
		t.Errorf("the server certificate does not chain to the run-local CA: %v", err)
	}
	for _, h := range []string{"127.0.0.1", "10.0.2.2", "s3.lab.internal"} {
		if err := leaf.VerifyHostname(h); err != nil {
			t.Errorf("VerifyHostname(%s): %v", h, err)
		}
	}
	if leaf.NotAfter.Sub(leaf.NotBefore).Hours() > 24*30 {
		t.Errorf("certificate validity %v is not run-local short", leaf.NotAfter.Sub(leaf.NotBefore))
	}
}

// 05-AC-6: every run gets its own CA.
func TestPrepareS3_CAIsRunLocal_05AC6(t *testing.T) {
	_, a := prepared(t)
	_, b := prepared(t)
	ca, err1 := os.ReadFile(a.Info.CAFile)
	cb, err2 := os.ReadFile(b.Info.CAFile)
	if err1 != nil || err2 != nil {
		t.Fatalf("reading the CA files: %v, %v", err1, err2)
	}
	if bytes.Equal(ca, cb) {
		t.Errorf("two runs share one CA")
	}
}

// 05-AC-6: the production StartS3 must start the emulator through PrepareS3 (TLS), not over plain HTTP.
func TestProductionStartS3_UsesPrepareS3Not_PlainHTTP_05AC6(t *testing.T) {
	b, err := os.ReadFile("prod.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	if !strings.Contains(src, "PrepareS3(") {
		t.Errorf("prod.go does not call PrepareS3")
	}
	if strings.Contains(src, "http://127.0.0.1:9000") {
		t.Errorf("prod.go still returns the plain-HTTP endpoint http://127.0.0.1:9000")
	}
}

// 05-AC-6: with a TLS emulator, the generated bootstrap.env and `sc-lab env` carry the https endpoints and the CA file.
func TestUp_TLSEmulator_EnvCarriesHTTPSEndpointsAndCA_05AC6(t *testing.T) {
	r := newRig(t, debianSHA256(sha256hex(debianBytes)))
	ca := filepath.Join(t.TempDir(), "ca.pem")
	r.env.StartS3 = func(context.Context, string) (lab.S3Info, error) {
		return lab.S3Info{Endpoint: "https://127.0.0.1:9000", CAFile: ca}, nil
	}
	if code := lab.Run(context.Background(), upArgs("lab-small", "debian-13"), r.env); code != 0 {
		t.Fatalf("sc-lab up exited %d: %s", code, r.stderr.String())
	}
	r.stdout.Reset()
	if code := lab.Run(context.Background(), []string{"env", "--run-id", "t1"}, r.env); code != 0 {
		t.Fatalf("sc-lab env exited %d: %s", code, r.stderr.String())
	}
	got := r.stdout.String()
	for _, want := range []string{"S3_ENDPOINT=https://127.0.0.1:9000\n", "SC_LAB_S3_INCLUSTER_ENDPOINT=https://10.0.2.2:9000\n", "SC_LAB_S3_CA_FILE=" + ca + "\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("sc-lab env output lacks %q:\n%s", want, got)
		}
	}
}
