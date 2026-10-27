package s3emu

// Credentials sign the requests a re-signing proxy forwards to an authentication-enabled emulator.
type Credentials struct {
	AccessKey, SecretKey, Region string
}

// SC-STUB(F08 06-AC-2) NewResigningProxy is NewStrippingProxy for an upstream that verifies SigV4: after removing
// If-None-Match it signs every request again with c, so the upstream accepts it (S4 follow-up N3).
func NewResigningProxy(upstream string, c Credentials) (*StrippingProxy, error) {
	return nil, nil
}
