package workloadidentity

import (
	"context"
	"testing"
	"time"

	entryv1 "github.com/spiffe/spire-api-sdk/proto/spire/api/server/entry/v1"
	spiretypes "github.com/spiffe/spire-api-sdk/proto/spire/api/types"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"

	domain "github.com/xraph/loomplane/pkg/federation"
)

type entryClient struct {
	created *spiretypes.Entry
	tamper  bool
}

func (c *entryClient) BatchCreateEntry(_ context.Context, in *entryv1.BatchCreateEntryRequest, _ ...grpc.CallOption) (*entryv1.BatchCreateEntryResponse, error) {
	c.created = proto.Clone(in.Entries[0]).(*spiretypes.Entry)
	out := proto.Clone(c.created).(*spiretypes.Entry)
	if c.tamper {
		out.Admin = true
	}
	return &entryv1.BatchCreateEntryResponse{Results: []*entryv1.BatchCreateEntryResponse_Result{{Status: &spiretypes.Status{}, Entry: out}}}, nil
}
func (c *entryClient) BatchDeleteEntry(_ context.Context, in *entryv1.BatchDeleteEntryRequest, _ ...grpc.CallOption) (*entryv1.BatchDeleteEntryResponse, error) {
	return &entryv1.BatchDeleteEntryResponse{Results: []*entryv1.BatchDeleteEntryResponse_Result{{Id: in.Ids[0], Status: &spiretypes.Status{}}}}, nil
}
func bindingFixture(t *testing.T) Binding {
	t.Helper()
	a := identityFixture()
	uri, err := AgentURI("loomplane.test", a)
	if err != nil {
		t.Fatal(err)
	}
	a.URI = uri
	d := domain.Destination{Scope: a.Scope, ID: a.DestinationID, Generation: 1, Identity: domain.PhysicalIdentity{Provider: "kubernetes", Account: "cluster", ResourceID: "ca-fingerprint", NamespaceUID: "namespace-uid"}}
	return Binding{EntryID: "01234567-89ab-cdef-0123-456789abcdef", Agent: a, Destination: d, ParentURI: "spiffe://loomplane.test/spire/agent/test-node", CheckedAt: time.Now(), Method: "kubernetes", Namespace: "apps", NamespaceUID: "namespace-uid", ServiceAccount: "loomplane-agent", PodUID: "pod-uid", Container: "agent", TTL: time.Minute}
}
func TestSPIRERegistrationPinsAttestation(t *testing.T) {
	client := &entryClient{}
	issuer := &SPIREIssuer{client: client, domain: "loomplane.test", now: time.Now}
	binding := bindingFixture(t)
	if _, err := issuer.Register(t.Context(), binding); err != nil {
		t.Fatal(err)
	}
	if client.created.SpiffeId.Path == "" || len(client.created.Selectors) != 4 || client.created.Admin || client.created.Downstream {
		t.Fatalf("unsafe entry: %+v", client.created)
	}
	for _, change := range []func(*Binding){
		func(b *Binding) { b.Agent.DestinationID = "foreign" }, func(b *Binding) { b.NamespaceUID = "recreated" }, func(b *Binding) { b.PodUID = "" }, func(b *Binding) { b.Container = "" }, func(b *Binding) { b.CheckedAt = time.Now().Add(-10 * time.Minute) }, func(b *Binding) { b.ParentURI = "spiffe://foreign.test/node" }, func(b *Binding) { b.Method = "miget" }, func(b *Binding) { b.Destination.Quarantined = true },
	} {
		b := binding
		change(&b)
		if _, err := issuer.Register(t.Context(), b); err == nil {
			t.Fatal("invalid binding issued identity")
		}
	}
	client.tamper = true
	if _, err := issuer.Register(t.Context(), binding); err == nil {
		t.Fatal("broadened response accepted")
	}
}
