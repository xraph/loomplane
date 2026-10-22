package catalogconnect

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/security"
)

const testDSN = "postgres://u:p@db.example:5432/app?sslmode=require"

// roleAuthz allows what the principal's role allows in security.Catalog.
// The principal's UserID names its role.
type roleAuthz struct{}

func (roleAuthz) Require(_ context.Context, p security.Principal, perm security.Permission, _ string) error {
	for _, role := range security.Catalog() {
		if role.Slug == p.UserID && slices.Contains(role.Permissions, perm) {
			return nil
		}
	}
	return security.ErrForbidden
}

var (
	admin     = security.Principal{OrgID: "org", UserID: security.RoleAdmin}
	developer = security.Principal{OrgID: "org", UserID: security.RoleDeveloper}
	viewer    = security.Principal{OrgID: "org", UserID: security.RoleViewer}
	stranger  = security.Principal{OrgID: "org", UserID: "nobody"}
)

type binding struct {
	orgID  string
	config string
}

// memStore keeps endpoints in memory and finds uses in binding configs the
// way the Postgres store's jsonpath does.
type memStore struct {
	rows     map[string]Endpoint
	bindings []binding
}

func newMemStore() *memStore { return &memStore{rows: map[string]Endpoint{}} }

func (m *memStore) Create(_ context.Context, e *Endpoint) error {
	for _, row := range m.rows {
		if row.OrgID == e.OrgID && row.Name == e.Name {
			return ErrNameTaken
		}
	}
	m.rows[e.ID] = *e
	return nil
}

func (m *memStore) Get(_ context.Context, orgID, id string) (*Endpoint, error) {
	row, ok := m.rows[id]
	if !ok || row.OrgID != orgID {
		return nil, ErrNotFound
	}
	return &row, nil
}

func (m *memStore) List(_ context.Context, orgID string) ([]*Endpoint, error) {
	out := []*Endpoint{}
	for _, row := range m.rows {
		if row.OrgID == orgID {
			out = append(out, &row)
		}
	}
	return out, nil
}

func (m *memStore) Delete(_ context.Context, orgID, id string) error {
	row, ok := m.rows[id]
	if !ok || row.OrgID != orgID {
		return ErrNotFound
	}
	for _, b := range m.bindings {
		if b.orgID != orgID {
			continue
		}
		var cfg struct {
			Composition struct {
				Dependencies map[string]struct {
					External string `json:"external"`
				} `json:"dependencies"`
			} `json:"composition"`
		}
		if err := json.Unmarshal([]byte(b.config), &cfg); err != nil {
			return err
		}
		for _, dep := range cfg.Composition.Dependencies {
			if dep.External == id {
				return ErrInUse
			}
		}
	}
	delete(m.rows, id)
	return nil
}

type memSecrets map[string][]byte

func (m memSecrets) Put(_ context.Context, key string, value []byte) error {
	m[key] = slices.Clone(value)
	return nil
}

func (m memSecrets) Get(_ context.Context, key string) ([]byte, error) {
	v, ok := m[key]
	if !ok {
		return nil, errors.New("no secret")
	}
	return v, nil
}

func (m memSecrets) Delete(_ context.Context, key string) error {
	delete(m, key)
	return nil
}

// strings in a value, field by field, so a test can say none is a secret.
func fieldStrings(v any) []string {
	var out []string
	rv := reflect.Indirect(reflect.ValueOf(v))
	for i := range rv.NumField() {
		if f := rv.Field(i); f.Kind() == reflect.String {
			out = append(out, f.String())
		}
	}
	return out
}

func TestCreateStoresDSNInVaultOnly(t *testing.T) {
	store, secrets := newMemStore(), memSecrets{}
	svc := NewService(store, secrets, roleAuthz{})
	got, err := svc.Create(t.Context(), admin, CreateInput{Name: "prod-pg", Engine: "postgres", DSN: testDSN})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got.ID, "endpoint_") {
		t.Fatalf("id: %q", got.ID)
	}
	want := Endpoint{
		ID: got.ID, OrgID: "org", Name: "prod-pg", Engine: "postgres", Host: "db.example", Port: 5432,
		Database: "app", Username: "u", TLS: "require", CreatedBy: security.RoleAdmin, CreatedAt: got.CreatedAt, Revision: 1,
	}
	if *got != want || got.CreatedAt.IsZero() {
		t.Fatalf("endpoint:\n got %+v\nwant %+v", *got, want)
	}
	if string(secrets[DSNKey("org", got.ID)]) != testDSN || len(secrets) != 1 {
		t.Fatalf("vault: %q", secrets)
	}
	if DSNKey("org", got.ID) != "org/catalog-endpoints/"+got.ID+"/dsn" {
		t.Fatalf("key: %s", DSNKey("org", got.ID))
	}
	row := store.rows[got.ID]
	for _, v := range []any{got, &row} {
		body, _ := json.Marshal(v)
		if strings.Contains(string(body), "u:p@") || strings.Contains(string(body), testDSN) {
			t.Fatalf("the DSN leaked: %s", body)
		}
		if slices.Contains(fieldStrings(v), "p") {
			t.Fatalf("the password leaked: %+v", v)
		}
	}
	ep, dsn, err := svc.Resolve(t.Context(), "org", got.ID)
	if err != nil || dsn != testDSN || *ep != want {
		t.Fatalf("resolve: %+v %q %v", ep, dsn, err)
	}
	if _, _, err = svc.Resolve(t.Context(), "other", got.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another organization resolved it: %v", err)
	}

	redis, err := svc.Create(t.Context(), admin, CreateInput{Name: "cache", Engine: "redis", DSN: "rediss://default:hunter2@cache.example/3"})
	if err != nil {
		t.Fatal(err)
	}
	if redis.Host != "cache.example" || redis.Port != 6379 || redis.Database != "3" || redis.Username != "default" || redis.TLS != "verify-full" {
		t.Fatalf("redis endpoint: %+v", redis)
	}
	if _, err = svc.Create(t.Context(), admin, CreateInput{Name: "prod-pg", Engine: "postgres", DSN: testDSN}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a taken name: %v", err)
	}
	if len(secrets) != 2 {
		t.Fatalf("a refused create left its DSN in Vault: %d secrets", len(secrets))
	}
}

