package platformcloud

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/extensions/identity"
	"github.com/xraph/loomplane/internal/instance"
	"github.com/xraph/loomplane/internal/platformcloud"
	"github.com/xraph/loomplane/internal/security"
)

const testCert = "-----BEGIN CERTIFICATE-----\nMIIBplatformclientcert\n-----END CERTIFICATE-----\n"

// fakeControl is the control plane in memory, as far as the routes go.
type fakeControl struct {
	mu        sync.Mutex
	hosts     []platformcloud.Host
	placed    []platformcloud.Placement
	suspended []string
	tenants   map[string]*platformcloud.Tenant
	secrets   []string
}

func newFakeControl() *fakeControl {
	return &fakeControl{
		tenants: map[string]*platformcloud.Tenant{"org_2": {OrgID: "org_2", Status: "enabled", SuspendReasons: []platformcloud.SuspendReason{}}},
		hosts: []platformcloud.Host{{ID: "host_1", RegionID: "reg_1", Endpoint: "https://10.0.0.5:9443", HostID: "hw-1", Status: "ready",
			Evidence: platformcloud.Evidence{Reason: "reached https://10.0.0.5:9443"}}},
		placed: []platformcloud.Placement{{ID: "pl_1", OrgID: "org_2", Resource: "web", Kind: "service", State: "running", InstanceID: "inst_1"}},
	}
}

func (f *fakeControl) Regions(context.Context) ([]platformcloud.Region, error) {
	return []platformcloud.Region{{ID: "reg_1", Name: "eu-west", DisplayName: "EU West", Status: "active", Revision: 1}}, nil
}
func (f *fakeControl) CreateRegion(_ context.Context, name, display, status string) (*platformcloud.Region, error) {
	return &platformcloud.Region{ID: "reg_2", Name: name, DisplayName: display, Status: "active", Revision: 1}, nil
}
func (f *fakeControl) UpdateRegion(_ context.Context, region string, _ platformcloud.RegionUpdate) (*platformcloud.Region, error) {
	return &platformcloud.Region{ID: region, Name: "eu-west", Status: "closed", Revision: 2}, nil
}
func (f *fakeControl) Hosts(context.Context) ([]platformcloud.HostView, error) {
	out := []platformcloud.HostView{}
	for _, h := range f.hosts {
		out = append(out, platformcloud.HostView{Host: h, Placements: 1})
	}
	return out, nil
}
func (f *fakeControl) HostPlacements(context.Context, string) ([]platformcloud.Placement, error) {
	return f.placed, nil
}
func (f *fakeControl) AddHost(_ context.Context, _ string, in platformcloud.HostInput) (*platformcloud.NewHost, error) {
	h := platformcloud.Host{ID: "host_2", RegionID: in.RegionID, Endpoint: in.Endpoint, HostID: in.HostID, Status: "pending", ClientFingerprint: "ab12"}
	f.hosts = append(f.hosts, h)
	return &platformcloud.NewHost{Host: h, ClientCertificate: testCert, ClientFingerprint: "ab12"}, nil
}
func (f *fakeControl) host(id string) (*platformcloud.Host, error) {
	for i := range f.hosts {
		if f.hosts[i].ID == id {
			return &f.hosts[i], nil
		}
	}
	return nil, platformcloud.ErrNotFound
}
func (f *fakeControl) CheckHost(_ context.Context, _, id string) (*platformcloud.Host, error) {
	return f.host(id)
}
func (f *fakeControl) DrainHost(_ context.Context, _, id string) (*platformcloud.Host, error) {
	h, err := f.host(id)
	if err == nil {
		h.Status = "draining"
	}
	return h, err
}
func (f *fakeControl) RetireHost(_ context.Context, _, id string) error {
	_, err := f.host(id)
	return err
}
func (f *fakeControl) SetAllotment(_ context.Context, _, id string, a platformcloud.Allotment) (*platformcloud.Host, error) {
	h, err := f.host(id)
	if err == nil {
		h.Allotment = a
	}
	return h, err
}
func (f *fakeControl) Tenants(context.Context) ([]platformcloud.TenantView, error) {
	return []platformcloud.TenantView{{OrgID: "org_2", Tenant: f.tenants["org_2"]}}, nil
}
func (f *fakeControl) Tenant(_ context.Context, org string) (*platformcloud.Tenant, error) {
	if t := f.tenants[org]; t != nil {
		return t, nil
	}
	return nil, platformcloud.ErrNotFound
}
func (f *fakeControl) SetTenant(_ context.Context, org, status string, limits *platformcloud.Limits) (*platformcloud.Tenant, error) {
	t := &platformcloud.Tenant{OrgID: org, Status: status, Limits: limits, SuspendReasons: []platformcloud.SuspendReason{}}
	f.tenants[org] = t
	return t, nil
}
func (f *fakeControl) EnableTenant(_ context.Context, org string, _ *platformcloud.Limits) (*platformcloud.Tenant, error) {
	return f.tenants[org], nil
}
func (f *fakeControl) Suspend(_ context.Context, org string, _ platformcloud.SuspendReason) error {
	f.suspended = append(f.suspended, org)
	return nil
}
func (f *fakeControl) SetRegistrySecret(_ context.Context, _, user, _, _ string) error {
	f.secrets = append(f.secrets, "registry:"+user)
	return nil
}
func (f *fakeControl) SetDNSSecret(_ context.Context, _, account, _, _ string) error {
	f.secrets = append(f.secrets, "dns:"+account)
	return nil
}
func (f *fakeControl) SecretsSet(context.Context) (bool, bool) { return len(f.secrets) > 0, false }

