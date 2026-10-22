package server

import (
	"net/http"
	"testing"
)

func TestPoolMutationRequiresPhysicalFence(t *testing.T) {
	client := &Client{RequireFence: true}
	if response, err := client.Request(t.Context(), http.MethodPost, "/containers/create", nil); err == nil {
		response.Body.Close()
		t.Fatal("unfenced pooled mutation accepted")
	}
	ctx := WithEffectFence(t.Context(), EffectFence{Operation: "org/plan/service", Generation: 1})
	if response, err := client.Request(ctx, http.MethodPost, "/containers/create", nil); err == nil {
		response.Body.Close()
		t.Fatal("runtime without SSH helper accepted mutation")
	}
	if err := client.ArmEffectFence(ctx, EffectFence{Operation: "org/plan/service", Generation: 1}); err == nil {
		t.Fatal("runtime without verified identity advanced generation")
	}
}
