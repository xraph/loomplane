package deployments

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"github.com/xraph/loomplane/internal/catalogconnect"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/providerresources"
	"github.com/xraph/loomplane/internal/recipes"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/provider"
)

func (r *Runner) runExistingResource(ctx context.Context, d *Deployment, plan *planning.Plan, a planning.Action, current *planning.Binding, step *Step, progress func(string, string)) error {
	if r.authz == nil {
		return errors.New("external resource authorization is unavailable")
	}
	actor := plan.DecidedBy
	if actor == "" {
		actor = plan.CreatedBy
	}
	p := security.ActorPrincipal(d.OrgID, actor)
	if err := r.authz.Require(ctx, p, security.DeploymentCreate, d.ProjectID); err != nil {
		return err
	}
	if verifier, ok := r.authz.(interface {
		RequireCurrentMembership(context.Context, security.Principal) error
	}); ok {
		if err := verifier.RequireCurrentMembership(ctx, p); err != nil {
			return err
		}
	}
	if current != nil && (!planning.ExistingResourceBinding(current) || len(current.Replaced) > 0) {
		return errors.New("external attachment cannot replace an owned resource or unresolved lifecycle history")
	}
	if a.Op == planning.Delete {
		if current == nil {
			return nil
		}
		progress(StepDeploying, "Disconnecting "+a.Name+"; the cloud resource remains unchanged")
		if clearErr := r.clearExistingAccess(ctx, d, a.Name); clearErr != nil {
			return clearErr
		}
		return r.plans.DeleteBinding(ctx, d.OrgID, current.ID)
	}
	if a.Resource == nil || a.Resource.ExistingResource == nil || r.existingResources == nil || r.resources == nil {
		return errors.New("the external attachment's verified access contract is unavailable")
	}
	pin := a.Resource.ExistingResource
	var record *providerresources.ProviderResource
	var endpoint *catalogconnect.Endpoint
	var dsn string
	var err error
	if current != nil {
		if scoped, ok := r.existingResources.(planning.ExistingProjectResourceResolver); ok {
			record, endpoint, dsn, err = scoped.ResolveProject(ctx, p, d.ProjectID, d.EnvironmentID, pin.ResourceID, pin.Revision)
		}
	}
	if record == nil {
		record, endpoint, dsn, err = r.existingResources.Resolve(ctx, p, pin.ResourceID, pin.Revision)
	}
	if err != nil {
		return errors.New("the external resource or its access changed; sync and review the deployment again")
	}
	if record == nil || !providerresources.AttachableIdentity(record) || endpoint == nil || record.OrgID != d.OrgID || record.ID != pin.ResourceID || record.Revision != pin.Revision || record.AccountIdentity != pin.AccountIdentity || record.ExternalID != pin.ExternalID || record.Incarnation != pin.Incarnation || record.EndpointID != pin.EndpointID || endpoint.ID != pin.EndpointID || endpoint.Revision != pin.EndpointRevision || record.EndpointRevision != pin.EndpointRevision || dsn == "" {
		return errors.New("the external resource identity or credential revision changed; review the deployment again")
	}
	for _, consumer := range plan.Actions {
		if consumer.Service == nil || consumer.Op == planning.Delete {
			continue
		}
		uses := false
		for _, link := range consumer.Service.Env {
			if link.Subject == planning.ResourceSubject && link.From == a.Name {
				uses = true
				break
			}
		}
		if !uses {
			continue
		}
		if consumer.Service.Workload == provider.StaticSite || consumer.Service.Execution == provider.Static {
			return errors.New("external resource credentials cannot be sent to a static site's build")
		}
		if r.existingEndpoints == nil || r.existingDialer == nil {
			return errors.New("the external resource network verifier is unavailable")
		}
		connection := consumer.Service.Target
		if connection == "" {
			connection = consumer.ConnectionID
		}
		if connection == "" {
			if target, e := r.providers.Get(consumer.Provider); e == nil {
				connection = plan.Connections[target.Cloud]
			}
		}
		dial, closer, e := r.existingDialer.TargetDial(ctx, d.OrgID, connection)
		if e != nil {
			return errors.New("the deployment network could not verify the external connection")
		}
		report, e := r.existingEndpoints.Check(ctx, d.OrgID, pin.EndpointID, recipes.Requirement{Engine: endpoint.Engine, TLS: []string{catalogconnect.TLSRequire, catalogconnect.TLSVerifyFull}}, dial)
		if closer != nil {
			if closeErr := closer.Close(); closeErr != nil && e == nil {
				e = closeErr
			}
		}
		if e != nil || report == nil || len(report.Problems) > 0 || len(report.Missing) > 0 {
			return errors.New("the external connection failed its deployment network check")
		}
	}
	progress(StepDeploying, "Connecting "+a.Name+" without changing its cloud resource")
	var storeErr error
	if catalogconnect.IsAPIEngine(endpoint.Engine) || endpoint.Engine == catalogconnect.EngineR2 {
		storeErr = r.storeAPIExistingAccess(ctx, d, a, record, endpoint, dsn)
	} else {
		storeErr = r.storeExistingAccess(ctx, d, a, dsn)
	}
	if storeErr != nil {
		return storeErr
	}
	out := &Outcome{ExternalID: record.ExternalID, PhysicalID: record.Incarnation, Revision: existingRevision(pin.Revision, pin.EndpointRevision)}
	if _, err := r.bind(ctx, d, a, current, out, record.ConnectionID, nil); err != nil {
		return err
	}
	step.Revision = out.Revision
	return nil
}

func existingRevision(revision int64, endpointRevision int) string {
	return "attachment:" + strconv.FormatInt(revision, 10) + ":" + strconv.Itoa(endpointRevision)
}

func decodeExistingBinding(b *planning.Binding) (*planning.ExistingResourceConfig, error) {
	var cfg planning.ResourceConfig
	if b == nil || json.Unmarshal(b.Config, &cfg) != nil || cfg.ExistingResource == nil {
		return nil, fmt.Errorf("the external attachment metadata is invalid")
	}
	return cfg.ExistingResource, nil
}
