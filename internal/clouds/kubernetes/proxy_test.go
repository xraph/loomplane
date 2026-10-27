package kubernetes

import (
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"k8s.io/client-go/rest"
)

func TestServiceProxyTransportRewritesToAPIServer(t *testing.T) {
	var redirected atomic.Int32
	elsewhere := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirected.Add(1) }))
	defer elsewhere.Close()
	type seen struct{ method, path, query, body, auth string }
	var mu sync.Mutex
	var requests []seen
	apiserver := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		requests = append(requests, seen{r.Method, r.URL.EscapedPath(), r.URL.RawQuery, string(body), r.Header.Get("Authorization")})
		mu.Unlock()
		if strings.HasSuffix(r.URL.Path, "/moved") {
			http.Redirect(w, r, elsewhere.URL+"/stolen", http.StatusFound)
			return
		}
		_, _ = io.WriteString(w, "routes")
	}))
	defer apiserver.Close()
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: apiserver.Certificate().Raw})
	for _, prefix := range []string{"", "/k8s/clusters/c-1"} {
		t.Run("prefix"+prefix, func(t *testing.T) {
			mu.Lock()
			requests = nil
			mu.Unlock()
			client, err := New(&Config{Namespace: "ns", REST: &rest.Config{Host: apiserver.URL + prefix, BearerToken: "cluster-token", TLSClientConfig: rest.TLSClientConfig{CAData: ca}}})
			if err != nil {
				t.Fatal(err)
			}
			transport, err := client.ServiceProxyTransport("ns", "lp-x", 9090)
			if err != nil {
				t.Fatal(err)
			}
			channel := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

			req, err := http.NewRequestWithContext(t.Context(), http.MethodPut, "http://component/admin/api/routes?scope=all&x=a%2Fb", strings.NewReader(`{"route":"r1"}`))
			if err != nil {
				t.Fatal(err)
			}
			res, err := channel.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			got, _ := io.ReadAll(res.Body)
			_ = res.Body.Close()
			if string(got) != "routes" {
				t.Fatalf("response %q", got)
			}
			if req.URL.String() != "http://component/admin/api/routes?scope=all&x=a%2Fb" {
				t.Fatalf("the caller's request changed: %s", req.URL)
			}

			moved, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://component/moved", http.NoBody)
			if err != nil {
				t.Fatal(err)
			}
			res, err = transport.RoundTrip(moved)
			if err != nil {
				t.Fatal(err)
			}
			_ = res.Body.Close()
			if res.StatusCode != http.StatusFound || redirected.Load() != 0 {
				t.Fatalf("redirect followed: %d, %d", res.StatusCode, redirected.Load())
			}

			mu.Lock()
			defer mu.Unlock()
			want := []seen{
				{http.MethodPut, prefix + "/api/v1/namespaces/ns/services/lp-x:9090/proxy/admin/api/routes", "scope=all&x=a%2Fb", `{"route":"r1"}`, "Bearer cluster-token"},
				{http.MethodGet, prefix + "/api/v1/namespaces/ns/services/lp-x:9090/proxy/moved", "", "", "Bearer cluster-token"},
			}
			if !slices.Equal(requests, want) {
				t.Fatalf("API server saw %+v", requests)
			}
		})
	}

	client, err := New(&Config{Namespace: "ns", REST: &rest.Config{Host: apiserver.URL, BearerToken: "cluster-token", TLSClientConfig: rest.TLSClientConfig{CAData: ca}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []struct {
		namespace, service string
		port               int
	}{{"other", "lp-x", 9090}, {"ns", "Not_A_Service", 9090}, {"ns", "lp-x/../secrets", 9090}, {"ns", "lp-x", 0}, {"ns", "lp-x", 65536}} {
		if _, err = client.ServiceProxyTransport(bad.namespace, bad.service, bad.port); err == nil {
			t.Fatalf("proxied to %+v", bad)
		}
	}
	// The cluster's credential authenticates the proxy; a caller's own
	// Authorization header would replace it.
	transport, err := client.ServiceProxyTransport("ns", "lp-x", 9090)
	if err != nil {
		t.Fatal(err)
	}
	// Nothing leaves the Service's proxy path, even where something on
	// the way normalises dot segments.
	mu.Lock()
	requests = nil
	mu.Unlock()
	for _, raw := range []string{
		"http://component/../../../secrets/db",
		"http://component/admin/../../../../secrets/db",
		"http://component/admin/./x",
		"http://component/%2e%2e/%2e%2e/secrets",
		"http://component/admin%2F..%2F..%2Fsecrets",
		"http://component/..",
	} {
		escape, parseErr := http.NewRequestWithContext(t.Context(), http.MethodGet, raw, http.NoBody)
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		if res, roundErr := transport.RoundTrip(escape); roundErr == nil {
			_ = res.Body.Close()
			t.Fatalf("proxied %s", raw)
		}
	}
	opaque, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://component/admin", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	opaque.URL.Opaque = "//component/api/v1/namespaces/ns/secrets"
	if res, roundErr := transport.RoundTrip(opaque); roundErr == nil {
		_ = res.Body.Close()
		t.Fatal("proxied an opaque URL")
	}
	relative, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://component/admin", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	relative.URL.Path = "admin"
	if res, roundErr := transport.RoundTrip(relative); roundErr == nil {
		_ = res.Body.Close()
		t.Fatal("proxied a relative path")
	}
	mu.Lock()
	escaped := len(requests)
	mu.Unlock()
	if escaped != 0 {
		t.Fatalf("API server saw %+v", requests)
	}
	// A dot inside a name isn't a segment.
	dotted, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://component/admin/v1.2/..config", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	res, err := transport.RoundTrip(dotted)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://component/admin", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer component-admin")
	if res, err := transport.RoundTrip(req); err == nil {
		_ = res.Body.Close()
		t.Fatal("sent a caller's Authorization header to the API server")
	}
}

func TestKubernetesCheckReportsServiceProxy(t *testing.T) {
	check := func(denied ...string) (*Result, []string) {
		t.Helper()
		s, paths, mu := clusterFixture(t, false, denied...)
		ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: s.Certificate().Raw})
		cfg, err := ParseConfig(kubeconfig(t, s.URL, ca), map[string]string{"namespace": "production"})
		if err != nil {
			t.Fatal(err)
		}
		c, err := New(cfg)
		if err != nil {
			t.Fatal(err)
		}
		result, err := c.Check(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		defer mu.Unlock()
		return result, slices.Clone(*paths)
	}
	result, paths := check()
	if result.Details["service_proxy"] != "ready" {
		t.Fatalf("service_proxy: %+v", result.Details)
	}
	for _, want := range []string{"production:services/proxy:get", "production:services/proxy:create"} {
		if !slices.Contains(paths, want) {
			t.Fatalf("unchecked service proxy permission %s", want)
		}
	}
	for _, denied := range []string{"services/proxy:get", "services/proxy:create"} {
		result, _ = check(denied)
		if _, ok := result.Details["service_proxy"]; ok {
			t.Fatalf("without %s, service_proxy = %q", denied, result.Details["service_proxy"])
		}
		if slices.ContainsFunc(result.Missing, func(m string) bool { return strings.HasPrefix(m, "services/proxy") }) {
			t.Fatalf("service proxy permissions made the connection incomplete: %v", result.Missing)
		}
	}
}
