package fabriqloom_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/fabriqloom"
)

// fakeFabriq stands in for fabriq-admin's nginx and the Fabriq server
// behind it, as a component channel to the admin component's http port
// reaches them. It requires the control credential before serving the API.
// The legacy fixture expects no control header. Setting refuse answers
// requests that pass the gate with the specified status.
type fakeFabriq struct {
	mu           sync.Mutex
	tenants      map[string]time.Time
	keys         []*fakeKey // in issue order
	requests     []fakeRequest
	controlToken string
	closed       int
	refuse       int
	// failRevoke answers DELETE /admin/keys/:id with 500.
	failRevoke bool
	// revokeNotFound answers DELETE /admin/keys/:id with 404, as Fabriq
	// does for a key it no longer has, and leaves the key as it was.
	revokeNotFound bool
	types          []string
	applied        []string
	// noMigrations answers GET /admin/migrations with this status.
	noMigrations int
}

type fakeKey struct {
	ID, Prefix, Key, Label, TenantID string
	CanManageKeys                    bool
	CreatedAt                        time.Time
	RevokedAt, LastUsedAt            *time.Time
}

type fakeRequest struct {
	Method, Path string
	Header       http.Header
	Body         string
}

const (
	fixtureControlToken  = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	fixtureControlHeader = "X-Fabriq-Control-Token"
	fixtureControlSecret = "admin-control-token"
	fixtureControlEnv    = "FABRIQ_ADMIN_CONTROL_TOKEN"
)

var tenantRule = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

func newFakeFabriq(tenants ...string) *fakeFabriq {
	f := &fakeFabriq{tenants: map[string]time.Time{}, controlToken: fixtureControlToken}
	for _, t := range tenants {
		f.tenants[t] = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	}
	return f
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (f *fakeFabriq) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var raw []byte
	if r.Body != nil {
		raw, _ = io.ReadAll(r.Body)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, fakeRequest{Method: r.Method, Path: r.URL.Path, Header: r.Header.Clone(), Body: string(raw)})
	control := r.Header.Values(fixtureControlHeader)
	switch {
	case f.controlToken != "" && (len(control) != 1 || control[0] != f.controlToken):
		w.WriteHeader(http.StatusForbidden)
		return
	case f.controlToken == "" && len(control) != 0:
		w.WriteHeader(http.StatusBadRequest)
		return
	case r.Header.Get("Authorization") != "":
		// Loomplane never sends its own: nginx injects the operator key.
		w.WriteHeader(http.StatusBadRequest)
		return
	case r.Header.Get("Sec-Fetch-Site") != "":
		w.WriteHeader(http.StatusForbidden)
		return
	case f.refuse != 0:
		writeJSON(w, f.refuse, map[string]string{"error": "refused"})
		return
	case r.Header.Get("X-Fabriq-Api-Version") != "1":
		w.WriteHeader(http.StatusUpgradeRequired)
		return
	}
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/admin/tenants":
		var body struct {
			TenantID string `json:"tenantId"`
		}
		if json.Unmarshal(raw, &body) != nil || !tenantRule.MatchString(body.TenantID) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid tenant id"})
			return
		}
		status := http.StatusOK
		if _, ok := f.tenants[body.TenantID]; !ok {
			f.tenants[body.TenantID] = time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC)
			status = http.StatusCreated
		}
		writeJSON(w, status, map[string]any{"tenant": f.tenantJSON(body.TenantID)})
	case r.Method == http.MethodGet && r.URL.Path == "/admin/tenants":
		ids := make([]string, 0, len(f.tenants))
		for id := range f.tenants {
			ids = append(ids, id)
		}
		slices.Sort(ids)
		out := make([]map[string]any, 0, len(ids))
		for _, id := range ids {
			out = append(out, f.tenantJSON(id))
		}
		writeJSON(w, http.StatusOK, map[string]any{"tenants": out})
	case r.Method == http.MethodPost && r.URL.Path == "/admin/keys":
		var body struct {
			Label         string  `json:"label"`
			CanManageKeys bool    `json:"canManageKeys"`
			TenantID      *string `json:"tenantId"`
		}
		if json.Unmarshal(raw, &body) != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		n := len(f.keys) + 1
		k := &fakeKey{ID: fmt.Sprintf("01KFAKE%04d", n), Prefix: fmt.Sprintf("fq_k%03d", n), Key: fmt.Sprintf("fq_k%03dsecret%04d", n, n), Label: body.Label,
			CanManageKeys: body.CanManageKeys, CreatedAt: time.Date(2026, 10, 22, 0, 0, n, 0, time.UTC)}
		if body.TenantID != nil {
			k.TenantID = *body.TenantID
		}
		f.keys = append(f.keys, k)
		writeJSON(w, http.StatusCreated, map[string]string{"id": k.ID, "prefix": k.Prefix, "key": k.Key})
	case r.Method == http.MethodGet && r.URL.Path == "/admin/keys":
		out := []map[string]any{}
		for i := len(f.keys) - 1; i >= 0; i-- {
			k := f.keys[i]
			m := map[string]any{"id": k.ID, "prefix": k.Prefix, "label": k.Label, "canManageKeys": k.CanManageKeys, "createdAt": k.CreatedAt}
			if k.TenantID != "" {
				m["tenantId"] = k.TenantID
			}
			if k.RevokedAt != nil {
				m["revokedAt"] = *k.RevokedAt
			}
			if k.LastUsedAt != nil {
				m["lastUsedAt"] = *k.LastUsedAt
			}
			out = append(out, m)
		}
		writeJSON(w, http.StatusOK, map[string]any{"keys": out})
	case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/admin/keys/"):
		if f.failRevoke {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if f.revokeNotFound {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/admin/keys/")
		for _, k := range f.keys {
			if k.ID == id {
				if k.RevokedAt == nil {
					at := time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC)
					k.RevokedAt = &at
				}
				writeJSON(w, http.StatusOK, map[string]bool{"revoked": true})
				return
			}
		}
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
	case r.Method == http.MethodGet && r.URL.Path == "/admin/entities/types":
		if r.Header.Get("X-Tenant-ID") == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"types": f.types})
	case r.Method == http.MethodGet && r.URL.Path == "/admin/migrations":
		if f.noMigrations != 0 {
			w.WriteHeader(f.noMigrations)
			return
		}
		applied := []map[string]string{}
		for _, v := range f.applied {
			applied = append(applied, map[string]string{"version": v, "name": "m" + v})
		}
		writeJSON(w, http.StatusOK, map[string]any{"groups": []map[string]any{
			{"name": "grove", "applied": []map[string]string{{"version": "1"}}, "pending": []any{}},
			{"name": "fabriq", "applied": applied, "pending": []any{}},
		}})
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *fakeFabriq) tenantJSON(id string) map[string]any {
	return map[string]any{"tenantId": id, "label": id, "state": "active", "mode": "static", "createdAt": f.tenants[id]}
}

