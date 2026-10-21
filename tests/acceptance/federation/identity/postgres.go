package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/xraph/grove/drivers/pgdriver"

	"github.com/xraph/loomplane/database/migrations"
	"github.com/xraph/loomplane/internal/access"
	"github.com/xraph/loomplane/internal/agent"
	"github.com/xraph/loomplane/internal/config"
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
	policies := access.NewPolicyStore(db)
	allow := func(context.Context) error { return nil }
	export, err := policies.PutExport(ctx, domain.ServiceExport{Metadata: domain.Metadata{Scope: scope, ID: "export"}, ServiceID: "fixture-api", DestinationIDs: []string{binding.Destination.ID}, Port: 8080, Protocol: domain.HTTP, IdentityProfile: domain.WorkloadIdentity}, 0, "fixture", allow)
	if err != nil {
		return err
	}
	proposed, err := policies.ProposeGrant(ctx, domain.AccessGrant{Metadata: domain.Metadata{Scope: scope, ID: "fixture-grant"}, Source: domain.WorkloadSelector{Scope: scope, DestinationID: binding.Destination.ID, ServiceID: "fixture-client"}, ExportID: export.ID, ExportRevision: export.Revision, Port: export.Port, Protocol: export.Protocol}, 0, "fixture", allow)
	if err != nil {
		return err
	}
	approved, err := policies.ApproveGrant(ctx, scope.OrgID, proposed.ID, proposed.Generation, proposed.Revision, "fixture", allow)
	if err != nil {
		return err
	}
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	signer, err := access.NewSnapshotSigner(scope, private, now.Add(-time.Minute), now.Add(10*time.Minute))
	if err != nil {
		return err
	}
	signingTrust, err := access.NewSnapshotTrust(scope, 1, []access.SnapshotKey{signer.Key()})
	if err != nil {
		return err
	}
	keyDirectory, err := os.MkdirTemp("", "loomplane-runtime-keys-")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(keyDirectory)) }()
	keyFile := filepath.Join(keyDirectory, "signing.json")
	keyBody, err := json.Marshal(map[string]any{"version": 1, "keys": []any{map[string]any{"org_id": scope.OrgID, "project_id": scope.ProjectID, "environment_id": scope.EnvironmentID, "private_key": base64.StdEncoding.EncodeToString(private), "not_before": signer.Key().NotBefore, "not_after": signer.Key().NotAfter}}})
	if err != nil {
		return err
	}
	if stepErr := os.WriteFile(keyFile, keyBody, 0o600); stepErr != nil {
		return stepErr
	}
	runtime, err := runtimeapi.NewRuntime(config.FederationRuntimeConfig{Enabled: true, ListenAddress: "127.0.0.1:9443", OrganizationID: scope.OrgID, TrustDomain: control.Domain(), WorkloadAPI: socket, SigningKeysFile: keyFile}, db, noWake{})
	if err != nil {
		return err
	}
	if stepErr := runtime.Start(ctx); stepErr != nil {
		return stepErr
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		err = errors.Join(err, runtime.Stop(cleanup))
	}()
	if stepErr := runtime.Health(ctx); stepErr != nil {
		return stepErr
	}
	endpoint := "https://" + runtime.Address()
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
	receiver, err := agent.NewSnapshotReceiver(state, first.Binding.Agent, signingTrust)
	if err != nil {
		return err
	}
	installed, err := receivePersistedPolicy(ctx, client, receiver, 1)
	if err != nil {
		return err
	}
	receipt, err := policies.Delivery(ctx, first.Binding.Agent)
	if err != nil {
		return err
	}
	if receipt.AcknowledgedSequence != installed.Sequence || receipt.AcknowledgedPolicyGeneration != receipt.PolicyGeneration {
		return errors.New("live acknowledgment was not persisted")
	}
	session, err := receiver.Open(ctx, access.Request{Source: proposed.Source, Target: domain.WorkloadSelector{Scope: scope, DestinationID: binding.Destination.ID, ServiceID: export.ServiceID}, ExportID: export.ID, Port: export.Port, Protocol: export.Protocol, IdentityProfile: export.IdentityProfile, HTTPMethod: "GET", HTTPPath: "/fixture"})
	if err != nil {
		return err
	}
	defer session.Close()
	if _, revokeErr := policies.RevokeGrant(ctx, scope.OrgID, approved.ID, approved.Generation, "fixture", allow); revokeErr != nil {
		return revokeErr
	}
	if _, receiveErr := receivePersistedPolicy(ctx, client, receiver, 0); receiveErr != nil {
		return receiveErr
	}
	if session.Context().Err() == nil {
		return errors.New("live signed revocation retained the local session")
	}
	receipt, err = policies.Delivery(ctx, first.Binding.Agent)
	if err != nil {
		return err
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
	// The old enrollment retains trust references until all issued leases end.
	if waitErr := until(ctx, func() bool { return !receipt.LeaseUntil.After(time.Now()) }); waitErr != nil {
		return waitErr
	}
	if stepErr := enrollments.Revoke(ctx, scope.OrgID, first.ID, issuer); stepErr != nil {
		return stepErr
	}
	if stepErr := qualifyAgentExecutable(ctx, enrollments, issuer, binding, endpoint, socket, signer, policies); stepErr != nil {
		return stepErr
	}
	fmt.Println("PASS persisted enrollment, signed policy, acknowledgment and session revocation over live SPIRE mTLS; replay, restore and replaced epoch denied")
	return nil
}

func receivePersistedPolicy(ctx context.Context, client *agent.Transport, receiver *agent.SnapshotReceiver, want int) (domain.Acknowledgment, error) {
	received := errors.New("received and acknowledged signed policy")
	var ack domain.Acknowledgment
	err := client.Watch(ctx, func(delivery context.Context, snapshot domain.Snapshot) error {
		if len(snapshot.Endpoints) != want || len(snapshot.Policy.Grants) != want {
			return errors.New("unexpected live policy contents")
		}
		if want > 0 && snapshot.Endpoints[0].Address != "10.20.0.2" {
			return errors.New("persisted endpoint missing from live snapshot")
		}
		var applyErr error
		ack, applyErr = receiver.Apply(delivery, snapshot)
		if applyErr != nil {
			return applyErr
		}
		if ackErr := client.Acknowledge(delivery, ack); ackErr != nil {
			return ackErr
		}
		return received
	})
	if !errors.Is(err, received) {
		return domain.Acknowledgment{}, fmt.Errorf("persisted signed snapshot: %w", err)
	}
	return ack, nil
}
