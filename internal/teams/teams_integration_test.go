//go:build integration

package teams

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/driver"
	"github.com/xraph/grove/drivers/pgdriver"

	"github.com/xraph/loomplane/database/migrations"
	"github.com/xraph/loomplane/internal/ops"
	"github.com/xraph/loomplane/internal/security"
)

const (
	org      = "o"
	otherOrg = "x"
	ada      = "ausr_ada"   // owner of o, and of x
	bo       = "ausr_bo"    // admin of o
	vi       = "ausr_vi"    // viewer of o
	grace    = "ausr_grace" // viewer of o, member of x
	stranger = "ausr_stranger"
)

// teamsDB is a fresh database migrated with every group Loomplane runs,
// seeded with org o (Ada owner, Bo admin, Vi and Grace viewers), org x
// (Ada owner, Grace member), projects proj_a and proj_b in o and proj_x in
// x. It returns the database's URL and a connection pool.
func teamsDB(t *testing.T) (string, *pgdriver.PgDB) {
	t.Helper()
	dsn := os.Getenv("LOOMPLANE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set LOOMPLANE_TEST_DATABASE_URL for the teams tests")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("teams_%d", time.Now().UnixNano())
	if _, err = admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		_ = admin.Close(ctx)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(ctx, "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)")
		_ = admin.Close(ctx)
	})
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	if err = ops.Migrate(ctx, u.String(), migrations.All()); err != nil {
		t.Fatal(err)
	}
	pg := openPool(t, u.String())
	for _, stmt := range []string{
		`INSERT INTO authsome_apps(id,name,slug) VALUES('app','App','app')`,
		`INSERT INTO authsome_environments(id,app_id,name,slug,type,is_default) VALUES('env','app','Production','production','production',true)`,
		`INSERT INTO authsome_users(id,app_id,env_id,email,first_name,last_name) VALUES
			('ausr_ada','app','env','ada@example.com','Ada','Lovelace'),
			('ausr_bo','app','env','bo@example.com','Bo',''),
			('ausr_vi','app','env','vi@example.com','Vi','Hart'),
			('ausr_grace','app','env','grace@example.com','Grace','Hopper'),
			('ausr_stranger','app','env','stan@example.com','Stan','')`,
		`INSERT INTO authsome_organizations(id,app_id,env_id,name,slug,created_by) VALUES('o','app','env','O','o','ausr_ada'),('x','app','env','X','x','ausr_ada')`,
		`INSERT INTO authsome_members(id,org_id,user_id,role) VALUES
			('amem_ada','o','ausr_ada','owner'),('amem_bo','o','ausr_bo','admin'),
			('amem_vi','o','ausr_vi','viewer'),('amem_grace','o','ausr_grace','viewer'),
			('amem_xada','x','ausr_ada','owner'),('amem_xgrace','x','ausr_grace','member')`,
		`INSERT INTO loomplane_projects(id,org_id,name,slug,status,created_by,created_at,updated_at) VALUES
			('proj_a','o','A','a','ready','ausr_ada',now(),now()),
			('proj_b','o','B','b','ready','ausr_ada',now(),now()),
			('proj_x','x','X','x','ready','ausr_ada',now(),now())`,
	} {
		if _, err = pg.Exec(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	return u.String(), pg
}

func openPool(t *testing.T, dsn string, opts ...driver.Option) *pgdriver.PgDB {
	t.Helper()
	pg := pgdriver.New()
	if err := pg.Open(context.Background(), dsn, opts...); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pg.Close() })
	return pg
}

// fakeAuthz allows member:read for everyone and member:manage for the
// managers, and records every Sync and TrySync. When probe is set, every
// call first checks that a connection of that pool is free: the service
// must never call it while holding one.
type fakeAuthz struct {
	mu        sync.Mutex
	managers  map[string]bool
	syncs     []string // "org/user"
	trySyncs  []string
	failSyncs int             // fail the next n syncs
	failUsers map[string]bool // every sync of these users fails
	busy      map[string]bool // TrySync reports these users busy
	onSync    func(ctx context.Context, orgID, userID string)
	probe     *pgdriver.PgDB
	held      []string // calls made while the service held a connection
}

func (f *fakeAuthz) checkFree(ctx context.Context, call string) {
	if f.probe == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var one int
	if err := f.probe.QueryRow(ctx, `SELECT 1`).Scan(&one); err != nil {
		f.mu.Lock()
		f.held = append(f.held, call)
		f.mu.Unlock()
	}
}

func (f *fakeAuthz) Require(ctx context.Context, p security.Principal, perm security.Permission, _ string) error {
	f.checkFree(ctx, "Require")
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case perm == security.MemberRead:
		return nil
	case perm == security.MemberManage && f.managers[p.UserID]:
		return nil
	}
	return fmt.Errorf("%w (%s)", security.ErrForbidden, perm.Name())
}

func (f *fakeAuthz) record(ctx context.Context, list *[]string, orgID, userID string) error {
	f.mu.Lock()
	*list = append(*list, orgID+"/"+userID)
	fail := f.failSyncs > 0 || f.failUsers[userID]
	if f.failSyncs > 0 {
		f.failSyncs--
	}
	onSync := f.onSync
	f.mu.Unlock()
	if fail {
		return errors.New("warden is down")
	}
	if onSync != nil {
		onSync(ctx, orgID, userID)
	}
	return nil
}

