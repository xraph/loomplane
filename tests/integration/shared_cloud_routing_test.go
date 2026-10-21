//go:build integration

package integration

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/planning"
)

func TestSharedCloudRoutingOverlapCapacity(t *testing.T) {
	f := newPoolFixture(t)
	ctx := t.Context()
	request := f.request("env", "api")
	request.StickyServerID = "server-1"
	source, err := f.store.PreviewPlacement(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	sourcePin := f.plan(t, "route-source", request, source)
	f.accept(t, "route-source")
	sourceConfig, _ := json.Marshal(planning.ServiceConfig{Provider: "server", Target: source.ConnectionID, CloudPlacement: sourcePin})
	sourceBinding := &planning.Binding{ID: "route-binding", OrgID: "org", ProjectID: "proj", EnvironmentID: "env", Subject: planning.ServiceSubject, Name: "api", Provider: "server", ConnectionID: source.ConnectionID, ExternalID: "source", Config: sourceConfig, PlanID: "route-source", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err = f.plans.PutBinding(ctx, sourceBinding); err != nil {
		t.Fatal(err)
	}
	request.Allocation = "route-candidate"
	candidate, err := f.store.PreviewPlacement(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	candidatePin := f.plan(t, "route-candidate", request, candidate)
	f.accept(t, "route-candidate")
	assertCapacity := func(want int64) {
		t.Helper()
		var cpu int64
		if err = f.sql.QueryRow(ctx, `SELECT cpu FROM loomplane_server_capacity('server-1')`).Scan(&cpu); err != nil || cpu != want {
			t.Fatalf("capacity=%d want=%d error=%v", cpu, want, err)
		}
	}
	assertCapacity(2000)
	other := f.request("second-env", "worker")
	other.StickyServerID = "server-1"
	if _, err = f.store.PreviewPlacement(ctx, other); err == nil {
		t.Fatal("another project entered retained overlap capacity")
	}
	candidateConfig, _ := json.Marshal(planning.ServiceConfig{Provider: "server", Target: candidate.ConnectionID, CloudPlacement: candidatePin})
	current := *sourceBinding
	current.PlanID, current.ExternalID, current.Config = "route-candidate", "candidate", candidateConfig
	current.Replaced = []planning.Binding{*sourceBinding}
	if err = f.plans.PutBinding(ctx, &current); err != nil {
		t.Fatal(err)
	}
	assertCapacity(2000)
	if err = f.store.ValidatePlacement(ctx, "org", "proj", "env", "route-candidate", "api", *candidate); err != nil {
		t.Fatal(err)
	}
	if err = f.plans.SetReplaced(ctx, "org", current.ID, nil); err != nil {
		t.Fatal(err)
	}
	assertCapacity(1000)
	if _, err = f.store.PreviewPlacement(ctx, other); err != nil {
		t.Fatal(err)
	}
}
