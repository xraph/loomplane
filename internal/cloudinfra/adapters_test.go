package cloudinfra_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/clouds/cloudflare"
	"github.com/xraph/loomplane/internal/clouds/digitalocean"
	"github.com/xraph/loomplane/internal/clouds/vercel"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/providers"
	"github.com/xraph/loomplane/pkg/provider"
)

func TestRegistryExposesOnlyImplementedCapabilities(t *testing.T) {
	registry := cloudinfra.BuiltinAdapters(cloudinfra.Endpoints{})
	if !slices.Equal(registry.Providers(), []string{"cloudflare", "digitalocean", "vercel"}) {
		t.Fatal(registry.Providers())
	}
	for _, name := range []string{"vercel", "server", "gcp", "aws", "unknown"} {
		_, err := registry.Server(name)
		var unsupported *provider.UnsupportedCapabilityError
		if !errors.As(err, &unsupported) || unsupported.Provider != name || unsupported.Capability != provider.ServerProvisioning {
			t.Fatalf("unsupported %s: %v", name, err)
		}
	}
	for _, p := range providers.Catalog() {
		if !slices.Equal(p.Infrastructure, registry.Capabilities(p.Cloud)) {
			t.Fatalf("catalog capability drift: %s", p.Name)
		}
		if p.Name == "digitalocean" && (p.Model != provider.Build || p.Title != "DigitalOcean App Platform") {
			t.Fatal("changed App Platform meaning")
		}
	}
	if !slices.Equal(digitalocean.InfrastructureCapabilities(), registry.Capabilities("digitalocean")) || !slices.Equal(vercel.InfrastructureCapabilities(), registry.Capabilities("vercel")) || !slices.Equal(cloudflare.InfrastructureCapabilities(), registry.Capabilities("cloudflare")) {
		t.Fatal("adapter metadata drift")
	}
}
func TestCredentialRequiresRecordedImmutableIdentity(t *testing.T) {
	for _, cloud := range []string{"vercel", "digitalocean", "cloudflare"} {
		connection := &connections.Connection{Cloud: cloud, Account: "mutable name", Status: connections.StatusReady, Details: map[string]string{}}
		if _, err := cloudinfra.Credential(connection, []byte("secret")); !errors.Is(err, provider.ErrInfrastructureIdentity) {
			t.Fatalf("display name established identity: %v", err)
		}
		key, team := "team_id", "immutable-team"
		switch cloud {
		case "digitalocean":
			key = "team_uuid"
		case "cloudflare":
			key, team = "account_id", ""
		}
		connection.Details[key] = "immutable-team"
		credential, err := cloudinfra.Credential(connection, []byte("secret"))
		if err != nil || credential.AccountID != "immutable-team" || credential.TeamID != team {
			t.Fatalf("credential %v", err)
		}
	}
}

// fixtureServer is independent of cloud clients and their HTTP representations.
// The same lifecycle assertions run against this adapter and DigitalOcean.
type fixtureServer struct {
	server *provider.InfrastructureServer
	intent string
	action provider.InfrastructureAction
}

