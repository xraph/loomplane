package client

import (
	"context"
	"net/http"
	"time"
)

type NativeCopyCredentials struct {
	CopyID         string    `json:"copy_id"`
	Provider       string    `json:"provider"`
	ConnectionName string    `json:"connection_name"`
	Database       string    `json:"database"`
	Username       string    `json:"username"`
	Password       string    `json:"password"`
	ExpiresAt      time.Time `json:"expires_at"`
	CheckedAt      time.Time `json:"checked_at"`
}

func (c *Client) NativeRecoveryCopyCredentials(ctx context.Context, id string) (*NativeCopyCredentials, error) {
	var credentials NativeCopyCredentials
	if err := c.do(ctx, http.MethodPost, "/api/v1/native-recovery-copies/"+esc(id)+"/credentials", nil, &credentials); err != nil {
		return nil, err
	}
	return &credentials, nil
}
