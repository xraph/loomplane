package deployments

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/catalogconnect"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/providerresources"
	"github.com/xraph/loomplane/internal/recipes"
	"github.com/xraph/loomplane/internal/recipes/recipestest"
	"github.com/xraph/loomplane/internal/security"
)

func searchDependencyCA(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(21), Subject: pkix.Name{CommonName: "dependency CA"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func searchDependencyFields(a catalogconnect.EngineAccess) map[string]string {
	scheme := "https"
	if a.TLS == catalogconnect.TLSDisable {
		scheme = "http"
	}
	return map[string]string{recipes.FieldURL: scheme + "://search.example:9200", recipes.FieldHost: a.Host, recipes.FieldHostPort: "search.example:9200", recipes.FieldPort: "9200", recipes.FieldUsername: a.Username, recipes.FieldPassword: a.Password, recipes.FieldDatabase: "", recipes.FieldQuery: "", recipes.FieldCACert: a.CACert}
}
func searchDependencyEnv(field string) string { return "SEARCH_" + strings.ToUpper(field) }

func searchDependencyKind(t *testing.T, registry *recipes.Registry) recipes.Kind {
	t.Helper()
	kind, ok := registry.Kind("sample")
	if !ok {
		t.Fatal("sample recipe missing")
	}
	return kind
}

func searchDependencyKinds(t *testing.T, a catalogconnect.EngineAccess) *recipes.Registry {
	t.Helper()
	return sampleKinds(t, func(k *recipes.Kind) {
		k.Dependencies[0].External = &recipes.Requirement{Engine: a.Engine, MinVersion: "3.9", TLS: []string{a.TLS}}
		if a.Engine == catalogconnect.EngineElasticsearch {
			k.Dependencies[0].External.MinVersion = "9.4"
		}
		for i := range k.Components {
			if k.Components[i].Name == "api" {
				k.Components[i].Env = nil
				for _, field := range []string{recipes.FieldURL, recipes.FieldHost, recipes.FieldHostPort, recipes.FieldPort, recipes.FieldUsername, recipes.FieldPassword, recipes.FieldDatabase, recipes.FieldQuery, recipes.FieldCACert} {
					k.Components[i].Env = append(k.Components[i].Env, recipes.EnvVar{Name: searchDependencyEnv(field), From: &recipes.Ref{Dependency: "postgres", Field: field}})
				}
			}
		}
	})
}

type searchDependencyChecker struct {
	t                     *testing.T
	raw                   string
	req                   recipes.Requirement
	checks                int
	fail                  bool
	errorEcho, reportEcho string
}

func (c *searchDependencyChecker) Check(ctx context.Context, org, id string, req recipes.Requirement, dial catalogconnect.DialFunc) (*catalogconnect.Report, error) {
	c.checks++
	if org != "org" || id != "search-endpoint" || req.Engine != c.req.Engine || req.MinVersion != c.req.MinVersion || !slices.Equal(req.TLS, c.req.TLS) || catalogconnect.ConsumerFrom(ctx).Title != "Sample" {
		c.t.Error("ordinary search recheck lost scope or requirement")
	}
	if _, err := dial(ctx, "tcp", "search.example:9200"); err != nil {
		return nil, err
	}
	if c.errorEcho != "" {
		return nil, errors.New(c.errorEcho)
	}
	if c.reportEcho != "" {
		return &catalogconnect.Report{Problems: []string{c.reportEcho}}, nil
	}
	if c.fail {
		return &catalogconnect.Report{Problems: []string{"read permission refused"}}, nil
	}
	return &catalogconnect.Report{Version: map[string]string{"elasticsearch": "9.4.1", "opensearch": "3.9.0"}[req.Engine]}, nil
}
func (c *searchDependencyChecker) CheckDSN(ctx context.Context, raw string, req recipes.Requirement, dial catalogconnect.DialFunc) (*catalogconnect.Report, error) {
	if raw != c.raw {
		c.t.Error("inventory did not probe exact approved envelope")
	}
	return c.Check(ctx, "org", "search-endpoint", req, dial)
}

func searchDependencyFixture(t *testing.T, engine, tlsMode string) (*componentFixture, *Job, catalogconnect.EngineAccess, *searchDependencyChecker) {
	t.Helper()
	a := catalogconnect.EngineAccess{Engine: engine, Host: "search.example", Port: 9200, Username: "app", Password: "dependency-secret:/?", TLS: tlsMode}
	if tlsMode == catalogconnect.TLSVerifyFull {
		a.CACert = searchDependencyCA(t)
	}
	raw, err := catalogconnect.EncodeEngineAccess(a)
	if err != nil {
		t.Fatal(err)
	}
	f := newComponentFixture(t, searchDependencyKinds(t, a))
	kind := searchDependencyKind(t, f.kinds)
	checker := &searchDependencyChecker{t: t, raw: raw, req: *kind.Dependencies[0].External}
	f.target.Endpoints = fakeEndpoints{endpoint: &catalogconnect.Endpoint{ID: "search-endpoint", OrgID: "org", Name: "search", Engine: engine, Host: a.Host, Port: a.Port, Username: a.Username, TLS: tlsMode, Revision: 3}, dsn: raw}
	f.target.Checker = checker
	f.target.endpointDial = func(_ context.Context, org, target string) (catalogconnect.DialFunc, io.Closer, error) {
		if org != "org" || target != "conn" {
			t.Error("wrong deployment target")
		}
		return func(_ context.Context, network, address string) (net.Conn, error) {
			if network != "tcp" || address != "search.example:9200" {
				t.Error("wrong native target")
			}
			return nil, nil
		}, noClose{}, nil
	}
	deps := map[string]planning.DependencyChoice{"postgres": {Mode: planning.DependencyExternal, Kind: engine, External: "search-endpoint"}, "cache": {Mode: planning.DependencyOff}}
	return f, f.componentJob(t, "api", deps), a, checker
}

func TestSearchExternalNormalizedFieldsAndRecheckCache(t *testing.T) {
	for _, tc := range []struct{ engine, tls string }{{catalogconnect.EngineElasticsearch, catalogconnect.TLSDisable}, {catalogconnect.EngineElasticsearch, catalogconnect.TLSVerifyFull}, {catalogconnect.EngineOpenSearch, catalogconnect.TLSVerifyFull}} {
		t.Run(tc.engine+"/"+tc.tls, func(t *testing.T) {
			f, job, a, checker := searchDependencyFixture(t, tc.engine, tc.tls)
			kind := searchDependencyKind(t, f.kinds)
			run, secrets, err := f.target.componentEnv(t.Context(), job, kind)
			if err != nil {
				t.Fatal(err)
			}
			for field, want := range searchDependencyFields(a) {
				key := searchDependencyEnv(field)
				got, exists := run.Env[key]
				if !exists || got != want || !secrets[key] {
					t.Fatalf("normalized field %s missing or not secret", field)
				}
			}
			if checker.checks != 1 {
				t.Fatal("multiple field reads did not share recheck")
			}
			out, err := f.target.Apply(t.Context(), job)
			if err != nil {
				t.Fatal(err)
			}
			spec, _, env := f.started(t, out)
			for field, want := range searchDependencyFields(a) {
				key := searchDependencyEnv(field)
				if env[key] != want || spec.Env[key] != "" {
					t.Fatalf("search field %s entered public runtime spec", field)
				}
			}
		})
	}
}

func TestSearchFailedRecheckPrecedesFieldsSecretsAndRuntime(t *testing.T) {
	for _, engine := range []string{catalogconnect.EngineElasticsearch, catalogconnect.EngineOpenSearch} {
		f, job, _, checker := searchDependencyFixture(t, engine, catalogconnect.TLSVerifyFull)
		checker.fail = true
		run, secrets, err := f.target.componentEnv(t.Context(), job, searchDependencyKind(t, f.kinds))
		if err == nil || len(run.Env) != 0 || len(run.Secrets) != 0 || len(secrets) != 0 || len(f.secrets.m) != 0 {
			t.Fatal("failed recheck delivered material")
		}
		_, err = f.target.Apply(t.Context(), job)
		if err == nil || strings.Contains(err.Error(), "dependency-secret") || f.instances.creates != 0 || f.instances.updates != 0 || f.backend.applied != 0 {
			t.Fatal("failed recheck mutated runtime")
		}
	}
}

func TestSearchExternalPrivateFieldsAndOffDependencies(t *testing.T) {
	f, job, _, checker := searchDependencyFixture(t, catalogconnect.EngineOpenSearch, catalogconnect.TLSVerifyFull)
	kind := searchDependencyKind(t, f.kinds)
	deps := job.Action.Resource.Component.Dependencies
	reads := f.target.reads(job, kind, "app", deps)
	for _, field := range []string{"output:password", "secret:password", "admin-url", "ca_file"} {
		value, on, err := reads.dependency(t.Context(), "postgres", field)
		if err == nil || on || value != "" || strings.Contains(err.Error(), "dependency-secret") {
			t.Fatal("external private field escaped")
		}
	}
	if checker.checks != 1 {
		t.Fatal("recheck was not cached")
	}
	deps["postgres"] = planning.DependencyChoice{Mode: planning.DependencyOff}
	for _, field := range []string{"url", "password", "ca_cert"} {
		value, on, err := reads.dependency(t.Context(), "postgres", field)
		if err != nil || on || value != "" {
			t.Fatal("off dependency delivered a field")
		}
	}
	kind.Components[1].Env = nil
	for field := range searchDependencyFields(catalogconnect.EngineAccess{}) {
		kind.Components[1].Env = append(kind.Components[1].Env, recipes.EnvVar{Name: searchDependencyEnv(field), From: &recipes.Ref{Dependency: "postgres", Field: field}, Requires: "postgres"})
	}
	job.Action.Resource.Component.Component.Env = kind.Components[1].Env
	run, secrets, err := f.target.componentEnv(t.Context(), job, kind)
	if err != nil || len(run.Env) != 0 || len(secrets) != 0 {
		t.Fatal("off dependency supplied environment")
	}
}

func TestSearchManagedDeclaredConnectionTakesPrecedence(t *testing.T) {
	for _, engine := range []string{catalogconnect.EngineElasticsearch, catalogconnect.EngineOpenSearch} {
		service := recipestest.Graph()
		service.Name = engine
		service.Placement = recipes.PlacementServiceProject
		service.Service = &recipes.ServiceDefinition{Connection: map[string]string{}}
		want := searchDependencyFields(catalogconnect.EngineAccess{Host: "search.example", Username: "declared-user", Password: "declared-password", TLS: catalogconnect.TLSVerifyFull, CACert: searchDependencyCA(t)})
		// Outputs deliberately differ from the legacy URL's credentials and host.
		for _, field := range []string{recipes.FieldURL, recipes.FieldHost, recipes.FieldHostPort, recipes.FieldPort, recipes.FieldUsername, recipes.FieldPassword, recipes.FieldCACert} {
			name := "connection-" + strings.ReplaceAll(field, "_", "-")
			service.Outputs = append(service.Outputs, recipes.Output{Name: name, EnvKey: searchDependencyEnv(field), Template: "declared", Secret: field == recipes.FieldPassword})
			service.Service.Connection[field] = name
		}
		consumer := recipestest.Sample()
		consumer.Dependencies[0].Kinds = []string{engine}
		consumer.Dependencies[0].External = nil
		consumer.Components[1].Env = nil
		for field := range service.Service.Connection {
			consumer.Components[1].Env = append(consumer.Components[1].Env, recipes.EnvVar{Name: searchDependencyEnv(field), From: &recipes.Ref{Dependency: "postgres", Field: field}})
		}
		registry := recipes.NewRegistry()
		for _, k := range []recipes.Kind{service, consumer} {
			if err := registry.Register(k); err != nil {
				t.Fatal(err)
			}
		}
		f := newComponentFixture(t, registry)
		f.secrets.m[ResourceKey("org", "env", "linked", "url")] = []byte("https://fallback-user:fallback-password@fallback-host:9443")
		for field, output := range service.Service.Connection {
			f.secrets.m[ResourceKey("org", "env", "linked", "output:"+output)] = []byte(want[field])
		}
		for _, mode := range []string{planning.DependencyNew, planning.DependencyResource} {
			deps := map[string]planning.DependencyChoice{"postgres": {Mode: mode, Kind: engine, Resource: "linked"}, "cache": {Mode: planning.DependencyOff}}
			run, secrets, err := f.target.componentEnv(t.Context(), f.componentJob(t, "api", deps), searchDependencyKind(t, registry))
			if err != nil {
				t.Fatal(err)
			}
			for field := range service.Service.Connection {
				if run.Env[searchDependencyEnv(field)] != want[field] || !secrets[searchDependencyEnv(field)] {
					t.Fatalf("managed %s bypassed declared output %s", mode, field)
				}
			}
		}
	}
}

type searchInventoryResolver struct {
	t             *testing.T
	record        providerresources.ProviderResource
	endpoint      catalogconnect.Endpoint
	raw, scenario string
	calls         int
}

func (r *searchInventoryResolver) VerifyProject(context.Context, security.Principal, string, string, string, int64) (*providerresources.ProviderResource, *catalogconnect.Endpoint, error) {
	return nil, nil, errors.New("must resolve scoped access")
}
func (r *searchInventoryResolver) ResolveProject(_ context.Context, p security.Principal, project, environment, id string, revision int64) (*providerresources.ProviderResource, *catalogconnect.Endpoint, string, error) {
	r.calls++
	if p.OrgID != "org" || p.UserID != "usr" || project != "proj" || environment != "env" || id != "inventory-search" || revision != 7 {
		r.t.Error("inventory search lost principal or project/environment grant")
	}
	record, endpoint := r.record, r.endpoint
	switch r.scenario {
	case "revoked":
		return nil, nil, "", security.ErrForbidden
	case "account":
		record.AccountIdentity = "other"
	case "identity":
		record.ExternalID = "other"
	case "incarnation":
		record.Incarnation = "other"
	case "resource revision":
		record.Revision++
	case "endpoint revision":
		endpoint.Revision++
	case "record endpoint revision":
		record.EndpointRevision++
	case "endpoint identity":
		record.EndpointID = "other"
	case "engine":
		endpoint.Engine = "redis"
	case "plaintext":
		endpoint.TLS = catalogconnect.TLSDisable
	}
	return &record, &endpoint, r.raw, nil
}

func TestSearchInventoryUsesExactScopedAccessAndRetainsTLSBoundary(t *testing.T) {
	for _, engine := range []string{catalogconnect.EngineElasticsearch, catalogconnect.EngineOpenSearch} {
		for _, scenario := range []string{"", "revoked", "account", "identity", "incarnation", "resource revision", "endpoint revision", "record endpoint revision", "endpoint identity", "engine", "plaintext", "read denied"} {
			t.Run(engine+"/"+scenario, func(t *testing.T) {
				f, job, a, checker := searchDependencyFixture(t, engine, catalogconnect.TLSVerifyFull)
				job.Plan.CreatedBy = "usr"
				resolver := &searchInventoryResolver{t: t, scenario: scenario, raw: checker.raw, record: providerresources.ProviderResource{ID: "inventory-search", OrgID: "org", Revision: 7, AccountIdentity: "account", ExternalID: "native", Incarnation: "created", EndpointID: "search-endpoint", EndpointRevision: 3, Kind: engine}, endpoint: catalogconnect.Endpoint{ID: "search-endpoint", OrgID: "org", Revision: 3, Name: "search", Engine: engine, Host: a.Host, Port: 9200, TLS: catalogconnect.TLSVerifyFull}}
				f.target.ExistingResources = resolver
				choice := job.Action.Resource.Component.Dependencies["postgres"]
				choice.Resource = "selected-search"
				choice.ExistingResource = &planning.ExistingResourceConfig{ResourceID: "inventory-search", Revision: 7, EndpointID: "search-endpoint", EndpointRevision: 3, AccountIdentity: "account", ExternalID: "native", Incarnation: "created"}
				job.Action.Resource.Component.Dependencies["postgres"] = choice
				checker.fail = scenario == "read denied"
				run, secrets, err := f.target.componentEnv(t.Context(), job, searchDependencyKind(t, f.kinds))
				if scenario == "" {
					if err != nil || resolver.calls != 1 || checker.checks != 1 {
						t.Fatal("inventory search did not recheck exact access")
					}
					for field, want := range searchDependencyFields(a) {
						if run.Env[searchDependencyEnv(field)] != want || !secrets[searchDependencyEnv(field)] {
							t.Fatal("inventory search field mismatch")
						}
					}
					return
				}
				if err == nil || strings.Contains(err.Error(), a.Password) || len(run.Env) != 0 || len(run.Secrets) != 0 || len(f.secrets.m) != 0 || f.instances.creates != 0 || f.backend.applied != 0 {
					t.Fatal("changed inventory access delivered material")
				}
				if scenario != "read denied" && checker.checks != 0 {
					t.Fatal("inventory pin mismatch still probed")
				}
			})
		}
	}
}

func TestSearchCompositionRedactsInjectedCheckerErrorsAndReports(t *testing.T) {
	for _, engine := range []string{catalogconnect.EngineElasticsearch, catalogconnect.EngineOpenSearch} {
		for _, accessPath := range []string{"ordinary", "inventory"} {
			for _, diagnostic := range []string{"error", "report"} {
				t.Run(engine+"/"+accessPath+"/"+diagnostic, func(t *testing.T) {
					f, job, a, checker := searchDependencyFixture(t, engine, catalogconnect.TLSVerifyFull)
					var echoes []string
					for _, secret := range []string{a.Password, a.CACert} {
						escaped, _ := json.Marshal(secret)
						echoes = append(echoes, secret, url.QueryEscape(secret), url.PathEscape(secret), string(escaped[1:len(escaped)-1]), base64.StdEncoding.EncodeToString([]byte(secret)), base64.RawStdEncoding.EncodeToString([]byte(secret)), base64.URLEncoding.EncodeToString([]byte(secret)), base64.RawURLEncoding.EncodeToString([]byte(secret)))
					}
					echoes = append(echoes, base64.StdEncoding.EncodeToString([]byte(a.Username+":"+a.Password)))
					message := "untrusted checker diagnostic: " + strings.Join(echoes, " | ")
					if diagnostic == "error" {
						checker.errorEcho = message
					} else {
						checker.reportEcho = message
					}
					if accessPath == "inventory" {
						job.Plan.CreatedBy = "usr"
						f.target.ExistingResources = &searchInventoryResolver{t: t, raw: checker.raw, record: providerresources.ProviderResource{ID: "inventory-search", OrgID: "org", Revision: 7, AccountIdentity: "account", ExternalID: "native", Incarnation: "created", EndpointID: "search-endpoint", EndpointRevision: 3, Kind: engine}, endpoint: catalogconnect.Endpoint{ID: "search-endpoint", OrgID: "org", Revision: 3, Name: "search", Engine: engine, Host: a.Host, Port: 9200, TLS: catalogconnect.TLSVerifyFull}}
						choice := job.Action.Resource.Component.Dependencies["postgres"]
						choice.Resource = "selected-search"
						choice.ExistingResource = &planning.ExistingResourceConfig{ResourceID: "inventory-search", Revision: 7, EndpointID: "search-endpoint", EndpointRevision: 3, AccountIdentity: "account", ExternalID: "native", Incarnation: "created"}
						job.Action.Resource.Component.Dependencies["postgres"] = choice
					}
					run, secretFlags, err := f.target.componentEnv(t.Context(), job, searchDependencyKind(t, f.kinds))
					if err == nil || len(run.Env) != 0 || len(run.Secrets) != 0 || len(secretFlags) != 0 || len(f.secrets.m) != 0 {
						t.Fatal("failed search check delivered material")
					}
					for _, echo := range echoes {
						if strings.Contains(err.Error(), echo) {
							t.Fatal("composition error exposed a credential or CA representation")
						}
					}
					_, err = f.target.Apply(t.Context(), job)
					if err == nil || f.instances.creates != 0 || f.instances.updates != 0 || f.backend.applied != 0 {
						t.Fatal("malicious checker failure changed runtime")
					}
					for _, echo := range echoes {
						if strings.Contains(err.Error(), echo) {
							t.Fatal("apply error exposed a credential or CA representation")
						}
					}
				})
			}
		}
	}
}

func TestSearchCompositionSanitizerPreservesLegacyWholeDSNBehavior(t *testing.T) {
	const dsn = "postgres://app:legacy-secret@db.example/main?sslmode=require"
	if got := withoutDSN("refused "+dsn+" after connect", dsn); got != "refused [redacted] after connect" {
		t.Fatal("legacy whole DSN redaction changed")
	}
	if got := withoutDSN("ordinary connection failure", dsn); got != "ordinary connection failure" {
		t.Fatal("legacy diagnostic changed")
	}
}

// The real endpoint service needs only Get for the already-authorized recheck.
type searchRecheckRows struct {
	catalogconnect.Store
	row   catalogconnect.Endpoint
	reads int
	err   error
}

func (s *searchRecheckRows) Get(_ context.Context, org, endpoint string) (*catalogconnect.Endpoint, error) {
	s.reads++
	if s.err != nil {
		return nil, s.err
	}
	if org != s.row.OrgID || endpoint != s.row.ID {
		return nil, catalogconnect.ErrNotFound
	}
	row := s.row
	return &row, nil
}

type searchRecheckSlots struct {
	*memSecrets
	reads []string
}

func (s *searchRecheckSlots) Get(ctx context.Context, key string) ([]byte, error) {
	s.reads = append(s.reads, key)
	return s.memSecrets.Get(ctx, key)
}

type searchRecheckProber func(context.Context, catalogconnect.DialFunc, string, recipes.Requirement) (*catalogconnect.Report, error)

func (p searchRecheckProber) Probe(ctx context.Context, dial catalogconnect.DialFunc, raw string, requirement recipes.Requirement) (*catalogconnect.Report, error) {
	return p(ctx, dial, raw, requirement)
}

type searchRecheckCheckOnly struct{ EndpointChecker }

func TestSearchExternalRecheckUsesExactResolvedMaterial(t *testing.T) {
	for _, tc := range []struct {
		name                string
		acceptA, apply      bool
		closeFails          bool
		checkOnly, notFound bool
		engineOverride      string
	}{
		{name: "accepts and delivers resolved snapshot", acceptA: true},
		{name: "refuses resolved snapshot when only replacement works"},
		{name: "refusal precedes runtime mutation", apply: true},
		{name: "closer failure withholds accepted snapshot", acceptA: true, closeFails: true},
		{name: "checker must support exact material", checkOnly: true},
		{name: "different search engine fails before dialing", engineOverride: catalogconnect.EngineOpenSearch},
		{name: "legacy requirement cannot consume search row", engineOverride: catalogconnect.EnginePostgres},
		{name: "resolver refusal fails before dialing", notFound: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, job, a, _ := searchDependencyFixture(t, catalogconnect.EngineElasticsearch, catalogconnect.TLSVerifyFull)
			kind := searchDependencyKind(t, f.kinds)
			if tc.engineOverride != "" {
				changed := *kind.Dependencies[0].External
				changed.Engine = tc.engineOverride
				kind.Dependencies[0].External = &changed
			}
			b := a
			b.Password = "rotated-fixture-credential:/?"
			rawA, err := catalogconnect.EncodeEngineAccess(a)
			if err != nil {
				t.Fatal("could not encode initial search fixture")
			}
			rawB, err := catalogconnect.EncodeEngineAccess(b)
			if err != nil {
				t.Fatal("could not encode replacement search fixture")
			}
			const endpoint = "search-endpoint"
			const revision = 3
			key := catalogconnect.DSNKey("org", endpoint)
			rows := &searchRecheckRows{row: catalogconnect.Endpoint{ID: endpoint, OrgID: "org", Name: "search", Engine: a.Engine, Host: a.Host, Port: a.Port, Username: a.Username, TLS: a.TLS, Revision: revision}}
			if tc.notFound {
				rows.err = catalogconnect.ErrNotFound
			}
			slots := &searchRecheckSlots{memSecrets: &memSecrets{m: map[string][]byte{key: []byte(rawA)}}}
			service := catalogconnect.NewService(rows, slots, nil)
			var probed []string
			accepted := rawB
			if tc.acceptA {
				accepted = rawA
			}
			checker := &catalogconnect.Checker{Endpoints: service, Probers: map[string]catalogconnect.Prober{
				a.Engine: searchRecheckProber(func(ctx context.Context, dial catalogconnect.DialFunc, raw string, requirement recipes.Requirement) (*catalogconnect.Report, error) {
					probed = append(probed, raw)
					if requirement.Engine != a.Engine || requirement.MinVersion != "9.4" || !slices.Equal(requirement.TLS, []string{a.TLS}) || catalogconnect.ConsumerFrom(ctx).Title != "Sample" {
						t.Error("recheck changed the search requirement or consumer")
					}
					if _, dialErr := dial(ctx, "tcp", "search.example:9200"); dialErr != nil {
						return nil, dialErr
					}
					if raw != accepted {
						return &catalogconnect.Report{Problems: []string{"fixture rejected the search credential"}}, nil
					}
					return &catalogconnect.Report{Version: "9.4.1"}, nil
				}),
			}}
			f.target.Endpoints, f.target.Checker = service, checker
			if tc.checkOnly {
				f.target.Checker = searchRecheckCheckOnly{checker}
			}
			acquired, dialed, closed := 0, 0, 0
			f.target.endpointDial = func(ctx context.Context, org, target string) (catalogconnect.DialFunc, io.Closer, error) {
				acquired++
				if org != "org" || target != "conn" || !slices.Equal(slots.reads, []string{key}) {
					t.Error("recheck did not resolve the scoped slot before target acquisition")
				}
				if putErr := slots.Put(ctx, key, []byte(rawB)); putErr != nil {
					t.Fatal("could not replace the same in-memory Vault slot")
				}
				return func(_ context.Context, network, address string) (net.Conn, error) {
						dialed++
						if network != "tcp" || address != "search.example:9200" {
							t.Error("recheck did not retain the guarded target authority")
						}
						return nil, nil
					}, closerFunc(func() error {
						closed++
						if tc.closeFails {
							return errors.New("fixture closer refused " + a.Password)
						}
						return nil
					}), nil
			}
			if tc.apply {
				_, err = f.target.Apply(t.Context(), job)
				if err == nil || len(f.secrets.m) != 0 || f.instances.creates != 0 || f.instances.updates != 0 || f.backend.applied != 0 {
					t.Error("failed exact-material recheck mutated runtime state")
				}
			} else {
				var run ComponentRun
				var flags map[string]bool
				run, flags, err = f.target.componentEnv(t.Context(), job, kind)
				if tc.acceptA && !tc.closeFails {
					if err != nil {
						t.Error("recheck refused the accepted resolved snapshot")
					} else {
						for field, value := range searchDependencyFields(a) {
							name := searchDependencyEnv(field)
							got, present := run.Env[name]
							if !present || got != value || !flags[name] {
								t.Error("component did not deliver the checked snapshot field as secret material")
							}
						}
					}
				} else if err == nil || len(run.Env) != 0 || len(run.Secrets) != 0 || len(flags) != 0 || len(f.secrets.m) != 0 {
					t.Error("failed exact-material recheck delivered fields or generated secrets")
				}
			}
			if err != nil && (strings.Contains(err.Error(), a.Password) || strings.Contains(err.Error(), b.Password) || strings.Contains(err.Error(), a.CACert)) {
				t.Error("exact-material recheck exposed a credential or CA")
			}
			wantReads, wantProbes := 1, 1
			if tc.checkOnly || tc.engineOverride != "" || tc.notFound {
				wantProbes = 0
			}
			if tc.notFound {
				wantReads = 0
			}
			if rows.reads != 1 || len(slots.reads) != wantReads || rows.row.Revision != revision {
				t.Error("recheck did not use exactly one scoped resolution at the original row revision")
			}
			if len(probed) != wantProbes || (wantProbes == 1 && probed[0] != rawA) {
				t.Error("recheck did not probe exactly the resolved snapshot")
			}
			if acquired != wantProbes || dialed != wantProbes || closed != wantProbes {
				t.Error("recheck did not retain the expected target and close boundary")
			}
			if wantProbes == 1 && string(slots.m[key]) != rawB {
				t.Error("fixture did not rotate the same Vault slot")
			}
		})
	}
}
