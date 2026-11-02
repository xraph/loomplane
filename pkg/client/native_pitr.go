package client

import (
	"context"
	"net/http"
	"time"
)

type NativePITRWindow struct {
	SourceID          string     `json:"source_id"`
	Provider          string     `json:"provider"`
	Available         bool       `json:"available"`
	PointID           string     `json:"point_id,omitempty"`
	Earliest          *time.Time `json:"earliest,omitempty"`
	Latest            *time.Time `json:"latest,omitempty"`
	ObservedAt        *time.Time `json:"observed_at,omitempty"`
	UnavailableReason string     `json:"unavailable_reason,omitempty"`
}

func (c *Client) NativeRecoveryPITRWindow(ctx context.Context, source string) (*NativePITRWindow, error) {
	var out NativePITRWindow
	err := c.do(ctx, http.MethodGet, "/api/v1/native-recovery-sources/"+esc(source)+"/pitr-window", nil, &out)
	return &out, err
}
