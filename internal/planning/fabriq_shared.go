package planning

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

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

// checkFabriqDatabases refuses a Fabriq resource whose Postgres server
// another Fabriq resource already uses: one earlier in this manifest, by
// name, or one in another of the organization's environments
// (FabriqPostgres). The server, not just the database, is what they can't
// share: each Fabriq creates the server-wide fabriq_app role.
func (p *planner) checkFabriqDatabases() {
	m := p.in.Manifest
	if m == nil {
		return
	}
	here := map[string]string{}
	for _, name := range slices.Sorted(maps.Keys(m.Resources)) {
		key, path := fabriqDatabase(m, p.in.Environment, name)
		if key == "" {
			continue
		}
		endpoint, external := strings.CutPrefix(key, "external:")
		elsewhere, inOther := p.in.FabriqPostgres[endpoint]
		earlier, inManifest := here[key]
		var used string
		switch {
		case external && inOther:
			used = fmt.Sprintf("Fabriq %s %s. Each Fabriq needs its own Postgres server: turn Fabriq off in one environment or point it at another server.", elsewhere.Name, elsewhere.where())
		case inManifest:
			used = fmt.Sprintf("Fabriq %s. Each Fabriq needs its own Postgres server: point one of them at another server.", earlier)
		default:
			here[key] = name
			continue
		}
		p.problem(Finding{
			Code: "catalog-dependency-shared", Subject: ResourceSubject, Name: name, Path: path,
			Message: name + "'s Postgres server is already used by " + used,
		})
	}
}

// FabriqUser is a Fabriq resource in another environment that uses a
// Postgres server.
type FabriqUser struct {
	Name string
	// Environment is the name of the environment it runs in, when known.
	Environment string
	// Other is a Fabriq in another project, and Project that project's
	// name, when known.
	Other   bool
	Project string
}

// where is "in staging", "in project shop (staging)", or "in another
// environment" or "in another project" when the names aren't known.
func (u FabriqUser) where() string {
	switch {
	case u.Other && u.Project != "" && u.Environment != "":
		return "in project " + u.Project + " (" + u.Environment + ")"
	case u.Other:
		return "in another project"
	case u.Environment != "":
		return "in " + u.Environment
	}
	return "in another environment"
}

// FabriqPostgres is Input.FabriqPostgres from the organization's resource
// bindings: the external endpoints each Fabriq outside environmentID
// connects as its Postgres, with one Fabriq that does.
func FabriqPostgres(resources []*OrgResource, projectID, environmentID string) map[string]FabriqUser {
	out := map[string]FabriqUser{}
	for _, r := range resources {
		b := r.Binding
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
		if _, ok := out[choice.External]; ok {
			continue
		}
		user := FabriqUser{Name: b.Name, Environment: r.Environment}
		if b.ProjectID != projectID {
			user.Other, user.Project = true, r.Project
		}
		out[choice.External] = user
	}
	return out
}

// orgResources lists an organization's resource bindings across its
// projects and environments. *PgStore is one; a Store that isn't plans
// without the check against other environments.
type orgResources interface {
	OrgResourceBindings(ctx context.Context, orgID string) ([]*OrgResource, error)
}

var _ orgResources = (*PgStore)(nil)

// fabriqPostgres is Input.FabriqPostgres for plan.
func (s *Service) fabriqPostgres(ctx context.Context, plan *Plan) (map[string]FabriqUser, error) {
	lister, ok := s.store.(orgResources)
	if !ok {
		return nil, nil
	}
	resources, err := lister.OrgResourceBindings(ctx, plan.OrgID)
	if err != nil {
		return nil, err
	}
	return FabriqPostgres(resources, plan.ProjectID, plan.EnvironmentID), nil
}
