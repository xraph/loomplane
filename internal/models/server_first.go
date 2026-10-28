package models

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/xraph/loomplane/internal/apikeys"
	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/gpus"
	"github.com/xraph/loomplane/internal/manifests"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/provider"
)

// Servers previews, approves and reads GPU servers as a principal, which
// it authorizes. *cloudinfra.Service is the real one.
type Servers interface {
	ServerOptions(ctx context.Context, p security.Principal, cloud, connection string) (provider.ServerOptions, error)
	PreviewServer(ctx context.Context, p security.Principal, cloud string, in cloudinfra.ServerInput) (*cloudinfra.Operation, error)
	ApplyServerOperation(ctx context.Context, p security.Principal, cloud, id string, in cloudinfra.ApplyInput) (*cloudinfra.Operation, error)
	GetOperation(ctx context.Context, p security.Principal, cloud, id string) (*cloudinfra.Operation, error)
	ListServers(ctx context.Context, p security.Principal, cloud string) ([]*cloudinfra.Server, error)
}

// Clouds says which cloud an environment deploys to, with its targets.
// *cloudprofiles.Service is the real one.
type Clouds interface {
	EnvironmentCloud(ctx context.Context, p security.Principal, projectID, environmentID string) (*cloudprofiles.Assignment, int64, error)
}

var (
	_ Servers = (*cloudinfra.Service)(nil)
	_ Clouds  = (*cloudprofiles.Service)(nil)
)

// How long the reconcile cron waits before it gives up on a step.
const (
	// serverStartLimit is how long a deployment waits for its server to be
	// created and enrolled, from the deploy.
	serverStartLimit = 45 * time.Minute
	// gpuReadyLimit is how long an enrolled server has to report its GPUs
	// ready, from the operation's success.
	gpuReadyLimit = 20 * time.Minute
	// StrandedAfter is how old a planned row with no plan, no message and
	// no manifest resource is before the cron deletes it. A deploy writes
	// its row, edits the manifest and plans in one request, which takes
	// seconds; a row that's still like that ten minutes on is one whose
	// deploy stopped before the edit.
	StrandedAfter = 10 * time.Minute
)

// sizeTarget finds the GPU server size a server size target names, in the
// region it asks for.
func (s *Service) sizeTarget(ctx context.Context, p security.Principal, choice TargetChoice, problem func(code, format string, args ...any)) (deployTarget, bool) {
	if choice.GPU == nil {
		problem(ProblemTarget, "Pick the GPU the new server runs the model on.")
		return deployTarget{}, false
	}
	if s.d.GPUs == nil || s.d.Servers == nil || s.d.Clouds == nil {
		problem(ProblemTarget, "Loomplane can't create a GPU server here. Pick a server or cluster you have.")
		return deployTarget{}, false
	}
	list, err := s.d.GPUs.Targets(ctx, p, gpus.GPUTargetQuery{Count: 1})
	if err != nil {
		problem(ProblemTarget, "%s", FitsUnknown)
		return deployTarget{}, false
	}
	for i := range list.Targets {
		t := list.Targets[i]
		if t.Source != gpus.SourceServerSize || t.ConnectionID != choice.ConnectionID || t.SizeID != choice.SizeID || t.Accelerator.Kind != choice.GPU.Kind {
			continue
		}
		if t.Cloud != connections.DigitalOcean {
			break
		}
		if !slices.Contains(t.Regions, choice.Region) {
			problem(ProblemTarget, "%s isn't sold with a GPU-ready image in %q. Pick one of its regions.", t.SizeID, choice.Region)
			return deployTarget{}, false
		}
		// The model runs on the new server, so its kinds' target is a server.
		return deployTarget{name: t.Name, provider: connections.Server, gpu: &t, size: true}, true
	}
	problem(ProblemTarget, "This workspace has no DigitalOcean account that sells %s with a %s GPU.", choice.SizeID, gpuTitle(choice.GPU.Kind))
	return deployTarget{}, false
}

