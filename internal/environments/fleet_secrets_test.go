package environments

import (
	"context"
	"errors"
	"testing"

	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/pkg/fleet"
)

type instanceSecretStore struct {
	Store
	variable *Variable
}

func (s instanceSecretStore) GetVariable(_ context.Context, org, id string) (*Variable, error) {
	if org != s.variable.OrgID || id != s.variable.ID {
		return nil, ErrNotFound
	}
	return s.variable, nil
}

type instanceSecretProjects struct {
	Projects
	env *projects.Environment
}

func (s instanceSecretProjects) GetEnvironment(_ context.Context, org, project, id string) (*projects.Environment, error) {
	if org != "org" || project != "project" || id != s.env.ID {
		return nil, projects.ErrEnvironmentNotFound
	}
	return s.env, nil
}

type instanceSecretVersions struct {
	Secrets
	reads   int
	version int64
}

func (s *instanceSecretVersions) RevealVersion(_ context.Context, org, id string, version int64) ([]byte, error) {
	s.reads++
	s.version = version
	if org != "org" || id != "secret" || version != 2 {
		return nil, ErrNotFound
	}
	return []byte("pinned-value"), nil
}
func TestInstanceSecretScope(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Variable, *projects.Environment, *PinnedSecretScope, *fleet.SecretRef)
	}{
		{"valid", func(*Variable, *projects.Environment, *PinnedSecretScope, *fleet.SecretRef) {}},
		{"foreign-org", func(v *Variable, _ *projects.Environment, _ *PinnedSecretScope, _ *fleet.SecretRef) {
			v.OrgID = "other"
		}},
		{"foreign-project", func(v *Variable, _ *projects.Environment, _ *PinnedSecretScope, _ *fleet.SecretRef) {
			v.ProjectID = "other"
		}},
		{"project-wide", func(v *Variable, _ *projects.Environment, _ *PinnedSecretScope, _ *fleet.SecretRef) {
			v.environmentID = ""
			v.Environment = ""
		}},
		{"sibling-environment", func(v *Variable, _ *projects.Environment, _ *PinnedSecretScope, _ *fleet.SecretRef) {
			v.environmentID = "other"
		}},
		{"sibling-service", func(v *Variable, _ *projects.Environment, _ *PinnedSecretScope, _ *fleet.SecretRef) {
			v.Service = "svc:artifact/other"
		}},
		{"wrong-instance", func(_ *Variable, e *projects.Environment, _ *PinnedSecretScope, _ *fleet.SecretRef) {
			e.InstanceID = "other"
		}},
		{"non-secret", func(v *Variable, _ *projects.Environment, _ *PinnedSecretScope, _ *fleet.SecretRef) { v.Secret = false }},
		{"organization-wide", func(v *Variable, _ *projects.Environment, _ *PinnedSecretScope, _ *fleet.SecretRef) {
			v.Scope = ScopeOrganization
			v.ProjectID = ""
		}},
		{"unknown-version", func(_ *Variable, _ *projects.Environment, _ *PinnedSecretScope, r *fleet.SecretRef) { r.Version = 3 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := &Variable{ID: "secret", OrgID: "org", ProjectID: "project", Scope: ScopeService, Service: "svc:artifact/api", Secret: true, Environment: "instance-env", environmentID: "environment"}
			env := &projects.Environment{ID: "environment", Name: "instance-env", InstanceID: "instance"}
			scope := PinnedSecretScope{OrgID: "org", ProjectID: "project", EnvironmentID: "environment", InstanceID: "instance", Service: "api"}
			ref := fleet.SecretRef{ID: "secret", Version: 2}
			tc.change(v, env, &scope, &ref)
			versions := &instanceSecretVersions{}
			s := NewService(instanceSecretStore{variable: v}, versions, instanceSecretProjects{env: env}, nil, nil, nil, nil)
			value, err := s.ResolvePinnedSecret(t.Context(), scope, ref)
			if tc.name == "valid" {
				if err != nil || string(value) != "pinned-value" || versions.version != 2 {
					t.Fatalf("pinned read: %q %v", value, err)
				}
				return
			}
			if !errors.Is(err, ErrNotFound) {
				t.Fatalf("scope accepted or disclosed: %q %v", value, err)
			}
			if tc.name != "unknown-version" && versions.reads != 0 {
				t.Fatal("read Vault before validating scope")
			}
		})
	}
}
