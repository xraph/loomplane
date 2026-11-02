//go:build integration

package integration

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/dispatch/engine"
	"github.com/xraph/dispatch/job"
	"github.com/xraph/forge"

	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/orchestration"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/teams"
)

// teamsWorld is a workspace, "engines", made by Ada (its owner), with
// Grace and Vi in it as viewers. AI is off: teams don't depend on it.
type teamsWorld struct {
	t       *testing.T
	r       *running
	app     forge.App
	sql     *pgx.Conn
	key     string
	org     string
	ada     *client
	grace   *client
	vi      *client
	adaID   string
	graceID string
	viID    string
}

func newTeamsWorld(t *testing.T) *teamsWorld {
	t.Helper()
	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	cfg.Orchestration.PollInterval = 100 * time.Millisecond
	cfg.AI.Enabled = false
	w := &teamsWorld{t: t, key: cfg.Identity.PublishableKey}
	w.r = startApp(t, cfg, func(a forge.App) { w.app = a })
	t.Cleanup(w.r.stop)
	conn, err := pgx.Connect(t.Context(), cfg.Database.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(t.Context()) })
	w.sql = conn

	w.ada = w.signUp("ada@example.com")
	var org struct {
		ID string `json:"id"`
	}
	w.ada.must(http.StatusCreated, http.MethodPost, "/authsome/v1/orgs", map[string]string{"name": "Engines", "slug": "engines"}, &org)
	w.ada.must(http.StatusOK, http.MethodPost, "/authsome/v1/me/switch-org", map[string]string{"org_id": org.ID}, nil)
	w.org = org.ID
	w.grace = w.signUp("grace@example.com")
	w.join(w.grace, "grace@example.com")
	w.vi = w.signUp("vi@example.com")
	w.join(w.vi, "vi@example.com")
	w.adaID, w.graceID, w.viID = w.userID("ada@example.com"), w.userID("grace@example.com"), w.userID("vi@example.com")
	return w
}

func (w *teamsWorld) signUp(email string) *client {
	c := &client{t: w.t, base: w.r.srv.URL, key: w.key}
	account(w.t, c, email)
	return c
}

// join invites the address as a viewer and accepts as c.
func (w *teamsWorld) join(c *client, email string) {
	w.t.Helper()
	var created struct {
		AcceptPath string `json:"accept_path"`
	}
	w.ada.must(http.StatusCreated, http.MethodPost, "/api/v1/invitations", map[string]string{"email": email, "role": "viewer"}, &created)
	u, err := url.Parse(created.AcceptPath)
	if err != nil {
		w.t.Fatal(err)
	}
	if code := c.do(http.MethodPost, "/authsome/v1/orgs/invitations/accept", map[string]string{"token": u.Query().Get("token")}, nil); code != http.StatusOK && code != http.StatusCreated {
		w.t.Fatalf("%s accepts: status %d", email, code)
	}
	c.must(http.StatusOK, http.MethodPost, "/authsome/v1/me/switch-org", map[string]string{"org_id": w.org}, nil)
}

type teamsMember struct {
	ID     string `json:"id"`
	UserID string `json:"user_id"`
	Email  string `json:"email"`
}

func (w *teamsWorld) members() []teamsMember {
	w.t.Helper()
	var list struct {
		Members []teamsMember `json:"members"`
	}
	w.ada.must(http.StatusOK, http.MethodGet, "/api/v1/members", nil, &list)
	return list.Members
}

func (w *teamsWorld) userID(email string) string {
	w.t.Helper()
	for _, m := range w.members() {
		if m.Email == email {
			return m.UserID
		}
	}
	w.t.Fatalf("%s isn't a member", email)
	return ""
}

func (w *teamsWorld) memberID(email string) string {
	w.t.Helper()
	for _, m := range w.members() {
		if m.Email == email {
			return m.ID
		}
	}
	w.t.Fatalf("%s isn't a member", email)
	return ""
}

func (w *teamsWorld) project(name string) string {
	w.t.Helper()
	var p projects.Project
	w.ada.must(http.StatusCreated, http.MethodPost, "/api/v1/projects", map[string]string{"name": name}, &p)
	waitReady(w.t, w.ada, p.ID)
	return p.ID
}

func (w *teamsWorld) team(name string, members ...string) teams.Team {
	w.t.Helper()
	var t teams.Team
	w.ada.must(http.StatusCreated, http.MethodPost, "/api/v1/teams", map[string]string{"name": name}, &t)
	for _, m := range members {
		w.ada.must(http.StatusOK, http.MethodPut, "/api/v1/teams/"+t.ID+"/members/"+m, nil, &t)
	}
	return t
}

