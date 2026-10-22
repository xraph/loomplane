package deployments

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/recipes/recipestest"
	"github.com/xraph/loomplane/internal/security"
)

// accessLog records, in order, what CatalogAccess asked for.
type accessLog struct {
	mu    sync.Mutex
	calls []string
}

func (l *accessLog) add(call string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = append(l.calls, call)
}

func (l *accessLog) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.calls)
}

// accessAuthz lets every principal but "intruder" in, and logs each check.
type accessAuthz struct{ log *accessLog }

func (a accessAuthz) Require(_ context.Context, p security.Principal, perm security.Permission, resourceID string) error {
	a.log.add("require " + p.UserID + " " + perm.Name() + " " + resourceID)
	if p.UserID == "intruder" {
		return security.ErrForbidden
	}
	return nil
}

// componentDialer stands in for a target: it reaches a component through
// handler, and records what it was asked to reach.
type componentDialer struct {
	Executor
	log     *accessLog
	handler http.Handler
	closed  int
}

func (d *componentDialer) ComponentTransport(_ context.Context, b *planning.Binding, port int) (http.RoundTripper, io.Closer, error) {
	d.log.add("dial " + b.Name + " " + b.ExternalID + " " + strconv.Itoa(port))
	return handlerTransport{d.handler}, closerFunc(func() error { d.closed++; return nil }), nil
}

type handlerTransport struct{ h http.Handler }

func (t handlerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	w := httptest.NewRecorder()
	t.h.ServeHTTP(w, r)
	res := w.Result()
	res.Request = r
	return res, nil
}

// composedBindings are what a deployed "app" of the sample kind leaves in
// environment env of project proj: the parent, its job and two processes.
func composedBindings(t *testing.T) []*planning.Binding {
	t.Helper()
	composition, err := recipestest.Registry(t).Resolve("sample", "1", "amd64", "server")
	if err != nil {
		t.Fatal(err)
	}
	out := make([]*planning.Binding, 0, len(composition.Components)+2)
	for _, c := range composition.Components {
		cfg := planning.ResourceConfig{Provider: "server", Target: "conn", Kind: "sample", Version: "1", Component: &planning.ComponentConfig{Kind: "sample", Resource: "app", Revision: composition.Revision, Component: c}}
		raw, _ := json.Marshal(cfg)
		name := planning.ComponentActionName("app", c.Name)
		out = append(out, &planning.Binding{OrgID: "org", ProjectID: "proj", EnvironmentID: "env", Subject: planning.ResourceSubject, Name: name, Provider: "server", ConnectionID: "conn", ExternalID: "inst_" + c.Name, Config: raw})
	}
	sample := recipestest.Sample()
	parent, _ := json.Marshal(planning.ResourceConfig{Provider: "server", Target: "conn", Kind: "sample", Version: "1", Composition: &planning.CompositionConfig{Kind: "sample", Version: "1", Revision: composition.Revision, Outputs: sample.Outputs, Secrets: sample.Secrets}})
	out = append(out, &planning.Binding{OrgID: "org", ProjectID: "proj", EnvironmentID: "env", Subject: planning.ResourceSubject, Name: "app", Provider: "server", ConnectionID: "conn", ExternalID: "catalog/app", Config: parent})
	// A service literally named like a component isn't one.
	out = append(out, &planning.Binding{OrgID: "org", ProjectID: "proj", EnvironmentID: "env", Subject: planning.ServiceSubject, Name: "web--api", Provider: "server", ExternalID: "inst_web", Config: json.RawMessage(`{}`)})
	return out
}

func newCatalogAccess(t *testing.T, log *accessLog, dialer Executor, secrets ResourceSecrets) *CatalogAccess {
	t.Helper()
	bindings := composedBindings(t)
	return NewCatalogAccess(CatalogAccessDeps{
		Authz: accessAuthz{log},
		Bindings: func(_ context.Context, orgID, environmentID string) ([]*planning.Binding, error) {
			log.add("bindings " + orgID + " " + environmentID)
			var out []*planning.Binding
			for _, b := range bindings {
				if b.OrgID == orgID && b.EnvironmentID == environmentID {
					out = append(out, b)
				}
			}
			return out, nil
		},
		Executors: map[string]Executor{connections.Server: dialer, "vercel": &Vercel{}},
		Secrets:   secrets,
	})
}

