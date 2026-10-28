package models

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"

	"github.com/xraph/loomplane/internal/connections"
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
// from a registry re-pin, as that environment runs it. ai:read.
func (s *Service) CheckPin(ctx context.Context, p security.Principal, projectID, environmentID, resource string) (*PinStatus, error) {
	if err := s.d.Authz.Require(ctx, p, security.AIRead, ""); err != nil {
		return nil, err
	}
	env, err := s.environment(ctx, p.OrgID, projectID, environmentID)
	if err != nil {
		return nil, err
	}
	here, _, err := s.modelResource(ctx, p, projectID, env, resource)
	if err != nil {
		return nil, err
	}
	return pinStatusOf(s.pinOf(resource, here))
}

// modelResource reads a model resource that runs in env from the
// project's current manifest m, and returns it as env runs it: here is
// m.Resources[resource].In(env.Name, m.Defaults).
func (s *Service) modelResource(ctx context.Context, p security.Principal, projectID string, env *projects.Environment, resource string) (here manifest.Resource, m *manifest.Manifest, err error) {
	if s.d.Manifests == nil {
		return manifest.Resource{}, nil, errors.New("models: manifests aren't configured")
	}
	rev, err := s.d.Manifests.Current(ctx, p, projectID)
	if err != nil {
		return manifest.Resource{}, nil, err
	}
	r, ok := rev.Manifest.Resources[resource]
	if !ok || !recipes.IsModelKind(r.Kind) {
		return manifest.Resource{}, nil, fmt.Errorf("%w: the manifest has no model %s", ErrNotFound, resource)
	}
	if !enabledIn(r, env.Name) {
		return manifest.Resource{}, nil, fmt.Errorf("%w: %s doesn't run in %s", ErrNotFound, resource, env.Name)
	}
	return r.In(env.Name, rev.Manifest.Defaults), rev.Manifest, nil
}

// MoveToPin is the one-click fix for a model deployed before a registry
// re-pin: it sets the model, revision and file settings of the
// environment's copy to the weights the registry pins now, grows its
// weight cache when the new weights need more, and plans the environment.
// The plan shows the change for review like any other.
//
// It changes that environment's copy alone (Decision 5): the edit goes
// where only this environment reads the pin from (pinUpdate), so the other
// environments keep their weights, their rows and their open plans, and
// show the update on their own page until it's moved from there. It needs
// WriteNeed of the environment, and refuses a copy that's already on its
// pin.
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
	here, m, err := s.modelResource(ctx, p, projectID, env, resource)
	if err != nil {
		return nil, err
	}
	e, q, ok := registryQuantization(s.d.Registry, here.Settings)
	if !ok {
		return nil, fmt.Errorf("%w: %s doesn't run a registry quantization, so there's no pin to move it to", ErrInvalid, resource)
	}
	var repinned *RepinnedError
	if err = checkPin(resource, e, q, here.Settings); !errors.As(err, &repinned) {
		return nil, fmt.Errorf("%w: %s already runs the weights the registry pins in %s", ErrInvalid, resource, env.Name)
	}
	need := engines.CacheMB(q.WeightBytes)
	if cacheGrows(here.Settings, need) {
		fixed, ferr := s.fixedCache(ctx, p.OrgID, env.ID, resource, "")
		if ferr != nil {
			return nil, ferr
		}
		if fixed {
			return nil, fmt.Errorf("%w: %s keeps its weight cache on a Kubernetes volume, which can't grow in place. Remove %s from %s and deploy it again to move to the new pin",
				ErrInvalid, resource, resource, env.Name)
		}
	}
	update := pinUpdate(m.Resources[resource], env.Name, repinned, need)
	update.Resource = resource
	rev, _, err := s.d.Manifests.Edit(ctx, p, projectID, manifests.EditInput{UpdateResource: update})
	if err != nil {
		return nil, err
	}
	s.audit(ctx, p, projectID, "ai.model.repin", map[string]any{
		"resource": resource, "environment": env.Name, "entry": e.ID, "quantization": q.ID,
		"from": repinned.Asked, "to": weights(repinned.Model, repinned.Revision, repinned.File),
	})
	var row *Deployment
	if s.d.Store != nil {
		if d, gerr := s.d.Store.Get(ctx, p.OrgID, projectID, env.ID, resource); gerr == nil {
			row = d
		}
	}
	plan, err := s.d.Plans.Create(ctx, p, projectID, planning.CreatePlanInput{Environment: env.Name, Catalog: rev.Manifest.CatalogOnly()})
	if err != nil {
		perr := &PlanAfterEditError{Resource: resource, Environment: env.Name, Repin: true, err: err}
		if row == nil {
			return nil, perr
		}
		// As Deploy does, the row says what to do, so the page shows it.
		row.Message = fmt.Sprintf("The manifest has %s on the registry's new pin now. Plan %s from the project to update it.", resource, env.Name)
		if uerr := s.d.Store.Update(context.WithoutCancel(ctx), row); uerr != nil {
			return &DeployResult{Deployment: row}, errors.Join(perr, uerr)
		}
		return &DeployResult{Deployment: row}, perr
	}
	return &DeployResult{PlanID: plan.ID, Deployment: row}, nil
}