// serverCost is what a whole new server costs per hour: it's created for
// the model, so all of it is the model's. A size whose GPUs aren't known
// has an unknown cost, never a CPU's "0.00" (Ruling M25).
func serverCost(accel provider.Accelerator, hourlyUSD float64, known bool) Cost {
	if accel.Count <= 0 {
		return Cost{Source: CostSourceUnknown}
	}
	return HourlyCost(gpus.GPUTarget{
		Source: gpus.SourceConnection, Accelerator: accel, HourlyUSD: hourlyUSD, HourlyPriceKnown: known,
	}, accel.Count, "")
}

// operationCost is serverCost from a server review's price and GPUs.
func operationCost(op *cloudinfra.Operation) Cost {
	if op.Accelerator == nil {
		return Cost{Source: CostSourceUnknown}
	}
	return serverCost(*op.Accelerator, op.EstimatedHourlyUSD, op.HourlyPriceKnown)
}

// serverNameFor is the name of the GPU server a deploy creates for resource
// in the project: "<project slug>-<resource>-gpu", a DNS label of at most
// 63 characters.
func serverNameFor(projectSlug, resource string) string {
	const suffix = "-gpu"
	name := strings.Trim(nonLabel.ReplaceAllString(strings.ToLower(projectSlug+"-"+resource), "-"), "-")
	if len(name) > 63-len(suffix) {
		name = strings.TrimRight(name[:63-len(suffix)], "-")
	}
	return name + suffix
}

// reviewServer works out the GPU server a server size target creates
// first. Preview asks cloudinfra to review a new one; Deploy checks the
// review it's given is that server's, still open. Only an admin creates
// GPU servers, and only on a DigitalOcean account the environment's cloud
// has.
func (s *Service) reviewServer(ctx context.Context, p security.Principal, projectID string, env *projects.Environment, in DeployInput,
	deploying bool, pv *DeployPreview, problem func(code, format string, args ...any),
) {
	admin, err := s.d.Authz.Allowed(ctx, p, security.OrganizationCloudManage)
	if err != nil || !admin {
		problem(ProblemServerAdmin, "An admin creates GPU servers. Ask one to deploy this, or pick a server you have.")
		return
	}
	project, err := s.d.Projects.GetProject(ctx, p.OrgID, projectID)
	if err != nil {
		problem(ProblemTarget, "Loomplane couldn't read this project. Try again.")
		return
	}
	name := serverNameFor(project.Slug, pv.Resource)
	pv.TargetName = name

	assignment, _, err := s.d.Clouds.EnvironmentCloud(ctx, p, projectID, env.ID)
	switch {
	case errors.Is(err, cloudprofiles.ErrNotFound) || err == nil && assignment == nil:
		problem(ProblemNoCloud, "Add this DigitalOcean account to %s's cloud first.", env.Name)
		return
	case err != nil:
		problem(ProblemTarget, "Loomplane couldn't read %s's cloud. Try again.", env.Name)
		return
	}
	if !slices.ContainsFunc(assignment.Targets, func(t cloudprofiles.Target) bool {
		return t.Provider == connections.DigitalOcean && t.ConnectionID == in.Target.ConnectionID
	}) {
		problem(ProblemNoCloud, "Add this DigitalOcean account to %s's cloud first.", env.Name)
		return
	}

	if in.Server != nil {
		s.checkApproval(ctx, p, assignment.CloudID, name, in, pv, problem)
		return
	}
	if deploying {
		problem(ProblemServerReview, "Review the GPU server first: preview the deploy, then approve the server it shows.")
		return
	}
	options, err := s.d.Servers.ServerOptions(ctx, p, assignment.CloudID, in.Target.ConnectionID)
	if err != nil {
		problem(ProblemTarget, "Loomplane couldn't read this DigitalOcean account's sizes and images. Try again.")
		return
	}
	image, ok := gpuImage(options, in.Target.Region, in.Target.SizeID)
	if !ok {
		problem(ProblemTarget, "%s offers no GPU-ready image for %s.", in.Target.Region, in.Target.SizeID)
		return
	}
	op, err := s.d.Servers.PreviewServer(ctx, p, assignment.CloudID, cloudinfra.ServerInput{
		Kind: "create", ConnectionID: in.Target.ConnectionID, Name: name, Region: in.Target.Region, Size: in.Target.SizeID,
		Image: image, CloudRevision: assignment.CloudRevision,
	})
	if err != nil {
		problem(ProblemTarget, "%s", reviewFailure(err))
		return
	}
	pv.Server = op
	pv.Cost = operationCost(op)
}

