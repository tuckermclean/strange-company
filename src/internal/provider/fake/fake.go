// Package fake is the OpenTofu-free fake provider (plan 03 section 22, BOOT-11): it simulates creation for the
// bootstrap tests and counts creates. It is refused outside SC_TEST_MODE=1.
package fake

// Provider is the fake provider bound to its state directory.
type Provider struct{}

// SC-STUB(F08 50-AC-10) New returns the fake provider with state in dir; without SC_TEST_MODE=1 in env it fails with
// TEST_MODE_REQUIRED.
func New(env map[string]string, dir string) (*Provider, error) {
	return nil, nil
}

// SC-STUB(F08 50-AC-10) CreateCount reads the create counter in dir (the file "creates"); a missing file is zero.
func CreateCount(dir string) (int, error) {
	return 0, nil
}
