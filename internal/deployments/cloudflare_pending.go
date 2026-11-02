package deployments

import (
	"context"
	"encoding/json"
	"errors"
	"slices"

	"github.com/xraph/loomplane/internal/clouds/cloudflare"
	"github.com/xraph/loomplane/internal/planning"
)

// ResolvePendingCloudResource returns original native identity evidence after an interrupted apply.
func (c *Cloudflare) ResolvePendingCloudResource(ctx context.Context, job *Job) (*PendingCloudIdentity, error) {
	if len(job.Resume) == 0 {
		return &PendingCloudIdentity{Proof: json.RawMessage(`{"absent":true}`)}, nil
	}
	account, err := cloudflareAccount(job.Connection)
	if err != nil {
		return nil, err
	}
	if job.Action.Resource != nil {
		var state cloudflareResourceState
		kind, kindErr := cloudflareResourceKind(job.Action.Resource.Kind)
		if kindErr != nil || json.Unmarshal(job.Resume, &state) != nil || state.Account != account || state.Kind != kind || state.Name != CloudflareWorkerName(job) || state.Jurisdiction != cloudflareResourceJurisdiction(kind, job.Action.Resource.Settings) {
			return nil, errors.New("Cloudflare resource cleanup checkpoint scope changed")
		}
		if state.ID == "" {
			if state.Pending {
				return nil, errors.New("Cloudflare creation result is unresolved; cleanup cannot adopt a resource by name")
			}
			return &PendingCloudIdentity{Proof: append(json.RawMessage(nil), job.Resume...)}, nil
		}
		resource, getErr := c.Client.GetResourceInJurisdiction(ctx, string(job.Credential), account, kind, state.ID, state.Jurisdiction)
		if cloudflareNotFound(getErr) {
			return &PendingCloudIdentity{Proof: append(json.RawMessage(nil), job.Resume...)}, nil
		}
		if getErr != nil {
			return nil, getErr
		}
		if state.PhysicalID == "" || state.PhysicalID != cloudflareResourcePhysical(resource) {
			return nil, errors.New("Cloudflare resource cleanup incarnation changed")
		}
		return &PendingCloudIdentity{ExternalID: state.ID, PhysicalID: state.PhysicalID, Proof: append(json.RawMessage(nil), job.Resume...)}, nil
	}
	var state cloudflareWorkerState
	if json.Unmarshal(job.Resume, &state) != nil || state.Account != account || state.Script != CloudflareWorkerName(job) || state.Owner != cloudflareOwner(job.Deployment.OrgID, job.Deployment.ProjectID, job.Deployment.EnvironmentID, job.Action.Name) || state.Configuration != cloudflareConfigurationDigest(job.Action.Name, job.Action.Service) {
		return nil, errors.New("Cloudflare Worker cleanup checkpoint scope changed")
	}
	settings, getErr := c.Client.GetWorkerSettings(ctx, string(job.Credential), account, state.Script)
	if cloudflareNotFound(getErr) {
		return &PendingCloudIdentity{Proof: append(json.RawMessage(nil), job.Resume...)}, nil
	}
	if getErr != nil {
		return nil, getErr
	}
	if !slices.Contains(settings.Tags, state.Owner) || !slices.Contains(settings.Tags, state.Release) {
		return nil, errors.New("Cloudflare Worker cleanup ownership or release changed")
	}
	native, getErr := c.Client.GetWorkerScript(ctx, string(job.Credential), account, state.Script)
	if getErr != nil {
		return nil, getErr
	}
	if native.CreatedAt == "" || state.CreatedAt != "" && native.CreatedAt != state.CreatedAt {
		return nil, errors.New("Cloudflare Worker cleanup incarnation changed")
	}
	state.CreatedAt = native.CreatedAt
	if len(state.CreatedClasses) > 0 || len(state.Namespaces) > 0 {
		namespaces, listErr := c.Client.ListResources(ctx, string(job.Credential), account, cloudflare.ResourceDurableObject)
		if listErr != nil {
			return nil, listErr
		}
		if receiptErr := cloudflareConfirmNamespaces(&state, namespaces); receiptErr != nil {
			return nil, receiptErr
		}
	}
	ownedJSON, marshalErr := cloudflareWorkerOwnedJSON(state)
	if marshalErr != nil {
		return nil, marshalErr
	}
	var owned map[string]json.RawMessage
	if decodeErr := json.Unmarshal([]byte(ownedJSON), &owned); decodeErr != nil {
		return nil, decodeErr
	}
	proof, err := json.Marshal(state)
	if err != nil {
		return nil, err
	}
	return &PendingCloudIdentity{ExternalID: state.Script, PhysicalID: cloudflarePhysical(state.Script, state.CreatedAt), OwnedResources: owned, Proof: proof}, nil
}

// RemovePendingCloudResource verifies the saved receipt before removing an unfinished resource.
func (c *Cloudflare) RemovePendingCloudResource(ctx context.Context, job *Job, identity *PendingCloudIdentity) error {
	if identity == nil || !json.Valid(identity.Proof) {
		return errors.New("Cloudflare pending cleanup requires original identity evidence")
	}
	if identity.ExternalID == "" {
		return nil
	}
	copyJob := *job
	copyJob.Resume = append([]byte(nil), identity.Proof...)
	current, err := c.ResolvePendingCloudResource(ctx, &copyJob)
	if err != nil {
		return err
	}
	if current.ExternalID == "" {
		return nil
	}
	if current.ExternalID != identity.ExternalID || current.PhysicalID != identity.PhysicalID {
		return errors.New("Cloudflare pending cleanup identity changed")
	}
	subject := planning.ServiceSubject
	var config any = job.Action.Service
	if job.Action.Resource != nil {
		subject = planning.ResourceSubject
		config = job.Action.Resource
	}
	raw, err := json.Marshal(config)
	if err != nil {
		return err
	}
	binding := &planning.Binding{OrgID: job.Deployment.OrgID, ProjectID: job.Deployment.ProjectID, EnvironmentID: job.Deployment.EnvironmentID, Name: job.Action.Name, Subject: subject, ExternalID: identity.ExternalID, PhysicalID: identity.PhysicalID, Config: raw}
	return c.Remove(ctx, &copyJob, binding)
}
