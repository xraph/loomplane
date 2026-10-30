package cloudinfra

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/extensions/identity"
	infra "github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/client"
	"github.com/xraph/loomplane/pkg/provider"
)

type ClusterQuery struct {
	Cursor string `query:"cursor" json:"cursor,omitempty" optional:"true"`
	Limit  int    `query:"limit" json:"limit,omitempty" optional:"true"`
	Region string `query:"region" json:"region,omitempty" optional:"true"`
}

// publicClusterOperation is an allowlist. Durable enrollment receipts stay private.
func publicClusterOperation(o *infra.ClusterOperation) *client.CloudClusterOperation {
	if o == nil {
		return nil
	}
	in := client.ClusterInput{PolicyRevision: o.Input.PolicyRevision, ManagementReachable: o.Input.ManagementReachable, AcknowledgeUnknownCosts: o.Input.AcknowledgeUnknownCosts, AcknowledgeNodeCosts: o.Input.AcknowledgeNodeCosts, WorkerImage: o.Input.WorkerImage, Kind: o.Input.Kind, Provider: o.Input.Provider, ConnectionID: o.Input.ConnectionID, AccountID: o.Input.AccountID, ResourceID: o.Input.ResourceID, ResourceRevision: o.Input.ResourceRevision, ConnectionGeneration: o.Input.ConnectionGeneration, CloudRevision: o.Input.CloudRevision, OptionsDigest: o.Input.OptionsDigest, OptionsCheckedAt: o.Input.OptionsCheckedAt, Spec: o.Input.Spec, Ref: o.Input.Ref}
	if a := o.Input.Attachment; a != nil {
		in.Attachment = &client.AttachmentInput{TargetSlug: a.TargetSlug, CloudRevision: a.CloudRevision, ResourceRevision: a.ResourceRevision, GrantRevision: a.GrantRevision, TrustFingerprint: a.TrustFingerprint, EndpointFingerprint: a.EndpointFingerprint, Prerequisites: client.AttachmentPrerequisites(a.Prerequisites), ProjectID: a.ProjectID, Namespace: a.Namespace, NamespaceUID: a.NamespaceUID, ServiceAccountName: a.ServiceAccountName, AdoptNamespace: a.AdoptNamespace}
	}
	out := &client.CloudClusterOperation{ID: o.ID, CloudID: o.CloudID, ResourceID: o.ResourceID, Kind: o.Kind, Input: in, Digest: o.Digest, Revision: o.Revision, Status: o.Status, Error: publicClusterError(o), Attempt: o.Attempt, CancelRequested: o.CancelRequested, EstimatedNodeHourlyMicroUSD: o.EstimatedNodeHourlyMicroUSD, UnknownCosts: o.UnknownCosts, Steps: []client.ClusterStep{}, CreatedAt: o.CreatedAt, UpdatedAt: o.UpdatedAt, ExpiresAt: o.ExpiresAt}
	for _, step := range o.Steps {
		out.Steps = append(out.Steps, client.ClusterStep(step))
	}
	if a := o.Enrollment; a != nil {
		out.Enrollment = &client.ClusterEnrollmentSummary{AttachmentID: a.AttachmentID, RuntimeConnectionID: a.RuntimeConnectionID, TargetID: a.TargetID, CredentialExpiry: a.CredentialExpiry, RuntimeVerified: o.Status == "succeeded" && a.RuntimeConnectionID != ""}
	}
	return out
}
func publicClusterError(o *infra.ClusterOperation) string {
	if o.Error == "" {
		return ""
	}
	return infra.ClusterPublicReason(o)
}