func TestCatalogAccessChannelChecksPermissionAndPort(t *testing.T) {
	log := &accessLog{}
	elsewhere := 0
	dialer := &componentDialer{log: log, handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/moved" {
			http.Redirect(w, r, "http://elsewhere.example/stolen", http.StatusFound)
			return
		}
		if r.Host == "elsewhere.example" {
			elsewhere++
		}
		body, _ := io.ReadAll(r.Body)
		_, _ = io.WriteString(w, r.Method+" "+r.URL.RequestURI()+" "+string(body))
	})}
	access := newCatalogAccess(t, log, dialer, &memSecrets{m: map[string][]byte{}})
	member := security.Principal{OrgID: "org", UserID: "usr"}
	ref := func(project, resource, component, port string) ComponentRef {
		return ComponentRef{ResourceRef: ResourceRef{ProjectID: project, EnvironmentID: "env", Resource: resource}, Component: component, Port: port}
	}

	// Permission comes first: someone who may not doesn't learn what runs.
	if _, err := access.Channel(t.Context(), security.Principal{OrgID: "org", UserID: "intruder"}, security.DeploymentRead, ref("proj", "app", "api", "admin")); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("intruder: %v", err)
	}
	if calls := log.all(); !slices.Equal(calls, []string{"require intruder deployment:read proj"}) {
		t.Fatalf("intruder calls: %v", calls)
	}

	for name, r := range map[string]ComponentRef{
		"unknown resource":  ref("proj", "other", "api", "admin"),
		"unknown component": ref("proj", "app", "cron", "admin"),
		"a job":             ref("proj", "app", "migrate", "admin"),
		"another project":   ref("elsewhere", "app", "api", "admin"),
		"a service":         ref("proj", "web", "api", "admin"),
		"the parent":        ref("proj", "app", "", "admin"),
	} {
		if _, err := access.Channel(t.Context(), member, security.DeploymentRead, r); !errors.Is(err, ErrComponentNotFound) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	for _, port := range []string{"http", "metrics", ""} {
		if _, err := access.Channel(t.Context(), member, security.DeploymentRead, ref("proj", "app", "api", port)); !errors.Is(err, ErrNotControlPort) {
			t.Fatalf("port %q: %v", port, err)
		}
	}
	if slices.ContainsFunc(log.all(), func(c string) bool { return strings.HasPrefix(c, "dial") }) {
		t.Fatalf("dialed for a refused channel: %v", log.all())
	}

	log.calls = nil
	channel, err := access.Channel(t.Context(), member, security.DeploymentRead, ref("proj", "app", "api", "admin"))
	if err != nil {
		t.Fatal(err)
	}
	if calls := log.all(); !slices.Equal(calls, []string{"require usr deployment:read proj", "bindings org env", "dial app--api inst_api 9090"}) {
		t.Fatalf("calls: %v", calls)
	}
	if channel.BaseURL.String() != "http://component" || channel.Client.Timeout != 30*time.Second {
		t.Fatalf("channel: %s, %s", channel.BaseURL, channel.Client.Timeout)
	}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, channel.BaseURL.String()+"/admin/api/routes?all=1", strings.NewReader("r1"))
	if err != nil {
		t.Fatal(err)
	}
	res, err := channel.Client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if string(got) != "POST /admin/api/routes?all=1 r1" {
		t.Fatalf("component answered %q", got)
	}
	// A redirect is refused, not followed.
	moved, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, channel.BaseURL.String()+"/moved", http.NoBody)
	if res, err := channel.Client.Do(moved); err == nil {
		_ = res.Body.Close()
		t.Fatal("followed a redirect")
	}
	if elsewhere != 0 {
		t.Fatal("a redirect reached another host")
	}
	// Every target refuses a request's own Authorization header, and
	// anything but http://component.
	for name, tc := range map[string]struct {
		url, auth string
		want      error
	}{
		"authorization": {"http://component/admin", "Bearer admin", errChannelAuthorization},
		"another host":  {"http://elsewhere.example/admin", "", errChannelHost},
		"https":         {"https://component/admin", "", errChannelHost},
	} {
		req, reqErr := http.NewRequestWithContext(t.Context(), http.MethodPost, tc.url, strings.NewReader("r1"))
		if reqErr != nil {
			t.Fatal(reqErr)
		}
		if tc.auth != "" {
			req.Header.Set("Authorization", tc.auth)
		}
		if res, doErr := channel.Client.Do(req); !errors.Is(doErr, tc.want) {
			if res != nil {
				_ = res.Body.Close()
			}
			t.Fatalf("%s: %v", name, doErr)
		}
	}
	if elsewhere != 0 {
		t.Fatal("a refused request reached another host")
	}
	if err := channel.Close(); err != nil || dialer.closed != 1 {
		t.Fatalf("close: %v, %d", err, dialer.closed)
	}

	// A target that can't reach a component privately says so.
	vercel := composedBindings(t)
	for _, b := range vercel {
		b.Provider = "vercel"
	}
	other := NewCatalogAccess(CatalogAccessDeps{
		Authz:     accessAuthz{log},
		Bindings:  func(context.Context, string, string) ([]*planning.Binding, error) { return vercel, nil },
		Executors: map[string]Executor{"vercel": &Vercel{}},
	})
	if _, err := other.Channel(t.Context(), member, security.DeploymentRead, ref("proj", "app", "api", "admin")); err == nil || errors.Is(err, ErrComponentNotFound) {
		t.Fatalf("vercel channel: %v", err)
	}
}

