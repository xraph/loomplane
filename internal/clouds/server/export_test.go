package server

import (
	"sync"
	"testing"

	"github.com/xraph/loomplane/internal/clouds/server/fencerules"
)

// pinIngressImageForTest stands a test image in for the published
// DNS-capable proxy until the test ends. The SSH helper reads the pinned
// image with each request; a test's nodes are told of it through the pins
// they registered with PinNodeImages.
func pinIngressImageForTest(t testing.TB, image string) {
	t.Helper()
	previous := fencerules.DNSImage
	fencerules.DNSImage = image
	t.Cleanup(func() { fencerules.DNSImage = previous })
	nodePins.Lock()
	pins := nodePins.byTest[t]
	nodePins.Unlock()
	for _, pin := range pins {
		pin(t, image)
	}
}

// nodePins are the nodes each test runs, as functions that make a node
// trust a pinned proxy image.
var nodePins struct {
	sync.Mutex
	byTest map[testing.TB][]func(testing.TB, string)
}

// PinNodeImages makes pin run whenever t pins an ingress image, until t
// ends. A node fixture registers one, since a node's fence reads the image
// list it was built with.
func PinNodeImages(t testing.TB, pin func(t testing.TB, image string)) {
	nodePins.Lock()
	defer nodePins.Unlock()
	if nodePins.byTest == nil {
		nodePins.byTest = map[testing.TB][]func(testing.TB, string){}
	}
	nodePins.byTest[t] = append(nodePins.byTest[t], pin)
	t.Cleanup(func() {
		nodePins.Lock()
		defer nodePins.Unlock()
		delete(nodePins.byTest, t)
	})
}

// PinnedIngressImage is the DNS-capable image the test process trusts now.
func PinnedIngressImage() string { return fencerules.DNSImage }

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
