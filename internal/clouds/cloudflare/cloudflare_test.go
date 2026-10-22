package cloudflare_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/xraph/loomplane/internal/clouds/cloudflare"
	"github.com/xraph/loomplane/pkg/provider"
)

type recorded struct {
	Method, Path, Query, Auth string
	Body                      map[string]any
}

// fixture is a Cloudflare that records every request. Each probe answers
// with the status its field holds.
type fixture struct {
	*httptest.Server
	account, zone  string
	zoneRead       int
	dnsWrite       int
	registrarRead  int
	registrarWrite int
	verifyStatus   string
	noZones        bool

	mu       sync.Mutex
	requests []recorded
	deleted  map[string]bool
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{
		account: strings.Repeat("a", 32), zone: "zone1",
		zoneRead: http.StatusOK, dnsWrite: http.StatusBadRequest,
		registrarRead: http.StatusOK, registrarWrite: http.StatusBadRequest,
		verifyStatus: "active", deleted: map[string]bool{},
	}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Close)
	return f
}

func (f *fixture) reply(w http.ResponseWriter, status int, result any) {
	w.WriteHeader(status)
	if status >= 300 {
		fmt.Fprint(w, `{"success":false,"errors":[{"code":10000,"message":"secret detail tok-secret"}],"result":null}`)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "errors": []any{}, "result": result})
}

func (f *fixture) serve(w http.ResponseWriter, r *http.Request) {
	rec := recorded{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Auth: r.Header.Get("Authorization")}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&rec.Body)
	}
	f.mu.Lock()
	f.requests = append(f.requests, rec)
	f.mu.Unlock()
	acct := "/accounts/" + f.account
	switch {
	case r.Method == http.MethodGet && r.URL.Path == acct+"/tokens/verify":
		f.reply(w, http.StatusOK, map[string]any{"id": "tok1", "status": f.verifyStatus})
	case r.Method == http.MethodGet && r.URL.Path == acct:
		f.reply(w, http.StatusOK, map[string]any{"id": f.account, "name": "Acme"})
	case r.Method == http.MethodGet && r.URL.Path == "/zones":
		zones := []map[string]any{{"id": f.zone, "name": "acme.dev"}}
		if f.noZones {
			zones = nil
		}
		f.reply(w, f.zoneRead, zones)
	case r.Method == http.MethodPost && r.URL.Path == "/zones/"+f.zone+"/dns_records":
		f.reply(w, f.dnsWrite, map[string]any{"id": "rec_probe"})
	case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/zones/"+f.zone+"/dns_records/"):
		f.mu.Lock()
		f.deleted[strings.TrimPrefix(r.URL.Path, "/zones/"+f.zone+"/dns_records/")] = true
		f.mu.Unlock()
		f.reply(w, http.StatusOK, map[string]any{"id": "rec_probe"})
	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/registrations"):
		f.reply(w, f.registrarRead, []any{})
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/registrations"):
		f.reply(w, f.registrarWrite, map[string]any{})
	default:
		http.NotFound(w, r)
	}
}

func (f *fixture) saw(path string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.requests {
		if r.Path == path {
			return true
		}
	}
	return false
}

func TestPermissionsProbeWithoutSideEffects(t *testing.T) {
	f := newFixture(t)
	f.dnsWrite, f.registrarWrite = http.StatusBadRequest, http.StatusForbidden
	p, err := cloudflare.New(f.URL, false).Permissions(t.Context(), "tok", f.account)
	if err != nil || p.ZoneRead != "granted" || p.DNSWrite != "granted" || p.RegistrarWrite != "missing" || p.RegistrarRead != "granted" {
		t.Fatalf("%+v %v", p, err)
	}
	for _, r := range f.requests {
		if r.Method == http.MethodPost && r.Path == "/zones/"+f.zone+"/dns_records" && r.Body["content"] != "permission-probe" {
			t.Fatal("probe could be valid")
		}
		if r.Method == http.MethodPost && strings.HasSuffix(r.Path, "/registrations") && r.Body["domain_name"] != "" {
			t.Fatal("probe named a domain")
		}
		if r.Method != http.MethodGet && r.Method != http.MethodPost {
			t.Fatalf("unexpected %s %s", r.Method, r.Path)
		}
		if r.Auth != "Bearer tok" {
			t.Fatalf("authorization %q", r.Auth)
		}
	}
	if f.deleted["rec_probe"] {
		t.Fatal("deleted a record that was never created")
	}
}

func TestPermissionsProbeTreatsSuccessAsAmbiguous(t *testing.T) {
	f := newFixture(t)
	f.dnsWrite = http.StatusOK // the fixture returns {"result":{"id":"rec_probe"}}
	if _, err := cloudflare.New(f.URL, false).Permissions(t.Context(), "tok", f.account); !errors.Is(err, provider.ErrInfrastructureAmbiguous) {
		t.Fatal(err)
	}
	if !f.deleted["rec_probe"] {
		t.Fatal("accidental probe record kept")
	}
}