// RoundTrip reaches the fake the way a component channel does, refusing
// what a channel refuses.
func (f *fakeFabriq) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Scheme != "http" || r.URL.Host != "component" {
		return nil, errors.New("a component channel sends requests only to http://component")
	}
	if r.Header.Get("Authorization") != "" {
		return nil, errors.New("a component channel can't carry an Authorization header")
	}
	// A request whose context has ended fails, as it would on the wire.
	if err := r.Context().Err(); err != nil {
		return nil, err
	}
	w := httptest.NewRecorder()
	f.ServeHTTP(w, r)
	res := w.Result()
	res.Request = r
	return res, nil
}

// channel is a component channel to the fake.
func (f *fakeFabriq) channel() *deployments.ComponentChannel {
	return &deployments.ComponentChannel{
		Client:  &http.Client{Transport: f, Timeout: 5 * time.Second},
		BaseURL: &url.URL{Scheme: "http", Host: "component"},
		Close: func() error {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.closed++
			return nil
		},
	}
}

func (f *fakeFabriq) closes() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closed
}

func fixtureAdmin(t *testing.T, f *fakeFabriq) *fabriqloom.AdminClient {
	t.Helper()
	ch := f.channel()
	admin, err := fabriqloom.NewTestAdminClient(ch, func(name string) ([]byte, error) {
		if name != fixtureControlSecret {
			t.Fatal("looked up the wrong secret")
		}
		return []byte(fixtureControlToken), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ch.Close() })
	return admin
}

func (f *fakeFabriq) seen() []fakeRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.requests)
}

func (f *fakeFabriq) count(method, path string) int {
	n := 0
	for _, r := range f.seen() {
		if r.Method == method && r.Path == path {
			n++
		}
	}
	return n
}

func (f *fakeFabriq) key(id string) *fakeKey {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, k := range f.keys {
		if k.ID == id {
			c := *k
			return &c
		}
	}
	return nil
}

// valid lists the ids of the keys Fabriq still accepts with label.
func (f *fakeFabriq) valid(label string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, k := range f.keys {
		if k.Label == label && k.RevokedAt == nil {
			out = append(out, k.ID)
		}
	}
	return out
}

