//go:build integration

package cloudinfra

import (
	"errors"
	"testing"
	"time"

	"github.com/xraph/ctrlplane/id"

	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/security"
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
}
