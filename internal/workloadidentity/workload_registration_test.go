package workloadidentity

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	entryv1 "github.com/spiffe/spire-api-sdk/proto/spire/api/server/entry/v1"
	spiretypes "github.com/spiffe/spire-api-sdk/proto/spire/api/types"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"

	domain "github.com/xraph/loomplane/pkg/federation"
)

func workloadBindingFixture(t *testing.T) WorkloadBinding {
	t.Helper()
	b := bindingFixture(t)
	b.Agent = domain.AgentIdentity{}
	return WorkloadBinding{Binding: b, ContainerImage: "registry.example/api@sha256:" + strings.Repeat("a", 64), Workload: domain.WorkloadSelector{Scope: b.Destination.Scope, DestinationID: b.Destination.ID, ServiceID: "api"}, PruneAfter: time.Now().Add(2 * time.Minute).Truncate(time.Second)}
}

func TestWorkloadRegistrationPinsIdentityAttestationAndPruning(t *testing.T) {
	b := workloadBindingFixture(t)
	client := &entryClient{}
	issuer := &SPIREIssuer{client: client, domain: "loomplane.test", now: time.Now}
	entry, err := issuer.RegisterWorkload(t.Context(), b)
	if err != nil {
		t.Fatal(err)
	}
	uri, err := WorkloadURI("loomplane.test", b.Workload)
	if err != nil || "spiffe://"+entry.SpiffeId.TrustDomain+entry.SpiffeId.Path != uri || entry.ExpiresAt != b.PruneAfter.Unix() || entry.X509SvidTtl != 60 || entry.JwtSvidTtl != 60 || len(entry.Selectors) != 5 || entry.Selectors[4].Type != "k8s" || entry.Selectors[4].Value != "container-image:"+b.ContainerImage || entry.Admin || entry.Downstream {
		t.Fatalf("wrong workload registration: %v, %v", entry, err)
	}
	for name, change := range map[string]func(*WorkloadBinding){
		"agent authority":                func(b *WorkloadBinding) { b.Binding.Agent = identityFixture() },
		"foreign organization":           func(b *WorkloadBinding) { b.Workload.OrgID = "foreign" },
		"foreign project":                func(b *WorkloadBinding) { b.Workload.ProjectID = "foreign" },
		"foreign environment":            func(b *WorkloadBinding) { b.Workload.EnvironmentID = "foreign" },
		"foreign destination":            func(b *WorkloadBinding) { b.Workload.DestinationID = "foreign" },
		"missing image":                  func(b *WorkloadBinding) { b.ContainerImage = "" },
		"mutable image":                  func(b *WorkloadBinding) { b.ContainerImage = "api:latest" },
		"missing service":                func(b *WorkloadBinding) { b.Workload.ServiceID = "" },
		"path injection":                 func(b *WorkloadBinding) { b.Workload.ServiceID = "api/agent" },
		"missing expiry":                 func(b *WorkloadBinding) { b.PruneAfter = time.Time{} },
		"expired":                        func(b *WorkloadBinding) { b.PruneAfter = time.Now().Add(-time.Second).Truncate(time.Second) },
		"unbounded":                      func(b *WorkloadBinding) { b.PruneAfter = time.Now().Add(time.Hour).Truncate(time.Second) },
		"fractional expiry":              func(b *WorkloadBinding) { b.PruneAfter = b.PruneAfter.Add(time.Nanosecond) },
		"excessive certificate lifetime": func(b *WorkloadBinding) { b.Binding.TTL = 6 * time.Minute },
		"stale evidence":                 func(b *WorkloadBinding) { b.Binding.CheckedAt = time.Now().Add(-10 * time.Minute) },
		"evidence cannot extend lease":   func(b *WorkloadBinding) { b.Binding.CheckedAt = time.Now().Add(-4 * time.Minute) },
		"recreated namespace":            func(b *WorkloadBinding) { b.Binding.NamespaceUID = "replacement" },
		"missing pod":                    func(b *WorkloadBinding) { b.Binding.PodUID = "" },
		"missing container":              func(b *WorkloadBinding) { b.Binding.Container = "" },
		"quarantined destination":        func(b *WorkloadBinding) { b.Binding.Destination.Quarantined = true },
		"foreign parent":                 func(b *WorkloadBinding) { b.Binding.ParentURI = "spiffe://foreign.test/node" },
		"self parent":                    func(b *WorkloadBinding) { b.Binding.ParentURI = uri },
		"privileged parent":              func(b *WorkloadBinding) { b.Binding.ParentURI = "spiffe://loomplane.test/spire/server" },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := b
			change(&candidate)
			client.created = nil
			if _, registerErr := issuer.RegisterWorkload(t.Context(), candidate); !errors.Is(registerErr, ErrIdentity) || client.created != nil {
				t.Fatalf("invalid registration reached SPIRE: %v", registerErr)
			}
		})
	}
	shortPruning := workloadBindingFixture(t)
	shortPruning.PruneAfter = time.Now().Add(30 * time.Second).Truncate(time.Second)
	shortPruning.Binding.TTL = 5 * time.Minute
	if issued, issueErr := issuer.RegisterWorkload(t.Context(), shortPruning); issueErr != nil || issued.X509SvidTtl != 300 {
		t.Fatalf("certificate TTL incorrectly tied to housekeeping: %v", issueErr)
	}
	b.ContainerImage = ""
	uid := uint32(1000)
	b.Binding.Method, b.Binding.Destination.Identity.Provider = "unix", "server"
	b.Binding.UnixUID, b.Binding.Executable, b.Binding.SHA256 = &uid, "/opt/app", strings.Repeat("a", 64)
	if entry, err = issuer.RegisterWorkload(t.Context(), b); err != nil || len(entry.Selectors) != 3 {
		t.Fatalf("Unix binding failed: %v", err)
	}
	b.Binding.SHA256 = ""
	if _, err = issuer.RegisterWorkload(t.Context(), b); !errors.Is(err, ErrIdentity) {
		t.Fatal("unpinned Unix executable accepted", err)
	}
}

