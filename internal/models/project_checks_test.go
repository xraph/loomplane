package models

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"go.jetify.com/typeid/v2"

	"github.com/xraph/loomplane/internal/gpus"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/internal/security/securitytest"
)

// recordingAuthz writes down every check as "permission resource", with
// "allowed " in front of an answer-only one, and passes it on to inner,
// which can be swapped.
type recordingAuthz struct {
	mu     sync.Mutex
	inner  Authorizer
	checks []string
}

func (r *recordingAuthz) add(check string) Authorizer {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.checks = append(r.checks, check)
	return r.inner
}

func (r *recordingAuthz) swap(inner Authorizer) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.inner, r.checks = inner, nil
}

func (r *recordingAuthz) recorded() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.checks)
}

func (r *recordingAuthz) Require(ctx context.Context, p security.Principal, perm security.Permission, id string) error {
	return r.add(perm.Name()+" "+id).Require(ctx, p, perm, id)
}

func (r *recordingAuthz) Allowed(ctx context.Context, p security.Principal, perm security.Permission) (bool, error) {
	return r.add("allowed "+perm.Name()+" ").Allowed(ctx, p, perm)
}

func (r *recordingAuthz) AllowedOn(ctx context.Context, p security.Principal, perm security.Permission, projectID string) (bool, error) {
	return r.add("allowed "+perm.Name()+" "+projectID).AllowedOn(ctx, p, perm, projectID)
}

// aiChecks are the recorded checks of the ai permissions.
func aiChecks(checks []string) []string {
	var out []string
	for _, c := range checks {
		if strings.HasPrefix(strings.TrimPrefix(c, "allowed "), "ai:") {
			out = append(out, c)
		}
	}
	return out
}

// A model's reads and writes check the model's project, never the whole
// workspace, so a team's grant on the project counts.
func TestModelChecksUseTheProject(t *testing.T) {
	ctx := context.Background()
	rec := &recordingAuthz{inner: deployer}
	d := newDeploy(t, deployRegistry(t, llamaRev, ""), rec, []gpus.GPUTarget{l40sServer()}, boxes()...)
	d.grantLlama()

	pv, err := d.svc.Preview(ctx, member, projectID, stagingID, DeployInput{EntryID: llama8BID, Target: onGPU("nvidia-l40s")})
	if err != nil || !pv.CanDeploy {
		t.Fatalf("preview: %+v, %v", pv, err)
	}
	got := aiChecks(rec.recorded())
	if !slices.Contains(got, "ai:read "+projectID) || !slices.Contains(got, "allowed ai:edit "+projectID) {
		t.Fatalf("Preview checked %v, want ai:read and AllowedOn ai:edit on %s", got, projectID)
	}

	rec.swap(deployer)
	deployLlama(t, d, stagingID)
	if got = aiChecks(rec.recorded()); !slices.Contains(got, "ai:edit "+projectID) {
		t.Fatalf("Deploy checked %v, want ai:edit on %s", got, projectID)
	}

	rec.swap(deployer)
	st, err := d.svc.Status(ctx, member, projectID, stagingID, llamaResource)
	if err != nil || !st.CanEdit {
		t.Fatalf("status: %+v, %v", st, err)
	}
	if got = aiChecks(rec.recorded()); !slices.Contains(got, "ai:read "+projectID) || !slices.Contains(got, "allowed ai:edit "+projectID) {
		t.Fatalf("Status checked %v, want ai:read and AllowedOn ai:edit on %s", got, projectID)
	}
	if _, err = d.svc.CheckPin(ctx, member, projectID, stagingID, llamaResource); err != nil {
		t.Fatalf("check pin: %v", err)
	}
	if st.Deployment == nil {
		t.Fatal("status has no deployment")
	}
	if _, err = d.svc.Update(ctx, member, projectID, stagingID, llamaResource, UpdateInput{Revision: st.Deployment.Revision}); err != nil {
		t.Fatalf("update: %v", err)
	}
	for _, c := range aiChecks(rec.recorded()) {
		if !strings.HasSuffix(c, " "+projectID) {
			t.Errorf("%q checks the whole workspace", c)
		}
	}
}

