package vercel

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/xraph/loomplane/internal/clouds/buildlogs"
)

// BuildLogs follows NDJSON events for exactly one deployment. Cursor overlaps
// the last timestamp; stable provider IDs let persisted output deduplicate it.
func (c *Client) BuildLogs(ctx context.Context, tok Token, id, cursor string, follow bool, emit buildlogs.Emit) (nextCursor string, more bool, err error) {
	query := url.Values{"direction": {"forward"}, "builds": {"1"}, "format": {"lines"}, "limit": {"-1"}}
	if follow {
		query.Set("follow", "1")
	} else {
		query.Set("follow", "0")
	}
	if tok.TeamID != "" {
		query.Set("teamId", tok.TeamID)
	}
	if since, parseErr := strconv.ParseInt(cursor, 10, 64); follow && parseErr == nil && since > 0 {
		query.Set("since", strconv.FormatInt(max(0, since-5000), 10))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint+"/v3/deployments/"+url.PathEscape(id)+"/events?"+query.Encode(), http.NoBody)
	if err != nil {
		return cursor, false, err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(tok.Value))
	response, err := c.http.Do(req)
	if err != nil {
		return cursor, false, err
	}
	defer response.Body.Close() // read-only
	if response.StatusCode >= 300 {
		seconds, parseErr := strconv.Atoi(response.Header.Get("Retry-After"))
		if parseErr != nil {
			seconds = 0
		}
		return cursor, false, &buildlogs.Error{Status: response.StatusCode, RetryAfter: time.Duration(seconds) * time.Second, Message: "Vercel deployment output is unavailable"}
	}
	reader := bufio.NewReaderSize(io.LimitReader(response.Body, 10<<20), 64<<10)
	last := int64(0)
	if cursor != "" {
		var parseErr error
		last, parseErr = strconv.ParseInt(cursor, 10, 64)
		if parseErr != nil {
			return cursor, false, &buildlogs.Error{Status: 410, Message: "Invalid Vercel log cursor"}
		}
	}
	for count := 0; count < 10000; count++ {
		raw, readErr := reader.ReadBytes('\n')
		if len(raw) > 64<<10 {
			return cursor, false, errors.New("vercel output frame exceeds the limit")
		}
		raw = []byte(strings.TrimSpace(string(raw)))
		if len(raw) > 0 {
			var event struct {
				ID, Type, Text, DeploymentID string
				Created                      json.Number
				Payload                      struct {
					ID, Text, Type, DeploymentID string
					Created                      json.Number
				}
			}
			if err := json.Unmarshal(raw, &event); err != nil {
				return cursor, false, err
			}
			deployment := event.Payload.DeploymentID
			if deployment == "" {
				deployment = event.DeploymentID
			}
			if deployment == "" || deployment == id {
				kind := event.Type
				if kind == "" {
					kind = event.Payload.Type
				}
				if kind == "stdout" || kind == "stderr" || kind == "command" {
					identity := event.Payload.ID
					if identity == "" {
						identity = event.ID
					}
					if identity == "" {
						return cursor, false, errors.New("vercel output has no stable event identity")
					}
					created, parseErr := event.Created.Int64()
					if parseErr != nil {
						created = 0
					}
					if created == 0 {
						created, parseErr = event.Payload.Created.Int64()
						if parseErr != nil {
							created = 0
						}
					}
					message := event.Payload.Text
					if message == "" {
						message = event.Text
					}
					if err := emit(buildlogs.Line{ID: identity, Stream: kind, Message: message, Source: "vercel", Level: "info", Time: time.UnixMilli(created)}); err != nil {
						return cursor, false, err
					}
					last = max(last, created)
				}
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return strconv.FormatInt(last, 10), false, nil
			}
			return cursor, false, readErr
		}
	}
	return cursor, false, errors.New("vercel output range exceeds the bounded catch-up")
}