// pinUpdate is MoveToPin's one edit of raw for env (Decision 6): the new
// pin's model, revision and file ("" deletes a file the pin doesn't
// have), and cache_mb when env's copy has less than needCacheMB. It
// rewrites the top level when env is the last environment running raw
// that reads those keys from there (soleReader), and env's own block
// otherwise. The caller sets Resource.
func pinUpdate(raw manifest.Resource, env string, repinned *RepinnedError, needCacheMB int64) *manifests.ResourceUpdateInput {
	settings := map[string]string{"model": repinned.Model, "revision": repinned.Revision, "file": repinned.File}
	if cacheGrows(raw.For(env, manifest.Defaults{}).Settings, needCacheMB) {
		settings["cache_mb"] = strconv.FormatInt(needCacheMB, 10)
	}
	// What the edit changes: a key whose new value differs from the top
	// level's, or from env's own block where that sets it (then only the
	// block moves env). A file the pin doesn't have that neither has
	// changes nothing.
	block := raw.Environments[env].Settings
	var keys []string
	for _, key := range slices.Sorted(maps.Keys(settings)) {
		own, set := block[key]
		if settings[key] != raw.Settings[key] || (set && own != settings[key]) {
			keys = append(keys, key)
		}
	}
	u := &manifests.ResourceUpdateInput{Settings: settings}
	if !soleReader(raw, env, keys...) {
		u.Environment = env
	}
	return u
}

// fixedCache reports whether env's deployed engine of resource keeps its
// weights on a Kubernetes volume, on connectionID ("" for wherever it
// runs). The volume's size can't change in place, so the planner refuses
// an update that grows it (catalog-cache-resize), and a change that needs
// a bigger cache there is refused before the manifest is edited. A
// replace onto another target gets a new volume, so it isn't held to it.
func (s *Service) fixedCache(ctx context.Context, orgID, environmentID, resource, connectionID string) (bool, error) {
	if s.d.Deployed == nil {
		return false, nil
	}
	bindings, err := s.d.Deployed.Bindings(ctx, orgID, environmentID)
	if err != nil {
		return false, err
	}
	b, cfg := engineBinding(bindings, resource)
	return b != nil && b.Provider == connections.Kubernetes && cfg.Component.WeightCache != nil &&
		(connectionID == "" || b.ConnectionID == connectionID), nil
}

// cacheGrows reports whether settings' weight cache is smaller than
// needMB, or has no size yet.
func cacheGrows(settings map[string]string, needMB int64) bool {
	have, err := strconv.ParseInt(settings["cache_mb"], 10, 64)
	return err != nil || have < needMB
}

// soleReader reports whether env is the only environment raw runs in
// that reads any of keys from raw's top level, so an edit of those keys
// there changes env's copy alone. An environment reads a key from the top
// level whenever its block doesn't set it, whether the top level sets it
// or not: a key added there reaches every block without one. "target",
// "provider" and "gpu" are fields of the block; any other key is one of
// its settings. It's false when env's own block sets one of keys, since
// a top-level edit wouldn't reach env then.
func soleReader(raw manifest.Resource, env string, keys ...string) bool {
	inBlock := func(name, key string) bool {
		o := raw.Environments[name]
		switch key {
		case "target":
			return o.Target != ""
		case "provider":
			return o.Provider != ""
		case "gpu":
			return o.GPU != nil
		}
		_, ok := o.Settings[key]
		return ok
	}
	for _, key := range keys {
		if inBlock(env, key) {
			return false
		}
	}
	for _, name := range slices.Sorted(maps.Keys(raw.Environments)) {
		if name == env || !enabledIn(raw, name) {
			continue
		}
		for _, key := range keys {
			if !inBlock(name, key) {
				return false
			}
		}
	}
	return true
}