// twoProjects has projects a and b, each with staging and production.
type twoProjects struct {
	Projects
	a, b string
}

func (f twoProjects) GetEnvironment(_ context.Context, orgID, projectID, id string) (*projects.Environment, error) {
	if orgID != "org" || (projectID != f.a && projectID != f.b) {
		return nil, projects.ErrEnvironmentNotFound
	}
	switch id {
	case stagingID:
		return &projects.Environment{ID: id, OrgID: orgID, ProjectID: projectID, Name: "staging", Kind: projects.KindStaging}, nil
	case productID:
		return &projects.Environment{ID: id, OrgID: orgID, ProjectID: projectID, Name: "production", Kind: projects.KindProduction}, nil
	}
	return nil, projects.ErrEnvironmentNotFound
}

// Every moved check, walked as a workspace role and as a grant on one
// project: the workspace role passes on both projects as before, and the
// grant passes on its project and is refused on the other.
func TestModelProjectGrants(t *testing.T) {
	ctx := context.Background()
	w := securitytest.New(t, "org")
	projA, projB := typeid.MustGenerate("proj").String(), typeid.MustGenerate("proj").String()
	svc := NewService(Deps{Authz: w.Authz, Projects: twoProjects{a: projA, b: projB}})

	// The reads check before the environment is read: an unknown
	// environment is what an allowed call meets next.
	viewer := w.Workspace(t, "ausr_viewer", security.RoleViewer)
	reader := w.ProjectOnly(t, "ausr_reader", security.RoleDeveloper, projA)
	reads := map[string]func(p security.Principal, project string) error{
		"Preview": func(p security.Principal, project string) error {
			_, err := svc.Preview(ctx, p, project, "env_none", DeployInput{})
			return err
		},
		"CheckPin": func(p security.Principal, project string) error {
			_, err := svc.CheckPin(ctx, p, project, "env_none", "m")
			return err
		},
		"Status": func(p security.Principal, project string) error {
			_, err := svc.Status(ctx, p, project, "env_none", "m")
			return err
		},
		"MovePreview": func(p security.Principal, project string) error {
			_, err := svc.MovePreview(ctx, p, project, "env_none", "m", MoveInput{})
			return err
		},
	}
	for name, read := range reads {
		t.Run(name, func(t *testing.T) {
			for _, project := range []string{projA, projB} {
				if err := read(viewer, project); !errors.Is(err, projects.ErrEnvironmentNotFound) {
					t.Fatalf("workspace viewer: %v", err)
				}
			}
			if err := read(reader, projA); !errors.Is(err, projects.ErrEnvironmentNotFound) {
				t.Fatalf("grant on its project: %v", err)
			}
			if err := read(reader, projB); !errors.Is(err, security.ErrForbidden) {
				t.Fatalf("grant on another project: %v, want ErrForbidden", err)
			}
		})
	}

	// The writes need ai:edit in staging (developer) and ai:manage in
	// production (admin). The service has no store, so an allowed call
	// stops with "isn't configured" right after its check.
	writes := map[string]func(p security.Principal, project, env string) error{
		"Deploy": func(p security.Principal, project, env string) error {
			_, err := svc.Deploy(ctx, p, project, env, DeployInput{})
			return err
		},
		"MoveToPin": func(p security.Principal, project, env string) error {
			_, err := svc.MoveToPin(ctx, p, project, env, "m")
			return err
		},
		"Update": func(p security.Principal, project, env string) error {
			_, err := svc.Update(ctx, p, project, env, "m", UpdateInput{})
			return err
		},
		"Retry": func(p security.Principal, project, env string) error {
			_, err := svc.Retry(ctx, p, project, env, "m")
			return err
		},
		"Move": func(p security.Principal, project, env string) error {
			_, err := svc.Move(ctx, p, project, env, "m", MoveInput{})
			return err
		},
		"Remove": func(p security.Principal, project, env string) error {
			_, err := svc.Remove(ctx, p, project, env, "m")
			return err
		},
		"CancelWaiting": func(p security.Principal, project, env string) error {
			_, err := svc.CancelWaiting(ctx, p, project, env, "m")
			return err
		},
	}
	allowed := func(err error) bool {
		return err != nil && !errors.Is(err, security.ErrForbidden) && strings.Contains(err.Error(), "configured")
	}
	envs := []struct {
		env, role string
	}{{stagingID, security.RoleDeveloper}, {productID, security.RoleAdmin}}
	for i, e := range envs {
		ws := w.Workspace(t, "ausr_ws"+e.role, e.role)
		grant := w.TeamGrant(t, "ausr_grant"+e.role, e.role, projA)
		below := w.Workspace(t, "ausr_below"+e.role, []string{security.RoleViewer, security.RoleDeveloper}[i])
		for name, write := range writes {
			t.Run(name+"/"+e.env, func(t *testing.T) {
				for _, project := range []string{projA, projB} {
					if err := write(ws, project, e.env); !allowed(err) {
						t.Fatalf("workspace %s: %v", e.role, err)
					}
					if err := write(below, project, e.env); !errors.Is(err, security.ErrForbidden) {
						t.Fatalf("workspace role below %s: %v, want ErrForbidden", e.role, err)
					}
				}
				if err := write(grant, projA, e.env); !allowed(err) {
					t.Fatalf("%s grant on its project: %v", e.role, err)
				}
				if err := write(grant, projB, e.env); !errors.Is(err, security.ErrForbidden) {
					t.Fatalf("%s grant on another project: %v, want ErrForbidden", e.role, err)
				}
			})
		}
		// The reconcile's recheck of the person a deployment acts as.
		t.Run("stillAllowed/"+e.env, func(t *testing.T) {
			for _, tc := range []struct {
				p       security.Principal
				project string
				want    bool
			}{{ws, projA, true}, {ws, projB, true}, {grant, projA, true}, {grant, projB, false}, {below, projA, false}} {
				env, err := svc.environment(ctx, "org", tc.project, e.env)
				if err != nil {
					t.Fatal(err)
				}
				if ok, aerr := svc.stillAllowed(ctx, tc.p, env); aerr != nil || ok != tc.want {
					t.Fatalf("%s on %s: %v, %v, want %v", tc.p.UserID, tc.project, ok, aerr, tc.want)
				}
			}
		})
	}
}

