package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/xraph/grove/drivers/pgdriver"

	"github.com/xraph/loomplane/database/migrations"
	"github.com/xraph/loomplane/internal/agent"
	"github.com/xraph/loomplane/internal/ops"
	"github.com/xraph/loomplane/internal/runtimeapi"
	"github.com/xraph/loomplane/internal/runtimecatalog"
	"github.com/xraph/loomplane/internal/workloadidentity"
	domain "github.com/xraph/loomplane/pkg/federation"
)

func qualifyPersisted(ctx context.Context, issuer *workloadidentity.SPIREIssuer, binding workloadidentity.Binding, socket string, control *workloadidentity.Source) (err error) {
	dsn := os.Getenv("LOOMPLANE_FIXTURE_DATABASE_URL")
	if dsn == "" {
		return nil
	}
	db := pgdriver.New()
	if stepErr := db.Open(ctx, dsn); stepErr != nil {
		return stepErr
	}
	defer func() { err = errors.Join(err, db.Close()) }()
	if stepErr := until(ctx, func() bool { var one int; return db.QueryRow(ctx, `SELECT 1`).Scan(&one) == nil }); stepErr != nil {
		return stepErr
	}
	if stepErr := ops.Migrate(ctx, dsn, migrations.All()); stepErr != nil {
		return stepErr
	}
	scope := binding.Destination.Scope
	for _, query := range []string{
		`INSERT INTO loomplane_projects(id,org_id,name,slug,status,created_by,created_at,updated_at) VALUES('project','fixture','fixture','fixture','ready','fixture',now(),now())`,
		`INSERT INTO loomplane_environments(id,org_id,project_id,name,kind,created_at,updated_at) VALUES('test','fixture','project','fixture','production',now(),now())`,
		`INSERT INTO loomplane_cloud_connections(id,org_id,cloud,name,account,status,created_by,created_at,updated_at) VALUES('connection','fixture','server','fixture','fixture','ready','fixture',now(),now())`,
	} {
		if _, execErr := db.Exec(ctx, query); execErr != nil {
			return execErr
		}
	}
	physical, err := json.Marshal(binding.Destination.Identity)
	if err != nil {
		return err
	}
	if _, err = db.Exec(ctx, `INSERT INTO loomplane_federation_destinations(id,org_id,project_id,environment_id,name,connection_id,identity,identity_revision,generation,created_at,updated_at) VALUES($1,$2,$3,$4,'fixture','connection',$5,$6,$7,now(),now())`, binding.Destination.ID, scope.OrgID, scope.ProjectID, scope.EnvironmentID, physical, binding.Destination.Identity.Revision(), binding.Destination.Generation); err != nil {
		return err
	}
	enrollments := workloadidentity.NewEnrollmentStore(db, control.Domain())
	bounds := map[string]runtimecatalog.ExportBounds{"export": {Scope: scope, DestinationID: binding.Destination.ID, PlacementID: "placement", BindingID: "binding", Port: 8080, CIDRs: []string{"10.20.0.0/24"}}}
	binding.CheckedAt = time.Now()
	first, err := enrollments.Reserve(ctx, binding, bounds)
	if err != nil {
		return err
	}
	if stepErr := enrollments.Issue(ctx, scope.OrgID, first.ID, issuer); stepErr != nil {
		return stepErr
	}
	// A new store instance reads the exact durable enrollment and retries issuance.
	enrollments = workloadidentity.NewEnrollmentStore(db, control.Domain())
	if stepErr := enrollments.Issue(ctx, scope.OrgID, first.ID, issuer); stepErr != nil {
		return stepErr
	}
	source, err := readySource(ctx, socket, first.Binding.Agent.URI)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, source.Close()) }()
	registry := runtimecatalog.NewPgStore(db)
	catalog := runtimecatalog.New(registry, func(context.Context, domain.AgentIdentity) (runtimecatalog.Visibility, error) {
		return runtimecatalog.Visibility{Revision: "fixture-policy", ExpiresAt: time.Now().Add(time.Minute), Exports: map[string]domain.Scope{"export": scope}}, nil
	}, nil)
	handler, err := runtimeapi.NewHandler(scope.OrgID, control, registry, enrollments, catalog, noWake{})
	if err != nil {
		return err
	}
	endpoint, closeServer, err := startRuntime(ctx, handler, control)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, closeServer()) }()
	audience, err := workloadidentity.ControlURI(first.Binding.Agent)
	if err != nil {
		return err
	}
	client, err := agent.NewTransport(endpoint, audience, source, first.Binding.Agent)
	if err != nil {
		return err
	}
	defer client.Close()
	if stepErr := client.Enroll(ctx, first.ID); stepErr != nil {
		return stepErr
	}
	if replayErr := client.Enroll(ctx, first.ID); !errors.Is(replayErr, agent.ErrClosed) {
		return errors.Join(errors.New("persisted bootstrap replay was accepted"), replayErr)
	}
	stateDirectory, err := os.MkdirTemp("", "loomplane-enrolled-state-")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(stateDirectory)) }()
	statePath := filepath.Join(stateDirectory, "agent", "state.db")
	state, err := agent.OpenStore(statePath)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, state.Close()) }()
	if stepErr := state.Activate(first.Binding.Agent); stepErr != nil {
		return stepErr
	}
	sequence, err := state.NextSequence(ctx)
	if err != nil {
		return err
	}
	if sequence != 1 {
		return errors.New("new epoch did not start at sequence one")
	}
	endpointRecord := domain.Endpoint{Metadata: domain.Metadata{Scope: scope, ID: "endpoint", Generation: 1}, DestinationID: binding.Destination.ID, ExportID: "export", PlacementID: "placement", BindingID: "binding", ObserverID: first.Binding.Agent.AgentID, Address: "10.20.0.2", Port: 8080, Readiness: domain.Ready}
	batch := domain.ObservationBatch{Agent: first.Binding.Agent, DestinationGeneration: binding.Destination.Generation, Sequence: sequence, Full: true, Endpoints: []domain.Endpoint{endpointRecord}}
	if stepErr := client.Publish(ctx, batch); stepErr != nil {
		return stepErr
	}
	received := errors.New("received persisted endpoint")
	watchErr := client.Watch(ctx, func(_ context.Context, snapshot domain.Snapshot) error {
		if len(snapshot.Endpoints) != 1 || snapshot.Endpoints[0].Address != "10.20.0.2" {
			return errors.New("persisted endpoint missing from live snapshot")
		}
		return received
	})
	if !errors.Is(watchErr, received) {
		return fmt.Errorf("persisted snapshot: %w", watchErr)
	}
	restored, err := agent.OpenStore(statePath)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, restored.Close()) }()
	if _, sequenceErr := restored.NextSequence(ctx); !errors.Is(sequenceErr, agent.ErrClosed) {
		return errors.New("restored process reopened authority")
	}
	if activateErr := restored.Activate(first.Binding.Agent); !errors.Is(activateErr, agent.ErrClosed) {
		return errors.New("restored process reused epoch")
	}
	binding.CheckedAt = time.Now()
	second, err := enrollments.Reserve(ctx, binding, bounds)
	if err != nil {
		return err
	}
	if second.Binding.Agent.Epoch <= first.Binding.Agent.Epoch {
		return errors.New("persistent enrollment epoch did not increase")
	}
	if stepErr := enrollments.Issue(ctx, scope.OrgID, second.ID, issuer); stepErr != nil {
		return stepErr
	}
	replacementSource, err := readySource(ctx, socket, second.Binding.Agent.URI)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, replacementSource.Close()) }()
	replacement, err := agent.NewTransport(endpoint, audience, replacementSource, second.Binding.Agent)
	if err != nil {
		return err
	}
	defer replacement.Close()
	if stepErr := replacement.Enroll(ctx, second.ID); stepErr != nil {
		return stepErr
	}
	if stepErr := restored.Activate(second.Binding.Agent); stepErr != nil {
		return stepErr
	}
	if _, sequenceErr := state.NextSequence(ctx); !errors.Is(sequenceErr, agent.ErrClosed) {
		return errors.New("replaced process retained SQLite authority")
	}
	// Confirm the old SVID still exists, so denial proves registry fencing.
	if _, credentialErr := source.GetX509SVID(); credentialErr != nil {
		return credentialErr
	}
	if deniedErr := client.Publish(ctx, batch); !errors.Is(deniedErr, agent.ErrClosed) {
		return errors.Join(errors.New("old SVID retained runtime access after replacement"), deniedErr)
	}
	if stepErr := enrollments.Revoke(ctx, scope.OrgID, second.ID, issuer); stepErr != nil {
		return stepErr
	}
	if deniedErr := registry.CheckAgent(ctx, second.Binding.Agent); deniedErr == nil {
		return errors.New("revoked agent remained in active registry")
	}
	var references int
	if stepErr := db.QueryRow(ctx, `SELECT count(*) FROM loomplane_federation_destination_refs WHERE org_id=$1 AND owner_id=$2 AND kind='trust'`, scope.OrgID, second.ID).Scan(&references); stepErr != nil {
		return stepErr
	}
	if references != 0 {
		return errors.New("confirmed SPIRE deletion retained trust cleanup reference")
	}
	if stepErr := enrollments.Revoke(ctx, scope.OrgID, first.ID, issuer); stepErr != nil {
		return stepErr
	}
	fmt.Println("PASS persisted enrollment and endpoint discovery over live SPIRE mTLS; replay, restore, replaced epoch and revoked authority denied")
	return nil
}
