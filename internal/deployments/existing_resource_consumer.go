package deployments

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/xraph/loomplane/internal/catalogconnect"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/providerresources"
	"github.com/xraph/loomplane/internal/recipes"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/provider"
)

// verifyExistingServiceAccess also runs when the resource's action is Keep.
// A new service release cannot reuse a revoked project grant or stale endpoint.
func (r *Runner) verifyExistingServiceAccess(ctx context.Context, job *Job, bindings map[string]*planning.Binding) error {
	if job.Action.Service == nil {
		return nil
	}
	seen := map[string]bool{}
	for _, link := range job.Action.Service.Env {
		binding := bindings[link.From]
		if link.Subject != planning.ResourceSubject || seen[link.From] {
			continue
		}
		pin, err := approvedExistingServicePin(job, link.From, binding)
		if err != nil {
			return err
		}
		if pin == nil {
			continue
		}
		if job.Action.Service.Workload == provider.StaticSite || job.Action.Service.Execution == provider.Static {
			return errors.New("external resource credentials cannot be sent to a static site's build")
		}
		seen[link.From] = true
		scoped, ok := r.existingResources.(planning.ExistingProjectResourceResolver)
		if !ok {
			return errors.New("the external resource project verifier is unavailable")
		}
		actor := job.Plan.DecidedBy
		if actor == "" {
			actor = job.Plan.CreatedBy
		}
		p := security.ActorPrincipal(job.Deployment.OrgID, actor)
		record, endpoint, err := scoped.VerifyProject(ctx, p, job.Deployment.ProjectID, job.Deployment.EnvironmentID, pin.ResourceID, pin.Revision)
		if err != nil || record == nil || !providerresources.AttachableIdentity(record) || endpoint == nil || record.AccountIdentity != pin.AccountIdentity || record.ExternalID != pin.ExternalID || record.Incarnation != pin.Incarnation || record.EndpointID != pin.EndpointID || record.EndpointRevision != pin.EndpointRevision || record.Revision != pin.Revision || endpoint.Revision != pin.EndpointRevision {
			return errors.New("the external resource project grant or connection changed; review the deployment again")
		}
		if r.existingEndpoints == nil || r.existingDialer == nil {
			return errors.New("the external resource network verifier is unavailable")
		}
		connection := job.Action.Service.Target
		if connection == "" {
			connection = job.Action.ConnectionID
		}
		if connection == "" {
			if target, e := r.providers.Get(job.Action.Provider); e == nil {
				connection = job.Plan.Connections[target.Cloud]
			}
		}
		dial, closer, err := r.existingDialer.TargetDial(ctx, job.Deployment.OrgID, connection)
		if err != nil {
			return errors.New("the deployment network could not verify the external connection")
		}
		report, err := r.existingEndpoints.Check(ctx, job.Deployment.OrgID, pin.EndpointID, recipes.Requirement{Engine: endpoint.Engine, TLS: []string{catalogconnect.TLSRequire, catalogconnect.TLSVerifyFull}}, dial)
		if closer != nil {
			if closeErr := closer.Close(); closeErr != nil && err == nil {
				err = closeErr
			}
		}
		if err != nil || report == nil || len(report.Problems) > 0 || len(report.Missing) > 0 {
			return errors.New("the external connection failed its deployment network check")
		}
	}
	return nil
}

// approvedExistingServicePin fences a retry's captured credentials to the
// resource action approved in that same plan, before current grants are read.
func approvedExistingServicePin(job *Job, name string, binding *planning.Binding) (*planning.ExistingResourceConfig, error) {
	changed := errors.New("the external resource binding no longer matches this plan's approved access; review the deployment again")
	approved := approvedServiceResource(job.Plan, name)
	external := approved != nil && approved.Resource != nil && approved.Resource.ExistingResource != nil
	if !external {
		if planning.ExistingResourceBinding(binding) {
			return nil, changed
		}
		return nil, nil
	}
	if approved.Op == planning.Delete || binding == nil || !planning.ExistingResourceBinding(binding) || binding.Name != name || binding.Provider != approved.Provider || binding.ConnectionID != approved.ConnectionID {
		return nil, changed
	}
	var current planning.ResourceConfig
	if json.Unmarshal(binding.Config, &current) != nil || current.ExistingResource == nil || *current.ExistingResource != *approved.Resource.ExistingResource || current.Kind != approved.Resource.Kind || current.Provider != approved.Resource.Provider || current.AccountIdentity != approved.Resource.AccountIdentity {
		return nil, changed
	}
	return approved.Resource.ExistingResource, nil
}

func approvedServiceResource(plan *planning.Plan, name string) *planning.Action {
	if plan == nil {
		return nil
	}
	for i := range plan.Actions {
		action := &plan.Actions[i]
		if action.Subject == planning.ResourceSubject && action.Name == name {
			return action
		}
	}
	return nil
}
