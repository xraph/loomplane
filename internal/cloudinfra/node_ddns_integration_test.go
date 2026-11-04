//go:build integration

package cloudinfra

import (
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/xraph/ctrlplane/id"

	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/nodes"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/nodeproto"
	"github.com/xraph/loomplane/pkg/provider"
)

// TestDDNSAcceptReplacesAnEarlierAddressChange pins the store's rules for
// an address operation: it cancels an earlier one that hasn't finished and
// replaces the record that one wrote, and it waits for anything else.
func TestDDNSAcceptReplacesAnEarlierAddressChange(t *testing.T) {
	pg := routeBindingDatabase(t)
	ctx := t.Context()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := pg.Exec(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO loomplane_projects(id,org_id,name,slug,status,created_by,created_at,updated_at) VALUES('shop','org','Shop','shop','ready','usr',now(),now())`)
	exec(`INSERT INTO loomplane_environments(id,org_id,project_id,name,kind,created_at,updated_at) VALUES('prod','org','shop','production','production',now(),now())`)
	now := time.Now().UTC()
	details := map[string]string{"namespace": "shop", "namespace_uid": "ns-uid", "ingress_class": "nginx", "ingress_address": "203.0.113.10", "endpoint": "https://cluster.example.test"}
	if err := connections.NewPgStore(pg).CreateConnection(ctx, &connections.Connection{ID: "conn_k8s", OrgID: "org", Cloud: "kubernetes", Name: "Cluster", Status: connections.StatusReady, Details: details, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	exec(`CREATE TABLE cp_instances(id TEXT PRIMARY KEY,tenant_id TEXT,current_release TEXT,service_refs JSONB,services JSONB,labels JSONB,state TEXT)`)
	exec(`CREATE TABLE cp_deployments(id TEXT PRIMARY KEY,tenant_id TEXT,instance_id TEXT,release_id TEXT,state TEXT,created_at TIMESTAMPTZ DEFAULT now())`)
	instance := id.New(id.PrefixInstance)
	exec(`INSERT INTO cp_instances VALUES($1,'org',NULL,'{}',jsonb_build_array(jsonb_build_object('name','gateway','ports','[{"container":8080}]'::jsonb)),'{}','running')`, instance.String())
	exec(`INSERT INTO loomplane_bindings(id,org_id,project_id,environment_id,subject,name,provider,engine,external_id,connection_id,config,created_at,updated_at) VALUES('b_web','org','shop','prod','resource','web--gateway','kubernetes','http',$1,'conn_k8s',$2,now(),now())`,
		instance.String(), `{"component":{"kind":"octopus","resource":"web","component":{"name":"gateway","role":"process"}},"routable_ports":[8080]}`)
	exec(`INSERT INTO loomplane_dns_zones(id,org_id,name,connection_id,revision,body) VALUES('zone','org','example.com','',1,$1)`,
		`{"id":"zone","org_id":"org","name":"example.com","mode":"external","project_ids":["shop"],"proof_name":"_loomplane.example.com","proof_value":"proof","proof_expires_at":"2026-11-06T12:00:00Z","verified_at":"2026-11-05T12:00:00Z","revision":1}`)
	st := NewPgStore(pg)
	in := RouteInput{ZoneID: "zone", EnvironmentID: "prod", Service: "web--gateway", Port: 8080, Hostname: "app.example.com"}
	binding, err := st.ResolveRouteBinding(ctx, "org", "shop", in)
	if err != nil {
		t.Fatal(err)
	}
	user := security.Principal{OrgID: "org", UserID: "usr"}
	apply := &RouteOperation{ID: "op_apply", Kind: "apply", Status: "preview", Revision: 1, Principal: user,
		Route: Route{ID: "route_app", OrgID: "org", ProjectID: "shop", Input: in, Binding: binding, Status: "pending", OperationID: "op_apply", Revision: 1}}
	if err = st.PreviewRoute(ctx, apply); err != nil {
		t.Fatal(err)
	}
	apply.Status = "pending"
	if err = st.AcceptRoute(ctx, apply); err != nil {
		t.Fatal(err)
	}
	system := security.SystemPrincipal("org")
	route := apply.Route
	address := func(id string) *RouteOperation {
		r := route
		r.Binding.Address = "198.51.100.9"
		return &RouteOperation{ID: id, Kind: "address", Automatic: true, Status: "pending", Attempt: 1, Revision: 1, Route: r, PreviousAddress: route.Binding.Address,
			DesiredRecord: provider.DNSRecord{ID: "rec_1", Name: "app", Type: "A", Value: "198.51.100.9", TTL: 60}, PreviousReceipt: &provider.DNSRecord{ID: "rec_1", Name: "app", Type: "A", Value: "203.0.113.10", TTL: 60},
			Principal: system, ExecutionPrincipal: &system}
	}
	status := func(id string) string {
		t.Helper()
		var s string
		if scanErr := pg.QueryRow(ctx, `SELECT status FROM loomplane_route_operations WHERE id=$1`, id).Scan(&s); scanErr != nil {
			t.Fatal(scanErr)
		}
		return s
	}

	// The route's own apply is still pending: an address change waits.
	if err = st.AcceptRoute(ctx, address("op_early")); !errors.Is(err, ErrBusy) {
		t.Fatalf("accepted over a pending apply: %v", err)
	}
	exec(`UPDATE loomplane_route_operations SET status='succeeded',body=jsonb_set(body,'{status}','"succeeded"') WHERE id='op_apply'`)
	exec(`UPDATE loomplane_domain_routes SET status='ready',body=jsonb_set(body,'{status}','"ready"') WHERE id='route_app'`)

	first := address("op_first")
	if err = st.AcceptRoute(ctx, first); err != nil {
		t.Fatal(err)
	}
	// The first one wrote its record and is waiting for DNS.
	exec(`UPDATE loomplane_route_operations SET status='waiting',body=jsonb_set(jsonb_set(body,'{status}','"waiting"'),'{receipt}',$1::jsonb) WHERE id='op_first'`,
		`{"id":"rec_1b","zone_id":"","name":"app","type":"A","value":"198.51.100.9","ttl":60,"owner":"loomplane:o"}`)
	second := address("op_second")
	second.Route.Binding.Address = "192.0.2.77"
	if err = st.AcceptRoute(ctx, second); err != nil {
		t.Fatal(err)
	}
	if s := status("op_first"); s != "cancelled" {
		t.Fatalf("first is %s", s)
	}
	if second.PreviousReceipt == nil || second.PreviousReceipt.ID != "rec_1b" || second.PreviousReceipt.Value != "198.51.100.9" || second.DesiredRecord.ID != "rec_1b" || second.DesiredRecord.Owner != "loomplane:o" {
		t.Fatalf("second replaces %+v with %+v", second.PreviousReceipt, second.DesiredRecord)
	}
	stored, err := st.RouteOperation(ctx, "org", "op_second")
	if err != nil || !stored.Automatic || stored.PreviousAddress != route.Binding.Address || stored.PreviousReceipt.ID != "rec_1b" || stored.Principal != system {
		t.Fatalf("stored %+v %v", stored, err)
	}

	// A running one, or one whose write has no answer, holds the route.
	exec(`UPDATE loomplane_route_operations SET status='running',body=jsonb_set(body,'{status}','"running"') WHERE id='op_second'`)
	if err = st.AcceptRoute(ctx, address("op_third")); !errors.Is(err, ErrBusy) {
		t.Fatalf("accepted over a running change: %v", err)
	}
	exec(`UPDATE loomplane_route_operations SET status='waiting',body=jsonb_set(jsonb_set(body,'{status}','"waiting"'),'{dns_attempted}','true') WHERE id='op_second'`)
	if err = st.AcceptRoute(ctx, address("op_third")); !errors.Is(err, ErrBusy) {
		t.Fatalf("accepted over an unanswered write: %v", err)
	}
	if s := status("op_second"); s != "waiting" {
		t.Fatalf("second is %s", s)
	}

	// A handoff goes ahead of an address change that is only waiting, and
	// waits for one that is running.
	// One that failed with its write unanswered still holds the route;
	// a retry reconciles that write first.
	exec(`UPDATE loomplane_route_operations SET status='failed',body=jsonb_set(body,'{status}','"failed"') WHERE id='op_second'`)
	if err = st.AcceptRoute(ctx, address("op_waiting")); !errors.Is(err, ErrBusy) {
		t.Fatalf("accepted over a failed unanswered write: %v", err)
	}
	exec(`UPDATE loomplane_route_operations SET status='cancelled',body=jsonb_set(body,'{status}','"cancelled"') WHERE id='op_second'`)
	if err = st.AcceptRoute(ctx, address("op_waiting")); err != nil {
		t.Fatal(err)
	}
	exec(`UPDATE loomplane_route_operations SET status='running',body=jsonb_set(body,'{status}','"running"') WHERE id='op_waiting'`)
	source := route
	prepare := func() *RouteOperation {
		return &RouteOperation{ID: "op_prepare", Kind: "prepare", Status: "pending", Attempt: 1, Revision: 1, Route: route, Source: &source, Principal: user}
	}
	if err = st.AcceptRoute(ctx, prepare()); !errors.Is(err, ErrBusy) {
		t.Fatalf("handoff over a running address change: %v", err)
	}
	exec(`UPDATE loomplane_route_operations SET status='waiting',body=jsonb_set(body,'{status}','"waiting"') WHERE id='op_waiting'`)
	if err = st.AcceptRoute(ctx, prepare()); err != nil {
		t.Fatal(err)
	}
	if s := status("op_waiting"); s != "cancelled" {
		t.Fatalf("waiting address change is %s", s)
	}
	if s := status("op_prepare"); s != "pending" {
		t.Fatalf("prepare is %s", s)
	}
}

func TestDDNSNodesBehindAddressQuery(t *testing.T) {
	pg := routeBindingDatabase(t)
	ctx := t.Context()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := pg.Exec(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	ns := nodes.NewPgStore(pg)
	cs := connections.NewPgStore(pg)
	add := func(id, advertise string, offered []string) {
		t.Helper()
		n := &nodes.Node{ID: id, OrgID: "org", Name: id, PublicKey: []byte(id), KeyFingerprint: "fp-" + id, IdentityGeneration: 1, Status: nodes.StatusOnline,
			Endpoints: []nodeproto.Endpoint{nodeproto.EndpointDocker}, IngressMode: "none", Role: "node", Labels: map[string]string{}, Version: "1.0.0", ProtocolVersion: 1,
			Inventory: nodeproto.Inventory{OS: "linux", PublicAddresses: offered}, JoinedBy: "usr", JoinedByKeyID: "key", CreatedAt: now, UpdatedAt: now}
		if err := ns.Create(ctx, n); err != nil {
			t.Fatal(err)
		}
		if _, err := ns.SetIngress(ctx, id, nodes.IngressSetting{Mode: "direct", PublicAddress: "203.0.113.7", ConfirmedAt: &now, SentAt: now}); err != nil {
			t.Fatal(err)
		}
		if err := cs.CreateConnection(ctx, &connections.Connection{ID: "conn_" + id, OrgID: "org", Cloud: connections.Server, Method: connections.MethodNode, Name: id, Status: connections.StatusReady,
			Details: map[string]string{"node_id": id, "advertise_host": advertise, "docker_id": "daemon-" + id}, CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	settled := []string{"203.0.113.7"}
	add("node_settled", "203.0.113.7", settled)
	add("node_half", "198.51.100.9", []string{"198.51.100.9"})
	add("node_moved", "203.0.113.7", []string{"198.51.100.9"})

	// Settled nodes whose routes are, or aren't, behind.
	exec(`INSERT INTO loomplane_projects(id,org_id,name,slug,status,created_by,created_at,updated_at) VALUES('shop','org','Shop','shop','ready','usr',now(),now())`)
	exec(`INSERT INTO loomplane_environments(id,org_id,project_id,name,kind,created_at,updated_at) VALUES('prod','org','shop','production','production',now(),now())`)
	exec(`INSERT INTO loomplane_dns_zones(id,org_id,name,connection_id,revision,body) VALUES('zone','org','example.com','',1,'{"id":"zone","org_id":"org","name":"example.com","mode":"external"}')`)
	if err := cs.CreateConnection(ctx, &connections.Connection{ID: "runtime", OrgID: "org", Cloud: "server", Name: "Runtime", Status: connections.StatusReady, Details: map[string]string{"host": "192.0.2.42", "docker_id": "daemon"}, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := cloudprofiles.NewPgStore(pg).CreateProfile(ctx, &cloudprofiles.Profile{ID: "cloud_1", OrgID: "org", ProjectID: "shop", Name: "cloud_1", Revision: 1, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	st := NewPgStore(pg)
	if err := st.SaveServer(ctx, &Server{ID: "srv_1", OrgID: "org", CloudID: "cloud_1", Name: "web-1", Ownership: "adopted", Provider: "server", AccountID: "daemon", ResourceID: "daemon", ConnectionID: "runtime", RuntimeConnectionID: "runtime", Revision: 1, Status: "ready"}); err != nil {
		t.Fatal(err)
	}
	route := func(node, status, address string) string {
		t.Helper()
		add(node, "203.0.113.7", settled)
		id := "route_" + node
		raw := fmt.Sprintf(`{"id":%q,"org_id":"org","project_id":"shop","status":%q,"binding":{"connection_id":"conn_%s","server_id":"srv_1","address":%q}}`, id, status, node, address)
		exec(`INSERT INTO loomplane_domain_routes(id,org_id,project_id,environment_id,cloud_id,server_id,connection_id,zone_id,hostname,status,revision,body,backend) VALUES($1,'org','shop','prod','cloud_1','srv_1',$2,'zone',$3,$4,1,$5,'server')`,
			id, "conn_"+node, node+".example.com", status, raw)
		return id
	}
	operation := func(routeID, node, kind, status string) {
		t.Helper()
		body := fmt.Sprintf(`{"kind":%q,"status":%q,"route":{"id":%q,"binding":{"connection_id":"conn_%s"}}}`, kind, status, routeID, node)
		exec(`INSERT INTO loomplane_route_operations(id,org_id,project_id,route_id,server_id,cloud_id,status,revision,body,principal) VALUES($1,'org','shop',$2,'srv_1','cloud_1',$3,1,$4::jsonb,'{}')`, "op_"+routeID, routeID, status, body)
	}
	route("node_r1", "ready", "192.0.2.1")                                           // on its old address: behind
	operation(route("node_r2", "ready", "192.0.2.1"), "node_r2", "verify", "failed") // a failed operation waits for a person
	route("node_r3", RouteRoutingPending, "203.0.113.7")                             // routing pending, nothing moving it: behind
	operation(route("node_r4", RouteRoutingPending, "203.0.113.7"), "node_r4", "address", "waiting")
	handed := route("node_r5", "ready", "192.0.2.1") // a handoff holds it
	exec(`INSERT INTO loomplane_route_handoffs(id,org_id,route_id,source_connection_id,destination_connection_id) VALUES('handoff_1','org',$1,'conn_node_r5','conn_node_r5')`, handed)
	route("node_r6", "ready", "203.0.113.7")                                             // on the node's address
	operation(route("node_r7", "ready", "192.0.2.1"), "node_r7", "address", "succeeded") // a finished operation doesn't count

	// Nodes without a confirmed address: only one with public addresses
	// and a ready server on its connection has its address checked.
	unconfirmed := func(id string, offered []string, serverStatus string) {
		t.Helper()
		n := &nodes.Node{ID: id, OrgID: "org", Name: id, PublicKey: []byte(id), KeyFingerprint: "fp-" + id, IdentityGeneration: 1, Status: nodes.StatusOnline,
			Endpoints: []nodeproto.Endpoint{nodeproto.EndpointDocker}, IngressMode: "none", Role: "node", Labels: map[string]string{}, Version: "1.0.0", ProtocolVersion: 1,
			Inventory: nodeproto.Inventory{OS: "linux", PublicAddresses: offered}, JoinedBy: "usr", JoinedByKeyID: "key", CreatedAt: now, UpdatedAt: now}
		if err := ns.Create(ctx, n); err != nil {
			t.Fatal(err)
		}
		if err := cs.CreateConnection(ctx, &connections.Connection{ID: "conn_" + id, OrgID: "org", Cloud: connections.Server, Method: connections.MethodNode, Name: id, Status: connections.StatusReady,
			Details: map[string]string{"node_id": id, "docker_id": "daemon-" + id}, CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
		if serverStatus != "" {
			if err := st.SaveServer(ctx, &Server{ID: "srv_" + id, OrgID: "org", CloudID: "cloud_1", Name: id, Ownership: "adopted", Provider: "server", AccountID: "daemon-" + id, ResourceID: "daemon-" + id, ConnectionID: "conn_" + id, RuntimeConnectionID: "conn_" + id, Revision: 1, Status: serverStatus}); err != nil {
				t.Fatal(err)
			}
		}
	}
	unconfirmed("node_u1", []string{"203.0.113.9"}, "ready") // its first check found nothing, or it was adopted by review
	unconfirmed("node_u2", []string{"203.0.113.9"}, "")      // no server on its connection
	unconfirmed("node_u3", []string{"203.0.113.9"}, "adopting")
	unconfirmed("node_u4", nil, "ready") // nothing to check
	// An owner who put a confirmed node in mode none keeps it there.
	unconfirmed("node_u5", []string{"203.0.113.9"}, "ready")
	if _, err := ns.SetIngress(ctx, "node_u5", nodes.IngressSetting{Mode: "direct", PublicAddress: "203.0.113.9", ConfirmedAt: &now, SentAt: now}); err != nil {
		t.Fatal(err)
	}
	none := "none"
	if _, err := ns.UpdateSettings(ctx, "org", "node_u5", nodes.SettingsUpdate{IngressMode: &none}); err != nil {
		t.Fatal(err)
	}

	list := func(after string, limit int) []string {
		t.Helper()
		behind, err := st.NodesBehindAddress(ctx, after, limit)
		if err != nil {
			t.Fatal(err)
		}
		ids := make([]string, 0, len(behind))
		for _, n := range behind {
			if n.OrgID != "org" {
				t.Fatalf("node %+v", n)
			}
			ids = append(ids, n.ID)
		}
		return ids
	}
	if got := list("", 10); !slices.Equal(got, []string{"node_half", "node_moved", "node_r1", "node_r3", "node_r7", "node_u1"}) {
		t.Fatalf("behind %v", got)
	}
	// A pass carries on after the last node the one before it reached.
	if got := list("", 2); !slices.Equal(got, []string{"node_half", "node_moved"}) {
		t.Fatalf("first pass %v", got)
	}
	if got := list("node_moved", 2); !slices.Equal(got, []string{"node_r1", "node_r3"}) {
		t.Fatalf("next pass %v", got)
	}
}