func publicCluster(r *infra.ManagedResource) *client.CloudCluster {
	if r == nil {
		return nil
	}
	return &client.CloudCluster{ID: r.ID, CloudID: r.CloudID, Provider: r.Provider, AccountID: r.AccountID, ConnectionID: r.ConnectionID, ExternalID: r.ExternalID, Ownership: r.Ownership, LifecycleAuthority: append([]string{}, r.LifecycleAuthority...), Revision: r.Revision, Status: r.Status, Observation: r.Observation, UpdatedAt: r.UpdatedAt}
}
func clusterResult(c forge.Context, status int, value any, err error) error {
	if err == nil {
		return c.JSON(status, value)
	}
	var admission *infra.CapacityAdmissionError
	if errors.As(err, &admission) {
		return forge.NewHTTPError(http.StatusUnprocessableEntity, "Cluster capacity requires a fresh policy, inventory and available quota")
	}
	code := security.StatusFor(err)
	message := "Cluster operation failed"
	switch {
	case errors.Is(err, infra.ErrConflict), errors.Is(err, infra.ErrBusy):
		code = http.StatusConflict
		message = "The reviewed cluster state changed. Refresh options and review again."
	case errors.Is(err, infra.ErrNotFound), errors.Is(err, provider.ErrInfrastructureNotFound):
		code = http.StatusNotFound
		message = "Cluster or operation not found"
	case errors.Is(err, infra.ErrInvalid), errors.Is(err, provider.ErrInfrastructureInvalid), errors.Is(err, provider.ErrInfrastructureUnsupported), errors.Is(err, provider.ErrInfrastructureIdentity):
		code = http.StatusUnprocessableEntity
		message = "Cluster input or prerequisites are unsupported. Check the selected scope and runtime requirements."
	case errors.Is(err, provider.ErrInfrastructureScope):
		code = http.StatusForbidden
	}
	if code == http.StatusForbidden {
		message = "You do not have access to manage clusters in this scope"
	}
	if code == http.StatusUnauthorized {
		message = "Sign in to continue"
	}
	return forge.NewHTTPError(code, message)
}
func (e *Extension) clusterRoutes(r forge.Router, guard identity.Guard) error {
	discovery := r.Group("/api/v1/connections/:connectionId", forge.WithGroupMiddleware(guard...), forge.WithGroupTags("Cloud clusters"))
	if err := discovery.GET("/clusters", e.discoverClusters, forge.WithName("connectionClusters.list"), forge.WithQuerySchema(ClusterQuery{}), forge.WithResponseSchema(http.StatusOK, "Clusters", provider.ClusterPage{})); err != nil {
		return err
	}
	g := r.Group("/api/v1/clouds/:cloudId", forge.WithGroupMiddleware(guard...), forge.WithGroupTags("Cloud clusters"))
	routes := []route{
		{http.MethodGet, "/clusters", "cloudClusters.list", e.clusterList, nil, client.CloudClusterPage{}, http.StatusOK},
		{http.MethodGet, "/clusters/:clusterId", "cloudClusters.get", e.clusterGet, nil, client.CloudCluster{}, http.StatusOK},
		{http.MethodGet, "/cluster-options/:connectionId", "cloudClusters.options", e.clusterOptions, nil, provider.ClusterOptions{}, http.StatusOK},
		{http.MethodPost, "/clusters/preview", "cloudClusters.preview", e.clusterPreview, client.ClusterInput{}, client.CloudClusterOperation{}, http.StatusOK},
		{http.MethodGet, "/cluster-operations", "clusterOperations.list", e.clusterOperations, nil, client.CloudClusterOperationPage{}, http.StatusOK},
		{http.MethodGet, "/cluster-operations/:operationId", "clusterOperations.get", e.clusterOperation, nil, client.CloudClusterOperation{}, http.StatusOK},
		{http.MethodPost, "/cluster-operations/:operationId/apply", "clusterOperations.apply", e.clusterApply, infra.ApplyInput{}, client.CloudClusterOperation{}, http.StatusAccepted},
		{http.MethodPost, "/cluster-operations/:operationId/retry", "clusterOperations.retry", e.clusterRetry, nil, client.CloudClusterOperation{}, http.StatusAccepted},
		{http.MethodPost, "/cluster-operations/:operationId/cancel", "clusterOperations.cancel", e.clusterCancel, nil, client.CloudClusterOperation{}, http.StatusOK},
	}
	for _, rt := range routes {
		opts := []forge.RouteOption{forge.WithName(rt.name), forge.WithSummary(rt.name), forge.WithResponseSchema(rt.status, "Result", rt.response)}
		if rt.method == http.MethodGet {
			opts = append(opts, forge.WithQuerySchema(ClusterQuery{}))
		}
		if rt.request != nil {
			opts = append(opts, forge.WithRequestSchema(rt.request))
		}
		if rt.method == http.MethodPost {
			opts = append(opts, forge.WithInvalidates("CloudCluster", "CloudClusterOperation", "CloudClusterPage", "CloudClusterOperationPage"))
		}
		var err error
		if rt.method == http.MethodGet {
			err = g.GET(rt.path, rt.handler, opts...)
		} else {
			err = g.POST(rt.path, rt.handler, opts...)
		}
		if err != nil {
			return err
		}
	}
	return nil
}
func (e *Extension) discoverClusters(c forge.Context) error {
	p, err := caller(c)
	if err != nil {
		return clusterResult(c, 0, nil, err)
	}
	v, err := e.svc.DiscoverClusters(c.Request().Context(), p, c.Param("connectionId"), c.Request().URL.Query().Get("cursor"))
	return clusterResult(c, http.StatusOK, v, err)
}
func (e *Extension) clusterOptions(c forge.Context) error {
	p, err := caller(c)
	if err != nil {
		return clusterResult(c, 0, nil, err)
	}
	v, err := e.svc.ClusterOptions(c.Request().Context(), p, c.Param("cloudId"), c.Param("connectionId"), c.Request().URL.Query().Get("region"))
	return clusterResult(c, http.StatusOK, v, err)
}
func (e *Extension) clusterList(c forge.Context) error {
	p, err := caller(c)
	if err != nil {
		return clusterResult(c, 0, nil, err)
	}
	limit := 0
	if value := c.Request().URL.Query().Get("limit"); value != "" {
		parsed, parseErr := strconv.Atoi(value)
		if parseErr != nil || parsed < 1 || parsed > 100 {
			return clusterResult(c, 0, nil, infra.ErrInvalid)
		}
		limit = parsed
	}
	page, err := e.svc.ListClusters(c.Request().Context(), p, c.Param("cloudId"), c.Request().URL.Query().Get("cursor"), limit)
	out := client.CloudClusterPage{Items: []client.CloudCluster{}, NextCursor: page.NextCursor}
	for _, r := range page.Items {
		out.Items = append(out.Items, *publicCluster(r))
	}
	return clusterResult(c, http.StatusOK, out, err)
}
func (e *Extension) clusterGet(c forge.Context) error {
	p, err := caller(c)
	if err != nil {
		return clusterResult(c, 0, nil, err)
	}
	v, err := e.svc.GetCluster(c.Request().Context(), p, c.Param("cloudId"), c.Param("clusterId"))
	return clusterResult(c, http.StatusOK, publicCluster(v), err)
}
func (e *Extension) clusterOperations(c forge.Context) error {
	p, err := caller(c)
	if err != nil {
		return clusterResult(c, 0, nil, err)
	}
	limit := 0
	if value := c.Request().URL.Query().Get("limit"); value != "" {
		parsed, parseErr := strconv.Atoi(value)
		if parseErr != nil || parsed < 1 || parsed > 100 {
			return clusterResult(c, 0, nil, infra.ErrInvalid)
		}
		limit = parsed
	}
	page, err := e.svc.ListClusterOperations(c.Request().Context(), p, c.Param("cloudId"), c.Request().URL.Query().Get("cursor"), limit)
	out := client.CloudClusterOperationPage{Items: []client.CloudClusterOperation{}, NextCursor: page.NextCursor}
	for _, o := range page.Items {
		out.Items = append(out.Items, *publicClusterOperation(o))
	}
	return clusterResult(c, http.StatusOK, out, err)
}
func (e *Extension) clusterOperation(c forge.Context) error {
	p, err := caller(c)
	if err != nil {
		return clusterResult(c, 0, nil, err)
	}
	v, err := e.svc.GetClusterOperation(c.Request().Context(), p, c.Param("cloudId"), c.Param("operationId"))
	return clusterResult(c, http.StatusOK, publicClusterOperation(v), err)
}
func (e *Extension) clusterPreview(c forge.Context) error {
	p, err := caller(c)
	if err != nil {
		return clusterResult(c, 0, nil, err)
	}
	// Reject unknown fields, including internal qualification receipts.
	var input client.ClusterInput
	dec := json.NewDecoder(http.MaxBytesReader(c.Response(), c.Request().Body, 64<<10))
	dec.DisallowUnknownFields()
	if decodeErr := dec.Decode(&input); decodeErr != nil {
		return clusterResult(c, 0, nil, infra.ErrInvalid)
	}
	if decodeErr := dec.Decode(new(any)); !errors.Is(decodeErr, io.EOF) {
		return clusterResult(c, 0, nil, infra.ErrInvalid)
	}
	in := infra.ClusterInput{PolicyRevision: input.PolicyRevision, ManagementReachable: input.ManagementReachable, AcknowledgeUnknownCosts: input.AcknowledgeUnknownCosts, AcknowledgeNodeCosts: input.AcknowledgeNodeCosts, WorkerImage: input.WorkerImage, Kind: input.Kind, Provider: input.Provider, ConnectionID: input.ConnectionID, AccountID: input.AccountID, ResourceID: input.ResourceID, ResourceRevision: input.ResourceRevision, ConnectionGeneration: input.ConnectionGeneration, CloudRevision: input.CloudRevision, OptionsDigest: input.OptionsDigest, OptionsCheckedAt: input.OptionsCheckedAt, Spec: input.Spec, Ref: input.Ref}
	if a := input.Attachment; a != nil {
		in.Attachment = &infra.AttachmentInput{TargetSlug: a.TargetSlug, CloudRevision: a.CloudRevision, ResourceRevision: a.ResourceRevision, GrantRevision: a.GrantRevision, TrustFingerprint: a.TrustFingerprint, EndpointFingerprint: a.EndpointFingerprint, Prerequisites: infra.AttachmentPrerequisites(a.Prerequisites), ProjectID: a.ProjectID, Namespace: a.Namespace, NamespaceUID: a.NamespaceUID, ServiceAccountName: a.ServiceAccountName, AdoptNamespace: a.AdoptNamespace}
	}
	if in.Kind != "create" && len(in.Spec.NodePools) == 0 {
		in.Spec.NodePools = nil
	}
	v, err := e.svc.PreviewCluster(c.Request().Context(), p, c.Param("cloudId"), in)
	return clusterResult(c, http.StatusOK, publicClusterOperation(v), err)
}
func (e *Extension) clusterApply(c forge.Context) error {
	p, err := caller(c)
	if err != nil {
		return clusterResult(c, 0, nil, err)
	}
	var in infra.ApplyInput
	if bindErr := c.BindJSON(&in); bindErr != nil {
		return clusterResult(c, 0, nil, infra.ErrInvalid)
	}
	v, err := e.svc.ApplyCluster(c.Request().Context(), p, c.Param("cloudId"), c.Param("operationId"), in)
	return clusterResult(c, http.StatusAccepted, publicClusterOperation(v), err)
}
func (e *Extension) clusterRetry(c forge.Context) error {
	p, err := caller(c)
	if err != nil {
		return clusterResult(c, 0, nil, err)
	}
	v, err := e.svc.RetryCluster(c.Request().Context(), p, c.Param("cloudId"), c.Param("operationId"))
	return clusterResult(c, http.StatusAccepted, publicClusterOperation(v), err)
}
func (e *Extension) clusterCancel(c forge.Context) error {
	p, err := caller(c)
	if err != nil {
		return clusterResult(c, 0, nil, err)
	}
	v, err := e.svc.CancelCluster(c.Request().Context(), p, c.Param("cloudId"), c.Param("operationId"))
	return clusterResult(c, http.StatusOK, publicClusterOperation(v), err)
}