func (*fixtureServer) check(ctx context.Context, credential provider.InfrastructureCredential) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if credential.AccountID != "account" {
		return provider.ErrInfrastructureIdentity
	}
	return nil
}
func (f *fixtureServer) ListServers(ctx context.Context, credential provider.InfrastructureCredential) ([]provider.InfrastructureServer, error) {
	if err := f.check(ctx, credential); err != nil {
		return nil, err
	}
	if f.server == nil {
		return []provider.InfrastructureServer{}, nil
	}
	return []provider.InfrastructureServer{*f.server}, nil
}
func (f *fixtureServer) GetServer(ctx context.Context, credential provider.InfrastructureCredential, id string) (provider.InfrastructureServer, error) {
	if err := f.check(ctx, credential); err != nil {
		return provider.InfrastructureServer{}, err
	}
	if f.server == nil || f.server.ID != id {
		return provider.InfrastructureServer{}, provider.ErrInfrastructureNotFound
	}
	return *f.server, nil
}
func (f *fixtureServer) CreateServer(ctx context.Context, credential provider.InfrastructureCredential, spec provider.ServerSpec) (provider.ServerResult, error) {
	if err := f.check(ctx, credential); err != nil {
		return provider.ServerResult{}, err
	}
	if f.server != nil {
		if f.intent != spec.OperationID || f.server.Name != spec.Name || f.server.Size != spec.Size {
			return provider.ServerResult{}, provider.ErrInfrastructureDrift
		}
		return provider.ServerResult{Server: *f.server, Reconciled: true}, nil
	}
	if spec.ReconcileOnly {
		return provider.ServerResult{}, provider.ErrInfrastructureAmbiguous
	}
	f.intent = spec.OperationID
	f.server = &provider.InfrastructureServer{ID: "42", AccountID: credential.AccountID, Name: spec.Name, Region: spec.Region, Size: spec.Size, Status: "new"}
	f.action = provider.InfrastructureAction{ID: "7", ResourceID: "42", Status: "in-progress", Kind: "create"}
	return provider.ServerResult{Server: *f.server, Actions: []provider.InfrastructureAction{f.action}}, nil
}
func (f *fixtureServer) ResizeServer(ctx context.Context, credential provider.InfrastructureCredential, id, size string) (provider.InfrastructureAction, error) {
	if _, err := f.GetServer(ctx, credential, id); err != nil {
		return provider.InfrastructureAction{}, err
	}
	f.server.Size = size
	f.action = provider.InfrastructureAction{ID: "7", ResourceID: id, Status: "in-progress", Kind: "resize"}
	return f.action, nil
}
func (f *fixtureServer) DeleteServer(ctx context.Context, credential provider.InfrastructureCredential, id string) error {
	if err := f.check(ctx, credential); err != nil {
		return err
	}
	if f.server != nil && f.server.ID == id {
		f.server = nil
	}
	return nil
}
func (f *fixtureServer) GetServerAction(ctx context.Context, credential provider.InfrastructureCredential, id, actionID string) (provider.InfrastructureAction, error) {
	if err := f.check(ctx, credential); err != nil {
		return provider.InfrastructureAction{}, err
	}
	if id != f.action.ResourceID || actionID != f.action.ID {
		return provider.InfrastructureAction{}, provider.ErrInfrastructureNotFound
	}
	action := f.action
	action.Status = "completed"
	return action, nil
}

func digitalOceanConformanceFixture(t *testing.T) (provider.ServerAdapter, provider.InfrastructureCredential) {
	t.Helper()
	var server map[string]any
	actionType := "create"
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in map[string]any
		if r.Method == http.MethodPost {
			_ = json.NewDecoder(r.Body).Decode(&in)
		}
		action := map[string]any{"id": 7, "resource_id": 42, "status": "in-progress", "type": actionType}
		var response any
		switch r.Method + " " + r.URL.Path {
		case "GET /v2/account":
			response = map[string]any{"account": map[string]any{"uuid": "account"}}
		case "GET /v2/droplets":
			list := []any{}
			if server != nil {
				list = append(list, server)
			}
			response = map[string]any{"droplets": list}
		case "POST /v2/droplets":
			server = map[string]any{"id": 42, "name": in["name"], "size_slug": in["size"], "region": map[string]any{"slug": in["region"]}, "tags": in["tags"], "status": "new"}
			response = map[string]any{"droplet": server, "links": map[string]any{"actions": []any{map[string]any{"id": 7, "href": "https://api.digitalocean.com/v2/actions/7", "rel": "create"}}}}
		case "GET /v2/droplets/42":
			if server == nil {
				w.WriteHeader(404)
				fmt.Fprint(w, `{"message":"gone"}`)
				return
			}
			response = map[string]any{"droplet": server}
		case "POST /v2/droplets/42/actions":
			actionType = "resize"
			action["type"] = actionType
			server["size_slug"] = in["size"]
			response = map[string]any{"action": action}
		case "GET /v2/droplets/42/actions":
			response = map[string]any{"actions": []any{map[string]any{"id": 7, "resource_id": 42, "status": "completed", "type": "create"}}}
		case "GET /v2/droplets/42/actions/7":
			action["status"] = "completed"
			response = map[string]any{"action": action}
		case "DELETE /v2/droplets/42":
			server = nil
			w.WriteHeader(204)
			return
		default:
			t.Errorf("unexpected fixture call %s %s", r.Method, r.URL)
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(response)
	}))
	t.Cleanup(api.Close)
	return digitalocean.New(api.URL), provider.InfrastructureCredential{Provider: "digitalocean", AccountID: "account", Token: "fixture"}
}

