package planning

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/internal/catalogconnect"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/recipes"
	"github.com/xraph/loomplane/internal/recipes/recipestest"
	"github.com/xraph/loomplane/internal/security"
)

func TestProductionEndpointCheckerIncludesSearchAndExistingEngines(t *testing.T) {
	checker := newEndpointChecker(nil)
	for engine, want := range map[string]catalogconnect.Prober{
		"postgres": catalogconnect.PostgresProber{}, "mysql": catalogconnect.MySQLProber{}, "redis": catalogconnect.RedisProber{},
		"elasticsearch": catalogconnect.SearchProber{}, "opensearch": catalogconnect.SearchProber{},
		"s3": catalogconnect.BucketProber{}, "gcs": catalogconnect.BucketProber{}, "r2": catalogconnect.BucketProber{},
		"cloudflare-d1": catalogconnect.APIProber{}, "cloudflare-kv": catalogconnect.APIProber{}, "cloudflare-r2": catalogconnect.APIProber{}, "cloudflare-queue": catalogconnect.APIProber{}, "cloudflare-vectorize": catalogconnect.APIProber{}, "cloudflare-hyperdrive": catalogconnect.APIProber{}, "cloudflare-durable-object": catalogconnect.APIProber{}, "vercel-blob": catalogconnect.APIProber{}, "vercel-global-config": catalogconnect.APIProber{}, "vercel-integration": catalogconnect.APIProber{},
	} {
		if fmt.Sprintf("%T", checker.Probers[engine]) != fmt.Sprintf("%T", want) {
			t.Fatalf("production prober missing for %s", engine)
		}
	}
	if checker.Probers["falkordb"] != nil || checker.Probers["duckdb"] != nil {
		t.Fatal("unqualified engine enabled")
	}
}

type searchRouteAuthz struct{}

func (searchRouteAuthz) Require(_ context.Context, p security.Principal, perm security.Permission, _ string) error {
	for _, role := range security.Catalog() {
		if role.Slug == p.UserID && slices.Contains(role.Permissions, perm) {
			return nil
		}
	}
	return security.ErrForbidden
}

type searchRouteSecrets struct {
	endpointSecrets
	puts, gets, deletes int
}

func (s *searchRouteSecrets) Put(ctx context.Context, key string, value []byte) error {
	s.puts++
	return s.endpointSecrets.Put(ctx, key, value)
}
func (s *searchRouteSecrets) Get(ctx context.Context, key string) ([]byte, error) {
	s.gets++
	return s.endpointSecrets.Get(ctx, key)
}
func (s *searchRouteSecrets) Delete(ctx context.Context, key string) error {
	s.deletes++
	return s.endpointSecrets.Delete(ctx, key)
}

type searchRouteDialer struct {
	address       string
	calls, closed atomic.Int32
}

func (d *searchRouteDialer) TargetCloud(context.Context, string, string) (string, error) {
	return connections.Server, nil
}
func (d *searchRouteDialer) TargetDial(_ context.Context, org, target string) (catalogconnect.DialFunc, io.Closer, error) {
	if org != "org" || target != "search-target" {
		return nil, nil, connections.ErrNotFound
	}
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		d.calls.Add(1)
		if network != "tcp" || address != "search.example:9200" {
			return nil, errors.New("unexpected target")
		}
		return (&net.Dialer{}).DialContext(ctx, "tcp", d.address)
	}, closer(func() error { d.closed.Add(1); return nil }), nil
}

