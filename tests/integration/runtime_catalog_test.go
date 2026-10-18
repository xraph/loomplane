//go:build integration

package integration

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/drivers/pgdriver"

	"github.com/xraph/loomplane/database/migrations"
	"github.com/xraph/loomplane/internal/ops"
	"github.com/xraph/loomplane/internal/runtimecatalog"
	domain "github.com/xraph/loomplane/pkg/federation"
)

func TestRuntimeCatalog(t *testing.T) {
	ctx := t.Context()
	dsn := freshDatabase(t)
	if err := ops.Migrate(ctx, dsn, migrations.All()); err != nil {
		t.Fatal(err)
	}
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	for _, sql := range []string{
		`INSERT INTO loomplane_projects(id,org_id,name,slug,status,created_by,created_at,updated_at) VALUES('p','o','p','p','ready','u',now(),now())`,
		`INSERT INTO loomplane_environments(id,org_id,project_id,name,kind,created_at,updated_at) VALUES('e','o','p','prod','production',now(),now())`,
		`INSERT INTO loomplane_cloud_connections(id,org_id,cloud,name,account,status,created_by,created_at,updated_at) VALUES('c','o','server','c','daemon','ready','u',now(),now())`,
		`INSERT INTO loomplane_federation_destinations(id,org_id,project_id,environment_id,name,connection_id,identity,identity_revision,generation,created_at,updated_at) VALUES('d','o','p','e','primary','c','{}','physical',1,now(),now())`,
	} {
		if _, err = conn.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	pg := pgdriver.New()
	if err = pg.Open(ctx, dsn); err != nil {
		t.Fatal(err)
	}
	defer pg.Close()
	store := runtimecatalog.NewPgStore(pg)
	scope := domain.Scope{OrgID: "o", ProjectID: "p", EnvironmentID: "e"}
	agent := domain.AgentIdentity{Scope: scope, AgentID: "agent", DestinationID: "d", URI: "spiffe://example.test/agent", Epoch: 1}
	reg := runtimecatalog.Registration{Agent: agent, DestinationGeneration: 1, Active: true, Exports: map[string]runtimecatalog.ExportBounds{"export": {Scope: scope, DestinationID: "d", PlacementID: "placement", BindingID: "binding", Port: 8080, CIDRs: []string{"10.20.0.0/24"}}}}
	if err = store.Register(ctx, reg); err != nil {
		t.Fatal(err)
	}
	if _, err = conn.Exec(ctx, `DELETE FROM loomplane_federation_destinations WHERE id='d'`); err == nil {
		t.Fatal("agent did not retain destination")
	}
	policy := func(context.Context, domain.AgentIdentity) (runtimecatalog.Visibility, error) {
		return runtimecatalog.Visibility{Revision: "approved", ExpiresAt: time.Now().Add(time.Minute), Exports: map[string]domain.Scope{"export": scope}}, nil
	}
	c := runtimecatalog.New(store, policy, nil)
	ep := domain.Endpoint{Metadata: domain.Metadata{Scope: scope, ID: "a", Generation: 1}, DestinationID: "d", ExportID: "export", PlacementID: "placement", BindingID: "binding", ObserverID: "agent", Address: "10.20.0.2", Port: 8080, Readiness: domain.Ready}
	other := ep
	other.ID = "b"
	other.Address = "10.20.0.3"
	batch := domain.ObservationBatch{Agent: agent, DestinationGeneration: 1, Sequence: 1, Endpoints: []domain.Endpoint{ep, other}}
	if observeErr := c.Observe(ctx, agent, batch); observeErr != nil {
		t.Fatal(observeErr)
	}
	first, err := c.Snapshot(ctx, agent, domain.SnapshotRequest{Agent: agent, Limit: 1})
	if err != nil || first.NextCursor == "" {
		t.Fatalf("snapshot: %+v %v", first, err)
	}
	other.Generation = 2
	other.Address = "10.20.0.4"
	batch.Sequence = 2
	batch.Endpoints = []domain.Endpoint{other}
	if observeErr := c.Observe(ctx, agent, batch); observeErr != nil {
		t.Fatal(observeErr)
	}
	restarted := runtimecatalog.New(runtimecatalog.NewPgStore(pg), policy, nil)
	next, err := restarted.Snapshot(ctx, agent, domain.SnapshotRequest{Agent: agent, Cursor: first.NextCursor, Limit: 1})
	if err != nil || next.Revision != first.Revision || len(next.Endpoints) != 1 || next.Endpoints[0].Address != "10.20.0.3" {
		t.Fatalf("mixed or lost snapshot after restart: %+v %v", next, err)
	}
	// A rejected multi-endpoint batch must not partially update or consume sequence.
	ep.Generation = 3
	ep.Address = "10.20.0.5"
	bad := other
	bad.Generation = 3
	bad.Port = 5432
	batch.Sequence = 3
	batch.Endpoints = []domain.Endpoint{ep, bad}
	if observeErr := c.Observe(ctx, agent, batch); observeErr == nil {
		t.Fatal("invalid partial batch accepted")
	}
	batch.Endpoints = []domain.Endpoint{ep}
	if observeErr := c.Observe(ctx, agent, batch); observeErr != nil {
		t.Fatalf("rejected batch consumed sequence: %v", observeErr)
	}
	batch.Sequence = 9
	batch.Endpoints = nil
	batch.Tombstones = []domain.EndpointTombstone{{Metadata: domain.Metadata{Scope: scope, ID: "a", Generation: 9}, DestinationID: "d", ExportID: "export", ObserverID: "agent"}}
	if observeErr := c.Observe(ctx, agent, batch); observeErr != nil {
		t.Fatal(observeErr)
	}
	batch.Sequence = 10
	batch.Tombstones = nil
	ep.Generation = 8
	batch.Endpoints = []domain.Endpoint{ep}
	if observeErr := restarted.Observe(ctx, agent, batch); !errors.Is(observeErr, runtimecatalog.ErrStale) {
		t.Fatalf("old endpoint resurrected after restart: %v", observeErr)
	}
	foreign := agent
	foreign.OrgID = "foreign"
	if _, err = c.Snapshot(ctx, foreign, domain.SnapshotRequest{Agent: foreign, Cursor: first.NextCursor}); err == nil {
		t.Fatal("foreign cursor accepted")
	}
	// An independent consumer must also lose cached pages when the source closes.
	if _, err = conn.Exec(ctx, `INSERT INTO loomplane_federation_destinations(id,org_id,project_id,environment_id,name,connection_id,identity,identity_revision,generation,created_at,updated_at) VALUES('consumer-d','o','p','e','consumer','c','{}','physical-consumer',1,now(),now())`); err != nil {
		t.Fatal(err)
	}
	consumer := agent
	consumer.AgentID = "consumer"
	consumer.DestinationID = "consumer-d"
	consumer.URI = "spiffe://example.test/consumer"
	if registerErr := store.Register(ctx, runtimecatalog.Registration{Agent: consumer, DestinationGeneration: 1, Active: true, Exports: map[string]runtimecatalog.ExportBounds{}}); registerErr != nil {
		t.Fatal(registerErr)
	}
	var consumerPage *domain.Snapshot
	for range runtimecatalog.MaxViewsPerAgent + 1 {
		consumerPage, err = c.Snapshot(ctx, consumer, domain.SnapshotRequest{Agent: consumer, Limit: 1})
		if err != nil || consumerPage.NextCursor == "" {
			t.Fatalf("consumer snapshot: %+v %v", consumerPage, err)
		}
	}
	var views int
	if err = conn.QueryRow(ctx, `SELECT count(*) FROM loomplane_federation_catalog_views WHERE org_id='o' AND agent_id='consumer'`).Scan(&views); err != nil || views != runtimecatalog.MaxViewsPerAgent {
		t.Fatalf("unbounded views: %d %v", views, err)
	}
	if _, err = conn.Exec(ctx, `UPDATE loomplane_federation_destinations SET quarantined=true,generation=generation+1 WHERE id='d'`); err != nil {
		t.Fatal(err)
	}
	if _, err = c.Snapshot(ctx, agent, domain.SnapshotRequest{Agent: agent, Cursor: first.NextCursor}); !errors.Is(err, runtimecatalog.ErrUnauthorized) {
		t.Fatalf("quarantine did not revoke snapshot: %v", err)
	}
	if _, err = c.Snapshot(ctx, consumer, domain.SnapshotRequest{Agent: consumer, Cursor: consumerPage.NextCursor}); !errors.Is(err, runtimecatalog.ErrCursorExpired) {
		t.Fatalf("quarantined source remained visible to consumer: %v", err)
	}
	fresh, err := c.Snapshot(ctx, consumer, domain.SnapshotRequest{Agent: consumer})
	if err != nil || len(fresh.Endpoints) != 0 || len(fresh.Tombstones) != 0 {
		t.Fatalf("quarantined source leaked: %+v %v", fresh, err)
	}
	if err = c.Observe(ctx, agent, batch); !errors.Is(err, runtimecatalog.ErrUnauthorized) {
		t.Fatalf("quarantine did not reject observer: %v", err)
	}
}