// Workspace is the real view's shape, filled in as fully as it can be.
func (f *fakeControl) Workspace(_ context.Context, org string) (*platformcloud.WorkspaceView, error) {
	return &platformcloud.WorkspaceView{Enabled: true, SuspendReasons: []platformcloud.SuspendReason{platformcloud.ReasonPastDue},
		Limits: platformcloud.Limits{VCPUs: 4, MemoryMiB: 8192, VMs: 10, ConcurrentBuilds: 1}, Use: platformcloud.Use{VCPUs: 1, MemoryMiB: 512, VMs: 1},
		Regions:    []platformcloud.WorkspaceRegion{{Name: "eu-west", DisplayName: "EU West", Status: "active"}},
		Placements: []platformcloud.WorkspacePlacement{{Project: "prj_1", Environment: "env_1", Service: "web", Region: "eu-west", State: "running", WaitingCapacity: true}}}, nil
}

type memSettings struct {
	v instance.PlatformCloudSettings
}

func (m *memSettings) PlatformCloud(context.Context) (instance.PlatformCloudSettings, error) {
	return m.v, nil
}
func (m *memSettings) SetPlatformCloud(_ context.Context, v instance.PlatformCloudSettings) error {
	m.v = v
	return nil
}

type auditEvent struct{ action, user, resource, id, outcome string }

type memAudit struct {
	mu     sync.Mutex
	events []auditEvent
}

func (a *memAudit) Record(_ context.Context, action, userID, resource, id, outcome string, _ map[string]string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.events = append(a.events, auditEvent{action, userID, resource, id, outcome})
}

func (a *memAudit) take() []auditEvent {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := a.events
	a.events = nil
	return out
}

type memMover struct {
	moved []string
	full  bool
}

func (m *memMover) Move(_ context.Context, id string) error {
	if m.full {
		return platformcloud.ErrNoCapacity
	}
	m.moved = append(m.moved, id)
	return nil
}

// cloudsAuthz gives the workspace owner every Clouds permission and a
// reader only Clouds read.
type cloudsAuthz struct{}

func (cloudsAuthz) Require(_ context.Context, p security.Principal, perm security.Permission, _ string) error {
	switch {
	case p.UserID == "ws_owner" && (perm == security.OrganizationCloudManage || perm == security.CloudAccountRead):
		return nil
	case p.UserID == "ws_reader" && perm == security.CloudAccountRead:
		return nil
	}
	return security.ErrForbidden
}

type sessionKey struct{}

// testGuard authenticates the way identity's does, as far as the gate can
// tell: a session sets the principal and the session user, an API key sets
// only a principal with its key, and nothing is a 401.
var testGuard = identity.Guard{func(next forge.Handler) forge.Handler {
	return func(c forge.Context) error {
		req := c.Request()
		ctx := req.Context()
		switch {
		case req.Header.Get("X-Key") != "":
			ctx = security.WithPrincipal(ctx, security.Principal{OrgID: "org_1", UserID: req.Header.Get("X-Key"), KeyID: "key_1"})
		case req.Header.Get("X-User") != "":
			ctx = security.WithPrincipal(ctx, security.Principal{OrgID: "org_1", UserID: req.Header.Get("X-User")})
			ctx = context.WithValue(ctx, sessionKey{}, req.Header.Get("X-User"))
		default:
			return forge.NewHTTPError(http.StatusUnauthorized, "sign in")
		}
		c.WithContext(ctx)
		return next(c)
	}
}}