func searchRouteTLS(t *testing.T) (tls.Certificate, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ca := &x509.Certificate{SerialNumber: big.NewInt(41), Subject: pkix.Name{CommonName: "route CA"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, ca, ca, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf := &x509.Certificate{SerialNumber: big.NewInt(42), DNSNames: []string{"search.example"}, NotBefore: ca.NotBefore, NotAfter: ca.NotAfter, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	signed, err := x509.CreateCertificate(rand.Reader, leaf, ca, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{signed, der}, PrivateKey: key}, string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func TestSearchEndpointActualRoutesAuthorizeAuditAndLimit(t *testing.T) {
	for _, engine := range []string{catalogconnect.EngineElasticsearch, catalogconnect.EngineOpenSearch} {
		t.Run(engine, func(t *testing.T) {
			const password = "route-secret:/?\""
			var phase atomic.Int32
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.Host != "search.example:9200" {
					w.WriteHeader(400)
					return
				}
				// The root stays public, so only the native identity and read prove access.
				if r.URL.Path != "/" {
					u, p, ok := r.BasicAuth()
					if !ok || u != "app" || p != password || phase.Load() == 1 {
						w.WriteHeader(401)
						_, _ = io.WriteString(w, password)
						return
					}
				}
				if r.URL.Path == "/_cluster/health" && phase.Load() == 2 {
					w.WriteHeader(403)
					_, _ = io.WriteString(w, password)
					return
				}
				switch r.URL.Path {
				case "/":
					if engine == catalogconnect.EngineElasticsearch {
						w.Header().Set("X-Elastic-Product", "Elasticsearch")
						_, _ = io.WriteString(w, `{"version":{"number":"9.4.1","build_flavor":"default"},"tagline":"You Know, for Search"}`)
					} else {
						_, _ = io.WriteString(w, `{"version":{"number":"3.9.0","distribution":"opensearch"},"tagline":"The OpenSearch Project: https://opensearch.org/"}`)
					}
				case "/_security/_authenticate":
					_, _ = io.WriteString(w, `{"username":"app","authentication_type":"realm"}`)
				case "/_plugins/_security/authinfo":
					_, _ = io.WriteString(w, `{"user_name":"app"}`)
				case "/_cluster/health":
					_, _ = io.WriteString(w, `{"status":"yellow","timed_out":false,"number_of_nodes":1}`)
				default:
					w.WriteHeader(404)
				}
			})
			access := catalogconnect.EngineAccess{Engine: engine, Host: "search.example", Username: "app", Password: password, TLS: catalogconnect.TLSDisable}
			server := httptest.NewUnstartedServer(handler)
			if engine == catalogconnect.EngineOpenSearch {
				cert, ca := searchRouteTLS(t)
				access.TLS, access.CACert = catalogconnect.TLSVerifyFull, ca
				server.TLS = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}}
				server.StartTLS()
			} else {
				server.Start()
			}
			defer server.Close()
			raw, err := catalogconnect.EncodeEngineAccess(access)
			if err != nil {
				t.Fatal(err)
			}
			kinds := recipes.NewRegistry()
			kind := recipestest.Sample()
			kind.Name = "search-consumer"
			kind.Dependencies[0].External = &recipes.Requirement{Engine: engine, TLS: []string{access.TLS}}
			if err = kinds.Register(kind); err != nil {
				t.Fatal(err)
			}
			store := &endpointStore{rows: map[string]catalogconnect.Endpoint{}, inUse: map[string]bool{}}
			secrets := &searchRouteSecrets{endpointSecrets: endpointSecrets{}}
			audit := &endpointAudit{}
			svc := catalogconnect.NewService(store, secrets, searchRouteAuthz{})
			svc.Audit = audit
			dialer := &searchRouteDialer{address: server.Listener.Addr().String()}
			ext := &Extension{endpoints: svc, endpointChecks: newEndpointChecker(svc), endpointDialer: dialer, catalogKinds: kinds, checkLimit: newCheckLimiter(100, 100)}
			router := forge.NewRouter()
			if err = ext.routes(router, nil); err != nil {
				t.Fatal(err)
			}
			send := func(method, path, user string, body any) *httptest.ResponseRecorder {
				t.Helper()
				encoded, marshalErr := json.Marshal(body)
				if marshalErr != nil {
					t.Fatal(marshalErr)
				}
				req := httptest.NewRequestWithContext(t.Context(), method, "/api/v1/catalog/endpoints"+path, strings.NewReader(string(encoded)))
				req.Header.Set("Content-Type", "application/json")
				if user != "" {
					req = req.WithContext(security.WithPrincipal(req.Context(), security.Principal{OrgID: "org", UserID: user}))
				}
				out := httptest.NewRecorder()
				router.ServeHTTP(out, req)
				if strings.Contains(out.Body.String(), "route-secret") || strings.Contains(out.Body.String(), "BEGIN CERTIFICATE") || strings.Contains(out.Body.String(), `\"password\"`) {
					t.Fatal("response leaked access")
				}
				return out
			}
			check := catalogconnect.CheckInput{Engine: engine, DSN: raw, Target: "search-target", Kind: kind.Name, Dependency: "postgres"}
			result := func(out *httptest.ResponseRecorder) catalogconnect.CheckResult {
				t.Helper()
				var got catalogconnect.CheckResult
				if out.Code != 200 || json.Unmarshal(out.Body.Bytes(), &got) != nil {
					t.Fatalf("check returned %d", out.Code)
				}
				return got
			}
			if got := result(send("POST", "/checks", security.RoleAdmin, check)); !got.OK || dialer.calls.Load() != 3 || dialer.closed.Load() != 1 {
				t.Fatal("production search route failed")
			}
			for _, p := range []int32{1, 2} {
				phase.Store(p)
				if got := result(send("POST", "/checks", security.RoleAdmin, check)); got.OK {
					t.Fatal("native denial accepted")
				}
			}
			phase.Store(0)
			for _, tc := range []struct {
				name, user string
				change     func(*catalogconnect.CheckInput)
				status     int
				outcome    string
			}{
				{"anonymous", "", func(*catalogconnect.CheckInput) {}, 401, ""},
				{"denied", security.RoleViewer, func(*catalogconnect.CheckInput) {}, 403, catalogconnect.OutcomeDenied},
				{"bad envelope", security.RoleAdmin, func(c *catalogconnect.CheckInput) {
					c.DSN = `{"engine":"` + engine + `","password":"` + password + `"}`
				}, 422, catalogconnect.OutcomeInvalid},
				{"engine mismatch", security.RoleAdmin, func(c *catalogconnect.CheckInput) { c.Engine = "redis" }, 200, catalogconnect.OutcomeFailure},
				{"target missing", security.RoleAdmin, func(c *catalogconnect.CheckInput) { c.Target = "" }, 422, catalogconnect.OutcomeInvalid},
				{"target unknown", security.RoleAdmin, func(c *catalogconnect.CheckInput) { c.Target = "unknown" }, 404, catalogconnect.OutcomeFailure},
				{"kind missing", security.RoleAdmin, func(c *catalogconnect.CheckInput) { c.Kind = "" }, 422, catalogconnect.OutcomeInvalid},
				{"dependency missing", security.RoleAdmin, func(c *catalogconnect.CheckInput) { c.Dependency = "" }, 422, catalogconnect.OutcomeInvalid},
				{"external unsupported", security.RoleAdmin, func(c *catalogconnect.CheckInput) { c.Dependency = "cache" }, 422, catalogconnect.OutcomeInvalid},
			} {
				c := check
				tc.change(&c)
				before := dialer.calls.Load()
				events := len(audit.events)
				out := send("POST", "/checks", tc.user, c)
				if out.Code != tc.status || dialer.calls.Load() != before {
					t.Fatalf("%s did not reject before probing: %d", tc.name, out.Code)
				}
				if tc.outcome != "" && (len(audit.events) != events+1 || audit.events[events].Outcome != tc.outcome) {
					t.Fatalf("%s audit mismatch", tc.name)
				}
				if tc.name == "bad envelope" && audit.events[events].Address != "" {
					t.Fatal("invalid access audited an address")
				}
			}
			create := catalogconnect.CreateInput{Name: "checked-search", Engine: engine, DSN: raw}
			for _, user := range []string{"", security.RoleViewer} {
				before := len(audit.events)
				vaultCalls, probes := secrets.puts+secrets.gets+secrets.deletes, dialer.calls.Load()
				out := send("POST", "", user, create)
				if out.Code != 401 && out.Code != 403 || len(secrets.endpointSecrets) != 0 || len(store.rows) != 0 || secrets.puts+secrets.gets+secrets.deletes != vaultCalls || dialer.calls.Load() != probes {
					t.Fatal("unauthorized create touched Vault")
				}
				if user == security.RoleViewer && (len(audit.events) != before+1 || audit.events[before].Outcome != catalogconnect.OutcomeDenied) {
					t.Fatal("denied create audit missing")
				}
			}
			out := send("POST", "", security.RoleAdmin, create)
			var saved catalogconnect.Endpoint
			if out.Code != 201 || json.Unmarshal(out.Body.Bytes(), &saved) != nil || string(secrets.endpointSecrets[catalogconnect.DSNKey("org", saved.ID)]) != raw {
				t.Fatal("search endpoint not stored in Vault")
			}
			if _, _, err = svc.Resolve(t.Context(), "org", saved.ID); err != nil {
				t.Fatal(err)
			}
			if out = send("GET", "", security.RoleViewer, nil); out.Code != 200 || !strings.Contains(out.Body.String(), saved.ID) {
				t.Fatal("authorized list failed")
			}
			vaultCalls, probes := secrets.puts+secrets.gets+secrets.deletes, dialer.calls.Load()
			if out = send("GET", "", "nobody", nil); out.Code != 403 {
				t.Fatal("unauthorized list accepted")
			}
			if out = send("DELETE", "/"+saved.ID, security.RoleViewer, nil); out.Code != 403 || len(secrets.endpointSecrets) != 1 || secrets.puts+secrets.gets+secrets.deletes != vaultCalls || dialer.calls.Load() != probes {
				t.Fatal("denied delete touched Vault")
			}
			if out = send("DELETE", "/"+saved.ID, security.RoleAdmin, nil); out.Code != 204 || len(secrets.endpointSecrets) != 0 || len(store.rows) != 0 {
				t.Fatal("delete did not clear row and Vault")
			}
			ext.checkLimit = newCheckLimiter(1, 1)
			before := dialer.calls.Load()
			events := len(audit.events)
			if !result(send("POST", "/checks", security.RoleAdmin, check)).OK {
				t.Fatal("first limited check failed")
			}
			for range 2 {
				if send("POST", "/checks", security.RoleAdmin, check).Code != 429 {
					t.Fatal("rate limit missing")
				}
			}
			if dialer.calls.Load() != before+3 || len(audit.events) != events+2 || audit.events[len(audit.events)-1].Outcome != catalogconnect.OutcomeRateLimited {
				t.Fatal("rate refusal probed or repeated audit")
			}
			encoded, _ := json.Marshal(audit.events)
			if strings.Contains(string(encoded), "route-secret") || strings.Contains(string(encoded), "BEGIN CERTIFICATE") || strings.Contains(string(encoded), `\"engine\"`) {
				t.Fatal("audit leaked envelope")
			}
			for _, event := range audit.events {
				if event.Engine != engine && event.Engine != "redis" && event.Engine != "" {
					t.Fatal("audit engine changed")
				}
				if event.Address != "" && event.Address != "search.example:9200" {
					t.Fatal("audit contains more than authority")
				}
				if event.Action == catalogconnect.AuditCheck && event.Target != "" && event.Target != "search-target" && event.Target != "unknown" {
					t.Fatal("audit target mismatch")
				}
			}
		})
	}
}
