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
	if a.Resource == nil || a.Resource.ExistingResource == nil || r.resources == nil {
		return errors.New("the external attachment's verified access contract is unavailable")
	}
	pin := a.Resource.ExistingResource
	if workspacePin(pin) {
		return r.runWorkspaceService(ctx, d, plan, a, current, step, progress, pin)
	}
	if r.existingResources == nil {
		return errors.New("the external attachment's verified access contract is unavailable")
	}
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
	if err := r.checkConsumersReach(ctx, d, plan, a, endpoint, cloudTLS); err != nil {
		return err
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

// cloudTLS is how a cloud resource's credentials may travel: over TLS. A
// workspace service may run on a private network without it.
var cloudTLS = []string{catalogconnect.TLSRequire, catalogconnect.TLSVerifyFull}

// checkConsumersReach checks the endpoint from the target of each service
// in the plan that gets the attachment's credentials, before any of them
// do. A static site's build never gets them.
func (r *Runner) checkConsumersReach(ctx context.Context, d *Deployment, plan *planning.Plan, a planning.Action, endpoint *catalogconnect.Endpoint, tls []string) error {
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
		if err := r.checkReach(ctx, d.OrgID, plan, consumer, endpoint, tls); err != nil {
			return err
		}
	}
	return nil
}

// checkReach checks an endpoint from the target one service deploys to.
func (r *Runner) checkReach(ctx context.Context, org string, plan *planning.Plan, consumer planning.Action, endpoint *catalogconnect.Endpoint, tls []string) error {
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
	dial, closer, err := r.existingDialer.TargetDial(ctx, org, connection)
	if err != nil {
		return errors.New("the deployment network could not verify the external connection")
	}
	report, err := r.existingEndpoints.Check(ctx, org, endpoint.ID, recipes.Requirement{Engine: endpoint.Engine, TLS: tls}, dial)
	if closer != nil {
		if closeErr := closer.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	}
	if err != nil || report == nil || len(report.Problems) > 0 || len(report.Missing) > 0 {
		return errors.New("the external connection failed its deployment network check")
	}
	return nil
}

// workspacePin reports whether an attachment is of a workspace service: an
// endpoint registered by its connection string, with no provider resource.
func workspacePin(pin *planning.ExistingResourceConfig) bool {
	return pin != nil && pin.ResourceID == "" && catalogconnect.IsEndpointID(pin.EndpointID)
}

// resolveWorkspaceService reads an attached workspace service and its
// connection string, as the plan pinned it: still registered, still shared
// with the project, and at the revision the plan saw. Replaced credentials
// mean planning again, so nobody deploys ones they didn't review.
func (r *Runner) resolveWorkspaceService(ctx context.Context, org, projectID string, pin *planning.ExistingResourceConfig) (*catalogconnect.Endpoint, string, error) {
	if r.workspaceEndpoints == nil {
		return nil, "", errors.New("workspace services are unavailable on this Loomplane server")
	}
	endpoint, dsn, err := r.workspaceEndpoints.Resolve(ctx, org, pin.EndpointID)
	switch {
	case errors.Is(err, catalogconnect.ErrNotFound):
		return nil, "", errors.New("the workspace service was removed; use another one and plan again")
	case err != nil:
		return nil, "", errors.New("the workspace service couldn't be read; try the deployment again")
	case !endpoint.Registered():
		return nil, "", errors.New("the attached endpoint belongs to a provider resource; review the deployment again")
	case !endpoint.SharedWith(projectID):
		return nil, "", fmt.Errorf("%s isn't shared with this project any more; ask a workspace admin to share it, or plan again", endpoint.Name)
	case endpoint.Revision != pin.EndpointRevision || dsn == "":
		return nil, "", fmt.Errorf("%s's credentials were replaced since this plan; plan again to use the new ones", endpoint.Name)
	}
	return endpoint, dsn, nil
}

// runWorkspaceService connects an attached workspace service: it checks
// the service from each consumer's target, keeps its connection string
// as the environment's copy of the resource's address, and binds it.
// Nothing on the service changes, and removing the attachment only
// forgets the copy.
func (r *Runner) runWorkspaceService(ctx context.Context, d *Deployment, plan *planning.Plan, a planning.Action, current *planning.Binding, step *Step, progress func(string, string), pin *planning.ExistingResourceConfig) error {
	endpoint, dsn, err := r.resolveWorkspaceService(ctx, d.OrgID, d.ProjectID, pin)
	if err != nil {
		return err
	}
	var tls []string
	if endpoint.TLS != catalogconnect.TLSDisable {
		tls = cloudTLS
	}
	if err := r.checkConsumersReach(ctx, d, plan, a, endpoint, tls); err != nil {
		return err
	}
	progress(StepDeploying, "Connecting "+a.Name+" to the workspace service "+endpoint.Name)
	if err := r.storeExistingAccess(ctx, d, a, dsn); err != nil {
		return err
	}
	out := &Outcome{ExternalID: endpoint.ID, Revision: "endpoint:" + strconv.Itoa(pin.EndpointRevision)}
	if _, err := r.bind(ctx, d, a, current, out, "", nil); err != nil {
		return err
	}
	step.Revision = out.Revision
	return nil
}