// testPlatformUser is identity's PlatformUser: the session's user, when
// that user is the platform owner.
func testPlatformUser(ctx context.Context) (string, bool) {
	user, _ := ctx.Value(sessionKey{}).(string)
	return user, user == "platform_owner"
}

type rig struct {
	r     forge.Router
	svc   *fakeControl
	audit *memAudit
	mover *memMover
}

func newRig(t *testing.T) *rig {
	t.Helper()
	rg := &rig{r: forge.NewRouter(), svc: newFakeControl(), audit: &memAudit{}, mover: &memMover{}}
	routes := &routes{svc: rg.svc, settings: &memSettings{v: instance.PlatformCloudSettings{Attach: "manual"}}, mover: rg.mover, audit: rg.audit,
		authz: cloudsAuthz{}, isPlatform: testPlatformUser,
		orgNames: func(context.Context) (map[string]string, error) { return map[string]string{"org_2": "Acme"}, nil }}
	if err := routes.mount(rg.r, testGuard); err != nil {
		t.Fatal(err)
	}
	return rg
}

type caller map[string]string

var (
	platformOwner = caller{"X-User": "platform_owner"}
	workspaceUser = caller{"X-User": "ws_owner"}
	// The platform owner's own API key still isn't the platform owner.
	ownersKey = caller{"X-Key": "platform_owner"}
	anonymous = caller{}
)

func (rg *rig) do(t *testing.T, method, path, body string, who caller) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range who {
		req.Header.Set(k, v)
	}
	out := httptest.NewRecorder()
	rg.r.ServeHTTP(out, req)
	return out
}

type call struct{ method, path, body string }

var platformReads = []call{
	{http.MethodGet, platformBase + "/settings", ""},
	{http.MethodGet, platformBase + "/regions", ""},
	{http.MethodGet, platformBase + "/hosts", ""},
	{http.MethodGet, platformBase + "/tenants", ""},
}

var platformWrites = []call{
	{http.MethodPut, platformBase + "/settings", `{"attach":"all_workspaces","default_limits":{"vcpus":4,"memory_mib":8192,"vms":10,"concurrent_builds":1},"apex":"apps.example.com","registry":{"endpoint":"https://registry.example.com","repository":"platform"},"dns":{"provider":"cloudflare","zone":"example.com"},"registry_username":"robot","registry_password":"s3cret","dns_account_id":"acct","dns_token":"tok"}`},
	{http.MethodPost, platformBase + "/regions", `{"name":"us-east","display_name":"US East"}`},
	{http.MethodPatch, platformBase + "/regions/reg_1", `{"status":"closed"}`},
	{http.MethodPost, platformBase + "/hosts", `{"region_id":"reg_1","endpoint":"https://10.0.0.6:9443","host_id":"hw-2","server_ca":"ca","allotment":{"vcpus":8,"memory_mib":16384,"disk_mib":100000,"vms":10}}`},
	{http.MethodPost, platformBase + "/hosts/host_1/check", ""},
	{http.MethodPost, platformBase + "/hosts/host_1/drain", ""},
	{http.MethodPost, platformBase + "/hosts/host_1/move-all", ""},
	{http.MethodPost, platformBase + "/hosts/host_1/retire", ""},
	{http.MethodPut, platformBase + "/hosts/host_1/allotment", `{"vcpus":8,"memory_mib":16384,"disk_mib":100000,"vms":10}`},
	{http.MethodPut, platformBase + "/tenants/org_2", `{"status":"enabled","limits":{"vcpus":2,"memory_mib":2048,"vms":2,"concurrent_builds":1}}`},
	{http.MethodPost, platformBase + "/tenants/org_2/suspend", ""},
	{http.MethodPost, platformBase + "/tenants/org_2/restore", ""},
}

