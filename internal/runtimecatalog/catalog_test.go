package runtimecatalog

import (
	"context"
	"errors"
	"testing"
	"time"

	domain "github.com/xraph/loomplane/pkg/federation"
)

func catalogFixture(t *testing.T) (*Catalog, *time.Time, domain.AgentIdentity, domain.Endpoint) {
	t.Helper()
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	scope := domain.Scope{OrgID: "org", ProjectID: "project", EnvironmentID: "env"}
	agent := domain.AgentIdentity{Scope: scope, AgentID: "agent", DestinationID: "dest", URI: "spiffe://loomplane.test/agent", Epoch: 1}
	endpoint := domain.Endpoint{Metadata: domain.Metadata{Scope: scope, ID: "endpoint", Generation: 1}, DestinationID: "dest", ExportID: "export", PlacementID: "placement", BindingID: "binding", ObserverID: "agent", Address: "10.20.0.2", Port: 8080, Readiness: domain.Ready}
	store := NewMemoryStore()
	registration := Registration{Agent: agent, DestinationGeneration: 1, Active: true, Exports: map[string]ExportBounds{"export": {Scope: scope, DestinationID: "dest", PlacementID: "placement", BindingID: "binding", Port: 8080, CIDRs: []string{"10.20.0.0/24"}}}}
	if err := store.Register(t.Context(), registration); err != nil {
		t.Fatal(err)
	}
	permission := func(context.Context, domain.AgentIdentity) (Visibility, error) {
		return Visibility{Revision: "approved", ExpiresAt: now.Add(time.Minute), Exports: map[string]domain.Scope{"export": scope}}, nil
	}
	catalog := New(store, permission, nil)
	catalog.now = func() time.Time { return now }
	return catalog, &now, agent, endpoint
}

func observe(t *testing.T, c *Catalog, agent domain.AgentIdentity, sequence uint64, ep domain.Endpoint) {
	t.Helper()
	if err := c.Observe(t.Context(), agent, domain.ObservationBatch{Agent: agent, DestinationGeneration: 1, Sequence: sequence, Endpoints: []domain.Endpoint{ep}}); err != nil {
		t.Fatal(err)
	}
}
func snapshot(t *testing.T, c *Catalog, agent domain.AgentIdentity, request domain.SnapshotRequest) *domain.Snapshot {
	t.Helper()
	request.Agent = agent
	out, err := c.Snapshot(t.Context(), agent, request)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestEndpointLeaseAndGeneration(t *testing.T) {
	c, now, agent, ep := catalogFixture(t)
	ep.ExpiresAt = now.Add(24 * time.Hour)
	observe(t, c, agent, 1, ep)
	first := snapshot(t, c, agent, domain.SnapshotRequest{})
	if len(first.Endpoints) != 1 || first.Endpoints[0].ExpiresAt != now.Add(30*time.Second) {
		t.Fatalf("unbounded lease: %+v", first)
	}
	*now = now.Add(30 * time.Second)
	expired := snapshot(t, c, agent, domain.SnapshotRequest{})
	if len(expired.Endpoints) != 0 {
		t.Fatal("expired endpoint remained routable")
	}
	ep.Generation = 2
	observe(t, c, agent, 2, ep)
	stale := ep
	stale.Generation = 1
	if err := c.Observe(t.Context(), agent, domain.ObservationBatch{Agent: agent, DestinationGeneration: 1, Sequence: 3, Endpoints: []domain.Endpoint{stale}}); !errors.Is(err, ErrStale) {
		t.Fatalf("stale generation: %v", err)
	}
}

func TestEndpointTombstoneWins(t *testing.T) {
	c, _, agent, ep := catalogFixture(t)
	ep.Generation = 8
	observe(t, c, agent, 8, ep)
	tomb := domain.EndpointTombstone{Metadata: domain.Metadata{Scope: ep.Scope, ID: ep.ID, Generation: 9}, DestinationID: agent.DestinationID, ExportID: ep.ExportID, ObserverID: agent.AgentID}
	batch := domain.ObservationBatch{Agent: agent, DestinationGeneration: 1, Sequence: 9, Tombstones: []domain.EndpointTombstone{tomb}}
	if err := c.Observe(t.Context(), agent, batch); err != nil {
		t.Fatal(err)
	}
	batch.Sequence = 10
	batch.Tombstones = nil
	batch.Endpoints = []domain.Endpoint{ep}
	if err := c.Observe(t.Context(), agent, batch); !errors.Is(err, ErrStale) {
		t.Fatalf("resurrected generation 8: %v", err)
	}
	page := snapshot(t, c, agent, domain.SnapshotRequest{})
	if len(page.Endpoints) != 0 || len(page.Tombstones) != 1 || page.Tombstones[0].Generation != 9 {
		t.Fatalf("lost tombstone: %+v", page)
	}
}

func TestCatalogScope(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*domain.AgentIdentity, *domain.ObservationBatch)
	}{
		{"forged principal", func(a *domain.AgentIdentity, _ *domain.ObservationBatch) { a.OrgID = "foreign" }},
		{"forged batch", func(_ *domain.AgentIdentity, b *domain.ObservationBatch) { b.Agent.OrgID = "foreign" }},
		{"wrong epoch", func(a *domain.AgentIdentity, b *domain.ObservationBatch) { a.Epoch++; b.Agent = *a }},
		{"old destination", func(_ *domain.AgentIdentity, b *domain.ObservationBatch) { b.DestinationGeneration = 2 }},
		{"foreign endpoint", func(_ *domain.AgentIdentity, b *domain.ObservationBatch) { b.Endpoints[0].ProjectID = "foreign" }},
		{"foreign destination", func(_ *domain.AgentIdentity, b *domain.ObservationBatch) { b.Endpoints[0].DestinationID = "neighbor" }},
		{"foreign binding", func(_ *domain.AgentIdentity, b *domain.ObservationBatch) { b.Endpoints[0].BindingID = "neighbor" }},
		{"unregistered port", func(_ *domain.AgentIdentity, b *domain.ObservationBatch) { b.Endpoints[0].Port = 5432 }},
		{"metadata", func(_ *domain.AgentIdentity, b *domain.ObservationBatch) { b.Endpoints[0].Address = "169.254.169.254" }},
		{"loopback", func(_ *domain.AgentIdentity, b *domain.ObservationBatch) { b.Endpoints[0].Address = "127.0.0.1" }},
		{"ipv6 mapped loopback", func(_ *domain.AgentIdentity, b *domain.ObservationBatch) { b.Endpoints[0].Address = "::ffff:127.0.0.1" }},
		{"outside CIDR", func(_ *domain.AgentIdentity, b *domain.ObservationBatch) { b.Endpoints[0].Address = "10.30.0.2" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _, agent, ep := catalogFixture(t)
			b := domain.ObservationBatch{Agent: agent, DestinationGeneration: 1, Sequence: 1, Endpoints: []domain.Endpoint{ep}}
			tc.change(&agent, &b)
			if err := c.Observe(t.Context(), agent, b); err == nil {
				t.Fatal("accepted unauthorized observation")
			}
		})
	}
}

