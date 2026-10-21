//go:build integration

package integration

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/drivers/pgdriver"

	"github.com/xraph/loomplane/database/migrations"
	"github.com/xraph/loomplane/internal/access"
	"github.com/xraph/loomplane/internal/ops"
	"github.com/xraph/loomplane/internal/runtimecatalog"
	"github.com/xraph/loomplane/internal/workloadidentity"
	domain "github.com/xraph/loomplane/pkg/federation"
)

func TestFederationDelivery(t *testing.T) {
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
	for _, query := range []string{
		`INSERT INTO loomplane_projects(id,org_id,name,slug,status,created_by,created_at,updated_at) VALUES('p','o','p','p','ready','u',now(),now())`,
		`INSERT INTO loomplane_environments(id,org_id,project_id,name,kind,created_at,updated_at) VALUES('e','o','p','prod','production',now(),now())`,
		`INSERT INTO loomplane_cloud_connections(id,org_id,cloud,name,account,status,created_by,created_at,updated_at) VALUES('c','o','server','c','daemon','ready','u',now(),now())`,
		`INSERT INTO loomplane_federation_destinations(id,org_id,project_id,environment_id,name,connection_id,identity,identity_revision,generation,created_at,updated_at) VALUES('d','o','p','e','primary','c','{}','physical',1,now(),now())`,
	} {
		if _, seedErr := conn.Exec(ctx, query); seedErr != nil {
			t.Fatal(seedErr)
		}
	}
	db := pgdriver.New()
	if openErr := db.Open(ctx, dsn); openErr != nil {
		t.Fatal(openErr)
	}
	defer db.Close()
	scope := domain.Scope{OrgID: "o", ProjectID: "p", EnvironmentID: "e"}
	agent := domain.AgentIdentity{Scope: scope, DestinationID: "d", AgentID: "agent", Epoch: 1}
	agent.URI, err = workloadidentity.AgentURI("loomplane.test", agent)
	if err != nil {
		t.Fatal(err)
	}
	catalog := runtimecatalog.NewPgStore(db)
	if registerErr := catalog.Register(ctx, runtimecatalog.Registration{Agent: agent, DestinationGeneration: 1, Active: true}); registerErr != nil {
		t.Fatal(registerErr)
	}
	store := access.NewPolicyStore(db)
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	signer, err := access.NewSnapshotSigner(scope, key, now.Add(-time.Hour), now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	trust, err := access.NewSnapshotTrust(scope, 1, []access.SnapshotKey{signer.Key()})
	if err != nil {
		t.Fatal(err)
	}
	prepare := func() (domain.Snapshot, uint64) {
		t.Helper()
		input, inputErr := store.Input(ctx, scope)
		if inputErr != nil {
			t.Fatal(inputErr)
		}
		bundle, compileErr := access.Compile(input)
		if compileErr != nil {
			t.Fatal(compileErr)
		}
		_, sequence, readErr := catalog.Read(ctx, agent)
		if readErr != nil {
			t.Fatal(readErr)
		}
		return domain.Snapshot{Scope: scope, DestinationID: agent.DestinationID, Epoch: agent.Epoch, Full: true, IssuedAt: input.EvaluatedAt, ExpiresAt: input.EvaluatedAt.Add(30 * time.Second), Policy: bundle}, sequence
	}
	prepared, cursor := prepare()
	first, err := store.IssueSnapshot(ctx, agent, cursor, prepared, signer)
	if err != nil {
		t.Fatal(err)
	}
	if first.Sequence != 1 || first.Revision == "" {
		t.Fatal("missing durable delivery identity")
	}
	verified, err := trust.Verify(first, agent, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	local, err := access.NewPolicyState(agent)
	if err != nil {
		t.Fatal(err)
	}
	defer local.Close()
	ack, err := local.Apply(ctx, verified)
	if err != nil {
		t.Fatal(err)
	}
	if ackErr := store.AcknowledgeSnapshot(ctx, agent, ack); ackErr != nil {
		t.Fatal(ackErr)
	}
	if ackErr := store.AcknowledgeSnapshot(ctx, agent, ack); ackErr != nil {
		t.Fatal("ack retry failed", ackErr)
	}
	tampered := ack
	tampered.Revision = "forged"
	if ackErr := store.AcknowledgeSnapshot(ctx, agent, tampered); !errors.Is(ackErr, access.ErrPolicyConflict) {
		t.Fatal("unissued revision acknowledged", ackErr)
	}
	// Renewal advances the durable sequence even when catalog and policy stay unchanged.
	restarted := access.NewPolicyStore(db)
	second, err := restarted.IssueSnapshot(ctx, agent, cursor, prepared, signer)
	if err != nil || second.Sequence != 2 {
		t.Fatalf("restart renewal: %+v %v", second, err)
	}
	if ackErr := restarted.AcknowledgeSnapshot(ctx, agent, ack); !errors.Is(ackErr, access.ErrPolicyConflict) {
		t.Fatal("old delivery acknowledged", ackErr)
	}
	status, err := restarted.Delivery(ctx, agent)
	if err != nil || status.Sequence != 2 || status.AcknowledgedSequence != 1 || status.PolicyGeneration != prepared.Policy.Generation || status.LeaseUntil.Before(first.ExpiresAt) {
		t.Fatalf("delivery state: %+v %v", status, err)
	}
	// Concurrent controllers cannot issue the same sequence.
	sequences := make(chan uint64, 8)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			signed, issueErr := restarted.IssueSnapshot(ctx, agent, cursor, prepared, signer)
			if issueErr != nil {
				t.Error(issueErr)
				return
			}
			sequences <- signed.Sequence
		})
	}
	wg.Wait()
	close(sequences)
	seen := map[uint64]bool{}
	for sequence := range sequences {
		if seen[sequence] {
			t.Fatal("duplicate delivery sequence")
		}
		seen[sequence] = true
	}
	if len(seen) != 8 {
		t.Fatal("delivery lost")
	}
	// A new catalog observation invalidates an assembled snapshot.
	observer := runtimecatalog.New(catalog, nil, nil)
	if observeErr := observer.Observe(ctx, agent, domain.ObservationBatch{Agent: agent, DestinationGeneration: 1, Sequence: 1, Full: true}); observeErr != nil {
		t.Fatal(observeErr)
	}
	if _, issueErr := store.IssueSnapshot(ctx, agent, cursor, prepared, signer); !errors.Is(issueErr, access.ErrPolicyConflict) {
		t.Fatal("stale endpoints signed", issueErr)
	}
	prepared, cursor = prepare()

	t.Run("coordinator cannot invent approval", func(t *testing.T) {
		forged := prepared
		export := domain.ServiceExport{Metadata: domain.Metadata{Scope: scope, ID: "invented", Generation: 1}, ServiceID: "orders", DestinationIDs: []string{"d"}, Port: 443, Protocol: domain.HTTP, IdentityProfile: domain.WorkloadIdentity}
		revision, revisionErr := access.ExportRevision(export)
		if revisionErr != nil {
			t.Fatal(revisionErr)
		}
		grant := domain.AccessGrant{Metadata: domain.Metadata{Scope: scope, ID: "invented-grant", Generation: 1}, Source: domain.WorkloadSelector{Scope: scope, DestinationID: "d", ServiceID: "caller"}, ExportID: export.ID, ExportRevision: revision, Port: 443, Protocol: domain.HTTP, OwnerID: "admin"}
		grant.ApprovedRevision, revisionErr = access.GrantRevision(grant)
		if revisionErr != nil {
			t.Fatal(revisionErr)
		}
		forged.Policy, revisionErr = access.CompileAt(domain.PolicyInput{Scope: scope, Generation: prepared.Policy.Generation, Exports: []domain.ServiceExport{export}, Grants: []domain.AccessGrant{grant}}, prepared.IssuedAt)
		if revisionErr != nil {
			t.Fatal(revisionErr)
		}
		if signed, issueErr := store.IssueSnapshot(ctx, agent, cursor, forged, signer); issueErr == nil || len(signed.Signature) != 0 {
			t.Fatal("invented grant signed")
		}
	})
	allow := func(context.Context) error { return nil }
	export, err := store.PutExport(ctx, domain.ServiceExport{Metadata: domain.Metadata{Scope: scope, ID: "api"}, ServiceID: "orders", DestinationIDs: []string{"d"}, Port: 443, Protocol: domain.HTTP, IdentityProfile: domain.WorkloadIdentity}, 0, "admin", allow)
	if err != nil {
		t.Fatal(err)
	}
	if _, issueErr := store.IssueSnapshot(ctx, agent, cursor, prepared, signer); !errors.Is(issueErr, access.ErrPolicyConflict) {
		t.Fatal("stale policy signed", issueErr)
	}
	prepared, cursor = prepare()
	wrongAgent := agent
	wrongAgent.Epoch++
	if _, issueErr := store.IssueSnapshot(ctx, wrongAgent, cursor, prepared, signer); issueErr == nil {
		t.Fatal("foreign epoch signed")
	}

	t.Run("signed watch revokes installed session", func(t *testing.T) {
		grant := domain.AccessGrant{Metadata: domain.Metadata{Scope: scope, ID: "live-grant"}, Source: domain.WorkloadSelector{Scope: scope, DestinationID: agent.DestinationID, ServiceID: "caller"}, ExportID: export.ID, ExportRevision: export.Revision, Port: export.Port, Protocol: export.Protocol}
		proposed, proposeErr := store.ProposeGrant(ctx, grant, 0, "admin", allow)
		if proposeErr != nil {
			t.Fatal(proposeErr)
		}
		approved, approveErr := store.ApproveGrant(ctx, "o", proposed.ID, proposed.Generation, proposed.Revision, "admin", allow)
		if approveErr != nil {
			t.Fatal(approveErr)
		}
		if updateErr := catalog.Update(ctx, agent, func(state *runtimecatalog.AgentState) error {
			state.Registration.Exports = map[string]runtimecatalog.ExportBounds{export.ID: {Scope: scope, DestinationID: agent.DestinationID, PlacementID: "placement", BindingID: "binding", Port: export.Port, CIDRs: []string{"10.20.0.0/24"}}}
			return nil
		}); updateErr != nil {
			t.Fatal(updateErr)
		}
		distributor, createErr := access.NewDistributor(store, catalog, func(context.Context, domain.Scope) (*access.SnapshotSigner, error) { return signer, nil })
		if createErr != nil {
			t.Fatal(createErr)
		}
		endpoint := domain.Endpoint{Metadata: domain.Metadata{Scope: scope, ID: "endpoint", Generation: 2}, DestinationID: agent.DestinationID, ExportID: export.ID, PlacementID: "placement", BindingID: "binding", ObserverID: agent.AgentID, Address: "10.20.0.2", Port: export.Port, Readiness: domain.Ready}
		if observeErr := distributor.Observe(ctx, agent, domain.ObservationBatch{Agent: agent, DestinationGeneration: 1, Sequence: 2, Full: true, Endpoints: []domain.Endpoint{endpoint}}); observeErr != nil {
			t.Fatal(observeErr)
		}
		stop := errors.New("test transport closed")
		bus := &deliveryWake{wake: make(chan struct{}, 1)}
		deliveries := 0
		var session *access.Session
		watchErr := distributor.Watch(ctx, agent, bus, func(delivery context.Context, snapshot *domain.Snapshot) error {
			deliveries++
			if snapshot.ExpiresAt.Sub(snapshot.IssuedAt) > runtimecatalog.EndpointLease {
				t.Fatal("signed snapshot exceeds runtime transport lease")
			}
			verified, verifyErr := trust.Verify(*snapshot, agent, time.Now())
			if verifyErr != nil {
				return verifyErr
			}
			installed, applyErr := local.Apply(delivery, verified)
			if applyErr != nil {
				return applyErr
			}
			if ackErr := store.AcknowledgeSnapshot(delivery, agent, installed); ackErr != nil {
				return ackErr
			}
			if deliveries == 1 {
				if len(snapshot.Endpoints) != 1 || len(snapshot.Policy.Grants) != 1 {
					t.Fatal("approved route missing")
				}
				session, applyErr = local.Open(ctx, access.Request{Source: grant.Source, Target: domain.WorkloadSelector{Scope: scope, DestinationID: agent.DestinationID, ServiceID: export.ServiceID}, ExportID: export.ID, Port: export.Port, Protocol: export.Protocol, IdentityProfile: export.IdentityProfile, HTTPMethod: "GET", HTTPPath: "/orders"})
				if applyErr != nil {
					return applyErr
				}
				if _, revokeErr := store.RevokeGrant(ctx, "o", approved.ID, approved.Generation, "admin", allow); revokeErr != nil {
					return revokeErr
				}
				bus.wake <- struct{}{}
				return nil
			}
			if len(snapshot.Endpoints) != 0 || len(snapshot.Policy.Grants) != 0 || session.Context().Err() == nil {
				t.Fatal("revoked route remained usable")
			}
			return stop
		})
		if !errors.Is(watchErr, stop) || deliveries != 2 || !bus.closed || bus.org != agent.OrgID {
			t.Fatalf("watch: deliveries=%d err=%v", deliveries, watchErr)
		}
	})
	if _, retireErr := store.RetireExport(ctx, "o", export.ID, export.Generation, "admin", allow); retireErr != nil {
		t.Fatal(retireErr)
	}
	prepared, cursor = prepare()
	if _, issueErr := store.IssueSnapshot(ctx, agent, cursor, prepared, nil); issueErr == nil {
		t.Fatal("missing signer accepted")
	}
	before, err := store.Delivery(ctx, agent)
	if err != nil {
		t.Fatal(err)
	}
	// Failed persistence must not release a signed payload or consume a sequence.
	if _, triggerErr := conn.Exec(ctx, `CREATE FUNCTION reject_delivery() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'delivery failed'; END $$; CREATE TRIGGER reject_delivery BEFORE UPDATE ON loomplane_federation_deliveries FOR EACH ROW EXECUTE FUNCTION reject_delivery()`); triggerErr != nil {
		t.Fatal(triggerErr)
	}
	denied, err := store.IssueSnapshot(ctx, agent, cursor, prepared, signer)
	if err == nil || len(denied.Signature) != 0 {
		t.Fatal("uncommitted snapshot released")
	}
	after, err := store.Delivery(ctx, agent)
	if err != nil || after.Sequence != before.Sequence {
		t.Fatal("failed issuance consumed sequence", err)
	}
	if _, triggerErr := conn.Exec(ctx, `DROP TRIGGER reject_delivery ON loomplane_federation_deliveries`); triggerErr != nil {
		t.Fatal(triggerErr)
	}

	t.Run("write cannot outlive delivery lease", func(t *testing.T) {
		preparedSnapshot, snapshotCursor := prepare()
		preparedSnapshot.ExpiresAt = time.Now().Add(domain.MaxClockSkew + time.Second)
		if _, triggerErr := conn.Exec(ctx, `CREATE FUNCTION delay_delivery() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_sleep(2); RETURN NEW; END $$; CREATE TRIGGER delay_delivery BEFORE UPDATE ON loomplane_federation_deliveries FOR EACH ROW EXECUTE FUNCTION delay_delivery()`); triggerErr != nil {
			t.Fatal(triggerErr)
		}
		refused, issueErr := store.IssueSnapshot(ctx, agent, snapshotCursor, preparedSnapshot, signer)
		if _, triggerErr := conn.Exec(ctx, `DROP TRIGGER delay_delivery ON loomplane_federation_deliveries`); triggerErr != nil {
			t.Fatal(triggerErr)
		}
		if issueErr == nil || len(refused.Signature) != 0 {
			t.Fatal("snapshot escaped after safe lease elapsed")
		}
	})
	if _, quarantineErr := conn.Exec(ctx, `UPDATE loomplane_federation_destinations SET quarantined=true WHERE id='d'`); quarantineErr != nil {
		t.Fatal(quarantineErr)
	}
	if _, issueErr := store.IssueSnapshot(ctx, agent, cursor, prepared, signer); issueErr == nil {
		t.Fatal("quarantined destination signed")
	}
	if ackErr := store.AcknowledgeSnapshot(ctx, agent, ack); ackErr == nil {
		t.Fatal("quarantined destination acknowledged")
	}

	if _, deleteErr := conn.Exec(ctx, `DELETE FROM loomplane_federation_observers WHERE org_id='o' AND agent_id='agent'`); deleteErr == nil {
		t.Fatal("observer cleanup erased a live delivery lease")
	}
	if _, expireErr := conn.Exec(ctx, `UPDATE loomplane_federation_deliveries SET state=jsonb_set(state,'{lease_until}',to_jsonb(now()-interval '1 minute')) WHERE org_id='o' AND agent_id='agent'`); expireErr != nil {
		t.Fatal(expireErr)
	}
	if _, deleteErr := conn.Exec(ctx, `DELETE FROM loomplane_federation_observers WHERE org_id='o' AND agent_id='agent'`); deleteErr != nil {
		t.Fatal("expired receipt blocked cleanup", deleteErr)
	}
	var receipts int
	if readErr := conn.QueryRow(ctx, `SELECT count(*) FROM loomplane_federation_deliveries`).Scan(&receipts); readErr != nil || receipts != 0 {
		t.Fatal("expired receipt remained", readErr)
	}
	if migrateErr := ops.Migrate(ctx, dsn, migrations.All()); migrateErr != nil {
		t.Fatal(migrateErr)
	}
}

type deliveryWake struct {
	org    string
	wake   chan struct{}
	closed bool
}

func (b *deliveryWake) Watch(org string) (<-chan struct{}, func()) {
	b.org = org
	return b.wake, func() { b.closed = true }
}