func TestRoutesAreRegisteredWithTheirNames(t *testing.T) {
	rg := newRig(t)
	want := map[string]string{
		"platformCloudSettings.get":       "GET /api/v1/platform/cloud/settings",
		"platformCloudSettings.set":       "PUT /api/v1/platform/cloud/settings",
		"platformCloudRegions.list":       "GET /api/v1/platform/cloud/regions",
		"platformCloudRegions.create":     "POST /api/v1/platform/cloud/regions",
		"platformCloudRegions.update":     "PATCH /api/v1/platform/cloud/regions/:region",
		"platformCloudHosts.list":         "GET /api/v1/platform/cloud/hosts",
		"platformCloudHosts.create":       "POST /api/v1/platform/cloud/hosts",
		"platformCloudHosts.check":        "POST /api/v1/platform/cloud/hosts/:host/check",
		"platformCloudHosts.drain":        "POST /api/v1/platform/cloud/hosts/:host/drain",
		"platformCloudHosts.moveAll":      "POST /api/v1/platform/cloud/hosts/:host/move-all",
		"platformCloudHosts.retire":       "POST /api/v1/platform/cloud/hosts/:host/retire",
		"platformCloudHosts.setAllotment": "PUT /api/v1/platform/cloud/hosts/:host/allotment",
		"platformCloudTenants.list":       "GET /api/v1/platform/cloud/tenants",
		"platformCloudTenants.set":        "PUT /api/v1/platform/cloud/tenants/:org",
		"platformCloudTenants.suspend":    "POST /api/v1/platform/cloud/tenants/:org/suspend",
		"platformCloudTenants.restore":    "POST /api/v1/platform/cloud/tenants/:org/restore",
		"workspacePlatformCloud.get":      "GET /api/v1/platform-cloud",
	}
	got := map[string]string{}
	for _, info := range rg.r.Routes() {
		if _, ours := want[info.Name]; ours {
			got[info.Name] = info.Method + " " + info.Path
			if !slices.Contains(info.Tags, "Platform Cloud") {
				t.Errorf("%s has tags %v", info.Name, info.Tags)
			}
		}
	}
	for name, route := range want {
		if got[name] != route {
			t.Errorf("%s is %q, want %q", name, got[name], route)
		}
	}
}

// Every platform route is a 404 for anyone but a platform user: a
// workspace owner, an API key (even the platform owner's own) and an
// anonymous caller alike. Nothing they send changes or audits anything.
func TestPlatformRoutesAre404ForOthers(t *testing.T) {
	rg := newRig(t)
	all := append(slices.Clone(platformReads), platformWrites...)
	routed := 0
	for _, info := range rg.r.Routes() {
		if strings.HasPrefix(info.Path, platformBase) {
			routed++
		}
	}
	if routed != len(all) {
		t.Fatalf("%d platform routes registered, the test covers %d", routed, len(all))
	}
	for _, c := range all {
		for name, who := range map[string]caller{"workspace owner": workspaceUser, "API key": ownersKey, "anonymous": anonymous} {
			if out := rg.do(t, c.method, c.path, c.body, who); out.Code != http.StatusNotFound {
				t.Errorf("%s %s as %s: %d %s", c.method, c.path, name, out.Code, out.Body.String())
			}
		}
	}
	if events := rg.audit.take(); len(events) != 0 {
		t.Fatalf("refused calls audited: %+v", events)
	}
	if len(rg.svc.suspended) != 0 || len(rg.svc.secrets) != 0 || len(rg.mover.moved) != 0 {
		t.Fatal("a refused call changed something")
	}
	for _, c := range platformReads {
		if out := rg.do(t, c.method, c.path, "", platformOwner); out.Code != http.StatusOK {
			t.Errorf("%s %s as the platform owner: %d %s", c.method, c.path, out.Code, out.Body.String())
		}
	}
}

// keysIn collects every object key in a JSON document.
func keysIn(t *testing.T, raw []byte) []string {
	t.Helper()
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	var keys []string
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			for k, e := range x {
				keys = append(keys, k)
				walk(e)
			}
		case []any:
			for _, e := range x {
				walk(e)
			}
		}
	}
	walk(v)
	return keys
}