func (f *fakeAuthz) Sync(ctx context.Context, orgID, userID string) error {
	f.checkFree(ctx, "Sync")
	return f.record(ctx, &f.syncs, orgID, userID)
}

func (f *fakeAuthz) TrySync(ctx context.Context, orgID, userID string) (bool, error) {
	f.checkFree(ctx, "TrySync")
	f.mu.Lock()
	busy := f.busy[userID]
	f.mu.Unlock()
	if busy {
		f.mu.Lock()
		f.trySyncs = append(f.trySyncs, orgID+"/"+userID+" busy")
		f.mu.Unlock()
		return false, nil
	}
	return true, f.record(ctx, &f.trySyncs, orgID, userID)
}

// synced returns and clears the recorded Syncs, sorted.
func (f *fakeAuthz) synced() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := slices.Sorted(slices.Values(f.syncs))
	f.syncs = nil
	return out
}

func (f *fakeAuthz) trySynced() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.trySyncs
	f.trySyncs = nil
	return out
}

type fakeAudit struct {
	mu     sync.Mutex
	events []AuditEvent
}

func (a *fakeAudit) Record(_ context.Context, e AuditEvent) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.events = append(a.events, e)
}

func (a *fakeAudit) last() AuditEvent {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.events) == 0 {
		return AuditEvent{}
	}
	return a.events[len(a.events)-1]
}

// rig is a Service over a teams database with fakes around it.
type rig struct {
	t           *testing.T
	url         string
	db          *pgdriver.PgDB
	authz       *fakeAuthz
	audit       *fakeAudit
	svc         *Service
	memberCalls atomic.Int32
	roles       sync.Map // user ID -> member role that overrides authsome's
	mu          sync.Mutex
	logs        []string
}

func newRig(t *testing.T) *rig {
	t.Helper()
	dsn, pg := teamsDB(t)
	return newRigOn(t, dsn, pg, pg)
}

// newRigOn builds the service on pg, and reads memberships through
// membersDB.
func newRigOn(t *testing.T, dsn string, pg, membersDB *pgdriver.PgDB) *rig {
	t.Helper()
	r := &rig{
		t: t, url: dsn, db: pg,
		authz: &fakeAuthz{managers: map[string]bool{ada: true, bo: true}, busy: map[string]bool{}, failUsers: map[string]bool{}},
		audit: &fakeAudit{},
	}
	members := func(ctx context.Context, orgID, userID string) (string, bool, error) {
		r.memberCalls.Add(1)
		var role string
		err := membersDB.QueryRow(ctx, `SELECT role FROM authsome_members WHERE org_id=$1 AND user_id=$2`, orgID, userID).Scan(&role)
		if errors.Is(err, pgx.ErrNoRows) {
			return "", false, nil
		}
		if err != nil {
			return "", false, err
		}
		if v, ok := r.roles.Load(userID); ok {
			role, _ = v.(string)
		}
		return role, true, nil
	}
	r.svc = NewService(Deps{
		DB: pg, Authz: r.authz, Members: members, Audit: r.audit,
		Log: func(msg string, err error) {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.logs = append(r.logs, msg+": "+err.Error())
		},
	})
	return r
}

func as(userID string) security.Principal { return security.Principal{UserID: userID, OrgID: org} }

func (r *rig) exec(sql string, args ...any) {
	r.t.Helper()
	if _, err := r.db.Exec(context.Background(), sql, args...); err != nil {
		r.t.Fatalf("%s: %v", sql, err)
	}
}

