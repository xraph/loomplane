package nodes

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/apikeys"
	"github.com/xraph/loomplane/pkg/nodeproto"
)

func TestSetIngressSendsControlMessage(t *testing.T) {
	r := newHubRig(t, newFakeStore(), "a")
	priv := seedNode(t, r.store, "node_1", "web-1", nodeproto.EndpointDocker)
	n := r.connect("node_1", priv, "")
	n.next() // hello
	n.send(nodeproto.InventoryReport{Inventory: nodeproto.Inventory{OS: "linux"}})
	n.next() // narrow
	n.next() // the stored ingress mode
	waitFor(t, "online", func() bool { return r.hub.Online("node_1") })

	if err := r.svc.SetIngress(context.Background(), "node_1", nodeproto.IngressDirect, "203.0.113.7"); err != nil {
		t.Fatal(err)
	}
	if m := n.next(); !reflect.DeepEqual(m, nodeproto.SetIngress{Mode: nodeproto.IngressDirect}) {
		t.Fatalf("message %#v", m)
	}
	got := r.node("node_1")
	if got.IngressMode != nodeproto.IngressDirect || got.PublicAddress != "203.0.113.7" || got.PublicAddressConfirmedAt == nil || got.IngressDetail != "" {
		t.Fatalf("node %+v", got)
	}
	n.send(nodeproto.IngressApplied{Mode: nodeproto.IngressDirect})
	waitFor(t, "the applied mode", func() bool { return r.node("node_1").IngressApplied == nodeproto.IngressDirect })
}

func TestSetIngressNoneSaysNoAddressAnswered(t *testing.T) {
	r := newRig(t, apikeys.NodeJoinScope{}, true)
	id := seed(r, nodeproto.EndpointDocker)
	if err := r.svc.SetIngress(context.Background(), id, nodeproto.IngressDirect, "203.0.113.7"); err != nil {
		t.Fatal(err)
	}
	if err := r.svc.SetIngress(context.Background(), id, nodeproto.IngressNone, ""); err != nil {
		t.Fatal(err)
	}
	n, _ := r.store.GetByID(context.Background(), id)
	if n.IngressMode != nodeproto.IngressNone || n.PublicAddress != "" || n.PublicAddressConfirmedAt != nil || n.IngressDetail != NoPublicAddressDetail {
		t.Fatalf("node %+v", n)
	}
	if len(r.sent) != 2 || r.sent[1].msg != (nodeproto.SetIngress{Mode: nodeproto.IngressNone}) {
		t.Fatalf("sent %+v", r.sent)
	}
}

func TestSetIngressRefusesWhatPhaseTwoDoesNotSet(t *testing.T) {
	r := newRig(t, apikeys.NodeJoinScope{}, true)
	id := seed(r, nodeproto.EndpointDocker)
	for _, c := range []struct{ mode, address string }{
		{nodeproto.IngressRelay, ""},
		{nodeproto.IngressDirect, ""},
		{nodeproto.IngressDirect, "web.example.com"},
		{nodeproto.IngressNone, "203.0.113.7"},
	} {
		if err := r.svc.SetIngress(context.Background(), id, c.mode, c.address); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s %q: %v", c.mode, c.address, err)
		}
	}
	if len(r.sent) != 0 {
		t.Fatalf("sent %+v", r.sent)
	}
}

func TestSetIngressMismatchShowsAfter30Seconds(t *testing.T) {
	r := newRig(t, apikeys.NodeJoinScope{}, true)
	id := seed(r, nodeproto.EndpointDocker)
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	r.svc.clock = func() time.Time { return now }
	if err := r.svc.SetIngress(context.Background(), id, nodeproto.IngressDirect, "203.0.113.7"); err != nil {
		t.Fatal(err)
	}
	detail := func() string {
		t.Helper()
		n, err := r.svc.Get(context.Background(), admin, id)
		if err != nil {
			t.Fatal(err)
		}
		return n.IngressDetail
	}
	now = now.Add(29 * time.Second)
	if d := detail(); d != "" {
		t.Fatalf("detail within the grace: %q", d)
	}
	now = now.Add(2 * time.Second)
	if d := detail(); d != "the node didn't apply ingress mode direct" {
		t.Fatalf("detail %q", d)
	}
	if list, err := r.svc.List(context.Background(), admin); err != nil || len(list) != 1 || list[0].IngressDetail != "the node didn't apply ingress mode direct" {
		t.Fatalf("list %+v %v", list, err)
	}
	if err := r.store.RecordIngressApplied(context.Background(), id, nodeproto.IngressDirect); err != nil {
		t.Fatal(err)
	}
	if d := detail(); d != "" {
		t.Fatalf("detail once applied: %q", d)
	}
}

func TestRecordIngressDetailKeepsTheAddress(t *testing.T) {
	r := newRig(t, apikeys.NodeJoinScope{}, true)
	id := seed(r, nodeproto.EndpointDocker)
	if err := r.svc.SetIngress(context.Background(), id, nodeproto.IngressDirect, "203.0.113.7"); err != nil {
		t.Fatal(err)
	}
	before, _ := r.store.GetByID(context.Background(), id)
	const detail = "address changed to 198.51.100.9 but it doesn't answer on port 80"
	if err := r.svc.RecordIngressDetail(context.Background(), id, detail); err != nil {
		t.Fatal(err)
	}
	n, _ := r.store.GetByID(context.Background(), id)
	if n.IngressDetail != detail || n.IngressMode != nodeproto.IngressDirect || n.PublicAddress != "203.0.113.7" || !n.PublicAddressConfirmedAt.Equal(*before.PublicAddressConfirmedAt) || !n.IngressSentAt.Equal(*before.IngressSentAt) {
		t.Fatalf("node %+v", n)
	}
	// Only the detail changed, so nothing goes to the node.
	if len(r.sent) != 1 {
		t.Fatalf("sent %+v", r.sent)
	}
	// A later confirmed address clears it.
	if err := r.svc.SetIngress(context.Background(), id, nodeproto.IngressDirect, "198.51.100.9"); err != nil {
		t.Fatal(err)
	}
	if n, _ = r.store.GetByID(context.Background(), id); n.IngressDetail != "" {
		t.Fatalf("detail %q", n.IngressDetail)
	}
	if err := r.svc.RecordIngressDetail(context.Background(), "node_missing", detail); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing node: %v", err)
	}
}
