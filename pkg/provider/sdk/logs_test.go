package sdk

import (
	"encoding/json"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

type loggingMarker struct{ Provider }

func (loggingMarker) OperationLogs() bool { return true }

func TestOperationOutput_retentionGapAndBoundedHandlerCache(t *testing.T) {
	s := &server{p: loggingMarker{}, ops: map[string]*Operation{}}
	s.ops["operation"] = &Operation{Status: StatusRunning}
	s.enableOutput("operation")
	epoch := s.logs["operation"].epoch
	for range MaxOperationLogLines + 4 {
		s.appendOutput("operation", epoch, "stdout", strings.Repeat("x", 2048))
	}
	request := httptest.NewRequestWithContext(t.Context(), "GET", "/v1/operations/operation/logs?limit=100&cursor="+epoch+":1", nil)
	request.SetPathValue("id", "operation")
	recorder := httptest.NewRecorder()
	s.operationLogs(recorder, request)
	var page LogPage
	if err := json.Unmarshal(recorder.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != 200 || !page.Gap || page.Reason != "expired" || len(page.Lines) != 100 || !page.More {
		t.Fatalf("retention boundary: %+v", page)
	}
	if s.logs["operation"].bytes > MaxOperationLogBytes || len(s.logs["operation"].lines) > MaxOperationLogLines {
		t.Fatal("output buffer exceeded its limit")
	}
	for index := range MaxLogOperations {
		key := "later-" + strconv.Itoa(index)
		s.ops[key] = &Operation{Status: StatusRunning}
		s.enableOutput(key)
	}
	if len(s.logs) > MaxLogOperations {
		t.Fatal("handler output cache exceeded its limit")
	}
	recorder = httptest.NewRecorder()
	s.operationLogs(recorder, request)
	if err := json.Unmarshal(recorder.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if !page.Gap || page.Reason != "expired" || len(page.Lines) != 0 {
		t.Fatalf("evicted operation not reported: %+v", page)
	}
}
