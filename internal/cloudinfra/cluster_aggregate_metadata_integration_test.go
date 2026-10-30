//go:build integration

package cloudinfra

import (
	"encoding/json"
	"testing"
)

func TestClusterAggregateMetadataReaders(t *testing.T) {
	db := overviewDatabase(t)
	ctx := t.Context()
	for _, sql := range []string{
		`CREATE TABLE loomplane_cluster_capacity_envelopes(org_id text,cloud_id text,id text,body jsonb,resource_id text,pool_id text)`,
		`CREATE TABLE loomplane_cluster_workload_holds(org_id text,resource_id text,id text,body jsonb,released boolean DEFAULT false)`,
		`ALTER TABLE loomplane_cloud_servers ADD COLUMN cloud_id text, ADD COLUMN status text`,
		`CREATE TABLE loomplane_infrastructure_operations(org_id text,cloud_id text,id text,body jsonb)`,
		`CREATE TABLE loomplane_cloud_capacity_reservations(org_id text,cloud_id text,id text,status text)`,
		`CREATE TABLE loomplane_cluster_operations(org_id text,cloud_id text,id text,body jsonb)`,
		`INSERT INTO loomplane_cluster_capacity_envelopes VALUES('org','cloud','envelope','{"schema_version":1,"resource_id":"cluster","pool_id":"pool","state":"active","maximum":{"servers":4}}','cluster','pool')`,
		`INSERT INTO loomplane_cluster_workload_holds(org_id,resource_id,id,body) VALUES('org','cluster','hold','{"schema_version":1,"revision":1,"updated_at":"2026-01-01T00:00:00Z","input":{"org_id":"org","id":"hold"}}')`,
	} {
		if _, err := db.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	store := NewPgStore(db)
	for _, version := range []string{`1`, `2`, `null`, `"1"`, `{}`, `missing`} {
		t.Run(version, func(t *testing.T) {
			for _, table := range []string{"loomplane_cluster_capacity_envelopes", "loomplane_cluster_workload_holds"} {
				sql := `UPDATE ` + table + ` SET body=jsonb_set(body,'{schema_version}',$1::jsonb)`
				var err error
				if version == "missing" {
					_, err = db.Exec(ctx, `UPDATE `+table+` SET body=body-'schema_version'`)
				} else {
					_, err = db.Exec(ctx, sql, version)
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			check := func(name string, err error) {
				t.Helper()
				if (err == nil) != (version == "1") {
					t.Fatalf("%s version %s: %v", name, version, err)
				}
			}
			_, err := store.ClusterScalingEnvelope(ctx, "org", "cluster", "pool")
			check("scaling", err)
			_, err = store.budgetPriceSources(ctx, "org", "cloud")
			check("pricing", err)
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback() }()
			_, err = clusterEnvelopesTx(ctx, tx, "org")
			check("envelopes", err)
			_, _, err = envelopeUsage(ctx, tx, "org", "cloud", "provider", "account", "")
			check("accounting", err)
			_, err = clusterWorkloadHolds(ctx, tx, "org", "cluster")
			check("workload accounting", err)
			_, err = clusterWorkloadHold(ctx, tx, ClusterWorkloadInput{OrgID: "org", ID: "hold"})
			check("hold point read", err)
		})
	}
}

func TestClusterAggregateMetadataDoesNotChangeReview(t *testing.T) {
	e := ClusterCapacityEnvelope{SchemaVersion: 1, Review: ClusterPoolEnvelopeReview{Key: "pool", Maximum: ResourceVector{Servers: 3}}}
	h := ClusterWorkloadHold{SchemaVersion: 1, Revision: 1, Input: ClusterWorkloadInput{ID: "hold", ReviewDigest: "signed"}}
	before := maintenanceHash([]any{e.Review, h.Input})
	e.SchemaVersion = 2
	h.SchemaVersion = 2
	h.Revision++
	if after := maintenanceHash([]any{e.Review, h.Input}); after != before {
		t.Fatal("metadata changed approved review")
	}
	raw, _ := json.Marshal(e)
	if json.Unmarshal(raw, &e) == nil {
		t.Fatal("future body accepted")
	}
}
