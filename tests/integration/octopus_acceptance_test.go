//go:build integration && acceptance

package integration

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/xraph/ctrlplane/id"
	"github.com/xraph/forge"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/xraph/loomplane/internal/catalogproxy"
	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/internal/clouds/owned"
	"github.com/xraph/loomplane/internal/clouds/server"
	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/octopusloom"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/recipes"
)

// The Octopus acceptance run deploys Octopus 0.5 and two services to a
// disposable SSH/Docker server and a disposable kind cluster, and checks
// what the fakes can't: that FARP discovery finds a real service, that a
// route added in Loomplane overrides it through Octopus's private port and
// that deleting it brings the FARP route back, that the reconcile puts the
// route back after Octopus restarts, and that the admin opens through the
// sandboxed admin proxy. Then it removes everything through the product.
//
// Each target skips on its own, naming what it still needs:
//
//	LOOMPLANE_ACCEPTANCE_SERVER      a JSON file {"credential": "<SSH private key, or the
//	                                 {private_key, passphrase} JSON>", "settings": {"host",
//	                                 "user", "port", "host_key", "docker_socket"}} for a
//	                                 disposable Docker server, as the server connection takes it
//	LOOMPLANE_ACCEPTANCE_KUBECONFIG  a kubeconfig for a disposable kind cluster, with embedded
//	                                 credentials that can create serviceaccounts, roles and
//	                                 rolebindings and use services/proxy in the namespace
//	                                 (LOOMPLANE_ACCEPTANCE_KUBE_CONTEXT and _KUBE_NAMESPACE pick
//	                                 them; the kubeconfig's own otherwise)
//	LOOMPLANE_ACCEPTANCE_FARP_IMAGE  the FARP sample service: an image pinned by digest that
//	                                 serves an OpenAPI 3 document at /openapi.json listing at
//	                                 least one GET path without parameters, answers that path
//	                                 with a 2xx, listens on LOOMPLANE_ACCEPTANCE_FARP_PORT (8080
//	                                 by default) and isn't Caddy, which the run tells apart from
//	                                 the manual route's target
//
// With LOOMPLANE_ACCEPTANCE_ZONE set, the cluster also gets a hostname in
// that Cloudflare zone, with a certificate from Let's Encrypt staging, and
// the run checks HTTPS reaches Octopus and /admin there is blocked. That
// needs LOOMPLANE_CF_SANDBOX_TOKEN, LOOMPLANE_CF_ACCOUNT_ID and
// LOOMPLANE_ACCEPTANCE_INGRESS_ADDRESS as the domains run does
// (docs/domains.md), and cert-manager and an ingress controller in the
// cluster. A server serves hostnames only through a Cloud, and this run
// deploys to a bare connection, so the server has no domain check.
//
// Run it with:
//
//	LOOMPLANE_TEST_DATABASE_URL=... go test -tags='integration acceptance' \
//	  -count=1 -timeout=90m -run TestAcceptanceOctopus -v ./tests/integration/

const (
	octoResource = "edge"
	octoFarp     = "api"
	octoManual   = "manual"
	// octoManualPort is the port Caddy's default config listens on.
	octoManualPort = 80
	// octoRestored is how long the reconcile has to put a route back once
	// Octopus answers again after a restart.
	octoRestored = 60 * time.Second
)

// octoTarget is one target the run deploys to.
type octoTarget struct {
	provider   string
	credential string
	settings   map[string]string
	missing    []string
}

func octoServerTarget(image string) octoTarget {
	target := octoTarget{provider: "server"}
	if image == "" {
		target.missing = append(target.missing, "LOOMPLANE_ACCEPTANCE_FARP_IMAGE")
	}
	path := acceptanceEnv("SERVER")
	if path == "" {
		target.missing = append([]string{"LOOMPLANE_ACCEPTANCE_SERVER"}, target.missing...)
		return target
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		target.missing = append([]string{"LOOMPLANE_ACCEPTANCE_SERVER (" + err.Error() + ")"}, target.missing...)
		return target
	}
	var file struct {
		Credential string            `json:"credential"`
		Settings   map[string]string `json:"settings"`
	}
	if err = json.Unmarshal(raw, &file); err != nil || file.Credential == "" || file.Settings["host"] == "" {
		target.missing = append([]string{"LOOMPLANE_ACCEPTANCE_SERVER (a JSON file with credential and settings.host)"}, target.missing...)
		return target
	}
	target.credential, target.settings = file.Credential, file.Settings
	return target
}

