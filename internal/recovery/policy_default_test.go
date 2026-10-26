package recovery

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/recipes"
	"github.com/xraph/loomplane/internal/security"
)

type defaultPolicyStore struct {
	PolicyStore
	input   *Policy
	stored  *Policy
	created bool
	err     error
}

func (s *defaultPolicyStore) EnsurePolicy(_ context.Context, p *Policy) (*Policy, bool, error) {
	s.input = p
	if s.stored != nil || s.err != nil {
		return s.stored, false, s.err
	}
	s.stored, s.created = p, true
	return p, true, nil
}

type policyAuditor struct{ events []projects.AuditEvent }

func (a *policyAuditor) Record(_ context.Context, e projects.AuditEvent) {
	a.events = append(a.events, e)
}

type policyAuthorization struct {
	permission security.Permission
	project    string
}

func (a *policyAuthorization) Require(_ context.Context, _ security.Principal, permission security.Permission, project string) error {
	a.permission, a.project = permission, project
	return nil
}

func TestDefaultPolicyPreservesExistingOperatorChoice(t *testing.T) {
	existing := &Policy{ID: "operator-policy", OrgID: "org", ProjectID: "project", EnvironmentID: "environment", Environment: "production", Resource: "data", Enabled: false, EveryHours: 72, CreatedBy: "operator", NextAt: time.Now().Add(9 * time.Hour), LastError: "previous failure"}
	store := &defaultPolicyStore{stored: existing}
	audit := &policyAuditor{}
	auth := &policyAuthorization{}
	s := Service{Authz: auth, Projects: fixedProjects{}, Bindings: fixedBindings{bindingFor(t, "data", engineConfig(t, "server", "postgres"))}, Policies: store, Audit: audit}
	got, err := s.EnsurePolicy(t.Context(), security.Principal{OrgID: "org", UserID: "deployer"}, "project", PolicyInput{Environment: "production", Resource: "data", EveryHours: 24, Enabled: true})
	if err != nil || got != existing || store.created || len(audit.events) != 0 {
		t.Fatalf("existing policy: %+v, %v; audit: %+v", got, err, audit.events)
	}
	if auth.permission != security.BackupManage || auth.project != "project" {
		t.Fatalf("authority: %+v", auth)
	}
}

func TestDefaultPolicyCreatesScopedDailyScheduleOnce(t *testing.T) {
	store := &defaultPolicyStore{}
	audit := &policyAuditor{}
	s := Service{Authz: allowAll{}, Projects: fixedProjects{}, Bindings: fixedBindings{bindingFor(t, "data", engineConfig(t, "server", "postgres"))}, Policies: store, Audit: audit}
	p := security.Principal{OrgID: "org", UserID: "deployer"}
	in := PolicyInput{Environment: "production", Resource: "data", EveryHours: 24, Enabled: true}
	before := time.Now().UTC()
	got, err := s.EnsurePolicy(t.Context(), p, "project", in)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID == "" || got.OrgID != "org" || got.ProjectID != "project" || got.EnvironmentID != "environment" || got.Environment != "production" || got.Resource != "data" || got.CreatedBy != "deployer" || !got.Enabled || got.EveryHours != 24 {
		t.Fatalf("scope: %+v", got)
	}
	if got.NextAt.Before(before.Add(24*time.Hour)) || got.NextAt.After(time.Now().Add(24*time.Hour)) {
		t.Fatalf("due time: %v", got.NextAt)
	}
	again, err := s.EnsurePolicy(t.Context(), p, "project", in)
	if err != nil || again.ID != got.ID || !again.NextAt.Equal(got.NextAt) || len(audit.events) != 1 {
		t.Fatalf("retry changed the schedule: %+v, %v; audit: %+v", again, err, audit.events)
	}
}

func TestDefaultPolicyChecksAuthorityBeforeReadingSource(t *testing.T) {
	s := Service{Authz: denied{}}
	_, err := s.EnsurePolicy(t.Context(), security.Principal{}, "project", PolicyInput{})
	if !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("authorization: %v", err)
	}
}

