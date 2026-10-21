package planning

import (
	"context"
	"net/http"
	"time"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/extensions/identity"
	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/security"
)

// CloudList wraps the project's named Clouds.
type CloudList struct {
	Clouds []*cloudprofiles.Profile `json:"clouds"`
}

func (CloudList) ForgeEnvelope() forge.EnvelopeDef { return forge.EnvelopeDef{ItemsField: "clouds"} }

// CloudTargetList wraps the targets of one Cloud.
type CloudTargetList struct {
	Targets []*cloudprofiles.Target `json:"targets"`
}

func (CloudTargetList) ForgeEnvelope() forge.EnvelopeDef {
	return forge.EnvelopeDef{ItemsField: "targets"}
}

// CloudSyncList is a page of persisted operations, newest first.
type CloudSyncList struct {
	Syncs []*cloudprofiles.Sync `json:"syncs"`
	Next  string                `json:"next,omitempty"`
}

func (CloudSyncList) ForgeEnvelope() forge.EnvelopeDef { return forge.EnvelopeDef{ItemsField: "syncs"} }

// EnvironmentCloud distinguishes an unassigned environment from missing scope.
type EnvironmentCloud struct {
	Assignment *cloudprofiles.Assignment `json:"assignment" nullable:"true" required:"true"`
	Revision   int64                     `json:"revision"`
}

// EnvironmentCloudInput reviews a Cloud assignment at the current generation.
type EnvironmentCloudInput struct {
	CloudID  string `json:"cloud_id"`
	Revision int64  `json:"revision"`
}

// ApplyCloudInput accepts exactly the immutable preview you reviewed.
type ApplyCloudInput struct {
	PreviewID      string `json:"preview_id"`
	Revision       string `json:"revision"`
	IdempotencyKey string `json:"idempotency_key"`
}

// CompleteCloudRoutingInput records external routing evidence for one environment.
type CompleteCloudRoutingInput struct {
	EnvironmentID string `json:"environment_id"`
	Evidence      string `json:"evidence"`
}

type cloudRoute struct {
	method, path, name string
	handler            forge.Handler
	request, response  any
	status             int
}

