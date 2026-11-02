//go:build integration

package nodes

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/xraph/loomplane/pkg/nodeproto"
)

// TestHubOverPostgres runs a session end to end on the real store: claim,
// online, inventory, allow, open, revoke, release, and a refused replay.
func TestHubOverPostgres(t *testing.T) {
	store, conn := testStore(t)
	r := newHubRigOn(t, store, "a")
	priv := seedNode(t, store, "node_1", "web-1", nodeproto.EndpointProbe)
	a := r.signed("node_1", priv)
	n := r.connectWith(a, "pg:")
	if m := n.next(); m.(nodeproto.Hello).Epoch != 1 {
		t.Fatalf("hello %#v", m)
	}
	waitFor(t, "online", func() bool { return r.hub.Online("node_1") && r.node("node_1").Status == StatusOnline })

	n.send(nodeproto.Hello{Version: nodeproto.Version, NodeVersion: "v1.9.0"})
	n.send(nodeproto.InventoryReport{Inventory: nodeproto.Inventory{OS: "linux", CPUs: 2, Endpoints: []nodeproto.Endpoint{nodeproto.EndpointProbe, nodeproto.EndpointDocker}}})
	if m := n.next(); !slices.Equal(m.(nodeproto.Narrow).Endpoints, []nodeproto.Endpoint{nodeproto.EndpointProbe}) {
		t.Fatalf("narrow %#v", m)
	}
	got := r.node("node_1")
	if got.Inventory.CPUs != 2 || got.Version != "v1.9.0" || !slices.Equal(got.Endpoints, []nodeproto.Endpoint{nodeproto.EndpointProbe}) {
		t.Fatalf("node %+v", got)
	}
	n.send(nodeproto.Allowed{Endpoints: []nodeproto.Endpoint{nodeproto.EndpointDocker}})
	waitFor(t, "widened", func() bool { return len(r.node("node_1").Endpoints) == 2 })
	echo(t, r.hub, "node_1", "pg:")

	if err := r.svc.Revoke(context.Background(), admin, "node_1"); err != nil {
		t.Fatal(err)
	}
	if m := n.next(); m != (nodeproto.Revoked{}) {
		t.Fatalf("message %#v", m)
	}
	n.closedWithin(time.Second)
	select {
	case err := <-r.accepted:
		if err != nil {
			t.Fatalf("session ended with %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Accept did not return after the revoke")
	}
	waitFor(t, "released", func() bool {
		_, err := store.Session(context.Background(), "node_1")
		return errors.Is(err, ErrNotFound)
	})
	if got := r.node("node_1"); got.Status != StatusRevoked {
		t.Fatalf("status %s", got.Status)
	}
	var nonces int
	if err := conn.QueryRow(t.Context(), `SELECT count(*) FROM loomplane_node_nonces WHERE nonce=$1`, a.Challenge.Nonce).Scan(&nonces); err != nil || nonces != 1 {
		t.Fatalf("nonce rows %d %v", nonces, err)
	}
	if err := r.refused(r.signed("node_1", priv)); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("revoked reconnect: %v", err)
	}
}