// plant issues a key the way a Loomplane that crashed before recording it
// would have, long before the harness's clock.
func (f *fakeFabriq) plant(label string) *fakeKey {
	return f.plantAt(label, time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC))
}

// plantAt is plant with the key created at.
func (f *fakeFabriq) plantAt(label string, at time.Time) *fakeKey {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := len(f.keys) + 1
	k := &fakeKey{ID: fmt.Sprintf("01KORPHAN%04d", n), Prefix: fmt.Sprintf("fq_o%03d", n), Key: fmt.Sprintf("fq_o%03dsecret", n), Label: label, CreatedAt: at}
	f.keys = append(f.keys, k)
	return k
}

// use records that key id was last used at.
func (f *fakeFabriq) use(id string, at time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, k := range f.keys {
		if k.ID == id {
			k.LastUsedAt = &at
		}
	}
}

func TestAdminClientSpeaksPlan2aContract(t *testing.T) {
	f := newFakeFabriq()
	admin := fixtureAdmin(t, f)
	ctx := t.Context()

	tenant, created, err := admin.EnsureTenant(ctx, "acme")
	if err != nil || !created || tenant.ID != "acme" {
		t.Fatalf("first EnsureTenant: %+v %v %v", tenant, created, err)
	}
	tenant, created, err = admin.EnsureTenant(ctx, "acme")
	if err != nil || created || tenant.ID != "acme" {
		t.Fatalf("second EnsureTenant: %+v %v %v", tenant, created, err)
	}
	if body := f.seen()[0].Body; strings.TrimSpace(body) != `{"tenantId":"acme"}` {
		t.Fatalf("tenant body: %s", body)
	}
	tenants, err := admin.Tenants(ctx)
	if err != nil || len(tenants) != 1 || tenants[0].ID != "acme" || tenants[0].CreatedAt.IsZero() {
		t.Fatalf("Tenants: %+v %v", tenants, err)
	}

	issued, err := admin.IssueKey(ctx, "loomplane:api")
	if err != nil || issued.ID == "" || !strings.HasPrefix(issued.Key, "fq_") || issued.Prefix == "" {
		t.Fatalf("IssueKey: id %q prefix %q %v", issued.ID, issued.Prefix, err)
	}
	var body map[string]any
	for _, r := range f.seen() {
		if r.Method == http.MethodPost && r.Path == "/admin/keys" {
			if uerr := json.Unmarshal([]byte(r.Body), &body); uerr != nil {
				t.Fatal(uerr)
			}
		}
	}
	if len(body) != 2 || body["label"] != "loomplane:api" || body["canManageKeys"] != false {
		t.Fatalf("key body: %v", body)
	}
	keys, err := admin.Keys(ctx)
	if err != nil || len(keys) != 1 || keys[0].ID != issued.ID || keys[0].Label != "loomplane:api" || keys[0].TenantID != "" || keys[0].RevokedAt != nil {
		t.Fatalf("Keys: %+v %v", keys, err)
	}

	if err := admin.RevokeKey(ctx, issued.ID); err != nil {
		t.Fatal(err)
	}
	if err := admin.RevokeKey(ctx, issued.ID); err != nil {
		t.Fatalf("repeat revoke: %v", err)
	}
	if err := admin.RevokeKey(ctx, "01KUNKNOWN"); err != nil {
		t.Fatalf("a 404 is revoked: %v", err)
	}
	if keys, _ := admin.Keys(ctx); keys[0].RevokedAt == nil {
		t.Fatal("the key wasn't revoked")
	}

	for _, r := range f.seen() {
		if r.Header.Get("X-Fabriq-Api-Version") != "1" {
			t.Fatalf("%s %s has no X-Fabriq-Api-Version: 1", r.Method, r.Path)
		}
		if r.Header.Get("Authorization") != "" || r.Header.Get("Sec-Fetch-Site") != "" {
			t.Fatalf("%s %s carries its own credential headers", r.Method, r.Path)
		}
	}

	f.refuse = http.StatusUnauthorized
	if _, err := admin.Tenants(ctx); !errors.Is(err, fabriqloom.ErrOperatorKey) {
		t.Fatalf("401: %v", err)
	}
	f.refuse = http.StatusUpgradeRequired
	if _, err := admin.Keys(ctx); err == nil || !strings.Contains(err.Error(), "this Fabriq server speaks another admin API version") {
		t.Fatalf("426: %v", err)
	}
	for _, status := range []int{http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout} {
		f.refuse = status
		_, err := admin.IssueKey(ctx, "loomplane:api")
		if !errors.Is(err, fabriqloom.ErrUnreachable) || !strings.Contains(err.Error(), fmt.Sprint(status)) {
			t.Fatalf("%d: %v", status, err)
		}
	}
	f.refuse = 0
	if _, _, err := admin.EnsureTenant(ctx, "Not_A_Tenant"); err == nil {
		t.Fatal("a 400 tenant id was accepted")
	}
}