// reviewFailure says why cloudinfra couldn't review a server, without
// passing on what a provider said.
func reviewFailure(err error) string {
	switch {
	case errors.Is(err, cloudinfra.ErrConflict):
		return "The cloud changed while Loomplane was reviewing the GPU server. Try again."
	case errors.Is(err, security.ErrForbidden):
		return "An admin creates GPU servers. Ask one to deploy this, or pick a server you have."
	case errors.Is(err, cloudinfra.ErrInvalid):
		// cloudinfra's ErrInvalid sentences are written for this page.
		if msg := err.Error(); msg != cloudinfra.ErrInvalid.Error() {
			return msg
		}
	}
	return "Loomplane couldn't review a GPU server for this. Try again."
}

// gpuImage is the first image in region that boots a server Loomplane can
// enroll and ships NVIDIA's driver, with room on size's disk.
func gpuImage(options provider.ServerOptions, region, size string) (string, bool) {
	disk := 0
	for _, v := range options.Sizes {
		if v.ID == size {
			disk = v.DiskGB
		}
	}
	for _, v := range options.Images {
		if v.Bootstrap && slices.Contains(v.Regions, region) && slices.Contains(v.Accelerators, provider.VendorNVIDIA) && v.MinDiskGB <= disk {
			return v.ID, true
		}
	}
	return "", false
}

// checkApproval checks the server review a deploy approves is the one it
// needs: a create of the target's size and region with that account and
// name, in the environment's cloud, still open.
func (s *Service) checkApproval(ctx context.Context, p security.Principal, cloudID, name string, in DeployInput, pv *DeployPreview,
	problem func(code, format string, args ...any),
) {
	a := in.Server
	if a.CloudID != cloudID {
		problem(ProblemServerReview, "The GPU server you approved isn't in this environment's cloud. Preview the deploy again.")
		return
	}
	op, err := s.d.Servers.GetOperation(ctx, p, a.CloudID, a.OperationID)
	if err != nil {
		problem(ProblemServerReview, "Loomplane can't find the GPU server you approved. Preview the deploy again.")
		return
	}
	t, kind := in.Target, ""
	if op.Accelerator != nil {
		kind = op.Accelerator.Kind
	}
	if op.Review != a.Review || op.Input.Kind != "create" || op.Input.ConnectionID != t.ConnectionID || op.Input.Size != t.SizeID ||
		op.Input.Region != t.Region || op.Input.Name != name || kind != t.GPU.Kind {
		problem(ProblemServerReview, "The GPU server you approved isn't the one this deploy needs. Preview the deploy again.")
		return
	}
	if op.Status != "preview" || !s.d.Now().Before(op.ExpiresAt) {
		problem(ProblemServerReview, "The GPU server's review has expired or was used. Preview the deploy again.")
		return
	}
	pv.Server = op
	pv.Cost = operationCost(op)
}

