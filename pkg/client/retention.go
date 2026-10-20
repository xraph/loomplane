package client

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

type RetentionPoint struct {
	BackupID string `json:"backup_id"`
	Bytes    int64  `json:"bytes"`
	Eligible bool   `json:"eligible"`
	Reason   string `json:"reason"`
}

type RetentionPreview struct {
	AsOf          time.Time        `json:"as_of"`
	Complete      bool             `json:"complete"`
	EligibleCount int              `json:"eligible_count"`
	EligibleBytes int64            `json:"eligible_bytes"`
	Points        []RetentionPoint `json:"points"`
}

func (c *Client) BackupRetentionPreview(ctx context.Context, id string, days, keep int) (*RetentionPreview, error) {
	query := url.Values{"older_than_days": {strconv.Itoa(days)}, "keep_last": {strconv.Itoa(keep)}}
	var preview RetentionPreview
	err := c.do(ctx, http.MethodGet, "/api/v1/service-backups/"+esc(id)+"/retention-preview?"+query.Encode(), nil, &preview)
	return &preview, err
}
