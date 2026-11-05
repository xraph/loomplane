package notifications

import (
	"errors"
	"testing"

	"github.com/xraph/relay/endpoint"
	"github.com/xraph/relay/id"
	relaymem "github.com/xraph/relay/store/memory"
)

// RawEndpoints goes into DI for readers such as Hallproof's witness. It is
// the store under the encrypting wrapper, so anything that wrote through it
// would keep a signing secret in plaintext. Its writes are refused and its
// reads pass through.
func TestRawEndpointsOnlyReads(t *testing.T) {
	ctx := t.Context()
	mem := relaymem.New()
	stored := &endpoint.Endpoint{ID: id.NewEndpointID(), TenantID: "aorg_1", URL: "https://example.com/hook", Secret: "whsec_sealed", EventTypes: []string{"*"}, Enabled: true}
	if err := mem.CreateEndpoint(ctx, stored); err != nil {
		t.Fatal(err)
	}
	s := &Service{relayStore: &encryptedRelayStore{Store: mem}}
	var raw endpoint.Store = s.RawEndpoints()

	plain := &endpoint.Endpoint{ID: id.NewEndpointID(), TenantID: "aorg_1", URL: "https://example.com/other", Secret: "whsec_plaintext", Enabled: true}
	for name, err := range map[string]error{
		"create":      raw.CreateEndpoint(ctx, plain),
		"update":      raw.UpdateEndpoint(ctx, stored),
		"delete":      raw.DeleteEndpoint(ctx, stored.ID),
		"set enabled": raw.SetEnabled(ctx, stored.ID, false),
	} {
		if !errors.Is(err, ErrEndpointsReadOnly) {
			t.Errorf("%s through the raw store = %v, want ErrEndpointsReadOnly", name, err)
		}
	}
	if _, err := mem.GetEndpoint(ctx, plain.ID); err == nil {
		t.Fatal("the raw store saved an endpoint")
	}

	got, err := raw.GetEndpoint(ctx, stored.ID)
	if err != nil || got.URL != stored.URL || !got.Enabled {
		t.Fatalf("get = %+v, %v; want the stored endpoint, still enabled", got, err)
	}
	list, err := raw.ListEndpoints(ctx, "aorg_1", endpoint.ListOpts{})
	if err != nil || len(list) != 1 {
		t.Fatalf("list = %d, %v; want the one endpoint", len(list), err)
	}
	resolved, err := raw.Resolve(ctx, "aorg_1", "deployment.finished")
	if err != nil || len(resolved) != 1 {
		t.Fatalf("resolve = %d, %v; want the one endpoint", len(resolved), err)
	}
}
