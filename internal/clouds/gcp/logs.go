package gcp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/xraph/loomplane/internal/clouds/buildlogs"
)

// BuildLogs queries Cloud Logging by project and Cloud Build ID. Finishing a
// scan clears its token so the next scan catches entries ingested late.
func (s *Session) BuildLogs(ctx context.Context, id, cursor string, emit buildlogs.Emit) (nextCursor string, more bool, err error) {
	request := map[string]any{"resourceNames": []string{"projects/" + s.key.ProjectID}, "filter": `resource.type="build" AND resource.labels.build_id=` + strconv.Quote(id), "orderBy": "timestamp asc", "pageSize": 1000, "pageToken": cursor}
	var page struct {
		Entries []struct {
			InsertID, LogName, TextPayload, Severity string
			Timestamp                                time.Time
			JSONPayload                              struct{ Message string }
		} `json:"entries"`
		NextPageToken string `json:"nextPageToken"`
	}
	if err := s.do(ctx, http.MethodPost, s.c.url("logging.googleapis.com", "/v2/entries:list"), request, &page); err != nil {
		var api *APIError
		if errors.As(err, &api) {
			if api.Status == 400 && cursor != "" {
				return cursor, false, &buildlogs.Error{Status: 410, Message: "Cloud Logging page token expired"}
			}
			return cursor, false, &buildlogs.Error{Status: api.Status, Message: "Cloud Logging build output is unavailable"}
		}
		return cursor, false, err
	}
	for _, entry := range page.Entries {
		if entry.InsertID == "" {
			return cursor, false, fmt.Errorf("cloud logging output has no insertId")
		}
		message := entry.TextPayload
		if message == "" {
			message = entry.JSONPayload.Message
		}
		if err := emit(buildlogs.Line{ID: entry.LogName + "/" + entry.InsertID, Stream: "stdout", Message: message, Time: entry.Timestamp, Source: "cloud-build", Level: entry.Severity}); err != nil {
			return cursor, false, err
		}
	}
	return page.NextPageToken, page.NextPageToken != "", nil
}