type workloadEntryClient struct {
	entryClient
	mutate func(*spiretypes.Entry)
}

func (c *workloadEntryClient) BatchCreateEntry(ctx context.Context, in *entryv1.BatchCreateEntryRequest, opts ...grpc.CallOption) (*entryv1.BatchCreateEntryResponse, error) {
	result, err := c.entryClient.BatchCreateEntry(ctx, in, opts...)
	if err == nil {
		result.Results[0].Status.Code = int32(codes.AlreadyExists)
		c.mutate(result.Results[0].Entry)
	}
	return result, err
}

func TestWorkloadRegistrationRejectsBroadenedOrConflictingRetry(t *testing.T) {
	for name, mutate := range map[string]func(*spiretypes.Entry){
		"expiry removed":   func(e *spiretypes.Entry) { e.ExpiresAt = 0 },
		"expiry extended":  func(e *spiretypes.Entry) { e.ExpiresAt++ },
		"selector removed": func(e *spiretypes.Entry) { e.Selectors = e.Selectors[:3] },
		"service changed":  func(e *spiretypes.Entry) { e.SpiffeId.Path += "neighbor" },
		"parent changed":   func(e *spiretypes.Entry) { e.ParentId.Path += "neighbor" },
		"admin":            func(e *spiretypes.Entry) { e.Admin = true },
		"downstream":       func(e *spiretypes.Entry) { e.Downstream = true },
		"federation added": func(e *spiretypes.Entry) { e.FederatesWith = []string{"foreign.test"} },
		"ttl extended":     func(e *spiretypes.Entry) { e.X509SvidTtl++ },
	} {
		t.Run(name, func(t *testing.T) {
			client := &workloadEntryClient{mutate: mutate}
			issuer := &SPIREIssuer{client: client, domain: "loomplane.test", now: time.Now}
			if _, err := issuer.RegisterWorkload(t.Context(), workloadBindingFixture(t)); !errors.Is(err, ErrIdentity) {
				t.Fatal("conflicting existing registration accepted", err)
			}
		})
	}
	client := &workloadEntryClient{mutate: func(e *spiretypes.Entry) { e.CreatedAt = time.Now().Unix(); e.RevisionNumber = 1 }}
	issuer := &SPIREIssuer{client: client, domain: "loomplane.test", now: time.Now}
	if _, err := issuer.RegisterWorkload(t.Context(), workloadBindingFixture(t)); err != nil {
		t.Fatal("identical retry rejected", err)
	}
}

func TestAdminScopesWorkloadRegistrationAndRevocation(t *testing.T) {
	fixture := &adminFixture{domain: "loomplane.test", entries: map[string]*spiretypes.Entry{}}
	admin, err := OpenAdmin(t.Context(), serveAdminFixture(t, fixture), "loomplane.test", "org")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := admin.Close(); err != nil {
			t.Error(err)
		}
	})
	b := workloadBindingFixture(t)
	if _, err := admin.RegisterWorkload(t.Context(), b); err != nil {
		t.Fatal(err)
	}
	foreign := b
	foreign.Workload.OrgID, foreign.Binding.Destination.OrgID = "foreign", "foreign"
	if _, err := admin.RegisterWorkload(t.Context(), foreign); !errors.Is(err, ErrIdentity) {
		t.Fatal("foreign registration accepted", err)
	}
	fixture.mu.Lock()
	fixture.entries[b.Binding.EntryID].SpiffeId.Path = strings.Replace(fixture.entries[b.Binding.EntryID].SpiffeId.Path, "/org/org/", "/org/foreign/", 1)
	fixture.mu.Unlock()
	if err := admin.Revoke(t.Context(), b.Binding.EntryID); !errors.Is(err, ErrIdentity) {
		t.Fatal("foreign workload deleted", err)
	}
	fixture.mu.Lock()
	if fixture.created != 1 || fixture.deleted != 0 {
		t.Error("foreign operation reached SPIRE")
	}
	fixture.entries[b.Binding.EntryID].SpiffeId.Path = strings.Replace(fixture.entries[b.Binding.EntryID].SpiffeId.Path, "/org/foreign/", "/org/org/", 1)
	fixture.mu.Unlock()
	if err := admin.Revoke(t.Context(), b.Binding.EntryID); err != nil {
		t.Fatal(err)
	}
	if err := admin.Revoke(t.Context(), b.Binding.EntryID); err != nil {
		t.Fatal("revoke retry failed", err)
	}
}