func TestCatalogAccessGeneratedSecret(t *testing.T) {
	log := &accessLog{}
	secrets := &memSecrets{m: map[string][]byte{resourceKey("org", "env", "app", "secret:admin-password"): []byte("s3cret-admin")}}
	access := newCatalogAccess(t, log, &componentDialer{log: log}, secrets)
	member := security.Principal{OrgID: "org", UserID: "usr"}
	app := ResourceRef{ProjectID: "proj", EnvironmentID: "env", Resource: "app"}

	if _, err := access.GeneratedSecret(t.Context(), security.Principal{OrgID: "org", UserID: "intruder"}, security.DeploymentApprove, app, "admin-password"); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("intruder: %v", err)
	}
	if calls := log.all(); !slices.Equal(calls, []string{"require intruder deployment:approve proj"}) {
		t.Fatalf("intruder calls: %v", calls)
	}
	value, err := access.GeneratedSecret(t.Context(), member, security.DeploymentApprove, app, "admin-password")
	if err != nil || string(value) != "s3cret-admin" {
		t.Fatalf("secret %q: %v", value, err)
	}
	for name, tc := range map[string]struct {
		ref    ResourceRef
		secret string
	}{
		"undeclared":      {app, "root-password"},
		"a component":     {ResourceRef{ProjectID: "proj", EnvironmentID: "env", Resource: "app--api"}, "admin-password"},
		"another project": {ResourceRef{ProjectID: "elsewhere", EnvironmentID: "env", Resource: "app"}, "admin-password"},
		"unknown":         {ResourceRef{ProjectID: "proj", EnvironmentID: "env", Resource: "other"}, "admin-password"},
	} {
		if _, err := access.GeneratedSecret(t.Context(), member, security.DeploymentApprove, tc.ref, tc.secret); !errors.Is(err, ErrComponentNotFound) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	// Declared and not generated yet is not the same as not declared.
	delete(secrets.m, resourceKey("org", "env", "app", "secret:admin-password"))
	if _, err := access.GeneratedSecret(t.Context(), member, security.DeploymentApprove, app, "admin-password"); err == nil || errors.Is(err, ErrComponentNotFound) {
		t.Fatalf("missing secret: %v", err)
	}
}

