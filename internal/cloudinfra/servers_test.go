package cloudinfra

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/provider"
)

// A refusal beside a direct deployment still maps to 409 through ErrBusy,
// but says what's running rather than "infrastructure operation".
func TestErrDirectApplyRunning(t *testing.T) {
	err := fmt.Errorf("save pool: %w", ErrDirectApplyRunning)
	if !errors.Is(err, ErrBusy) || !errors.Is(err, ErrDirectApplyRunning) {
		t.Fatalf("errors.Is lost the sentinel: %v", err)
	}
	if errors.Is(ErrBusy, ErrDirectApplyRunning) {
		t.Fatal("every busy operation reads as a direct deployment")
	}
	if !strings.HasPrefix(ErrDirectApplyRunning.Error(), "a direct deployment is running on this server") {
		t.Fatalf("message: %q", ErrDirectApplyRunning.Error())
	}
}

type uncreatedServerStore struct {
	gpuServerStore
	operations map[string]*Operation
}

func (s *uncreatedServerStore) CreatePreview(_ context.Context, o *Operation) error {
	copied := *o
	s.operations[o.ID] = &copied
	return nil
}

func (s *uncreatedServerStore) SaveOperation(_ context.Context, o *Operation) error {
	copied := *o
	s.operations[o.ID] = &copied
	return nil
}

type uncreatedCloudReader struct{}

func (uncreatedCloudReader) GetProfile(_ context.Context, org, _, id string) (*cloudprofiles.Profile, error) {
	return &cloudprofiles.Profile{ID: id, OrgID: org, Revision: 1}, nil
}
func (uncreatedCloudReader) ListAllProfiles(context.Context) ([]*cloudprofiles.Profile, error) {
	return nil, nil
}
func (uncreatedCloudReader) EnsureServerConnection(context.Context, string, string, string) error {
	return nil
}
func (uncreatedCloudReader) RemoveServerConnection(context.Context, string, string, string) error {
	return nil
}

type uncreatedAuthorizer struct{}

func (uncreatedAuthorizer) Require(context.Context, security.Principal, security.Permission, string) error {
	return nil
}

type uncreatedServerAdapter struct {
	servers []provider.InfrastructureServer
	deleted []string
}

func (a *uncreatedServerAdapter) ListServers(context.Context, provider.InfrastructureCredential) ([]provider.InfrastructureServer, error) {
	return a.servers, nil
}
func (a *uncreatedServerAdapter) GetServer(_ context.Context, _ provider.InfrastructureCredential, id string) (provider.InfrastructureServer, error) {
	if id == "" {
		return provider.InfrastructureServer{}, provider.ErrInfrastructureInvalid
	}
	for _, s := range a.servers {
		if s.ID == id {
			return s, nil
		}
	}
	return provider.InfrastructureServer{}, provider.ErrInfrastructureNotFound
}
func (a *uncreatedServerAdapter) CreateServer(context.Context, provider.InfrastructureCredential, provider.ServerSpec) (provider.ServerResult, error) {
	return provider.ServerResult{}, nil
}
func (a *uncreatedServerAdapter) ResizeServer(context.Context, provider.InfrastructureCredential, string, string) (provider.InfrastructureAction, error) {
	return provider.InfrastructureAction{}, nil
}
func (a *uncreatedServerAdapter) DeleteServer(_ context.Context, _ provider.InfrastructureCredential, id string) error {
	if id == "" {
		return provider.ErrInfrastructureInvalid
	}
	a.deleted = append(a.deleted, id)
	a.servers = nil
	return nil
}
func (a *uncreatedServerAdapter) GetServerAction(context.Context, provider.InfrastructureCredential, string, string) (provider.InfrastructureAction, error) {
	return provider.InfrastructureAction{ID: "act-del", Kind: "delete", Status: "completed"}, nil
}

