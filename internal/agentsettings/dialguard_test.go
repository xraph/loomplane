package agentsettings_test

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/xraph/nexus/provider"
	"golang.org/x/net/http/httpproxy"

	"github.com/xraph/loomplane/internal/agentsettings"
	"github.com/xraph/loomplane/internal/config"
)

// providerTransport is a transport like the one a nexus provider sends
// through: the stock settings, with the proxy read from the environment
// on every request (http.ProxyFromEnvironment reads it once per process,
// which would hide the variable a test sets).
func providerTransport() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = func(r *http.Request) (*url.URL, error) {
		return httpproxy.FromEnvironment().ProxyFunc()(r.URL)
	}
	return t
}

// workspaceCall is a context carrying a workspace credential with its own
// base URL, as nexus hands it to the provider.
func workspaceCall(base string) context.Context {
	return provider.WithCredential(context.Background(), provider.Credential{APIKey: "oc-secret-9876", BaseURL: base})
}

// guarded is a client like the provider's, over the guarded transport.
func guarded(h *harness) *http.Client {
	return &http.Client{Transport: h.svc.ProviderTransport(providerTransport())}
}

func get(ctx context.Context, c *http.Client, u string) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, http.NoBody)
	if err != nil {
		return 0, err
	}
	resp, err := c.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode, nil
}

// port is the port of a test server's URL.
func port(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return u.Port()
}

// A name checked when the key was chosen can resolve somewhere else by the
// time nexus dials it. The guard checks the address it dials, from the
// same resolver, so the name that now points at 10.0.0.5 is refused before
// any connection.
func TestDialGuardRefusesANameThatReResolvesPrivate(t *testing.T) {
	h := newHarness(t, func(d *agentsettings.Deps) { d.Environment = config.EnvStaging })
	h.putKey(t, "local", openCompatKey("http://llm.example.com/v1"))
	h.mustUpdate(t, agentsettings.AgentUpdateInput{ProviderKey: ptr("local"), Model: ptr("llama")})

	h.hosts["llm.example.com"] = []netip.Addr{netip.MustParseAddr("10.0.0.5")}
	c := guarded(h)
	_, err := get(workspaceCall("http://llm.example.com/v1"), c, "http://llm.example.com/v1/chat/completions")
	if !errors.Is(err, agentsettings.ErrDialRefused) {
		t.Fatalf("got %v, want ErrDialRefused", err)
	}
	// The client puts the URL around a transport's error; the guard's own
	// refusal names no URL.
	var ue *url.Error
	if !errors.As(err, &ue) || strings.Contains(ue.Err.Error(), "llm.example.com") {
		t.Errorf("the refusal quotes the URL: %v", err)
	}

	// A literal private address is refused the same way.
	if _, err = get(workspaceCall("http://10.0.0.5/v1"), c, "http://10.0.0.5/v1/models"); !errors.Is(err, agentsettings.ErrDialRefused) {
		t.Fatalf("literal: %v", err)
	}
}

// In development a local model server is allowed, and the guard dials the
// address it checked.
func TestDialGuardDialsTheAddressItChecked(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	h := newHarness(t, func(d *agentsettings.Deps) { d.Environment = config.EnvDevelopment })
	h.hosts["model.internal.test"] = []netip.Addr{netip.MustParseAddr("127.0.0.1")}
	base := "http://model.internal.test:" + port(t, srv) + "/v1"
	code, err := get(workspaceCall(base), guarded(h), base+"/models")
	if err != nil || code != http.StatusOK || hits.Load() != 1 {
		t.Fatalf("status %d, %v, %d hits", code, err, hits.Load())
	}
	// A link-local or metadata address never, in development either.
	h.hosts["model.internal.test"] = []netip.Addr{netip.MustParseAddr("169.254.169.254")}
	if _, err = get(workspaceCall(base), guarded(h), base+"/models"); !errors.Is(err, agentsettings.ErrDialRefused) {
		t.Fatalf("metadata: %v", err)
	}
}

// A workspace's server can't send nexus on somewhere else: a redirect is
// refused, not followed, so the metadata address it names is never
// dialed.
func TestDialGuardRefusesRedirects(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://169.254.169.254/latest/meta-data/iam/security-credentials/", http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	var lookups []string
	h := newHarness(t, func(d *agentsettings.Deps) { d.Environment = config.EnvDevelopment })
	next := h.deps.Lookup
	h.restart(func(d *agentsettings.Deps) {
		d.Lookup = func(ctx context.Context, host string) ([]netip.Addr, error) {
			lookups = append(lookups, host)
			return next(ctx, host)
		}
	})
	h.hosts["model.internal.test"] = []netip.Addr{netip.MustParseAddr("127.0.0.1")}
	base := "http://model.internal.test:" + port(t, srv) + "/v1"
	_, err := get(workspaceCall(base), guarded(h), base+"/chat/completions")
	if !errors.Is(err, agentsettings.ErrRedirectRefused) {
		t.Fatalf("got %v, want ErrRedirectRefused", err)
	}
	if len(lookups) != 1 || lookups[0] != "model.internal.test" {
		t.Fatalf("lookups %v", lookups)
	}
}

