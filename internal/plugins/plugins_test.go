package plugins

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/pkg/provider"
	"github.com/xraph/loomplane/pkg/provider/sdk"
)

type fake struct {
	applies atomic.Int32
	release chan struct{}
	panics  bool
}

func (f *fake) Describe() provider.Provider {
	return provider.Provider{
		Name: "fake", Title: "Fake", Cloud: "fake", Description: "A provider for tests.", Model: provider.Build,
		Runs: []provider.Runs{{Workload: provider.HTTPService, Execution: provider.Container, Creates: "thing"}},
	}
}

func (f *fake) Check(_ context.Context, c sdk.Credential) (*sdk.Account, error) {
	if string(c.Secret) != "good" {
		return nil, fmt.Errorf("%w: bad secret", sdk.ErrRejected)
	}
	return &sdk.Account{Account: "acct", Details: map[string]string{"region": c.Settings["region"]}}, nil
}

func (f *fake) Apply(_ context.Context, job *sdk.Job) (*sdk.Outcome, error) {
	f.applies.Add(1)
	if f.panics {
		panic("boom")
	}
	job.Progress("working")
	job.Checkpoint([]byte("step-1"))
	if f.release != nil {
		<-f.release
	}
	src, err := job.Source()
	if err != nil {
		return nil, err
	}
	defer src.Close()
	b, _ := io.ReadAll(src)
	return &sdk.Outcome{ExternalID: job.Name, Revision: string(b)}, nil
}

func (f *fake) Remove(context.Context, *sdk.Job) error { return nil }

func (f *fake) Observe(_ context.Context, t *sdk.Target) (*sdk.Observation, error) {
	return &sdk.Observation{Exists: t.ExternalID == "api", Revision: "r1"}, nil
}

func wait(t *testing.T, c *Client, id string) *sdk.Operation {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		op, err := c.Operation(t.Context(), id)
		if err != nil {
			t.Fatal(err)
		}
		if op.Status != sdk.StatusRunning {
			return op
		}
		if time.Now().After(deadline) {
			t.Fatal("the operation never finished")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestProtocol(t *testing.T) {
	f := &fake{release: make(chan struct{})}
	srv := httptest.NewServer(sdk.Handler(f, "tok"))
	defer srv.Close()
	ctx := t.Context()

	if _, err := New(srv.URL, "wrong").Describe(ctx); err == nil {
		t.Fatal("a wrong token was let in")
	}
	plugs, err := Load(ctx, []config.PluginConfig{{URL: srv.URL, Token: "tok"}})
	if err != nil || len(plugs) != 1 || plugs[0].Name() != "fake" || !plugs[0].Description.Provider.Available {
		t.Fatalf("load %+v, %v", plugs, err)
	}
	c := plugs[0].Client

	if _, cerr := c.Check(ctx, sdk.Credential{Secret: []byte("bad")}); !errors.Is(cerr, sdk.ErrRejected) {
		t.Fatalf("a bad credential: %v", cerr)
	}
	if a, cerr := c.Check(ctx, sdk.Credential{Secret: []byte("good"), Settings: map[string]string{"region": "eu"}}); cerr != nil || a.Details["region"] != "eu" {
		t.Fatalf("a good credential: %+v, %v", a, cerr)
	}

	// An apply needs its source uploaded first.
	req := sdk.OperationRequest{Kind: sdk.OpApply, Job: sdk.Job{Key: "dep:svc", Name: "api"}, Source: "abc123"}
	if _, serr := c.Start(ctx, req); serr == nil {
		t.Fatal("started without a source")
	}
	if has, _ := c.HasSource(ctx, "abc123"); has {
		t.Fatal("has a source it was never sent")
	}
	if perr := c.PutSource(ctx, "abc123", strings.NewReader("tarball")); perr != nil {
		t.Fatal(perr)
	}
	if has, _ := c.HasSource(ctx, "abc123"); !has {
		t.Fatal("lost the source")
	}

	op, err := c.Start(ctx, req)
	if err != nil || op.Status != sdk.StatusRunning {
		t.Fatalf("start %+v, %v", op, err)
	}
	// The same job again finds the running operation.
	again, err := c.Start(ctx, req)
	if err != nil || again.ID != op.ID || f.applies.Load() != 1 {
		t.Fatalf("restarted a running operation: %+v, %v, %d applies", again, err, f.applies.Load())
	}
	close(f.release)
	done := wait(t, c, op.ID)
	if done.Status != sdk.StatusSucceeded || done.Outcome.Revision != "tarball" || done.Progress != "working" || string(done.Checkpoint) != "step-1" {
		t.Fatalf("done %+v", done)
	}
	if _, err := c.Operation(ctx, "apply-nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an unknown operation: %v", err)
	}

	f.panics = true
	op, _ = c.Start(ctx, sdk.OperationRequest{Kind: sdk.OpApply, Job: sdk.Job{Key: "dep:other", Name: "w"}})
	if done := wait(t, c, op.ID); done.Status != sdk.StatusFailed || !strings.Contains(done.Error, "panicked") {
		t.Fatalf("a panic %+v", done)
	}

	if o, err := c.Observe(ctx, sdk.Target{ExternalID: "api"}); err != nil || !o.Exists {
		t.Fatalf("observe %+v, %v", o, err)
	}
}
