package registry

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	domain "github.com/xraph/loomplane/pkg/federation"
)

type imageDescriptor struct {
	MediaType string                                     `json:"mediaType"`
	Digest    string                                     `json:"digest"`
	Size      int64                                      `json:"size"`
	URLs      []string                                   `json:"urls"`
	Platform  struct{ OS, Architecture, Variant string } `json:"platform"`
}

func (d imageDescriptor) valid() bool {
	return len(d.URLs) == 0 && d.Size > 0 && d.Size <= domain.MaxImageIndexBytes && domain.ValidPinnedImage("registry.example.test/image@"+d.Digest)
}

// VerifyPlatform follows only content-addressed descriptors within the selected
// repository. An index's platform label alone does not prove its image config.
func (c *Client) VerifyPlatform(ctx context.Context, image, os, architecture string) error {
	if os != "linux" || (architecture != "amd64" && architecture != "arm64") {
		return ErrInvalid
	}
	return c.verifyPlatform(ctx, image, os, architecture, 0)
}
func (c *Client) verifyPlatform(ctx context.Context, image, os, architecture string, depth int) error {
	if depth > 3 {
		return ErrInvalid
	}
	raw, err := c.Manifest(ctx, image)
	if err != nil {
		return err
	}
	var manifest struct {
		SchemaVersion int               `json:"schemaVersion"`
		MediaType     string            `json:"mediaType"`
		Config        imageDescriptor   `json:"config"`
		Manifests     []imageDescriptor `json:"manifests"`
	}
	if json.Unmarshal(raw, &manifest) != nil || manifest.SchemaVersion != 2 {
		return ErrInvalid
	}
	repository, _, _ := strings.Cut(image, "@")
	switch manifest.MediaType {
	case "application/vnd.oci.image.index.v1+json", "application/vnd.docker.distribution.manifest.list.v2+json":
		if len(manifest.Manifests) == 0 || len(manifest.Manifests) > 64 {
			return ErrInvalid
		}
		selected := ""
		for _, descriptor := range manifest.Manifests {
			if !descriptor.valid() {
				return ErrInvalid
			}
			if descriptor.Platform.OS != os || descriptor.Platform.Architecture != architecture {
				continue
			}
			if descriptor.Platform.Variant != "" && (architecture != "arm64" || descriptor.Platform.Variant != "v8") {
				continue
			}
			if selected != "" {
				return ErrInvalid
			}
			selected = descriptor.Digest
		}
		if selected == "" {
			return ErrInvalid
		}
		return c.verifyPlatform(ctx, repository+"@"+selected, os, architecture, depth+1)
	case "application/vnd.oci.image.manifest.v1+json", "application/vnd.docker.distribution.manifest.v2+json":
		descriptor := manifest.Config
		if !descriptor.valid() || (descriptor.MediaType != "application/vnd.oci.image.config.v1+json" && descriptor.MediaType != "application/vnd.docker.container.image.v1+json") {
			return ErrInvalid
		}
		raw, err = c.imageConfig(ctx, repository, descriptor)
		if err != nil {
			return err
		}
		var config struct{ OS, Architecture string }
		if json.Unmarshal(raw, &config) != nil || config.OS != os || config.Architecture != architecture {
			return ErrInvalid
		}
		return nil
	default:
		return ErrInvalid
	}
}
func (c *Client) imageConfig(ctx context.Context, repository string, descriptor imageDescriptor) ([]byte, error) {
	endpoint, err := url.Parse(c.Config.Endpoint)
	if err != nil || !strings.HasPrefix(repository, endpoint.Host+"/") {
		return nil, ErrInvalid
	}
	repository = strings.TrimPrefix(repository, endpoint.Host+"/")
	if !repositoryPattern.MatchString(repository) || !strings.HasPrefix(repository, c.Config.Repository+"/") {
		return nil, ErrInvalid
	}
	response, err := c.requestScoped(ctx, http.MethodGet, "/v2/"+repository+"/blobs/"+descriptor.Digest, repository, "pull", nil)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, ErrRejected
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, domain.MaxImageIndexBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) != descriptor.Size || fmt.Sprintf("sha256:%x", sha256.Sum256(raw)) != descriptor.Digest {
		return nil, ErrInvalid
	}
	if header := response.Header.Get("Docker-Content-Digest"); header != "" && header != descriptor.Digest {
		return nil, ErrInvalid
	}
	return raw, nil
}
