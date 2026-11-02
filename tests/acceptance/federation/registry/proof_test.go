package registry_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/xraph/loomplane/internal/clouds/registry"
	domain "github.com/xraph/loomplane/pkg/federation"
)

// TestRegistryProofLive writes only into a disposable loopback registry selected
// explicitly by the local qualification runner. Removing that container removes
// the fixture repository and blobs; no external registry credential is accepted.
func TestRegistryProofLive(t *testing.T) {
	endpoint := os.Getenv("LOOMPLANE_TEST_REGISTRY_URL")
	if endpoint == "" {
		t.Skip("requires a disposable loopback registry")
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path != "" || parsed.Scheme != "http" || !net.ParseIP(parsed.Hostname()).IsLoopback() {
		t.Fatal("fixture requires a literal loopback registry URL")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	httpClient := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer httpClient.CloseIdleConnections()
	request := func(method, path, media string, body []byte) http.Header {
		t.Helper()
		req, requestErr := http.NewRequestWithContext(ctx, method, endpoint+path, bytes.NewReader(body))
		if requestErr != nil {
			t.Fatal(requestErr)
		}
		if media != "" {
			req.Header.Set("Content-Type", media)
		}
		response, requestErr := httpClient.Do(req)
		if requestErr != nil {
			t.Fatal(requestErr)
		}
		defer response.Body.Close()
		diagnostic, readErr := io.ReadAll(io.LimitReader(response.Body, 4096))
		if readErr != nil {
			t.Fatal(readErr)
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			t.Fatalf("fixture %s: HTTP %d: %s", method, response.StatusCode, diagnostic)
		}
		return response.Header
	}
	repository := "qualification/proof-" + uuid.NewString()
	config := []byte(`{"architecture":"arm64","os":"linux","rootfs":{"type":"layers","diff_ids":[]}}`)
	configDigest := fmt.Sprintf("sha256:%x", sha256.Sum256(config))
	headers := request(http.MethodPost, "/v2/"+repository+"/blobs/uploads/", "", nil)
	upload, err := url.Parse(headers.Get("Location"))
	if err != nil || upload.User != nil || upload.Fragment != "" || (upload.IsAbs() && (upload.Scheme != parsed.Scheme || upload.Host != parsed.Host)) || !strings.HasPrefix(upload.Path, "/v2/"+repository+"/blobs/uploads/") {
		t.Fatal("untrusted upload location", err)
	}
	query := upload.Query()
	query.Set("digest", configDigest)
	upload.RawQuery = query.Encode()
	request(http.MethodPut, upload.RequestURI(), "application/octet-stream", config)
	manifest := []byte(fmt.Sprintf(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":%q,"size":%d},"layers":[]}`, configDigest, len(config)))
	manifestDigest := fmt.Sprintf("sha256:%x", sha256.Sum256(manifest))
	request(http.MethodPut, "/v2/"+repository+"/manifests/"+manifestDigest, "application/vnd.oci.image.manifest.v1+json", manifest)
	index := []byte(fmt.Sprintf("{\n\"schemaVersion\":2,\"mediaType\":\"application/vnd.oci.image.index.v1+json\",\"manifests\":[{\"mediaType\":\"application/vnd.oci.image.manifest.v1+json\",\"digest\":%q,\"size\":%d,\"platform\":{\"os\":\"linux\",\"architecture\":\"arm64\"}}]\n}", manifestDigest, len(manifest)))
	indexDigest := fmt.Sprintf("sha256:%x", sha256.Sum256(index))
	request(http.MethodPut, "/v2/"+repository+"/manifests/"+indexDigest, "application/vnd.oci.image.index.v1+json", index)
	cfg, err := registry.ParseConfig([]byte(`{}`), map[string]string{"endpoint": endpoint, "repository": "qualification"})
	if err != nil {
		t.Fatal(err)
	}
	client := registry.New(cfg)
	defer client.HTTP.CloseIdleConnections()
	image := parsed.Host + "/" + repository + "@" + indexDigest
	reported := "runtime.invalid/import@" + manifestDigest
	proofs, err := domain.CollectImageIndexes(ctx, image, reported, "linux", "arm64", client.Manifest)
	if err != nil || len(proofs) != 1 || !bytes.Equal(proofs[0], index) {
		t.Fatal("live raw index proof failed", err)
	}
	if selected, ok := domain.ResolveImageSelector(image, reported, "linux", "arm64", proofs); !ok || selected != reported {
		t.Fatal("retrieved proof did not verify")
	}
	if _, err = domain.CollectImageIndexes(ctx, image, reported, "linux", "amd64", client.Manifest); err == nil {
		t.Fatal("wrong platform accepted")
	}
	if _, err = client.Manifest(ctx, parsed.Host+"/"+repository+":latest"); err == nil {
		t.Fatal("mutable tag accepted")
	}
	t.Log("PASS real registry preserves raw index bytes, proves platform digest and rejects wrong platform and mutable tags")
}
