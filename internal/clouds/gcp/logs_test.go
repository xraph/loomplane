package gcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/clouds/buildlogs"
)

func TestBuildLogsEntryIDsAndPagination(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ResourceNames              []string
			Filter, PageToken, OrderBy string
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if r.URL.Path != "/v2/entries:list" || len(request.ResourceNames) != 1 || request.ResourceNames[0] != "projects/project-test" || !strings.Contains(request.Filter, `resource.labels.build_id="build-test"`) || request.OrderBy != "timestamp asc" {
			t.Errorf("unscoped request: %+v", request)
		}
		if request.PageToken == "" {
			w.Write([]byte(`{"entries":[{"insertId":"a","textPayload":"same","timestamp":"2026-10-14T00:00:00Z","logName":"build"}],"nextPageToken":"next"}`))
		} else {
			w.Write([]byte(`{"entries":[{"insertId":"b","textPayload":"same","timestamp":"2026-10-14T00:00:00Z","logName":"build"}]}`))
		}
	}))
	defer srv.Close()
	s := &Session{c: New(srv.URL), key: &Key{ProjectID: "project-test"}, http: srv.Client()}
	var lines []buildlogs.Line
	emit := func(l buildlogs.Line) error { lines = append(lines, l); return nil }
	cursor, more, err := s.BuildLogs(context.Background(), "build-test", "", emit)
	if err != nil || cursor != "next" || !more {
		t.Fatalf("first page: %q %v %v", cursor, more, err)
	}
	cursor, more, err = s.BuildLogs(context.Background(), "build-test", cursor, emit)
	if err != nil || cursor != "" || more || len(lines) != 2 || lines[0].ID == lines[1].ID {
		t.Fatalf("last page: %q %v %+v %v", cursor, more, lines, err)
	}
}
