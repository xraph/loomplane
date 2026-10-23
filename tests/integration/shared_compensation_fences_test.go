//go:build integration

package integration

import (
	"errors"
	"testing"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/security"
)

func TestSharedCompensationRouteCancellationAndCompletionCAS(t *testing.T) {
	f := newPoolFixture(t)
	ctx := t.Context()
	actor := security.Principal{OrgID: "org", UserID: "current"}
	makeOperation := func(id string) *cloudinfra.RouteOperation {
		t.Helper()
		route := cloudinfra.Route{ID: id, OrgID: "org", ProjectID: "proj", Input: cloudinfra.RouteInput{CloudID: "cloud", EnvironmentID: "env", Service: "web"}, Binding: cloudinfra.RouteBinding{ServerID: "server-1", ConnectionID: "runtime-1"}}
		handoff := "handoff"
		if id == "successful" {
			handoff = "successful-handoff"
		}
		op := &cloudinfra.RouteOperation{ID: id, Kind: "release", Status: "waiting", Revision: 1, Attempt: 1, Route: route, Source: &route, HandoffID: handoff, Principal: security.Principal{OrgID: "org", UserID: "original"}, ExecutionPrincipal: &actor}
		if err := f.store.PreviewRoute(ctx, op); err != nil {
			t.Fatal(err)
		}
		return op
	}
	first, second := makeOperation("first"), makeOperation("second")
	stale := *second
	if err := f.store.SaveRouteOperation(ctx, second); err != nil {
		t.Fatal(err)
	}
	if err := f.store.CancelCompensationOperations(ctx, []*cloudinfra.RouteOperation{first, &stale}); !errors.Is(err, cloudinfra.ErrConflict) {
		t.Fatal("stale cancellation accepted", err)
	}
	unchanged, err := f.store.RouteOperation(ctx, "org", first.ID)
	if err != nil || unchanged.Status != "waiting" || unchanged.Revision != first.Revision {
		t.Fatal("partial cancellation committed", unchanged, err)
	}
	if _, err = f.store.BeginIngress(ctx, first); err != nil {
		t.Fatal(err)
	}
	if _, err = f.sql.Exec(ctx, `INSERT INTO loomplane_route_handoffs(id,org_id,route_id,source_connection_id,destination_connection_id) VALUES('handoff','org','first','runtime-1','runtime-1')`); err != nil {
		t.Fatal(err)
	}
	detached := *first
	if _, err = f.store.BeginServerIngress(ctx, "org", "server-1"); err != nil {
		t.Fatal(err)
	}
	if err = f.store.CompleteHandoff(ctx, &detached); !errors.Is(err, cloudinfra.ErrConflict) {
		t.Fatal("detached old release completed after generation takeover", err)
	}
	var released bool
	if err = f.sql.QueryRow(ctx, `SELECT released FROM loomplane_route_handoffs WHERE id='handoff'`).Scan(&released); err != nil || released {
		t.Fatal("stale release retired restored source", released, err)
	}
	if err = f.store.CancelCompensationOperations(ctx, []*cloudinfra.RouteOperation{first, second}); err != nil {
		t.Fatal(err)
	}
	saved, err := f.store.RouteOperation(ctx, "org", first.ID)
	if err != nil || saved.Status != "cancelled" || saved.Principal.UserID != "original" || saved.ExecutionPrincipal == nil || *saved.ExecutionPrincipal != actor {
		t.Fatal("cancellation lost actor checkpoint", saved, err)
	}
	if err = f.store.CompleteHandoff(ctx, first); !errors.Is(err, cloudinfra.ErrConflict) {
		t.Fatal("cancelled release completed", err)
	}
	if err = f.store.SaveRouteOperation(ctx, &detached); !errors.Is(err, cloudinfra.ErrConflict) {
		t.Fatal("stale release republished after cancellation", err)
	}
	successful := makeOperation("successful")
	if _, err = f.sql.Exec(ctx, `INSERT INTO loomplane_route_handoffs(id,org_id,route_id,source_connection_id,destination_connection_id) VALUES('successful-handoff','org','successful','runtime-1','runtime-1')`); err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.BeginIngress(ctx, successful); err != nil {
		t.Fatal(err)
	}
	if err = f.store.CompleteHandoff(ctx, successful); err != nil {
		t.Fatal(err)
	}
	if err = f.store.CompleteHandoff(ctx, successful); err != nil {
		t.Fatal("exact completion is not idempotent", err)
	}
}