func TestServerAdapterConformance(t *testing.T) {
	factories := map[string]func(*testing.T) (provider.ServerAdapter, provider.InfrastructureCredential){
		"digitalocean": digitalOceanConformanceFixture,
		"independent-fixture": func(*testing.T) (provider.ServerAdapter, provider.InfrastructureCredential) {
			return &fixtureServer{}, provider.InfrastructureCredential{Provider: "fixture", AccountID: "account", Token: "fixture"}
		},
	}
	for name, factory := range factories {
		t.Run(name, func(t *testing.T) {
			adapter, credential := factory(t)
			ctx := t.Context()
			list, err := adapter.ListServers(ctx, credential)
			if err != nil || len(list) != 0 {
				t.Fatalf("initial list: %+v %v", list, err)
			}
			spec := provider.ServerSpec{OperationID: "intent", Name: "host", Region: "nyc3", Size: "small", Image: "ubuntu-24-04-x64"}
			result, err := adapter.CreateServer(ctx, credential, spec)
			if err != nil || result.Server.ID == "" || result.Server.AccountID != credential.AccountID || result.Server.Status == "active" || len(result.Actions) != 1 || result.Actions[0].ID == "" {
				t.Fatalf("accepted create: %+v %v", result, err)
			}
			id := result.Server.ID
			spec.ReconcileOnly = true
			reconciled, err := adapter.CreateServer(ctx, credential, spec)
			if err != nil || !reconciled.Reconciled || reconciled.Server.ID != id {
				t.Fatalf("reconciliation: %+v %v", reconciled, err)
			}
			list, err = adapter.ListServers(ctx, credential)
			if err != nil || len(list) != 1 {
				t.Fatalf("duplicate create: %+v %v", list, err)
			}
			changed := spec
			changed.Size = "different"
			if _, err = adapter.CreateServer(ctx, credential, changed); !errors.Is(err, provider.ErrInfrastructureDrift) {
				t.Fatalf("intent drift: %v", err)
			}
			wrong := credential
			wrong.AccountID = "other"
			if _, err = adapter.GetServer(ctx, wrong, id); !errors.Is(err, provider.ErrInfrastructureIdentity) {
				t.Fatalf("foreign account: %v", err)
			}
			canceled, cancel := context.WithCancel(ctx)
			cancel()
			if _, err = adapter.ListServers(canceled, credential); !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation: %v", err)
			}
			action, err := adapter.ResizeServer(ctx, credential, id, "larger")
			if err != nil || action.Status != "in-progress" || action.ResourceID != id {
				t.Fatalf("pending resize: %+v %v", action, err)
			}
			action, err = adapter.GetServerAction(ctx, credential, id, action.ID)
			if err != nil || action.Status != "completed" {
				t.Fatalf("poll: %+v %v", action, err)
			}
			if err = adapter.DeleteServer(ctx, credential, id); err != nil {
				t.Fatal(err)
			}
			if err = adapter.DeleteServer(ctx, credential, id); err != nil {
				t.Fatalf("idempotent delete: %v", err)
			}
			if _, err = adapter.GetServer(ctx, credential, id); !errors.Is(err, provider.ErrInfrastructureNotFound) {
				t.Fatalf("deleted server: %v", err)
			}
			if _, err = adapter.CreateServer(ctx, credential, spec); !errors.Is(err, provider.ErrInfrastructureAmbiguous) {
				t.Fatalf("missing attempted intent recreated: %v", err)
			}
		})
	}
}

func TestRegistryChecksIndividualOperations(t *testing.T) {
	registry := cloudinfra.NewAdapters()
	if err := registry.RegisterServer("discovery-only", &fixtureServer{}, provider.ServerDiscovery); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Server("discovery-only", provider.ServerDiscovery); err != nil {
		t.Fatal(err)
	}
	for _, operation := range []provider.InfrastructureCapability{provider.ServerAdoption, provider.ServerProvisioning, provider.ServerResize, provider.ServerDeletion, provider.DNSZoneCreation, provider.ManagedDomainAttachment, provider.IngressManagement, provider.TLSManagement} {
		if _, err := registry.Server("discovery-only", operation); !errors.Is(err, provider.ErrInfrastructureUnsupported) {
			t.Fatalf("advertised unsupported operation %s: %v", operation, err)
		}
	}
	if !slices.Equal(registry.Capabilities("discovery-only"), []provider.InfrastructureCapability{provider.ServerDiscovery}) {
		t.Fatal("discovery-only metadata contains writes")
	}
	builtins := cloudinfra.BuiltinAdapters(cloudinfra.Endpoints{})
	if _, err := builtins.Registrar("cloudflare"); err != nil {
		t.Fatalf("cloudflare registrar missing: %v", err)
	}
	for _, name := range []string{"vercel", "digitalocean"} {
		if _, err := builtins.Registrar(name); !errors.Is(err, provider.ErrInfrastructureUnsupported) {
			t.Fatalf("%s registrar: %v", name, err)
		}
	}
	for _, operation := range []provider.InfrastructureCapability{provider.DNSZoneDiscovery, provider.DNSZoneCreation, provider.DNSRecordDiscovery, provider.DNSRecordUpsert, provider.DNSRecordDeletion} {
		for _, name := range []string{"vercel", "cloudflare"} {
			if _, err := builtins.DNS(name, operation); err != nil {
				t.Fatalf("missing implemented DNS operation %s on %s: %v", operation, name, err)
			}
		}
	}
	for _, operation := range []provider.InfrastructureCapability{provider.ServerAdoption, provider.ManagedDomainAttachment, provider.IngressManagement, provider.TLSManagement} {
		if err := builtins.Require("digitalocean", operation); !errors.Is(err, provider.ErrInfrastructureUnsupported) {
			t.Fatalf("unimplemented capability %s: %v", operation, err)
		}
	}
}

