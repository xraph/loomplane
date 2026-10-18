package kubernetes

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	authv1 "k8s.io/api/authorization/v1"
)

func clusterFixture(t *testing.T, denySecret bool, denied ...string) (*httptest.Server, *[]string, *sync.Mutex) {
	t.Helper()
	paths := []string{}
	mu := &sync.Mutex{}
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Authorization") != "Bearer test-token" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"Unauthorized","code":401}`))
			return
		}
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		switch {
		case r.URL.Path == "/version":
			_, _ = w.Write([]byte(`{"major":"1","minor":"36","gitVersion":"v1.36.1"}`))
		case r.URL.Path == "/apis/storage.k8s.io/v1/storageclasses/retained":
			_, _ = w.Write([]byte(`{"kind":"StorageClass","apiVersion":"storage.k8s.io/v1","metadata":{"name":"retained","uid":"class-identity"},"provisioner":"example.csi","reclaimPolicy":"Retain","volumeBindingMode":"WaitForFirstConsumer","allowVolumeExpansion":true}`))
		case strings.HasPrefix(r.URL.Path, "/api/v1/namespaces/"):
			ns := strings.TrimPrefix(r.URL.Path, "/api/v1/namespaces/")
			_ = json.NewEncoder(w).Encode(map[string]any{"kind": "Namespace", "apiVersion": "v1", "metadata": map[string]string{"name": ns, "uid": "uid-" + ns}})
		case r.URL.Path == "/apis/authorization.k8s.io/v1/selfsubjectaccessreviews":
			var review authv1.SelfSubjectAccessReview
			if err := json.NewDecoder(r.Body).Decode(&review); err != nil {
				t.Errorf("permission request used %s: %v", r.Header.Get("Content-Type"), err)
				w.WriteHeader(400)
				return
			}
			attrs := review.Spec.ResourceAttributes
			if attrs == nil || strings.Contains(attrs.Resource, "/") || attrs.Namespace == "" && attrs.Resource != "persistentvolumes" {
				t.Error("unscoped object permission check")
				w.WriteHeader(400)
				return
			}
			mu.Lock()
			resource := attrs.Resource
			if attrs.Subresource != "" {
				resource += "/" + attrs.Subresource
			}
			paths = append(paths, attrs.Namespace+":"+resource+":"+attrs.Verb)
			mu.Unlock()
			review.Status.Allowed = (!denySecret || attrs.Resource != "secrets" || attrs.Verb != "create") && !slices.Contains(denied, resource+":"+attrs.Verb)
			_ = json.NewEncoder(w).Encode(review)
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"NotFound","code":404}`))
		}
	}))
	t.Cleanup(s.Close)
	return s, &paths, mu
}

func TestClientChecksSelectedPersistentStorage(t *testing.T) {
	s, paths, mu := clusterFixture(t, false)
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: s.Certificate().Raw})
	cfg, err := ParseConfig(kubeconfig(t, s.URL, ca), map[string]string{"namespace": "production", "storage_class": "retained"})
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
	if result.Details["storage_class"] != "retained" || result.Details["storage_class_uid"] != "class-identity" || result.Details["persistent_storage"] != "ready" || result.Details["storage_expansion"] != "true" {
		t.Fatalf("storage was not checked: %+v", result)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, want := range []string{"production:statefulsets:create", "production:statefulsets:delete", "production:persistentvolumeclaims:create", "production:persistentvolumeclaims:list", ":persistentvolumes:get"} {
		if !slices.Contains(*paths, want) {
			t.Fatalf("unchecked lifecycle permission %s", want)
		}
	}
}

func TestClient_namespacePermissionsRemainIsolated(t *testing.T) {
	s, paths, mu := clusterFixture(t, true)
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: s.Certificate().Raw})
	raw := kubeconfig(t, s.URL, ca)
	for _, ns := range []string{"production", "preview"} {
		cfg, err := ParseConfig(raw, map[string]string{"namespace": ns})
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
		if result.Details["namespace"] != ns || result.Details["namespace_uid"] != "uid-"+ns || !slices.Contains(result.Missing, "secrets:create") {
			t.Fatalf("scope or permissions: %+v", result)
		}
		if result.Details["hpa"] == "ready" {
			t.Fatal("no metrics API was reported as ready")
		}
	}
	mu.Lock()
	defer mu.Unlock()
	for _, want := range []string{"/api/v1/namespaces/production", "/api/v1/namespaces/preview", "production:deployments:create", "preview:deployments:create"} {
		if !slices.Contains(*paths, want) {
			t.Fatalf("missing scoped request %s: %v", want, *paths)
		}
	}
}

func TestClient_refusedTokenIsRejected(t *testing.T) {
	s, _, _ := clusterFixture(t, false)
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: s.Certificate().Raw})
	raw := kubeconfig(t, s.URL, ca)
	cfg, err := ParseConfig(raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	cfg.REST.BearerToken = "refused-token"
	c, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.Check(t.Context()); !errors.Is(err, ErrRejected) {
		t.Fatalf("rejected credential: %v", err)
	}
}

func TestClient_cancelledCheckDoesNotContactTheCluster(t *testing.T) {
	s, paths, mu := clusterFixture(t, false)
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: s.Certificate().Raw})
	cfg, err := ParseConfig(kubeconfig(t, s.URL, ca), nil)
	if err != nil {
		t.Fatal(err)
	}
	c, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err = c.Check(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled check: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(*paths) != 0 {
		t.Fatalf("cancelled check sent requests: %v", *paths)
	}
}
