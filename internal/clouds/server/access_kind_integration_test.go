//go:build integration

package server

import (
	"os"
	"testing"
)

// The node fixtures need internal/nodes, which imports this package, so
// they live in package server_test (node_fixture_integration_test.go) and
// set these hooks from its init.
var (
	// NodeFixture runs a node in this process against the Docker daemon at
	// socket, joined to a real hub, and returns a Client that reaches the
	// daemon through it.
	NodeFixture func(t *testing.T, socket string) *Client
	// ContainerNodeFixture runs a node inside the container, against the
	// container's own Docker daemon, joined to a real hub in this process,
	// and returns a Client that reaches the daemon through it.
	ContainerNodeFixture func(t *testing.T, container string) *Client
)

// testAccess is how the suites reach their daemon, from
// LOOMPLANE_TEST_ACCESS: "ssh" (the default) or "node".
func testAccess(t *testing.T) string {
	t.Helper()
	switch access := os.Getenv("LOOMPLANE_TEST_ACCESS"); access {
	case "", "ssh":
		return "ssh"
	case "node":
		if NodeFixture == nil || ContainerNodeFixture == nil {
			t.Fatal("the node fixtures are not linked into this test binary")
		}
		return "node"
	default:
		t.Fatalf("LOOMPLANE_TEST_ACCESS=%q: use ssh or node", access)
		return ""
	}
}

// nodeFixture is NodeFixture.
func nodeFixture(t *testing.T, socket string) *Client {
	t.Helper()
	return NodeFixture(t, socket)
}