type fixtureRegistrar struct {
	domains map[string]provider.RegisteredDomain
}

func (*fixtureRegistrar) RegistrationExtensions(context.Context, provider.InfrastructureCredential) ([]string, error) {
	return []string{"dev"}, nil
}
func (*fixtureRegistrar) SearchDomains(context.Context, provider.InfrastructureCredential, string, int) ([]provider.DomainOffer, error) {
	return nil, nil
}
func (*fixtureRegistrar) CheckDomains(_ context.Context, _ provider.InfrastructureCredential, names []string) ([]provider.DomainOffer, error) {
	offers := make([]provider.DomainOffer, 0, len(names))
	for _, name := range names {
		offers = append(offers, provider.DomainOffer{Name: name, Registrable: true, Price: &provider.DomainPrice{Currency: "USD", RegistrationCost: "10.11", RenewalCost: "10.11"}})
	}
	return offers, nil
}
func (r *fixtureRegistrar) RegisterDomain(_ context.Context, _ provider.InfrastructureCredential, spec provider.DomainRegistrationSpec) (provider.DomainRegistrationResult, error) {
	if r.domains == nil {
		r.domains = map[string]provider.RegisteredDomain{}
	}
	r.domains[spec.Name] = provider.RegisteredDomain{Name: spec.Name, Status: "active", AutoRenew: spec.AutoRenew, PrivacyMode: spec.PrivacyMode}
	return provider.DomainRegistrationResult{Name: spec.Name, State: provider.RegistrationSucceeded, Completed: true, AutoRenew: spec.AutoRenew, PrivacyMode: spec.PrivacyMode}, nil
}
func (r *fixtureRegistrar) RegistrationStatus(_ context.Context, _ provider.InfrastructureCredential, name string) (provider.DomainRegistrationResult, error) {
	if _, ok := r.domains[name]; !ok {
		return provider.DomainRegistrationResult{}, provider.ErrInfrastructureNotFound
	}
	return provider.DomainRegistrationResult{Name: name, State: provider.RegistrationSucceeded, Completed: true}, nil
}
func (r *fixtureRegistrar) GetRegisteredDomain(_ context.Context, _ provider.InfrastructureCredential, name string) (provider.RegisteredDomain, error) {
	domain, ok := r.domains[name]
	if !ok {
		return provider.RegisteredDomain{}, provider.ErrInfrastructureNotFound
	}
	return domain, nil
}
func (r *fixtureRegistrar) SetAutoRenew(ctx context.Context, cred provider.InfrastructureCredential, name string, enabled bool) (provider.RegisteredDomain, error) {
	domain, err := r.GetRegisteredDomain(ctx, cred, name)
	if err != nil {
		return provider.RegisteredDomain{}, err
	}
	domain.AutoRenew = enabled
	r.domains[name] = domain
	return domain, nil
}