func TestComponentRunChannelReachesItselfAndEarlierComponents(t *testing.T) {
	f := newComponentFixture(t, recipestest.Registry(t))
	f.secrets.m[resourceKey("org", "env", "app--postgres", "url")] = []byte(postgresURL)
	log := &accessLog{}
	dialer := &componentDialer{log: log, handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") })}
	f.target.componentDial = dialer.ComponentTransport
	var bindings []*planning.Binding
	f.target.Bindings = func(_ context.Context, orgID, environmentID string) ([]*planning.Binding, error) {
		log.add("bindings " + orgID + " " + environmentID)
		return bindings, nil
	}

	apiJob := f.componentJob(t, "api", newPostgres)
	api, err := f.target.Apply(t.Context(), apiJob)
	if err != nil {
		t.Fatal(err)
	}
	run := f.hooks.ready[len(f.hooks.ready)-1]
	channel, err := run.Channel(t.Context(), "api", "admin")
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, channel.BaseURL.String()+"/admin", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	res, err := channel.Client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	_ = channel.Close()
	if calls := log.all(); !slices.Equal(calls, []string{"dial app--api " + api.ExternalID + " 9090"}) {
		t.Fatalf("api reached itself with %v", calls)
	}
	if _, err = run.Channel(t.Context(), "api", "http"); !errors.Is(err, ErrNotControlPort) {
		t.Fatalf("api's http port: %v", err)
	}
	// Before it's bound, a later component can't find it.
	if _, err = run.Channel(t.Context(), "worker", "health"); !errors.Is(err, ErrComponentNotFound) {
		t.Fatalf("worker before it runs: %v", err)
	}

	bindings = []*planning.Binding{bound(apiJob, api)}
	log.calls = nil
	if _, err = f.target.Apply(t.Context(), f.componentJob(t, "worker", newPostgres)); err != nil {
		t.Fatal(err)
	}
	run = f.hooks.ready[len(f.hooks.ready)-1]
	channel, err = run.Channel(t.Context(), "api", "admin")
	if err != nil {
		t.Fatal(err)
	}
	_ = channel.Close()
	if calls := log.all(); !slices.Equal(calls, []string{"bindings org env", "dial app--api " + api.ExternalID + " 9090"}) {
		t.Fatalf("worker reached the api with %v", calls)
	}
	if _, err := run.Channel(t.Context(), "worker", "health"); !errors.Is(err, ErrNotControlPort) {
		t.Fatalf("worker's health port: %v", err)
	}
}

func TestComponentTransportOpensTheReviewedSession(t *testing.T) {
	f := newComponentFixture(t, recipestest.Registry(t))
	f.secrets.m[resourceKey("org", "env", "app--postgres", "url")] = []byte(postgresURL)
	job := f.componentJob(t, "api", newPostgres)
	out, err := f.target.Apply(t.Context(), job)
	if err != nil {
		t.Fatal(err)
	}
	b := bound(job, out)
	for name, edit := range map[string]func(*planning.Binding){
		"another provider":    func(b *planning.Binding) { b.Provider = connections.Kubernetes },
		"another project":     func(b *planning.Binding) { b.ProjectID = "other" },
		"another environment": func(b *planning.Binding) { b.EnvironmentID = "other" },
		"another connection":  func(b *planning.Binding) { b.ConnectionID = "conn_other" },
		"not running":         func(b *planning.Binding) { b.ExternalID = "" },
	} {
		changed := *b
		edit(&changed)
		if transport, closer, err := f.target.ComponentTransport(t.Context(), &changed, 9090); err == nil || transport != nil || closer != nil {
			t.Fatalf("%s: reached (%v)", name, err)
		}
	}
	if _, _, err := f.target.ComponentTransport(t.Context(), b, 0); err == nil {
		t.Fatal("reached port 0")
	}
	// The binding's own instance opens the target's session, which checks
	// the connection the way a deployment does.
	if _, _, err := f.target.ComponentTransport(t.Context(), b, 9090); !errors.Is(err, ErrNoCredential) {
		t.Fatalf("session: %v", err)
	}
}
