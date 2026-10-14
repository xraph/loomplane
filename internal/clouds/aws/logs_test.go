package aws

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/clouds/buildlogs"
)

func TestBuildLogsForwardTokensAndRepeatedText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-amz-json-1.1")
		if strings.Contains(r.Header.Get("X-Amz-Target"), "BatchGetBuilds") {
			w.Write([]byte(`{"builds":[{"id":"project:build","logs":{"groupName":"build-group","streamName":"build-stream"}}]}`))
			return
		}
		var request struct {
			LogGroupName, LogStreamName, NextToken string
			StartFromHead                          bool
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if request.LogGroupName != "build-group" || request.LogStreamName != "build-stream" || !request.StartFromHead {
			t.Errorf("unscoped read: %+v", request)
		}
		if request.NextToken == "" {
			w.Write([]byte(`{"events":[{"timestamp":1000,"ingestionTime":1001,"message":"same"},{"timestamp":1000,"ingestionTime":1001,"message":"same"}],"nextForwardToken":"next"}`))
		} else {
			w.Write([]byte(`{"events":[],"nextForwardToken":"next"}`))
		}
	}))
	defer srv.Close()
	s := New(srv.URL).Session(&Key{AccessKeyID: "AKIATESTTESTTESTTEST", SecretAccessKey: "secret-test-secret-test"}, "us-east-1", "account")
	var lines []buildlogs.Line
	emit := func(l buildlogs.Line) error { lines = append(lines, l); return nil }
	cursor, more, err := s.BuildLogs(context.Background(), "project:build", "", emit)
	if err != nil || cursor != "next" || !more || len(lines) != 2 || lines[0].ID == lines[1].ID {
		t.Fatalf("first page: %q %v %+v %v", cursor, more, lines, err)
	}
	first := lines[0].ID
	lines = nil
	_, _, err = s.BuildLogs(context.Background(), "project:build", "", emit)
	if err != nil || lines[0].ID != first {
		t.Fatalf("unstable replay: %+v %v", lines, err)
	}
	cursor, more, err = s.BuildLogs(context.Background(), "project:build", cursor, emit)
	if err != nil || cursor != "next" || more {
		t.Fatalf("terminal token: %q %v %v", cursor, more, err)
	}
}
