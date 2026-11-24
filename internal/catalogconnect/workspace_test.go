package catalogconnect

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/xraph/loomplane/internal/recipes"
	"github.com/xraph/loomplane/internal/security"
)

// memStore keeps a workspace service's extras the way the Postgres store
// does. Its failReplace makes the next Replace fail, as a lost connection
// would.
var _ WorkspaceStore = (*memStore)(nil)

func (m *memStore) Update(_ context.Context, orgID, id string, in Details, at time.Time) (*Endpoint, error) {
	row, ok := m.rows[id]
	if !ok || row.OrgID != orgID {
		return nil, ErrNotFound
	}
	for otherID, other := range m.rows {
		if otherID != id && other.OrgID == orgID && other.Name == in.Name {
			return nil, ErrNameTaken
		}
	}
	row.Name, row.Description, row.EnvironmentLabel, row.UpdatedAt = in.Name, in.Description, in.EnvironmentLabel, &at
	m.rows[id] = row
	return &row, nil
}

func (m *memStore) Replace(_ context.Context, e *Endpoint, from int, at time.Time) error {
	if m.failReplace != nil {
		err := m.failReplace
		m.failReplace = nil
		return err
	}
	row, ok := m.rows[e.ID]
	if !ok || row.OrgID != e.OrgID {
		return ErrNotFound
	}
	if row.Revision != from {
		return ErrConflict
	}
	row.Host, row.Port, row.Database, row.Username, row.TLS = e.Host, e.Port, e.Database, e.Username, e.TLS
	row.Revision++
	row.UpdatedAt = &at
	m.rows[e.ID] = row
	return nil
}

func (m *memStore) SetSharing(_ context.Context, orgID, id string, restricted bool, projects []string, _ string, at time.Time) error {
	row, ok := m.rows[id]
	if !ok || row.OrgID != orgID {
		return ErrNotFound
	}
	row.Restricted, row.Projects, row.UpdatedAt = restricted, slices.Clone(projects), &at
	m.rows[id] = row
	return nil
}

func (m *memStore) SetAdmin(_ context.Context, orgID, id string, set bool, at time.Time) error {
	row, ok := m.rows[id]
	if !ok || row.OrgID != orgID {
		return ErrNotFound
	}
	row.AdminSet, row.UpdatedAt = set, &at
	m.rows[id] = row
	return nil
}

func (m *memStore) SaveCheck(_ context.Context, orgID, id string, c *LastCheck) error {
	row, ok := m.rows[id]
	if !ok || row.OrgID != orgID {
		return ErrNotFound
	}
	saved := *c
	row.LastCheck = &saved
	m.rows[id] = row
	return nil
}

func (m *memStore) Uses(_ context.Context, _, id string) ([]Use, error) {
	return []Use{{ProjectID: "project_a", Project: "shop", Environment: "production", Resource: id}}, nil
}

var passed = &LastCheck{OK: true, Version: "16.4", Target: "conn_1", Capabilities: &Capabilities{CreateDatabase: true}}