// deployServerFirst records the deployment as waiting for its server and
// approves the server. A capacity policy that holds the server back for
// now leaves the deployment waiting, with the reason; one that refuses it
// refuses the deploy.
func (s *Service) deployServerFirst(ctx context.Context, p security.Principal, projectID string, env *projects.Environment, in DeployInput, pv *DeployPreview) (*DeployResult, error) {
	op := pv.Server
	if s.d.Servers == nil {
		return nil, errors.New("models: creating GPU servers isn't configured")
	}
	req := in
	req.Resource, req.Quantization = pv.Resource, pv.Quantization
	req.Server = &ServerApproval{CloudID: op.CloudID, OperationID: op.ID, Review: op.Review}
	row := &Deployment{
		OrgID: p.OrgID, ProjectID: projectID, EnvironmentID: env.ID, Resource: pv.Resource, EntryID: pv.entry.ID,
		Quantization: pv.quant.ID, Engine: string(pv.quant.Engine), MonthlyBudgetUSD: pv.budgetUSD, Phase: PhaseWaitingForServer,
		ServerCloudID: op.CloudID, ServerOperationID: op.ID, ServerName: op.Input.Name, Request: &req, RequestedBy: p,
	}
	// The row comes first: a server that's paid for always has one.
	if err := s.createRow(ctx, p.OrgID, pv.entry, row); err != nil {
		return nil, err
	}
	_, err := s.d.Servers.ApplyServerOperation(ctx, p, op.CloudID, op.ID, cloudinfra.ApplyInput{Review: op.Review})
	var blocked *cloudinfra.CapacityAdmissionError
	switch {
	case errors.As(err, &blocked) && capacityPaused(blocked.Block):
		row.Message = capacitySentence(blocked.Block)
		if uerr := s.d.Store.Update(ctx, row); uerr != nil {
			return &DeployResult{Deployment: row}, uerr
		}
	case err != nil:
		if derr := s.d.Store.Delete(context.WithoutCancel(ctx), p.OrgID, projectID, env.ID, row.Resource); derr != nil {
			err = errors.Join(err, fmt.Errorf("and its deployment row stays: %w", derr))
		}
		if blocked != nil {
			return nil, &ProblemError{Problem: Problem{Code: ProblemCapacity, Message: capacitySentence(blocked.Block)}}
		}
		return nil, err
	}
	s.audit(ctx, p, projectID, "ai.model.server", map[string]any{
		"entry": pv.entry.ID, "resource": row.Resource, "environment": env.Name, "cloud_id": op.CloudID,
		"operation_id": op.ID, "server": op.Input.Name, "size": op.Input.Size, "region": op.Input.Region,
	})
	return &DeployResult{Deployment: row}, nil
}

// capacityPaused reports whether a capacity block holds the server back
// for now, so the deployment keeps waiting: a count that's out of date, or
// an approval or expansion still to come. Anything else, a ceiling or a
// provider quota, refuses it until someone changes the policy.
func capacityPaused(b cloudinfra.CapacityBlock) bool {
	switch b.Reason {
	case "expansion_approval", "review_required", "provisioning":
		return true
	}
	return b.Retryable
}

// capacitySentence says what a capacity block means for the server, for
// the page.
func capacitySentence(b cloudinfra.CapacityBlock) string {
	what := capacityResource(b.Resource)
	switch b.Reason {
	case "stale_inventory":
		return fmt.Sprintf("The cloud's capacity count of %s is out of date, so the GPU server waits until Loomplane has counted again.", what)
	case "expansion_approval", "review_required":
		return "The GPU server waits for an admin to approve it in the cloud's capacity policy."
	case "provisioning":
		return "The GPU server waits for the cloud's capacity to grow."
	case "hard_limit":
		if strings.HasPrefix(b.Resource, "gpus:") && b.Limit == 0 {
			return fmt.Sprintf("The cloud's capacity policy has no ceiling for %s, so it allows none. An admin can add one, then deploy the model again.", what)
		}
		return fmt.Sprintf("The GPU server would take the cloud past its capacity limit of %d %s: %d are counted and it needs %d more. An admin can raise the limit, then deploy the model again.",
			b.Limit, what, b.Counted, b.Requested)
	case "provider_quota":
		return fmt.Sprintf("The DigitalOcean account's quota has no room for the GPU server's %s: it allows %d, %d are counted and it needs %d more.",
			what, b.Limit, b.Counted, b.Requested)
	}
	return fmt.Sprintf("The cloud's capacity policy holds the GPU server back (%s, %s).", b.Reason, what)
}

// capacityResource names a capacity dimension for a sentence.
func capacityResource(resource string) string {
	if kind, ok := strings.CutPrefix(resource, "gpus:"); ok {
		return gpuTitle(kind) + " GPUs"
	}
	switch resource {
	case "servers":
		return "servers"
	case "cpu_millis":
		return "CPU millicores"
	case "memory_bytes":
		return "memory bytes"
	case "storage_bytes":
		return "storage bytes"
	case "", "request", "inventory", "account", "host", "server":
		return "servers and their resources"
	}
	return resource
}