func (w *teamsWorld) grant(teamID, role, projectID string) teams.Team {
	w.t.Helper()
	var t teams.Team
	w.ada.must(http.StatusOK, http.MethodPut, "/api/v1/teams/"+teamID+"/grants", teams.SetGrantInput{Role: role, ProjectID: projectID}, &t)
	return t
}

// patch tries a change to a project as c and returns the status.
func patch(c *client, projectID string) int {
	return c.do(http.MethodPatch, "/api/v1/projects/"+projectID, map[string]string{"description": "touched"}, nil)
}

// send is client.do that also returns the body, whatever the status.
func send(c *client, method, path string, body any) (int, string) {
	c.t.Helper()
	var rd io.Reader = http.NoBody
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			c.t.Fatal(err)
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(c.context(), method, c.base+path, rd)
	if err != nil {
		c.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Publishable-Key", c.key)
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		c.t.Fatal(err)
	}
	return res.StatusCode, string(raw)
}

// assignments is the person's Warden rows in the workspace as
// "namespace|role slug", sorted. authsome's platform role, in its app's
// tenant, isn't the workspace's.
func (w *teamsWorld) assignments(userID string) []string {
	w.t.Helper()
	rows, err := w.sql.Query(w.t.Context(), `SELECT a.namespace_path||'|'||r.slug FROM warden_assignments a JOIN warden_roles r ON r.id=a.role_id
		WHERE a.tenant_id=$1 AND a.subject_kind='user' AND a.subject_id=$2 ORDER BY 1`, w.org, userID)
	if err != nil {
		w.t.Fatal(err)
	}
	got, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		w.t.Fatal(err)
	}
	return got
}

// assignmentID is the ID of the person's row in the namespace.
func (w *teamsWorld) assignmentID(userID, namespace string) string {
	w.t.Helper()
	var id string
	if err := w.sql.QueryRow(w.t.Context(), `SELECT id FROM warden_assignments WHERE tenant_id=$1 AND subject_kind='user' AND subject_id=$2 AND namespace_path=$3`, w.org, userID, namespace).Scan(&id); err != nil {
		w.t.Fatalf("%s's row at %q: %v", userID, namespace, err)
	}
	return id
}

func namespaceOf(projectID string) string {
	return "proj-" + strings.TrimPrefix(projectID, "proj_")
}

func listTeams(c *client) []teams.Team {
	c.t.Helper()
	var list struct {
		Teams []teams.Team `json:"teams"`
	}
	c.must(http.StatusOK, http.MethodGet, "/api/v1/teams", nil, &list)
	return list.Teams
}

func memberIDs(t teams.Team) []string {
	ids := make([]string, 0, len(t.Members))
	for _, m := range t.Members {
		ids = append(ids, m.UserID)
	}
	return ids
}

// A team's project grant counts on that project alone: Grace, a viewer,
// changes P through Payments and nothing else. Everyone sees the team, and
// every change is in the audit trail under Ada.
func TestTeams_grantReachesOnlyItsProject(t *testing.T) {
	w := newTeamsWorld(t)
	p, q := w.project("P"), w.project("Q")
	team := w.team("Payments", w.graceID)
	team = w.grant(team.ID, "developer", p)
	if len(team.Grants) != 1 || team.Grants[0].Role != "developer" || team.Grants[0].ProjectID != p {
		t.Fatalf("grants %+v", team.Grants)
	}

	if got := patch(w.grace, p); got != http.StatusOK {
		t.Fatalf("Grace PATCH P: %d, want 200", got)
	}
	if got := patch(w.grace, q); got != http.StatusForbidden {
		t.Fatalf("Grace PATCH Q: %d, want 403", got)
	}
	w.grace.must(http.StatusForbidden, http.MethodPost, "/api/v1/projects", map[string]string{"name": "R"}, nil)

	list := listTeams(w.vi)
	if len(list) != 1 || list[0].Name != "Payments" || !slices.Equal(memberIDs(list[0]), []string{w.graceID}) ||
		len(list[0].Grants) != 1 || list[0].Grants[0].Role != "developer" || list[0].Grants[0].ProjectID != p {
		t.Fatalf("Vi sees %+v", list)
	}
	var one teams.Team
	w.vi.must(http.StatusOK, http.MethodGet, "/api/v1/teams/"+team.ID, nil, &one)
	if one.ID != team.ID {
		t.Fatalf("Vi gets %+v", one)
	}

	// Each write names the team, the actor and what it changed.
	rows, err := w.sql.Query(t.Context(), `SELECT action||'|'||resource_id||'|'||user_id||'|'||tenant_id||'|'||category||'|'||coalesce(metadata->>'user_id','')||'|'||coalesce(metadata->>'role','')||'|'||coalesce(metadata->>'project_id','')
		FROM chronicle_events WHERE resource='team' ORDER BY sequence`)
	if err != nil {
		t.Fatal(err)
	}
	got, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	prefix := "|" + team.ID + "|" + w.adaID + "|" + w.org + "|members|"
	want := []string{
		"team.create" + prefix + "||",
		"team.member.add" + prefix + w.graceID + "||",
		"team.grant.set" + prefix + "|developer|" + p,
	}
	if !slices.Equal(got, want) {
		t.Fatalf("audit\n got %q\nwant %q", got, want)
	}
}