func (r *rig) strs(sql string, args ...any) []string {
	r.t.Helper()
	rows, err := r.db.Query(context.Background(), sql, args...)
	if err != nil {
		r.t.Fatalf("%s: %v", sql, err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			r.t.Fatal(err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		r.t.Fatal(err)
	}
	return out
}

// queue is every loomplane_authz_sync row as "org/user", sorted.
func (r *rig) queue() []string {
	r.t.Helper()
	return r.strs(`SELECT org_id||'/'||user_id FROM loomplane_authz_sync ORDER BY 1`)
}

func (r *rig) team(name string, members ...string) *Team {
	r.t.Helper()
	ctx := context.Background()
	team, err := r.svc.Create(ctx, as(ada), CreateTeamInput{Name: name})
	if err != nil {
		r.t.Fatal(err)
	}
	for _, m := range members {
		if team, err = r.svc.AddMember(ctx, as(ada), team.ID, m); err != nil {
			r.t.Fatal(err)
		}
	}
	r.authz.synced()
	return team
}

func (r *rig) grant(teamID, role, projectID string) *Team {
	r.t.Helper()
	team, err := r.svc.SetGrant(context.Background(), as(ada), teamID, SetGrantInput{Role: role, ProjectID: projectID})
	if err != nil {
		r.t.Fatal(err)
	}
	r.authz.synced()
	return team
}

func memberIDs(team *Team) []string {
	out := make([]string, 0, len(team.Members))
	for _, m := range team.Members {
		out = append(out, m.UserID)
	}
	return out
}

func TestCreateListAndUpdateTeams(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)

	team, err := r.svc.Create(ctx, as(ada), CreateTeamInput{Name: "  Payments ", Description: " Takes the money "})
	if err != nil {
		t.Fatal(err)
	}
	if team.Slug != "payments" || team.Name != "Payments" || team.Description != "Takes the money" ||
		!strings.HasPrefix(team.ID, "team_") || team.OrgID != org {
		t.Fatalf("created %+v", team)
	}
	if team.Members == nil || team.Grants == nil || len(team.Members)+len(team.Grants) != 0 {
		t.Fatalf("a new team's members %v and grants %v, want empty lists", team.Members, team.Grants)
	}
	if e := r.audit.last(); e.Action != "team.create" || e.TeamID != team.ID || e.Principal.UserID != ada {
		t.Fatalf("audit %+v", e)
	}

	for _, in := range []CreateTeamInput{
		{Name: "  "},
		{Name: "!!!"}, // no slug
		{Name: strings.Repeat("é", 81)},
		{Name: "Long", Description: strings.Repeat("é", 501)},
	} {
		if _, createErr := r.svc.Create(ctx, as(ada), in); !errors.Is(createErr, ErrInvalid) {
			t.Errorf("Create(%.20q) = %v, want ErrInvalid", in.Name, createErr)
		}
	}
	if _, err = r.svc.Create(ctx, as(ada), CreateTeamInput{Name: strings.Repeat("é", 79) + "x"}); err != nil {
		t.Fatalf("80 runes and a slug: %v", err)
	}
	if _, err = r.svc.Create(ctx, as(bo), CreateTeamInput{Name: "payments"}); !errors.Is(err, ErrSlugTaken) {
		t.Fatalf("second Payments = %v, want ErrSlugTaken", err)
	}
	if _, err = r.svc.Create(ctx, as(vi), CreateTeamInput{Name: "Mine"}); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("Vi's Create = %v, want ErrForbidden", err)
	}

	name := "Billing"
	updated, err := r.svc.Update(ctx, as(bo), team.ID, UpdateTeamInput{Name: &name})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Slug != "billing" || updated.Name != "Billing" || updated.Description != "Takes the money" ||
		updated.UpdatedAt.Before(team.UpdatedAt) {
		t.Fatalf("updated %+v", updated)
	}
	if e := r.audit.last(); e.Action != "team.update" || e.TeamID != team.ID {
		t.Fatalf("audit %+v", e)
	}
	desc := "  Sends invoices "
	if updated, err = r.svc.Update(ctx, as(bo), team.ID, UpdateTeamInput{Description: &desc}); err != nil ||
		updated.Name != "Billing" || updated.Description != "Sends invoices" {
		t.Fatalf("description update %+v, %v", updated, err)
	}
	ops, err := r.svc.Create(ctx, as(ada), CreateTeamInput{Name: "Ops"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.svc.Update(ctx, as(ada), ops.ID, UpdateTeamInput{Name: &name}); !errors.Is(err, ErrSlugTaken) {
		t.Fatalf("renaming Ops to Billing = %v, want ErrSlugTaken", err)
	}
	blank := " "
	if _, err = r.svc.Update(ctx, as(ada), ops.ID, UpdateTeamInput{Name: &blank}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("blank rename = %v, want ErrInvalid", err)
	}
	if _, err = r.svc.Update(ctx, as(vi), ops.ID, UpdateTeamInput{Name: &blank}); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("Vi's Update = %v, want ErrForbidden", err)
	}
	if _, err = r.svc.Update(ctx, as(ada), "team_nope", UpdateTeamInput{Name: &name}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Update of a missing team = %v, want ErrNotFound", err)
	}

	list, err := r.svc.List(ctx, as(vi))
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(list.Teams))
	for _, tm := range list.Teams {
		names = append(names, tm.Name)
	}
	if len(names) != 3 || names[0] != "Billing" || names[1] != "Ops" {
		t.Fatalf("Vi lists %v", names)
	}
	if got, err := r.svc.Get(ctx, as(vi), team.ID); err != nil || got.Name != "Billing" {
		t.Fatalf("Vi's Get = %+v, %v", got, err)
	}
	if _, err := r.svc.Get(ctx, as(vi), "team_nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get of a missing team = %v", err)
	}
	if got := r.authz.synced(); len(got) != 0 {
		t.Fatalf("team edits synced %v", got)
	}
}

func TestAddMemberRefusesAutomation(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	team := r.team("Payments")
	for _, id := range []string{"loomplane:o", "agent:o", "", security.SystemSubject(org), "ausr_a:b"} {
		if _, err := r.svc.AddMember(ctx, as(ada), team.ID, id); !errors.Is(err, ErrInvalid) {
			t.Errorf("AddMember(%q) = %v, want ErrInvalid", id, err)
		}
	}
	if n := r.memberCalls.Load(); n != 0 {
		t.Fatalf("refused IDs were looked up %d times", n)
	}
	if _, err := r.svc.AddMember(ctx, as(ada), team.ID, stranger); !errors.Is(err, ErrNotMember) {
		t.Fatalf("AddMember(stranger) = %v, want ErrNotMember", err)
	}
	if _, err := r.svc.AddMember(ctx, as(vi), team.ID, grace); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("Vi's AddMember = %v, want ErrForbidden", err)
	}
	if got := r.strs(`SELECT user_id FROM loomplane_team_members`); len(got) != 0 {
		t.Fatalf("membership rows %v", got)
	}
	if q, s := r.queue(), r.authz.synced(); len(q)+len(s) != 0 {
		t.Fatalf("refused adds queued %v and synced %v", q, s)
	}
}