func octoClusterTarget(image string) octoTarget {
	target := octoTarget{provider: "kubernetes", settings: map[string]string{}}
	path := acceptanceEnv("KUBECONFIG")
	if path == "" {
		target.missing = append(target.missing, "LOOMPLANE_ACCEPTANCE_KUBECONFIG")
	} else if raw, err := os.ReadFile(path); err != nil {
		target.missing = append(target.missing, "LOOMPLANE_ACCEPTANCE_KUBECONFIG ("+err.Error()+")")
	} else {
		target.credential = string(raw)
	}
	if image == "" {
		target.missing = append(target.missing, "LOOMPLANE_ACCEPTANCE_FARP_IMAGE")
	}
	for setting, name := range map[string]string{"context": "KUBE_CONTEXT", "namespace": "KUBE_NAMESPACE", "ingress_class": "INGRESS_CLASS", "ingress_address": "INGRESS_ADDRESS"} {
		if v := acceptanceEnv(name); v != "" {
			target.settings[setting] = v
		}
	}
	return target
}

// octoDomainVars are what the cluster's domain check needs, and the ones
// that are unset. Without LOOMPLANE_ACCEPTANCE_ZONE there's no check.
func octoDomainVars() (map[string]string, []string) {
	return domainsVars("LOOMPLANE_ACCEPTANCE_ZONE", "LOOMPLANE_CF_SANDBOX_TOKEN", "LOOMPLANE_CF_ACCOUNT_ID", "LOOMPLANE_ACCEPTANCE_INGRESS_ADDRESS")
}

func skipOctoGap(t *testing.T, missing []string) {
	t.Helper()
	if len(missing) > 0 {
		t.Skipf("gap: set %s; the comment on TestAcceptanceOctopus says what each is", strings.Join(missing, ", "))
	}
}