// Taking a grant or a team away takes back that and nothing else: Grace
// keeps Q through B when A's grant goes, keeps her member role through
// both, and her other rows keep their IDs.
func TestTeams_removalTakesBackExactlyWhatItGave(t *testing.T) {
	w := newTeamsWorld(t)
	p, q := w.project("P"), w.project("Q")
	a := w.grant(w.team("A", w.graceID).ID, "developer", p)
	b := w.grant(w.team("B", w.graceID).ID, "developer", q)
	root, onQ := w.assignmentID(w.graceID, ""), w.assignmentID(w.graceID, namespaceOf(q))

	w.ada.must(http.StatusOK, http.MethodDelete, "/api/v1/teams/"+a.ID+"/grants/"+a.Grants[0].ID, nil, nil)
	if got := patch(w.grace, p); got != http.StatusForbidden {
		t.Fatalf("PATCH P after A's grant went: %d, want 403", got)
	}
	if got := patch(w.grace, q); got != http.StatusOK {
		t.Fatalf("PATCH Q after A's grant went: %d, want 200", got)
	}
	if w.assignmentID(w.graceID, "") != root || w.assignmentID(w.graceID, namespaceOf(q)) != onQ {
		t.Fatal("a row the removal didn't touch has a new ID")
	}
	// Removing someone who isn't in the team says so.
	if code, body := send(w.ada, http.MethodDelete, "/api/v1/teams/"+a.ID+"/members/"+w.viID, nil); code != http.StatusNotFound || !strings.Contains(body, "isn't in this team") {
		t.Fatalf("removing Vi from A: %d %s, want 404", code, body)
	}
	w.ada.must(http.StatusNotFound, http.MethodDelete, "/api/v1/teams/"+a.ID+"/grants/"+a.Grants[0].ID, nil, nil)

	w.ada.must(http.StatusNoContent, http.MethodDelete, "/api/v1/teams/"+b.ID, nil, nil)
	if got := patch(w.grace, q); got != http.StatusForbidden {
		t.Fatalf("PATCH Q after B went: %d, want 403", got)
	}
	w.grace.must(http.StatusOK, http.MethodGet, "/api/v1/projects/"+p, nil, nil)
	if got := w.assignments(w.graceID); !slices.Equal(got, []string{"|viewer"}) || w.assignmentID(w.graceID, "") != root {
		t.Fatalf("Grace's rows %v", got)
	}
	w.ada.must(http.StatusNotFound, http.MethodGet, "/api/v1/teams/"+b.ID, nil, nil)
}

// Leaving the workspace ends every team right, and coming back as a viewer
// brings none of them back.
func TestTeams_leavingTheWorkspaceEndsTeamRights(t *testing.T) {
	w := newTeamsWorld(t)
	p := w.project("P")
	team := w.grant(w.team("Platform", w.graceID).ID, "admin", "")
	if got := patch(w.grace, p); got != http.StatusOK {
		t.Fatalf("Grace as a team admin: PATCH P %d", got)
	}

	if code := w.ada.do(http.MethodDelete, "/authsome/v1/orgs/"+w.org+"/members/"+w.memberID("grace@example.com"), nil, nil); code != http.StatusOK && code != http.StatusNoContent {
		t.Fatalf("remove Grace: status %d", code)
	}
	w.grace.must(http.StatusForbidden, http.MethodGet, "/api/v1/session", nil, nil)
	if got := w.assignments(w.graceID); len(got) != 0 {
		t.Fatalf("Grace still holds %v", got)
	}

	w.join(w.grace, "grace@example.com")
	if got := patch(w.grace, p); got != http.StatusForbidden {
		t.Fatalf("Grace back as a viewer: PATCH P %d, want 403", got)
	}
	if got := w.assignments(w.graceID); !slices.Equal(got, []string{"|viewer"}) {
		t.Fatalf("Grace back holds %v", got)
	}
	list := listTeams(w.grace)
	if len(list) != 1 || list[0].ID != team.ID || len(list[0].Members) != 0 {
		t.Fatalf("teams after Grace came back: %+v", list)
	}
}

