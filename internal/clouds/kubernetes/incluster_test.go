package kubernetes

import (
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// serviceAccount writes what the kubelet mounts in a pod and returns its
// directory.
func serviceAccount(t *testing.T, token, namespace string, ca []byte) string {
	t.Helper()
	dir := t.TempDir()
	for name, data := range map[string][]byte{"token": []byte(token), "ca.crt": ca, "namespace": []byte(namespace)} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func podEnv(host, port string) func(string) string {
	return func(key string) string {
		return map[string]string{"KUBERNETES_SERVICE_HOST": host, "KUBERNETES_SERVICE_PORT": port}[key]
	}
}

// useInCluster turns in-cluster credentials on for one test.
func useInCluster(t *testing.T, ic *InCluster) {
	t.Helper()
	UseInCluster(ic)
	t.Cleanup(func() { UseInCluster(nil) })
}

func TestDetectInCluster_readsThePodServiceAccount(t *testing.T) {
	ca := testCA(t)
	dir := serviceAccount(t, "pod-token\n", "loomplane\n", ca)
	ic, err := detectInCluster(podEnv("10.96.0.1", "443"), dir)
	if err != nil {
		t.Fatal(err)
	}
	if ic.Host != "https://10.96.0.1:443" || ic.Namespace != "loomplane" || ic.TokenFile != filepath.Join(dir, "token") || string(ic.CAData) != string(ca) {
		t.Fatalf("wrong cluster: %+v", ic)
	}
	v6, err := detectInCluster(podEnv("fd00::1", "6443"), dir)
	if err != nil || v6.Host != "https://[fd00::1]:6443" {
		t.Fatalf("IPv6 service host: %v %+v", err, v6)
	}
}

func TestDetectInCluster_refusesWhatIsNotAPod(t *testing.T) {
	ca := testCA(t)
	for _, tc := range []struct {
		name string
		env  func(string) string
		dir  string
	}{
		{"no service variables", podEnv("", ""), serviceAccount(t, "token", "loomplane", ca)},
		{"no token mounted", podEnv("10.96.0.1", "443"), t.TempDir()},
		{"empty token", podEnv("10.96.0.1", "443"), serviceAccount(t, " \n", "loomplane", ca)},
		{"no CA", podEnv("10.96.0.1", "443"), serviceAccount(t, "token", "loomplane", []byte("not a certificate"))},
		{"bad namespace", podEnv("10.96.0.1", "443"), serviceAccount(t, "token", "Not_A_Label", ca)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := detectInCluster(tc.env, tc.dir); !errors.Is(err, ErrNotInCluster) {
				t.Fatalf("got %v, want ErrNotInCluster", err)
			}
		})
	}
}

func TestIsInClusterCredential_onlyTheMarker(t *testing.T) {
	if !IsInClusterCredential(InClusterCredential()) || !IsInClusterCredential([]byte(`{"type":"loomplane_in_cluster","host":"https://evil.example"}`)) {
		t.Fatal("marker not recognized")
	}
	for _, raw := range []string{"", "{}", `{"type":"loomplane_keyless"}`, "apiVersion: v1\nkind: Config\n"} {
		if IsInClusterCredential([]byte(raw)) {
			t.Fatalf("%q taken for the marker", raw)
		}
	}
}

func TestParseConfig_inClusterRefusedUnlessTurnedOn(t *testing.T) {
	UseInCluster(nil)
	if _, err := ParseConfig(InClusterCredential(), map[string]string{"namespace": "apps"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("in-cluster credential accepted with the feature off: %v", err)
	}
}

func TestParseConfig_inClusterUsesThePodNeverTheCredential(t *testing.T) {
	ca := testCA(t)
	dir := serviceAccount(t, "pod-token", "loomplane", ca)
	useInCluster(t, &InCluster{Host: "https://10.96.0.1:443", CAData: ca, TokenFile: filepath.Join(dir, "token"), Namespace: "loomplane"})
	// Anything else the credential carries is ignored, so it can't redirect
	// the token.
	forged := []byte(`{"type":"loomplane_in_cluster","host":"https://evil.example","token_file":"/etc/shadow"}`)
	c, err := ParseConfig(forged, map[string]string{"namespace": "apps", "storage_class": "retained", "architecture": "arm64"})
	if err != nil {
		t.Fatal(err)
	}
	if c.REST.Host != "https://10.96.0.1:443" || c.REST.BearerTokenFile != filepath.Join(dir, "token") || c.REST.BearerToken != "" || string(c.REST.CAData) != string(ca) {
		t.Fatalf("wrong REST config: host=%s file=%s", c.REST.Host, c.REST.BearerTokenFile)
	}
	if c.Context != InClusterContext || c.Namespace != "apps" || c.Details["endpoint"] != "https://10.96.0.1:443" || c.Details["storage_class"] != "retained" || c.Details["architecture"] != "arm64" || c.Details["cluster_ca"] == "" {
		t.Fatalf("wrong details: %+v", c.Details)
	}
	if proxy, proxyErr := c.REST.Proxy(&http.Request{URL: &url.URL{Scheme: "https", Host: "10.96.0.1"}}); proxy != nil || proxyErr != nil {
		t.Fatal("in-cluster connection can be proxied")
	}
	for _, v := range c.Details {
		if strings.Contains(v, "pod-token") {
			t.Fatal("token escaped into details")
		}
	}
}

func TestParseConfig_inClusterNamespaces(t *testing.T) {
	ca := testCA(t)
	dir := serviceAccount(t, "pod-token", "loomplane", ca)
	useInCluster(t, &InCluster{Host: "https://10.96.0.1:443", CAData: ca, TokenFile: filepath.Join(dir, "token"), Namespace: "loomplane"})
	for _, tc := range []struct{ name, namespace, want string }{
		{"missing", "", "choose the namespace"},
		{"invalid", "Apps", "DNS label"},
		{"Loomplane's own", "loomplane", "runs in namespace loomplane"},
		{"kube-system", "kube-system", "belongs to Kubernetes"},
		{"kube-public", "kube-public", "belongs to Kubernetes"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseConfig(InClusterCredential(), map[string]string{"namespace": tc.namespace})
			if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %q", err, tc.want)
			}
		})
	}
}

func TestNew_inClusterSendsTheMountedToken(t *testing.T) {
	var got string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"gitVersion":"v1.31.0"}`))
	}))
	t.Cleanup(server.Close)
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	dir := serviceAccount(t, "rotated-by-kubelet", "loomplane", ca)
	useInCluster(t, &InCluster{Host: server.URL, CAData: ca, TokenFile: filepath.Join(dir, "token"), Namespace: "loomplane"})
	cfg, err := ParseConfig(InClusterCredential(), map[string]string{"namespace": "apps"})
	if err != nil {
		t.Fatal(err)
	}
	c, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Kube.Discovery().RESTClient().Get().AbsPath("/version").Do(t.Context()).Raw(); err != nil {
		t.Fatal(err)
	}
	if got != "Bearer rotated-by-kubelet" {
		t.Fatalf("sent %q, not the mounted token", got)
	}
}
