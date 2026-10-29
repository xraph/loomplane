package realtime

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/forge"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/discovery"
	"github.com/xraph/loomplane/internal/manifests"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/projects"
	store "github.com/xraph/loomplane/internal/realtime"
	"github.com/xraph/loomplane/internal/security"
)

type ScopeQuery struct {
	OrgID         string `query:"org_id" json:"org_id" optional:"true"`
	ProjectIDs    string `query:"project_ids" json:"project_ids,omitempty" optional:"true"`
	DeploymentIDs string `query:"deployment_ids" json:"deployment_ids,omitempty" optional:"true"`
	Cursor        string `query:"cursor" json:"cursor,omitempty" optional:"true"`
}

type LogQuery struct {
	Cursor string `query:"cursor" json:"cursor,omitempty" optional:"true"`
	Limit  int    `query:"limit" json:"limit,omitempty" optional:"true"`
}

func streamBindings() forge.RouteOption {
	return forge.WithStreamBinding(
		forge.Emits[projects.Project]("project.updated").As(forge.StreamUpsert), forge.Emits[projects.Project]("project.deleted"),
		forge.Emits[projects.Environment]("environment.updated").As(forge.StreamUpsert), forge.Emits[projects.Environment]("environment.deleted"),
		forge.Emits[discovery.Source]("source.updated").As(forge.StreamUpsert), forge.Emits[discovery.Source]("source.deleted"),
		forge.Emits[discovery.Analysis]("analysis.updated").As(forge.StreamUpsert), forge.Emits[discovery.Analysis]("analysis.deleted"),
		forge.Emits[manifests.Revision]("manifest.updated").As(forge.StreamUpsert), forge.Emits[manifests.Revision]("manifest.deleted"),
		forge.Emits[planning.Plan]("plan.updated").As(forge.StreamUpsert), forge.Emits[planning.Plan]("plan.deleted"),
		forge.Emits[planning.Binding]("binding.updated").As(forge.StreamUpsert).Invalidates("Binding[]", "Instance[]"), forge.Emits[planning.Binding]("binding.deleted"),
		forge.Emits[connections.Connection]("connection.updated").As(forge.StreamUpsert), forge.Emits[connections.Connection]("connection.deleted"),
		forge.Emits[deployments.Deployment]("deployment.updated").As(forge.StreamUpsert).Invalidates("Deployment[]", "Instance[]"), forge.Emits[deployments.Deployment]("deployment.deleted"),
		forge.Emits[deployments.CanaryVerdict]("canary_verdict.updated").As(forge.StreamUpsert).Invalidates("CanaryReport"), forge.Emits[deployments.CanaryVerdict]("canary_verdict.deleted").Invalidates("CanaryReport"),
		forge.Emits[cloudprofiles.Profile]("cloud.updated").As(forge.StreamUpsert), forge.Emits[cloudprofiles.Profile]("cloud.deleted"),
		forge.Emits[cloudprofiles.Target]("cloud_target.updated").As(forge.StreamUpsert), forge.Emits[cloudprofiles.Target]("cloud_target.deleted"),
		forge.Emits[cloudprofiles.EnvironmentCloudEvent]("environment_cloud.updated").Invalidates("EnvironmentCloud"),
		forge.Emits[cloudprofiles.CloudSyncEvent]("cloud_sync.updated").Invalidates("Sync", "Sync[]", "EnvironmentCloud"),
		forge.Emits[cloudinfra.ClusterChange]("cloud_cluster.updated").Invalidates("CloudCluster", "CloudClusterPage", "CloudClusterOperation", "CloudClusterOperationPage", "Target", "Target[]", "Connection", "Connection[]", "Profile", "Profile[]"),
		forge.Emits[cloudinfra.CapacityChange]("cloud_capacity.updated").Invalidates("CapacityPolicyView:{cloud_id}", "CapacityUsage:{cloud_id}", "Expansion[]"),
		forge.Emits[store.Log]("build.log").As(forge.StreamUpsert), forge.Emits[store.Collection]("build.collection").As(forge.StreamUpsert),
	)
}

