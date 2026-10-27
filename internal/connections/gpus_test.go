package connections

import (
	"context"
	"errors"
	"testing"

	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/provider"
)

type cloudConnections struct {
	Store
	cloud string
}

func (s cloudConnections) GetConnection(_ context.Context, org, key string) (*Connection, error) {
	if org != "org" || key != "connection" {
		return nil, ErrNotFound
	}
	return &Connection{ID: key, OrgID: org, Cloud: s.cloud, Status: StatusReady}, nil
}

func TestGPUInventoryNeedsCloudAccountRead(t *testing.T) {
	secrets := &inventorySecrets{}
	called := false
	service := &Service{
		store: cloudConnections{cloud: Server}, secrets: secrets,
		authz: inventoryAuthorization{denied: security.CloudAccountRead},
		GPUReader: func(context.Context, *Connection, []byte) (provider.GPUInventory, error) {
			called = true
			return provider.GPUInventory{}, nil
		},
	}
	_, err := service.GPUInventory(t.Context(), security.Principal{OrgID: "org"}, "connection")
	if !errors.Is(err, security.ErrForbidden) || secrets.reads != 0 || called {
		t.Fatalf("unauthorized read: err=%v reads=%d called=%v", err, secrets.reads, called)
	}
}

func TestGPUInventoryRefusesOtherClouds(t *testing.T) {
	secrets := &inventorySecrets{}
	service := &Service{store: cloudConnections{cloud: Vercel}, secrets: secrets, authz: inventoryAuthorization{}}
	_, err := service.GPUInventory(t.Context(), security.Principal{OrgID: "org"}, "connection")
	if !errors.Is(err, ErrInvalid) || secrets.reads != 0 {
		t.Fatalf("vercel connection: err=%v reads=%d", err, secrets.reads)
	}
}

func TestGPUInventoryReadsAServerLive(t *testing.T) {
	want := provider.GPUInventory{Status: provider.GPUStatusReady, Devices: []provider.GPUDevice{{UUID: "GPU-a"}}}
	service := &Service{
		store: cloudConnections{cloud: Server}, secrets: &inventorySecrets{}, authz: inventoryAuthorization{},
		GPUReader: func(_ context.Context, c *Connection, credential []byte) (provider.GPUInventory, error) {
			if c.Cloud != Server || string(credential) != "private" {
				t.Fatalf("reader got %s %q", c.Cloud, credential)
			}
			return want, nil
		},
	}
	got, err := service.GPUInventory(t.Context(), security.Principal{OrgID: "org"}, "connection")
	if err != nil || got == nil || got.Status != want.Status || len(got.Devices) != 1 {
		t.Fatalf("inventory: %+v %v", got, err)
	}
}

func TestGPUInventoryHidesReaderErrors(t *testing.T) {
	service := &Service{
		store: cloudConnections{cloud: Server}, secrets: &inventorySecrets{}, authz: inventoryAuthorization{},
		GPUReader: func(context.Context, *Connection, []byte) (provider.GPUInventory, error) {
			return provider.GPUInventory{}, errors.New("ssh: secret detail")
		},
	}
	_, err := service.GPUInventory(t.Context(), security.Principal{OrgID: "org"}, "connection")
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err=%v", err)
	}
}
