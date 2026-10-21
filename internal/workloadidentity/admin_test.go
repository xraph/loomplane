package workloadidentity

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	bundlev1 "github.com/spiffe/spire-api-sdk/proto/spire/api/server/bundle/v1"
	entryv1 "github.com/spiffe/spire-api-sdk/proto/spire/api/server/entry/v1"
	spiretypes "github.com/spiffe/spire-api-sdk/proto/spire/api/types"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

type adminFixture struct {
	entryv1.UnimplementedEntryServer
	bundlev1.UnimplementedBundleServer
	mu               sync.Mutex
	domain           string
	denied           bool
	entries          map[string]*spiretypes.Entry
	created, deleted int
}

func (f *adminFixture) GetBundle(context.Context, *bundlev1.GetBundleRequest) (*spiretypes.Bundle, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return &spiretypes.Bundle{TrustDomain: f.domain, X509Authorities: []*spiretypes.X509Certificate{{Asn1: []byte{1}}}}, nil
}
func (f *adminFixture) CountEntries(_ context.Context, r *entryv1.CountEntriesRequest) (*entryv1.CountEntriesResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.denied {
		return nil, status.Error(codes.PermissionDenied, "administration denied")
	}
	if r.GetFilter().GetBySpiffeId().GetPath() != "/loomplane/control/org/org" {
		return nil, status.Error(codes.InvalidArgument, "unscoped probe")
	}
	return &entryv1.CountEntriesResponse{}, nil
}
func (f *adminFixture) GetEntry(_ context.Context, r *entryv1.GetEntryRequest) (*spiretypes.Entry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	e, ok := f.entries[r.Id]
	if !ok {
		return nil, status.Error(codes.NotFound, "missing")
	}
	return proto.Clone(e).(*spiretypes.Entry), nil
}
func (f *adminFixture) BatchCreateEntry(_ context.Context, r *entryv1.BatchCreateEntryRequest) (*entryv1.BatchCreateEntryResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.created++
	e := proto.Clone(r.Entries[0]).(*spiretypes.Entry)
	f.entries[e.Id] = e
	return &entryv1.BatchCreateEntryResponse{Results: []*entryv1.BatchCreateEntryResponse_Result{{Status: &spiretypes.Status{}, Entry: e}}}, nil
}
func (f *adminFixture) BatchDeleteEntry(_ context.Context, r *entryv1.BatchDeleteEntryRequest) (*entryv1.BatchDeleteEntryResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted++
	delete(f.entries, r.Ids[0])
	return &entryv1.BatchDeleteEntryResponse{Results: []*entryv1.BatchDeleteEntryResponse_Result{{Status: &spiretypes.Status{}, Id: r.Ids[0]}}}, nil
}
func serveAdminFixture(t *testing.T, f *adminFixture) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "lp-admin-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if removeErr := os.RemoveAll(dir); removeErr != nil {
			t.Error(removeErr)
		}
	})
	socket := filepath.Join(dir, "admin.sock")
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	entryv1.RegisterEntryServer(server, f)
	bundlev1.RegisterBundleServer(server, f)
	done := make(chan struct{})
	go func() { defer close(done); _ = server.Serve(listener) }()
	t.Cleanup(func() { server.Stop(); <-done })
	return "unix://" + socket
}
func TestAdminScopesRegistrationAndDeletion(t *testing.T) {
	f := &adminFixture{domain: "loomplane.test", entries: map[string]*spiretypes.Entry{}}
	a, err := OpenAdmin(t.Context(), serveAdminFixture(t, f), "loomplane.test", "org")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := a.Close(); err != nil {
			t.Error(err)
		}
	})
	b := bindingFixture(t)
	if _, err := a.Register(t.Context(), b); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*spiretypes.Entry){
		"foreign domain": func(e *spiretypes.Entry) { e.SpiffeId.TrustDomain = "foreign.test" },
		"foreign organization": func(e *spiretypes.Entry) {
			e.SpiffeId.Path = "/loomplane/org/foreign/project/project/environment/env/destination/destination/agent/agent/epoch/1"
		},
		"control":        func(e *spiretypes.Entry) { e.SpiffeId.Path = "/loomplane/control/org/org" },
		"admin":          func(e *spiretypes.Entry) { e.Admin = true },
		"downstream":     func(e *spiretypes.Entry) { e.Downstream = true },
		"node":           func(e *spiretypes.Entry) { e.ParentId.Path = "/spire/server" },
		"missing parent": func(e *spiretypes.Entry) { e.ParentId = nil },
		"foreign parent": func(e *spiretypes.Entry) { e.ParentId.TrustDomain = "foreign.test" },
		"wrong id":       func(e *spiretypes.Entry) { e.Id = "11234567-89ab-cdef-0123-456789abcdef" },
	} {
		t.Run(name, func(t *testing.T) {
			f.mu.Lock()
			original := proto.Clone(f.entries[b.EntryID]).(*spiretypes.Entry)
			change(f.entries[b.EntryID])
			f.mu.Unlock()
			if err := a.Revoke(t.Context(), b.EntryID); err == nil {
				t.Error("unrelated entry deleted")
			}
			f.mu.Lock()
			f.entries[b.EntryID] = original
			if f.deleted != 0 {
				t.Error("delete RPC invoked")
			}
			f.mu.Unlock()
		})
	}
	foreign := b
	foreign.Agent.OrgID = "foreign"
	foreign.Destination.OrgID = "foreign"
	foreign.Agent.URI, _ = AgentURI("loomplane.test", foreign.Agent)
	if _, err := a.Register(t.Context(), foreign); err == nil {
		t.Fatal("foreign binding accepted")
	}
	f.mu.Lock()
	count := f.created
	f.mu.Unlock()
	if count != 1 {
		t.Fatal("foreign create RPC invoked")
	}
	if err := a.Revoke(t.Context(), b.EntryID); err != nil {
		t.Fatal(err)
	}
	if err := a.Revoke(t.Context(), b.EntryID); err != nil {
		t.Fatal("missing entry not idempotent", err)
	}
	if err := a.Health(t.Context()); err != nil {
		t.Fatal(err)
	}
}
func TestAdminRejectsInvalidConfigurationAndServer(t *testing.T) {
	for _, address := range []string{"localhost:8081", "http://localhost:8081", "unix:///run/../admin.sock", "unix://host/admin.sock", "unix:///run/admin.sock?x=1"} {
		if a, err := OpenAdmin(t.Context(), address, "loomplane.test", "org"); err == nil {
			_ = a.Close()
			t.Fatalf("accepted %s", address)
		}
	}
	for _, tc := range []struct {
		name, domain, org, serverDomain string
		denied                          bool
	}{
		{"foreign bundle", "loomplane.test", "org", "foreign.test", false},
		{"no admin permission", "loomplane.test", "org", "loomplane.test", true},
		{"invalid org", "loomplane.test", "../org", "loomplane.test", false},
		{"invalid domain", "spiffe://loomplane.test", "org", "loomplane.test", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &adminFixture{domain: tc.serverDomain, denied: tc.denied}
			if a, err := OpenAdmin(t.Context(), serveAdminFixture(t, f), tc.domain, tc.org); err == nil {
				_ = a.Close()
				t.Fatal("invalid administrator accepted")
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if a, err := OpenAdmin(ctx, "unix:///nonexistent/loomplane-admin.sock", "loomplane.test", "org"); err == nil {
		_ = a.Close()
		t.Fatal("cancellation ignored")
	}
}
func TestAdminHealthRechecksServerAndClose(t *testing.T) {
	f := &adminFixture{domain: "loomplane.test"}
	a, err := OpenAdmin(t.Context(), serveAdminFixture(t, f), "loomplane.test", "org")
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.domain = "foreign.test"
	f.mu.Unlock()
	if err := a.Health(t.Context()); err == nil {
		t.Fatal("server identity change ignored")
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := a.Health(ctx); err == nil {
		t.Fatal("closed connection healthy")
	}
}