func TestAddMemberQueuesAndSyncs(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	team := r.team("Payments")
	got, err := r.svc.AddMember(ctx, as(bo), team.ID, grace)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Members) != 1 || got.Members[0].UserID != grace || got.Members[0].Email != "grace@example.com" ||
		got.Members[0].Name != "Grace Hopper" || got.Members[0].AddedAt.IsZero() {
		t.Fatalf("members %+v", got.Members)
	}
	if s := r.authz.synced(); !slices.Equal(s, []string{org + "/" + grace}) {
		t.Fatalf("synced %v", s)
	}
	if q := r.queue(); len(q) != 0 {
		t.Fatalf("queue after a good sync %v", q)
	}
	if e := r.audit.last(); e.Action != "team.member.add" || e.Attributes["user_id"] != grace {
		t.Fatalf("audit %+v", e)
	}
	// The row names the authsome membership it was added for.
	if ids := r.strs(`SELECT member_id FROM loomplane_team_members WHERE user_id=$1`, grace); !slices.Equal(ids, []string{"amem_grace"}) {
		t.Fatalf("member_id %v", ids)
	}

	// Members are ordered by name, then email.
	if got, err = r.svc.AddMember(ctx, as(bo), team.ID, ada); err != nil {
		t.Fatal(err)
	}
	if ids := memberIDs(got); !slices.Equal(ids, []string{ada, grace}) {
		t.Fatalf("members %v, want Ada Lovelace then Grace Hopper", ids)
	}
	r.authz.synced()

	// Removing her queues and syncs her too.
	if got, err = r.svc.RemoveMember(ctx, as(bo), team.ID, grace); err != nil {
		t.Fatal(err)
	}
	if ids := memberIDs(got); !slices.Equal(ids, []string{ada}) {
		t.Fatalf("members after the removal %v", ids)
	}
	if s := r.authz.synced(); !slices.Equal(s, []string{org + "/" + grace}) {
		t.Fatalf("synced %v", s)
	}
	if e := r.audit.last(); e.Action != "team.member.remove" || e.Attributes["user_id"] != grace {
		t.Fatalf("audit %+v", e)
	}
	if _, err = r.svc.RemoveMember(ctx, as(vi), team.ID, ada); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("Vi's RemoveMember = %v, want ErrForbidden", err)
	}
	if q := r.queue(); len(q) != 0 {
		t.Fatalf("queue %v", q)
	}
}

func TestSetGrantRules(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	team := r.team("Payments", grace)

	for _, in := range []SetGrantInput{
		{Role: "owner"}, {Role: ""}, {Role: security.RoleSystem},
		{Role: "developer", ProjectID: "proj_x"}, // another workspace's project
		{Role: "developer", ProjectID: "proj_nope"},
	} {
		_, err := r.svc.SetGrant(ctx, as(ada), team.ID, in)
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("SetGrant(%+v) = %v, want ErrInvalid", in, err)
		}
		if in.ProjectID != "" && !strings.Contains(fmt.Sprint(err), "no such project in this workspace") {
			t.Errorf("SetGrant(%+v) = %v, want it to name the project", in, err)
		}
	}

	got, err := r.svc.SetGrant(ctx, as(ada), team.ID, SetGrantInput{Role: "developer", ProjectID: "proj_a"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Grants) != 1 || got.Grants[0].Role != "developer" || got.Grants[0].ProjectID != "proj_a" ||
		!strings.HasPrefix(got.Grants[0].ID, "tgrant_") {
		t.Fatalf("grants %+v", got.Grants)
	}
	first := got.Grants[0].ID
	if s := r.authz.synced(); !slices.Equal(s, []string{org + "/" + grace}) {
		t.Fatalf("synced %v", s)
	}
	if got, err = r.svc.SetGrant(ctx, as(bo), team.ID, SetGrantInput{Role: "admin", ProjectID: "proj_a"}); err != nil {
		t.Fatal(err)
	}
	if len(got.Grants) != 1 || got.Grants[0].ID != first || got.Grants[0].Role != "admin" {
		t.Fatalf("grants after the role change %+v, want one admin grant with ID %s", got.Grants, first)
	}
	if e := r.audit.last(); e.Action != "team.grant.set" || e.Attributes["grant_id"] != first ||
		e.Attributes["role"] != "admin" || e.Attributes["project_id"] != "proj_a" {
		t.Fatalf("audit %+v", e)
	}
	if got, err = r.svc.SetGrant(ctx, as(ada), team.ID, SetGrantInput{Role: "viewer"}); err != nil {
		t.Fatal(err)
	}
	if len(got.Grants) != 2 || got.Grants[0].ProjectID != "" || got.Grants[1].ProjectID != "proj_a" {
		t.Fatalf("grants %+v, want the workspace first", got.Grants)
	}

	// A member:manage holder whose own member role is viewer can't grant
	// above viewer.
	r.authz.managers[vi] = true
	_, err = r.svc.SetGrant(ctx, as(vi), team.ID, SetGrantInput{Role: "developer", ProjectID: "proj_b"})
	if !errors.Is(err, security.ErrForbidden) || !strings.Contains(err.Error(), "above your own") {
		t.Fatalf("Vi granting developer = %v, want ErrForbidden above your own", err)
	}
	// An admin can't grant above admin either, through a role override.
	r.roles.Store(bo, "member")
	if _, err = r.svc.SetGrant(ctx, as(bo), team.ID, SetGrantInput{Role: "admin"}); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("a developer granting admin = %v, want ErrForbidden", err)
	}
	if _, err = r.svc.SetGrant(ctx, as(grace), team.ID, SetGrantInput{Role: "viewer"}); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("Grace's SetGrant = %v, want ErrForbidden", err)
	}
	if _, err = r.svc.SetGrant(ctx, as(ada), "team_nope", SetGrantInput{Role: "viewer"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SetGrant on a missing team = %v, want ErrNotFound", err)
	}
	if n := len(r.strs(`SELECT id FROM loomplane_team_grants`)); n != 2 {
		t.Fatalf("%d grant rows, want 2", n)
	}
}