// Retry puts a deployment that needs attention back to waiting for its
// server, carried on as the caller from now on. It needs WriteNeed of the
// environment. A server that failed is retried in Infrastructure first.
func (s *Service) Retry(ctx context.Context, p security.Principal, projectID, environmentID, resource string) (*Deployment, error) {
	d, err := s.waitingRow(ctx, p, projectID, environmentID, resource)
	if err != nil {
		return nil, err
	}
	if d.Phase != PhaseNeedsAttention || d.ServerOperationID == "" {
		return nil, fmt.Errorf("%w: only a model whose GPU server needs attention can be retried", ErrInvalid)
	}
	d.Phase, d.Message, d.RequestedBy = PhaseWaitingForServer, "", p
	if uerr := s.d.Store.Update(ctx, d); uerr != nil {
		return nil, uerr
	}
	s.audit(ctx, p, projectID, "ai.model.retry", map[string]any{"resource": resource, "operation_id": d.ServerOperationID})
	return d, nil
}

// CancelWaiting stops a deployment that's waiting for its GPU server, or
// needs attention there. The row goes; the server, if Loomplane created
// one, stays on the cloud's servers until someone deletes it from
// Infrastructure. It needs WriteNeed of the environment.
func (s *Service) CancelWaiting(ctx context.Context, p security.Principal, projectID, environmentID, resource string) error {
	d, err := s.waitingRow(ctx, p, projectID, environmentID, resource)
	if err != nil {
		return err
	}
	if d.Phase != PhaseWaitingForServer && d.Phase != PhaseNeedsAttention {
		return fmt.Errorf("%w: only a model waiting for its GPU server can be cancelled", ErrInvalid)
	}
	if derr := s.d.Store.Delete(ctx, d.OrgID, d.ProjectID, d.EnvironmentID, d.Resource); derr != nil {
		return derr
	}
	s.audit(ctx, p, projectID, "ai.model.cancel", map[string]any{
		"resource": resource, "server": d.ServerName, "cloud_id": d.ServerCloudID, "operation_id": d.ServerOperationID,
	})
	return nil
}

// waitingRow reads a deployment the caller may change.
func (s *Service) waitingRow(ctx context.Context, p security.Principal, projectID, environmentID, resource string) (*Deployment, error) {
	env, err := s.environment(ctx, p.OrgID, projectID, environmentID)
	if err != nil {
		return nil, err
	}
	if aerr := s.d.Authz.Require(ctx, p, writeNeed(env), ""); aerr != nil {
		return nil, aerr
	}
	if s.d.Store == nil {
		return nil, errors.New("models: deployments aren't configured")
	}
	return s.d.Store.Get(ctx, p.OrgID, projectID, env.ID, resource)
}

// Reconcile carries on every deployment waiting for its GPU server, in
// every organization, and deletes the planned rows a deploy stopped
// writing. The Dispatch cron "ai-models.reconcile" runs it every minute.
//
// It only continues what someone started. It never previews or creates a
// server: the one it approves again is the exact review the person who
// deployed approved, while the cloud's capacity policy held it back, and
// it acts as that person after checking they still may. A row that asked
// for no server needs attention.
func (s *Service) Reconcile(ctx context.Context) error {
	if s.d.Waiting == nil || s.d.Store == nil {
		return nil
	}
	var errs []error
	rows, err := s.d.Waiting.Waiting(ctx)
	if err != nil {
		errs = append(errs, err)
	}
	for _, d := range rows {
		if rerr := s.reconcileWaiting(ctx, d); rerr != nil && !errors.Is(rerr, ErrStale) && !errors.Is(rerr, ErrNotFound) {
			errs = append(errs, fmt.Errorf("model deployment %s: %w", d.ID, rerr))
		}
	}
	stranded, err := s.d.Waiting.Stranded(ctx, s.d.Now().Add(-StrandedAfter))
	if err != nil {
		errs = append(errs, err)
	}
	for _, d := range stranded {
		if rerr := s.reconcileStranded(ctx, d); rerr != nil && !errors.Is(rerr, ErrStale) && !errors.Is(rerr, ErrNotFound) {
			errs = append(errs, fmt.Errorf("model deployment %s: %w", d.ID, rerr))
		}
	}
	return errors.Join(errs...)
}

// attention puts d in needs_attention with msg.
func (s *Service) attention(ctx context.Context, d *Deployment, msg string) error {
	d.Phase, d.Message = PhaseNeedsAttention, msg
	return s.d.Store.Update(ctx, d)
}