func TestRegistryRegistrarIsSeparateCapability(t *testing.T) {
	r := cloudinfra.NewAdapters()
	if _, err := r.Registrar("cloudflare"); !errors.Is(err, provider.ErrInfrastructureUnsupported) {
		t.Fatal(err)
	}
	if err := r.RegisterRegistrar("fixture", &fixtureRegistrar{}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Registrar("fixture"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(r.Capabilities("fixture"), []provider.InfrastructureCapability{provider.DomainRegistration}) {
		t.Fatal(r.Capabilities("fixture"))
	}
	if !slices.Equal(r.Providers(), []string{"fixture"}) {
		t.Fatal(r.Providers())
	}
	if err := r.RegisterRegistrar("fixture", &fixtureRegistrar{}); err == nil {
		t.Fatal("duplicate registrar")
	}
	if err := r.RegisterRegistrar("", &fixtureRegistrar{}); err == nil {
		t.Fatal("empty registrar name")
	}
}

func reply(w http.ResponseWriter, body any) { _ = json.NewEncoder(w).Encode(body) }

type vercelRecord struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Type       string `json:"type"`
	RecordType string `json:"recordType,omitempty"`
	Value      string `json:"value"`
	TTL        int    `json:"ttl"`
	Comment    string `json:"comment"`
	Domain     string `json:"domain,omitempty"`
}

func vercelDNSFixture(t *testing.T) (provider.DNSAdapter, provider.InfrastructureCredential, provider.DNSZone) {
	t.Helper()
	var records []vercelRecord
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		decode := func() vercelRecord {
			var in vercelRecord
			_ = json.NewDecoder(r.Body).Decode(&in)
			return in
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /v2/teams/team_a":
			reply(w, map[string]any{"id": "team_a", "slug": "renamed"})
		case "GET /v5/domains/example.test":
			reply(w, map[string]any{"domain": map[string]any{"id": "zone_a", "name": "example.test", "teamId": "team_a", "verified": true, "serviceType": "zeit.world"}})
		case "GET /v5/domains/example.test/records":
			reply(w, map[string]any{"records": records, "pagination": map[string]any{"next": nil}})
		case "POST /v2/domains/example.test/records":
			in := decode()
			in.ID = "rec_a"
			records = append(records, in)
			reply(w, map[string]any{"uid": in.ID})
		case "PATCH /v1/domains/records/rec_a":
			in := decode()
			in.ID, in.Domain, in.RecordType, in.Type = "rec_a", "example.test", in.Type, "record"
			records = []vercelRecord{in}
			reply(w, in)
		case "DELETE /v2/domains/example.test/records/rec_a":
			records = nil
		default:
			t.Errorf("unexpected fixture call %s %s", r.Method, r.URL)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(api.Close)
	return vercel.New(api.URL), provider.InfrastructureCredential{Provider: "vercel", AccountID: "team_a", TeamID: "team_a", Token: "fixture-token"}, provider.DNSZone{ID: "zone_a", Name: "example.test", AccountID: "team_a", Verified: true}
}

type digitalOceanRecord struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
	Data string `json:"data"`
	TTL  int    `json:"ttl"`
}

func digitalOceanDNSFixture(t *testing.T) (provider.DNSAdapter, provider.InfrastructureCredential, provider.DNSZone) {
	t.Helper()
	var records []digitalOceanRecord
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		decode := func() digitalOceanRecord {
			var in digitalOceanRecord
			_ = json.NewDecoder(r.Body).Decode(&in)
			return in
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /v2/account":
			reply(w, map[string]any{"account": map[string]any{"uuid": "account"}})
		case "GET /v2/domains/example.test":
			reply(w, map[string]any{"domain": map[string]any{"name": "example.test"}})
		case "GET /v2/domains/example.test/records":
			reply(w, map[string]any{"domain_records": records, "links": map[string]any{}})
		case "POST /v2/domains/example.test/records":
			in := decode()
			in.ID = 7
			records = append(records, in)
			reply(w, map[string]any{"domain_record": in})
		case "PUT /v2/domains/example.test/records/7":
			in := decode()
			in.ID = 7
			records = []digitalOceanRecord{in}
			reply(w, map[string]any{"domain_record": in})
		case "DELETE /v2/domains/example.test/records/7":
			records = nil
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected fixture call %s %s", r.Method, r.URL)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(api.Close)
	return digitalocean.New(api.URL), provider.InfrastructureCredential{Provider: "digitalocean", AccountID: "account", Token: "fixture"}, provider.DNSZone{ID: "example.test", Name: "example.test", AccountID: "account", Verified: true}
}

type cloudflareRecord struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Type    string `json:"type"`
	Content string `json:"content"`
	TTL     int    `json:"ttl"`
	Proxied bool   `json:"proxied"`
	Comment string `json:"comment"`
}

func cloudflareDNSFixture(t *testing.T) (provider.DNSAdapter, provider.InfrastructureCredential, provider.DNSZone) {
	t.Helper()
	var records []cloudflareRecord
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ok := func(result any) {
			reply(w, map[string]any{"success": true, "errors": []any{}, "result": result, "result_info": map[string]any{"page": 1, "total_pages": 1}})
		}
		decode := func() cloudflareRecord {
			var in cloudflareRecord
			_ = json.NewDecoder(r.Body).Decode(&in)
			return in
		}
		const base = "/zones/zone1/dns_records"
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/zones/zone1":
			ok(map[string]any{"id": "zone1", "name": "example.test", "status": "active", "account": map[string]any{"id": "acct1"}})
		case r.Method == http.MethodGet && r.URL.Path == base:
			ok(records)
		case r.Method == http.MethodPost && r.URL.Path == base:
			in := decode()
			in.ID = "rec" + strconv.Itoa(len(records)+1)
			records = append(records, in)
			ok(in)
		case r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, base+"/"):
			in := decode()
			in.ID = strings.TrimPrefix(r.URL.Path, base+"/")
			records = []cloudflareRecord{in}
			ok(in)
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, base+"/"):
			records = nil
			ok(map[string]any{"id": strings.TrimPrefix(r.URL.Path, base+"/")})
		default:
			t.Errorf("unexpected fixture call %s %s", r.Method, r.URL)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(api.Close)
	return cloudflare.New(api.URL, false), provider.InfrastructureCredential{Provider: "cloudflare", AccountID: "acct1", Token: "fixture-token"}, provider.DNSZone{ID: "zone1", Name: "example.test", AccountID: "acct1", Verified: true}
}

