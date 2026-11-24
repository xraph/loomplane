package deployments

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/xraph/loomplane/internal/catalogconnect"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/providerresources"
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
		actor := job.Plan.DecidedBy
		if actor == "" {
			actor = job.Plan.CreatedBy
		}
		p := security.ActorPrincipal(job.Deployment.OrgID, actor)
		var endpoint *catalogconnect.Endpoint
		tls := cloudTLS
		if workspacePin(pin) {
			// The workspace may have stopped sharing it, or replaced its
			// credentials, since the plan.
			if r.authz == nil {
				return errors.New("external resource authorization is unavailable")
			}
			if err := r.authz.Require(ctx, p, security.DeploymentCreate, job.Deployment.ProjectID); err != nil {
				return err
			}
			resolved, _, err := r.resolveWorkspaceService(ctx, job.Deployment.OrgID, job.Deployment.ProjectID, pin)
			if err != nil {
				return err
			}
			endpoint = resolved
			if endpoint.TLS == catalogconnect.TLSDisable {
				tls = nil
			}
		} else {
			scoped, ok := r.existingResources.(planning.ExistingProjectResourceResolver)
			if !ok {
				return errors.New("the external resource project verifier is unavailable")
			}
			record, verified, err := scoped.VerifyProject(ctx, p, job.Deployment.ProjectID, job.Deployment.EnvironmentID, pin.ResourceID, pin.Revision)
			if err != nil || record == nil || !providerresources.AttachableIdentity(record) || verified == nil || record.AccountIdentity != pin.AccountIdentity || record.ExternalID != pin.ExternalID || record.Incarnation != pin.Incarnation || record.EndpointID != pin.EndpointID || record.EndpointRevision != pin.EndpointRevision || record.Revision != pin.Revision || verified.Revision != pin.EndpointRevision {
				return errors.New("the external resource project grant or connection changed; review the deployment again")
			}
			endpoint = verified
		}
		if err := r.checkReach(ctx, job.Deployment.OrgID, job.Plan, job.Action, endpoint, tls); err != nil {
			return err
		}
		if workspacePin(pin) {
			if err := r.grantOwnAccess(ctx, job, link.From, pin); err != nil {
				return err
			}
		} else if _, own := ownAccessLink(job.Action.Service, link.From); own {
			return fmt.Errorf("%s has a database or user of its own only on a workspace service, and %s isn't one; plan again", job.Action.Name, link.From)
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
