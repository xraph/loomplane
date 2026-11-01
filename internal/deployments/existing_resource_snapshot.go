package deployments

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/xraph/loomplane/internal/catalogconnect"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/providerresources"
	"github.com/xraph/loomplane/internal/security"
)

// verifyExistingSnapshotAccess compares generated access material with the exact
// immutable endpoint approved in this plan. It never refreshes a saved snapshot.
func (r *Runner) verifyExistingSnapshotAccess(ctx context.Context, job *Job, bindings map[string]*planning.Binding) error {
	cfg := job.Action.Service
	if cfg == nil || cfg.AppliedInputs == nil || cfg.AppliedInputs.Variables == "" {
		return nil
	}
	refusal := errors.New("the captured external resource access no longer matches this approved deployment; review the deployment again")
	pins := map[string]*planning.ExistingResourceConfig{}
	for _, link := range cfg.Env {
		if link.Subject != planning.ResourceSubject {
			continue
		}
		pin, err := approvedExistingServicePin(job, link.From, bindings[link.From])
		if err != nil {
			return err
		}
		if pin != nil {
			pins[link.From] = pin
		}
	}
	if len(pins) == 0 {
		return nil
	}
	scoped, ok := r.existingResources.(planning.ExistingProjectResourceResolver)
	if !ok || r.resources == nil || job.Plan == nil || job.Deployment == nil {
		return refusal
	}
	captured, err := r.resources.Get(ctx, cfg.AppliedInputs.Variables)
	if err != nil {
		return refusal
	}
	var snapshot appliedVariables
	if json.Unmarshal(captured, &snapshot) != nil {
		return refusal
	}
	actor := job.Plan.DecidedBy
	if actor == "" {
		actor = job.Plan.CreatedBy
	}
	principal := security.ActorPrincipal(job.Deployment.OrgID, actor)
	expected := map[string]map[string]string{}
	for _, link := range cfg.Env {
		pin := pins[link.From]
		if pin == nil || link.Subject != planning.ResourceSubject {
			continue
		}
		if _, overridden := snapshot.Values[link.Key]; overridden {
			continue
		}
		outputs := expected[link.From]
		if outputs == nil {
			record, endpoint, access, e := scoped.ResolveProject(ctx, principal, job.Deployment.ProjectID, job.Deployment.EnvironmentID, pin.ResourceID, pin.Revision)
			if e != nil || record == nil || !providerresources.AttachableIdentity(record) || endpoint == nil || record.ID != pin.ResourceID || record.OrgID != job.Deployment.OrgID || record.Revision != pin.Revision || record.AccountIdentity != pin.AccountIdentity || record.ExternalID != pin.ExternalID || record.Incarnation != pin.Incarnation || record.EndpointID != pin.EndpointID || record.EndpointRevision != pin.EndpointRevision || endpoint.ID != pin.EndpointID || endpoint.OrgID != job.Deployment.OrgID || endpoint.Revision != pin.EndpointRevision || access == "" {
				return refusal
			}
			outputs = map[string]string{"": access, ResourceURL: access}
			if endpoint.Engine == catalogconnect.EngineS3 || endpoint.Engine == catalogconnect.EngineGCS {
				values, e := catalogconnect.BucketOutputs(access)
				if e != nil {
					return refusal
				}
				outputs = map[string]string{"": values["STORAGE_URL"], ResourceURL: values["STORAGE_URL"]}
				for key, value := range values {
					outputs["output:"+key] = value
				}
			}
			expected[link.From] = outputs
		}
		want, known := outputs[link.Field]
		got, present := snapshot.ResourceValues[link.Key]
		if !known || !present || got != want || strings.TrimSpace(link.Gateway) != "" {
			return refusal
		}
	}
	return nil
}