func TestDefaultPolicyRejectsUnsupportedSources(t *testing.T) {
	for name, cfg := range map[string]planning.ResourceConfig{
		"external":         {Provider: planning.DependencyExternal, Kind: "postgres"},
		"composition":      {Provider: "server", Kind: "fabriq", Composition: &planning.CompositionConfig{Kind: "fabriq"}},
		"projection":       {Provider: "server", Kind: "fabriq", Component: &planning.ComponentConfig{Protection: recipes.ProtectionRebuildable}},
		"kubernetes-cache": engineConfig(t, "kubernetes", "redis"),
	} {
		t.Run(name, func(t *testing.T) {
			s := Service{Authz: allowAll{}, Projects: fixedProjects{}, Bindings: fixedBindings{bindingFor(t, "data", cfg)}}
			_, err := s.EnsurePolicy(t.Context(), security.Principal{OrgID: "org", UserID: "deployer"}, "project", PolicyInput{Environment: "production", Resource: "data", EveryHours: 24, Enabled: true})
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("unsupported source: %v", err)
			}
		})
	}
}

func TestDefaultPolicyPropagatesStoreFailure(t *testing.T) {
	failure := errors.New("storage unavailable")
	store := &defaultPolicyStore{err: failure}
	audit := &policyAuditor{}
	s := Service{Authz: allowAll{}, Projects: fixedProjects{}, Bindings: fixedBindings{bindingFor(t, "data", engineConfig(t, "server", "postgres"))}, Policies: store, Audit: audit}
	_, err := s.EnsurePolicy(t.Context(), security.Principal{OrgID: "org", UserID: "deployer"}, "project", PolicyInput{Environment: "production", Resource: "data", EveryHours: 24, Enabled: true})
	if !errors.Is(err, failure) || len(audit.events) != 0 {
		t.Fatalf("store failure: %v; audit: %+v", err, audit.events)
	}
}

func TestDefaultPolicyUsesTheQualifiedTimescaleProfile(t *testing.T) {
	for _, provider := range []string{"server", "kubernetes"} {
		t.Run(provider, func(t *testing.T) {
			recipe, err := recipes.Default().LookupEngine("timescaledb", "16", "arm64", provider)
			if err != nil {
				t.Fatal(err)
			}
			cfg := planning.ResourceConfig{Provider: provider, Kind: "timescaledb", Catalog: recipe}
			store := &defaultPolicyStore{}
			s := Service{Authz: allowAll{}, Projects: fixedProjects{}, Bindings: fixedBindings{bindingFor(t, "fabriq--postgres", cfg)}, Policies: store}
			got, err := s.EnsurePolicy(t.Context(), security.Principal{OrgID: "org", UserID: "deployer"}, "project", PolicyInput{Environment: "production", Resource: "fabriq--postgres", EveryHours: 24, Enabled: true})
			if provider == "kubernetes" {
				if !errors.Is(err, ErrNotOnKubernetes) || store.input != nil {
					t.Fatalf("unqualified target: %+v, %v", got, err)
				}
			} else if err != nil || got.Resource != "fabriq--postgres" || !got.Enabled || got.EveryHours != 24 {
				t.Fatalf("Timescale policy: %+v, %v", got, err)
			}
		})
	}
}

func TestDefaultPolicyRejectsInvalidScheduleAndMissingCreator(t *testing.T) {
	for _, tc := range []struct {
		name, creator string
		hours         int
	}{
		{"zero interval", "deployer", 0},
		{"long interval", "deployer", 169},
		{"no creator", "", 24},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := Service{Authz: allowAll{}, Projects: fixedProjects{}}
			_, err := s.EnsurePolicy(t.Context(), security.Principal{OrgID: "org", UserID: tc.creator}, "project", PolicyInput{Environment: "production", Resource: "data", EveryHours: tc.hours, Enabled: true})
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("invalid default: %v", err)
			}
		})
	}
}

func TestDefaultPolicyDoesNotUseAnotherProjectsBinding(t *testing.T) {
	b := bindingFor(t, "data", engineConfig(t, "server", "postgres"))
	b.ProjectID = "other-project"
	s := Service{Authz: allowAll{}, Projects: fixedProjects{}, Bindings: fixedBindings{b}}
	_, err := s.EnsurePolicy(t.Context(), security.Principal{OrgID: "org", UserID: "deployer"}, "project", PolicyInput{Environment: "production", Resource: "data", EveryHours: 24, Enabled: true})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("foreign binding: %v", err)
	}
}