// waitWith keeps d waiting, saying msg.
func (s *Service) waitWith(ctx context.Context, d *Deployment, msg string) error {
	if d.Message == msg {
		return nil
	}
	d.Message = msg
	return s.d.Store.Update(ctx, d)
}

// serverOf names d's server for a sentence.
func serverOf(d *Deployment) string {
	if d.ServerName != "" {
		return d.ServerName
	}
	return "The GPU server"
}

// reconcileWaiting carries one waiting deployment a step on.
func (s *Service) reconcileWaiting(ctx context.Context, d *Deployment) error {
	env, err := s.environment(ctx, d.OrgID, d.ProjectID, d.EnvironmentID)
	if errors.Is(err, projects.ErrEnvironmentNotFound) || errors.Is(err, projects.ErrNotFound) {
		return s.environmentDeleted(ctx, d)
	}
	if err != nil {
		return err
	}
	r := d.Request
	asked := r != nil && r.Target.Source == TargetServerSize && r.Target.GPU != nil && r.Server != nil &&
		r.Server.OperationID == d.ServerOperationID && r.Server.CloudID == d.ServerCloudID && d.ServerOperationID != "" &&
		d.RequestedBy.OrgID == d.OrgID && d.RequestedBy.UserID != ""
	if !asked {
		return s.attention(ctx, d, "Nobody asked Loomplane to create a GPU server for this, so it won't. Cancel it, or deploy the model again.")
	}
	p := d.RequestedBy
	allowed, err := s.stillAllowed(ctx, p, env)
	if err != nil {
		return err
	}
	if !allowed {
		return s.attention(ctx, d, "The person who asked for this can't deploy models any more.")
	}
	op, err := s.d.Servers.GetOperation(ctx, p, d.ServerCloudID, d.ServerOperationID)
	switch {
	case errors.Is(err, security.ErrForbidden):
		return s.attention(ctx, d, "The person who asked for this can't manage the cloud's servers any more.")
	case errors.Is(err, cloudinfra.ErrNotFound):
		return s.attention(ctx, d, "Loomplane can't find the GPU server's operation any more. Cancel this, or deploy the model again.")
	case err != nil:
		return err
	}
	now := s.d.Now()
	switch op.Status {
	case "preview":
		return s.applyAgain(ctx, d, p, op, now)
	case "failed":
		msg := strings.TrimSpace(op.Message)
		if msg == "" {
			msg = "The provider didn't say why."
		}
		return s.attention(ctx, d, fmt.Sprintf("Creating %s failed: %s Retry it from Infrastructure, then press Retry here.", serverOf(d), msg))
	case "cancelled":
		return s.attention(ctx, d, fmt.Sprintf("Creating %s was cancelled. Cancel this, or deploy the model again.", serverOf(d)))
	case "succeeded":
		return s.serverUp(ctx, d, p, env, op, now)
	}
	// Pending, running or waiting for the provider or enrollment.
	if now.Sub(d.CreatedAt) > serverStartLimit {
		return s.attention(ctx, d, "The server took too long to start.")
	}
	return s.waitWith(ctx, d, "")
}

// applyAgain approves the server once more, for a deployment whose server
// the capacity policy held back when it was approved. It's the review the
// person approved, so it's applied only while it's open.
func (s *Service) applyAgain(ctx context.Context, d *Deployment, p security.Principal, op *cloudinfra.Operation, now time.Time) error {
	if !now.Before(op.ExpiresAt) {
		return s.attention(ctx, d, "The GPU server's review expired before the cloud had room for it. Cancel this and deploy the model again to review a new server.")
	}
	t := d.Request.Target
	if op.Review != d.Request.Server.Review || op.Input.Kind != "create" || op.Input.ConnectionID != t.ConnectionID ||
		op.Input.Size != t.SizeID || op.Input.Region != t.Region {
		return s.attention(ctx, d, "The GPU server's review isn't the one this deploy approved. Cancel this, or deploy the model again.")
	}
	_, err := s.d.Servers.ApplyServerOperation(ctx, p, d.ServerCloudID, op.ID, cloudinfra.ApplyInput{Review: op.Review})
	var blocked *cloudinfra.CapacityAdmissionError
	switch {
	case errors.As(err, &blocked) && capacityPaused(blocked.Block):
		return s.waitWith(ctx, d, capacitySentence(blocked.Block))
	case blocked != nil:
		return s.attention(ctx, d, capacitySentence(blocked.Block))
	case errors.Is(err, security.ErrForbidden):
		return s.attention(ctx, d, "The person who asked for this can't manage the cloud's servers any more.")
	case errors.Is(err, cloudinfra.ErrConflict):
		return s.attention(ctx, d, "The cloud or the DigitalOcean account changed since the GPU server was reviewed. Cancel this and deploy the model again.")
	case err != nil:
		return err
	}
	return s.waitWith(ctx, d, "")
}