func TestRemoveGrantQueuesAndSyncsMembers(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	team := r.team("Payments", grace, vi)
	r.grant(team.ID, "developer", "proj_a")
	team = r.grant(team.ID, "viewer", "")
	keep, drop := team.Grants[0], team.Grants[1]

	got, err := r.svc.RemoveGrant(ctx, as(bo), team.ID, drop.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Grants) != 1 || got.Grants[0] != keep {
		t.Fatalf("grants %+v, want only %+v", got.Grants, keep)
	}
	if s := r.authz.synced(); !slices.Equal(s, []string{org + "/" + grace, org + "/" + vi}) {
		t.Fatalf("synced %v", s)
	}
	if e := r.audit.last(); e.Action != "team.grant.remove" || e.Attributes["grant_id"] != drop.ID {
		t.Fatalf("audit %+v", e)
	}
	if q := r.queue(); len(q) != 0 {
		t.Fatalf("queue %v", q)
	}
	if _, err = r.svc.RemoveGrant(ctx, as(bo), team.ID, drop.ID); !errors.Is(err, ErrGrantNotFound) {
		t.Fatalf("removing it again = %v, want ErrGrantNotFound", err)
	}
	other := r.team("Ops")
	if _, err = r.svc.RemoveGrant(ctx, as(bo), other.ID, keep.ID); !errors.Is(err, ErrGrantNotFound) {
		t.Fatalf("removing another team's grant = %v, want ErrGrantNotFound", err)
	}
	if _, err = r.svc.RemoveGrant(ctx, as(vi), team.ID, keep.ID); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("Vi's RemoveGrant = %v, want ErrForbidden", err)
	}
	if s := r.authz.synced(); len(s) != 0 {
		t.Fatalf("failed removals synced %v", s)
	}
}

func TestDeleteTeamQueuesMembersAndClearsProjects(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	team := r.team("Payments", grace, vi)
	r.grant(team.ID, "developer", "proj_a")
	r.exec(`UPDATE loomplane_projects SET responsible_team_id=$1 WHERE id='proj_a'`, team.ID)

	if err := r.svc.Delete(ctx, as(vi), team.ID); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("Vi's Delete = %v, want ErrForbidden", err)
	}
	if err := r.svc.Delete(ctx, as(ada), team.ID); err != nil {
		t.Fatal(err)
	}
	if s := r.authz.synced(); !slices.Equal(s, []string{org + "/" + grace, org + "/" + vi}) {
		t.Fatalf("synced %v", s)
	}
	if _, err := r.svc.Get(ctx, as(ada), team.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after Delete = %v", err)
	}
	if got := r.strs(`SELECT coalesce(responsible_team_id,'none') FROM loomplane_projects WHERE id='proj_a'`); !slices.Equal(got, []string{"none"}) {
		t.Fatalf("proj_a's responsible team %v", got)
	}
	if e := r.audit.last(); e.Action != "team.delete" || e.TeamID != team.ID {
		t.Fatalf("audit %+v", e)
	}
	if q := r.queue(); len(q) != 0 {
		t.Fatalf("queue %v", q)
	}
	if err := r.svc.Delete(ctx, as(ada), team.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second Delete = %v, want ErrNotFound", err)
	}
}

