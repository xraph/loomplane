package registry

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRegistryManifestPinsBytesAndPullScope(t *testing.T) {
	raw := []byte("{\n \"schemaVersion\":2,\"mediaType\":\"application/vnd.oci.image.index.v1+json\",\"manifests\":[]\n}")
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256(raw))
	calls := 0
	var endpoint string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodGet {
			t.Error("manifest read mutated registry")
		}
		if r.URL.Path == "/token" {
			if r.URL.Query().Get("scope") != "repository:team/api:pull" {
				t.Error("excessive registry scope", r.URL.RawQuery)
			}
			user, password, ok := r.BasicAuth()
			if !ok || user != "reader" || password != "fixture-secret" {
				t.Error("missing approved token authentication")
			}
			fmt.Fprint(w, `{"token":"scoped-token"}`)
			return
		}
		if r.URL.Path != "/v2/team/api/manifests/"+digest {
			t.Error("wrong manifest path", r.URL.Path)
		}
		if !strings.Contains(r.Header.Get("Accept"), "application/vnd.oci.image.index.v1+json") || !strings.Contains(r.Header.Get("Accept"), "application/vnd.docker.distribution.manifest.list.v2+json") {
			t.Error("missing content negotiation")
		}
		if r.Header.Get("Authorization") != "Bearer scoped-token" {
			w.Header().Set("WWW-Authenticate", `Bearer realm="`+endpoint+`/token",service="fixture",scope="repository:foreign:pull,push"`)
			w.WriteHeader(401)
			return
		}
		w.Header().Set("Content-Type", "application/vnd.oci.image.index.v1+json")
		w.Write(raw)
	}))
	defer server.Close()
	endpoint = server.URL
	cfg, err := ParseConfig([]byte(`{"username":"reader","password":"fixture-secret"}`), map[string]string{"endpoint": endpoint, "repository": "team"})
	if err != nil {
		t.Fatal(err)
	}
	image := strings.TrimPrefix(endpoint, "http://") + "/team/api@" + digest
	got, err := New(cfg).Manifest(t.Context(), image)
	if err != nil || string(got) != string(raw) || calls != 3 {
		t.Fatal("manifest proof bytes or authentication lost", string(got), err, calls)
	}
	for _, bad := range []string{"foreign.example/team/api@" + digest, strings.Replace(image, "/team/", "/team-other/", 1), strings.Replace(image, "/api@", "/../api@", 1), strings.Replace(image, "@"+digest, ":latest", 1), image + "?token=bad"} {
		if _, err = New(cfg).Manifest(t.Context(), bad); err == nil {
			t.Fatal("out-of-scope reference accepted", bad)
		}
	}
	if calls != 3 {
		t.Fatal("invalid references reached registry", calls)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err = New(cfg).Manifest(ctx, image); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation lost", err)
	}
	if calls != 3 {
		t.Fatal("cancelled fetch reached registry")
	}
}

func TestRegistryManifestDeniesUntrustedResponses(t *testing.T) {
	raw := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[]}`)
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256(raw))
	for _, scenario := range []string{"changed bytes", "oversized", "digest header", "redirect", "forbidden", "wrong media", "partial"} {
		t.Run(scenario, func(t *testing.T) {
			sinkCalls := 0
			sink := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { sinkCalls++ }))
			defer sink.Close()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/vnd.oci.image.index.v1+json")
				switch scenario {
				case "changed bytes":
					w.Write(append(raw, ' '))
				case "oversized":
					fmt.Fprint(w, strings.Repeat("a", 256*1024+1))
				case "digest header":
					w.Header().Set("Docker-Content-Digest", "sha256:"+strings.Repeat("a", 64))
					w.Write(raw)
				case "redirect":
					http.Redirect(w, r, sink.URL, http.StatusTemporaryRedirect)
				case "forbidden":
					w.WriteHeader(403)
				case "wrong media":
					w.Header().Set("Content-Type", "text/html")
					w.Write(raw)
				case "partial":
					w.WriteHeader(206)
					w.Write(raw)
				}
			}))
			defer server.Close()
			cfg, err := ParseConfig([]byte(`{"token":"private-token"}`), map[string]string{"endpoint": server.URL, "repository": "team"})
			if err != nil {
				t.Fatal(err)
			}
			got, err := New(cfg).Manifest(t.Context(), strings.TrimPrefix(server.URL, "http://")+"/team/api@"+digest)
			if err == nil || got != nil || sinkCalls != 0 {
				t.Fatal("unsafe manifest response accepted", err, sinkCalls)
			}
			if strings.Contains(err.Error(), "private-token") {
				t.Fatal("token leaked")
			}
		})
	}
}
