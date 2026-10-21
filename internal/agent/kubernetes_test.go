package agent

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"

	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
)

func TestAgentKubernetesClientPinsNamespaceAndRejectsRedirect(t *testing.T) {
	var redirected atomic.Bool
	var reads atomic.Int32
	var redirect atomic.Bool
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirected" {
			redirected.Store(true)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.URL.Path != "/apis/discovery.k8s.io/v1/namespaces/workloads/endpointslices" || r.Header.Get("Authorization") != "Bearer local-service-account" {
			t.Error("unscoped or unauthenticated Kubernetes request")
			w.WriteHeader(http.StatusForbidden)
			return
		}
		reads.Add(1)
		if redirect.Load() {
			http.Redirect(w, r, "/redirected", http.StatusTemporaryRedirect)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(discoveryv1.EndpointSliceList{TypeMeta: metav1.TypeMeta{APIVersion: "discovery.k8s.io/v1", Kind: "EndpointSliceList"}, ListMeta: metav1.ListMeta{ResourceVersion: "1"}}); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	cfg := &rest.Config{Host: server.URL, BearerToken: "local-service-account", TLSClientConfig: rest.TLSClientConfig{CAData: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})}, Proxy: func(*http.Request) (*url.URL, error) {
		t.Error("configured proxy used")
		return nil, errors.New("proxy forbidden")
	}}
	client, closeClient, err := scopedSlices(cfg, "workloads")
	if err != nil {
		t.Fatal(err)
	}
	defer closeClient()
	if _, err := client.List(t.Context(), metav1.ListOptions{}); err != nil {
		t.Fatal(err)
	}
	redirect.Store(true)
	if _, err := client.List(t.Context(), metav1.ListOptions{}); err == nil {
		t.Fatal("redirect accepted")
	}
	if reads.Load() != 2 || redirected.Load() {
		t.Fatal("Kubernetes credentials followed redirect")
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := client.List(canceled, metav1.ListOptions{}); err == nil {
		t.Fatal("canceled request continued")
	}
}
func TestAgentKubernetesDoesNotLoadAmbientKubeconfig(t *testing.T) {
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	t.Setenv("KUBERNETES_SERVICE_PORT", "")
	t.Setenv("KUBECONFIG", "/must-not-be-read")
	if _, _, err := inClusterSlices("workloads"); !errors.Is(err, rest.ErrNotInCluster) {
		t.Fatal("ambient kubeconfig fallback", err)
	}
	for _, cfg := range []*rest.Config{nil, {Host: "http://cluster"}, {Host: "https://cluster", TLSClientConfig: rest.TLSClientConfig{Insecure: true}}, {Host: "https://cluster"}} {
		if _, _, err := scopedSlices(cfg, "workloads"); !errors.Is(err, ErrConfiguration) {
			t.Fatal("unsafe API transport", err)
		}
	}
}