// serverUp continues a deployment whose server was created: once the
// server's GPUs are ready it plans the model onto it, as a deploy onto a
// server you have would.
func (s *Service) serverUp(ctx context.Context, d *Deployment, p security.Principal, env *projects.Environment, op *cloudinfra.Operation, now time.Time) error {
	servers, err := s.d.Servers.ListServers(ctx, p, d.ServerCloudID)
	if err != nil {
		return err
	}
	i := slices.IndexFunc(servers, func(v *cloudinfra.Server) bool { return v.ID == op.ServerID })
	if i < 0 || servers[i].RuntimeConnectionID == "" {
		if now.Sub(d.CreatedAt) > serverStartLimit {
			return s.attention(ctx, d, "The server took too long to start.")
		}
		return s.waitWith(ctx, d, "")
	}
	v := servers[i]
	d.ServerName = v.Name
	inv, reason := s.runtimeGPUs(ctx, p, v.RuntimeConnectionID)
	if inv != provider.GPUStatusReady {
		if now.Sub(op.UpdatedAt) > gpuReadyLimit {
			return s.attention(ctx, d, notReadySentence(v, reason))
		}
		return s.waitWith(ctx, d, fmt.Sprintf("%s is up. Waiting for its GPUs: %s", v.Name, reason))
	}

	in := *d.Request
	in.Resource, in.Server = d.Resource, nil
	in.Target = TargetChoice{Source: TargetConnection, ConnectionID: v.RuntimeConnectionID, Provider: connections.Server, GPU: d.Request.Target.GPU}
	pv, err := s.preview(ctx, p, d.ProjectID, env, in, true)
	if err != nil {
		return err
	}
	if len(pv.Problems) > 0 {
		return s.attention(ctx, d, fmt.Sprintf("%s is ready, but the model can't deploy onto it: %s", v.Name, pv.Problems[0].Message))
	}
	// The row says planned before the edit, so a stop between the two
	// leaves a stranded row the next run tidies.
	d.Phase, d.Message, d.Quantization, d.Engine = PhasePlanned, "", pv.quant.ID, string(pv.quant.Engine)
	if uerr := s.d.Store.Update(ctx, d); uerr != nil {
		return uerr
	}
	rev, err := s.addResource(ctx, p, d.ProjectID, env, pv)
	if err != nil {
		return s.attention(context.WithoutCancel(ctx), d, fmt.Sprintf("%s is ready, but Loomplane couldn't add %s to the manifest: %v", v.Name, d.Resource, err))
	}
	if _, err = s.planRow(ctx, p, d.ProjectID, env, rev, d); err != nil {
		var perr *PlanAfterEditError
		if errors.As(err, &perr) {
			// The row says how to plan it from the project.
			return nil
		}
		return err
	}
	return nil
}

// runtimeGPUs reads the GPU state of a server's runtime connection: its
// status and, when it isn't ready, why.
func (s *Service) runtimeGPUs(ctx context.Context, p security.Principal, connectionID string) (status, reason string) {
	unknown := provider.GPUStatusMessage(provider.GPUStatusUnknown)
	if s.d.Connections == nil {
		return provider.GPUStatusUnknown, unknown
	}
	conns, err := s.d.Connections.List(ctx, p)
	if err != nil {
		return provider.GPUStatusUnknown, unknown
	}
	for _, c := range conns {
		if c.ID != connectionID {
			continue
		}
		inv, ok := provider.ParseGPUInventory(c.Details)
		if !ok {
			return provider.GPUStatusUnknown, unknown
		}
		if inv.Status == provider.GPUStatusReady {
			return inv.Status, ""
		}
		if inv.Message != "" {
			return inv.Status, inv.Message
		}
		return inv.Status, provider.GPUStatusMessage(inv.Status)
	}
	return provider.GPUStatusUnknown, unknown
}

