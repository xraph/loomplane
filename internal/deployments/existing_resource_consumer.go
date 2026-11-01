package deployments

import (
	"context"
	"errors"

	"github.com/xraph/loomplane/internal/catalogconnect"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/recipes"
	"github.com/xraph/loomplane/internal/security"
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
		if link.Subject != planning.ResourceSubject || !planning.ExistingResourceBinding(binding) || seen[link.From] {
			continue
		}
		seen[link.From] = true
		pin, err := decodeExistingBinding(binding)
		if err != nil {
			return err
		}
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
		if err != nil || record == nil || endpoint == nil || record.AccountIdentity != pin.AccountIdentity || record.ExternalID != pin.ExternalID || record.Incarnation != pin.Incarnation || record.EndpointID != pin.EndpointID || record.EndpointRevision != pin.EndpointRevision || record.Revision != pin.Revision || endpoint.Revision != pin.EndpointRevision {
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