// TestDNSAdapterConformance runs one record lifecycle against each real
// adapter's HTTP fixture.
func TestDNSAdapterConformance(t *testing.T) {
	factories := map[string]func(*testing.T) (provider.DNSAdapter, provider.InfrastructureCredential, provider.DNSZone){
		"vercel": vercelDNSFixture, "digitalocean": digitalOceanDNSFixture, "cloudflare": cloudflareDNSFixture,
	}
	for name, factory := range factories {
		t.Run(name, func(t *testing.T) {
			adapter, cred, zone := factory(t)
			owned := false
			if o, ok := adapter.(provider.DNSRecordOwnership); ok {
				owned = o.SupportsDNSRecordOwnership()
			}
			record := provider.DNSRecord{ZoneID: zone.ID, Name: "app", Type: "A", Value: "192.0.2.1", TTL: 60}
			if owned {
				record.Owner = "loomplane:route:route_1"
			}
			created, err := adapter.UpsertRecord(t.Context(), cred, provider.DNSUpsertRequest{Zone: zone, Record: record})
			if err != nil || created.ID == "" {
				t.Fatalf("create: %+v %v", created, err)
			}
			if owned {
				again, err := adapter.UpsertRecord(t.Context(), cred, provider.DNSUpsertRequest{Zone: zone, Record: record, ReconcileOnly: true})
				if err != nil || again != created {
					t.Fatalf("reconcile: %+v %v", again, err)
				}
			}
			desired := created
			desired.Value = "192.0.2.2"
			updated, err := adapter.UpsertRecord(t.Context(), cred, provider.DNSUpsertRequest{Zone: zone, Record: desired, Expected: &created})
			if err != nil || updated.Value != "192.0.2.2" {
				t.Fatalf("update: %+v %v", updated, err)
			}
			if err = adapter.DeleteRecord(t.Context(), cred, provider.DNSDeleteRequest{Zone: zone, Expected: created}); !errors.Is(err, provider.ErrInfrastructureDrift) && created.ID == updated.ID {
				t.Fatalf("stale delete: %v", err)
			}
			if err = adapter.DeleteRecord(t.Context(), cred, provider.DNSDeleteRequest{Zone: zone, Expected: updated}); err != nil {
				t.Fatal(err)
			}
			if err = adapter.DeleteRecord(t.Context(), cred, provider.DNSDeleteRequest{Zone: zone, Expected: updated}); err != nil {
				t.Fatalf("idempotent delete: %v", err)
			}
			wrong := cred
			wrong.AccountID = "other"
			if _, err = adapter.ListRecords(t.Context(), wrong, zone); !errors.Is(err, provider.ErrInfrastructureIdentity) {
				t.Fatalf("foreign account: %v", err)
			}
			canceled, cancel := context.WithCancel(t.Context())
			cancel()
			if _, err = adapter.ListRecords(canceled, cred, zone); !errors.Is(err, context.Canceled) {
				t.Fatalf("cancel: %v", err)
			}
		})
	}
}