func TestSnapshotAcrossReconnect(t *testing.T) {
	c, now, agent, ep := catalogFixture(t)
	other := ep
	other.ID = "other"
	other.Address = "10.20.0.3"
	if err := c.Observe(t.Context(), agent, domain.ObservationBatch{Agent: agent, DestinationGeneration: 1, Sequence: 1, Endpoints: []domain.Endpoint{ep, other}}); err != nil {
		t.Fatal(err)
	}
	first := snapshot(t, c, agent, domain.SnapshotRequest{Limit: 1})
	if first.NextCursor == "" || len(first.Endpoints) != 1 {
		t.Fatalf("not paginated: %+v", first)
	}
	other.Generation = 2
	other.Address = "10.20.0.4"
	observe(t, c, agent, 2, other)
	second := snapshot(t, c, agent, domain.SnapshotRequest{Limit: 1, Cursor: first.NextCursor})
	if second.Revision != first.Revision || len(second.Endpoints) != 1 || second.Endpoints[0].Address != "10.20.0.3" {
		t.Fatalf("mixed revision: %+v", second)
	}
	foreign := agent
	foreign.OrgID = "foreign"
	if _, err := c.Snapshot(t.Context(), foreign, domain.SnapshotRequest{Agent: foreign, Cursor: first.NextCursor}); err == nil {
		t.Fatal("foreign cursor accepted")
	}
	*now = now.Add(30 * time.Second)
	if _, err := c.Snapshot(t.Context(), agent, domain.SnapshotRequest{Agent: agent, Cursor: first.NextCursor}); !errors.Is(err, ErrCursorExpired) {
		t.Fatalf("expired cursor: %v", err)
	}
	full := snapshot(t, c, agent, domain.SnapshotRequest{AfterSequence: first.Sequence})
	if !full.Full {
		t.Fatal("reconnect cannot request a full resync")
	}
}

