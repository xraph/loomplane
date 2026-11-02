package server

import "testing"

// pinIngressImageForTest stands a test image in for the published
// DNS-capable proxy until the test ends.
func pinIngressImageForTest(t testing.TB, image string) {
	t.Helper()
	previous := dnsIngressImage
	dnsIngressImage = image
	t.Cleanup(func() { dnsIngressImage = previous })
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
