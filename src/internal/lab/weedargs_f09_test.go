package lab

import (
	"strconv"
	"strings"
	"testing"
)

// F09 05-AC-6 (review of the S3 front wiring): weed derives the S3 gRPC port as s3.port+10000 when -s3.port.grpc is
// absent, so an internal S3 port above 55535 makes it exit with "invalid port". The plain arguments pin it explicitly.
// weedPlainArgs(args, port) takes the internal port as a string, so an internal port near the top is expressible.

func plainArgsFor(t *testing.T, port string) []string {
	t.Helper()
	prep, err := PrepareS3(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return weedPlainArgs(prep.Args, port)
}

func flagValues(args []string, name string) []string {
	var vs []string
	for _, a := range args {
		if strings.HasPrefix(a, name+"=") {
			vs = append(vs, strings.TrimPrefix(a, name+"="))
		}
	}
	return vs
}

// 05-AC-6
func TestWeedPlainArgs_ExplicitValidGRPCPort_05AC6(t *testing.T) {
	t.Parallel()
	for _, port := range []string{"60000", "65535", "40000", "1024"} {
		args := plainArgsFor(t, port)
		grpc := flagValues(args, "-s3.port.grpc")
		if len(grpc) != 1 {
			t.Errorf("port %s: want exactly one -s3.port.grpc flag, got %v in %v", port, grpc, args)
			continue
		}
		n, err := strconv.Atoi(grpc[0])
		if err != nil || n < 1 || n > 65535 {
			t.Errorf("port %s: -s3.port.grpc=%q is not a valid port", port, grpc[0])
		}
		if grpc[0] == port {
			t.Errorf("port %s: the gRPC port equals the S3 port", port)
		}
	}
}

// 05-AC-6
func TestWeedPlainArgs_PlainHTTPOnInternalPort_05AC6(t *testing.T) {
	t.Parallel()
	args := plainArgsFor(t, "60000")
	for _, a := range args {
		if strings.HasPrefix(a, "-s3.cert.file") || strings.HasPrefix(a, "-s3.key.file") {
			t.Errorf("TLS flag kept: %s", a)
		}
	}
	if p := flagValues(args, "-s3.port"); len(p) != 1 || p[0] != "60000" {
		t.Errorf("-s3.port = %v, want exactly [60000]", p)
	}
	if b := flagValues(args, "-ip.bind"); len(b) != 1 || b[0] != "127.0.0.1" {
		t.Errorf("-ip.bind = %v, want [127.0.0.1]", b)
	}
}