func TestCreateRejectsBadDSN(t *testing.T) {
	for _, tc := range []struct{ name, engine, dsn string }{
		{"scheme", "postgres", "mysql://u:p@db.example:3306/app"},
		{"no host", "postgres", "postgres://u:p@:5432/app?sslmode=require"},
		{"unknown engine", "mongodb", "mongodb://u:p@db.example/app"},
		{"redis scheme for postgres", "postgres", "redis://db.example:6379"},
		{"no sslmode", "postgres", "postgres://u:p@db.example/app"},
		{"prefer", "postgres", "postgres://u:p@db.example/app?sslmode=prefer"},
		{"host in query", "postgres", "postgres://u:p@db.example/app?sslmode=disable&host=/var/run/postgresql"},
		{"root certificate file", "postgres", "postgres://u:p@db.example/app?sslmode=verify-full&sslrootcert=/etc/passwd"},
		{"two hosts", "postgres", "postgres://u:p@a.example,b.example/app?sslmode=require"},
		{"bad port", "postgres", "postgres://u:p@db.example:0/app?sslmode=require"},
		{"key value", "postgres", "host=db.example user=u password=p sslmode=require"},
		{"redis database", "redis", "redis://cache.example/zero"},
		{"redis query", "redis", "redis://cache.example?protocol=3"},
		{"no name", "", "postgres://u:p@db.example/app?sslmode=require"},
		{"no user", "postgres", "postgres://db.example/app?sslmode=require"},
		{"empty user", "postgres", "postgres://:p@db.example/app?sslmode=require"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			secrets := memSecrets{}
			name := "endpoint"
			if tc.engine == "" {
				name, tc.engine = " ", "postgres"
			}
			_, err := NewService(newMemStore(), secrets, roleAuthz{}).Create(t.Context(), admin, CreateInput{Name: name, Engine: tc.engine, DSN: tc.dsn})
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("got %v", err)
			}
			if strings.Contains(err.Error(), "u:p@") || strings.Contains(err.Error(), tc.dsn) {
				t.Fatalf("the error repeats the DSN: %v", err)
			}
			if len(secrets) != 0 {
				t.Fatalf("stored a refused DSN")
			}
		})
	}
}

func TestCreateNeedsSecretManage(t *testing.T) {
	store, secrets := newMemStore(), memSecrets{}
	svc := NewService(store, secrets, roleAuthz{})
	in := CreateInput{Name: "prod-pg", Engine: "postgres", DSN: testDSN}
	if _, err := svc.Create(t.Context(), developer, in); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("developer created an endpoint: %v", err)
	}
	if len(store.rows) != 0 || len(secrets) != 0 {
		t.Fatal("a refused create stored something")
	}
	ep, err := svc.Create(t.Context(), admin, in)
	if err != nil {
		t.Fatal(err)
	}
	list, err := svc.List(t.Context(), viewer)
	if err != nil || len(list) != 1 || list[0].ID != ep.ID {
		t.Fatalf("viewer list: %+v %v", list, err)
	}
	if _, err = svc.List(t.Context(), stranger); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("a principal without InfrastructureRead listed endpoints: %v", err)
	}
	if list, err = svc.List(t.Context(), security.Principal{OrgID: "other", UserID: security.RoleAdmin}); err != nil || len(list) != 0 {
		t.Fatalf("another organization's list: %+v %v", list, err)
	}
	if err = svc.Delete(t.Context(), developer, ep.ID); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("developer deleted an endpoint: %v", err)
	}
	if len(store.rows) != 1 || len(secrets) != 1 {
		t.Fatal("a refused delete removed something")
	}
}

func TestDeleteRefusesEndpointInUse(t *testing.T) {
	store, secrets := newMemStore(), memSecrets{}
	svc := NewService(store, secrets, roleAuthz{})
	ep, err := svc.Create(t.Context(), admin, CreateInput{Name: "prod-pg", Engine: "postgres", DSN: testDSN})
	if err != nil {
		t.Fatal(err)
	}
	store.bindings = []binding{
		{"org", `{"composition":{"dependencies":{"postgres":{"external":"` + ep.ID + `"}}}}`},
	}
	if err = svc.Delete(t.Context(), admin, ep.ID); !errors.Is(err, ErrInUse) {
		t.Fatalf("deleted an endpoint in use: %v", err)
	}
	if string(secrets[DSNKey("org", ep.ID)]) != testDSN {
		t.Fatal("the Vault entry went with a refused delete")
	}
	if _, _, err = svc.Resolve(t.Context(), "org", ep.ID); err != nil {
		t.Fatal(err)
	}
	store.bindings = []binding{
		{"org", `{"composition":{"dependencies":{"postgres":{"resource":"db"}}}}`},
		{"other", `{"composition":{"dependencies":{"postgres":{"external":"` + ep.ID + `"}}}}`},
	}
	if err = svc.Delete(t.Context(), admin, ep.ID); err != nil {
		t.Fatal(err)
	}
	if len(secrets) != 0 {
		t.Fatalf("the DSN outlived its endpoint: %q", secrets)
	}
	if err = svc.Delete(t.Context(), admin, ep.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted twice: %v", err)
	}
}
