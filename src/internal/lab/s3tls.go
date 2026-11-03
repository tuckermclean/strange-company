package lab

// S3Prep is what the lab needs to start the s3-emulator over TLS: the complete `weed server` arguments, the run-local
// certificate material and the S3Info (https endpoint, CA file) it yields.
// SC-STUB(F09 05-AC-6)
type S3Prep struct {
	Args     []string
	CertFile string
	KeyFile  string
	Info     S3Info
}

// PrepareS3 creates <runDir>/s3 and a run-local CA with a server certificate for the s3-emulator (SANs IP:127.0.0.1,
// IP:10.0.2.2, DNS:s3.lab.internal) and returns the arguments of `weed server`.
// SC-STUB(F09 05-AC-6)
func PrepareS3(runDir string) (S3Prep, error) {
	return S3Prep{}, nil
}
