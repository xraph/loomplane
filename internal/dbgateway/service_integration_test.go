//go:build integration

package dbgateway

import (
	"crypto/ed25519"
	"crypto/rand"
	"net/http"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/dbgateway/plane"
)

// Over the real store: after a restore from an older backup the stored
// version is behind what the gateway holds, and its next poll gets a
// snapshot past its own version that it will accept.
func TestSnapshotRecoversAfterARestore(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()
	gw, _, err := s.EnsureGateway(ctx, newGateway(t, "env_1", "main"))
	if err != nil {
		t.Fatal(err)
	}
	route, err := s.EnsureRoute(ctx, "org_1", gw.ID, "orders")
	if err != nil {
		t.Fatal(err)
	}
	urls := &memURLs{urls: map[string]string{"orders": "postgres://app:pw@db.do.example:25060/orders"}}
	now := time.Now()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(s, urls, []SigningKey{{Private: priv, Trust: plane.TrustKey{PublicKey: pub, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour)}}}, time.Now)
	if first, ferr := snapshotOf(t, svc, &gw, 0, time.Now()); ferr != nil || first.Version != 1 {
		t.Fatalf("first: %+v %v", first, ferr)
	}

	// The gateway served version 41 before the restore.
	held := &plane.Store{}
	if err = held.Swap(plane.Snapshot{GatewayID: gw.ID, Version: 41}); err != nil {
		t.Fatal(err)
	}
	next, err := snapshotOf(t, svc, &gw, 41, time.Now())
	if err != nil || next == nil || next.Version != 42 {
		t.Fatalf("a gateway at 41 got %+v %v, want version 42", next, err)
	}
	if err = held.Swap(*next); err != nil {
		t.Fatalf("the gateway refuses the snapshot: %v", err)
	}
	if v, _, lerr := s.LatestSnapshot(ctx, "org_1", gw.ID); lerr != nil || v != 42 {
		t.Fatalf("stored version %d %v, want 42", v, lerr)
	}
	if none, nerr := snapshotOf(t, svc, &gw, 42, time.Now()); nerr != nil || none != nil {
		t.Fatalf("at 42 with nothing new: %+v %v", none, nerr)
	}
	if err = s.SetRouteState(ctx, "org_1", gw.ID, route.ID, "paused"); err != nil {
		t.Fatal(err)
	}
	if paused, perr := snapshotOf(t, svc, &gw, 42, time.Now()); perr != nil || paused == nil || paused.Version != 43 {
		t.Fatalf("a change after the raise: %+v %v, want 43", paused, perr)
	}
}

// The service over the real store: versions follow content, pausing shows in
// the next snapshot, and a gateway's request is checked against its stored key.
func TestServiceOverTheStore(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()
	g, keys := testGateway(t, "")
	g.ID = ""
	gw, _, err := s.EnsureGateway(ctx, g)
	if err != nil {
		t.Fatal(err)
	}
	route, err := s.EnsureRoute(ctx, "org_1", gw.ID, "orders")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.EnsureRoute(ctx, "org_1", gw.ID, "billing"); err != nil {
		t.Fatal(err)
	}
	urls := &memURLs{urls: map[string]string{"orders": "postgres://app:pw@db.do.example:25060/orders?sslmode=require"}}
	now := time.Now()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(s, urls, []SigningKey{{Private: priv, Trust: plane.TrustKey{PublicKey: pub, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour)}}}, time.Now)

	first, err := snapshotOf(t, svc, &gw, 0, time.Now())
	if err != nil || first == nil || first.Version != 1 || len(first.Routes) != 1 || first.Routes[0].ID != route.ID {
		t.Fatalf("first snapshot %+v %v", first, err)
	}
	if user, password := openCredential(t, keys.sealing, first.Routes[0]); user != "app" || password != "pw" {
		t.Fatalf("credential %q/%q", user, password)
	}
	again, err := snapshotOf(t, svc, &gw, 1, time.Now())
	if err != nil || again != nil {
		t.Fatalf("an unchanged state gave %+v %v", again, err)
	}

	if err = s.SetRouteState(ctx, "org_1", gw.ID, route.ID, "paused"); err != nil {
		t.Fatal(err)
	}
	paused, err := snapshotOf(t, svc, &gw, 1, time.Now())
	if err != nil || paused == nil || paused.Version != 2 || paused.Routes[0].State != plane.RouteStatePaused {
		t.Fatalf("after pausing %+v %v", paused, err)
	}

	req := signed(t, http.MethodGet, "/api/v1/db-gateways/"+gw.ID+"/snapshot?after=2", nil, gw.ID, keys.signing, now)
	got, err := svc.VerifyRequest(req, nil, now)
	if err != nil || got.ID != gw.ID || got.OrgID != "org_1" {
		t.Fatalf("VerifyRequest: %+v %v", got, err)
	}
	resp, err := svc.Report(ctx, &gw, plane.Report{InstanceID: "inst-1", SnapshotVersion: 2})
	if err != nil || resp.LiveInstances != 1 {
		t.Fatalf("Report: %+v %v", resp, err)
	}
}
