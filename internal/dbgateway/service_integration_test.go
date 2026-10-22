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
	svc := NewService(s, urls, priv, plane.TrustKey{PublicKey: pub, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour)}, time.Now)

	first, err := svc.Snapshot(ctx, &gw, 0)
	if err != nil || first == nil || first.Version != 1 || len(first.Routes) != 1 || first.Routes[0].ID != route.ID {
		t.Fatalf("first snapshot %+v %v", first, err)
	}
	if err = plane.VerifySnapshot(*first, svc.TrustKeys(), gw.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	if user, password := openCredential(t, keys.sealing, first.Routes[0]); user != "app" || password != "pw" {
		t.Fatalf("credential %q/%q", user, password)
	}
	again, err := svc.Snapshot(ctx, &gw, 1)
	if err != nil || again != nil {
		t.Fatalf("an unchanged state gave %+v %v", again, err)
	}

	if err = s.SetRouteState(ctx, "org_1", gw.ID, route.ID, "paused"); err != nil {
		t.Fatal(err)
	}
	paused, err := svc.Snapshot(ctx, &gw, 1)
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