// A workspace's view of Loomplane Cloud has every field the dashboard needs
// and nothing that names a host or its evidence.
func TestWorkspaceRouteHasNoHostFields(t *testing.T) {
	rg := newRig(t)
	out := rg.do(t, http.MethodGet, "/api/v1/platform-cloud", "", workspaceUser)
	if out.Code != http.StatusOK {
		t.Fatalf("workspace view: %d %s", out.Code, out.Body.String())
	}
	keys := keysIn(t, out.Body.Bytes())
	for _, banned := range []string{"host", "endpoint", "host_id", "evidence", "reason", "ingress_address", "client_fingerprint"} {
		if slices.Contains(keys, banned) {
			t.Errorf("the workspace view has a %q key: %s", banned, out.Body.String())
		}
	}
	for _, need := range []string{"enabled", "suspend_reasons", "limits", "use", "regions", "placements", "name", "display_name", "status",
		"project", "environment", "service", "region", "state", "waiting_capacity"} {
		if !slices.Contains(keys, need) {
			t.Errorf("the workspace view has no %q: %s", need, out.Body.String())
		}
	}
	if strings.Contains(out.Body.String(), "10.0.0.5") {
		t.Fatalf("the workspace view names a host address: %s", out.Body.String())
	}
	// Clouds read is enough.
	if out = rg.do(t, http.MethodGet, "/api/v1/platform-cloud", "", caller{"X-User": "ws_reader"}); out.Code != http.StatusOK {
		t.Fatalf("a member with Clouds read: %d %s", out.Code, out.Body.String())
	}
	// Without it there is nothing to see.
	if out = rg.do(t, http.MethodGet, "/api/v1/platform-cloud", "", caller{"X-User": "ws_viewer"}); out.Code != http.StatusForbidden {
		t.Fatalf("a member without the Clouds permission: %d", out.Code)
	}
}

// The client certificate a host's agent must trust comes back when the
// host is registered and never again.
func TestCreateHostReturnsCertificateOnlyOnCreate(t *testing.T) {
	rg := newRig(t)
	out := rg.do(t, http.MethodPost, platformBase+"/hosts", platformWrites[3].body, platformOwner)
	if out.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", out.Code, out.Body.String())
	}
	var created NewHost
	if err := json.Unmarshal(out.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.ClientCertificate != testCert || created.ClientFingerprint != "ab12" || created.Host.ID != "host_2" {
		t.Fatalf("created = %+v", created)
	}
	keys := keysIn(t, out.Body.Bytes())
	for _, k := range []string{"host", "client_certificate", "client_fingerprint"} {
		if !slices.Contains(keys, k) {
			t.Errorf("the create answer has no %q", k)
		}
	}
	list := rg.do(t, http.MethodGet, platformBase+"/hosts", "", platformOwner)
	if list.Code != http.StatusOK {
		t.Fatalf("list: %d", list.Code)
	}
	if slices.Contains(keysIn(t, list.Body.Bytes()), "client_certificate") || strings.Contains(list.Body.String(), "BEGIN CERTIFICATE") {
		t.Fatalf("the host list carries the certificate: %s", list.Body.String())
	}
}

// Every platform write records one platform_cloud event with the platform
// user as the actor. Reads record nothing, and no event or answer carries
// a credential.
func TestPlatformWritesAreAudited(t *testing.T) {
	rg := newRig(t)
	for _, c := range platformReads {
		rg.do(t, c.method, c.path, "", platformOwner)
	}
	if events := rg.audit.take(); len(events) != 0 {
		t.Fatalf("reads audited: %+v", events)
	}
	want := map[string]string{
		platformBase + "/settings":               "platform_cloud.settings.update",
		platformBase + "/regions":                "platform_cloud.region.create",
		platformBase + "/regions/reg_1":          "platform_cloud.region.update",
		platformBase + "/hosts":                  "platform_cloud.host.create",
		platformBase + "/hosts/host_1/check":     "platform_cloud.host.check",
		platformBase + "/hosts/host_1/drain":     "platform_cloud.host.drain",
		platformBase + "/hosts/host_1/move-all":  "platform_cloud.host.move_all",
		platformBase + "/hosts/host_1/retire":    "platform_cloud.host.retire",
		platformBase + "/hosts/host_1/allotment": "platform_cloud.host.allotment_update",
		platformBase + "/tenants/org_2":          "platform_cloud.tenant.update",
		platformBase + "/tenants/org_2/suspend":  "platform_cloud.tenant.suspend",
		platformBase + "/tenants/org_2/restore":  "platform_cloud.tenant.restore",
	}
	for _, c := range platformWrites {
		out := rg.do(t, c.method, c.path, c.body, platformOwner)
		if out.Code >= 300 {
			t.Errorf("%s %s: %d %s", c.method, c.path, out.Code, out.Body.String())
			continue
		}
		if strings.Contains(out.Body.String(), "s3cret") || strings.Contains(out.Body.String(), `"tok"`) {
			t.Errorf("%s %s answers with a credential: %s", c.method, c.path, out.Body.String())
		}
		events := rg.audit.take()
		outcome := "success"
		if strings.HasSuffix(c.path, "/restore") {
			// org_2 was never suspended by the platform owner.
			outcome = "no_change"
		}
		if len(events) != 1 || events[0].action != want[c.path] || events[0].user != "platform_owner" || events[0].outcome != outcome {
			t.Errorf("%s %s audited %+v, want one %s by the platform owner, %s", c.method, c.path, events, want[c.path], outcome)
		}
	}
	if !slices.Equal(rg.mover.moved, []string{"inst_1"}) {
		t.Errorf("move-all redeployed %v", rg.mover.moved)
	}
	if !slices.Equal(rg.svc.secrets, []string{"registry:robot", "dns:acct"}) {
		t.Errorf("secrets set = %v", rg.svc.secrets)
	}
}