// What a page offers (Preview's CanDeploy, Status's CanEdit) follows a
// team's grant on the model's project, and only there.
func TestModelPagesOfferWhatAProjectGrantAllows(t *testing.T) {
	ctx := context.Background()
	rec := &recordingAuthz{inner: deployer}
	d := newDeploy(t, deployRegistry(t, llamaRev, ""), rec, []gpus.GPUTarget{l40sServer()}, boxes()...)
	deployLlama(t, d, stagingID)

	w := securitytest.New(t, "org")
	rec.swap(w.Authz)
	other := typeid.MustGenerate("proj").String()
	people := []struct {
		name string
		p    security.Principal
		want bool
	}{
		{"workspace developer", w.Workspace(t, "ausr_dev", security.RoleDeveloper), true},
		{"workspace viewer", w.Workspace(t, "ausr_viewer", security.RoleViewer), false},
		{"developer grant here", w.TeamGrant(t, "ausr_grace", security.RoleDeveloper, projectID), true},
		{"developer grant elsewhere", w.TeamGrant(t, "ausr_ray", security.RoleDeveloper, other), false},
	}
	for _, tt := range people {
		t.Run(tt.name, func(t *testing.T) {
			pv, err := d.svc.Preview(ctx, tt.p, projectID, stagingID, DeployInput{EntryID: llama70BID, Target: onGPU("nvidia-l40s")})
			if err != nil {
				t.Fatal(err)
			}
			if pv.CanDeploy != tt.want {
				t.Errorf("CanDeploy %v, want %v (problems %v)", pv.CanDeploy, tt.want, codes(pv))
			}
			st, err := d.svc.Status(ctx, tt.p, projectID, stagingID, llamaResource)
			if err != nil {
				t.Fatal(err)
			}
			if st.CanEdit != tt.want {
				t.Errorf("CanEdit %v, want %v", st.CanEdit, tt.want)
			}
		})
	}
}
