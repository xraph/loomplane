package deployments

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"

	"github.com/xraph/loomplane/internal/clouds/gcp"
	"github.com/xraph/loomplane/internal/planning"
)

// ResolvePendingCloudResource recovers original creation evidence without
// provisioning later stages or publishing a usable database binding.
func (c *CloudSQL) ResolvePendingCloudResource(ctx context.Context, job *Job) (*PendingCloudIdentity, error) {
	state, key, err := readPendingCloudSQL(job)
	if err != nil {
		return nil, err
	}
	if state.Stage == "instance" && !state.Uncertain && state.ResourceID == "" {
		raw, encodeErr := json.Marshal(state)
		return &PendingCloudIdentity{Proof: raw}, encodeErr
	}
	client, err := c.Google.Session(ctx, key)
	if err != nil {
		return nil, err
	}
	cfg := job.Action.Resource
	if recoverErr := state.recoverCreate(ctx, client, sqlVersions[cfg.Kind+"/"+cfg.Version]); recoverErr != nil {
		return nil, recoverErr
	}
	if state.Uncertain {
		kind := map[string]string{"instance": "CREATE", "database": "CREATE_DATABASE", "user": "CREATE_USER"}[state.Stage]
		if awaitErr := client.AwaitSQLMutation(ctx, state.Scope.Instance, state.Operation, kind, state.guarded(client, true).Check); awaitErr != nil {
			return nil, awaitErr
		}
		next := map[string]string{"instance": "database", "database": "user", "user": "ready"}[state.Stage]
		if checkErr := state.advance(next); checkErr != nil {
			return nil, checkErr
		}
	}
	if checkErr := state.guarded(client, true).Check(ctx); checkErr != nil {
		return nil, checkErr
	}
	if state.PSC != nil {
		owned := state.PSC.Provision
		endpoint, reconcileErr := client.ReconcileSQLPSCSourceEndpoint(ctx, cloudSQLProvisionSpec(cfg, state.Scope.Instance, state.ResourceID), &owned, func(next gcp.SQLPSCProvisionState) error {
			copied := *state.PSC
			copied.Provision = next
			state.PSC = &copied
			return state.save()
		}, state.pscCheck)
		if reconcileErr != nil {
			return nil, reconcileErr
		}
		if state.PSC.Endpoint != (gcp.SQLPSCProvisionedEndpoint{}) && state.PSC.Endpoint != *endpoint {
			return nil, errCloudSQLPSCOwnership
		}
		state.PSC.Endpoint = *endpoint
		if checkErr := state.save(); checkErr != nil {
			return nil, checkErr
		}
	}
	raw, err := json.Marshal(state)
	if err != nil {
		return nil, err
	}
	owned, err := state.ownership(nil)
	if err != nil {
		return nil, err
	}
	identity := &PendingCloudIdentity{ExternalID: key.ProjectID + ":" + cfg.Region + ":" + state.Scope.Instance, PhysicalID: state.ResourceID, Proof: raw}
	if owned != "" {
		if checkErr := json.Unmarshal([]byte(owned), &identity.OwnedResources); checkErr != nil {
			return nil, checkErr
		}
	}
	return identity, nil
}

func readPendingCloudSQL(job *Job) (*cloudSQLState, *gcp.Key, error) {
	if job == nil || job.Binding != nil || job.Action.Resource == nil {
		return nil, nil, errors.New("pending Cloud SQL cleanup requires the original unbound action")
	}
	if _, err := managedDatabaseRequest(job, "cloud-sql"); err != nil {
		return nil, nil, err
	}
	key, err := gcp.ParseKey(job.Credential)
	if err != nil {
		return nil, nil, err
	}
	state, err := readCloudSQLState(job, job.Action.Resource, key.ProjectID, job.Action.Resource.Region, cloudSQLInstance(job), "apply")
	return state, key, err
}

// RemovePendingCloudResource consumes saved cleanup evidence. It never resolves
// ownership from the source name after SQL deletion has started.
func (c *CloudSQL) RemovePendingCloudResource(ctx context.Context, job *Job, identity *PendingCloudIdentity) error {
	if identity == nil || !json.Valid(identity.Proof) {
		return errors.New("pending Cloud SQL cleanup proof is missing")
	}
	original := *job
	original.Binding = nil
	original.Resume = identity.Proof
	state, key, err := readPendingCloudSQL(&original)
	if err != nil {
		return err
	}
	expected, err := state.ownership(nil)
	if err != nil {
		return err
	}
	owned, err := json.Marshal(identity.OwnedResources)
	if err != nil {
		return err
	}
	if expected == "" {
		expected = "null"
	}
	if !bytes.Equal([]byte(expected), owned) {
		return errCloudSQLPSCOwnership
	}
	if identity.ExternalID == "" {
		if state.Stage != "instance" || state.Uncertain || state.ResourceID != "" || state.PSC != nil && state.PSC.Provision != (gcp.SQLPSCProvisionState{}) {
			return errors.New("empty pending identity has creation evidence")
		}
		return nil
	}
	cfg := job.Action.Resource
	if state.Uncertain || state.ResourceID == "" || state.ResourceID != identity.PhysicalID || identity.ExternalID != key.ProjectID+":"+cfg.Region+":"+state.Scope.Instance {
		return errors.New("pending Cloud SQL identity differs from its original checkpoint")
	}
	if state.PSC != nil {
		client, sessionErr := c.Google.Session(ctx, key)
		if sessionErr != nil {
			return sessionErr
		}
		if checkErr := client.ValidateSQLPSCCleanupOwnership(cloudSQLProvisionSpec(cfg, state.Scope.Instance, state.ResourceID), state.PSC.Provision, state.PSC.Endpoint); checkErr != nil {
			return checkErr
		}
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	bound := &planning.Binding{Provider: "cloud-sql", ConnectionID: job.Connection.ID, Region: cfg.Region, ExternalID: identity.ExternalID, PhysicalID: identity.PhysicalID, OwnedResources: identity.OwnedResources, Config: raw}
	removing := *job
	removing.pendingIdentity = identity
	return c.Remove(ctx, &removing, bound)
}

func (s *cloudSQLState) restorePendingPSC(cfg *planning.ResourceConfig, b *planning.Binding, resumed bool, identity *PendingCloudIdentity) error {
	if !ownedCloudSQLPSC(cfg) {
		return s.restorePSC(cfg, b, resumed)
	}
	var proof cloudSQLState
	if err := decodeDatabaseCheckpoint(identity.Proof, &proof); err != nil {
		return err
	}
	kept := proof.PSC
	if kept == nil || kept.Version != 1 || kept.Route != cfg.Managed.CloudSQLPSC || proof.ResourceID != b.PhysicalID || kept.Provision.SourceProject != s.Scope.Project || kept.Provision.SourcePhysicalID != b.PhysicalID {
		return errCloudSQLPSCOwnership
	}
	if resumed {
		if s.Version != 4 || s.PSC == nil || *s.PSC != *kept {
			return errCloudSQLPSCOwnership
		}
	} else {
		s.Version = 4
		copied := *kept
		s.PSC = &copied
	}
	if s.PSCCleanup != nil && s.Stage != "removed" {
		return errCloudSQLPSCOwnership
	}
	return nil
}