// Deleting a project drops the rows its grants gave.
func TestTeams_deletingAProjectDropsItsRows(t *testing.T) {
	w := newTeamsWorld(t)
	p := w.project("P")
	w.grant(w.team("Payments", w.graceID).ID, "developer", p)
	if got := w.assignments(w.graceID); !slices.Equal(got, []string{namespaceOf(p) + "|developer", "|viewer"}) {
		t.Fatalf("Grace holds %v", got)
	}
	w.ada.must(http.StatusNoContent, http.MethodDelete, "/api/v1/projects/"+p, nil, nil)
	deadline := time.Now().Add(10 * time.Second)
	for {
		var n int
		if err := w.sql.QueryRow(t.Context(), `SELECT count(*) FROM warden_assignments WHERE tenant_id=$1 AND subject_id=$2 AND namespace_path LIKE 'proj-%'`, w.org, w.graceID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("Grace still has %d project rows", n)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// Only people join a team: never Loomplane automation, the agent, or
// someone from another workspace.
func TestTeams_automationCantJoin(t *testing.T) {
	w := newTeamsWorld(t)
	team := w.team("Payments")
	alan := w.signUp("alan@example.com")
	var me struct {
		ID string `json:"id"`
	}
	alan.must(http.StatusOK, http.MethodGet, "/authsome/v1/me", nil, &me)
	for _, id := range []string{"loomplane:" + w.org, "agent:" + w.org, me.ID} {
		if code, body := send(w.ada, http.MethodPut, "/api/v1/teams/"+team.ID+"/members/"+url.PathEscape(id), nil); code != http.StatusUnprocessableEntity {
			t.Errorf("adding %s: %d %s, want 422", id, code, body)
		}
	}
	if got := listTeams(w.ada); len(got[0].Members) != 0 {
		t.Fatalf("members %+v", got[0].Members)
	}
}

// Only admins manage teams, and nobody hands out or touches a role above
// their own. A caller's ceiling is their member role, raised by a
// workspace-wide team grant: Vi, a viewer who is an admin through a team,
// acts as a workspace admin. A project admin through a team is no
// workspace admin at all.
func TestTeams_grantCeiling(t *testing.T) {
	w := newTeamsWorld(t)
	p := w.project("P")
	admins := w.grant(w.team("Admins", w.viID).ID, "admin", "")
	other := w.team("Payments")
	w.grant(w.team("P admins", w.graceID).ID, "admin", p)

	// Grace is an admin on P alone, so she can't touch a team, and least
	// of all put herself in a workspace admin team.
	if code, body := send(w.grace, http.MethodPut, "/api/v1/teams/"+admins.ID+"/members/"+w.graceID, nil); code != http.StatusForbidden {
		t.Fatalf("Grace joins Admins: %d %s, want 403", code, body)
	}
	w.grace.must(http.StatusForbidden, http.MethodPost, "/api/v1/teams", map[string]string{"name": "Mine"}, nil)

	var sess struct {
		Permissions []string `json:"permissions"`
	}
	w.vi.must(http.StatusOK, http.MethodGet, "/api/v1/session", nil, &sess)
	if !contains(sess.Permissions, "member:manage") {
		t.Fatalf("Vi through Admins holds %v", sess.Permissions)
	}
	// Vi's ceiling is admin, through Admins: she grants developer and
	// admin, and puts Grace in Admins.
	w.vi.must(http.StatusOK, http.MethodPut, "/api/v1/teams/"+other.ID+"/grants", teams.SetGrantInput{Role: "developer"}, nil)
	w.vi.must(http.StatusOK, http.MethodPut, "/api/v1/teams/"+other.ID+"/grants", teams.SetGrantInput{Role: "admin", ProjectID: p}, nil)
	w.vi.must(http.StatusOK, http.MethodPut, "/api/v1/teams/"+admins.ID+"/members/"+w.graceID, nil, nil)
	if got := patch(w.grace, w.project("Q")); got != http.StatusOK {
		t.Fatalf("Grace in Admins: PATCH Q %d, want 200", got)
	}
}

// authsome's own team routes are gone, whatever the method; its other
// organization routes still work.
func TestTeams_authsomeTeamRoutesAreBlocked(t *testing.T) {
	w := newTeamsWorld(t)
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		code, body := send(w.ada, method, "/authsome/v1/orgs/"+w.org+"/teams", map[string]string{"name": "Shadow", "slug": "shadow"})
		if code != http.StatusNotFound || !strings.Contains(body, "/api/v1/teams") {
			t.Errorf("%s authsome teams: %d %s, want 404", method, code, body)
		}
	}
	w.ada.must(http.StatusOK, http.MethodGet, "/authsome/v1/orgs/"+w.org+"/members", nil, nil)
	var n int
	if err := w.sql.QueryRow(t.Context(), `SELECT count(*) FROM authsome_teams`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("authsome_teams: %d rows, %v", n, err)
	}
}

// The teams service is in DI for other extensions (the agent's routing).
func TestTeams_serviceIsInDI(t *testing.T) {
	w := newTeamsWorld(t)
	svc, err := forge.InjectType[*teams.Service](w.app.Container())
	if err != nil || svc == nil {
		t.Fatalf("resolve *teams.Service: %v", err)
	}
}

// The teams.sync job brings a queued person back in line with what their
// teams give them, through the real authorizer, the real sync lock and
// Warden's tables. While her sync lock is held elsewhere the job leaves her
// queued; once it's free the next run converges.
func TestTeams_syncJobConvergesAQueuedPerson(t *testing.T) {
	w := newTeamsWorld(t)
	p := w.project("P")
	w.grant(w.team("Payments", w.graceID).ID, "developer", p)
	ns := namespaceOf(p)
	// A sync that never ran: her project row is missing, and she's queued.
	if _, err := w.sql.Exec(t.Context(), `DELETE FROM warden_assignments WHERE tenant_id=$1 AND subject_id=$2 AND namespace_path=$3`, w.org, w.graceID, ns); err != nil {
		t.Fatal(err)
	}
	if _, err := w.sql.Exec(t.Context(), `INSERT INTO loomplane_authz_sync(org_id,user_id) VALUES($1,$2)`, w.org, w.graceID); err != nil {
		t.Fatal(err)
	}
	ext, err := w.app.GetExtension("dispatch")
	if err != nil {
		t.Fatal(err)
	}
	owner, ok := ext.(interface{ Engine() *engine.Engine })
	if !ok || owner.Engine() == nil {
		t.Fatal("the dispatch extension has no engine")
	}
	runJob := func() {
		t.Helper()
		j, enqueueErr := engine.Enqueue(t.Context(), owner.Engine(), teams.JobSync, teams.SyncPayload{}, job.WithQueue(orchestration.Queue))
		if enqueueErr != nil {
			t.Fatal(enqueueErr)
		}
		deadline := time.Now().Add(30 * time.Second)
		for {
			var state string
			if scanErr := w.sql.QueryRow(t.Context(), `SELECT state FROM dispatch_jobs WHERE id=$1`, j.ID.String()).Scan(&state); scanErr != nil {
				t.Fatal(scanErr)
			}
			if state == string(job.StateCompleted) {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("the job is %s", state)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	queued := func() int {
		t.Helper()
		var n int
		if scanErr := w.sql.QueryRow(t.Context(), `SELECT count(*) FROM loomplane_authz_sync`).Scan(&n); scanErr != nil {
			t.Fatal(scanErr)
		}
		return n
	}

	// Someone else holds her lock: the job skips her and leaves her queued.
	holder, err := pgx.Connect(t.Context(), w.sql.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close(t.Context())
	lock := "loomplane-authz-sync:" + w.org + ":" + w.graceID
	if _, err = holder.Exec(t.Context(), `SELECT pg_advisory_lock(hashtextextended($1, 0))`, lock); err != nil {
		t.Fatal(err)
	}
	runJob()
	if n, got := queued(), w.assignments(w.graceID); n != 1 || !slices.Equal(got, []string{"|viewer"}) {
		t.Fatalf("with her lock held: %d queued, Grace holds %v", n, got)
	}

	if _, err = holder.Exec(t.Context(), `SELECT pg_advisory_unlock(hashtextextended($1, 0))`, lock); err != nil {
		t.Fatal(err)
	}
	runJob()
	if n, got := queued(), w.assignments(w.graceID); n != 0 || !slices.Equal(got, []string{ns + "|developer", "|viewer"}) {
		t.Fatalf("after the job: %d queued, Grace holds %v", n, got)
	}
	if got := patch(w.grace, p); got != http.StatusOK {
		t.Fatalf("Grace PATCH P: %d", got)
	}
}
