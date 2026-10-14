package aws

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	sdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/codebuild"
	"github.com/aws/smithy-go"

	"github.com/xraph/loomplane/internal/clouds/buildlogs"
)

// BuildLogs obtains the stream from this CodeBuild operation, never from an
// environment-wide log group. Forward tokens are committed after the page.
func (s *Session) BuildLogs(ctx context.Context, id, cursor string, emit buildlogs.Emit) (nextCursor string, more bool, err error) {
	builds, err := codebuild.NewFromConfig(s.cfg).BatchGetBuilds(ctx, &codebuild.BatchGetBuildsInput{Ids: []string{id}})
	if err != nil {
		return cursor, false, logError(err)
	}
	if len(builds.Builds) != 1 || sdk.ToString(builds.Builds[0].Id) != id {
		return cursor, false, &buildlogs.Error{Status: 404, Message: "CodeBuild operation is unavailable"}
	}
	logs := builds.Builds[0].Logs
	if logs == nil || sdk.ToString(logs.GroupName) == "" || sdk.ToString(logs.StreamName) == "" {
		return cursor, false, &buildlogs.Error{Status: 503, Message: "CodeBuild has not published its log stream"}
	}
	in := &cloudwatchlogs.GetLogEventsInput{LogGroupName: logs.GroupName, LogStreamName: logs.StreamName, StartFromHead: sdk.Bool(true), Limit: sdk.Int32(1000)}
	if cursor != "" {
		in.NextToken = sdk.String(cursor)
	}
	page, err := cloudwatchlogs.NewFromConfig(s.cfg).GetLogEvents(ctx, in)
	if err != nil {
		return cursor, false, logError(err)
	}
	for index, event := range page.Events {
		// GetLogEvents has no event ID. A page token and ordinal preserve two
		// identical events while keeping replay of that page idempotent.
		sum := sha256.Sum256(fmt.Appendf(nil, "%s/%s/%d/%d/%d", sdk.ToString(logs.StreamName), cursor, index, sdk.ToInt64(event.Timestamp), sdk.ToInt64(event.IngestionTime)))
		if err := emit(buildlogs.Line{ID: hex.EncodeToString(sum[:]), Stream: "stdout", Message: sdk.ToString(event.Message), Time: time.UnixMilli(sdk.ToInt64(event.Timestamp)), Source: "codebuild", Level: "info"}); err != nil {
			return cursor, false, err
		}
	}
	next := sdk.ToString(page.NextForwardToken)
	return next, next != "" && next != cursor, nil
}

func logError(err error) error {
	var api smithy.APIError
	if errors.As(err, &api) {
		status := 500
		switch {
		case strings.Contains(api.ErrorCode(), "AccessDenied"), strings.Contains(api.ErrorCode(), "Unauthorized"):
			status = 403
		case strings.Contains(api.ErrorCode(), "Throttl"):
			status = 429
		case strings.Contains(api.ErrorCode(), "ResourceNotFound"):
			status = 404
		case strings.Contains(api.ErrorCode(), "InvalidParameter"):
			status = 410
		}
		return &buildlogs.Error{Status: status, Message: "CloudWatch CodeBuild output is unavailable"}
	}
	return err
}
