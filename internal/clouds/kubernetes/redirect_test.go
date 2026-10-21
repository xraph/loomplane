package kubernetes

import (
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
)

func TestClientDoesNotFollowCredentialBearingRedirect(t *testing.T) {
	var redirected atomic.Int32
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		redirected.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"apiVersion":"v1","kind":"Namespace","metadata":{"name":"workloads","uid":"namespace-uid"}}`))
	}))
	defer target.Close()
	selected := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer selected-cluster-token" {
			t.Error("credential missing on selected connection")
		}
		http.Redirect(w, r, target.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	defer selected.Close()
	client, err := New(&Config{Namespace: "workloads", REST: &rest.Config{Host: selected.URL, BearerToken: "selected-cluster-token", TLSClientConfig: rest.TLSClientConfig{CAData: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: selected.Certificate().Raw})}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = client.Kube.CoreV1().Namespaces().Get(t.Context(), "workloads", metav1.GetOptions{}); err == nil {
		t.Fatal("redirected cluster response accepted")
	}
	if redirected.Load() != 0 {
		t.Fatal("credential-bearing request followed a redirect")
	}
}
