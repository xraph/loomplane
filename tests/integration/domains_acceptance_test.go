//go:build integration && acceptance

package integration

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/clouds/cloudflare"
	"github.com/xraph/loomplane/internal/clouds/server"
	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/pkg/provider"
)

// The domains acceptance run checks what the Cloudflare fakes can't: that
// the Registrar sandbox, a real zone and a real cluster with cert-manager
// agree with them. Each part skips on its own, naming every variable it
// still needs, before it opens a database or makes a request. docs/domains.md
// lists the variables.

const (
	cloudflareAPI      = "https://api.cloudflare.com"
	letsEncryptStaging = "https://acme-staging-v02.api.letsencrypt.org/directory"
)

// domainsVars reads the named variables and returns the ones that are unset.
func domainsVars(names ...string) (map[string]string, []string) {
	values, missing := map[string]string{}, []string{}
	for _, name := range names {
		values[name] = strings.TrimSpace(os.Getenv(name))
		if values[name] == "" {
			missing = append(missing, name)
		}
	}
	return values, missing
}

func skipDomainsGap(t *testing.T, missing []string) {
	t.Helper()
	if len(missing) > 0 {
		t.Skipf("gap: set %s; docs/domains.md says how", strings.Join(missing, ", "))
	}
}

func TestAcceptanceDomains(t *testing.T) {
	t.Run("registrar sandbox", acceptanceRegistrar)
	t.Run("kubernetes routes", acceptanceRoutes)
}

// cloudflareGuard stands between Loomplane and Cloudflare. It refuses any
// production registrar path, anything to do with transfers and any write to
// a zone itself, so a mistaken configuration can't buy a real domain or
// leave a zone behind in the real account. With records false it refuses
// DNS record writes too. It records every registrar request it passes on.
type cloudflareGuard struct {
	srv     *httptest.Server
	mu      sync.Mutex
	sandbox []string
	refused []string
}

// zoneWrite is a request that changes a zone, or with records a DNS record
// in one. The zones API is the production account's even in sandbox mode.
func zoneWrite(r *http.Request, records bool) bool {
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		return false
	}
	_, rest, ok := strings.Cut(r.URL.Path, "/client/v4/zones")
	if !ok || (rest != "" && !strings.HasPrefix(rest, "/")) {
		return false
	}
	// "", "/{id}": the zone itself. Anything deeper is the zone's records.
	if strings.Count(strings.Trim(rest, "/"), "/") < 1 {
		return true
	}
	return !records
}

func TestCloudflareGuardRefusesZoneWrites(t *testing.T) {
	for _, tc := range []struct {
		method, path string
		records      bool
		refused      bool
	}{
		{http.MethodPost, "/client/v4/zones", true, true},
		{http.MethodPatch, "/client/v4/zones/z1", true, true},
		{http.MethodDelete, "/client/v4/zones/z1/", true, true},
		{http.MethodGet, "/client/v4/zones", false, false},
		{http.MethodPost, "/client/v4/zones/z1/dns_records", true, false},
		{http.MethodPost, "/client/v4/zones/z1/dns_records", false, true},
		{http.MethodPut, "/client/v4/zones/z1/dns_records/r1", false, true},
		{http.MethodGet, "/client/v4/zones/z1/dns_records", false, false},
		{http.MethodPost, "/client/v4/zonesx", false, false},
		{http.MethodPost, "/client/v4/accounts/a/registrar-sandbox/registrations", false, false},
	} {
		r := httptest.NewRequest(tc.method, tc.path, nil)
		if got := zoneWrite(r, tc.records); got != tc.refused {
			t.Errorf("%s %s records=%v: refused %v, want %v", tc.method, tc.path, tc.records, got, tc.refused)
		}
	}
}