func TestPermissionsRegistrarWriteSuccessIsAmbiguous(t *testing.T) {
	f := newFixture(t)
	f.registrarWrite = http.StatusOK
	if _, err := cloudflare.New(f.URL, false).Permissions(t.Context(), "tok", f.account); !errors.Is(err, provider.ErrInfrastructureAmbiguous) {
		t.Fatal(err)
	}
}

func TestPermissionsMissingAndUnchecked(t *testing.T) {
	f := newFixture(t)
	f.zoneRead, f.registrarRead = http.StatusForbidden, http.StatusForbidden
	p, err := cloudflare.New(f.URL, false).Permissions(t.Context(), "tok", f.account)
	if err != nil || p.ZoneRead != "missing" || p.DNSWrite != "unchecked" || p.RegistrarRead != "missing" || p.RegistrarWrite != "granted" {
		t.Fatalf("%+v %v", p, err)
	}

	f = newFixture(t)
	f.noZones = true
	p, err = cloudflare.New(f.URL, false).Permissions(t.Context(), "tok", f.account)
	if err != nil || p.ZoneRead != "granted" || p.DNSWrite != "unchecked" {
		t.Fatalf("no zones: %+v %v", p, err)
	}
}

func TestPermissionsServerErrorIsNotAPermissionAnswer(t *testing.T) {
	f := newFixture(t)
	f.dnsWrite = http.StatusInternalServerError
	if _, err := cloudflare.New(f.URL, false).Permissions(t.Context(), "tok", f.account); err == nil {
		t.Fatal("a 500 was read as a permission answer")
	}
}

func TestSandboxUsesRegistrarSandboxPaths(t *testing.T) {
	f := newFixture(t)
	_, _ = cloudflare.New(f.URL, true).Permissions(t.Context(), "tok", f.account)
	if !f.saw("/accounts/"+f.account+"/registrar-sandbox/registrations") || f.saw("/accounts/"+f.account+"/registrar/registrations") {
		t.Fatal(f.requests)
	}
	f = newFixture(t)
	_, _ = cloudflare.New(f.URL, false).Permissions(t.Context(), "tok", f.account)
	if !f.saw("/accounts/"+f.account+"/registrar/registrations") || f.saw("/accounts/"+f.account+"/registrar-sandbox/registrations") {
		t.Fatal(f.requests)
	}
}

func TestVerifyTokenAndAccount(t *testing.T) {
	f := newFixture(t)
	c := cloudflare.New(f.URL, false)
	info, err := c.VerifyToken(t.Context(), "tok", f.account)
	if err != nil || info.ID != "tok1" || info.Status != "active" {
		t.Fatalf("%+v %v", info, err)
	}
	acct, err := c.Account(t.Context(), "tok", f.account)
	if err != nil || acct.ID != f.account || acct.Name != "Acme" {
		t.Fatalf("%+v %v", acct, err)
	}
	f.verifyStatus = "disabled"
	if _, err := c.VerifyToken(t.Context(), "tok", f.account); !errors.Is(err, cloudflare.ErrRejected) {
		t.Fatalf("inactive token: %v", err)
	}
}

func TestVerifyTokenFallsBackToUserTokens(t *testing.T) {
	var paths []string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.URL.Path != "/user/tokens/verify" {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"success":false,"errors":[{"code":1000,"message":"Invalid API Token"}]}`)
			return
		}
		fmt.Fprint(w, `{"success":true,"errors":[],"result":{"id":"utok","status":"active"}}`)
	}))
	defer api.Close()
	info, err := cloudflare.New(api.URL, false).VerifyToken(t.Context(), "tok", strings.Repeat("b", 32))
	if err != nil || info.ID != "utok" || len(paths) != 2 {
		t.Fatalf("%+v %v %v", info, err, paths)
	}
}

func TestErrorsKeepBodiesAndTokensOut(t *testing.T) {
	f := newFixture(t)
	f.zoneRead = http.StatusUnauthorized
	_, err := cloudflare.New(f.URL, false).Permissions(t.Context(), "tok-secret", f.account)
	if !errors.Is(err, cloudflare.ErrRejected) {
		t.Fatal(err)
	}
	var ae *cloudflare.APIError
	if !errors.As(err, &ae) || ae.Status != http.StatusUnauthorized || len(ae.Codes) != 1 || ae.Codes[0] != 10000 {
		t.Fatalf("%+v", ae)
	}
	if strings.Contains(err.Error(), "tok-secret") || strings.Contains(err.Error(), "secret detail") {
		t.Fatalf("leaked: %v", err)
	}
}

func TestCheckToken(t *testing.T) {
	for _, bad := range []string{"", "has space", strings.Repeat("x", 600)} {
		if cloudflare.CheckToken(bad) == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if err := cloudflare.CheckToken(" abc_DEF-123 \n"); err != nil {
		t.Fatal(err)
	}
}