func registerOrders(t *testing.T, svc *Service, in RegisterInput) *Endpoint {
	t.Helper()
	if in.Name == "" {
		in.Name = "orders"
	}
	if in.Engine == "" {
		in.Engine, in.DSN = "postgres", testDSN
	}
	e, err := svc.Register(t.Context(), admin, in, passed)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestRegisterKeepsTheDetailsSharingAndCheck(t *testing.T) {
	store, secrets := newMemStore(), memSecrets{}
	svc := NewService(store, secrets, roleAuthz{})
	svc.ProjectsIn = func(_ context.Context, _ string, ids []string) ([]string, error) {
		var missing []string
		for _, id := range ids {
			if !strings.HasPrefix(id, "project_") {
				missing = append(missing, id)
			}
		}
		return missing, nil
	}
	svc.now = func() time.Time { return time.Date(2026, 11, 23, 9, 0, 0, 0, time.UTC) }
	e := registerOrders(t, svc, RegisterInput{
		Details:    Details{Name: "  orders ", Description: "Orders database", EnvironmentLabel: "production"},
		Restricted: true, Projects: []string{"project_b", "project_a", "project_b", " "},
	})
	if e.Name != "orders" || e.Description != "Orders database" || e.EnvironmentLabel != "production" || e.Origin != OriginRegistered {
		t.Fatalf("details: %+v", e)
	}
	if !e.Restricted || !reflect.DeepEqual(e.Projects, []string{"project_a", "project_b"}) {
		t.Fatalf("sharing: %v %v", e.Restricted, e.Projects)
	}
	if e.LastCheck == nil || !e.LastCheck.OK || !e.LastCheck.At.Equal(svc.now()) || !e.LastCheck.Capabilities.CreateDatabase {
		t.Fatalf("check: %+v", e.LastCheck)
	}
	if string(secrets[DSNKey("org", e.ID)]) != testDSN {
		t.Fatalf("vault: %q", secrets)
	}

	// Shared with every project, a list means nothing and isn't kept.
	open := registerOrders(t, svc, RegisterInput{Details: Details{Name: "cache"}, Projects: []string{"project_a"}})
	if open.Restricted || len(open.Projects) != 0 || !open.SharedWith("project_z") {
		t.Fatalf("unrestricted: %+v", open)
	}

	for name, tc := range map[string]struct {
		in    RegisterInput
		check *LastCheck
	}{
		"no check":           {RegisterInput{Details: Details{Name: "a"}, Engine: "postgres", DSN: testDSN}, nil},
		"a failed check":     {RegisterInput{Details: Details{Name: "b"}, Engine: "postgres", DSN: testDSN}, &LastCheck{Problems: []string{"refused"}}},
		"a stranger project": {RegisterInput{Details: Details{Name: "c"}, Engine: "postgres", DSN: testDSN, Restricted: true, Projects: []string{"elsewhere"}}, passed},
		"a two-line name":    {RegisterInput{Details: Details{Name: "d\ne"}, Engine: "postgres", DSN: testDSN}, passed},
		"a long label":       {RegisterInput{Details: Details{Name: "f", EnvironmentLabel: strings.Repeat("x", 41)}, Engine: "postgres", DSN: testDSN}, passed},
	} {
		if _, err := svc.Register(t.Context(), admin, tc.in, tc.check); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := svc.Register(t.Context(), developer, RegisterInput{Details: Details{Name: "g"}, Engine: "postgres", DSN: testDSN}, passed); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("a developer registered one: %v", err)
	}
	if len(store.rows) != 2 {
		t.Fatalf("refused registrations left rows: %d", len(store.rows))
	}
}

func TestOnlyRegisteredServicesChangeThroughTheWorkspace(t *testing.T) {
	store, secrets := newMemStore(), memSecrets{}
	svc := NewService(store, secrets, roleAuthz{})
	provider, err := svc.CreateForResource(t.Context(), admin, CreateInput{Name: "resource-x-1", Engine: "postgres", DSN: testDSN})
	if err != nil {
		t.Fatal(err)
	}
	if provider.Registered() {
		t.Fatal("a provider's endpoint reads as registered")
	}
	if _, err = svc.Update(t.Context(), admin, provider.ID, Details{Name: "mine"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("renamed a provider's endpoint: %v", err)
	}
	if _, err = svc.Share(t.Context(), admin, provider.ID, true, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("restricted a provider's endpoint: %v", err)
	}
	e := registerOrders(t, svc, RegisterInput{})
	if _, err = svc.Update(t.Context(), developer, e.ID, Details{Name: "x"}); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("a developer renamed it: %v", err)
	}
	got, err := svc.Update(t.Context(), admin, e.ID, Details{Name: "orders-db", Description: "Orders", EnvironmentLabel: "staging"})
	if err != nil || got.Name != "orders-db" || got.EnvironmentLabel != "staging" {
		t.Fatalf("update: %+v %v", got, err)
	}
	if _, err = svc.Update(t.Context(), admin, e.ID, Details{Name: "resource-x-1"}); !errors.Is(err, ErrNameTaken) {
		t.Fatalf("renamed onto a taken name: %v", err)
	}
	if got, err = svc.Share(t.Context(), admin, e.ID, true, []string{"project_a"}); err != nil || !got.SharedWith("project_a") || got.SharedWith("project_b") {
		t.Fatalf("share: %+v %v", got, err)
	}
	if got, err = svc.Share(t.Context(), admin, e.ID, false, []string{"project_a"}); err != nil || !got.SharedWith("project_b") || len(got.Projects) != 0 {
		t.Fatalf("share with every project: %+v %v", got, err)
	}
	if _, err = svc.Describe(t.Context(), viewer, e.ID); err != nil {
		t.Fatalf("a viewer can't read it: %v", err)
	}
	if uses, err := svc.Uses(t.Context(), viewer, e.ID); err != nil || len(uses) != 1 {
		t.Fatalf("uses: %v %v", uses, err)
	}
}

func TestReplaceSwapsCredentialsAtTheRevisionRead(t *testing.T) {
	store, secrets := newMemStore(), memSecrets{}
	svc := NewService(store, secrets, roleAuthz{})
	e := registerOrders(t, svc, RegisterInput{})
	next := "postgres://u:new@db2.example:5432/orders?sslmode=verify-full"

	if _, err := svc.Replace(t.Context(), admin, e.ID, 2, next, passed); !errors.Is(err, ErrConflict) {
		t.Fatalf("a replacement of an older read: %v", err)
	}
	if _, err := svc.Replace(t.Context(), admin, e.ID, 1, next, &LastCheck{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a replacement that failed its check: %v", err)
	}
	if _, err := svc.Replace(t.Context(), admin, e.ID, 1, "redis://:p@cache.example:6379/0", passed); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a replacement with another engine: %v", err)
	}
	// A row that can't be written puts the old credentials back.
	store.failReplace = errors.New("connection lost")
	if _, err := svc.Replace(t.Context(), admin, e.ID, 1, next, passed); err == nil || string(secrets[DSNKey("org", e.ID)]) != testDSN {
		t.Fatalf("a failed replacement: %v, vault %q", err, secrets[DSNKey("org", e.ID)])
	}
	got, err := svc.Replace(t.Context(), admin, e.ID, 1, next, passed)
	if err != nil {
		t.Fatal(err)
	}
	if got.Revision != 2 || got.Host != "db2.example" || got.TLS != TLSVerifyFull || string(secrets[DSNKey("org", e.ID)]) != next {
		t.Fatalf("replaced: %+v, vault %q", got, secrets[DSNKey("org", e.ID)])
	}
}

func TestTheAdminCredentialIsTheSameServers(t *testing.T) {
	store, secrets := newMemStore(), memSecrets{}
	svc := NewService(store, secrets, roleAuthz{})
	e := registerOrders(t, svc, RegisterInput{})
	adminCheck := &LastCheck{OK: true, Capabilities: &Capabilities{CreateDatabase: true, CreateUser: true}}
	if _, err := svc.SetAdmin(t.Context(), admin, e.ID, "postgres://root:x@elsewhere.example:5432/postgres?sslmode=require", adminCheck); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an admin credential for another server: %v", err)
	}
	adminDSN := "postgres://root:x@db.example:5432/postgres?sslmode=require"
	got, err := svc.SetAdmin(t.Context(), admin, e.ID, adminDSN, adminCheck)
	if err != nil {
		t.Fatal(err)
	}
	if !got.AdminSet || got.LastCheck.Admin == nil || !got.LastCheck.Admin.CreateUser || !got.LastCheck.Capabilities.CreateDatabase {
		t.Fatalf("admin: %+v %+v", got, got.LastCheck)
	}
	if _, dsn, resolveErr := svc.ResolveAdmin(t.Context(), "org", e.ID); resolveErr != nil || dsn != adminDSN {
		t.Fatalf("resolve the admin credential: %q %v", dsn, resolveErr)
	}
	if got, err = svc.ClearAdmin(t.Context(), admin, e.ID); err != nil || got.AdminSet || got.LastCheck.Admin != nil {
		t.Fatalf("clear: %+v %v", got, err)
	}
	if _, ok := secrets[AdminDSNKey("org", e.ID)]; ok {
		t.Fatal("the admin credential stayed in Vault")
	}
	if _, _, err = svc.ResolveAdmin(t.Context(), "org", e.ID); !errors.Is(err, ErrInvalid) {
		t.Fatalf("resolved a forgotten admin credential: %v", err)
	}
	// Deleting the service forgets both credentials.
	if _, err = svc.SetAdmin(t.Context(), admin, e.ID, adminDSN, adminCheck); err != nil {
		t.Fatal(err)
	}
	if err = svc.Delete(t.Context(), admin, e.ID); err != nil {
		t.Fatal(err)
	}
	if len(secrets) != 0 {
		t.Fatalf("credentials outlived the service: %v", slices.Collect(func(yield func(string) bool) {
			for k := range secrets {
				if !yield(k) {
					return
				}
			}
		}))
	}
}