func TestDeleteFailedServerWithoutResourceID(t *testing.T) {
	ctx := t.Context()
	p := security.Principal{UserID: "user_1", OrgID: "org"}

	newFixture := func(remote ...provider.InfrastructureServer) (*Service, *uncreatedServerStore, *uncreatedServerAdapter) {
		store := &uncreatedServerStore{
			gpuServerStore: gpuServerStore{servers: map[string]*Server{
				"server_1": {
					ID:                  "server_1",
					CreationOperationID: "infraop_create",
					OrgID:               "org",
					CloudID:             "cloud_1",
					Name:                "Digital Ocean Droplet 1 2VPC 4GB",
					Ownership:           "managed",
					Provider:            "digitalocean",
					ConnectionID:        "conn_1",
					AccountID:           "team-1",
					Region:              "ric1",
					Size:                "s-4vcpu-8gb-intel",
					Revision:            2,
					Status:              "failed",
					Draining:            true,
				},
			}},
			operations: map[string]*Operation{},
		}
		conns := &gpuConnectionStore{rows: map[string]*connections.Connection{
			"conn_1": {
				ID:        "conn_1",
				OrgID:     "org",
				Cloud:     "digitalocean",
				Status:    connections.StatusReady,
				Details:   map[string]string{"team_uuid": "team-1"},
				UpdatedAt: time.Now().UTC(),
			},
		}}
		adapter := &uncreatedServerAdapter{servers: remote}
		reg := NewAdapters()
		if err := reg.RegisterServer("digitalocean", adapter); err != nil {
			t.Fatal(err)
		}
		svc := NewService(Deps{
			Store:              store,
			Clouds:             uncreatedCloudReader{},
			Connections:        conns,
			RuntimeConnections: conns,
			Secrets:            bootstrapSecrets{},
			Authz:              uncreatedAuthorizer{},
			Adapters:           reg,
		})
		return svc, store, adapter
	}

	t.Run("no provider droplet created", func(t *testing.T) {
		svc, store, adapter := newFixture()

		if _, err := svc.PreviewServer(ctx, p, "cloud_1", ServerInput{
			Kind:           "resize",
			CloudRevision:  1,
			ServerID:       "server_1",
			ServerRevision: 2,
			Size:           "s-8vcpu-16gb",
		}); !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "Delete it and create it again") {
			t.Fatalf("resize without resource_id: got %v, want ErrConflict saying to delete and recreate", err)
		}

		preview, err := svc.PreviewServer(ctx, p, "cloud_1", ServerInput{
			Kind:           "delete",
			CloudRevision:  1,
			ServerID:       "server_1",
			ServerRevision: 2,
			ConnectionID:   "conn_1",
			Name:           "Digital Ocean Droplet 1 2VPC 4GB",
			Region:         "ric1",
			Size:           "s-4vcpu-8gb-intel",
		})
		if err != nil {
			t.Fatalf("PreviewServer delete: %v", err)
		}
		if preview.ResourceID != "" {
			t.Fatalf("expected empty resource_id on preview, got %q", preview.ResourceID)
		}

		v, err := store.GetServer(ctx, "org", "cloud_1", "server_1")
		if err != nil {
			t.Fatal(err)
		}
		if err := svc.deleteServer(ctx, preview, v, adapter, provider.InfrastructureCredential{Provider: "digitalocean", AccountID: "team-1"}); err != nil {
			t.Fatalf("deleteServer: %v", err)
		}
		updated, err := store.GetServer(ctx, "org", "cloud_1", "server_1")
		if err != nil {
			t.Fatal(err)
		}
		if updated.Status != "deleted" || updated.Revision != 3 {
			t.Fatalf("expected deleted server at revision 3, got %+v", updated)
		}
		if len(adapter.deleted) != 0 {
			t.Fatalf("expected no provider DeleteServer call, got %v", adapter.deleted)
		}
	})

	t.Run("tagged provider droplet exists", func(t *testing.T) {
		svc, store, adapter := newFixture(provider.InfrastructureServer{
			ID:        "998877",
			AccountID: "team-1",
			Name:      "Digital-Ocean-Droplet-1-2VPC-4GB",
			Region:    "ric1",
			Size:      "s-4vcpu-8gb-intel",
			Status:    "active",
			Tags:      []string{"loomplane", provider.ServerOperationTag("infraop_create")},
		})

		preview, err := svc.PreviewServer(ctx, p, "cloud_1", ServerInput{
			Kind:           "delete",
			CloudRevision:  1,
			ServerID:       "server_1",
			ServerRevision: 2,
		})
		if err != nil {
			t.Fatalf("PreviewServer delete: %v", err)
		}
		if preview.ResourceID != "998877" {
			t.Fatalf("expected preview to discover tagged droplet 998877, got %q", preview.ResourceID)
		}

		v, err := store.GetServer(ctx, "org", "cloud_1", "server_1")
		if err != nil {
			t.Fatal(err)
		}
		// Even if preview ran before the droplet appeared (ResourceID == ""), deleteServer reconciles by tag, issues DELETE, and confirms absence on the next pass.
		preview.ResourceID = ""
		cred := provider.InfrastructureCredential{Provider: "digitalocean", AccountID: "team-1"}
		if err := svc.deleteServer(ctx, preview, v, adapter, cred); !errors.Is(err, ErrPending) {
			t.Fatalf("first deleteServer pass: got %v, want ErrPending", err)
		}
		if err := svc.deleteServer(ctx, preview, v, adapter, cred); err != nil {
			t.Fatalf("second deleteServer pass: %v", err)
		}
		updated, err := store.GetServer(ctx, "org", "cloud_1", "server_1")
		if err != nil {
			t.Fatal(err)
		}
		if updated.Status != "deleted" || updated.ResourceID != "998877" {
			t.Fatalf("expected deleted server with recorded ResourceID 998877, got %+v", updated)
		}
		if preview.AbsenceConfirmedAt.IsZero() {
			t.Fatal("expected AbsenceConfirmedAt to be recorded once tagged droplet absence is confirmed")
		}
		if len(adapter.deleted) != 1 || adapter.deleted[0] != "998877" {
			t.Fatalf("expected provider DeleteServer(998877), got %v", adapter.deleted)
		}
	})
}
