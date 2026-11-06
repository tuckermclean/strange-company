package installf09b_test

import (
	"os"
	"path/filepath"
)

// useScenario is a copy of the installf09 helper: the fake provider's scenario comes from
// test/fixtures/fake-provider/<name>.yaml, copied to <.bootstrap/fake-provider>/scenario.yaml.
func (i *inst) useScenario(name string) {
	i.t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot(i.t), "test", "fixtures", "fake-provider", name+".yaml"))
	if err != nil {
		i.t.Fatalf("scenario fixture: %v", err)
	}
	write(i.t, filepath.Join(i.fakeProviderDir(), "scenario.yaml"), string(b), 0o600)
}