func TestAcceptanceOctopus(t *testing.T) {
	image := acceptanceEnv("FARP_IMAGE")
	farpPort := 8080
	if v := acceptanceEnv("FARP_PORT"); v != "" {
		if _, err := fmt.Sscan(v, &farpPort); err != nil || farpPort < 1 || farpPort > 65535 {
			t.Fatalf("LOOMPLANE_ACCEPTANCE_FARP_PORT %q isn't a port", v)
		}
	}
	targets := []octoTarget{octoServerTarget(image), octoClusterTarget(image)}
	if !slices.ContainsFunc(targets, func(x octoTarget) bool { return len(x.missing) == 0 }) {
		// Nothing to deploy to: each target says what it needs, and no
		// database or app is started.
		for _, target := range targets {
			t.Run(target.provider, func(t *testing.T) { skipOctoGap(t, target.missing) })
		}
		return
	}

	domainVars, domainMissing := octoDomainVars()
	var guard *cloudflareGuard
	if domainVars["LOOMPLANE_ACCEPTANCE_ZONE"] != "" && len(domainMissing) == 0 {
		guard = newCloudflareGuard(t, true)
	}
	source := tarball(t, "octopus-"+exampleSHA, map[string]string{
		"go.mod":             "module example.test/octopus\n\ngo 1.26\n",
		"cmd/api/main.go":    "package main\n\nimport \"net/http\"\n\nfunc main() {\n\t_ = http.ListenAndServe(\":8080\", http.NotFoundHandler())\n}\n",
		"cmd/manual/main.go": "package main\n\nimport \"net/http\"\n\nfunc main() {\n\t_ = http.ListenAndServe(\":80\", http.NotFoundHandler())\n}\n",
	})
	gh := fakeGitHubRepos(t, map[string][]byte{"acme/octopus": source})
	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	cfg.Orchestration.PollInterval = time.Second
	cfg.Analysis.WorkDir = t.TempDir()
	cfg.Storage.URL = "file://" + t.TempDir()
	cfg.GitHub.APIURL = gh.URL
	if guard != nil {
		cfg.Clouds.CloudflareEndpoint = guard.endpoint()
		cfg.Clouds.CloudflareRegistrarSandbox = true
		cfg.Clouds.ACMEDirectory = letsEncryptStaging
	}
	r := start(t, cfg)
	// Cleanups run last first: everything registered after this runs
	// before Loomplane stops.
	t.Cleanup(r.stop)
	c := &client{t: t, base: r.srv.URL, key: cfg.Identity.PublishableKey}
	var auth struct {
		SessionToken string `json:"session_token"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/authsome/v1/signup", map[string]string{"email": "octopus-acceptance@example.test", "password": "Acceptance-Octopus-42"}, &auth)
	c.token = auth.SessionToken
	var org struct {
		ID string `json:"id"`
	}
	c.must(http.StatusCreated, http.MethodPost, "/authsome/v1/orgs", map[string]string{"name": "Octopus acceptance", "slug": "octopus-acceptance"}, &org)
	c.must(http.StatusOK, http.MethodPost, "/authsome/v1/me/switch-org", map[string]string{"org_id": org.ID}, nil)
	access, err := forge.InjectType[*deployments.CatalogAccess](r.app.(forge.App).Container())
	if err != nil {
		t.Fatal(err)
	}

	for _, target := range targets {
		t.Run(target.provider, func(t *testing.T) {
			skipOctoGap(t, target.missing)
			run := &octoRun{
				t: t, c: &client{t: t, base: c.base, token: c.token, key: c.key}, cfg: cfg, base: r.srv.URL, orgID: org.ID,
				target: target, image: image, farpPort: farpPort, access: access, guard: guard, domainVars: domainVars, domainMissing: domainMissing,
			}
			run.roundTrip()
		})
	}
}

// octoRun deploys Octopus and the two services to one target, checks it
// and removes it.
type octoRun struct {
	t             *testing.T
	c             *client
	cfg           config.Config
	base, orgID   string
	target        octoTarget
	image         string
	farpPort      int
	access        *deployments.CatalogAccess
	guard         *cloudflareGuard
	domainVars    map[string]string
	domainMissing []string

	connection    domainsConnection
	projectID     string
	environmentID string
	gateway       id.ID
	namespace     string
	// get sends a GET through Octopus's gateway port over the target's
	// private channel.
	get func(ctx context.Context, path string) (octoAnswer, error)
	// restart restarts Octopus's container or pod.
	restart func(ctx context.Context) error
}

// octoAnswer is one answer through Octopus.
type octoAnswer struct {
	Status int
	Server string
	Body   string
}

// manual reports whether the answer came from the manual route's Caddy.
func (a octoAnswer) manual() bool {
	return strings.Contains(a.Server, "Caddy") || strings.Contains(a.Body, "Caddy")
}

// farp reports whether the answer came from the FARP sample service.
func (a octoAnswer) farp() bool { return a.Status >= 200 && a.Status < 300 && !a.manual() }

func (x *octoRun) roundTrip() {
	t, c := x.t, x.c
	x.connection = connectDomains(t, c, x.target.provider, "Octopus acceptance "+x.target.provider, x.target.credential, x.target.settings)
	if x.target.provider == "kubernetes" {
		for _, detail := range []string{"rbac", "service_proxy"} {
			if x.connection.Details[detail] != "ready" {
				t.Fatalf("the cluster connection's %s isn't ready (%q): Octopus needs it", detail, x.connection.Details[detail])
			}
		}
	}

	var project struct {
		ID string `json:"id"`
	}
	domainsCall(t, c, http.StatusCreated, http.MethodPost, "/api/v1/projects", map[string]string{"name": "Octopus on " + x.target.provider, "repository_url": "https://github.com/acme/octopus"}, &project)
	x.projectID = project.ID
	base := "/api/v1/projects/" + x.projectID
	analyze(t, x.cfg, c, x.projectID)
	var envs struct {
		Environments []struct{ ID, Name string } `json:"environments"`
	}
	domainsCall(t, c, http.StatusOK, http.MethodGet, base+"/environments", nil, &envs)
	i := slices.IndexFunc(envs.Environments, func(e struct{ ID, Name string }) bool { return e.Name == "production" })
	if i < 0 {
		t.Fatalf("no production environment: %+v", envs.Environments)
	}
	x.environmentID = envs.Environments[i].ID

	timeout := domainsTimeout(t)
	// The teardown runs whatever happens below, a failed deploy included.
	t.Cleanup(x.teardown)
	if d := domainsDeploy(t, c, base, x.manifest(), capped(t, timeout, cleanupMargin)); d.Status != "succeeded" {
		t.Fatalf("the deployment %s: %s", d.Status, d.Error)
	}
	x.find()
	t.Logf("Octopus %s runs as %s on %s", octoResource, x.gateway, x.target.provider)

	farpPath, request := x.waitFarpRoute()
	baseline := x.waitAnswer(request, 2*time.Minute, "the FARP route to answer", octoAnswer.farp)
	t.Logf("check 1 PASS: FARP published %s for %s, and GET %s through Octopus answered HTTP %d", farpPath, octoFarp, request, baseline.Status)

	route := x.addRoute(farpPath)
	answer := x.waitAnswer(request, 30*time.Second, "the manual route to answer", octoAnswer.manual)
	x.checkOverride(route.ID)
	t.Logf("check 2 PASS: the manual route %s overrides FARP's %s; GET %s answered HTTP %d from %s", route.ID, farpPath, request, answer.Status, octoManual)

	x.removeRoute(route)
	answer = x.waitAnswer(request, 30*time.Second, "the FARP route to answer again", octoAnswer.farp)
	t.Logf("check 3 PASS: with the manual route deleted, GET %s answered HTTP %d from %s again", request, answer.Status, octoFarp)

	kept := x.addRoute(farpPath)
	x.waitAnswer(request, 30*time.Second, "the second manual route to answer", octoAnswer.manual)
	x.checkRestore(request)
	x.removeRoute(kept)

	x.checkAdmin()
	x.checkDomain(request)
}

func (x *octoRun) manifest() string {
	return fmt.Sprintf(`version: 1
resources:
  %[1]s:
    kind: octopus
    version: "0.5"
    provider: %[2]s
    target: %[3]s
services:
  %[4]s:
    source: go:cmd/api
    kind: http-service
    provider: %[2]s
    target: %[3]s
    public: false
    port: %[5]d
    health_path: /openapi.json
    scale: {min: 1, max: 1}
    build: {image: %[6]s}
    farp:
      openapi: /openapi.json
  %[7]s:
    source: go:cmd/manual
    kind: http-service
    provider: %[2]s
    target: %[3]s
    public: false
    port: %[8]d
    health_path: /
    scale: {min: 1, max: 1}
    build: {image: %[9]s}
`, octoResource, x.target.provider, x.connection.ID, octoFarp, x.farpPort, x.image, octoManual, octoManualPort, server.LegacyIngressImage)
}

// find reads the gateway's binding and opens the private ways to it.
func (x *octoRun) find() {
	t := x.t
	var list struct {
		Bindings []*planning.Binding `json:"bindings"`
	}
	domainsCall(t, x.c, http.StatusOK, http.MethodGet, "/api/v1/projects/"+x.projectID+"/bindings?environment=production", nil, &list)
	name := octopusloom.GatewayAction(octoResource)
	i := slices.IndexFunc(list.Bindings, func(b *planning.Binding) bool { return b.Name == name })
	if i < 0 {
		t.Fatalf("no %s binding among %d", name, len(list.Bindings))
	}
	iid, err := id.Parse(list.Bindings[i].ExternalID)
	if err != nil {
		t.Fatalf("the gateway's instance id: %v", err)
	}
	x.gateway = iid
	status := x.status()
	x.namespace = status.Namespace
	t.Logf("gateway status: provider %s, namespace %q, private URL %s, generation %d applied %d", status.Provider, status.Namespace, status.PrivateURL, status.Generation, status.AppliedGeneration)

	if x.target.provider == "server" {
		serverCfg, parseErr := server.ParseConfig([]byte(x.target.credential), x.target.settings)
		if parseErr != nil {
			t.Fatalf("read the server credential: %v", parseErr)
		}
		container := server.ContainerName(iid)
		dial := func(ctx context.Context) (*server.Client, error) { return server.Dial(ctx, serverCfg) }
		x.get = func(ctx context.Context, path string) (octoAnswer, error) {
			client, dialErr := dial(ctx)
			if dialErr != nil {
				return octoAnswer{}, dialErr
			}
			defer func() { _ = client.Close() }()
			transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return client.DialContainerPort(ctx, container, recipes.OctopusGatewayPort)
			}}
			defer transport.CloseIdleConnections()
			return octoSend(ctx, transport, "http://gateway"+path)
		}
		x.restart = func(ctx context.Context) error {
			client, dialErr := dial(ctx)
			if dialErr != nil {
				return dialErr
			}
			defer func() { _ = client.Close() }()
			res, restartErr := client.Request(ctx, http.MethodPost, "/containers/"+container+"/restart?t=10", nil)
			if restartErr != nil {
				return restartErr
			}
			_ = res.Body.Close()
			if res.StatusCode != http.StatusNoContent {
				return fmt.Errorf("docker restart %s: HTTP %d", container, res.StatusCode)
			}
			return nil
		}
		return
	}
	cluster := x.cluster()
	transport, err := cluster.ServiceProxyTransport(x.namespace, kubernetes.WorkloadName(iid), recipes.OctopusGatewayPort)
	if err != nil {
		t.Fatalf("reach the gateway through the service proxy: %v", err)
	}
	x.get = func(ctx context.Context, path string) (octoAnswer, error) {
		return octoSend(ctx, transport, "http://gateway"+path)
	}
	x.restart = func(ctx context.Context) error {
		return cluster.Kube.CoreV1().Pods(x.namespace).DeleteCollection(ctx, metav1.DeleteOptions{}, metav1.ListOptions{LabelSelector: owned.LabelInstance + "=" + iid.String()})
	}
}

func (x *octoRun) cluster() *kubernetes.Client {
	x.t.Helper()
	cfg, err := kubernetes.ParseConfig([]byte(x.target.credential), x.target.settings)
	if err != nil {
		x.t.Fatalf("read the kubeconfig: %v", err)
	}
	client, err := kubernetes.New(cfg)
	if err != nil {
		x.t.Fatalf("connect to the cluster: %v", err)
	}
	return client
}

func octoSend(ctx context.Context, transport http.RoundTripper, target string) (octoAnswer, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, http.NoBody)
	if err != nil {
		return octoAnswer{}, err
	}
	res, err := (&http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(req)
	if err != nil {
		return octoAnswer{}, err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	if err != nil {
		return octoAnswer{}, err
	}
	return octoAnswer{Status: res.StatusCode, Server: res.Header.Get("Server"), Body: string(body)}, nil
}

func (x *octoRun) gatewayPath(rest string) string {
	return "/api/v1/projects/" + x.projectID + "/environments/" + x.environmentID + "/octopus/" + octoResource + rest
}

func (x *octoRun) status() octopusloom.OctopusStatus {
	var st octopusloom.OctopusStatus
	domainsCall(x.t, x.c, http.StatusOK, http.MethodGet, x.gatewayPath("/status"), nil, &st)
	return st
}

func (x *octoRun) board() octopusloom.Board {
	var b octopusloom.Board
	domainsCall(x.t, x.c, http.StatusOK, http.MethodGet, x.gatewayPath("/board"), nil, &b)
	return b
}

// waitFarpRoute waits for a FARP route of the sample service on the
// any-host card, and returns its path and a request path it answers.
func (x *octoRun) waitFarpRoute() (string, string) {
	t := x.t
	deadline := time.Now().Add(5 * time.Minute)
	for {
		b := x.board()
		for _, host := range b.Hosts {
			if host.Hostname != "" {
				continue
			}
			for _, row := range host.Routes {
				if row.Source != octopusloom.SourceFarp || row.Service != octoFarp || strings.ContainsAny(row.Path, "{:") {
					continue
				}
				request := strings.TrimSuffix(row.Path, "/*")
				if request == "" {
					request = "/"
				}
				t.Logf("board after FARP: live %v, discovery %+v", b.Live, b.Discovery.Services)
				return row.Path, request
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("no FARP route for %s on the board after 5 minutes: live %v (%s), discovery %+v, hosts %+v", octoFarp, b.Live, b.LiveError, b.Discovery, b.Hosts)
		}
		time.Sleep(5 * time.Second)
	}
}

// waitAnswer waits until a GET through Octopus answers as want says.
func (x *octoRun) waitAnswer(path string, within time.Duration, what string, want func(octoAnswer) bool) octoAnswer {
	t := x.t
	t.Helper()
	deadline := time.Now().Add(within)
	var last octoAnswer
	var lastErr error
	for {
		last, lastErr = x.get(t.Context(), path)
		if lastErr == nil && want(last) {
			return last
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: after %s GET %s answered HTTP %d (server %q) %.200q, error %v", what, within, path, last.Status, last.Server, last.Body, lastErr)
		}
		time.Sleep(2 * time.Second)
	}
}

func (x *octoRun) addRoute(path string) *octopusloom.Route {
	t := x.t
	var res octopusloom.RouteResult
	domainsCall(t, x.c, http.StatusCreated, http.MethodPost, x.gatewayPath("/routes"), octopusloom.RouteInput{
		Path: path, Service: octoManual, Port: octoManualPort, StripPrefix: true, Note: "acceptance override",
	}, &res)
	t.Logf("added route %s %s -> %s:%d: sync %s %s", res.Route.ID, path, octoManual, octoManualPort, res.Sync.State, res.Sync.Message)
	return res.Route
}

func (x *octoRun) removeRoute(route *octopusloom.Route) {
	t := x.t
	var res octopusloom.SyncResult
	domainsCall(t, x.c, http.StatusOK, http.MethodPost, x.gatewayPath("/routes/"+route.ID+"/remove"), map[string]int64{"revision": route.Revision}, &res)
	t.Logf("removed route %s: sync %s %s", route.ID, res.State, res.Message)
}

// checkOverride checks the board shows the manual route as overriding
// FARP's, with FARP's hidden.
func (x *octoRun) checkOverride(routeID string) {
	t := x.t
	for _, host := range x.board().Hosts {
		if host.Hostname != "" {
			continue
		}
		for _, row := range host.Routes {
			if row.RouteID == routeID {
				if !row.Overrides || row.Sync != octopusloom.SyncApplied {
					t.Fatalf("check 2: the manual route's row: %+v", row)
				}
				return
			}
		}
	}
	t.Fatalf("check 2: the manual route %s isn't on the any-host card", routeID)
}

// checkRestore restarts Octopus and checks the reconcile puts the manual
// route back within octoRestored of Octopus answering again.
func (x *octoRun) checkRestore(request string) {
	t := x.t
	before := x.status().StartTime
	// Without a start time to compare there's no telling the restarted
	// process apart from the old one.
	if before == "" {
		t.Fatal("check 4: Octopus reports no start time before the restart, so the run can't tell when it's back")
	}
	if err := x.restart(t.Context()); err != nil {
		t.Fatalf("check 4: restart Octopus: %v", err)
	}
	restarted := time.Now()
	// The status reads Octopus's start time live. It changes once the new
	// process answers, which a pod still terminating can't fake.
	deadline := restarted.Add(3 * time.Minute)
	after := before
	for after == before {
		if time.Now().After(deadline) {
			t.Fatalf("check 4: Octopus still reports start time %q 3 minutes after its restart", before)
		}
		time.Sleep(time.Second)
		after = x.status().StartTime
	}
	answering := time.Now()
	answer := x.waitAnswer(request, octoRestored, "check 4: the manual route to come back after the restart", octoAnswer.manual)
	back := time.Now()
	t.Logf("check 4 PASS: Octopus answered %s after the restart, and the manual route answered HTTP %d %s after that (start time %q -> %q)",
		answering.Sub(restarted).Round(time.Second), answer.Status, back.Sub(answering).Round(time.Second), before, after)
}

// checkAdmin opens the admin through the proxy and checks it renders
// sandboxed, without Octopus's cookies and without the password.
func (x *octoRun) checkAdmin() {
	t := x.t
	var session catalogproxy.Session
	domainsCall(t, x.c, http.StatusCreated, http.MethodPost, "/api/v1/projects/"+x.projectID+"/environments/"+x.environmentID+"/catalog/"+octoResource+"/admin-sessions", nil, &session)
	if !strings.HasPrefix(session.URL, "/catalog-admin/") {
		t.Fatalf("check 5: the session URL %q isn't under /catalog-admin/", session.URL)
	}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, x.base+session.URL, http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("check 5: open the admin: %v", err)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	_ = res.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	csp := res.Header.Get("Content-Security-Policy")
	if res.StatusCode != http.StatusOK || !strings.HasPrefix(csp, "sandbox") || !strings.Contains(strings.ToLower(string(body)), "<html") {
		t.Fatalf("check 5: GET %s ended at %s with HTTP %d, Content-Security-Policy %q: %.300q", session.URL, res.Request.URL.Path, res.StatusCode, csp, body)
	}
	if cookies := res.Header.Values("Set-Cookie"); len(cookies) > 0 {
		t.Fatalf("check 5: the proxied admin set cookies: %q", cookies)
	}
	password, err := x.access.InternalSecret(t.Context(), x.orgID, deployments.ResourceRef{ProjectID: x.projectID, EnvironmentID: x.environmentID, Resource: octoResource}, "admin-password")
	if err != nil || len(password) == 0 {
		t.Fatalf("check 5: read the generated admin password to look for it: %v", err)
	}
	if strings.Contains(string(body), string(password)) {
		t.Fatal("check 5: the admin page carries the admin password")
	}
	t.Logf("check 5 PASS: %s rendered at %s with HTTP 200, Content-Security-Policy %q, %d bytes, no cookies and no password", session.URL, res.Request.URL.Path, csp, len(body))
}

// checkDomain attaches a hostname to the gateway on the cluster and checks
// HTTPS reaches Octopus there and /admin doesn't.
func (x *octoRun) checkDomain(request string) {
	x.t.Run("domain", func(t *testing.T) {
		if x.target.provider != "kubernetes" {
			t.Skip("gap: a server serves hostnames only through a Cloud, and this run deploys to a bare connection")
		}
		if x.domainVars["LOOMPLANE_ACCEPTANCE_ZONE"] == "" {
			t.Skip("gap: set LOOMPLANE_ACCEPTANCE_ZONE to check a hostname on Octopus")
		}
		skipOctoGap(t, x.domainMissing)
		c := &client{t: t, base: x.c.base, token: x.c.token, key: x.c.key}
		base := "/api/v1/projects/" + x.projectID
		timeout := domainsTimeout(t)
		zoneName := strings.ToLower(strings.TrimSuffix(x.domainVars["LOOMPLANE_ACCEPTANCE_ZONE"], "."))
		dns := connectDomains(t, c, "cloudflare", "Octopus acceptance zone", x.domainVars["LOOMPLANE_CF_SANDBOX_TOKEN"], map[string]string{"account_id": x.domainVars["LOOMPLANE_CF_ACCOUNT_ID"]})
		var zone cloudinfra.Zone
		domainsCall(t, c, http.StatusCreated, http.MethodPost, "/api/v1/dns-zones", cloudinfra.ZoneInput{Name: zoneName, Mode: "managed", ConnectionID: dns.ID, ProjectIDs: []string{x.projectID}}, &zone)
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), capped(t, timeout, cleanupGrace))
			defer cancel()
			c.ctx = ctx
			domainsCall(t, c, http.StatusNoContent, http.MethodPost, "/api/v1/dns-zones/"+zone.ID+"/remove", map[string]int64{"revision": latestZone(t, c, zone.ID).Revision}, nil)
		})

		status := x.status()
		hostname := randomLabel(t, "lp-octopus-") + "." + zoneName
		var op cloudinfra.RouteOperation
		domainsCall(t, c, http.StatusOK, http.MethodPost, base+"/domain-routes/preview", cloudinfra.RouteInput{
			ZoneID: zone.ID, CloudID: status.Attach.CloudID, EnvironmentID: status.Attach.EnvironmentID, Service: status.Attach.Service,
			Hostname: hostname, Port: status.Attach.Port, BlockedPathPrefixes: status.Attach.BlockedPathPrefixes,
		}, &op)
		domainsCall(t, c, http.StatusAccepted, http.MethodPost, base+"/route-operations/"+op.ID+"/apply", cloudinfra.ApplyInput{Review: op.Review}, &op)
		routeID := op.Route.ID
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), capped(t, timeout, cleanupGrace))
			defer cancel()
			c.ctx = ctx
			if route := liveRoute(t, c, base, routeID); route != nil {
				var remove cloudinfra.RouteOperation
				domainsCall(t, c, http.StatusAccepted, http.MethodPost, base+"/domain-routes/"+routeID+"/remove", map[string]int64{"revision": route.Revision}, &remove)
				waitRouteOperation(t, c, base, remove.ID, capped(t, timeout, cleanupGrace))
				t.Logf("removed %s", hostname)
			}
		})
		ready := waitRouteReady(t, c, base, routeID, capped(t, timeout, cleanupMargin))
		t.Logf("%s is ready, certificate until %s", hostname, ready.CertificateExpiresAt.Format(time.DateOnly))

		address := x.domainVars["LOOMPLANE_ACCEPTANCE_INGRESS_ADDRESS"]
		answer := octoHTTPS(t, address, hostname, request)
		if !answer.farp() {
			t.Fatalf("check 6: GET https://%s%s answered HTTP %d (server %q) %.200q, not the FARP route", hostname, request, answer.Status, answer.Server, answer.Body)
		}
		admin := octoHTTPS(t, address, hostname, "/admin")
		if (admin.Status >= 200 && admin.Status < 400) || admin.Status == http.StatusUnauthorized {
			t.Fatalf("check 6: GET https://%s/admin reached Octopus: HTTP %d %.200q", hostname, admin.Status, admin.Body)
		}
		t.Logf("check 6 PASS: https://%s%s answered HTTP %d through Octopus, and /admin answered HTTP %d without reaching it", hostname, request, answer.Status, admin.Status)
		if x.guard != nil {
			x.guard.sandboxOnly(t)
		}
	})
}

// octoHTTPS sends a GET to hostname at the ingress address, so DNS
// doesn't have to have spread yet. The certificate is from Let's Encrypt
// staging, which nothing trusts, so the run checks it names the hostname
// instead of verifying its chain.
func octoHTTPS(t *testing.T, address, hostname, path string) octoAnswer {
	t.Helper()
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 15 * time.Second}).DialContext(ctx, network, net.JoinHostPort(address, "443"))
		},
		TLSClientConfig: &tls.Config{
			ServerName:         hostname,
			MinVersion:         tls.VersionTLS12,
			InsecureSkipVerify: true, // Let's Encrypt staging certificates have no trusted root; VerifyConnection checks the name.
			VerifyConnection: func(state tls.ConnectionState) error {
				if len(state.PeerCertificates) == 0 {
					return errors.New("no certificate")
				}
				return state.PeerCertificates[0].VerifyHostname(hostname)
			},
		},
	}
	defer transport.CloseIdleConnections()
	var answer octoAnswer
	var err error
	for deadline := time.Now().Add(2 * time.Minute); ; {
		answer, err = octoSend(t.Context(), transport, "https://"+hostname+path)
		if err == nil && (path == "/admin" || (answer.Status != http.StatusBadGateway && answer.Status != http.StatusServiceUnavailable)) {
			return answer
		}
		if time.Now().After(deadline) {
			t.Fatalf("GET https://%s%s: HTTP %d, error %v", hostname, path, answer.Status, err)
		}
		time.Sleep(5 * time.Second)
	}
}

// teardown removes everything through the product, then checks nothing
// of the project is left on the target.
func (x *octoRun) teardown() {
	t := x.t
	timeout := domainsTimeout(t)
	ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), capped(t, timeout, cleanupGrace))
	defer cancel()
	c := &client{t: t, base: x.c.base, token: x.c.token, key: x.c.key, ctx: ctx}
	base := "/api/v1/projects/" + x.projectID
	if d := domainsDeploy(t, c, base, "version: 1\n", capped(t, timeout, cleanupGrace)); d.Status != "succeeded" {
		t.Errorf("the teardown %s: %s", d.Status, d.Error)
	}
	var list struct {
		Bindings []*planning.Binding `json:"bindings"`
	}
	domainsCall(t, c, http.StatusOK, http.MethodGet, base+"/bindings?environment=production", nil, &list)
	if len(list.Bindings) != 0 {
		t.Errorf("bindings remain after the teardown: %d", len(list.Bindings))
	}
	x.cleanup()
}

// cleanup checks nothing of the project is left on the target, and
// removes what is: only objects labelled with this project are eligible.
func (x *octoRun) cleanup() {
	t := x.t
	ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 2*time.Minute)
	defer cancel()
	selector := owned.LabelProject + "=" + x.projectID
	if x.projectID == "" {
		return
	}
	if x.target.provider == "server" {
		cfg, err := server.ParseConfig([]byte(x.target.credential), x.target.settings)
		if err != nil {
			t.Errorf("cleanup: %v", err)
			return
		}
		client, err := server.Dial(ctx, cfg)
		if err != nil {
			t.Errorf("cleanup: reach the server: %v", err)
			return
		}
		defer func() { _ = client.Close() }()
		filters, _ := json.Marshal(map[string][]string{"label": {selector}})
		res, err := client.Request(ctx, http.MethodGet, "/containers/json?all=true&filters="+url.QueryEscape(string(filters)), nil)
		if err != nil {
			t.Errorf("cleanup: list containers: %v", err)
			return
		}
		var containers []struct {
			ID    string   `json:"Id"`
			Names []string `json:"Names"`
		}
		err = json.NewDecoder(res.Body).Decode(&containers)
		_ = res.Body.Close()
		if err != nil {
			t.Errorf("cleanup: read containers: %v", err)
			return
		}
		for _, container := range containers {
			t.Errorf("container %v is left after the teardown; removing it", container.Names)
			if res, err = client.Request(ctx, http.MethodDelete, "/containers/"+container.ID+"?force=true", nil); err == nil {
				_ = res.Body.Close()
			}
		}
		return
	}
	cfg, err := kubernetes.ParseConfig([]byte(x.target.credential), x.target.settings)
	if err != nil {
		t.Errorf("cleanup: %v", err)
		return
	}
	cluster, err := kubernetes.New(cfg)
	if err != nil {
		t.Errorf("cleanup: %v", err)
		return
	}
	ns, list := cfg.Namespace, metav1.ListOptions{LabelSelector: selector}
	kube := cluster.Kube
	if left, listErr := kube.AppsV1().Deployments(ns).List(ctx, list); listErr == nil && len(left.Items) > 0 {
		t.Errorf("%d deployments are left after the teardown; removing them", len(left.Items))
	}
	for name, remove := range map[string]func() error{
		"deployments": func() error { return kube.AppsV1().Deployments(ns).DeleteCollection(ctx, metav1.DeleteOptions{}, list) },
		"configmaps":  func() error { return kube.CoreV1().ConfigMaps(ns).DeleteCollection(ctx, metav1.DeleteOptions{}, list) },
		"secrets":     func() error { return kube.CoreV1().Secrets(ns).DeleteCollection(ctx, metav1.DeleteOptions{}, list) },
		"serviceaccounts": func() error {
			return kube.CoreV1().ServiceAccounts(ns).DeleteCollection(ctx, metav1.DeleteOptions{}, list)
		},
		"roles": func() error { return kube.RbacV1().Roles(ns).DeleteCollection(ctx, metav1.DeleteOptions{}, list) },
		"rolebindings": func() error {
			return kube.RbacV1().RoleBindings(ns).DeleteCollection(ctx, metav1.DeleteOptions{}, list)
		},
	} {
		if removeErr := remove(); removeErr != nil {
			t.Errorf("cleanup: remove %s: %v", name, removeErr)
		}
	}
	services, err := kube.CoreV1().Services(ns).List(ctx, list)
	if err != nil {
		t.Errorf("cleanup: list services: %v", err)
		return
	}
	for _, svc := range services.Items {
		t.Errorf("Service %s is left after the teardown; removing it", svc.Name)
		if err := kube.CoreV1().Services(ns).Delete(ctx, svc.Name, metav1.DeleteOptions{}); err != nil {
			t.Errorf("cleanup: remove Service %s: %v", svc.Name, err)
		}
	}
}