// An HTTPS_PROXY in the environment would carry the call to the proxy,
// whose address the guard never sees. The guarded transport ignores it and
// dials the checked address itself.
func TestDialGuardIgnoresTheEnvironmentProxy(t *testing.T) {
	var proxied atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		proxied.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer proxy.Close()
	var reached atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	t.Setenv("HTTPS_PROXY", proxy.URL)
	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("NO_PROXY", "")

	h := newHarness(t, func(d *agentsettings.Deps) { d.Environment = config.EnvDevelopment })
	h.hosts["model.internal.test"] = []netip.Addr{netip.MustParseAddr("127.0.0.1")}
	base := "http://model.internal.test:" + port(t, srv) + "/v1"
	code, err := get(workspaceCall(base), guarded(h), base+"/models")
	if err != nil || code != http.StatusOK {
		t.Fatalf("status %d, %v", code, err)
	}
	if proxied.Load() != 0 || reached.Load() != 1 {
		t.Fatalf("proxied %d, reached %d", proxied.Load(), reached.Load())
	}
	// The same call without the guard goes through the proxy, which is
	// what the guard is for.
	if _, err = get(workspaceCall(base), &http.Client{Transport: providerTransport()}, base+"/models"); err != nil || proxied.Load() != 1 {
		t.Fatalf("unguarded: %v, proxied %d", err, proxied.Load())
	}
}

// Calls that carry no workspace base URL go where the server sends them:
// its own ai.base_url, the embeddings provider, a provider's own API. The
// guard leaves them alone, private addresses and the proxy included.
func TestDialGuardLeavesTheServersEndpointsAlone(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer srv.Close()
	h := newHarness(t) // production
	for name, ctx := range map[string]context.Context{
		"no credential": context.Background(),
		"a key alone":   provider.WithCredential(context.Background(), provider.Credential{APIKey: "sk-workspace"}),
	} {
		code, err := get(ctx, guarded(h), srv.URL+"/v1/messages")
		if err != nil || code != http.StatusOK {
			t.Errorf("%s: status %d, %v", name, code, err)
		}
	}
	// A workspace base URL in production must be https.
	if _, err := get(workspaceCall(srv.URL), guarded(h), srv.URL+"/v1/messages"); !errors.Is(err, agentsettings.ErrDialRefused) {
		t.Errorf("plain http in production: %v", err)
	}
}

// planning.endpoint_denied_cidrs written in IPv4-mapped IPv6 form still
// refuses the IPv4 addresses it names, when a key is chosen and at dial.
func TestDeniedCIDRsInMappedFormMatch(t *testing.T) {
	h := newHarness(t, func(d *agentsettings.Deps) {
		d.Environment = config.EnvDevelopment
		d.Denied = []netip.Prefix{netip.MustParsePrefix("::ffff:93.184.215.0/120"), netip.MustParsePrefix("::ffff:127.0.0.0/104")}
	})
	h.putKey(t, "public", openCompatKey("https://llm.example.com/v1"))
	_, err := h.update(t, agentsettings.AgentUpdateInput{ProviderKey: ptr("public"), Model: ptr("llama")})
	wantInvalid(t, err, "planning.endpoint_denied_cidrs")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer srv.Close()
	h.hosts["model.internal.test"] = []netip.Addr{netip.MustParseAddr("127.0.0.1")}
	base := "http://model.internal.test:" + port(t, srv) + "/v1"
	if _, err = get(workspaceCall(base), guarded(h), base+"/models"); !errors.Is(err, agentsettings.ErrDialRefused) {
		t.Fatalf("dial into a denied range: %v", err)
	}
}

// The guard dials only what it checked: a resolver answer of several
// addresses is refused when any one of them is.
func TestDialGuardRefusesAMixedAnswer(t *testing.T) {
	h := newHarness(t, func(d *agentsettings.Deps) { d.Environment = config.EnvStaging })
	_, err := get(workspaceCall("http://sneaky.example.com/v1"), guarded(h), "http://sneaky.example.com/v1/models")
	if !errors.Is(err, agentsettings.ErrDialRefused) {
		t.Fatalf("got %v", err)
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr.Addr != nil && strings.HasPrefix(opErr.Addr.String(), "93.184.215.15") {
		t.Fatalf("dialed the public address of a mixed answer: %v", err)
	}
}
