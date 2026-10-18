package client

import (
	"context"
	"net/http"

	"github.com/xraph/loomplane/pkg/provider"
)

type StoredVolume = provider.StoredVolume

// StorageVolumes reads owned persistent storage on one checked connection.
func (c *Client) StorageVolumes(ctx context.Context, connection string) ([]StoredVolume, error) {
	var out struct {
		Volumes []StoredVolume `json:"volumes"`
	}
	err := c.do(ctx, http.MethodGet, "/api/v1/connections/"+esc(connection)+"/storage", nil, &out)
	return out.Volumes, err
}