// notReadySentence says a server can't take GPU work and what it costs
// meanwhile: its price only when it's known, never "$0.00" for one that
// isn't.
func notReadySentence(v *cloudinfra.Server, reason string) string {
	price := "Its price is unknown."
	if v.HourlyPriceKnown && !math.IsNaN(v.HourlyUSD) && !math.IsInf(v.HourlyUSD, 0) && v.HourlyUSD >= 0 {
		price = fmt.Sprintf("It costs $%s/hr while it runs.", hourlyText(v.HourlyUSD))
	}
	return fmt.Sprintf("%s can't take GPU work: %s. %s Delete it from Infrastructure or fix it and press Retry.",
		v.Name, strings.TrimRight(reason, "."), price)
}

// hourlyText is an hourly price to the cent, or to the ten-thousandth when
// it has more.
func hourlyText(usd float64) string {
	units := int64(math.Round(usd * unitsPerUSD))
	if units%100 == 0 {
		return fmt.Sprintf("%d.%02d", units/unitsPerUSD, units%unitsPerUSD/100)
	}
	return formatUSD(units)
}

// stillAllowed rechecks the person a deployment acts as: their API key,
// when they used one, is still valid, and they may still change the
// environment's models.
func (s *Service) stillAllowed(ctx context.Context, p security.Principal, env *projects.Environment) (bool, error) {
	if p.KeyID != "" {
		if s.d.Principals == nil {
			return false, nil
		}
		err := s.d.Principals.ValidatePrincipal(ctx, p)
		if errors.Is(err, apikeys.ErrRejected) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
	}
	return s.d.Authz.Allowed(ctx, p, writeNeed(env))
}

// environmentDeleted handles a waiting deployment whose environment is
// gone. Without a server it goes. With one it needs attention, since the
// deletion doesn't stop the server: it stays on the cloud's servers, and
// costs money, until someone deletes it.
func (s *Service) environmentDeleted(ctx context.Context, d *Deployment) error {
	if d.ServerOperationID == "" {
		return s.d.Store.Delete(ctx, d.OrgID, d.ProjectID, d.EnvironmentID, d.Resource)
	}
	return s.attention(ctx, d, fmt.Sprintf("Its environment was deleted. %s stays on the cloud's servers, and costs money while it runs, until you delete it from Infrastructure.", serverOf(d)))
}

// reconcileStranded tidies a planned row a deploy stopped writing: gone
// when its environment is, or when the manifest never got its resource;
// otherwise it says how to plan it from the project.
func (s *Service) reconcileStranded(ctx context.Context, d *Deployment) error {
	gone, err := s.environmentGone(ctx, d)
	if err != nil {
		return err
	}
	if gone {
		return s.d.Store.Delete(ctx, d.OrgID, d.ProjectID, d.EnvironmentID, d.Resource)
	}
	if s.d.Manifests == nil {
		return nil
	}
	rev, err := s.d.Manifests.Current(ctx, d.RequestedBy, d.ProjectID)
	switch {
	case errors.Is(err, manifests.ErrNotFound) || errors.Is(err, projects.ErrNotFound):
		return s.d.Store.Delete(ctx, d.OrgID, d.ProjectID, d.EnvironmentID, d.Resource)
	case errors.Is(err, security.ErrForbidden):
		// Whoever asked can't read the project any more; leave it.
		return nil
	case err != nil:
		return err
	}
	if _, ok := rev.Manifest.Resources[d.Resource]; !ok {
		return s.d.Store.Delete(ctx, d.OrgID, d.ProjectID, d.EnvironmentID, d.Resource)
	}
	env, err := s.environment(ctx, d.OrgID, d.ProjectID, d.EnvironmentID)
	if err != nil {
		return err
	}
	return s.waitWith(ctx, d, fmt.Sprintf("The manifest has %s now. Plan %s from the project to deploy it.", d.Resource, env.Name))
}
