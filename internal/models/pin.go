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
	"github.com/xraph/loomplane/internal/models/registry"
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
	here, err = modelIn(rev.Manifest, env, resource)
	return here, rev.Manifest, err
}

// modelIn is the model resource of m that runs in env, as env runs it.
func modelIn(m *manifest.Manifest, env *projects.Environment, resource string) (manifest.Resource, error) {
	if m == nil {
		return manifest.Resource{}, errors.New("models: the project's manifest doesn't parse")
	}
	r, ok := m.Resources[resource]
	if !ok || !recipes.IsModelKind(r.Kind) {
		return manifest.Resource{}, fmt.Errorf("%w: the manifest has no model %s", ErrNotFound, resource)
	}
	if !enabledIn(r, env.Name) {
		return manifest.Resource{}, fmt.Errorf("%w: %s doesn't run in %s", ErrNotFound, resource, env.Name)
	}
	return r.In(env.Name, m.Defaults), nil
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
	if s.d.Plans == nil || s.d.Manifests == nil {
		return nil, errors.New("models: planning isn't configured")
	}
	// The pin and the edit are decided from the manifest the edit is made
	// to (decideEdit).
	var e registry.Entry
	var q registry.Quantization
	var repinned *RepinnedError
	rev, err := s.decideEdit(ctx, p, projectID, fmt.Sprintf("moving %s in %s to the new pin", resource, env.Name), func(cur *manifests.Revision) (manifests.EditInput, error) {
		here, merr := modelIn(cur.Manifest, env, resource)
		if merr != nil {
			return manifests.EditInput{}, merr
		}
		var ok bool
		if e, q, ok = registryQuantization(s.d.Registry, here.Settings); !ok {
			return manifests.EditInput{}, fmt.Errorf("%w: %s doesn't run a registry quantization, so there's no pin to move it to", ErrInvalid, resource)
		}
		if perr := checkPin(resource, e, q, here.Settings); !errors.As(perr, &repinned) {
			return manifests.EditInput{}, fmt.Errorf("%w: %s already runs the weights the registry pins in %s", ErrInvalid, resource, env.Name)
		}
		need := engines.CacheMB(q.WeightBytes)
		if cacheGrows(here.Kind, here.Settings, need) {
			fixed, ferr := s.fixedCache(ctx, p.OrgID, env.ID, resource, "")
			if ferr != nil {
				return manifests.EditInput{}, ferr
			}
			if fixed {
				return manifests.EditInput{}, fmt.Errorf("%w: %s keeps its weight cache on a Kubernetes volume, which can't grow in place. Remove %s from %s and deploy it again to move to the new pin",
					ErrInvalid, resource, resource, env.Name)
			}
		}
		update, uerr := pinUpdate(cur.Manifest.Resources[resource], env.Name, repinned, need)
		if uerr != nil {
			return manifests.EditInput{}, uerr
		}
		update.Resource = resource
		return manifests.EditInput{UpdateResource: update}, nil
	})
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
//
// A block can't say "no file": "" deletes the block's key, and env would
// read the top level's file again. So a move to a pin with no file is
// refused when it has to go in env's block and the top level names a
// file, before anything is written.
func pinUpdate(raw manifest.Resource, env string, repinned *RepinnedError, needCacheMB int64) (*manifests.ResourceUpdateInput, error) {
	settings := map[string]string{"model": repinned.Model, "revision": repinned.Revision, "file": repinned.File}
	if cacheGrows(raw.Kind, raw.For(env, manifest.Defaults{}).Settings, needCacheMB) {
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
	if soleReader(raw, env, keys...) {
		return u, nil
	}
	u.Environment = env
	for _, key := range keys {
		if settings[key] == "" && raw.Settings[key] != "" {
			return nil, fmt.Errorf("%w: %s", ErrInvalid, topLevelRefusal("the registry's new pin", key, env, raw.Settings[key]))
		}
	}
	return u, nil
}

// topLevelRefusal says why env's block can't take weights, what, that
// have no key while raw's top level sets it to value: a block can't say
// "no file", so env would still read the top level's. It has no full
// stop, so it ends an error or, with one, a preview's problem.
func topLevelRefusal(what, key, env, value string) string {
	return fmt.Sprintf("%s has no %s, and %s would still read %s %s from the top level of the model's manifest entry, which its other environments run. Deploy it to %s as a separately named model, or remove it from the project's other environments first",
		what, key, env, key, value, env)
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

// cacheGrows reports whether the weight cache of a kind resource with
// settings is smaller than needMB (cacheMB).
func cacheGrows(kind string, settings map[string]string, needMB int64) bool {
	return cacheMB(kind, settings) < needMB
}

// cacheMB is the weight cache a kind resource with settings has on
// Kubernetes, sized the way the planner sizes the volume
// (recipes.WeightCache.SizeMB): its cache_mb when that's larger than the
// recipe's, else the recipe's, so a move or pin move refuses exactly the
// growth the planner would (catalog-cache-resize). An engine that keeps
// its weights on a plain volume has its cache_mb or the volume's size,
// and a kind the recipes don't have its cache_mb or the smallest cache.
func cacheMB(kind string, settings map[string]string) int64 {
	c, ok := engineComponent(kind)
	if ok && c.WeightCache != nil {
		return int64(c.WeightCache.SizeMB(settings))
	}
	have, err := strconv.ParseInt(settings["cache_mb"], 10, 64)
	if err != nil {
		return defaultCacheMB(kind)
	}
	return have
}

// defaultCacheMB is the weight cache a model kind's engine starts with on
// Kubernetes, the KubernetesSizeMB of its recipe, or the smallest cache
// for a kind the recipes don't have.
func defaultCacheMB(kind string) int64 {
	c, ok := engineComponent(kind)
	switch {
	case !ok:
	case c.WeightCache != nil:
		return int64(c.WeightCache.KubernetesSizeMB)
	case c.Volume != nil:
		return int64(c.Volume.KubernetesSizeMB)
	}
	return engines.MinCacheMB
}

// engineComponent is the model engine component of kind's recipe.
func engineComponent(kind string) (recipes.Component, bool) {
	k, ok := recipes.Default().Kind(kind)
	if !ok {
		return recipes.Component{}, false
	}
	for _, c := range k.Components {
		if c.Name == recipes.ModelEngine {
			return c, true
		}
	}
	return recipes.Component{}, false
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
