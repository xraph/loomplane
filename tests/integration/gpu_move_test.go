//go:build integration

package integration

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/manifest"
	"github.com/xraph/loomplane/pkg/provider"
)

// An accepted move whose new size asks for more GPUs reserves the new
// count on the destination. The move review covered the old one-GPU pin;
// planning re-previews the destination with the two GPUs the container
// will be labelled with, and approval holds both.
func TestGPUAcceptedMoveReservesTheNewGPUCount(t *testing.T) {
	f := newGPUPoolFixture(t)
	ctx := t.Context()
	now := time.Now().UTC()
	if err := connections.NewPgStore(f.db).CreateConnection(ctx, &connections.Connection{ID: "runtime-gpu-2", OrgID: "org", Cloud: "server", Name: "runtime-gpu-2", Account: "runtime-gpu-2", Details: map[string]string{"docker_id": "runtime-gpu-2", "architecture": "amd64"}, Status: "ready", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	second := &cloudinfra.Server{ID: "server-gpu-2", OrgID: "org", CloudID: "cloud", ConnectionID: "runtime-gpu-2", RuntimeConnectionID: "runtime-gpu-2", Ownership: "adopted", Provider: "generic", AccountID: "runtime-gpu-2", Status: "ready", Revision: 1, CreatedAt: now, UpdatedAt: now, Health: cloudinfra.RuntimeHealth{DockerID: "runtime-gpu-2", Architecture: "amd64", CPUs: 16, MemoryBytes: 64 << 30, EffectiveCPUMillis: 16000, EffectiveMemoryBytes: 64 << 30, GPURuntime: provider.GPUStatusReady, GPUs: []provider.GPUDevice{{Index: 0, UUID: "GPU-2-0", Kind: "nvidia-l4"}, {Index: 1, UUID: "GPU-2-1", Kind: "nvidia-l4"}}, CheckedAt: now}}
	if err := f.store.SaveServer(ctx, second); err != nil {
		t.Fatal(err)
	}
	pool, err := f.store.GetPool(ctx, "org", "cloud", "gpu-pool")
	if err != nil {
		t.Fatal(err)
	}
	pool.Revision = 2
	pool.Members = append(pool.Members, cloudinfra.PoolMember{ServerID: "server-gpu-2", CPUMillis: 8000, MemoryBytes: 32 << 30, GPUs: 2})
	if err = f.store.SavePool(ctx, pool, 1); err != nil {
		t.Fatal(err)
	}

	// api runs on server-gpu with one L4.
	r := gpuRequest(f, "api")
	p, err := f.store.PreviewPlacement(ctx, r)
	if err != nil || p.GPUs != 1 {
		t.Fatalf("original placement: %+v %v", p, err)
	}
	pin := gpuPlan(t, f, "original", r, p)
	f.accept(t, "original")
	pin.TargetID, pin.TargetName = "gpu-target", "gpu"
	cfg := planning.ServiceConfig{Provider: "server", Target: p.ConnectionID, TargetSettings: map[string]string{"docker_id": p.ConnectionID, "architecture": "amd64"}, CloudPlacement: pin, Size: &manifest.Size{CPU: 1, Memory: 512, GPU: &manifest.GPU{Kind: "nvidia-l4", Count: 1}}, GPU: &provider.GPUPlacement{GPURequest: provider.GPURequest{Kind: "nvidia-l4", Count: 1}}}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	old := planning.Binding{ID: "live-binding", OrgID: "org", ProjectID: "proj", EnvironmentID: "env", Subject: planning.ServiceSubject, Name: "api", Provider: "server", ConnectionID: p.ConnectionID, Config: raw, PlanID: "original", ExternalID: "original-runtime", Revision: "old-image", CreatedAt: now, UpdatedAt: now}
	if err = f.plans.PutBinding(ctx, &old); err != nil {
		t.Fatal(err)
	}

	// The operator drains server-gpu and accepts a move to server-gpu-2.
	svc := cloudinfra.NewService(cloudinfra.Deps{Store: f.store, Clouds: f.clouds, Authz: &syncAuth{}})
	actor := security.Principal{OrgID: "org", UserID: "manager"}
	source := p.ServerID
	destinationID := "server-gpu-2"
	if source == destinationID {
		t.Fatalf("fixture placed api on %s", source)
	}
	drain, err := svc.PreviewDrain(ctx, actor, "cloud", source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.ApplyDrain(ctx, actor, "cloud", source, cloudinfra.ApplyInput{Review: drain.Review}); err != nil {
		t.Fatal(err)
	}
	in := cloudinfra.MoveInput{ProjectID: "proj", EnvironmentID: "env", Service: "api", PoolID: "gpu-pool", DestinationServerID: destinationID}
	review, err := svc.PreviewMove(ctx, actor, "cloud", in)
	if err != nil || review.Placement.GPUs != 1 {
		t.Fatalf("move review: %+v %v", review, err)
	}
	in.Review = review.Review
	if _, err = svc.AcceptMove(ctx, actor, "cloud", in); err != nil {
		t.Fatal(err)
	}

	// The next plan asks for two L4s. Planning's first preview finds the
	// accepted destination; the re-preview there keeps the new GPU count.
	next := gpuRequest(f, "api")
	next.PlanID, next.StickyServerID, next.GPUs = "move", source, 2
	found, err := f.store.PreviewPlacement(ctx, next)
	if err != nil || found.ServerID != destinationID {
		t.Fatalf("accepted destination: %+v %v", found, err)
	}
	next.DestinationServerID = found.ServerID
	moved, err := f.store.PreviewPlacement(ctx, next)
	if err != nil || moved.ServerID != destinationID || moved.GPUKind != "nvidia-l4" || moved.GPUs != 2 {
		t.Fatalf("destination with the new GPU count: %+v %v", moved, err)
	}
	gpuPlan(t, f, "move", next, moved)
	if _, err = f.sql.Exec(ctx, `UPDATE loomplane_plans SET result=jsonb_set(result,'{actions,0,service,gpu}','{"kind":"nvidia-l4","count":2}'::jsonb) WHERE id='move'`); err != nil {
		t.Fatal(err)
	}
	f.accept(t, "move")
	var host string
	var gpus int64
	if err = f.sql.QueryRow(ctx, `SELECT server_id,gpus FROM loomplane_capacity_reservations WHERE plan_id='move' AND service='api'`).Scan(&host, &gpus); err != nil || host != destinationID || gpus != 2 {
		t.Fatalf("move reservation: %s %d %v", host, gpus, err)
	}
}
