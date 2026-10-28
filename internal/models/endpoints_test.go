package models

import (
	"context"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/catalogstate"
	"github.com/xraph/loomplane/internal/gpus"
	"github.com/xraph/loomplane/internal/models/registry"
)

func TestProviderName(t *testing.T) {
	if got := ProviderName("llama-8b"); got != "model-llama-8b" {
		t.Errorf("short name: %q", got)
	}
	exact := strings.Repeat("a", 57) // "model-" and 57 is 63
	if got := ProviderName(exact); got != "model-"+exact {
		t.Errorf("63 characters: %q", got)
	}
	long1, long2 := strings.Repeat("b", 70)+"-one", strings.Repeat("b", 70)+"-two"
	a, b := ProviderName(long1), ProviderName(long2)
	if len(a) != 63 || len(b) != 63 || a == b {
		t.Errorf("long names: %q (%d), %q (%d)", a, len(a), b, len(b))
	}
	if !strings.HasPrefix(a, "model-bbbb") || a[56] != '-' || strings.Trim(a[57:], "0123456789abcdef") != "" {
		t.Errorf("long name %q doesn't end with -<6 hex>", a)
	}
	if ProviderName(long1) != a {
		t.Error("the same resource got two names")
	}
}

// endpointsSetup has Llama 8B running in staging, and Qwen 7B and
// bge-small running on gpu-box in production, with bge-small's binding
// asking for a longer context than it serves.
func endpointsSetup(t *testing.T) (*deploySetup, map[string]*Deployment) {
	t.Helper()
	ctx := context.Background()
	d := newDeploy(t, deployRegistry(t, llamaRev, ""), aiAdmin, []gpus.GPUTarget{l40sServer()}, boxes()...)
	rows := map[string]*Deployment{"llama": deployLlama(t, d, stagingID)}
	for name, in := range map[string]DeployInput{
		"qwen": {EntryID: qwen7B, Target: onGPU("nvidia-l40s")},
		"bge":  {EntryID: bgeSmall, Target: TargetChoice{Source: TargetConnection, ConnectionID: gpuConn}},
	} {
		res, err := d.svc.Deploy(ctx, member, projectID, productID, in)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		r := current(t, d).Resources[res.Deployment.Resource]
		settings := maps.Clone(r.Settings)
		gpu := 0
		if name == "bge" {
			settings["max_model_len"] = "32768"
		} else {
			gpu = 1
		}
		d.facts.running(productID, res.Deployment.Resource, r.Kind, settings, gpu)
		rows[name] = res.Deployment
	}
	return d, rows
}

func TestEndpointsFromAPlacement(t *testing.T) {
	ctx := context.Background()
	d, rows := endpointsSetup(t)
	// A model added to the manifest by hand has no row, and still counts.
	qwen := current(t, d).Resources[rows["qwen"].Resource]
	d.facts.running(productID, "handmade", qwen.Kind, qwen.Settings, 1)

	from := Placement{EnvironmentID: productID, ConnectionID: gpuConn, Provider: "server", SharedNetworks: []string{"ai-models"}}
	got, skipped, err := d.svc.Endpoints(ctx, "org", projectID, from)
	if err != nil {
		t.Fatal(err)
	}
	qr, br := rows["qwen"].Resource, rows["bge"].Resource
	want := []Endpoint{
		{Deployment: rows["bge"].ID, Resource: br, EnvironmentID: productID, Provider: ProviderName(br),
			BaseURL: "http://" + br + "--guard:8080/v1", APIKey: "lpm_" + br + "_key", ServedName: bgeSmall,
			Task: registry.TaskEmbedding, ContextWindow: 512, MaxOutput: 256, Dimensions: 384, Alias: "bge-small-en-v1.5"},
		{Resource: "handmade", EnvironmentID: productID, Provider: "model-handmade",
			BaseURL: "http://handmade--guard:8080/v1", APIKey: "lpm_handmade_key", ServedName: qwen7B,
			Task: registry.TaskChat, ContextWindow: 32768, MaxOutput: 8192, Alias: "qwen2.5-7b-instruct"},
		{Deployment: rows["qwen"].ID, Resource: qr, EnvironmentID: productID, Provider: ProviderName(qr),
			BaseURL: "http://" + qr + "--guard:8080/v1", APIKey: "lpm_" + qr + "_key", ServedName: qwen7B,
			Task: registry.TaskChat, ContextWindow: 32768, MaxOutput: 8192, Alias: "qwen2.5-7b-instruct"},
	}
	if len(got) != len(want) {
		t.Fatalf("endpoints %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("endpoint %d:\n got %+v\nwant %+v", i, got[i], want[i])
		}
	}
	// Staging's model runs in staging only.
	if len(skipped) != 1 || skipped[0] != (Skipped{Resource: llamaResource, EnvironmentID: stagingID, Reason: "runs in staging"}) {
		t.Errorf("skipped %+v", skipped)
	}

	// A model that isn't ready yet is skipped with its state.
	d.facts.set(productID, qr, catalogstate.State{Value: catalogstate.Deploying, ConnectionID: gpuConn})
	got, skipped, err = d.svc.Endpoints(ctx, "org", projectID, from)
	if err != nil || len(got) != 2 || !slices.Contains(skipped, Skipped{Resource: qr, EnvironmentID: productID, Reason: "isn't ready: it's deploying"}) {
		t.Errorf("deploying: %+v %+v, %v", got, skipped, err)
	}
	// Another organization sees none of it.
	if got, skipped, err = d.svc.Endpoints(ctx, "org_other", projectID, from); err != nil || len(got) != 0 || len(skipped) != 0 {
		t.Errorf("another org: %+v %+v, %v", got, skipped, err)
	}
}

func TestEndpointsSkipOtherConnections(t *testing.T) {
	d, rows := endpointsSetup(t)
	got, skipped, err := d.svc.Endpoints(context.Background(), "org", projectID, Placement{EnvironmentID: productID, ConnectionID: cpuConn, Provider: "server"})
	if err != nil || len(got) != 0 {
		t.Fatalf("endpoints %+v, %v", got, err)
	}
	reason := "runs on " + gpuConn + ", and a caller on " + cpuConn + " can't reach it. Run them on the same server or cluster."
	for _, name := range []string{"bge", "qwen"} {
		want := Skipped{Resource: rows[name].Resource, EnvironmentID: productID, Reason: reason}
		if !slices.Contains(skipped, want) {
			t.Errorf("%s not skipped: %+v", name, skipped)
		}
	}
}
