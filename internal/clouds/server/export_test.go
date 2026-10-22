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