// A membership row counts only for the authsome membership it was added
// for. Matching on the member ID, not on timestamps, means a leftover row
// stays dead after a re-invite whatever the clocks say.
func TestGrantsIgnoreLeftoverMemberships(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	team := r.team("Payments", grace)
	r.grant(team.ID, "admin", "")
	want := []security.ScopedRole{{Role: "admin", ProjectID: "", TeamID: team.ID}}
	grants := func() []security.ScopedRole {
		t.Helper()
		got, err := r.svc.Grants(ctx, org, grace)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	if got := grants(); !slices.Equal(got, want) {
		t.Fatalf("Grants %v, want %v", got, want)
	}
	listed := func() []string {
		t.Helper()
		got, err := r.svc.Get(ctx, as(ada), team.ID)
		if err != nil {
			t.Fatal(err)
		}
		return memberIDs(got)
	}

	r.exec(`DELETE FROM authsome_members WHERE id='amem_grace'`)
	if got := grants(); len(got) != 0 {
		t.Fatalf("Grants after she left %v", got)
	}
	if got := listed(); len(got) != 0 {
		t.Fatalf("listed members after she left %v", got)
	}

	// Invited back: a new member row. Its created_at is dated before the
	// team row was added, which a timestamp guard would accept.
	r.exec(`INSERT INTO authsome_members(id,org_id,user_id,role,created_at) VALUES('amem_grace2','o','ausr_grace','viewer','2000-01-01')`)
	if got := grants(); len(got) != 0 {
		t.Fatalf("Grants after the re-invite %v", got)
	}
	if got := listed(); len(got) != 0 {
		t.Fatalf("listed members after the re-invite %v", got)
	}

	if _, err := r.svc.AddMember(ctx, as(ada), team.ID, grace); err != nil {
		t.Fatal(err)
	}
	if got := grants(); !slices.Equal(got, want) {
		t.Fatalf("Grants after adding her again %v, want %v", got, want)
	}
	if ids := r.strs(`SELECT member_id FROM loomplane_team_members WHERE user_id=$1`, grace); !slices.Equal(ids, []string{"amem_grace2"}) {
		t.Fatalf("member_id %v after adding her again", ids)
	}
}

// Teams, their grants and the projects they name stay in one workspace:
// another workspace's team is never found, and a grant row that points
// across workspaces (the schema can't stop one) grants nothing.
func TestTeamsStayInTheirWorkspace(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	ours := r.team("Payments", grace)
	theirs, err := r.svc.Create(ctx, security.Principal{UserID: ada, OrgID: otherOrg}, CreateTeamInput{Name: "Payments"})
	if err != nil {
		t.Fatalf("the same name in another workspace: %v", err)
	}
	name := "Mine"
	for what, call := range map[string]func() error{
		"Get": func() error { _, err := r.svc.Get(ctx, as(ada), theirs.ID); return err },
		"Update": func() error {
			_, err := r.svc.Update(ctx, as(ada), theirs.ID, UpdateTeamInput{Name: &name})
			return err
		},
		"AddMember": func() error { _, err := r.svc.AddMember(ctx, as(ada), theirs.ID, grace); return err },
		"Remove":    func() error { _, err := r.svc.RemoveMember(ctx, as(ada), theirs.ID, grace); return err },
		"SetGrant": func() error {
			_, err := r.svc.SetGrant(ctx, as(ada), theirs.ID, SetGrantInput{Role: "viewer"})
			return err
		},
		"Delete":     func() error { return r.svc.Delete(ctx, as(ada), theirs.ID) },
		"RemoveGrnt": func() error { _, err := r.svc.RemoveGrant(ctx, as(ada), theirs.ID, "tgrant_1"); return err },
	} {
		if err := call(); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s on another workspace's team = %v, want ErrNotFound", what, err)
		}
	}
	if list, err := r.svc.List(ctx, as(ada)); err != nil || len(list.Teams) != 1 || list.Teams[0].ID != ours.ID {
		t.Fatalf("List %+v, %v", list, err)
	}
	if _, err := r.svc.SetGrant(ctx, as(ada), ours.ID, SetGrantInput{Role: "viewer", ProjectID: "proj_x"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a grant on another workspace's project = %v, want ErrInvalid", err)
	}

	// Rows written around the service.
	r.exec(`INSERT INTO loomplane_team_grants(id,team_id,org_id,role,project_id,created_by) VALUES('tgrant_cross','` + ours.ID + `','o','admin','proj_x','ausr_ada')`)
	r.exec(`INSERT INTO loomplane_team_grants(id,team_id,org_id,role,project_id,created_by) VALUES('tgrant_wrongorg','` + ours.ID + `','x','admin',NULL,'ausr_ada')`)
	if got, err := r.svc.Grants(ctx, org, grace); err != nil || len(got) != 0 {
		t.Fatalf("Grants with cross-workspace rows = %v, %v; want none", got, err)
	}
	if got, err := r.svc.Get(ctx, as(ada), ours.ID); err != nil || len(got.Grants) != 0 {
		t.Fatalf("listed grants %+v, %v; want none", got, err)
	}
}

func TestFailedSyncStaysQueuedAndDrainConverges(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	team := r.team("Payments")
	r.authz.failSyncs = 1
	got, err := r.svc.AddMember(ctx, as(ada), team.ID, grace)
	if err != nil || got == nil || len(got.Members) != 1 {
		t.Fatalf("AddMember with a failing sync = %+v, %v; want the team", got, err)
	}
	if q := r.queue(); !slices.Equal(q, []string{org + "/" + grace}) {
		t.Fatalf("queue %v", q)
	}
	if len(r.logs) != 1 || !strings.Contains(r.logs[0], grace) || strings.Contains(r.logs[0], "@") {
		t.Fatalf("logs %v, want one line naming her ID and no email", r.logs)
	}
	if err = r.svc.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	if s := r.authz.trySynced(); !slices.Equal(s, []string{org + "/" + grace}) {
		t.Fatalf("Drain synced %v", s)
	}
	if q := r.queue(); len(q) != 0 {
		t.Fatalf("queue after Drain %v", q)
	}
}

func TestDrainKeepsARowQueuedAgainDuringSync(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	r.exec(`INSERT INTO loomplane_authz_sync(org_id,user_id) VALUES('o','ausr_grace')`)
	r.authz.onSync = func(_ context.Context, orgID, userID string) {
		// A second change queues her again while her sync runs.
		r.exec(`INSERT INTO loomplane_authz_sync(org_id,user_id) VALUES($1,$2) ON CONFLICT (org_id,user_id) DO UPDATE SET queued_at=clock_timestamp()`, orgID, userID)
	}
	if err := r.svc.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	if q := r.queue(); !slices.Equal(q, []string{org + "/" + grace}) {
		t.Fatalf("queue %v, want her row kept for the second change", q)
	}
	r.authz.onSync = nil
	if err := r.svc.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	if q := r.queue(); len(q) != 0 {
		t.Fatalf("queue after the second Drain %v", q)
	}
}

// The job never waits on a person's lock: a person whose sync is running
// elsewhere stays queued for the next run, and the rest go ahead.
func TestDrainSkipsABusyPerson(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	r.exec(`INSERT INTO loomplane_authz_sync(org_id,user_id,queued_at) VALUES('o','ausr_grace',now()-interval '2 minutes'),('o','ausr_vi',now()-interval '1 minute')`)
	r.authz.busy[grace] = true
	if err := SyncJob(r.svc).Handler(ctx, SyncPayload{}); err != nil {
		t.Fatal(err)
	}
	if s := r.authz.trySynced(); !slices.Equal(s, []string{org + "/" + grace + " busy", org + "/" + vi}) {
		t.Fatalf("Drain tried %v, want Grace (busy) then Vi", s)
	}
	if q := r.queue(); !slices.Equal(q, []string{org + "/" + grace}) {
		t.Fatalf("queue %v, want only the busy person", q)
	}
	if s := r.authz.synced(); len(s) != 0 {
		t.Fatalf("Drain used the waiting Sync for %v", s)
	}
	r.authz.busy[grace] = false
	if err := r.svc.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	if q := r.queue(); len(q) != 0 {
		t.Fatalf("queue %v", q)
	}
	if SyncJob(r.svc).Name != JobSync || JobSync != "teams.sync" {
		t.Fatalf("job name %q", SyncJob(r.svc).Name)
	}
}

// A row whose sync fails goes to the back of the queue, so a batch of rows
// that fail every time can't keep newer ones from ever being drained. A
// busy row keeps its place.
func TestDrainMovesAFailedRowToTheBack(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	r.svc.batch = 1
	r.exec(`INSERT INTO loomplane_authz_sync(org_id,user_id,queued_at) VALUES('o','ausr_grace',now()-interval '2 minutes'),('o','ausr_vi',now()-interval '1 minute')`)
	r.authz.failUsers[grace] = true
	if err := r.svc.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	if got := r.strs(`SELECT user_id FROM loomplane_authz_sync ORDER BY queued_at`); !slices.Equal(got, []string{vi, grace}) {
		t.Fatalf("queue order %v, want Vi then the failed Grace", got)
	}
	if err := r.svc.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	if q := r.queue(); !slices.Equal(q, []string{org + "/" + grace}) {
		t.Fatalf("queue %v, want Vi drained past the failing row", q)
	}

	// Busy keeps its place: it isn't failing, it's waiting its turn.
	r.authz.failUsers[grace] = false
	r.exec(`INSERT INTO loomplane_authz_sync(org_id,user_id) VALUES('o','ausr_vi')`)
	r.authz.busy[grace] = true
	if err := r.svc.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	if got := r.strs(`SELECT user_id FROM loomplane_authz_sync ORDER BY queued_at`); !slices.Equal(got, []string{grace, vi}) {
		t.Fatalf("queue order %v, want the busy Grace still first", got)
	}
}

// Requeue is the member removal's fallback when its last revoke fails: it
// queues the person without syncing, and the next Drain syncs them.
func TestRequeueQueuesWithoutSyncing(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	if err := r.svc.Requeue(ctx, org, grace); err != nil {
		t.Fatal(err)
	}
	if q, s := r.queue(), r.authz.synced(); !slices.Equal(q, []string{org + "/" + grace}) || len(s) != 0 {
		t.Fatalf("queue %v, synced %v", q, s)
	}
	if err := r.svc.Requeue(ctx, org, grace); err != nil {
		t.Fatalf("queueing twice: %v", err)
	}
	if err := r.svc.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	if s := r.authz.trySynced(); !slices.Equal(s, []string{org + "/" + grace}) {
		t.Fatalf("Drain synced %v", s)
	}
	if q := r.queue(); len(q) != 0 {
		t.Fatalf("queue %v", q)
	}
}

func TestDrainTakesAtMost500OldestFirst(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	r.exec(`INSERT INTO loomplane_authz_sync(org_id,user_id,queued_at)
		SELECT 'o', 'ausr_'||lpad(i::text,4,'0'), now() - (1000 - i) * interval '1 second' FROM generate_series(1,501) i`)
	if err := r.svc.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	if q := r.queue(); !slices.Equal(q, []string{"o/ausr_0501"}) {
		t.Fatalf("left %d rows, first %v; want only the newest", len(q), q[:min(len(q), 3)])
	}
}

func TestReapSyncsMembersOfADeletedProject(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	pay := r.team("Payments", grace)
	r.grant(pay.ID, "developer", "proj_a")
	ops := r.team("Ops", grace, vi)
	r.grant(ops.ID, "admin", "proj_a")
	other := r.team("Other", bo)
	r.grant(other.ID, "developer", "proj_b")

	if done := r.svc.Reap(ctx, org, "proj_a", "env_1"); done != nil {
		t.Fatal("an environment's deletion reaped teams")
	}
	if done := r.svc.Reap(ctx, org, "proj_none", ""); done != nil {
		t.Fatal("a project without grants returned work")
	}
	done := r.svc.Reap(ctx, org, "proj_a", "")
	if done == nil {
		t.Fatal("Reap returned nothing")
	}
	if q := r.queue(); !slices.Equal(q, []string{org + "/" + grace, org + "/" + vi}) {
		t.Fatalf("queued before the deletion %v", q)
	}
	if s := r.authz.synced(); len(s) != 0 {
		t.Fatalf("synced before the deletion %v", s)
	}
	r.exec(`DELETE FROM loomplane_projects WHERE id='proj_a'`)
	done(ctx)
	if s := r.authz.synced(); !slices.Equal(s, []string{org + "/" + grace, org + "/" + vi}) {
		t.Fatalf("synced after the deletion %v", s)
	}
	if q := r.queue(); len(q) != 0 {
		t.Fatalf("queue %v", q)
	}
}

func TestLeaveRemovesEveryMembershipInTheOrg(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	r.team("Payments", grace, vi)
	r.team("Ops", grace)
	theirs, err := r.svc.Create(ctx, security.Principal{UserID: ada, OrgID: otherOrg}, CreateTeamInput{Name: "Theirs"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.svc.AddMember(ctx, security.Principal{UserID: ada, OrgID: otherOrg}, theirs.ID, grace); err != nil {
		t.Fatal(err)
	}
	r.authz.synced()
	if err = r.svc.Leave(ctx, org, grace); err != nil {
		t.Fatal(err)
	}
	if got := r.strs(`SELECT org_id||'/'||user_id FROM loomplane_team_members ORDER BY 1`); !slices.Equal(got, []string{"o/" + vi, "x/" + grace}) {
		t.Fatalf("membership rows %v", got)
	}
	if q, s := r.queue(), r.authz.synced(); len(q)+len(s) != 0 {
		t.Fatalf("Leave queued %v and synced %v", q, s)
	}
}

// No team change calls the authorizer (Require, Sync, TrySync) or looks a
// membership up while it holds a connection. On a pool of one connection,
// any call made while the service held it would time out.
func TestNoAuthorizerCallHoldsAConnection(t *testing.T) {
	dsn, _ := teamsDB(t)
	one := openPool(t, dsn, driver.WithPoolSize(1))
	r := newRigOn(t, dsn, one, one)
	r.authz.probe = one
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	team, err := r.svc.Create(ctx, as(ada), CreateTeamInput{Name: "Payments"})
	if err != nil {
		t.Fatal(err)
	}
	steps := []func() error{
		func() error { _, err := r.svc.AddMember(ctx, as(ada), team.ID, grace); return err },
		func() error { _, err := r.svc.AddMember(ctx, as(ada), team.ID, vi); return err },
		func() error {
			_, err := r.svc.SetGrant(ctx, as(bo), team.ID, SetGrantInput{Role: "developer", ProjectID: "proj_a"})
			return err
		},
		func() error {
			_, err := r.svc.SetGrant(ctx, as(bo), team.ID, SetGrantInput{Role: "viewer"})
			return err
		},
		func() error {
			got, err := r.svc.Get(ctx, as(vi), team.ID)
			if err != nil {
				return err
			}
			_, err = r.svc.RemoveGrant(ctx, as(bo), team.ID, got.Grants[0].ID)
			return err
		},
		func() error { _, err := r.svc.RemoveMember(ctx, as(bo), team.ID, vi); return err },
		func() error { _, err := r.svc.List(ctx, as(vi)); return err },
		func() error {
			if done := r.svc.Reap(ctx, org, "proj_a", ""); done != nil {
				done(ctx)
			}
			return nil
		},
		func() error {
			if _, err := one.Exec(ctx, `INSERT INTO loomplane_authz_sync(org_id,user_id) VALUES('o','ausr_bo')`); err != nil {
				return err
			}
			return r.svc.Drain(ctx)
		},
		func() error { _, err := r.svc.Grants(ctx, org, grace); return err },
		func() error { return r.svc.Delete(ctx, as(ada), team.ID) },
	}
	for i, step := range steps {
		if err := step(); err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
	}
	if len(r.authz.held) != 0 {
		t.Fatalf("called the authorizer while holding the connection: %v", r.authz.held)
	}
	if len(r.logs) != 0 {
		t.Fatalf("logs %v", r.logs)
	}
	if q := r.queue(); len(q) != 0 {
		t.Fatalf("queue %v", q)
	}
}
