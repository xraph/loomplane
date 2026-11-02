package registry

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestImagePlatformVerifiesConfigBytes(t *testing.T) {
	for _, scenario := range []string{"valid", "architecture", "tampered-config", "descriptor-url", "oversized", "redirect"} {
		t.Run(scenario, func(t *testing.T) {
			config := []byte(`{"architecture":"arm64","os":"linux"}`)
			if scenario == "architecture" {
				config = []byte(`{"architecture":"amd64","os":"linux"}`)
			}
			configDigest := fmt.Sprintf("sha256:%x", sha256.Sum256(config))
			descriptor := fmt.Sprintf(`{"mediaType":"application/vnd.oci.image.config.v1+json","size":%d,"digest":%q}`, len(config), configDigest)
			if scenario == "descriptor-url" {
				descriptor = strings.TrimSuffix(descriptor, "}") + `,"urls":["https://foreign.example/config"]}`
			}
			manifest := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":` + descriptor + `,"layers":[]}`)
			manifestDigest := fmt.Sprintf("sha256:%x", sha256.Sum256(manifest))
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/v2/team/api/manifests/" + manifestDigest:
					w.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
					_, _ = w.Write(manifest)
				case "/v2/team/api/blobs/" + configDigest:
					switch scenario {
					case "tampered-config":
						_, _ = w.Write([]byte(`{"architecture":"arm64","os":"other"}`))
					case "oversized":
						_, _ = w.Write([]byte(strings.Repeat("x", 256<<10+1)))
					case "redirect":
						http.Redirect(w, r, "https://foreign.example/config", http.StatusTemporaryRedirect)
					default:
						_, _ = w.Write(config)
					}
				default:
					t.Error("out-of-scope registry read", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			cfg, err := ParseConfig([]byte(`{"token":"fixture-token"}`), map[string]string{"endpoint": server.URL, "repository": "team"})
			if err != nil {
				t.Fatal(err)
			}
			image := strings.TrimPrefix(server.URL, "http://") + "/team/api@" + manifestDigest
			err = New(cfg).VerifyPlatform(t.Context(), image, "linux", "arm64")
			if scenario == "valid" && err != nil || scenario != "valid" && err == nil {
				t.Fatal(scenario, err)
			}
		})
	}
}

func TestImagePlatformIndexCannotOverrideConfig(t *testing.T) {
	config := []byte(`{"architecture":"amd64","os":"linux"}`)
	configDigest := fmt.Sprintf("sha256:%x", sha256.Sum256(config))
	manifest := []byte(fmt.Sprintf(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"mediaType":"application/vnd.oci.image.config.v1+json","size":%d,"digest":%q}}`, len(config), configDigest))
	manifestDigest := fmt.Sprintf("sha256:%x", sha256.Sum256(manifest))
	index := []byte(fmt.Sprintf(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[{"mediaType":"application/vnd.oci.image.manifest.v1+json","size":%d,"digest":%q,"platform":{"os":"linux","architecture":"arm64"}}]}`, len(manifest), manifestDigest))
	indexDigest := fmt.Sprintf("sha256:%x", sha256.Sum256(index))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
		switch r.URL.Path {
		case "/v2/team/api/manifests/" + indexDigest:
			w.Header().Set("Content-Type", "application/vnd.oci.image.index.v1+json")
			_, _ = w.Write(index)
		case "/v2/team/api/manifests/" + manifestDigest:
			_, _ = w.Write(manifest)
		case "/v2/team/api/blobs/" + configDigest:
			_, _ = w.Write(config)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	cfg, err := ParseConfig([]byte(`{"token":"fixture-token"}`), map[string]string{"endpoint": server.URL, "repository": "team"})
	if err != nil {
		t.Fatal(err)
	}
	if err = New(cfg).VerifyPlatform(t.Context(), strings.TrimPrefix(server.URL, "http://")+"/team/api@"+indexDigest, "linux", "arm64"); err == nil {
		t.Fatal("index label overrode actual config")
	}
}
