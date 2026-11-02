package registry

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"slices"
	"strings"

	domain "github.com/xraph/loomplane/pkg/federation"
)

const manifestAccept = "application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json"

// Manifest reads bounded content-addressed bytes from the reviewed repository.
// It never follows a descriptor URL, tag or redirect and requests pull scope only.
func (c *Client) Manifest(ctx context.Context, reference string) ([]byte, error) {
	if c == nil || c.Config == nil || c.HTTP == nil || !domain.ValidPinnedImage(reference) {
		return nil, ErrInvalid
	}
	endpoint, err := url.Parse(c.Config.Endpoint)
	if err != nil {
		return nil, ErrInvalid
	}
	repository, digest, _ := strings.Cut(reference, "@")
	if !strings.HasPrefix(repository, endpoint.Host+"/") {
		return nil, ErrInvalid
	}
	repository = strings.TrimPrefix(repository, endpoint.Host+"/")
	if !repositoryPattern.MatchString(repository) || !strings.HasPrefix(repository, c.Config.Repository+"/") {
		return nil, ErrInvalid
	}
	response, err := c.requestScoped(ctx, http.MethodGet, "/v2/"+repository+"/manifests/"+digest, repository, "pull", http.Header{"Accept": {manifestAccept}})
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return nil, ErrRejected
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("registry manifest returned HTTP %d", response.StatusCode)
	}
	media, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || !slices.Contains(strings.Split(manifestAccept, ", "), media) {
		return nil, ErrInvalid
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, domain.MaxImageIndexBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 || len(raw) > domain.MaxImageIndexBytes || fmt.Sprintf("sha256:%x", sha256.Sum256(raw)) != digest {
		return nil, ErrInvalid
	}
	if header := response.Header.Get("Docker-Content-Digest"); header != "" && header != digest {
		return nil, ErrInvalid
	}
	return raw, nil
}
