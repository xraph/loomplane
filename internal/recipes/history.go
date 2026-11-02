package recipes

import (
	"errors"
	"fmt"
)

type historicalRelease struct{ kind, version, revision string }

// RegisterHistorical retains a reviewed recipe for replaying recorded plans.
// It never changes the version offered to a new plan.
func (r *Registry) RegisterHistorical(k Kind) error { return r.registerHistorical(k, "") }

// RegisterBlockedHistorical retains identity for cleanup while refusing unsafe replay.
func (r *Registry) RegisterBlockedHistorical(k Kind, reason string) error {
	if reason == "" {
		return errors.New("blocked history needs an upgrade reason")
	}
	return r.registerHistorical(k, reason)
}

// ReplayError reports a reviewed upgrade required for an unsafe old revision.
func (k Kind) ReplayError() error {
	if k.replayError != "" {
		return errors.New(k.replayError)
	}
	return nil
}

func (r *Registry) registerHistorical(k Kind, reason string) error {
	k = k.clone()
	normalizeKind(&k)
	if err := errors.Join(validateKind(k)...); err != nil {
		return err
	}
	if len(k.Releases) != 1 {
		return errors.New("a historical recipe must contain exactly one release")
	}
	release := k.Releases[0]
	key := historicalRelease{k.Name, release.Version, release.Revision}
	r.mu.Lock()
	defer r.mu.Unlock()
	current, ok := r.kinds[k.Name]
	if !ok {
		return errors.New("a historical recipe requires its current kind")
	}
	if latest, exists := current.release(release.Version); exists && latest.Revision == release.Revision {
		return errors.New("a historical recipe cannot replace the current revision")
	}
	if _, exists := r.history[key]; exists {
		return errors.New("a historical recipe revision is already registered")
	}
	if err := errors.Join(crossFindings(k, r.kinds, r.engines)...); err != nil {
		return err
	}
	k.replayError = reason
	r.history[key] = k
	return nil
}

// ResolveRevision resolves only catalog-owned definitions, including explicitly
// retained history. Callers must still compare their complete recorded config.
func (r *Registry) ResolveRevision(kind, version, revision, architecture, target string) (Kind, *Composition, error) {
	r.mu.RLock()
	k, ok := r.kinds[kind]
	if ok {
		release, exists := k.release(version)
		if !exists || release.Revision != revision {
			k, ok = r.history[historicalRelease{kind, version, revision}]
		}
	}
	k = k.clone()
	r.mu.RUnlock()
	if !ok {
		return Kind{}, nil, fmt.Errorf("%s %s revision %s is not retained in the catalog", kind, version, revision)
	}
	composition, err := resolveKind(k, version, architecture, target)
	return k, composition, err
}