func TestAdminClientReadsEntityTypesAndMigrations(t *testing.T) {
	f := newFakeFabriq("acme")
	f.types = []string{"a", "b", "c"}
	f.applied = []string{"202610210001", "202610210035"}
	admin := fixtureAdmin(t, f)
	ctx := t.Context()
	n, err := admin.EntityTypeCount(ctx, "acme")
	if err != nil || n != 3 {
		t.Fatalf("EntityTypeCount: %d %v", n, err)
	}
	if got := f.seen()[0].Header.Get("X-Tenant-ID"); got != "acme" {
		t.Fatalf("X-Tenant-ID: %q", got)
	}
	versions, known, err := admin.AppliedMigrations(ctx, "acme")
	if err != nil || !known || !slices.Equal(versions, f.applied) {
		t.Fatalf("AppliedMigrations: %v %v %v", versions, known, err)
	}
	for _, status := range []int{http.StatusForbidden, http.StatusNotFound, http.StatusNotImplemented} {
		f.noMigrations = status
		versions, known, err := admin.AppliedMigrations(ctx, "")
		if err != nil || known || versions != nil {
			t.Fatalf("%d: %v %v %v", status, versions, known, err)
		}
	}
}

func TestDSNFormat(t *testing.T) {
	if got := fabriqloom.DSN("fq_x", "lp-a:8081", "acme"); got != "fabriq://fq_x@lp-a:8081/acme?tls=false" {
		t.Fatalf("got %s", got)
	}
}

// noSecret fails when err carries any key Fabriq issued.
func noSecret(t *testing.T, f *fakeFabriq, err error) {
	t.Helper()
	if err == nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, k := range f.keys {
		if strings.Contains(err.Error(), k.Key) {
			t.Fatalf("an error carries a Fabriq key: %v", errors.New("redacted"))
		}
	}
	if strings.Contains(err.Error(), "fabriq://") {
		t.Fatal("an error carries a DSN")
	}
}

func TestAdminClientControlCredential(t *testing.T) {
	t.Run("zero value", func(t *testing.T) {
		var admin fabriqloom.AdminClient
		if _, err := admin.Tenants(t.Context()); err == nil {
			t.Fatal("zero credential was accepted")
		}
	})

	t.Run("header only", func(t *testing.T) {
		f := newFakeFabriq()
		ch := f.channel()
		ch.Client.Transport = roundTripper(func(r *http.Request) (*http.Response, error) {
			raw, err := io.ReadAll(r.Body)
			if err != nil {
				return nil, err
			}
			if r.Header.Get(fixtureControlHeader) != fixtureControlToken || r.Header.Get("Authorization") != "" {
				t.Error("request did not carry only the control credential")
			}
			if strings.Contains(r.URL.String(), fixtureControlToken) || strings.Contains(string(raw), fixtureControlToken) {
				t.Error("control credential appeared outside its header")
			}
			return nil, fmt.Errorf("transport echoed %s: %w", r.Header.Get(fixtureControlHeader), context.DeadlineExceeded)
		})
		admin, err := fabriqloom.NewTestAdminClient(ch, func(string) ([]byte, error) {
			return []byte(fixtureControlToken), nil
		})
		if err != nil {
			t.Fatal(err)
		}
		_, _, err = admin.EnsureTenant(t.Context(), "acme")
		if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "transport echoed [redacted]") || strings.Contains(err.Error(), fixtureControlToken) {
			t.Fatal("transport error lost its cause or exposed the credential")
		}
	})

	t.Run("historical", func(t *testing.T) {
		f := newFakeFabriq("acme")
		f.controlToken = ""
		admin, err := fabriqloom.NewTestAdminClient(f.channel(), func(string) ([]byte, error) {
			return nil, &deployments.GeneratedSecretNotDeclaredError{Kind: "fabriq", Version: "1.9", Revision: "1", Name: fixtureControlSecret}
		})
		if err != nil {
			t.Fatal(err)
		}
		tenants, err := admin.Tenants(t.Context())
		if err != nil || len(tenants) != 1 {
			t.Fatalf("historical tenants: %d, %v", len(tenants), err)
		}
		if got := f.seen(); len(got) != 1 || len(got[0].Header.Values(fixtureControlHeader)) != 0 {
			t.Fatal("historical request carried a control header")
		}
	})
}