func (e *Extension) cloudRoutes(r forge.Router, guard identity.Guard) error {
	const base = "/projects/:id"
	const clouds = base + "/clouds"
	const cloud = clouds + "/:cloudId"
	const targets = cloud + "/targets"
	const target = targets + "/:targetId"
	const syncs = base + "/cloud-syncs"
	const sync = syncs + "/:syncId"
	const assignment = base + "/environments/:environmentId/cloud"
	routes := []cloudRoute{
		{http.MethodGet, "/clouds", "organizationClouds.list", e.listClouds, nil, CloudList{}, http.StatusOK},
		{http.MethodPost, "/clouds", "organizationClouds.create", e.createCloud, cloudprofiles.CreateInput{}, cloudprofiles.Profile{}, http.StatusCreated},
		{http.MethodGet, "/clouds/:cloudId", "organizationClouds.get", e.getCloud, nil, cloudprofiles.Profile{}, http.StatusOK},
		{http.MethodPut, "/clouds/:cloudId", "organizationClouds.update", e.updateCloud, cloudprofiles.UpdateInput{}, cloudprofiles.Profile{}, http.StatusOK},
		{http.MethodDelete, "/clouds/:cloudId", "organizationClouds.delete", e.deleteCloud, nil, nil, http.StatusNoContent},
		{http.MethodGet, "/clouds/:cloudId/targets", "organizationCloudTargets.list", e.listCloudTargets, nil, CloudTargetList{}, http.StatusOK},
		{http.MethodGet, "/clouds/:cloudId/targets/:targetId", "organizationCloudTargets.get", e.getCloudTarget, nil, cloudprofiles.Target{}, http.StatusOK},
		{http.MethodPost, "/clouds/:cloudId/targets", "organizationCloudTargets.create", e.createCloudTarget, cloudprofiles.CreateTargetInput{}, cloudprofiles.Target{}, http.StatusCreated},
		{http.MethodPut, "/clouds/:cloudId/targets/:targetId", "organizationCloudTargets.update", e.updateCloudTarget, cloudprofiles.UpdateTargetInput{}, cloudprofiles.Target{}, http.StatusOK},
		{http.MethodDelete, "/clouds/:cloudId/targets/:targetId", "organizationCloudTargets.delete", e.deleteCloudTarget, nil, nil, http.StatusNoContent},
		{http.MethodGet, "/clouds/:cloudId/projects", "organizationClouds.listGrants", e.listCloudProjectGrants, nil, CloudProjectGrantList{}, http.StatusOK},
		{http.MethodPut, "/clouds/:cloudId/projects/:projectId", "organizationClouds.grantProject", e.grantCloudProject, CloudProjectGrantInput{}, cloudprofiles.ProjectGrant{}, http.StatusOK},
		{http.MethodDelete, "/clouds/:cloudId/projects/:projectId", "organizationClouds.revokeProject", e.revokeCloudProject, nil, cloudprofiles.ProjectGrant{}, http.StatusOK},
		{http.MethodPost, "/clouds/changes/preview", "organizationCloudChanges.preview", e.previewCloudChange, cloudprofiles.ChangeInput{}, cloudprofiles.Preview{}, http.StatusOK},
		{http.MethodPost, "/clouds/changes/apply", "organizationCloudChanges.apply", e.applyCloudChange, ApplyCloudInput{}, cloudprofiles.Sync{}, http.StatusAccepted},
		{http.MethodGet, "/clouds/changes/:previewId", "organizationCloudChanges.getPreview", e.getCloudPreview, nil, cloudprofiles.Preview{}, http.StatusOK},
		{http.MethodGet, "/clouds/syncs", "organizationCloudSyncs.list", e.listCloudSyncs, nil, CloudSyncList{}, http.StatusOK},
		{http.MethodGet, "/clouds/syncs/:syncId", "organizationCloudSyncs.get", e.getCloudSync, nil, cloudprofiles.Sync{}, http.StatusOK},
		{http.MethodPost, "/clouds/syncs/:syncId/retry", "organizationCloudSyncs.retry", e.retryCloudSync, nil, cloudprofiles.Sync{}, http.StatusAccepted},
		{http.MethodPost, "/clouds/syncs/:syncId/revert/preview", "organizationCloudSyncs.previewRevert", e.previewCloudRevert, nil, cloudprofiles.Preview{}, http.StatusOK},
		{http.MethodPost, "/clouds/syncs/revert/apply", "organizationCloudSyncs.applyRevert", e.applyCloudRevert, ApplyCloudInput{}, cloudprofiles.Sync{}, http.StatusAccepted},
		{http.MethodPost, "/clouds/syncs/:syncId/routing", "organizationCloudSyncs.completeRouting", e.completeCloudRouting, CompleteCloudRoutingInput{}, cloudprofiles.Sync{}, http.StatusAccepted},

		{http.MethodGet, base + "/cloud-changes/:previewId", "cloudChanges.getPreview", e.getCloudPreview, nil, cloudprofiles.Preview{}, http.StatusOK},
		{http.MethodGet, clouds, "clouds.list", e.listClouds, nil, CloudList{}, http.StatusOK},
		{http.MethodPost, clouds, "clouds.create", e.createCloud, cloudprofiles.CreateInput{}, cloudprofiles.Profile{}, http.StatusCreated},
		{http.MethodGet, cloud, "clouds.get", e.getCloud, nil, cloudprofiles.Profile{}, http.StatusOK},
		{http.MethodPut, cloud, "clouds.update", e.updateCloud, cloudprofiles.UpdateInput{}, cloudprofiles.Profile{}, http.StatusOK},
		{http.MethodDelete, cloud, "clouds.delete", e.deleteCloud, nil, nil, http.StatusNoContent},
		{http.MethodGet, targets, "cloudTargets.list", e.listCloudTargets, nil, CloudTargetList{}, http.StatusOK},
		{http.MethodGet, target, "cloudTargets.get", e.getCloudTarget, nil, cloudprofiles.Target{}, http.StatusOK},
		{http.MethodPost, targets, "cloudTargets.create", e.createCloudTarget, cloudprofiles.CreateTargetInput{}, cloudprofiles.Target{}, http.StatusCreated},
		{http.MethodPut, target, "cloudTargets.update", e.updateCloudTarget, cloudprofiles.UpdateTargetInput{}, cloudprofiles.Target{}, http.StatusOK},
		{http.MethodDelete, target, "cloudTargets.delete", e.deleteCloudTarget, nil, nil, http.StatusNoContent},
		{http.MethodPost, base + "/cloud-changes/preview", "cloudChanges.preview", e.previewCloudChange, cloudprofiles.ChangeInput{}, cloudprofiles.Preview{}, http.StatusOK},
		{http.MethodPost, base + "/cloud-changes/apply", "cloudChanges.apply", e.applyCloudChange, ApplyCloudInput{}, cloudprofiles.Sync{}, http.StatusAccepted},
		{http.MethodGet, assignment, "environmentClouds.get", e.getEnvironmentCloud, nil, EnvironmentCloud{}, http.StatusOK},
		{http.MethodPost, assignment + "/preview", "environmentClouds.preview", e.previewEnvironmentCloud, EnvironmentCloudInput{}, cloudprofiles.Preview{}, http.StatusOK},
		{http.MethodGet, syncs, "cloudSyncs.list", e.listCloudSyncs, nil, CloudSyncList{}, http.StatusOK},
		{http.MethodGet, sync, "cloudSyncs.get", e.getCloudSync, nil, cloudprofiles.Sync{}, http.StatusOK},
		{http.MethodPost, sync + "/retry", "cloudSyncs.retry", e.retryCloudSync, nil, cloudprofiles.Sync{}, http.StatusAccepted},
		{http.MethodPost, sync + "/revert/preview", "cloudSyncs.previewRevert", e.previewCloudRevert, nil, cloudprofiles.Preview{}, http.StatusOK},
		{http.MethodPost, syncs + "/revert/apply", "cloudSyncs.applyRevert", e.applyCloudRevert, ApplyCloudInput{}, cloudprofiles.Sync{}, http.StatusAccepted},
		{http.MethodPost, sync + "/routing", "cloudSyncs.completeRouting", e.completeCloudRouting, CompleteCloudRoutingInput{}, cloudprofiles.Sync{}, http.StatusAccepted},
	}
	g := r.Group("/api/v1", forge.WithGroupMiddleware(guard...), forge.WithGroupTags("Clouds"))
	for _, rt := range routes {
		opts := []forge.RouteOption{forge.WithName(rt.name), forge.WithSummary(rt.name)}
		if rt.request != nil {
			opts = append(opts, forge.WithRequestSchema(rt.request))
		}
		if rt.response != nil {
			opts = append(opts, forge.WithResponseSchema(rt.status, "Result", rt.response))
		}
		if rt.name == "cloudSyncs.list" || rt.name == "organizationCloudSyncs.list" {
			opts = append(opts, forge.WithQuerySchema(PageQuery{}))
		}
		var err error
		switch rt.method {
		case http.MethodGet:
			err = g.GET(rt.path, rt.handler, opts...)
		case http.MethodPost:
			err = g.POST(rt.path, rt.handler, opts...)
		case http.MethodPut:
			err = g.PUT(rt.path, rt.handler, opts...)
		case http.MethodDelete:
			err = g.DELETE(rt.path, rt.handler, opts...)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func cloudBody[T any](c forge.Context) (T, error) {
	var in T
	if err := c.BindJSON(&in); err != nil {
		return in, forge.NewHTTPError(http.StatusBadRequest, "the body must be valid JSON")
	}
	return in, nil
}
func cloudResult(c forge.Context, status int, value any, err error) error {
	if err != nil {
		return fail(err)
	}
	return c.JSON(status, value)
}
func cloudCall(c forge.Context, status int, call func(context.Context, security.Principal) (any, error)) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	v, err := call(c.Request().Context(), p)
	return cloudResult(c, status, v, err)
}

func (e *Extension) listClouds(c forge.Context) error {
	return cloudCall(c, http.StatusOK, func(ctx context.Context, p security.Principal) (any, error) {
		v, err := e.cloudProfiles.List(ctx, p, c.Param("id"))
		return CloudList{Clouds: v}, err
	})
}
func (e *Extension) createCloud(c forge.Context) error {
	in, err := cloudBody[cloudprofiles.CreateInput](c)
	if err != nil {
		return err
	}
	return cloudCall(c, http.StatusCreated, func(ctx context.Context, p security.Principal) (any, error) {
		return e.cloudProfiles.Create(ctx, p, c.Param("id"), in)
	})
}
func (e *Extension) getCloud(c forge.Context) error {
	return cloudCall(c, http.StatusOK, func(ctx context.Context, p security.Principal) (any, error) {
		return e.cloudProfiles.Get(ctx, p, c.Param("id"), c.Param("cloudId"))
	})
}
func (e *Extension) updateCloud(c forge.Context) error {
	in, err := cloudBody[cloudprofiles.UpdateInput](c)
	if err != nil {
		return err
	}
	return cloudCall(c, http.StatusOK, func(ctx context.Context, p security.Principal) (any, error) {
		return e.cloudProfiles.Update(ctx, p, c.Param("id"), c.Param("cloudId"), in)
	})
}
func (e *Extension) deleteCloud(c forge.Context) error {
	return cloudCall(c, http.StatusNoContent, func(ctx context.Context, p security.Principal) (any, error) {
		return nil, e.cloudProfiles.Delete(ctx, p, c.Param("id"), c.Param("cloudId"))
	})
}
func (e *Extension) listCloudTargets(c forge.Context) error {
	return cloudCall(c, http.StatusOK, func(ctx context.Context, p security.Principal) (any, error) {
		v, err := e.cloudProfiles.ListTargets(ctx, p, c.Param("id"), c.Param("cloudId"))
		return CloudTargetList{Targets: v}, err
	})
}
func (e *Extension) getCloudTarget(c forge.Context) error {
	return cloudCall(c, http.StatusOK, func(ctx context.Context, p security.Principal) (any, error) {
		return e.cloudProfiles.GetTarget(ctx, p, c.Param("id"), c.Param("cloudId"), c.Param("targetId"))
	})
}
func (e *Extension) createCloudTarget(c forge.Context) error {
	in, err := cloudBody[cloudprofiles.CreateTargetInput](c)
	if err != nil {
		return err
	}
	return cloudCall(c, http.StatusCreated, func(ctx context.Context, p security.Principal) (any, error) {
		return e.cloudProfiles.CreateTarget(ctx, p, c.Param("id"), c.Param("cloudId"), in)
	})
}
func (e *Extension) updateCloudTarget(c forge.Context) error {
	in, err := cloudBody[cloudprofiles.UpdateTargetInput](c)
	if err != nil {
		return err
	}
	return cloudCall(c, http.StatusOK, func(ctx context.Context, p security.Principal) (any, error) {
		return e.cloudProfiles.UpdateTarget(ctx, p, c.Param("id"), c.Param("cloudId"), c.Param("targetId"), in)
	})
}
func (e *Extension) deleteCloudTarget(c forge.Context) error {
	return cloudCall(c, http.StatusNoContent, func(ctx context.Context, p security.Principal) (any, error) {
		return nil, e.cloudProfiles.DeleteTarget(ctx, p, c.Param("id"), c.Param("cloudId"), c.Param("targetId"))
	})
}
func (e *Extension) getEnvironmentCloud(c forge.Context) error {
	return cloudCall(c, http.StatusOK, func(ctx context.Context, p security.Principal) (any, error) {
		a, revision, err := e.cloudProfiles.EnvironmentCloud(ctx, p, c.Param("id"), c.Param("environmentId"))
		return EnvironmentCloud{Assignment: a, Revision: revision}, err
	})
}
func (e *Extension) previewEnvironmentCloud(c forge.Context) error {
	in, err := cloudBody[EnvironmentCloudInput](c)
	if err != nil {
		return err
	}
	return cloudCall(c, http.StatusOK, func(ctx context.Context, p security.Principal) (any, error) {
		return e.plans.PreviewCloudChange(ctx, p, c.Param("id"), cloudprofiles.ChangeInput{EnvironmentID: c.Param("environmentId"), CloudID: in.CloudID, Revision: in.Revision})
	})
}
func (e *Extension) previewCloudChange(c forge.Context) error {
	in, err := cloudBody[cloudprofiles.ChangeInput](c)
	if err != nil {
		return err
	}
	return cloudCall(c, http.StatusOK, func(ctx context.Context, p security.Principal) (any, error) {
		return e.plans.PreviewCloudChange(ctx, p, c.Param("id"), in)
	})
}
func (e *Extension) applyCloudChange(c forge.Context) error {
	in, err := cloudBody[ApplyCloudInput](c)
	if err != nil {
		return err
	}
	return cloudCall(c, http.StatusAccepted, func(ctx context.Context, p security.Principal) (any, error) {
		return e.plans.ApplyCloudChange(ctx, p, c.Param("id"), in.PreviewID, in.Revision, in.IdempotencyKey)
	})
}
func (e *Extension) listCloudSyncs(c forge.Context) error {
	before, limit, err := pageOf(c)
	if err != nil {
		return err
	}
	return cloudCall(c, http.StatusOK, func(ctx context.Context, p security.Principal) (any, error) {
		v, next, err := e.plans.CloudSyncPage(ctx, p, c.Param("id"), before, limit)
		return CloudSyncList{Syncs: v, Next: next.String()}, err
	})
}
func (e *Extension) getCloudSync(c forge.Context) error {
	return cloudCall(c, http.StatusOK, func(ctx context.Context, p security.Principal) (any, error) {
		return e.plans.CloudSync(ctx, p, c.Param("id"), c.Param("syncId"))
	})
}
func (e *Extension) retryCloudSync(c forge.Context) error {
	return cloudCall(c, http.StatusAccepted, func(ctx context.Context, p security.Principal) (any, error) {
		return e.plans.RetryCloudSync(ctx, p, c.Param("id"), c.Param("syncId"))
	})
}
func (e *Extension) previewCloudRevert(c forge.Context) error {
	return cloudCall(c, http.StatusOK, func(ctx context.Context, p security.Principal) (any, error) {
		return e.plans.PreviewCloudRevert(ctx, p, c.Param("id"), c.Param("syncId"))
	})
}
func (e *Extension) applyCloudRevert(c forge.Context) error {
	in, err := cloudBody[ApplyCloudInput](c)
	if err != nil {
		return err
	}
	return cloudCall(c, http.StatusAccepted, func(ctx context.Context, p security.Principal) (any, error) {
		return e.plans.ApplyCloudRevert(ctx, p, c.Param("id"), in.PreviewID, in.Revision, in.IdempotencyKey)
	})
}
func (e *Extension) completeCloudRouting(c forge.Context) error {
	in, err := cloudBody[CompleteCloudRoutingInput](c)
	if err != nil {
		return err
	}
	return cloudCall(c, http.StatusAccepted, func(ctx context.Context, p security.Principal) (any, error) {
		return e.plans.CompleteCloudRouting(ctx, p, c.Param("id"), c.Param("syncId"), in.EnvironmentID, in.Evidence)
	})
}

func (e *Extension) startCloudRecovery(ctx context.Context, every time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	recoveryErr := e.deployments.RecoverCloudSyncs(ctx)
	if err := ctx.Err(); err != nil {
		return err
	}
	if recoveryErr != nil {
		e.Logger().Warn("Cloud recovery will retry", forge.F("error", recoveryErr.Error()))
	}
	work, cancel := context.WithCancel(context.WithoutCancel(ctx))
	e.cloudRecoveryCancel = cancel
	e.cloudRecoveryDone = make(chan struct{})
	go func() {
		defer close(e.cloudRecoveryDone)
		ticker := time.NewTicker(every)
		defer ticker.Stop()
		for {
			select {
			case <-work.Done():
				return
			case <-ticker.C:
				if work.Err() != nil {
					return
				}
				if err := e.deployments.RecoverCloudSyncs(work); err != nil && work.Err() == nil {
					e.Logger().Warn("Cloud recovery will retry", forge.F("error", err.Error()))
				}
			}
		}
	}()
	return nil
}

func (e *Extension) getCloudPreview(c forge.Context) error {
	return cloudCall(c, http.StatusOK, func(ctx context.Context, p security.Principal) (any, error) {
		return e.plans.CloudPreview(ctx, p, c.Param("id"), c.Param("previewId"))
	})
}

// CloudProjectGrantInput selects the target set. Empty means every Cloud target.
type CloudProjectGrantInput struct {
	TargetIDs []string `json:"target_ids"`
}

// CloudProjectGrantList includes revoked rows so grant history stays visible.
type CloudProjectGrantList struct {
	Grants []*cloudprofiles.ProjectGrant `json:"grants"`
}

func (CloudProjectGrantList) ForgeEnvelope() forge.EnvelopeDef {
	return forge.EnvelopeDef{ItemsField: "grants"}
}
func (e *Extension) listCloudProjectGrants(c forge.Context) error {
	return cloudCall(c, http.StatusOK, func(ctx context.Context, p security.Principal) (any, error) {
		v, err := e.cloudProfiles.ListProjectGrants(ctx, p, c.Param("cloudId"))
		return CloudProjectGrantList{Grants: v}, err
	})
}
func (e *Extension) grantCloudProject(c forge.Context) error {
	in, err := cloudBody[CloudProjectGrantInput](c)
	if err != nil {
		return err
	}
	return cloudCall(c, http.StatusOK, func(ctx context.Context, p security.Principal) (any, error) {
		return e.cloudProfiles.GrantProject(ctx, p, c.Param("cloudId"), c.Param("projectId"), in.TargetIDs)
	})
}
func (e *Extension) revokeCloudProject(c forge.Context) error {
	return cloudCall(c, http.StatusOK, func(ctx context.Context, p security.Principal) (any, error) {
		return e.cloudProfiles.RevokeProject(ctx, p, c.Param("cloudId"), c.Param("projectId"))
	})
}
