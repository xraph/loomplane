package plugins

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/xraph/loomplane/pkg/provider/sdk"
)

type logProvider struct {
	*fake
	hold chan struct{}
	huge bool
}

func (*logProvider) OperationLogs() bool { return true }
func (p *logProvider) Apply(_ context.Context, job *sdk.Job) (*sdk.Outcome, error) {
	job.Log("stdout", "same line")
	job.Log("stdout", "same line")
	if p.huge {
		job.Log("stderr", strings.Repeat("private-fragment", 2000))
	}
	if p.hold != nil {
		<-p.hold
	}
	return &sdk.Outcome{ExternalID: "service"}, nil
}
func TestOperationLogs_realHandlerPagesReplayAndRestartGap(t *testing.T) {
	p := &logProvider{fake: &fake{}, hold: make(chan struct{})}
	srv := httptest.NewServer(sdk.Handler(p, "tok"))
	defer srv.Close()
	client := New(srv.URL, "tok")
	d, err := client.Describe(t.Context())
	if err != nil || !d.OperationLogs {
		t.Fatalf("capability: %+v %v", d, err)
	}
	req := sdk.OperationRequest{Kind: sdk.OpApply, Job: sdk.Job{Key: "deployment:api", Name: "api"}}
	op, err := client.Start(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	var page *sdk.LogPage
	deadline := time.Now().Add(time.Second)
	for {
		page, err = client.OperationLogs(t.Context(), op.ID, "", 1)
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Lines) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("output not visible before completion")
		}
		time.Sleep(time.Millisecond)
	}
	if page.Status != sdk.StatusRunning || !page.More || page.Lines[0].Message != "same line" {
		t.Fatalf("live page: %+v", page)
	}
	firstID := page.Lines[0].ID
	cursor := page.Cursor
	replay, err := client.OperationLogs(t.Context(), op.ID, "", 1)
	if err != nil || replay.Lines[0].ID != firstID || replay.Cursor != cursor {
		t.Fatalf("unstable replay: %+v %v", replay, err)
	}
	second, err := client.OperationLogs(t.Context(), op.ID, cursor, 1)
	if err != nil || len(second.Lines) != 1 || second.Lines[0].Message != "same line" || second.Lines[0].ID == firstID {
		t.Fatalf("distinct repeated output: %+v %v", second, err)
	}
	close(p.hold)
	wait(t, client, op.ID)
	restarted := httptest.NewServer(sdk.Handler(&logProvider{fake: &fake{}}, "tok"))
	defer restarted.Close()
	next := New(restarted.URL, "tok")
	newOp, err := next.Start(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	wait(t, next, newOp.ID)
	gap, err := next.OperationLogs(t.Context(), newOp.ID, second.Cursor, 100)
	if err != nil || !gap.Gap || gap.Reason != "restarted" || len(gap.Lines) != 2 || gap.Lines[0].ID == firstID {
		t.Fatalf("restart gap: %+v %v", gap, err)
	}
}
func TestOperationLogs_oversizedLineOmittedAndLegacyCapabilityAbsent(t *testing.T) {
	srv := httptest.NewServer(sdk.Handler(&logProvider{fake: &fake{}, huge: true}, "tok"))
	defer srv.Close()
	client := New(srv.URL, "tok")
	op, err := client.Start(t.Context(), sdk.OperationRequest{Kind: sdk.OpApply, Job: sdk.Job{Key: "large-line"}})
	if err != nil {
		t.Fatal(err)
	}
	wait(t, client, op.ID)
	page, err := client.OperationLogs(t.Context(), op.ID, "", 100)
	if err != nil || len(page.Lines) != 3 || !page.Lines[2].Truncated || strings.Contains(page.Lines[2].Message, "private-fragment") {
		t.Fatalf("oversized output: %+v %v", page, err)
	}
	legacy := httptest.NewServer(sdk.Handler(&fake{}, "tok"))
	defer legacy.Close()
	d, err := New(legacy.URL, "tok").Describe(t.Context())
	if err != nil || d.OperationLogs {
		t.Fatalf("legacy capability: %+v %v", d, err)
	}
}