func TestSnapshotPolicyChangeInvalidatesCursor(t *testing.T) {
	c, _, agent, ep := catalogFixture(t)
	other := ep
	other.ID = "other"
	other.Address = "10.20.0.3"
	if err := c.Observe(t.Context(), agent, domain.ObservationBatch{Agent: agent, DestinationGeneration: 1, Sequence: 1, Endpoints: []domain.Endpoint{ep, other}}); err != nil {
		t.Fatal(err)
	}
	first := snapshot(t, c, agent, domain.SnapshotRequest{Limit: 1})
	// Even an accidentally unchanged provider revision cannot preserve revoked visibility.
	c.authorize = func(context.Context, domain.AgentIdentity) (Visibility, error) {
		return Visibility{Revision: "approved", ExpiresAt: c.now().Add(time.Minute), Exports: map[string]domain.Scope{}}, nil
	}
	if _, err := c.Snapshot(t.Context(), agent, domain.SnapshotRequest{Agent: agent, Cursor: first.NextCursor}); !errors.Is(err, ErrCursorExpired) {
		t.Fatalf("revoked visibility retained cursor: %v", err)
	}
}

func TestCatalogBatchAtomicAndFullResync(t *testing.T) {
	c, _, agent, ep := catalogFixture(t)
	other := ep
	other.ID = "other"
	other.Address = "10.20.0.3"
	observe(t, c, agent, 1, ep)
	other.Generation = 2
	bad := ep
	bad.Generation = 2
	bad.Address = "169.254.169.254"
	batch := domain.ObservationBatch{Agent: agent, DestinationGeneration: 1, Sequence: 2, Endpoints: []domain.Endpoint{other, bad}}
	if err := c.Observe(t.Context(), agent, batch); err == nil {
		t.Fatal("bad batch accepted")
	}
	page := snapshot(t, c, agent, domain.SnapshotRequest{})
	if len(page.Endpoints) != 1 || page.Endpoints[0].ID != ep.ID {
		t.Fatal("failed batch partially committed")
	}
	batch.Endpoints = []domain.Endpoint{other}
	batch.Full = true
	if err := c.Observe(t.Context(), agent, batch); err != nil {
		t.Fatal(err)
	}
	page = snapshot(t, c, agent, domain.SnapshotRequest{})
	if len(page.Endpoints) != 1 || page.Endpoints[0].ID != other.ID || len(page.Tombstones) != 1 || page.Tombstones[0].ID != ep.ID {
		t.Fatalf("full resync left missing endpoint live: %+v", page)
	}
	batch.Sequence = 3
	batch.Full = false
	batch.Endpoints = make([]domain.Endpoint, MaxBatch+1)
	if err := c.Observe(t.Context(), agent, batch); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unbounded batch: %v", err)
	}
}

func TestTombstoneCompactionRetainsReplayFence(t *testing.T) {
	c, now, agent, ep := catalogFixture(t)
	ep.Generation = 8
	observe(t, c, agent, 8, ep)
	tomb := domain.EndpointTombstone{Metadata: domain.Metadata{Scope: ep.Scope, ID: ep.ID, Generation: 9}, DestinationID: agent.DestinationID, ExportID: ep.ExportID, ObserverID: agent.AgentID}
	if err := c.Observe(t.Context(), agent, domain.ObservationBatch{Agent: agent, DestinationGeneration: 1, Sequence: 9, Tombstones: []domain.EndpointTombstone{tomb}}); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(EndpointLease)
	next := ep
	next.ID = "replacement"
	next.Generation = 10
	observe(t, c, agent, 10, next)
	if len(snapshot(t, c, agent, domain.SnapshotRequest{}).Tombstones) != 0 {
		t.Fatal("expired tombstone retained without bound")
	}
	if err := c.Observe(t.Context(), agent, domain.ObservationBatch{Agent: agent, DestinationGeneration: 1, Sequence: 11, Endpoints: []domain.Endpoint{ep}}); !errors.Is(err, ErrStale) {
		t.Fatalf("compacted tombstone resurrected: %v", err)
	}
}

func TestSnapshotShortenedPolicyInvalidatesCursor(t *testing.T) {
	c, _, agent, ep := catalogFixture(t)
	other := ep
	other.ID = "other"
	if err := c.Observe(t.Context(), agent, domain.ObservationBatch{Agent: agent, DestinationGeneration: 1, Sequence: 1, Endpoints: []domain.Endpoint{ep, other}}); err != nil {
		t.Fatal(err)
	}
	first := snapshot(t, c, agent, domain.SnapshotRequest{Limit: 1})
	c.authorize = func(context.Context, domain.AgentIdentity) (Visibility, error) {
		return Visibility{Revision: "approved", ExpiresAt: c.now().Add(time.Second), Exports: map[string]domain.Scope{"export": agent.Scope}}, nil
	}
	if _, err := c.Snapshot(t.Context(), agent, domain.SnapshotRequest{Agent: agent, Cursor: first.NextCursor}); !errors.Is(err, ErrCursorExpired) {
		t.Fatalf("shorter authority retained longer snapshot: %v", err)
	}
}

