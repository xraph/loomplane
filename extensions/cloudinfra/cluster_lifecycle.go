package cloudinfra

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/xraph/forge"

	infra "github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/pkg/client"
	"github.com/xraph/loomplane/pkg/provider"
)

func publicMoney(v *infra.MoneyEstimate) *client.MoneyEstimate {
	if v == nil {
		return nil
	}
	out := &client.MoneyEstimate{HourlyMicroUSD: strconv.FormatInt(v.HourlyMicroUSD, 10), HorizonMicroUSD: strconv.FormatInt(v.HorizonMicroUSD, 10), HorizonSeconds: v.HorizonSeconds, Known: v.Known, MissingComponents: v.MissingComponents, PriceDigest: v.PriceDigest, CheckedAt: v.CheckedAt, Components: []client.MoneyComponent{}}
	for _, c := range v.Components {
		p := client.MoneyComponent{Name: c.Name, Category: c.Category, Source: c.Source, Quantity: strconv.FormatInt(c.Quantity, 10), PriceCheckedAt: c.PriceCheckedAt}
		if c.HourlyMicroUSD != nil {
			s := strconv.FormatInt(*c.HourlyMicroUSD, 10)
			p.HourlyMicroUSD = &s
		}
		out.Components = append(out.Components, p)
	}
	return out
}
func publicBudget(v *infra.BudgetReview) *client.BudgetReview {
	if v == nil {
		return nil
	}
	return &client.BudgetReview{CloudID: v.CloudID, PolicyRevision: v.PolicyRevision, Estimate: *publicMoney(&v.Estimate), Digest: v.Digest}
}
func publicScalingInput(v *infra.ClusterScalingPolicy) *client.ClusterScalingInput {
	if v == nil {
		return nil
	}
	return &client.ClusterScalingInput{ExpectedRevision: v.ExpectedRevision, PoolID: v.PoolID, Mode: v.Mode, MinNodes: v.MinNodes, MaxNodes: v.MaxNodes, CooldownSeconds: v.CooldownSeconds, ContinuityPoolID: v.ContinuityPoolID, ContinuityFloor: v.ContinuityFloor}
}
func publicScalingPolicies(in map[string]infra.ClusterScalingPolicy) map[string]client.ClusterScalingPolicy {
	if in == nil {
		return nil
	}
	out := make(map[string]client.ClusterScalingPolicy, len(in))
	for k, v := range in {
		out[k] = client.ClusterScalingPolicy{ClusterScalingInput: *publicScalingInput(&v), Revision: v.Revision, PauseReason: v.PauseReason, Revoked: v.Revoked, UpdatedAt: v.UpdatedAt}
	}
	return out
}
func publicRetained(in []provider.AssociatedResource) []client.RetainedResource {
	if in == nil {
		return nil
	}
	out := make([]client.RetainedResource, 0, len(in))
	for _, v := range in {
		p := client.RetainedResource{ID: v.ID, Kind: v.Kind, Ownership: v.Ownership, SurvivesClusterDeletion: v.SurvivesClusterDeletion}
		if v.EstimatedHourlyMicroUSD != nil {
			s := strconv.FormatInt(*v.EstimatedHourlyMicroUSD, 10)
			p.EstimatedHourlyMicroUSD = &s
		}
		out = append(out, p)
	}
	return out
}
func publicMaintenance(v *infra.ClusterMaintenanceInput) *client.ClusterMaintenanceInput {
	if v == nil {
		return nil
	}
	out := &client.ClusterMaintenanceInput{ObservationDigest: v.ObservationDigest, PoolID: v.PoolID, ObservedRevision: v.ObservedRevision, Pool: publicPool(v.Pool), Update: v.Update, Version: v.Version, DrainDigest: v.DrainDigest}
	if v.Window != nil {
		w := client.ClusterMaintenanceWindow(*v.Window)
		out.Window = &w
	}
	return out
}
func privateMaintenance(v *client.ClusterMaintenanceInput) *infra.ClusterMaintenanceInput {
	if v == nil {
		return nil
	}
	out := &infra.ClusterMaintenanceInput{ObservationDigest: v.ObservationDigest, PoolID: v.PoolID, ObservedRevision: v.ObservedRevision, Pool: privatePool(v.Pool), Update: v.Update, Version: v.Version, DrainDigest: v.DrainDigest}
	if v.Window != nil {
		w := infra.ClusterMaintenanceWindow(*v.Window)
		out.Window = &w
	}
	return out
}
func publicRetirement(v *infra.ClusterRetirementInput) *client.ClusterRetirementInput {
	if v == nil {
		return nil
	}
	out := &client.ClusterRetirementInput{ReviewDigest: v.ReviewDigest}
	if v.Attachment != nil {
		out.AttachmentID = v.Attachment.ID
		out.ExpectedAttachmentRevision = v.Attachment.Revision
	}
	return out
}
func publicEnvelope(v *infra.ClusterEnvelopeReview) *client.ClusterEnvelopeReview {
	if v == nil {
		return nil
	}
	out := &client.ClusterEnvelopeReview{Automatic: v.Automatic, Pools: []client.ClusterPoolEnvelopeReview{}}
	for _, p := range v.Pools {
		out.Pools = append(out.Pools, client.ClusterPoolEnvelopeReview{PoolID: p.PoolID, Maximum: client.ClusterEnvelopeResources(p.Maximum), Initial: client.ClusterEnvelopeResources(p.Initial), Baseline: client.ClusterEnvelopeResources(p.Baseline), Money: *publicMoney(&p.Money)})
	}
	return out
}
func (e *Extension) clusterRetirementPreview(c forge.Context) error {
	p, err := caller(c)
	if err != nil {
		return clusterResult(c, 0, nil, err)
	}
	var in client.ClusterRetirementPreviewInput
	dec := json.NewDecoder(http.MaxBytesReader(c.Response(), c.Request().Body, 4096))
	dec.DisallowUnknownFields()
	if parseErr := dec.Decode(&in); parseErr != nil && !errors.Is(parseErr, io.EOF) {
		return clusterResult(c, 0, nil, infra.ErrInvalid)
	}
	if parseErr := dec.Decode(new(any)); !errors.Is(parseErr, io.EOF) {
		return clusterResult(c, 0, nil, infra.ErrInvalid)
	}
	var v *infra.ClusterRetirementReview
	if in.AttachmentID != "" {
		v, err = e.svc.PreviewClusterDetach(c.Request().Context(), p, c.Param("cloudId"), c.Param("clusterId"), in.AttachmentID, in.ExpectedAttachmentRevision)
	} else {
		if in.ExpectedAttachmentRevision != 0 {
			return clusterResult(c, 0, nil, infra.ErrInvalid)
		}
		v, err = e.svc.PreviewClusterRetirement(c.Request().Context(), p, c.Param("cloudId"), c.Param("clusterId"))
	}
	if err != nil {
		return clusterResult(c, 0, nil, err)
	}
	return clusterResult(c, http.StatusOK, &client.ClusterRetirementReview{ResourceID: v.ResourceID, Blockers: v.Blockers, DeletionCandidates: publicRetained(v.DeletionCandidates), RetainedResources: publicRetained(v.RetainedResources), EstimatedRemainingHourlyMicroUSD: strconv.FormatInt(v.EstimatedRemainingHourlyMicroUSD, 10), UnknownCosts: v.UnknownCosts, CheckedAt: v.Inventory.ObservedAt, Digest: v.Digest}, nil)
}

func publicRuntimeAccess(v *provider.RuntimeAccessInput) *client.RuntimeAccessReview {
	if v == nil {
		return nil
	}
	return &client.RuntimeAccessReview{AttachmentID: v.AttachmentID, AttachmentRevision: v.AttachmentRevision, CapabilityRevision: v.CapabilityRevision, ProjectID: v.ProjectID, Namespace: v.Namespace, Profile: v.Profile}
}
func privateRuntimeAccess(v *client.RuntimeAccessReview) *provider.RuntimeAccessInput {
	if v == nil {
		return nil
	}
	return &provider.RuntimeAccessInput{AttachmentID: v.AttachmentID, Profile: v.Profile}
}
