// Package securitytest gives other packages' tests the real Authorizer over
// Warden's in-memory store, with people who hold a role on the whole
// workspace or only on one project. It's for tests: nothing else imports it.
package securitytest

import (
	"context"
	"sync"
	"testing"

	"github.com/xraph/warden"
	"github.com/xraph/warden/assignment"
	"github.com/xraph/warden/store/memory"

	"github.com/xraph/loomplane/internal/security"
)

// World is one organization's Warden tenant.
type World struct {
	// Authz checks without repairing anyone, so a person keeps exactly
	// the rows World gave them.
	Authz *security.Authorizer
	OrgID string

	eng   *warden.Engine
	store *memory.Store

	mu     sync.Mutex
	grants map[string][]security.ScopedRole
}

// New returns an empty tenant for orgID.
func New(t testing.TB, orgID string) *World {
	t.Helper()
	store := memory.New()
	eng, err := warden.NewEngine(warden.WithStore(store))
	if err != nil {
		t.Fatalf("warden: %v", err)
	}
	return &World{
		Authz: security.NewAuthorizer(eng, store, nil),
		OrgID: orgID, eng: eng, store: store,
		grants: map[string][]security.ScopedRole{},
	}
}

// Workspace returns a person who holds role (security.RoleViewer and up)
// across the workspace, as a member role gives it.
func (w *World) Workspace(t testing.TB, userID, role string) security.Principal {
	t.Helper()
	if err := w.Authz.Assign(context.Background(), w.OrgID, userID, role, "test"); err != nil {
		t.Fatalf("assign %s: %v", role, err)
	}
	return security.Principal{UserID: userID, OrgID: w.OrgID}
}

// TeamGrant returns a person as a team grant leaves them: a viewer member,
// with role (security.RoleDeveloper or security.RoleAdmin) on projectID
// through a team. projectID must be a real "proj" typeid.
func (w *World) TeamGrant(t testing.TB, userID, role, projectID string) security.Principal {
	t.Helper()
	if _, ok := security.ProjectNamespace(projectID); !ok {
		t.Fatalf("%q isn't a project typeid, so a check on it stays at the root", projectID)
	}
	w.mu.Lock()
	w.grants[userID] = []security.ScopedRole{{Role: role, ProjectID: projectID, TeamID: "team_test"}}
	w.mu.Unlock()
	syncer := security.NewAuthorizer(w.eng, w.store, func(context.Context, string, string) (string, bool, error) {
		return security.MemberViewer, true, nil
	})
	syncer.SetTeamGrants(func(_ context.Context, _, user string) ([]security.ScopedRole, error) {
		w.mu.Lock()
		defer w.mu.Unlock()
		return w.grants[user], nil
	})
	if err := syncer.Sync(context.Background(), w.OrgID, userID); err != nil {
		t.Fatalf("sync: %v", err)
	}
	return security.Principal{UserID: userID, OrgID: w.OrgID}
}

// ProjectOnly returns a person whose only Warden row is role on
// projectID's namespace, written by Sync as a team grant writes it. They
// hold nothing at the root, not even viewer, so a check that doesn't pass
// projectID refuses them, and so does a check on any other project. That
// shows a call passes its project even for what every member may do.
func (w *World) ProjectOnly(t testing.TB, userID, role, projectID string) security.Principal {
	t.Helper()
	ctx := context.Background()
	p := w.TeamGrant(t, userID, role, projectID)
	rows, err := w.store.ListAssignments(ctx, &assignment.ListFilter{
		TenantID: w.OrgID, SubjectKind: string(warden.SubjectUser), SubjectID: userID,
	})
	if err != nil {
		t.Fatal(err)
	}
	projectRows := 0
	for _, row := range rows {
		if row.NamespacePath != "" {
			projectRows++
			continue
		}
		if err := w.store.DeleteAssignment(ctx, w.OrgID, row.ID); err != nil {
			t.Fatal(err)
		}
	}
	if projectRows != 1 {
		t.Fatalf("%s has %d project rows, want 1", userID, projectRows)
	}
	w.eng.InvalidateSubject(ctx, w.OrgID, warden.SubjectUser, userID)
	return p
}

// Recorder is an Authorizer that writes down every check as "permission
// resource" and passes it on to the real one.
type Recorder struct {
	*security.Authorizer

	mu     sync.Mutex
	checks []string
}

// NewRecorder records the checks a passes on.
func NewRecorder(a *security.Authorizer) *Recorder { return &Recorder{Authorizer: a} }

// Require implements the services' Authorizer.
func (r *Recorder) Require(ctx context.Context, p security.Principal, perm security.Permission, resourceID string) error {
	r.add(perm.Name() + " " + resourceID)
	return r.Authorizer.Require(ctx, p, perm, resourceID)
}

// AllowedOn implements the services' Authorizer.
func (r *Recorder) AllowedOn(ctx context.Context, p security.Principal, perm security.Permission, projectID string) (bool, error) {
	r.add("allowed " + perm.Name() + " " + projectID)
	return r.Authorizer.AllowedOn(ctx, p, perm, projectID)
}

// Allowed implements the services' Authorizer. It records the root
// resource as "", like Require.
func (r *Recorder) Allowed(ctx context.Context, p security.Principal, perm security.Permission) (bool, error) {
	r.add("allowed " + perm.Name() + " ")
	return r.Authorizer.Allowed(ctx, p, perm)
}

func (r *Recorder) add(check string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.checks = append(r.checks, check)
}

// Checks returns what was checked since the last Reset.
func (r *Recorder) Checks() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.checks...)
}

// Reset forgets the checks so far.
func (r *Recorder) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.checks = nil
}