func TestSnapshotRevokedSourceInvalidatesCursor(t *testing.T) {
	c, _, consumer, ep := catalogFixture(t)
	store := c.store.(*MemoryStore)
	source := consumer
	source.AgentID = "source"
	source.DestinationID = "source-destination"
	reg := clone(store.states[consumer.AgentID].Registration)
	reg.Agent = source
	bounds := reg.Exports["export"]
	bounds.DestinationID = source.DestinationID
	reg.Exports["export"] = bounds
	if err := store.Register(t.Context(), reg); err != nil {
		t.Fatal(err)
	}
	ep.ObserverID = source.AgentID
	ep.DestinationID = source.DestinationID
	other := ep
	other.ID = "other"
	if err := c.Observe(t.Context(), source, domain.ObservationBatch{Agent: source, DestinationGeneration: 1, Sequence: 1, Endpoints: []domain.Endpoint{ep, other}}); err != nil {
		t.Fatal(err)
	}
	first := snapshot(t, c, consumer, domain.SnapshotRequest{Limit: 1})
	state := store.states[source.AgentID]
	state.Registration.Active = false
	store.states[source.AgentID] = state
	if _, err := c.Snapshot(t.Context(), consumer, domain.SnapshotRequest{Agent: consumer, Cursor: first.NextCursor}); !errors.Is(err, ErrCursorExpired) {
		t.Fatalf("revoked source remained in cached page: %v", err)
	}
}

func TestEndpointIPv6MetadataDenied(t *testing.T) {
	c, _, agent, ep := catalogFixture(t)
	store := c.store.(*MemoryStore)
	reg := store.states[agent.AgentID].Registration
	bounds := reg.Exports["export"]
	bounds.CIDRs = []string{"fd00::/8"}
	reg.Exports["export"] = bounds
	if err := store.Register(t.Context(), reg); err != nil {
		t.Fatal(err)
	}
	ep.Address = "fd00:ec2::254"
	if err := c.Observe(t.Context(), agent, domain.ObservationBatch{Agent: agent, DestinationGeneration: 1, Sequence: 1, Endpoints: []domain.Endpoint{ep}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("IPv6 metadata accepted inside allowed CIDR: %v", err)
	}
	ep.Address = "fd12:3456::2"
	observe(t, c, agent, 1, ep)
}

func TestCatalogConcurrentGenerationFence(t *testing.T) {
	c, _, agent, ep := catalogFixture(t)
	failures := make(chan error, 64)
	for i := 1; i <= 64; i++ {
		go func(sequence int) {
			candidate := ep
			candidate.Generation = int64(sequence)
			failures <- c.Observe(t.Context(), agent, domain.ObservationBatch{Agent: agent, DestinationGeneration: 1, Sequence: uint64(sequence), Endpoints: []domain.Endpoint{candidate}})
		}(i)
	}
	for range 64 {
		if err := <-failures; err != nil && !errors.Is(err, ErrStale) {
			t.Fatal(err)
		}
	}
	out := snapshot(t, c, agent, domain.SnapshotRequest{})
	if len(out.Endpoints) != 1 || out.Endpoints[0].Generation != 64 {
		t.Fatalf("lost highest generation: %+v", out)
	}
}

func TestSnapshotDoesNotLeakForeignExport(t *testing.T) {
	c, _, agent, ep := catalogFixture(t)
	store := c.store.(*MemoryStore)
	reg := clone(store.states[agent.AgentID].Registration)
	reg.Agent.AgentID = "foreign-agent"
	reg.Agent.DestinationID = "foreign-destination"
	reg.Agent.EnvironmentID = "foreign-env"
	bounds := reg.Exports["export"]
	bounds.Scope = reg.Agent.Scope
	bounds.DestinationID = reg.Agent.DestinationID
	reg.Exports = map[string]ExportBounds{"foreign-export": bounds}
	if err := store.Register(t.Context(), reg); err != nil {
		t.Fatal(err)
	}
	ep.ObserverID = reg.Agent.AgentID
	ep.DestinationID = reg.Agent.DestinationID
	ep.Scope = reg.Agent.Scope
	ep.ExportID = "foreign-export"
	observe(t, c, reg.Agent, 1, ep)
	out := snapshot(t, c, agent, domain.SnapshotRequest{})
	if len(out.Endpoints) != 0 || len(out.Tombstones) != 0 {
		t.Fatal("foreign export leaked from org-wide store")
	}
}
