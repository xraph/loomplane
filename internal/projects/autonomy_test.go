package projects

import (
	"context"
	"errors"
	"testing"

	"github.com/xraph/loomplane/internal/security"
)

// countingAuthz counts the checks it's asked for and passes them on.
type countingAuthz struct {
	next  Authorizer
	calls int
}

func (c *countingAuthz) Require(ctx context.Context, p security.Principal, perm security.Permission, resourceID string) error {
	c.calls++
	return c.next.Require(ctx, p, perm, resourceID)
}

func ptr[T any](v T) *T { return &v }

func TestNewProjectsAskFirst(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	dev := f.principal(t, "ausr_dev", security.RoleDeveloper)
	p, err := f.svc.Create(ctx, dev, CreateProjectInput{Name: "Engine"})
	if err != nil {
		t.Fatal(err)
	}
	if p.AutonomousPromotion || p.MinStagingSoakSeconds != 0 {
		t.Fatalf("a new project: autonomous %v, soak %d", p.AutonomousPromotion, p.MinStagingSoakSeconds)
	}
	got, err := f.svc.Get(ctx, dev, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.AutonomousPromotion || got.MinStagingSoakSeconds != 0 {
		t.Fatalf("a new project read back: autonomous %v, soak %d", got.AutonomousPromotion, got.MinStagingSoakSeconds)
	}
}

func TestSetAutonomyNeedsApprove(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	dev := f.principal(t, "ausr_dev", security.RoleDeveloper)
	admin := f.principal(t, "ausr_admin", security.RoleAdmin)
	p, err := f.svc.Create(ctx, dev, CreateProjectInput{Name: "Engine"})
	if err != nil {
		t.Fatal(err)
	}
	f.audit.actions, f.audit.events = nil, nil

	// A developer can change the project but not let the agent promote it.
	if _, err = f.svc.SetAutonomy(ctx, dev, p.ID, AutonomyInput{AutonomousPromotion: ptr(true)}); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("developer: %v", err)
	}
	if len(f.audit.events) != 0 {
		t.Fatalf("a refused change was audited: %v", f.audit.actions)
	}

	got, err := f.svc.SetAutonomy(ctx, admin, p.ID, AutonomyInput{AutonomousPromotion: ptr(true), MinStagingSoakSeconds: ptr(int64(1800))})
	if err != nil {
		t.Fatalf("admin: %v", err)
	}
	if !got.AutonomousPromotion || got.MinStagingSoakSeconds != 1800 {
		t.Fatalf("saved %+v", got)
	}
	read, err := f.svc.Get(ctx, dev, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !read.AutonomousPromotion || read.MinStagingSoakSeconds != 1800 {
		t.Fatalf("read back %+v", read)
	}
	if len(f.audit.events) != 1 {
		t.Fatalf("audit %v", f.audit.actions)
	}
	e := f.audit.events[0]
	if e.Action != "project.autonomy.update" || e.ProjectID != p.ID || e.Principal.UserID != admin.UserID {
		t.Fatalf("audit event %+v", e)
	}
	if e.Attributes["autonomous_promotion"] != true || e.Attributes["min_staging_soak_seconds"] != int64(1800) {
		t.Fatalf("audit attributes %v", e.Attributes)
	}

	// A field left out keeps its value.
	got, err = f.svc.SetAutonomy(ctx, admin, p.ID, AutonomyInput{AutonomousPromotion: ptr(false)})
	if err != nil {
		t.Fatal(err)
	}
	if got.AutonomousPromotion || got.MinStagingSoakSeconds != 1800 {
		t.Fatalf("after turning it off: %+v", got)
	}

	// Another organization's project reads as not found.
	other := security.Principal{UserID: "ausr_other", OrgID: "aorg_other"}
	if err = f.authz.Assign(ctx, other.OrgID, other.UserID, security.RoleAdmin, "test"); err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.SetAutonomy(ctx, other, p.ID, AutonomyInput{AutonomousPromotion: ptr(true)}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another organization: %v", err)
	}
}

func TestSetAutonomyRefusesTheAgent(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	admin := f.principal(t, "ausr_admin", security.RoleAdmin)
	p, err := f.svc.Create(ctx, admin, CreateProjectInput{Name: "Engine"})
	if err != nil {
		t.Fatal(err)
	}
	counting := &countingAuthz{next: f.authz}
	svc := NewService(f.store, counting, f.init, f.audit)
	f.audit.actions, f.audit.events = nil, nil

	// The model can never turn autonomy on: not alone, not for an admin,
	// and Loomplane's own automation can't either.
	for name, who := range map[string]security.Principal{
		"agent for an admin": security.AgentPrincipal(org, &admin),
		"agent alone":        security.AgentPrincipal(org, nil),
		"automation":         security.SystemPrincipal(org),
	} {
		if _, serr := svc.SetAutonomy(ctx, who, p.ID, AutonomyInput{AutonomousPromotion: ptr(true)}); !errors.Is(serr, security.ErrForbidden) {
			t.Fatalf("%s: %v", name, serr)
		}
	}
	if counting.calls != 0 {
		t.Fatalf("the refusal asked Warden %d times", counting.calls)
	}
	if len(f.audit.events) != 0 {
		t.Fatalf("a refused change was audited: %v", f.audit.actions)
	}
	got, err := f.svc.Get(ctx, admin, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.AutonomousPromotion {
		t.Fatal("the agent turned autonomy on")
	}
}

func TestSetAutonomyValidatesSoak(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	admin := f.principal(t, "ausr_admin", security.RoleAdmin)
	p, err := f.svc.Create(ctx, admin, CreateProjectInput{Name: "Engine"})
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []int64{90, -60, 604860, 1} {
		if _, err := f.svc.SetAutonomy(ctx, admin, p.ID, AutonomyInput{MinStagingSoakSeconds: ptr(bad)}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("soak %d: %v", bad, err)
		}
	}
	if _, err := f.svc.SetAutonomy(ctx, admin, p.ID, AutonomyInput{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an empty change: %v", err)
	}
	for _, good := range []int64{1800, 0, 604800} {
		got, err := f.svc.SetAutonomy(ctx, admin, p.ID, AutonomyInput{MinStagingSoakSeconds: ptr(good)})
		if err != nil {
			t.Fatalf("soak %d: %v", good, err)
		}
		if got.MinStagingSoakSeconds != good {
			t.Fatalf("soak %d saved as %d", good, got.MinStagingSoakSeconds)
		}
	}
}

func TestUpdateLeavesAutonomyAlone(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	admin := f.principal(t, "ausr_admin", security.RoleAdmin)
	p, err := f.svc.Create(ctx, admin, CreateProjectInput{Name: "Engine"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.SetAutonomy(ctx, admin, p.ID, AutonomyInput{AutonomousPromotion: ptr(true), MinStagingSoakSeconds: ptr(int64(600))}); err != nil {
		t.Fatal(err)
	}
	got, err := f.svc.Update(ctx, admin, p.ID, UpdateProjectInput{Name: ptr("Analytical Engine")})
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "Analytical Engine" || !got.AutonomousPromotion || got.MinStagingSoakSeconds != 600 {
		t.Fatalf("after Update: %+v", got)
	}
	// The store's update writes only what Update may change, so a stale
	// copy of the project can't switch autonomy back off.
	stale := *got
	stale.AutonomousPromotion, stale.MinStagingSoakSeconds = false, 0
	if err = f.store.UpdateProject(ctx, &stale); err != nil {
		t.Fatal(err)
	}
	read, err := f.svc.Get(ctx, admin, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !read.AutonomousPromotion || read.MinStagingSoakSeconds != 600 {
		t.Fatalf("a stale UpdateProject changed autonomy: %+v", read)
	}
}