// The settings never come back with a credential, only whether one is
// stored.
func TestSettingsCredentialsAreWriteOnly(t *testing.T) {
	rg := newRig(t)
	if out := rg.do(t, platformWrites[0].method, platformWrites[0].path, platformWrites[0].body, platformOwner); out.Code != http.StatusOK {
		t.Fatalf("set: %d %s", out.Code, out.Body.String())
	}
	out := rg.do(t, http.MethodGet, platformBase+"/settings", "", platformOwner)
	body := out.Body.String()
	if strings.Contains(body, "s3cret") || strings.Contains(body, "robot") || strings.Contains(body, `"tok"`) {
		t.Fatalf("settings carry a credential: %s", body)
	}
	var v SettingsView
	if err := json.Unmarshal(out.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if !v.RegistryCredentialSaved || v.Attach != "all_workspaces" || v.Apex != "apps.example.com" {
		t.Fatalf("settings = %+v", v)
	}
	if out = rg.do(t, http.MethodPut, platformBase+"/settings", `{"attach":"sometimes"}`, platformOwner); out.Code != http.StatusUnprocessableEntity {
		t.Fatalf("a bad attach mode: %d", out.Code)
	}
}

// The routes' schemas build into the OpenAPI document, so the dashboard's
// generated hooks can be grafted from it, and the workspace view's schema
// names no host field.
func TestOpenAPIContract(t *testing.T) {
	r := forge.NewRouter(forge.WithOpenAPI(forge.OpenAPIConfig{Title: "Loomplane", Version: "1", SpecEnabled: true}))
	routes := &routes{svc: newFakeControl(), settings: &memSettings{}, audit: &memAudit{}, authz: cloudsAuthz{}, isPlatform: testPlatformUser}
	if err := routes.mount(r, testGuard); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(r.OpenAPISpec())
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range []string{"platformCloudSettings.set", "platformCloudHosts.create", "platformCloudHosts.moveAll", "platformCloudTenants.restore", "workspacePlatformCloud.get"} {
		if !strings.Contains(string(raw), op) {
			t.Errorf("the spec has no %s", op)
		}
	}
	var spec struct {
		Components struct {
			Schemas map[string]struct {
				Properties map[string]any `json:"properties"`
			} `json:"schemas"`
		} `json:"components"`
	}
	if err = json.Unmarshal(raw, &spec); err != nil {
		t.Fatal(err)
	}
	created := spec.Components.Schemas["NewHost"].Properties
	for _, k := range []string{"host", "client_certificate", "client_fingerprint"} {
		if _, ok := created[k]; !ok {
			t.Errorf("NewHost has no %s: %v", k, created)
		}
	}
	for name, s := range spec.Components.Schemas {
		if !strings.HasPrefix(name, "Workspace") {
			continue
		}
		for _, banned := range []string{"host", "host_id", "endpoint", "evidence", "reason"} {
			if _, ok := s.Properties[banned]; ok {
				t.Errorf("%s has %s", name, banned)
			}
		}
	}
}

// With no room on another host, move-all moves nothing and says the
// service was skipped; the host stays draining.
func TestMoveAllSkipsWhatHasNowhereToGo(t *testing.T) {
	rg := newRig(t)
	rg.mover.full = true
	out := rg.do(t, http.MethodPost, platformBase+"/hosts/host_1/move-all", "", platformOwner)
	if out.Code != http.StatusOK {
		t.Fatalf("move-all: %d %s", out.Code, out.Body.String())
	}
	var got MoveAll
	if err := json.Unmarshal(out.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Moved != 0 || len(got.Skipped) != 1 || len(got.Failed) != 0 || got.Host.Status != "draining" {
		t.Fatalf("move-all = %+v", got)
	}
	if events := rg.audit.take(); len(events) != 1 || events[0].outcome != "failure" {
		t.Fatalf("audited %+v", events)
	}
}
