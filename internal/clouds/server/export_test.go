package server

import (
	"testing"

	"github.com/xraph/loomplane/internal/clouds/server/fencerules"
)

// pinIngressImageForTest stands a test image in for the published
// DNS-capable proxy until the test ends.
func pinIngressImageForTest(t testing.TB, image string) {
	t.Helper()
	previous := fencerules.DNSImage
	fencerules.DNSImage = image
	t.Cleanup(func() { fencerules.DNSImage = previous })
}

// failUpgradeForTest fails UpgradeIngress after step until the test ends.
func failUpgradeForTest(t testing.TB, step string, err error) {
	t.Helper()
	previous := upgradeFault
	upgradeFault = func(at string) error {
		if at == step {
			return err
		}
		return nil
	}
	t.Cleanup(func() { upgradeFault = previous })
}
