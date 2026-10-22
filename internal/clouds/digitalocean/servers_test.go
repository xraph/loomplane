package digitalocean

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/xraph/loomplane/pkg/provider"
)

func infrastructureCredential() provider.InfrastructureCredential {
	return provider.InfrastructureCredential{Provider: "digitalocean", AccountID: "team-1", TeamID: "team-1", Token: "dop_v1_good"}
}
func infrastructureAccount(f *fake) {
	f.on("GET /v2/account", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		reply(w, 200, map[string]any{"account": map[string]any{"uuid": "account", "team": map[string]any{"uuid": "team-1", "name": "renamed"}}})
	})
}
func TestInfrastructureServerPaginationAndIdentity(t *testing.T) {
	f, c := newFake(t)
	infrastructureAccount(f)
	f.on("GET /v2/droplets", func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		if r.URL.Query().Get("per_page") != "200" {
			t.Error("missing page bound")
		}
		n, _ := strconv.Atoi(r.URL.Query().Get("page"))
		out := map[string]any{"droplets": []any{map[string]any{"id": n, "name": "renamed", "size_slug": "small", "region": map[string]any{"slug": "nyc3"}, "networks": map[string]any{"v4": []any{map[string]any{"ip_address": "192.0.2.1", "type": "public"}, map[string]any{"ip_address": "10.0.0.1", "type": "private"}}}}}}
		if n == 1 {
			out["links"] = map[string]any{"pages": map[string]any{"next": "https://evil.invalid/steal"}}
		}
		reply(w, 200, out)
	})
	servers, err := c.ListServers(t.Context(), infrastructureCredential())
	if err != nil || len(servers) != 2 || servers[1].ID != "2" || servers[1].AccountID != "team-1" || len(servers[0].PublicIPv4) != 1 {
		t.Fatalf("servers: %+v %v", servers, err)
	}
	credential := infrastructureCredential()
	credential.AccountID = "team-other"
	if _, err = c.ListServers(t.Context(), credential); !errors.Is(err, provider.ErrInfrastructureIdentity) {
		t.Fatalf("identity: %v", err)
	}
}
func TestInfrastructureServerCreateReconcilesLostResponse(t *testing.T) {
	f, c := newFake(t)
	infrastructureAccount(f)
	var created map[string]any
	creates := 0
	f.on("GET /v2/droplets", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		list := []any{}
		if created != nil {
			list = append(list, map[string]any{"id": 42, "name": created["name"], "size_slug": created["size"], "region": map[string]any{"slug": created["region"]}, "tags": created["tags"]})
		}
		reply(w, 200, map[string]any{"droplets": list})
	})
	f.on("GET /v2/droplets/42/actions", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		reply(w, 200, map[string]any{"actions": []any{map[string]any{"id": 7, "resource_id": 42, "status": "completed", "type": "create"}}})
	})
	f.on("POST /v2/droplets", func(w http.ResponseWriter, _ *http.Request, body map[string]any) {
		created = body
		creates++
		reply(w, 502, map[string]any{"message": "dop_v1_good bootstrap-secret"})
	})
	spec := provider.ServerSpec{OperationID: "intent-1", Name: "host", Region: "nyc3", Size: "small", Image: "ubuntu-24-04-x64", UserData: "bootstrap-secret"}
	_, err := c.CreateServer(t.Context(), infrastructureCredential(), spec)
	if !errors.Is(err, provider.ErrInfrastructureAmbiguous) || strings.Contains(err.Error(), "bootstrap-secret") || strings.Contains(err.Error(), "dop_v1_good") {
		t.Fatalf("unsafe ambiguous response: %v", err)
	}
	spec.ReconcileOnly = true
	result, err := c.CreateServer(t.Context(), infrastructureCredential(), spec)
	if err != nil || !result.Reconciled || result.Server.ID != "42" || creates != 1 {
		t.Fatalf("reconcile: %+v %v, creates %d", result, err, creates)
	}
	spec.Size = "changed"
	if _, err = c.CreateServer(t.Context(), infrastructureCredential(), spec); !errors.Is(err, provider.ErrInfrastructureDrift) {
		t.Fatalf("changed intent: %v", err)
	}
	spec.Size = "small"
	spec.OperationID = "not-visible"
	created = nil
	if _, err = c.CreateServer(t.Context(), infrastructureCredential(), spec); !errors.Is(err, provider.ErrInfrastructureAmbiguous) || creates != 1 {
		t.Fatalf("blind retry: %v", err)
	}
}
func TestInfrastructureServerCancellationScopeAndErrors(t *testing.T) {
	f, c := newFake(t)
	infrastructureAccount(f)
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := c.ListServers(canceled, infrastructureCredential()); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	for _, status := range []int{403, 429, 500} {
		f.on("GET /v2/droplets", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			reply(w, status, map[string]any{"message": "dop_v1_good"})
		})
		_, err := c.ListServers(t.Context(), infrastructureCredential())
		if err == nil || strings.Contains(err.Error(), "dop_v1_good") {
			t.Fatalf("HTTP %d: %v", status, err)
		}
		if status == 403 {
			var scope *provider.InfrastructureScopeError
			if !errors.As(err, &scope) || scope.Permission != "droplet:read" {
				t.Fatalf("scope: %v", err)
			}
		}
	}
}
func TestInfrastructureServerActionsRemainPending(t *testing.T) {
	f, c := newFake(t)
	infrastructureAccount(f)
	f.on("GET /v2/droplets/42", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		reply(w, 200, map[string]any{"droplet": map[string]any{"id": 42}})
	})
	f.on("POST /v2/droplets/42/actions", func(w http.ResponseWriter, _ *http.Request, body map[string]any) {
		if body["disk"] != false || body["type"] != "resize" {
			t.Errorf("resize: %v", body)
		}
		reply(w, 201, map[string]any{"action": map[string]any{"id": 7, "resource_id": 42, "status": "in-progress", "type": "resize"}})
	})
	f.on("GET /v2/droplets/42/actions/7", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		reply(w, 200, map[string]any{"action": map[string]any{"id": 7, "resource_id": 42, "status": "completed", "type": "resize"}})
	})
	action, err := c.ResizeServer(t.Context(), infrastructureCredential(), "42", "bigger")
	if err != nil || action.Status != "in-progress" || action.ID != "7" {
		t.Fatalf("resize: %+v %v", action, err)
	}
	action, err = c.GetServerAction(t.Context(), infrastructureCredential(), "42", "7")
	if err != nil || action.Status != "completed" {
		t.Fatalf("poll: %+v %v", action, err)
	}
}
func TestInfrastructureServerPaginationBound(t *testing.T) {
	f, c := newFake(t)
	infrastructureAccount(f)
	calls := 0
	f.on("GET /v2/droplets", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		calls++
		reply(w, 200, map[string]any{"droplets": []any{}, "links": map[string]any{"pages": map[string]any{"next": "always"}}})
	})
	if _, err := c.ListServers(t.Context(), infrastructureCredential()); !errors.Is(err, provider.ErrInfrastructurePagination) || calls != infrastructureMaxPages {
		t.Fatalf("pagination calls %d: %v", calls, err)
	}
}

