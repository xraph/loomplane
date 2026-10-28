package models

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/xraph/loomplane/internal/manifests"
	"github.com/xraph/loomplane/internal/models/engines"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/recipes"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/manifest"
)

// PinStatus says whether a model resource still names the weights the
// registry pins for its entry and quantization. After a registry re-pin
// it doesn't, and the model has an update: MoveToPin rewrites its
// settings to the new pin and plans it. That's an update to offer, not an
// error.
type PinStatus struct {
	UpdateAvailable bool `json:"update_available"`
	// Message says what changed and what moving does, when there's an
	// update.
	Message string `json:"message,omitempty"`
	// Model, Revision and File are the pin now, when there's an update.
	Model    string `json:"model,omitempty"`
	Revision string `json:"revision,omitempty"`
	File     string `json:"file,omitempty"`
}

// pinStatusOf turns the outcome of checking a resource's pin into its
// status: a RepinnedError is an update available, and any other error
// stays an error.
func pinStatusOf(err error) (*PinStatus, error) {
	var repinned *RepinnedError
	switch {
	case err == nil:
		return &PinStatus{}, nil
	case errors.As(err, &repinned):
		return &PinStatus{
			UpdateAvailable: true, Model: repinned.Model, Revision: repinned.Revision, File: repinned.File,
			Message: fmt.Sprintf("The model registry now pins %s %s to %s. %s runs %s. Move it to the new pin to plan the update.",
				repinned.Entry, repinned.Quantization, weights(repinned.Model, repinned.Revision, repinned.File), repinned.Resource, repinned.Asked),
		}, nil
	}
	return nil, err
}

// checkPin's outcome for a manifest resource: nil for one that isn't a
// registry model, or names the weights its pin does.
func (s *Service) pinOf(resource string, r manifest.Resource) error {
	if !recipes.IsModelKind(r.Kind) {
		return nil
	}
	e, q, ok := registryQuantization(s.d.Registry, r.Settings)
	if !ok {
		return nil
	}
	return checkPin(resource, e, q, r.Settings)
}

// CheckPin says whether a model resource in the environment has an update
// from a registry re-pin. ai:read.
func (s *Service) CheckPin(ctx context.Context, p security.Principal, projectID, environmentID, resource string) (*PinStatus, error) {
	if err := s.d.Authz.Require(ctx, p, security.AIRead, ""); err != nil {
		return nil, err
	}
	env, err := s.environment(ctx, p.OrgID, projectID, environmentID)
	if err != nil {
		return nil, err
	}
	r, err := s.modelResource(ctx, p, projectID, env, resource)
	if err != nil {
		return nil, err
	}
	return pinStatusOf(s.pinOf(resource, r))
}

// modelResource reads a model resource that runs in env from the
// project's current manifest.
func (s *Service) modelResource(ctx context.Context, p security.Principal, projectID string, env *projects.Environment, resource string) (manifest.Resource, error) {
	if s.d.Manifests == nil {
		return manifest.Resource{}, errors.New("models: manifests aren't configured")
	}
	rev, err := s.d.Manifests.Current(ctx, p, projectID)
	if err != nil {
		return manifest.Resource{}, err
	}
	r, ok := rev.Manifest.Resources[resource]
	if !ok || !recipes.IsModelKind(r.Kind) {
		return manifest.Resource{}, fmt.Errorf("%w: the manifest has no model %s", ErrNotFound, resource)
	}
	if !enabledIn(r, env.Name) {
		return manifest.Resource{}, fmt.Errorf("%w: %s doesn't run in %s", ErrNotFound, resource, env.Name)
	}
	return r, nil
}

// MoveToPin is the one-click fix for a model deployed before a registry
// re-pin: it sets the resource's model, revision and file settings to the
// weights the registry pins now, grows its weight cache when the new
// weights need more, and plans the environment. The plan shows the change
// for review like any other. It needs WriteNeed of the environment, and
// refuses a resource that's already on its pin.
func (s *Service) MoveToPin(ctx context.Context, p security.Principal, projectID, environmentID, resource string) (*DeployResult, error) {
	env, err := s.environment(ctx, p.OrgID, projectID, environmentID)
	if err != nil {
		return nil, err
	}
	if aerr := s.d.Authz.Require(ctx, p, writeNeed(env), ""); aerr != nil {
		return nil, aerr
	}
	if s.d.Plans == nil {
		return nil, errors.New("models: planning isn't configured")
	}
	r, err := s.modelResource(ctx, p, projectID, env, resource)
	if err != nil {
		return nil, err
	}
	e, q, ok := registryQuantization(s.d.Registry, r.Settings)
	if !ok {
		return nil, fmt.Errorf("%w: %s doesn't run a registry quantization, so there's no pin to move it to", ErrInvalid, resource)
	}
	var repinned *RepinnedError
	if err = checkPin(resource, e, q, r.Settings); !errors.As(err, &repinned) {
		return nil, fmt.Errorf("%w: %s already runs the weights the registry pins", ErrInvalid, resource)
	}
	// A setting set to "" is deleted, which is what a file the new pin
	// doesn't have needs.
	settings := map[string]string{"model": repinned.Model, "revision": repinned.Revision, "file": repinned.File}
	need := engines.CacheMB(q.WeightBytes)
	if have, perr := strconv.ParseInt(r.Settings["cache_mb"], 10, 64); perr != nil || have < need {
		settings["cache_mb"] = strconv.FormatInt(need, 10)
	}
	rev, _, err := s.d.Manifests.Edit(ctx, p, projectID, manifests.EditInput{
		UpdateResource: &manifests.ResourceUpdateInput{Resource: resource, Settings: settings},
	})
	if err != nil {
		return nil, err
	}
	s.audit(ctx, p, projectID, "ai.model.repin", map[string]any{
		"resource": resource, "environment": env.Name, "entry": e.ID, "quantization": q.ID,
		"from": repinned.Asked, "to": weights(repinned.Model, repinned.Revision, repinned.File),
	})
	plan, err := s.d.Plans.Create(ctx, p, projectID, planning.CreatePlanInput{Environment: env.Name, Catalog: rev.Manifest.CatalogOnly()})
	if err != nil {
		return nil, &PlanAfterEditError{Resource: resource, Environment: env.Name, Repin: true, err: err}
	}
	out := &DeployResult{PlanID: plan.ID}
	if s.d.Store != nil {
		if d, gerr := s.d.Store.Get(ctx, p.OrgID, projectID, env.ID, resource); gerr == nil {
			out.Deployment = d
		}
	}
	return out, nil
}