func (e *Extension) routes(router forge.Router) error {
	guard := append([]forge.Middleware{}, e.guard...)
	guard = append(guard, e.scopeGuard())
	g := router.Group("/api/v1", forge.WithGroupMiddleware(guard...), forge.WithGroupTags("Realtime"))
	if err := g.GET("/realtime/snapshot", e.snapshot, forge.WithName("realtime.snapshot"), forge.WithQuerySchema(ScopeQuery{}), forge.WithResponseSchema(http.StatusOK, "Current permitted state and replay cursor", Snapshot{}), forge.WithoutEntity()); err != nil {
		return err
	}
	if err := g.GET("/deployments/:id/logs", e.logs, forge.WithName("deployments.logs"), forge.WithQuerySchema(LogQuery{}), forge.WithResponseSchema(http.StatusOK, "Retained build output", store.LogPage{})); err != nil {
		return err
	}
	if err := g.WebSocket("/realtime/ws", e.websocket, forge.WithName("realtime.ws"), forge.WithQuerySchema(ScopeQuery{}), forge.WithWebSocketMessages(Subscription{}, Envelope{}), streamBindings()); err != nil {
		return err
	}
	return g.EventStream("/realtime/events", e.events, forge.WithName("realtime.events"), forge.WithQuerySchema(ScopeQuery{}), forge.WithSSEMessages(map[string]any{
		"project.updated": projects.Project{}, "project.deleted": projects.Project{}, "environment.updated": projects.Environment{}, "environment.deleted": projects.Environment{},
		"source.updated": discovery.Source{}, "source.deleted": discovery.Source{}, "analysis.updated": discovery.Analysis{}, "analysis.deleted": discovery.Analysis{},
		"manifest.updated": manifests.Revision{}, "manifest.deleted": manifests.Revision{}, "plan.updated": planning.Plan{}, "plan.deleted": planning.Plan{},
		"binding.updated": planning.Binding{}, "binding.deleted": planning.Binding{}, "connection.updated": connections.Connection{}, "connection.deleted": connections.Connection{},
		"deployment.updated": deployments.Deployment{}, "deployment.deleted": deployments.Deployment{}, "canary_verdict.updated": deployments.CanaryVerdict{}, "canary_verdict.deleted": deployments.CanaryVerdict{}, "build.log": store.Log{}, "build.collection": store.Collection{},
		"cloud.updated": cloudprofiles.Profile{}, "cloud.deleted": cloudprofiles.Profile{}, "cloud_target.updated": cloudprofiles.Target{}, "cloud_target.deleted": cloudprofiles.Target{},
		"environment_cloud.updated": cloudprofiles.EnvironmentCloudEvent{}, "cloud_sync.updated": cloudprofiles.CloudSyncEvent{},
		"cloud_cluster.updated": cloudinfra.ClusterChange{}, "cloud_capacity.updated": cloudinfra.CapacityChange{}, "realtime.snapshot": Snapshot{}, forge.EventResumed: forge.ResumedPayload{}, forge.EventGap: forge.GapPayload{}, "realtime.status": Status{},
	}), streamBindings())
}

func (e *Extension) scopeGuard() forge.Middleware {
	return func(next forge.Handler) forge.Handler {
		return func(c forge.Context) error {
			p, ok := security.PrincipalFrom(c.Request().Context())
			if !ok {
				return fail(security.ErrUnauthenticated)
			}
			if want := c.Query("org_id"); want != "" && want != p.OrgID {
				return fail(security.ErrOrgMismatch)
			}
			if _, err := e.verify(c.Request().Context(), c.Request()); err != nil {
				return fail(err)
			}
			if strings.HasPrefix(c.Request().URL.Path, "/api/v1/realtime/") {
				_, sub, err := requestScope(c)
				if err != nil {
					return fail(err)
				}
				if id := c.Request().Header.Get("Last-Event-ID"); id != "" {
					sub.Cursor = id
					raw, marshalErr := json.Marshal(sub)
					if marshalErr != nil {
						return marshalErr
					}
					if _, err = parseSubscription(p, raw); err != nil {
						return fail(err)
					}
				}
				if err = e.checkScope(c.Request().Context(), p, sub); err != nil {
					return fail(err)
				}
			}
			return next(c)
		}
	}
}

func requestScope(c forge.Context) (security.Principal, Subscription, error) {
	p, ok := security.PrincipalFrom(c.Request().Context())
	if !ok {
		return p, Subscription{}, security.ErrUnauthenticated
	}
	query := ScopeQuery{OrgID: c.Query("org_id"), ProjectIDs: c.Query("project_ids"), DeploymentIDs: c.Query("deployment_ids"), Cursor: c.Query("cursor")}
	if query.OrgID == "" {
		query.OrgID = p.OrgID
	}
	sub := Subscription{Op: "subscribe", OrgID: query.OrgID, Cursor: query.Cursor}
	if query.ProjectIDs != "" {
		sub.ProjectIDs = strings.Split(query.ProjectIDs, ",")
	}
	if query.DeploymentIDs != "" {
		sub.DeploymentIDs = strings.Split(query.DeploymentIDs, ",")
	}
	raw, err := json.Marshal(sub)
	if err != nil {
		return p, sub, err
	}
	sub, err = parseSubscription(p, raw)
	return p, sub, err
}

func logQuery(c forge.Context) (LogQuery, error) {
	q := LogQuery{Cursor: c.Query("cursor"), Limit: 200}
	if raw := c.Query("limit"); raw != "" {
		var err error
		q.Limit, err = strconv.Atoi(raw)
		if err != nil || q.Limit < 1 || q.Limit > 1000 {
			return q, ErrInvalid
		}
	}
	return q, nil
}

func fail(err error) error {
	if status := security.StatusFor(err); status != http.StatusInternalServerError {
		return forge.NewHTTPError(status, err.Error())
	}
	if errors.Is(err, ErrInvalid) || errors.Is(err, store.ErrInvalid) {
		return forge.NewHTTPError(http.StatusUnprocessableEntity, err.Error())
	}
	if errors.Is(err, pgx.ErrNoRows) || errors.Is(err, store.ErrNotFound) {
		return forge.NewHTTPError(http.StatusNotFound, "realtime resource not found")
	}
	return err
}
