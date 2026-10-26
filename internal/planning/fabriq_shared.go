package planning

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"

	"github.com/xraph/loomplane/pkg/manifest"
)

// fabriqKinds are the composed kinds that keep Fabriq's schema, and its
// cluster-wide fabriq_app role, in their Postgres. Two of them on one
// database would reset each other's role password and revoke each other's
// keys, so each needs a database of its own.
var fabriqKinds = map[string]bool{"fabriq": true, "fabriq-library": true}

// fabriqDatabase is the Postgres a Fabriq resource connects in the
// environment, as "resource:<name>" or "external:<endpoint>" with the
// path that sets it, or "" when it provisions its own (or is off).
func fabriqDatabase(m *manifest.Manifest, env, name string) (key, path string) {
	r := m.Resources[name]
	if !fabriqKinds[r.Kind] || !manifest.On(r.For(env, m.Defaults).Enabled) {
		return "", ""
	}
	u := r.Uses["postgres"]
	base := "resources." + name + ".uses.postgres"
	switch {
	case u.Resource != "":
		return "resource:" + u.Resource, base + ".resource"
	case u.External != "":
		return "external:" + u.External, base + ".external"
	}
	return "", ""
}

// checkFabriqDatabases refuses a Fabriq resource whose Postgres another
// Fabriq resource already uses: one earlier in this manifest, by name, or
// one in another of the organization's environments (FabriqPostgres).
func (p *planner) checkFabriqDatabases() {
	m := p.in.Manifest
	if m == nil {
		return
	}
	first := map[string]string{}
	for endpoint, other := range p.in.FabriqPostgres {
		first["external:"+endpoint] = other
	}
	for _, name := range slices.Sorted(maps.Keys(m.Resources)) {
		key, path := fabriqDatabase(m, p.in.Environment, name)
		if key == "" {
			continue
		}
		other, taken := first[key]
		if !taken {
			first[key] = name
			continue
		}
		p.problem(Finding{
			Code: "catalog-dependency-shared", Subject: ResourceSubject, Name: name, Path: path,
			Message: fmt.Sprintf("%s's Postgres is already used by Fabriq %s. Each Fabriq needs its own database.", name, other),
		})
	}
}

// FabriqPostgres is Input.FabriqPostgres from the organization's resource
// bindings: the external endpoints each Fabriq outside environmentID
// connects as its Postgres.
func FabriqPostgres(bindings []*Binding, environmentID string) map[string]string {
	out := map[string]string{}
	for _, b := range bindings {
		if b.Subject != ResourceSubject || b.EnvironmentID == environmentID {
			continue
		}
		var cfg ResourceConfig
		if json.Unmarshal(b.Config, &cfg) != nil || cfg.Composition == nil || !fabriqKinds[cfg.Composition.Kind] {
			continue
		}
		choice := cfg.Composition.Dependencies["postgres"]
		if choice.Mode != DependencyExternal || choice.External == "" {
			continue
		}
		if _, ok := out[choice.External]; !ok {
			out[choice.External] = b.Name
		}
	}
	return out
}

// orgResources lists an organization's resource bindings across its
// projects and environments. *PgStore is one; a Store that isn't plans
// without the check against other environments.
type orgResources interface {
	OrgResourceBindings(ctx context.Context, orgID string) ([]*Binding, error)
}

var _ orgResources = (*PgStore)(nil)

// fabriqPostgres is Input.FabriqPostgres for a plan in environmentID.
func (s *Service) fabriqPostgres(ctx context.Context, orgID, environmentID string) (map[string]string, error) {
	lister, ok := s.store.(orgResources)
	if !ok {
		return nil, nil
	}
	bindings, err := lister.OrgResourceBindings(ctx, orgID)
	if err != nil {
		return nil, err
	}
	return FabriqPostgres(bindings, environmentID), nil
}