func newCloudflareGuard(t *testing.T, records bool) *cloudflareGuard {
	t.Helper()
	target, err := url.Parse(cloudflareAPI)
	if err != nil {
		t.Fatal(err)
	}
	g := &cloudflareGuard{}
	proxy := &httputil.ReverseProxy{Rewrite: func(r *httputil.ProxyRequest) { r.SetURL(target) }}
	g.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		segments := strings.Split(r.URL.Path, "/")
		request := r.Method + " " + r.URL.Path
		g.mu.Lock()
		switch {
		case slices.Contains(segments, "registrar") || strings.Contains(r.URL.Path, "transfer") || zoneWrite(r, records):
			g.refused = append(g.refused, request)
			g.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"success":false,"errors":[{"code":0}],"result":null}`)
			return
		case slices.Contains(segments, "registrar-sandbox"):
			g.sandbox = append(g.sandbox, request)
		}
		g.mu.Unlock()
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(g.srv.Close)
	return g
}

func (g *cloudflareGuard) endpoint() string { return g.srv.URL + "/client/v4" }

// sandboxOnly fails unless every registrar request so far went to the
// sandbox and nothing wrote to a zone it shouldn't, and returns the
// sandbox requests.
func (g *cloudflareGuard) sandboxOnly(t *testing.T) []string {
	t.Helper()
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.refused) > 0 {
		t.Fatalf("Loomplane sent requests the guard refuses (outside the registrar sandbox, or a zone write): %v", g.refused)
	}
	return slices.Clone(g.sandbox)
}

// domainsApp starts Loomplane on a fresh database with its Cloudflare calls
// going through guard and its registrar in sandbox mode.
func domainsApp(t *testing.T, guard *cloudflareGuard, configure func(*config.Config)) (*client, config.Config) {
	t.Helper()
	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	cfg.Orchestration.PollInterval = time.Second
	cfg.Analysis.WorkDir = t.TempDir()
	cfg.Storage.URL = "file://" + t.TempDir()
	cfg.Clouds.CloudflareEndpoint = guard.endpoint()
	cfg.Clouds.CloudflareRegistrarSandbox = true
	cfg.Clouds.ACMEDirectory = letsEncryptStaging
	if configure != nil {
		configure(&cfg)
	}
	r := start(t, cfg)
	// Cleanups run last first: everything registered after this runs
	// before Loomplane stops.
	t.Cleanup(r.stop)
	c := &client{t: t, base: r.srv.URL, key: cfg.Identity.PublishableKey}
	signedIn(t, c)
	return c, cfg
}

// domainsCall is client.do with the response body in the failure, which is what
// you need to read a live refusal.
func domainsCall(t *testing.T, c *client, want int, method, path string, body, out any) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(c.context(), method, c.base+path, rd)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Publishable-Key", c.key)
	req.Header.Set("Authorization", "Bearer "+c.token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != want {
		t.Fatalf("%s %s: status %d, want %d: %s", method, path, res.StatusCode, want, raw)
	}
	if out != nil && len(raw) > 0 {
		if err = json.Unmarshal(raw, out); err != nil {
			t.Fatalf("%s %s: decode %q: %v", method, path, raw, err)
		}
	}
}

type domainsConnection struct {
	ID      string            `json:"id"`
	Status  string            `json:"status"`
	Account string            `json:"account"`
	Missing []string          `json:"missing"`
	Message string            `json:"message"`
	Details map[string]string `json:"details"`
}

func connectDomains(t *testing.T, c *client, cloud, name, credential string, settings map[string]string) domainsConnection {
	t.Helper()
	var conn domainsConnection
	domainsCall(t, c, http.StatusCreated, http.MethodPost, "/api/v1/connections",
		map[string]any{"cloud": cloud, "name": name, "credential": credential, "settings": settings}, &conn)
	if conn.Status != "ready" {
		t.Fatalf("the %s connection is %s: missing %v %s", name, conn.Status, conn.Missing, conn.Message)
	}
	t.Logf("connected %s as %s", name, conn.Account)
	return conn
}

func randomLabel(t *testing.T, prefix string) string {
	t.Helper()
	b := make([]byte, 5)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return prefix + hex.EncodeToString(b)
}

func domainsTimeout(t *testing.T) time.Duration {
	t.Helper()
	timeout := 30 * time.Minute
	if v := acceptanceEnv("TIMEOUT"); v != "" {
		var err error
		if timeout, err = time.ParseDuration(v); err != nil {
			t.Fatalf("LOOMPLANE_ACCEPTANCE_TIMEOUT: %v", err)
		}
	}
	return timeout
}

// acceptanceRegistrar registers a .com in the Cloudflare Registrar sandbox,
// which never charges anyone and creates no zone.
func acceptanceRegistrar(t *testing.T) {
	vars, missing := domainsVars("LOOMPLANE_CF_SANDBOX_TOKEN", "LOOMPLANE_CF_ACCOUNT_ID")
	skipDomainsGap(t, missing)
	// A sandbox registration writes nothing to the real account's zones.
	guard := newCloudflareGuard(t, false)
	c, _ := domainsApp(t, guard, nil)

	conn := connectDomains(t, c, "cloudflare", "Cloudflare sandbox", vars["LOOMPLANE_CF_SANDBOX_TOKEN"], map[string]string{"account_id": vars["LOOMPLANE_CF_ACCOUNT_ID"]})
	if conn.Details["registrar_mode"] != "sandbox" {
		t.Fatalf("the connection's registrar mode is %q; refusing to go on outside the sandbox", conn.Details["registrar_mode"])
	}
	if conn.Details["registrar"] != "granted" {
		t.Fatalf("the token's registrar permission is %q; it needs Registrar read and edit", conn.Details["registrar"])
	}
	probes := guard.sandboxOnly(t)
	if len(probes) == 0 {
		t.Fatal("the permission check made no registrar-sandbox request")
	}
	for _, request := range probes {
		if strings.HasPrefix(request, http.MethodPost+" ") {
			t.Fatalf("the permission check sent %s; only an approved purchase may", request)
		}
	}

	var extensions cloudinfra.RegistrationExtensionList
	domainsCall(t, c, http.StatusOK, http.MethodGet, "/api/v1/domain-registration/extensions?connection_id="+url.QueryEscape(conn.ID), nil, &extensions)
	if !slices.Contains(extensions.Extensions, "com") {
		t.Fatalf("the sandbox doesn't list com: %v", extensions.Extensions)
	}
	label := randomLabel(t, "loomplane-acceptance-")
	name := label + ".com"
	var found cloudinfra.DomainOfferList
	domainsCall(t, c, http.StatusOK, http.MethodGet, "/api/v1/domain-registration/search?connection_id="+url.QueryEscape(conn.ID)+"&q="+url.QueryEscape(label), nil, &found)
	t.Logf("search for %s found %d names", label, len(found.Offers))
	var checked cloudinfra.DomainOfferList
	domainsCall(t, c, http.StatusOK, http.MethodPost, "/api/v1/domain-registration/check", cloudinfra.DomainCheckInput{ConnectionID: conn.ID, Domains: []string{name}}, &checked)
	i := slices.IndexFunc(checked.Offers, func(o provider.DomainOffer) bool { return o.Name == name })
	if i < 0 || !checked.Offers[i].Registrable || checked.Offers[i].Price == nil {
		t.Fatalf("the sandbox won't offer %s: %+v", name, checked.Offers)
	}
	price := *checked.Offers[i].Price

	autoRenew := true
	var reg cloudinfra.Registration
	domainsCall(t, c, http.StatusOK, http.MethodPost, "/api/v1/domain-registrations/preview", cloudinfra.RegistrationInput{ConnectionID: conn.ID, Name: name, AutoRenew: &autoRenew}, &reg)
	// Nothing below may write unless every check here passes.
	guard.sandboxOnly(t)
	if !reg.Sandbox || reg.Status != cloudinfra.RegistrationPreview || !reg.AutoRenew || reg.Name != name {
		t.Fatalf("the preview isn't a sandbox registration of %s with auto-renew on: %+v", name, reg)
	}
	if provider.MatchDomainPrice(price, reg.Quote) != nil {
		t.Fatalf("the preview quotes %+v, the check quoted %+v", reg.Quote, price)
	}
	t.Logf("previewed %s for %s %s", name, reg.Quote.Currency, reg.Quote.RegistrationCost)
	before := len(guard.sandboxOnly(t))

	domainsCall(t, c, http.StatusAccepted, http.MethodPost, "/api/v1/domain-registrations/"+reg.ID+"/approve",
		cloudinfra.RegistrationApproval{Review: reg.Review, Currency: reg.Quote.Currency, RegistrationCost: reg.Quote.RegistrationCost}, &reg)
	for deadline := time.Now().Add(10 * time.Minute); reg.Status != cloudinfra.RegistrationSucceededStatus && reg.Status != cloudinfra.RegistrationZonePending; {
		switch reg.Status {
		case cloudinfra.RegistrationApproved, cloudinfra.RegistrationRegistering:
		default:
			t.Fatalf("the registration ended %s: %s", reg.Status, reg.Message)
		}
		if time.Now().After(deadline) {
			t.Fatalf("the registration is still %s: %s", reg.Status, reg.Message)
		}
		time.Sleep(5 * time.Second)
		domainsCall(t, c, http.StatusOK, http.MethodGet, "/api/v1/domain-registrations/"+reg.ID, nil, &reg)
	}
	t.Logf("%s is %s, expires %s: %s", name, reg.Status, reg.DomainExpiresAt.Format(time.DateOnly), reg.Message)
	if !reg.AutoRenew {
		t.Fatal("auto-renew is off after registration")
	}

	domainsCall(t, c, http.StatusOK, http.MethodPost, "/api/v1/domain-registrations/"+reg.ID+"/auto-renew", cloudinfra.AutoRenewInput{AutoRenew: false, Revision: reg.Revision}, &reg)
	if reg.AutoRenew {
		t.Fatal("the sandbox still reports auto-renew on")
	}
	domainsCall(t, c, http.StatusOK, http.MethodGet, "/api/v1/domain-registrations/"+reg.ID, nil, &reg)
	if reg.AutoRenew {
		t.Fatal("auto-renew reads back on")
	}

	purchases := 0
	for _, request := range guard.sandboxOnly(t)[before:] {
		if request == http.MethodPost+" /client/v4/accounts/"+vars["LOOMPLANE_CF_ACCOUNT_ID"]+"/registrar-sandbox/registrations" {
			purchases++
		}
	}
	if purchases != 1 {
		t.Fatalf("%d registration requests reached the sandbox after approval, want one", purchases)
	}
	// The zone hand-off may still be running; give it a moment, then make
	// sure it didn't try to create the zone.
	time.Sleep(10 * time.Second)
	guard.sandboxOnly(t)
}

// acceptanceRoutes deploys a service to a disposable cluster and gives it
// an exact and a wildcard hostname in a real Cloudflare zone, with
// certificates from Let's Encrypt staging.
//
// The cluster needs cert-manager 1.12 or later and an ingress controller
// that the internet reaches at LOOMPLANE_ACCEPTANCE_INGRESS_ADDRESS on
// ports 80 and 443, and that reports that address on its Ingresses:
// Let's Encrypt answers the exact hostname's HTTP-01 challenge there.
func acceptanceRoutes(t *testing.T) {
	vars, missing := domainsVars("LOOMPLANE_CF_SANDBOX_TOKEN", "LOOMPLANE_CF_ACCOUNT_ID", "LOOMPLANE_ACCEPTANCE_ZONE", "LOOMPLANE_ACCEPTANCE_KUBECONFIG",
		"LOOMPLANE_ACCEPTANCE_CF_CHALLENGE_TOKEN", "LOOMPLANE_ACCEPTANCE_INGRESS_ADDRESS")
	skipDomainsGap(t, missing)
	kubeconfig, err := os.ReadFile(vars["LOOMPLANE_ACCEPTANCE_KUBECONFIG"])
	if err != nil {
		t.Fatalf("read the kubeconfig: %v", err)
	}
	zoneName := strings.ToLower(strings.TrimSuffix(vars["LOOMPLANE_ACCEPTANCE_ZONE"], "."))
	timeout := domainsTimeout(t)

	guard := newCloudflareGuard(t, true)
	source := tarball(t, "routes-"+exampleSHA, map[string]string{
		"go.mod":          "module example.test/routes\n\ngo 1.26\n",
		"cmd/web/main.go": "package main\n\nimport \"net/http\"\n\nfunc main() {\n\t_ = http.ListenAndServe(\":80\", http.NotFoundHandler())\n}\n",
	})
	gh := fakeGitHubRepos(t, map[string][]byte{"acme/routes": source})
	c, cfg := domainsApp(t, guard, func(cfg *config.Config) { cfg.GitHub.APIURL = gh.URL })
	token, account := vars["LOOMPLANE_CF_SANDBOX_TOKEN"], vars["LOOMPLANE_CF_ACCOUNT_ID"]
	cleanupContext := func() context.CancelFunc {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), timeout)
		c.ctx = ctx
		return cancel
	}

	dns := connectDomains(t, c, "cloudflare", "Acceptance zone", token, map[string]string{"account_id": account})
	challenge := connectDomains(t, c, "cloudflare", "Acceptance DNS challenge", vars["LOOMPLANE_ACCEPTANCE_CF_CHALLENGE_TOKEN"], map[string]string{"account_id": account})
	settings := map[string]string{"ingress_address": vars["LOOMPLANE_ACCEPTANCE_INGRESS_ADDRESS"]}
	for setting, name := range map[string]string{"context": "KUBE_CONTEXT", "namespace": "KUBE_NAMESPACE", "ingress_class": "INGRESS_CLASS"} {
		if v := acceptanceEnv(name); v != "" {
			settings[setting] = v
		}
	}
	cluster := connectDomains(t, c, "kubernetes", "Acceptance cluster", string(kubeconfig), settings)
	if cluster.Details["route_prerequisites"] != "ready" {
		t.Fatalf("the cluster can't take routes: %s", cluster.Details["route_prerequisites"])
	}

	var project struct {
		ID string `json:"id"`
	}
	domainsCall(t, c, http.StatusCreated, http.MethodPost, "/api/v1/projects", map[string]string{"name": "Domains acceptance", "repository_url": "https://github.com/acme/routes"}, &project)
	base := "/api/v1/projects/" + project.ID
	analyze(t, cfg, c, project.ID)
	var envs struct {
		Environments []struct{ ID, Name string } `json:"environments"`
	}
	domainsCall(t, c, http.StatusOK, http.MethodGet, base+"/environments", nil, &envs)
	env := slices.IndexFunc(envs.Environments, func(e struct{ ID, Name string }) bool { return e.Name == "production" })
	if env < 0 {
		t.Fatalf("no production environment: %+v", envs.Environments)
	}
	environment := envs.Environments[env].ID

	var zone cloudinfra.Zone
	domainsCall(t, c, http.StatusCreated, http.MethodPost, "/api/v1/dns-zones", cloudinfra.ZoneInput{Name: zoneName, Mode: "managed", ConnectionID: dns.ID, ProjectIDs: []string{project.ID}}, &zone)
	t.Cleanup(func() {
		defer cleanupContext()()
		var current cloudinfra.Zone
		domainsCall(t, c, http.StatusOK, http.MethodPost, "/api/v1/dns-zones/"+zone.ID+"/challenge-credential", cloudinfra.ChallengeCredentialInput{Revision: latestZone(t, c, zone.ID).Revision}, &current)
		domainsCall(t, c, http.StatusNoContent, http.MethodPost, "/api/v1/dns-zones/"+zone.ID+"/remove", map[string]int64{"revision": current.Revision}, nil)
	})
	domainsCall(t, c, http.StatusOK, http.MethodPost, "/api/v1/dns-zones/"+zone.ID+"/challenge-credential", cloudinfra.ChallengeCredentialInput{ConnectionID: challenge.ID, Revision: zone.Revision}, &zone)
	if !zone.Wildcards() {
		t.Fatalf("the zone takes no wildcards after attaching the challenge credential: %+v", zone)
	}

	// The backend is the pinned multi-platform Caddy image, which answers on
	// port 80 out of the box. The source is only there to be analyzed.
	manifest := fmt.Sprintf(`version: 1
services:
  web:
    source: go:cmd/web
    kind: http-service
    provider: kubernetes
    target: %s
    public: false
    port: 80
    health_path: /
    scale: {min: 1, max: 1}
    build: {image: %s}
`, cluster.ID, server.LegacyIngressImage)
	if d := domainsDeploy(t, c, base, manifest, timeout); d.Status != "succeeded" {
		t.Fatalf("the deployment %s: %s", d.Status, d.Error)
	}
	t.Cleanup(func() {
		defer cleanupContext()()
		if d := domainsDeploy(t, c, base, "version: 1\n", timeout); d.Status != "succeeded" {
			t.Errorf("the teardown %s: %s. Check the namespace for what's left", d.Status, d.Error)
		}
	})

	exact := randomLabel(t, "lp-acceptance-") + "." + zoneName
	wildcard := "*." + randomLabel(t, "lp-wildcard-") + "." + zoneName
	routes := map[string]string{}
	t.Cleanup(func() {
		defer cleanupContext()()
		for hostname, id := range routes {
			if route := liveRoute(t, c, base, id); route != nil {
				t.Logf("removing %s after a failed run", hostname)
				var op cloudinfra.RouteOperation
				domainsCall(t, c, http.StatusAccepted, http.MethodPost, base+"/domain-routes/"+id+"/remove", map[string]int64{"revision": route.Revision}, &op)
				waitRouteOperation(t, c, base, op.ID, timeout)
			}
		}
	})
	for _, hostname := range []string{exact, wildcard} {
		var op cloudinfra.RouteOperation
		domainsCall(t, c, http.StatusOK, http.MethodPost, base+"/domain-routes/preview",
			cloudinfra.RouteInput{ZoneID: zone.ID, EnvironmentID: environment, Service: "web", Hostname: hostname, Port: 80}, &op)
		if op.Route.Binding.BackendName() != cloudinfra.RouteBackendKubernetes {
			t.Fatalf("%s previews on %q, not Kubernetes", hostname, op.Route.Binding.BackendName())
		}
		t.Logf("%s previews %s %s -> %s %v", hostname, op.DesiredRecord.Type, op.DesiredRecord.Name, op.DesiredRecord.Value, op.Notices)
		domainsCall(t, c, http.StatusAccepted, http.MethodPost, base+"/route-operations/"+op.ID+"/apply", cloudinfra.ApplyInput{Review: op.Review}, &op)
		routes[hostname] = op.Route.ID
	}
	for hostname, id := range routes {
		status := waitRouteReady(t, c, base, id, timeout)
		t.Logf("%s is ready, certificate until %s", hostname, status.CertificateExpiresAt.Format(time.DateOnly))
	}

	for hostname, id := range routes {
		route := liveRoute(t, c, base, id)
		if route == nil {
			t.Fatalf("%s disappeared before it was removed", hostname)
		}
		var op cloudinfra.RouteOperation
		domainsCall(t, c, http.StatusAccepted, http.MethodPost, base+"/domain-routes/"+id+"/remove", map[string]int64{"revision": route.Revision}, &op)
		waitRouteOperation(t, c, base, op.ID, timeout)
		t.Logf("removed %s", hostname)
	}

	// Read the zone straight from Cloudflare: nothing Loomplane wrote for
	// these routes may be left, proxied or not.
	cf := cloudflare.New(guard.endpoint(), true)
	cred := provider.InfrastructureCredential{Provider: "cloudflare", AccountID: account, Token: token}
	providerZone, err := cf.GetZone(t.Context(), cred, zoneName)
	if err != nil {
		t.Fatalf("read the zone back: %v", err)
	}
	records, err := cf.ListRecords(t.Context(), cred, providerZone)
	if err != nil {
		t.Fatalf("list the zone's records: %v", err)
	}
	names := map[string]bool{strings.TrimSuffix(exact, "."+zoneName): true, strings.TrimSuffix(wildcard, "."+zoneName): true}
	for _, record := range records {
		owned := false
		for _, id := range routes {
			owned = owned || record.Owner == "loomplane:route:"+id
		}
		if owned || ((record.Type == "A" || record.Type == "AAAA" || record.Type == "CNAME") && names[record.Name]) {
			t.Errorf("the zone still holds %s %s -> %s (owner %q)", record.Type, record.Name, record.Value, record.Owner)
		}
	}
}

func latestZone(t *testing.T, c *client, id string) cloudinfra.Zone {
	t.Helper()
	var zones struct {
		Zones []cloudinfra.Zone `json:"zones"`
	}
	domainsCall(t, c, http.StatusOK, http.MethodGet, "/api/v1/dns-zones", nil, &zones)
	for _, zone := range zones.Zones {
		if zone.ID == id {
			return zone
		}
	}
	t.Fatalf("zone %s isn't listed", id)
	return cloudinfra.Zone{}
}

// liveRoute is the route unless it's gone or removed.
func liveRoute(t *testing.T, c *client, base, id string) *cloudinfra.Route {
	t.Helper()
	var list struct {
		Routes []*cloudinfra.Route `json:"routes"`
	}
	domainsCall(t, c, http.StatusOK, http.MethodGet, base+"/domain-routes", nil, &list)
	for _, route := range list.Routes {
		if route.ID == id && route.Status != "removed" {
			return route
		}
	}
	return nil
}

func waitRouteReady(t *testing.T, c *client, base, id string, timeout time.Duration) cloudinfra.RouteStatus {
	t.Helper()
	var status cloudinfra.RouteStatus
	for deadline := time.Now().Add(timeout); ; {
		domainsCall(t, c, http.StatusOK, http.MethodGet, base+"/domain-routes/"+id+"/status", nil, &status)
		if status.DNS == cloudinfra.DomainReady && status.Certificate == cloudinfra.DomainReady && status.Route == cloudinfra.DomainReady {
			return status
		}
		if status.Route == cloudinfra.DomainFailed {
			t.Fatalf("%s failed: dns %s, certificate %s: %s", status.Hostname, status.DNS, status.Certificate, status.Message)
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s after %s: dns %s, certificate %s, route %s: %s", status.Hostname, timeout, status.DNS, status.Certificate, status.Route, status.Message)
		}
		time.Sleep(15 * time.Second)
	}
}

func waitRouteOperation(t *testing.T, c *client, base, id string, timeout time.Duration) {
	t.Helper()
	var op cloudinfra.RouteOperation
	for deadline := time.Now().Add(timeout); op.Status != "succeeded"; {
		domainsCall(t, c, http.StatusOK, http.MethodGet, base+"/route-operations/"+id, nil, &op)
		if op.Status == "failed" || op.Status == "cancelled" {
			t.Fatalf("%s of %s %s: %s", op.Kind, op.Route.Input.Hostname, op.Status, op.Message)
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s of %s still %s after %s: %s", op.Kind, op.Route.Input.Hostname, op.Status, timeout, op.Message)
		}
		time.Sleep(5 * time.Second)
	}
}

// domainsDeploy plans the manifest for production, approves it and waits.
func domainsDeploy(t *testing.T, c *client, base, yaml string, timeout time.Duration) deploymentBody {
	t.Helper()
	domainsCall(t, c, http.StatusCreated, http.MethodPut, base+"/manifest", map[string]string{"yaml": yaml}, nil)
	var plan planBody
	domainsCall(t, c, http.StatusCreated, http.MethodPost, base+"/plans", map[string]string{"environment": "production"}, &plan)
	waitPlanned(t, c, &plan)
	if !plan.Executable {
		t.Fatalf("the plan can't be carried out: %+v", plan.Problems)
	}
	domainsCall(t, c, http.StatusOK, http.MethodPost, "/api/v1/plans/"+plan.ID+"/approve", map[string]string{"revision": plan.Revision}, nil)
	var d deploymentBody
	for deadline := time.Now().Add(timeout); d.Status == "" || d.Status == "running"; {
		if time.Now().After(deadline) {
			t.Fatalf("the deployment is still %q after %s", d.Status, timeout)
		}
		time.Sleep(5 * time.Second)
		if c.do(http.MethodGet, "/api/v1/plans/"+plan.ID+"/deployment", nil, &d) == http.StatusNotFound {
			d.Status = ""
		}
	}
	for _, s := range d.Steps {
		t.Logf("%s %s %s", s.ActionID, s.Status, s.Message)
	}
	return d
}
