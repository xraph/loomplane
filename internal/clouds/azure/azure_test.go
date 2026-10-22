package azure

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/clouds/azure/azuretest"
)

const (
	tenant = "11111111-1111-1111-1111-111111111111"
	client = "22222222-2222-2222-2222-222222222222"
	sub    = "33333333-3333-3333-3333-333333333333"
)

func settings() map[string]string {
	return map[string]string{"tenant_id": tenant, "client_id": client, "subscription_id": sub}
}

func TestParseCredential(t *testing.T) {
	c, err := ParseCredential([]byte(" s3cr~t.value \n"), map[string]string{"tenant_id": "Contoso.onmicrosoft.com", "client_id": strings.ToUpper(client), "subscription_id": sub})
	if err != nil || c.Secret != "s3cr~t.value" || c.ClientID != client || c.ResourceGroup != DefaultResourceGroup || c.TenantID != "Contoso.onmicrosoft.com" {
		t.Fatalf("%+v, %v", c, err)
	}
	for name, mutate := range map[string]func(map[string]string){
		"no tenant":           func(m map[string]string) { delete(m, "tenant_id") },
		"client isn't a GUID": func(m map[string]string) { m["client_id"] = "my-app" },
		"no subscription":     func(m map[string]string) { m["subscription_id"] = "" },
		"bad resource group":  func(m map[string]string) { m["resource_group"] = "ends-in." },
	} {
		m := settings()
		mutate(m)
		if _, err := ParseCredential([]byte("secret"), m); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := ParseCredential([]byte("two words"), settings()); !errors.Is(err, ErrInvalid) {
		t.Errorf("a secret with a space: %v", err)
	}
}

func TestMatch(t *testing.T) {
	for _, tt := range []struct {
		pattern, action string
		want            bool
	}{
		{"*", "Microsoft.App/containerApps/write", true},
		{"microsoft.app/*", "Microsoft.App/containerApps/write", true},
		{"*/read", "Microsoft.App/containerApps/read", true},
		{"*/read", "Microsoft.App/containerApps/write", false},
		{"Microsoft.App/containerApps/*", "Microsoft.App/managedEnvironments/write", false},
		{"Microsoft.ContainerRegistry/registries/*/action", "Microsoft.ContainerRegistry/registries/scheduleRun/action", true},
		{"Microsoft.App/containerApps/read", "Microsoft.App/containerApps/read", true},
	} {
		if got := matchAny([]string{tt.pattern}, tt.action); got != tt.want {
			t.Errorf("%s ~ %s = %v", tt.pattern, tt.action, got)
		}
	}
}

// fakeARM is just enough of Entra ID and Resource Manager to check a
// principal: perms are what it may do in the group, or in the
// subscription when the group isn't there.
type fakeARM struct {
	group      bool
	registered map[string]bool
	groupPerms []permission
	subPerms   []permission
	state      string
	sawToken   bool
}

func (f *fakeARM) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.URL.Path == "/"+tenant+"/oauth2/v2.0/token" {
		_ = r.ParseForm()
		if r.PostForm.Get("client_secret") != "right" || r.PostForm.Get("scope") != DefaultManagement+"/.default" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"invalid_client","error_description":"AADSTS7000215: Invalid client secret provided.\r\nTrace ID: x"}`))
			return
		}
		_, _ = w.Write([]byte(`{"access_token":"tok","expires_in":3600}`))
		return
	}
	if r.Header.Get("Authorization") != "Bearer tok" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	f.sawToken = true
	reply := func(v string) { _, _ = w.Write([]byte(v)) }
	perms := func(p []permission) {
		var b strings.Builder
		b.WriteString(`{"value":[`)
		for i, x := range p {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(`{"actions":["` + strings.Join(x.Actions, `","`) + `"],"notActions":[` + quoted(x.NotActions) + `]}`)
		}
		b.WriteString(`]}`)
		reply(b.String())
	}
	p := r.URL.Path
	switch {
	case p == "/subscriptions/"+sub:
		reply(`{"subscriptionId":"` + sub + `","tenantId":"` + tenant + `","displayName":"Acme production","state":"` + f.state + `"}`)
	case strings.HasPrefix(p, "/subscriptions/"+sub+"/providers/Microsoft.Authorization"):
		perms(f.subPerms)
	case strings.HasPrefix(p, "/subscriptions/"+sub+"/providers/"):
		ns := strings.TrimPrefix(p, "/subscriptions/"+sub+"/providers/")
		state := "NotRegistered"
		if f.registered[ns] {
			state = "Registered"
		}
		reply(`{"namespace":"` + ns + `","registrationState":"` + state + `"}`)
	case p == "/subscriptions/"+sub+"/resourceGroups/loomplane":
		if !f.group {
			w.WriteHeader(http.StatusNotFound)
			reply(`{"error":{"code":"ResourceGroupNotFound","message":"Resource group 'loomplane' could not be found."}}`)
			return
		}
		reply(`{"name":"loomplane","location":"eastus"}`)
	case p == "/subscriptions/"+sub+"/resourceGroups/loomplane/providers/Microsoft.Authorization/permissions":
		perms(f.groupPerms)
	default:
		w.WriteHeader(http.StatusNotFound)
		reply(`{"error":{"code":"NotFound","message":"no route ` + p + `"}}`)
	}
}

func quoted(s []string) string {
	if len(s) == 0 {
		return ""
	}
	return `"` + strings.Join(s, `","`) + `"`
}

func session(t *testing.T, f *fakeARM, secret string) (*Session, error) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	cred, err := ParseCredential([]byte(secret), settings())
	if err != nil {
		t.Fatal(err)
	}
	return New(srv.URL).Session(t.Context(), cred)
}

func TestSession_aWrongSecretIsRejected(t *testing.T) {
	_, err := session(t, &fakeARM{}, "wrong")
	if !errors.Is(err, ErrRejected) || !strings.Contains(err.Error(), "AADSTS7000215") || strings.Contains(err.Error(), "Trace ID") {
		t.Fatalf("%v", err)
	}
}

func TestMissing(t *testing.T) {
	all := map[string]bool{"Microsoft.App": true, "Microsoft.ContainerRegistry": true}
	contributor := []permission{{Actions: []string{"*"}, NotActions: []string{"Microsoft.Authorization/*/Delete", "Microsoft.Authorization/*/Write"}}}
	t.Run("contributor on the group lacks nothing", func(t *testing.T) {
		s, err := session(t, &fakeARM{group: true, registered: all, groupPerms: contributor, state: "Enabled"}, "right")
		if err != nil {
			t.Fatal(err)
		}
		missing, err := s.Missing(t.Context())
		if err != nil || len(missing) != 0 {
			t.Fatalf("%v, %v", missing, err)
		}
	})
	t.Run("a reader lacks every write, in order", func(t *testing.T) {
		s, err := session(t, &fakeARM{group: true, registered: all, groupPerms: []permission{{Actions: []string{"*/read"}}}}, "right")
		if err != nil {
			t.Fatal(err)
		}
		missing, err := s.Missing(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		var want []string
		for _, a := range Actions {
			if !strings.HasSuffix(a, "/read") {
				want = append(want, a)
			}
		}
		if !slices.Equal(missing, want) {
			t.Fatalf("missing %v, want %v", missing, want)
		}
	})
	t.Run("a group that isn't there is checked in the subscription", func(t *testing.T) {
		// Container Apps only: making the group and building are left out.
		apps := []permission{{Actions: []string{"Microsoft.App/managedEnvironments/*", "Microsoft.App/containerApps/*", "Microsoft.Resources/subscriptions/resourceGroups/read", "Microsoft.ContainerRegistry/registries/*"}, NotActions: []string{"Microsoft.ContainerRegistry/registries/scheduleRun/action"}}}
		s, err := session(t, &fakeARM{registered: map[string]bool{"Microsoft.ContainerRegistry": true}, subPerms: apps}, "right")
		if err != nil {
			t.Fatal(err)
		}
		missing, err := s.Missing(t.Context())
		// Nor may it make databases.
		want := make([]string, 0, 11)
		want = append(want, createGroup, "Microsoft.ContainerRegistry/registries/scheduleRun/action")
		for _, ns := range []string{"Microsoft.DBforPostgreSQL", "Microsoft.DBforMySQL"} {
			for _, a := range []string{"read", "write", "delete", "firewallRules/write"} {
				want = append(want, ns+"/flexibleServers/"+a)
			}
		}
		want = append(want, "Microsoft.App/register/action")
		if err != nil || !slices.Equal(missing, want) {
			t.Fatalf("missing %v, want %v (%v)", missing, want, err)
		}
	})
}

func TestSubscription(t *testing.T) {
	s, err := session(t, &fakeARM{state: "Enabled"}, "right")
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Subscription(t.Context())
	if err != nil || got.DisplayName != "Acme production" || got.TenantID != tenant {
		t.Fatalf("%+v, %v", got, err)
	}
	s, err = session(t, &fakeARM{state: "Disabled"}, "right")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Subscription(t.Context()); !errors.Is(err, ErrRejected) {
		t.Fatalf("a disabled subscription: %v", err)
	}
}

func TestRegistryName(t *testing.T) {
	a, b := RegistryName(sub, "loomplane"), RegistryName(sub, "other")
	if a == b || len(a) > 50 || len(a) < 5 || strings.ToLower(a) != a || a != RegistryName(strings.ToUpper(sub), "Loomplane") {
		t.Fatalf("%s %s", a, b)
	}
}

func TestBuildRequest_task(t *testing.T) {
	task, err := BuildRequest{Source: "source/x.tar.gz", Image: "api-1234:abc", Dockerfile: "mono-abc/services/api/Dockerfile", Context: "mono-abc/services/api"}.task()
	want := "version: v1.1.0\nsteps:\n  - build: -t {{.Run.Registry}}/api-1234:abc -f mono-abc/services/api/Dockerfile mono-abc/services/api\n  - push:\n    - \"{{.Run.Registry}}/api-1234:abc\"\n"
	if err != nil || task != want {
		t.Fatalf("%q, %v", task, err)
	}
	if _, err := (BuildRequest{Image: "x:y", Dockerfile: "a b/Dockerfile", Context: "a"}).task(); err == nil {
		t.Fatal("a path with a space")
	}
}

func TestBuildRequest_packTask(t *testing.T) {
	pack := &PackBuild{
		Image: "gcr.io/k8s-skaffold/pack", Builder: "gcr.io/buildpacks/builder:latest", Path: "acme-mono-1/apps/web",
		Env: []string{"GOOGLE_ENTRYPOINT=gunicorn main:app"},
	}
	task, err := BuildRequest{Source: "s.tar.gz", Image: "web-1:abc", Pack: pack}.task()
	want := "version: v1.1.0\nsteps:\n" +
		"  - cmd: gcr.io/k8s-skaffold/pack build {{.Run.Registry}}/web-1:abc --builder gcr.io/buildpacks/builder:latest --path acme-mono-1/apps/web --env GOOGLE_ENTRYPOINT\n" +
		"    entryPoint: pack\n" +
		"    env: [\"GOOGLE_ENTRYPOINT=gunicorn main:app\"]\n" +
		"  - push:\n    - \"{{.Run.Registry}}/web-1:abc\"\n"
	if err != nil || task != want {
		t.Fatalf("%q, %v", task, err)
	}

	// Pack wins over the Dockerfile fields, and a build with no variables
	// has no env line.
	bare := *pack
	bare.Env = nil
	task, err = BuildRequest{Image: "web-1:abc", Dockerfile: "a b", Context: "c d", Pack: &bare}.task()
	if err != nil || strings.Contains(task, "env:") || strings.Contains(task, "--env") || !strings.Contains(task, "--path acme-mono-1/apps/web\n") {
		t.Fatalf("%q, %v", task, err)
	}

	spaced := *pack
	spaced.Path = "acme mono/apps/web"
	if _, err = (BuildRequest{Image: "x:y", Pack: &spaced}).task(); err == nil {
		t.Fatal("a path with a space")
	}
	multiline := *pack
	multiline.Env = []string{"GOOGLE_ENTRYPOINT=a\nb"}
	if _, err = (BuildRequest{Image: "x:y", Pack: &multiline}).task(); err == nil {
		t.Fatal("a value with a newline")
	}
	quoted := *pack
	quoted.Env = []string{`GOOGLE_ENTRYPOINT=gunicorn -k "uvicorn" main:app`}
	task, err = BuildRequest{Image: "x:y", Pack: &quoted}.task()
	if err != nil || !strings.Contains(task, `env: ["GOOGLE_ENTRYPOINT=gunicorn -k \"uvicorn\" main:app"]`) {
		t.Fatalf("%q, %v", task, err)
	}
	badKey := *pack
	badKey.Env = []string{"A B=1"}
	if _, err = (BuildRequest{Image: "x:y", Pack: &badKey}).task(); err == nil {
		t.Fatal("a name with a space")
	}
}

func TestOwns(t *testing.T) {
	s := &Session{cred: Credential{SubscriptionID: sub, ResourceGroup: "loomplane"}}
	if !s.Owns(s.AppPath("api")) || !s.Owns(strings.Replace(s.AppPath("api"), "resourceGroups", "resourcegroups", 1)) {
		t.Fatal("its own app")
	}
	for _, id := range []string{
		"/subscriptions/44444444-4444-4444-4444-444444444444/resourceGroups/loomplane/providers/Microsoft.App/containerApps/api",
		"/subscriptions/" + sub + "/resourceGroups/loomplane/providers/Microsoft.Storage/storageAccounts/x",
		"https://evil.example/subscriptions/" + sub + "/resourceGroups/x/providers/Microsoft.App/containerApps/api",
	} {
		if s.Owns(id) {
			t.Errorf("owns %s", id)
		}
	}
}

func TestRoleAssignmentName(t *testing.T) {
	scope := "/subscriptions/" + sub + "/resourceGroups/loomplane/providers/Microsoft.ContainerRegistry/registries/" + RegistryName(sub, "loomplane")
	a := RoleAssignmentName(scope, "66666666-6666-6666-6666-666666666666")
	if a != RoleAssignmentName(scope, "66666666-6666-6666-6666-666666666666") {
		t.Fatal("the name isn't deterministic")
	}
	if a == RoleAssignmentName(scope, "77777777-7777-7777-7777-777777777777") {
		t.Fatal("two principals share a name")
	}
	if a == RoleAssignmentName(scope+"x", "66666666-6666-6666-6666-666666666666") {
		t.Fatal("two scopes share a name")
	}
	if !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-5[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(a) {
		t.Fatalf("%s isn't a version 5 UUID", a)
	}
}

// fakeSession signs in to the Azure fake as its principal.
func fakeSession(t *testing.T, f *azuretest.Fake) *Session {
	t.Helper()
	cred, err := ParseCredential([]byte(azuretest.Secret), azuretest.Settings())
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(f.URL()).Session(t.Context(), cred)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestPullMode(t *testing.T) {
	f := azuretest.New(t)
	s := fakeSession(t, f)
	// A Contributor may not assign roles, in the subscription while the
	// group isn't there or in the group once it is.
	for _, when := range []string{"before the group", "with the group"} {
		got, err := s.PullMode(t.Context())
		if err != nil || got != PullModeAdmin {
			t.Fatalf("contributor %s: %q, %v", when, got, err)
		}
		if err := s.EnsureGroup(t.Context(), "eastus"); err != nil {
			t.Fatal(err)
		}
	}
	f.Lock()
	f.NotActions = nil
	f.Unlock()
	if got, err := s.PullMode(t.Context()); err != nil || got != PullModeIdentity {
		t.Fatalf("an owner: %q, %v", got, err)
	}
	// Assigning roles isn't enough: the app also needs the identity.
	f.Lock()
	f.Actions, f.NotActions = []string{"Microsoft.Authorization/roleAssignments/write", "Microsoft.ManagedIdentity/userAssignedIdentities/write"}, nil
	f.Unlock()
	if got, err := s.PullMode(t.Context()); err != nil || got != PullModeAdmin {
		t.Fatalf("a principal that can't assign the identity: %q, %v", got, err)
	}
}

func TestPullIdentityAndGrant(t *testing.T) {
	f := azuretest.New(t)
	s := fakeSession(t, f)
	ctx := t.Context()
	if err := s.EnsureGroup(ctx, "eastus"); err != nil {
		t.Fatal(err)
	}
	reg, err := s.EnsureRegistry(ctx, "eastus")
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.EnsurePullIdentity(ctx, "eastus")
	if err != nil || id.PrincipalID != azuretest.PullPrincipal || !strings.HasSuffix(id.ID, "/providers/Microsoft.ManagedIdentity/userAssignedIdentities/"+PullIdentity) {
		t.Fatalf("%+v, %v", id, err)
	}
	again, err := s.EnsurePullIdentity(ctx, "eastus")
	if err != nil || *again != *id {
		t.Fatalf("a second call: %+v, %v", again, err)
	}
	f.Lock()
	puts := f.IdentityPuts
	f.Unlock()
	if puts != 1 {
		t.Fatalf("the identity was PUT %d times", puts)
	}

	created, err := s.GrantPull(ctx, reg.ID, id.PrincipalID)
	if err != nil || !created {
		t.Fatalf("the first grant: %v, %v", created, err)
	}
	created, err = s.GrantPull(ctx, reg.ID, id.PrincipalID)
	if err != nil || created {
		t.Fatalf("the second grant: %v, %v", created, err)
	}
	f.Lock()
	got := slices.Clone(f.RoleAssignments)
	f.Unlock()
	if len(got) != 1 {
		t.Fatalf("assignments %+v", got)
	}
	want := "/subscriptions/" + sub + "/providers/Microsoft.Authorization/roleDefinitions/" + AcrPull
	if got[0].RoleDefinitionID != want || got[0].PrincipalID != id.PrincipalID || got[0].PrincipalType != "ServicePrincipal" ||
		!strings.EqualFold(got[0].Scope, reg.ID) || got[0].Name != RoleAssignmentName(reg.ID, id.PrincipalID) {
		t.Fatalf("assignment %+v, want role %s on %s", got[0], want, reg.ID)
	}
}

func TestFlexibleServer(t *testing.T) {
	f := azuretest.New(t)
	s := fakeSession(t, f)
	ctx := t.Context()
	if err := s.EnsureGroup(ctx, "westeurope"); err != nil {
		t.Fatal(err)
	}
	fqdn, err := s.EnsureFlexibleServer(ctx, FlexibleServer{Engine: "postgres", Name: "db-production-a1b2c3", Location: "westeurope", Version: "16", Login: "loomplane", Password: "Pw-1"})
	if err != nil || fqdn != "db-production-a1b2c3.postgres.database.azure.com" {
		t.Fatalf("%q, %v", fqdn, err)
	}
	// The database namespace is registered on first use.
	if !f.Registered("Microsoft.DBforPostgreSQL") || f.Registered("Microsoft.DBforMySQL") {
		t.Fatal("the postgres namespace wasn't registered on its own")
	}
	f.Lock()
	puts := slices.Clone(f.ServerPuts)
	rule := f.FirewallRules["db-production-a1b2c3/AllowAllAzureServicesAndResourcesWithinAzureIps"]
	f.Unlock()
	if len(puts) != 1 {
		t.Fatalf("puts %v", puts)
	}
	put := puts[0]
	props := put["properties"].(map[string]any)
	sku := put["sku"].(map[string]any)
	storage := props["storage"].(map[string]any)
	network := props["network"].(map[string]any)
	if put["location"] != "westeurope" || sku["name"] != "Standard_B1ms" || sku["tier"] != "Burstable" || storage["storageSizeGB"] != float64(32) ||
		props["version"] != "16" || props["administratorLogin"] != "loomplane" || props["administratorLoginPassword"] != "Pw-1" ||
		network["publicNetworkAccess"] != "Enabled" {
		t.Fatalf("server %v", put)
	}
	rp, _ := rule["properties"].(map[string]any)
	if rp["startIpAddress"] != "0.0.0.0" || rp["endIpAddress"] != "0.0.0.0" {
		t.Fatalf("firewall rule %v", rule)
	}
	id := s.FlexibleServerPath("postgres", "db-production-a1b2c3")
	if !strings.HasSuffix(id, "/resourceGroups/loomplane/providers/Microsoft.DBforPostgreSQL/flexibleServers/db-production-a1b2c3") || FlexibleServerEngine(id) != "postgres" {
		t.Fatalf("id %s", id)
	}
	srv, err := s.GetFlexibleServer(ctx, id)
	if err != nil || !strings.EqualFold(srv.Properties.State, "Ready") {
		t.Fatalf("%+v, %v", srv, err)
	}

	// MySQL gets its own namespace, API and storage.
	fqdn, err = s.EnsureFlexibleServer(ctx, FlexibleServer{Engine: "mysql", Name: "my-production-a1b2c3", Location: "westeurope", Version: "8.0.21", Login: "loomplane", Password: "Pw-2"})
	if err != nil || fqdn != "my-production-a1b2c3.mysql.database.azure.com" || !f.Registered("Microsoft.DBforMySQL") {
		t.Fatalf("%q, %v", fqdn, err)
	}
	f.Lock()
	mysql := f.ServerPuts[1]
	f.Unlock()
	if mysql["properties"].(map[string]any)["storage"].(map[string]any)["storageSizeGB"] != float64(20) {
		t.Fatalf("mysql %v", mysql)
	}

	if err := s.DeleteFlexibleServer(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetFlexibleServer(ctx, id); !IsNotFound(err) {
		t.Fatalf("after delete: %v", err)
	}
	if err := s.DeleteFlexibleServer(ctx, id); err != nil {
		t.Fatalf("deleting it again: %v", err)
	}
	// Nothing outside the subscription's flexible servers goes through it.
	for _, other := range []string{s.AppPath("api"), "/subscriptions/44444444-4444-4444-4444-444444444444/resourceGroups/loomplane/providers/Microsoft.DBforPostgreSQL/flexibleServers/x"} {
		if err := s.DeleteFlexibleServer(ctx, other); err == nil {
			t.Errorf("deleted %s", other)
		}
	}
	if _, err := s.EnsureFlexibleServer(ctx, FlexibleServer{Engine: "redis", Name: "x"}); err == nil {
		t.Fatal("made a redis flexible server")
	}
}

func TestFlexibleServer_aDisabledServerFails(t *testing.T) {
	f := azuretest.New(t)
	f.ServerState = "Disabled"
	s := fakeSession(t, f)
	if err := s.EnsureGroup(t.Context(), "eastus"); err != nil {
		t.Fatal(err)
	}
	_, err := s.EnsureFlexibleServer(t.Context(), FlexibleServer{Engine: "postgres", Name: "db", Location: "eastus", Version: "16", Login: "loomplane", Password: "Pw-1"})
	if err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("%v", err)
	}
}