func TestInfrastructureCreateMissingWriteScopeDoesNotClaimReadProvesWrite(t *testing.T) {
	f, c := newFake(t)
	infrastructureAccount(f)
	f.on("GET /v2/droplets", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		reply(w, 200, map[string]any{"droplets": []any{}})
	})
	f.on("POST /v2/droplets", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		reply(w, 403, map[string]any{"message": "token missing create scope"})
	})
	spec := provider.ServerSpec{OperationID: "intent", Name: "host", Region: "nyc3", Size: "small", Image: "ubuntu-24-04-x64"}
	_, err := c.CreateServer(t.Context(), infrastructureCredential(), spec)
	var scope *provider.InfrastructureScopeError
	if !errors.As(err, &scope) || !strings.Contains(scope.Permission, "droplet:create") || errors.Is(err, provider.ErrInfrastructureAmbiguous) {
		t.Fatalf("scope failure: %v", err)
	}
}

func TestInfrastructureRequestCancellationReachesHTTPClient(t *testing.T) {
	f, c := newFake(t)
	infrastructureAccount(f)
	started := make(chan struct{})
	f.on("GET /v2/droplets", func(_ http.ResponseWriter, r *http.Request, _ map[string]any) { close(started); <-r.Context().Done() })
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	result := make(chan error, 1)
	go func() { _, err := c.ListServers(ctx, infrastructureCredential()); result <- err }()
	<-started
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("in-flight cancellation: %v", err)
	}
}