func TestSaveCheckKeepsTheAdminsCapabilitiesUnlessItHasItsOwn(t *testing.T) {
	store, secrets := newMemStore(), memSecrets{}
	svc := NewService(store, secrets, roleAuthz{})
	e := registerOrders(t, svc, RegisterInput{})
	if _, err := svc.SetAdmin(t.Context(), admin, e.ID, "postgres://root:x@db.example:5432/postgres?sslmode=require", &LastCheck{OK: true, Capabilities: &Capabilities{CreateUser: true}}); err != nil {
		t.Fatal(err)
	}
	got, err := svc.SaveCheck(t.Context(), admin, e.ID, LastCheck{OK: false, Problems: []string{"no answer"}})
	if err != nil || got.LastCheck.OK || got.LastCheck.Admin == nil || !got.LastCheck.Admin.CreateUser {
		t.Fatalf("saved: %+v %v", got.LastCheck, err)
	}
}

func TestPostgresProberReportsCapabilitiesWhenAsked(t *testing.T) {
	for _, tc := range []struct {
		pg   fakePg
		want Capabilities
	}{
		{fakePg{version: "16.4"}, Capabilities{}},
		{fakePg{version: "16.4", createDB: true}, Capabilities{CreateDatabase: true}},
		{fakePg{version: "16.4", createRole: true}, Capabilities{CreateUser: true}},
		{fakePg{version: "16.4", super: true}, Capabilities{CreateDatabase: true, CreateUser: true}},
	} {
		fake := tc.pg
		prober := PostgresProber{connect: func(context.Context, *pgx.ConnConfig) (pgConn, error) { return &fake, nil }}
		report, err := prober.Probe(WithCapabilities(t.Context()), refuse, testDSN, recipes.Requirement{Engine: EnginePostgres})
		if err != nil {
			t.Fatal(err)
		}
		if report.Capabilities == nil || *report.Capabilities != tc.want {
			t.Errorf("%+v: %+v", tc.pg, report.Capabilities)
		}
		if report.Result().Capabilities != report.Capabilities {
			t.Error("the result dropped the capabilities")
		}
	}
	// Without asking, there's no extra query and nothing to report.
	fake := fakePg{version: "16.4", super: true}
	prober := PostgresProber{connect: func(context.Context, *pgx.ConnConfig) (pgConn, error) { return &fake, nil }}
	report, err := prober.Probe(t.Context(), refuse, testDSN, recipes.Requirement{Engine: EnginePostgres})
	if err != nil || report.Capabilities != nil || slices.Contains(fake.queried, "create") {
		t.Fatalf("unasked: %+v %v %v", report, err, fake.queried)
	}
}
