//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/clouds/server"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/provider"
)

type domainAuth struct{}

func (domainAuth) Require(_ context.Context, p security.Principal, _ security.Permission, scope string) error {
	if p.OrgID == "org" && p.UserID == "manager" && (scope == "org" || scope == "proj") {
		return nil
	}
	return security.ErrForbidden
}

type domainDNS struct{ proof string }

func (r *domainDNS) LookupTXT(context.Context, string) ([]string, error) {
	return []string{r.proof}, nil
}
func (*domainDNS) LookupIPAddr(context.Context, string) ([]net.IPAddr, error) { return nil, nil }

type domainWorkflow struct {
	infraWorkflow
	routes int
}

func (w *domainWorkflow) StartRoute(context.Context, string, string, int) error {
	w.routes++
	return nil
}

func TestDomainsDurableClaimsScopeAndServerGuards(t *testing.T) {
	domainsDurableClaims(t, 60, 60)
}

func TestDomainsCompensationTrustedReceiptDeadline(t *testing.T) {
	for _, tt := range []struct {
		name              string
		previous, current int
	}{{"previous-longer", 90000, 60}, {"current-longer", 60, 90000}} {
		t.Run(tt.name, func(t *testing.T) { domainsDurableClaims(t, tt.previous, tt.current) })
	}
}

func domainsDurableClaims(t *testing.T, cutoverTTL, rollbackTTL int, modes ...string) {
	t.Helper()
	clouds, sql, db := cloudSyncDatabase(t)
	ctx := t.Context()
	store := cloudinfra.NewPgStore(db)
	cs := connections.NewPgStore(db)
	now := time.Now().UTC()
	conn := &connections.Connection{ID: "runtime", OrgID: "org", Cloud: "server", Name: "Runtime", Status: "ready", Details: map[string]string{"host": "192.0.2.42", "docker_id": "daemon"}, CreatedAt: now, UpdatedAt: now}
	if err := cs.CreateConnection(ctx, conn); err != nil {
		t.Fatal(err)
	}
	host := &cloudinfra.Server{ID: "host", OrgID: "org", CloudID: "cloud", Name: "Host", Ownership: "adopted", Provider: "server", AccountID: "daemon", ResourceID: "daemon", ConnectionID: "runtime", RuntimeConnectionID: "runtime", Revision: 1, Status: "ready"}
	if err := store.SaveServer(ctx, host); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`CREATE TABLE cp_instances(id TEXT PRIMARY KEY,tenant_id TEXT,current_release TEXT,service_refs JSONB,services JSONB,labels JSONB,state TEXT)`,
		`CREATE TABLE cp_deployments(id TEXT PRIMARY KEY,tenant_id TEXT,instance_id TEXT,release_id TEXT,state TEXT,created_at TIMESTAMPTZ DEFAULT now())`,
		`INSERT INTO cp_instances VALUES('instance','org',NULL,'{"main":"container"}','[{"name":"main","ports":[{"container":8080}]}]','{"loomplane.revision":"deployment/action"}','running')`,
		`UPDATE loomplane_cloud_targets SET provider='server',connection_id='runtime',revision=revision+1 WHERE id='target'`,
		`INSERT INTO loomplane_bindings(id,org_id,project_id,environment_id,subject,name,provider,engine,external_id,connection_id,config,created_at,updated_at) VALUES('binding','org','proj','env','service','web','server','http','instance','runtime','{"cloud_placement":{"cloud_id":"cloud","target_id":"target","server_id":"host"},"port":8080}',now(),now())`,
	} {
		if _, err := sql.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	resolver, workflow := &domainDNS{}, &domainWorkflow{}
	adapters := cloudinfra.NewAdapters()
	if err := adapters.RegisterDNS("vercel", &controlDNSAdapter{}); err != nil {
		t.Fatal(err)
	}
	svc := cloudinfra.NewService(cloudinfra.Deps{Store: store, Clouds: clouds, Authz: domainAuth{}, Resolver: resolver, Workflow: workflow, Adapters: adapters, Connections: &syncAccounts{store: cs}})
	p := security.Principal{OrgID: "org", UserID: "manager"}
	mode, dnsConnection := "external", ""
	if len(modes) > 0 {
		mode, dnsConnection = modes[0], "old"
	}
	zone, err := svc.CreateZone(ctx, p, cloudinfra.ZoneInput{Name: "Example.COM.", Mode: mode, ConnectionID: dnsConnection, ProjectIDs: []string{"proj"}})
	if err != nil {
		t.Fatal(err)
	}
	if zone.Name != "example.com" || zone.ConnectionID != dnsConnection || !zone.VerifiedAt.IsZero() {
		t.Fatal("unexpected external zone", zone)
	}
	input := cloudinfra.RouteInput{ZoneID: zone.ID, CloudID: "cloud", EnvironmentID: "env", Service: "web", Hostname: "APP.EXAMPLE.COM.", Port: 8080}
	if _, err = svc.PreviewRoute(ctx, p, "proj", input); !errors.Is(err, cloudinfra.ErrNotFound) {
		t.Fatal("unverified control accepted", err)
	}
	if mode == "managed" {
		// A persisted hosting-only timestamp from an old version grants no control.
		legacy := *zone
		legacy.VerifiedAt, legacy.ProofName, legacy.ProofValue = now, "", ""
		if err = store.SaveZone(ctx, &legacy); err != nil {
			t.Fatal(err)
		}
		if _, err = svc.PreviewRoute(ctx, p, "proj", input); err == nil {
			t.Fatal("legacy hosting verification authorized route")
		}
		if _, err = svc.VerifyZone(ctx, p, zone.ID); err == nil {
			t.Fatal("legacy hosting verification renewed without public proof")
		}
		// Every check is saved, so the renewal goes on the zone's new revision.
		checked, checkErr := store.Zone(ctx, "org", zone.ID)
		if checkErr != nil || checked.ProofCheckedAt.IsZero() {
			t.Fatal("failed check not recorded", checked, checkErr)
		}
		renewed, renewErr := svc.UpdateZone(ctx, p, zone.ID, cloudinfra.ZoneUpdate{Revision: checked.Revision, ProjectIDs: legacy.ProjectIDs, RenewProof: true})
		if renewErr != nil || !renewed.VerifiedAt.IsZero() || renewed.ProofName == "" {
			t.Fatalf("legacy challenge renewal: %+v %v", renewed, renewErr)
		}
		zone = renewed
	}
	resolver.proof = "foreign-organization-proof"
	if _, err = svc.VerifyZone(ctx, p, zone.ID); err == nil {
		t.Fatal("foreign public proof accepted")
	}
	resolver.proof = zone.ProofValue
	if zone, err = svc.VerifyZone(ctx, p, zone.ID); err != nil {
		t.Fatal(err)
	}
	preview, err := svc.PreviewRoute(ctx, p, "proj", input)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Route.Input.Hostname != "app.example.com" || preview.DesiredRecord.Name != "app" || preview.DesiredRecord.Value != "192.0.2.42" {
		t.Fatal("incorrect external instructions", preview)
	}

	// Loomplane Create has no controller release or deployment row. Its original
	// service reference and revision label are the persisted runtime identity.
	if preview.Route.Binding.ReleaseID != "deployment/action" || preview.Route.Binding.ContainerID != "container" {
		t.Fatal("initial Create shape rejected", preview.Route.Binding)
	}
	for _, test := range []struct {
		name, state, receipt, revision, ref string
		valid                               bool
	}{
		{"successful update", "succeeded", "updated", "updated", "new-container", true},
		{"in progress", "running", "updated", "updated", "new-container", false},
		{"failed", "failed", "updated", "updated", "new-container", false},
		{"stale receipt", "succeeded", "old", "updated", "new-container", false},
		{"mismatched revision", "succeeded", "updated", "old", "new-container", false},
		{"legacy missing receipt", "succeeded", "", "updated", "", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, statementErr := sql.Exec(ctx, `INSERT INTO cp_deployments(id,tenant_id,instance_id,release_id,state,created_at) VALUES('older','org','instance','updated','succeeded',now()-interval '1 second'),('foreign','foreign','instance','updated','succeeded',now()+interval '1 second'),('other-instance','org','unrelated','updated','succeeded',now()+interval '1 second')`); statementErr != nil {
				t.Fatal(statementErr)
			}
			if _, statementErr := sql.Exec(ctx, `INSERT INTO cp_deployments(id,tenant_id,instance_id,release_id,state) VALUES('update','org','instance','updated',$1)`, test.state); statementErr != nil {
				t.Fatal(statementErr)
			}
			labels, _ := json.Marshal(map[string]string{"loomplane.revision": test.revision, "loomplane.runtime.receipt.release": test.receipt, "loomplane.runtime.receipt.service.main": test.ref})
			if _, statementErr := sql.Exec(ctx, `UPDATE cp_instances SET labels=$1 WHERE id='instance'`, labels); statementErr != nil {
				t.Fatal(statementErr)
			}
			binding, resolveErr := store.ResolveRouteBinding(ctx, "org", "proj", input)
			if test.valid {
				if resolveErr != nil || binding.ReleaseID != "updated" || binding.ContainerID != "new-container" {
					t.Fatalf("updated receipt: %+v %v", binding, resolveErr)
				}
			} else if !errors.Is(resolveErr, cloudinfra.ErrConflict) {
				t.Fatalf("unaccepted runtime identity: %+v %v", binding, resolveErr)
			}
			if resolveErr = store.ValidateRetainedRoute(ctx, preview.Route); resolveErr == nil {
				t.Fatal("old runtime identity accepted after update")
			}
			if _, resolveErr = sql.Exec(ctx, `DELETE FROM cp_deployments`); resolveErr != nil {
				t.Fatal(resolveErr)
			}
		})
	}
	if _, err = sql.Exec(ctx, `UPDATE cp_instances SET labels='{"loomplane.revision":"deployment/action"}' WHERE id='instance'`); err != nil {
		t.Fatal(err)
	}

	resolver.proof = "changed-proof"
	if _, err = svc.ApplyRoute(ctx, p, "proj", preview.ID, cloudinfra.ApplyInput{Review: preview.Review}); err == nil {
		t.Fatal("changed proof accepted")
	}
	if _, err = svc.PreviewRoute(ctx, p, "proj", input); err == nil {
		t.Fatal("changed proof allowed new preview")
	}
	var claims int
	if err = sql.QueryRow(ctx, `SELECT count(*) FROM loomplane_domain_routes`).Scan(&claims); err != nil || claims != 0 || workflow.routes != 0 {
		t.Fatalf("unproved ownership reached claims/effects: %d %d %v", claims, workflow.routes, err)
	}
	resolver.proof = zone.ProofValue
	accepted, err := svc.ApplyRoute(ctx, p, "proj", preview.ID, cloudinfra.ApplyInput{Review: preview.Review})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.ApplyRoute(ctx, p, "proj", preview.ID, cloudinfra.ApplyInput{Review: preview.Review}); err != nil || workflow.routes != 1 {
		t.Fatalf("duplicate acceptance queued another operation: %v %d", err, workflow.routes)
	}
	second, err := svc.PreviewRoute(ctx, p, "proj", input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.ApplyRoute(ctx, p, "proj", second.ID, cloudinfra.ApplyInput{Review: second.Review}); !errors.Is(err, cloudinfra.ErrConflict) {
		t.Fatal("duplicate global hostname accepted", err)
	}
	// A valid route row in a different organization still contends on the
	// same exact hostname, after unproved previews reserved nothing above.
	for _, statement := range []string{
		`INSERT INTO loomplane_projects(id,org_id,name,slug,status,created_by,created_at,updated_at) VALUES('other-project','other-org','Other','other','ready','manager',now(),now())`,
		`INSERT INTO loomplane_environments(id,org_id,project_id,name,kind,created_at,updated_at) VALUES('other-env','other-org','other-project','production','production',now(),now())`,
		`INSERT INTO loomplane_cloud_profiles(id,org_id,project_id,name,revision,created_at,updated_at) VALUES('other-cloud','other-org','other-project','Other',1,now(),now())`,
		`INSERT INTO loomplane_cloud_connections(id,org_id,cloud,name,status,details,created_by,created_at,updated_at) VALUES('other-runtime','other-org','server','Other','ready','{}','manager',now(),now())`,
		`INSERT INTO loomplane_cloud_servers(id,org_id,cloud_id,connection_id,provider,account_id,ownership,revision,status,body) VALUES('other-host','other-org','other-cloud','other-runtime','server','other-daemon','adopted',1,'ready','{}')`,
		`INSERT INTO loomplane_dns_zones(id,org_id,name,revision,body) VALUES('other-zone','other-org','example.com',1,'{}')`,
		`INSERT INTO loomplane_domain_routes(id,org_id,project_id,environment_id,cloud_id,server_id,connection_id,zone_id,hostname,status,revision,body) VALUES('other-route','other-org','other-project','other-env','other-cloud','other-host','other-runtime','other-zone','other.example.com','ready',1,'{}')`,
	} {
		if _, fixtureErr := sql.Exec(ctx, statement); fixtureErr != nil {
			t.Fatal(fixtureErr)
		}
	}
	_, claimErr := sql.Exec(ctx, `UPDATE loomplane_domain_routes SET hostname='app.example.com' WHERE id='other-route'`)
	var constraint *pgconn.PgError
	if !errors.As(claimErr, &constraint) || constraint.ConstraintName != "loomplane_hostname_claim" {
		t.Fatalf("cross-organization claim was not fenced: %v", claimErr)
	}
	if _, err = store.Zone(ctx, "foreign-org", zone.ID); !errors.Is(err, cloudinfra.ErrNotFound) {
		t.Fatal("foreign zone disclosed", err)
	}
	if _, err = svc.GetRouteOperation(ctx, security.Principal{OrgID: "foreign-org", UserID: "manager"}, "proj", accepted.ID); !errors.Is(err, security.ErrForbidden) {
		t.Fatal("foreign operation disclosed", err)
	}
	if _, err = svc.GetRouteOperation(ctx, p, "other-project", accepted.ID); !errors.Is(err, security.ErrForbidden) {
		t.Fatal("other project's operation disclosed", err)
	}
	used, err := store.ServerInUse(ctx, host)
	if err != nil || !used {
		t.Fatalf("pending route did not retain runtime: %v %v", used, err)
	}
	if err = store.RetireServer(ctx, host); !errors.Is(err, cloudinfra.ErrConflict) {
		t.Fatal("server retired under accepted route", err)
	}
	if _, err = sql.Exec(ctx, `DELETE FROM loomplane_dns_zones WHERE id=$1`, zone.ID); err == nil {
		t.Fatal("zone deleted under live claim")
	}
	if _, err = sql.Exec(ctx, `DELETE FROM loomplane_projects WHERE id='proj'`); err == nil {
		t.Fatal("project deletion silently orphaned route")
	}
	state1, err := store.BeginIngress(ctx, accepted)
	if err != nil {
		t.Fatal(err)
	}
	stale := *accepted
	state2, err := store.BeginIngress(ctx, accepted)
	if err != nil {
		t.Fatal(err)
	}
	if state2.Generation <= state1.Generation || store.SaveIngress(ctx, &stale, state1) == nil {
		t.Fatal("stale ingress worker published config")
	}
	accepted.Route.Status = "waiting"
	if err = store.PublishRoute(ctx, accepted); err != nil {
		t.Fatal(err)
	}
	recovered, err := store.RouteOperation(ctx, "org", accepted.ID)
	if err != nil || recovered.Route.Revision != accepted.Route.Revision {
		t.Fatalf("publication did not atomically checkpoint route revision: %v", err)
	}
	if _, err = sql.Exec(ctx, `UPDATE loomplane_route_operations SET body=jsonb_set(body,'{route,input,hostname}','"attacker.example.com"') WHERE id=$1`, accepted.ID); err == nil {
		t.Fatal("accepted hostname intent was mutable")
	}
	public, err := json.Marshal(recovered)
	if err != nil || string(public) == "" {
		t.Fatal(err)
	}
	var exposed map[string]any
	if err = json.Unmarshal(public, &exposed); err != nil {
		t.Fatal(err)
	}
	binding := exposed["route"].(map[string]any)["binding"].(map[string]any)
	if _, present := binding["config"]; present {
		t.Fatal("route API exposed the applied config snapshot")
	}

	// The handoff API accepts only the now-current binding and keeps the old
	// route independently. These fixtures exercise persistence, not proxy I/O.
	accepted.Route.Status = "ready"
	accepted.Receipt = &provider.DNSRecord{ID: "source-receipt", Name: "app", Type: "A", Value: "192.0.2.42", TTL: 300}
	if err = store.PublishRoute(ctx, accepted); err != nil {
		t.Fatal(err)
	}
	accepted.Status = "succeeded"
	if err = store.SaveRouteOperation(ctx, accepted); err != nil {
		t.Fatal(err)
	}
	conn2 := *conn
	conn2.ID = "runtime2"
	conn2.Name = "Destination"
	conn2.Details = map[string]string{"host": "192.0.2.43", "docker_id": "daemon2"}
	if err = cs.CreateConnection(ctx, &conn2); err != nil {
		t.Fatal(err)
	}
	host2 := *host
	host2.ID = "host2"
	host2.ConnectionID = "runtime2"
	host2.RuntimeConnectionID = "runtime2"
	host2.ResourceID = "daemon2"
	if err = store.SaveServer(ctx, &host2); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`INSERT INTO cp_instances VALUES('destination','org',NULL,'{"main":"destination-container"}','[{"name":"main","ports":[{"container":8080}]}]','{"loomplane.revision":"deployment/action"}','running')`,
		`UPDATE loomplane_bindings SET external_id='destination',connection_id='runtime2',config=jsonb_set(config,'{cloud_placement,server_id}','"host2"') WHERE id='binding'`,
	} {
		if _, err = sql.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	prepared, err := svc.PrepareRouteHandoff(ctx, p, "proj", accepted.Route.ID, cloudinfra.HandoffInput{Revision: accepted.Route.Revision, DestinationCloudID: "cloud"})
	if err != nil {
		t.Fatal(err)
	}
	history, historyErr := svc.RouteHistory(ctx, p, "proj", accepted.Route.ID)
	if historyErr != nil || len(history) == 0 || history[0].ID != prepared.ID {
		t.Fatal("lost response could not recover accepted handoff", historyErr)
	}
	current, err := store.Route(ctx, "org", "proj", accepted.Route.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Binding.ServerID != "host" || prepared.Route.Binding.ServerID != "host2" || prepared.Source.Binding.ServerID != "host" {
		t.Fatal("preparation changed source claim")
	}
	if _, err = svc.RemoveRoute(ctx, p, "proj", current.ID, current.Revision); !errors.Is(err, cloudinfra.ErrConflict) {
		t.Fatal("removal discarded pending source handoff", err)
	}
	prepared.Status = "succeeded"
	prepared.AppliedConfig = &server.IngressSnapshot{ContainerID: "proxy", ETag: "prepared"}
	prepared.DesiredConfig = &server.IngressConfig{OrgID: "org", ServerID: "host2", Version: prepared.ID}
	if err = store.SaveRouteOperation(ctx, prepared); err != nil {
		t.Fatal(err)
	}
	cutover, err := svc.CutoverRoute(ctx, p, "proj", prepared.ID)
	if err != nil {
		t.Fatal(err)
	}

	// Failure before DNS must retain the source receipt through rollback.
	cutover.Status = "failed"
	if err = store.SaveRouteOperation(ctx, cutover); err != nil {
		t.Fatal(err)
	}
	preDNSRollback, preDNSErr := svc.RollbackRoute(ctx, p, "proj", cutover.ID)
	if preDNSErr != nil {
		t.Fatal(preDNSErr)
	}
	if preDNSRollback.PreviousReceipt == nil || preDNSRollback.PreviousReceipt.ID != "source-receipt" || preDNSRollback.DesiredRecord.ID != "source-receipt" || preDNSRollback.DesiredRecord.TTL != 300 {
		t.Fatal("pre-DNS rollback lost source receipt")
	}
	// Explicit completed fixture, as with proxy snapshots in this database test.
	preDNSRollback.Status = "succeeded"
	if err = store.SaveRouteOperation(ctx, preDNSRollback); err != nil {
		t.Fatal(err)
	}
	cutover, err = svc.CutoverRoute(ctx, p, "proj", prepared.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.BeginIngress(ctx, cutover); err != nil {
		t.Fatal(err)
	}
	cutover.Route.Status = "ready"
	cutover.Receipt = &provider.DNSRecord{ID: "rotated-receipt", Name: "app", Type: "A", Value: "192.0.2.43", TTL: cutoverTTL}
	if err = store.PublishRoute(ctx, cutover); err != nil {
		t.Fatal(err)
	}
	cutover.Status = "succeeded"
	if err = store.SaveRouteOperation(ctx, cutover); err != nil {
		t.Fatal(err)
	}
	if used, err = store.ServerInUse(ctx, host); err != nil || !used {
		t.Fatal("cutover discarded retained source", err)
	}

	if _, err = sql.Exec(ctx, `UPDATE loomplane_cloud_project_grants SET revoked=true,revision=revision+1 WHERE org_id='org' AND cloud_id='cloud' AND project_id='proj'`); err != nil {
		t.Fatal(err)
	}
	if err = store.ValidateRouteCleanup(ctx, cutover.Route); err != nil {
		t.Fatal("revoked cutover denied exact cleanup", err)
	}
	if _, err = svc.RollbackRoute(ctx, p, "proj", cutover.ID); err == nil {
		t.Fatal("revoked cutover authorized rollback")
	}
	revokeRelease, revokeErr := svc.ReleaseRouteHandoff(ctx, p, "proj", prepared.ID, 60)
	if revokeErr != nil {
		t.Fatal("revoked cutover denied release", revokeErr)
	}
	if revokeRelease.Source.Binding.ServerID != "host" || revokeRelease.Route.Binding.ServerID != "host2" {
		t.Fatal("release targeted active destination")
	}
	// Cancel this unexecuted fixture so the rollback branch can also be covered.
	revokeRelease.Status = "cancelled"
	if err = store.SaveRouteOperation(ctx, revokeRelease); err != nil {
		t.Fatal(err)
	}
	if _, err = sql.Exec(ctx, `UPDATE loomplane_cloud_project_grants SET revoked=false,revision=revision+1 WHERE org_id='org' AND cloud_id='cloud' AND project_id='proj'`); err != nil {
		t.Fatal(err)
	}
	rolled, err := svc.RollbackRoute(ctx, p, "proj", cutover.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rolled.Route.Binding.ServerID != "host" {
		t.Fatal("rollback lost original destination")
	}
	if _, err = store.BeginIngress(ctx, rolled); err != nil {
		t.Fatal(err)
	}
	rolled.Route.Status = "ready"
	if rolled.PreviousReceipt == nil || rolled.PreviousReceipt.ID != "rotated-receipt" {
		t.Fatal("rollback lost latest DNS receipt")
	}
	rolled.Receipt = &provider.DNSRecord{ID: "rollback-receipt", Name: "app", Type: "A", Value: "192.0.2.42", TTL: rollbackTTL}
	if err = store.PublishRoute(ctx, rolled); err != nil {
		t.Fatal(err)
	}
	rolled.Status = "succeeded"
	if err = store.SaveRouteOperation(ctx, rolled); err != nil {
		t.Fatal(err)
	}

	resolver.proof = "lost-control"
	if _, err = svc.PreviewRoute(ctx, p, "proj", input); err == nil {
		t.Fatal("lost proof admitted new work")
	}
	if err = store.ValidateRouteCleanup(ctx, rolled.Route); err != nil {
		t.Fatal("lost proof blocked exact cleanup", err)
	}
	resolver.proof = zone.ProofValue
	// Revocation and credential rotation do not grant fresh route work, but exact
	// retained cleanup is still authorized for the active runtime.
	if _, err = sql.Exec(ctx, `UPDATE loomplane_cloud_project_grants SET revoked=true,revision=revision+1 WHERE org_id='org' AND cloud_id='cloud' AND project_id='proj'`); err != nil {
		t.Fatal(err)
	}
	if _, err = sql.Exec(ctx, `UPDATE loomplane_cloud_connections SET generation=generation+1 WHERE id='runtime'`); err != nil {
		t.Fatal(err)
	}
	if err = store.ValidateRetainedRoute(ctx, rolled.Route); err == nil {
		t.Fatal("revocation authorized new rollback")
	}
	if err = store.ValidateRouteCleanup(ctx, rolled.Route); err != nil {
		t.Fatal("retained cleanup denied after revoke/rotate", err)
	}
	if _, err = sql.Exec(ctx, `UPDATE loomplane_cloud_connections SET details=jsonb_set(details,'{docker_id}','"replacement"') WHERE id='runtime'`); err != nil {
		t.Fatal(err)
	}
	if err = store.ValidateRouteCleanup(ctx, rolled.Route); err == nil {
		t.Fatal("cleanup accepted replaced runtime")
	}
	if _, err = sql.Exec(ctx, `UPDATE loomplane_cloud_connections SET details=jsonb_set(details,'{docker_id}','"daemon"') WHERE id='runtime'`); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.RollbackRoute(ctx, p, "proj", cutover.ID); err == nil {
		t.Fatal("revoked grant authorized rollback")
	}
	if cutoverTTL > 86400 || rollbackTTL > 86400 {
		// Later operations from other handoffs must not hide the accepted maximum.
		if _, fixtureErr := sql.Exec(ctx, `INSERT INTO loomplane_route_operations(id,org_id,project_id,route_id,server_id,cloud_id,status,revision,body,principal,checkpoint)
 SELECT 'history-'||n,o.org_id,o.project_id,o.route_id,o.server_id,o.cloud_id,'succeeded',1,
 o.body||jsonb_build_object('id','history-'||n,'handoff_id','unrelated','status','succeeded','created_at',now()+interval '1 second','receipt',jsonb_build_object('ttl',180000)),o.principal,o.checkpoint
 FROM loomplane_route_operations o CROSS JOIN generate_series(1,105) n WHERE o.id=$1`, cutover.ID); fixtureErr != nil {
			t.Fatal(fixtureErr)
		}
		if _, fixtureErr := sql.Exec(ctx, `INSERT INTO loomplane_route_operations(id,org_id,project_id,route_id,server_id,cloud_id,status,revision,body,principal,checkpoint)
 SELECT 'unaccepted-receipt',org_id,project_id,route_id,server_id,cloud_id,'preview',1,
 body||jsonb_build_object('id','unaccepted-receipt','status','preview','receipt',jsonb_build_object('ttl',180000)),principal,checkpoint FROM loomplane_route_operations WHERE id=$1`, cutover.ID); fixtureErr != nil {
			t.Fatal(fixtureErr)
		}
		history, historyErr := store.RouteOperations(ctx, "org", "proj", cutover.Route.ID)
		if historyErr != nil || len(history) != 100 {
			t.Fatal("public history cap changed", len(history), historyErr)
		}
		for _, op := range history {
			if op.ID == cutover.ID {
				t.Fatal("fixture did not hide old cutover")
			}
		}
	}
	if _, invalidErr := svc.ReleaseRouteHandoff(ctx, p, "proj", prepared.ID, 90000); !errors.Is(invalidErr, cloudinfra.ErrInvalid) {
		t.Fatal("unbounded caller drain accepted", invalidErr)
	}
	released, err := svc.ReleaseRouteHandoff(ctx, p, "proj", prepared.ID, 60)
	if err != nil {
		t.Fatal(err)
	}
	if !released.NotBefore.Equal(rolled.Route.UpdatedAt.Add(time.Duration(max(300, cutoverTTL, rollbackTTL)) * time.Second)) {
		t.Fatal("prior source TTL was shortened", released.NotBefore)
	}
	persistedRelease, readErr := store.RouteOperation(ctx, "org", released.ID)
	if readErr != nil || !persistedRelease.NotBefore.Equal(released.NotBefore) {
		t.Fatal("trusted drain deadline was not persisted", readErr)
	}
	if released.Source.Binding.ServerID != "host2" || released.NotBefore.IsZero() {
		t.Fatal("release lost inactive identity or TTL gate")
	}
	if _, err = store.BeginIngress(ctx, released); err != nil {
		t.Fatal(err)
	}
	if err = store.CompleteHandoff(ctx, released); err != nil {
		t.Fatal(err)
	}
	released.Status = "succeeded"
	if err = store.SaveRouteOperation(ctx, released); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.CutoverRoute(ctx, p, "proj", prepared.ID); err == nil {
		t.Fatal("released handoff reused stale source authority")
	}

	var activeHolds, inactiveHolds int
	if err = sql.QueryRow(ctx, `SELECT count(*) FROM loomplane_connection_uses WHERE kind='domain_route' AND owner_id=$1`, rolled.Route.ID).Scan(&activeHolds); err != nil {
		t.Fatal(err)
	}
	if err = sql.QueryRow(ctx, `SELECT count(*) FROM loomplane_connection_uses WHERE kind='route_handoff' AND owner_id=$1`, prepared.ID).Scan(&inactiveHolds); err != nil {
		t.Fatal(err)
	}
	if activeHolds != 1 || inactiveHolds != 0 {
		t.Fatalf("cleanup changed active hold: %d %d", activeHolds, inactiveHolds)
	}
	if _, err = sql.Exec(ctx, `UPDATE loomplane_cloud_project_grants SET revoked=false,revision=revision+1 WHERE org_id='org' AND cloud_id='cloud' AND project_id='proj'`); err != nil {
		t.Fatal(err)
	}

	// A same-server candidate can be abandoned after either failed or successful
	// preparation, without restoring its Cloud grant or changing the source.
	if _, err = sql.Exec(ctx, `INSERT INTO cp_instances VALUES('same-server-candidate','org',NULL,'{"main":"candidate-container"}','[{"name":"main","ports":[{"container":8080}]}]','{"loomplane.revision":"deployment/action"}','running')`); err != nil {
		t.Fatal(err)
	}
	for _, prepareStatus := range []string{"failed", "succeeded"} {
		t.Run("abandon_same_server_"+prepareStatus, func(t *testing.T) {
			sourceInput := input
			sourceInput.Hostname = prepareStatus + ".example.com"
			previewSame, previewErr := svc.PreviewRoute(ctx, p, "proj", sourceInput)
			if previewErr != nil {
				t.Fatal(previewErr)
			}
			sourceOp, sourceErr := svc.ApplyRoute(ctx, p, "proj", previewSame.ID, cloudinfra.ApplyInput{Review: previewSame.Review})
			if sourceErr != nil {
				t.Fatal(sourceErr)
			}
			if _, err = store.BeginIngress(ctx, sourceOp); err != nil {
				t.Fatal(err)
			}
			sourceOp.Route.Status = "ready"
			if err = store.PublishRoute(ctx, sourceOp); err != nil {
				t.Fatal(err)
			}
			sourceOp.Status = "succeeded"
			if err = store.SaveRouteOperation(ctx, sourceOp); err != nil {
				t.Fatal(err)
			}
			if _, err = sql.Exec(ctx, `UPDATE loomplane_bindings SET external_id='same-server-candidate' WHERE id='binding'`); err != nil {
				t.Fatal(err)
			}
			prep, prepErr := svc.PrepareRouteHandoff(ctx, p, "proj", sourceOp.Route.ID, cloudinfra.HandoffInput{Revision: sourceOp.Route.Revision, DestinationCloudID: "cloud"})
			if prepErr != nil {
				t.Fatal(prepErr)
			}
			if prep.Route.Binding.ServerID != prep.Source.Binding.ServerID {
				t.Fatal("fixture is not same-server")
			}
			prep.Status = prepareStatus
			prep.DesiredConfig = &server.IngressConfig{OrgID: "org", ServerID: "host2", Version: prep.ID}
			if err = store.SaveRouteOperation(ctx, prep); err != nil {
				t.Fatal(err)
			}
			if _, err = sql.Exec(ctx, `UPDATE loomplane_cloud_project_grants SET revoked=true,revision=revision+1 WHERE org_id='org' AND cloud_id='cloud' AND project_id='proj'`); err != nil {
				t.Fatal(err)
			}
			abandoned, abandonErr := svc.ReleaseRouteHandoff(ctx, p, "proj", prep.ID, 60)
			if abandonErr != nil {
				t.Fatal(abandonErr)
			}
			if abandoned.Route.Binding.InstanceID != "destination" || abandoned.Source.Binding.InstanceID != "same-server-candidate" {
				t.Fatal("abandonment selected wrong active binding")
			}
			if err = store.ValidateRouteCleanup(ctx, abandoned.Route); err != nil {
				t.Fatal("revoked grant blocked cleanup", err)
			}
			// Checkpoint the cleanup-only CAS intent, then read it as a resumed worker.
			abandoned.DesiredConfig = &server.IngressConfig{OrgID: "org", ServerID: "host2", Version: abandoned.ID + "-abandoned"}
			abandoned.PreviousConfig = &server.IngressSnapshot{ContainerID: "proxy", ETag: "prepared"}
			if err = store.SaveRouteOperation(ctx, abandoned); err != nil {
				t.Fatal(err)
			}
			resumed, resumeErr := store.RouteOperation(ctx, "org", abandoned.ID)
			if resumeErr != nil {
				t.Fatal(resumeErr)
			}
			if resumed.DesiredConfig == nil || resumed.DesiredConfig.Version != abandoned.DesiredConfig.Version || resumed.PreviousConfig.ETag != "prepared" {
				t.Fatal("cleanup CAS intent was lost")
			}
			if _, err = store.BeginIngress(ctx, resumed); err != nil {
				t.Fatal(err)
			}
			if err = store.CompleteHandoff(ctx, resumed); err != nil {
				t.Fatal(err)
			}
			resumed.Status = "succeeded"
			if err = store.SaveRouteOperation(ctx, resumed); err != nil {
				t.Fatal(err)
			}
			stillActive, activeErr := store.Route(ctx, "org", "proj", sourceOp.Route.ID)
			if activeErr != nil || stillActive.Binding != sourceOp.Route.Binding {
				t.Fatal("cleanup published a different active binding", activeErr)
			}
			var holds int
			if err = sql.QueryRow(ctx, `SELECT count(*) FROM loomplane_connection_uses WHERE kind='route_handoff' AND owner_id=$1`, prep.ID).Scan(&holds); err != nil || holds != 0 {
				t.Fatal("abandoned preparation retained holds", err)
			}
			if _, err = sql.Exec(ctx, `UPDATE loomplane_cloud_project_grants SET revoked=false,revision=revision+1 WHERE org_id='org' AND cloud_id='cloud' AND project_id='proj'`); err != nil {
				t.Fatal(err)
			}
			if _, err = sql.Exec(ctx, `UPDATE loomplane_bindings SET external_id='destination' WHERE id='binding'`); err != nil {
				t.Fatal(err)
			}
		})
	}
	input.Hostname = "other.example.com"
	changed, err := svc.PreviewRoute(ctx, p, "proj", input)
	if err != nil {
		t.Fatal(err)
	}

	recoveryInput := input
	recoveryInput.Hostname = "recovery.example.com"
	recoveryPreview, recoveryErr := svc.PreviewRoute(ctx, p, "proj", recoveryInput)
	if recoveryErr != nil {
		t.Fatal(recoveryErr)
	}
	recoveryOp, recoveryErr := svc.ApplyRoute(ctx, p, "proj", recoveryPreview.ID, cloudinfra.ApplyInput{Review: recoveryPreview.Review})
	if recoveryErr != nil {
		t.Fatal(recoveryErr)
	}
	recoveryOp.Status = "failed"
	recoveryOp.DesiredConfig = &server.IngressConfig{OrgID: "org", ServerID: "host2", Version: "lost-save"}
	recoveryOp.PreviousConfig = &server.IngressSnapshot{ContainerID: "proxy", ETag: "before"}
	if err = store.SaveRouteOperation(ctx, recoveryOp); err != nil {
		t.Fatal(err)
	}
	resolver.proof = "lost-control-before-cleanup"
	removal, removeErr := svc.RemoveRoute(ctx, p, "proj", recoveryOp.Route.ID, recoveryOp.Route.Revision)
	if removeErr != nil {
		t.Fatal(removeErr)
	}
	reloaded, reloadErr := store.RouteOperation(ctx, "org", removal.ID)
	if reloadErr != nil {
		t.Fatal(reloadErr)
	}
	if reloaded.CancelledIngress == nil || reloaded.CancelledIngress.Desired.Version != "lost-save" || reloaded.CancelledIngress.Previous.ETag != "before" {
		t.Fatal("cancel lost ingress intent")
	}
	cancelled, cancelErr := store.RouteOperation(ctx, "org", recoveryOp.ID)
	if cancelErr != nil || cancelled.Status != "cancelled" {
		t.Fatal("old operation not cancelled", cancelErr)
	}
	if _, err = sql.Exec(ctx, `UPDATE loomplane_cloud_project_grants SET revoked=true,revision=revision+1 WHERE org_id='org' AND cloud_id='cloud' AND project_id='proj'`); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.ApplyRoute(ctx, p, "proj", changed.ID, cloudinfra.ApplyInput{Review: changed.Review}); err == nil {
		t.Fatal("revoked Cloud grant admitted a route")
	}
}

// The hosted-zone API can report Verified while public DNS is controlled elsewhere.
type controlDNSAdapter struct{ provider.DNSAdapter }

func (*controlDNSAdapter) ListZones(_ context.Context, c provider.InfrastructureCredential) ([]provider.DNSZone, error) {
	return []provider.DNSZone{{ID: "provider-zone", Name: "example.com", AccountID: c.AccountID, Verified: true}}, nil
}
func (*controlDNSAdapter) GetZone(_ context.Context, c provider.InfrastructureCredential, name string) (provider.DNSZone, error) {
	if name != "example.com" {
		return provider.DNSZone{}, provider.ErrInfrastructureNotFound
	}
	return provider.DNSZone{ID: "provider-zone", Name: name, AccountID: c.AccountID, Verified: true}, nil
}
func (*controlDNSAdapter) ListRecords(context.Context, provider.InfrastructureCredential, provider.DNSZone) ([]provider.DNSRecord, error) {
	return nil, nil
}
func TestDomainsManagedHostingRequiresPublicControl(t *testing.T) {
	domainsDurableClaims(t, 60, 60, "managed")
}

// A composed catalog component on a Cloud server takes a route on a port its
// recipe marks routable. Its control ports, engine databases, the composed
// resource itself, its jobs and a dependency-only kind's component never do.
func TestDomainsRouteComposedComponentRoutablePorts(t *testing.T) {
	_, sql, db := cloudSyncDatabase(t)
	ctx := t.Context()
	store := cloudinfra.NewPgStore(db)
	cs := connections.NewPgStore(db)
	now := time.Now().UTC()
	conn := &connections.Connection{ID: "runtime", OrgID: "org", Cloud: "server", Name: "Runtime", Status: "ready", Details: map[string]string{"host": "192.0.2.42", "docker_id": "daemon"}, CreatedAt: now, UpdatedAt: now}
	if err := cs.CreateConnection(ctx, conn); err != nil {
		t.Fatal(err)
	}
	host := &cloudinfra.Server{ID: "host", OrgID: "org", CloudID: "cloud", Name: "Host", Ownership: "adopted", Provider: "server", AccountID: "daemon", ResourceID: "daemon", ConnectionID: "runtime", RuntimeConnectionID: "runtime", Revision: 1, Status: "ready"}
	if err := store.SaveServer(ctx, host); err != nil {
		t.Fatal(err)
	}
	placement := `"cloud_placement":{"cloud_id":"cloud","target_id":"target","server_id":"host"}`
	for _, statement := range []string{
		`CREATE TABLE cp_instances(id TEXT PRIMARY KEY,tenant_id TEXT,current_release TEXT,service_refs JSONB,services JSONB,labels JSONB,state TEXT)`,
		`CREATE TABLE cp_deployments(id TEXT PRIMARY KEY,tenant_id TEXT,instance_id TEXT,release_id TEXT,state TEXT,created_at TIMESTAMPTZ DEFAULT now())`,
		`UPDATE loomplane_cloud_targets SET provider='server',connection_id='runtime',revision=revision+1 WHERE id='target'`,
		`INSERT INTO cp_instances VALUES('api','org',NULL,'{"main":"api-container"}','[{"name":"main","ports":[{"container":8080},{"container":9090}]}]','{"loomplane.revision":"deployment/api"}','running')`,
		`INSERT INTO cp_instances VALUES('db','org',NULL,'{"main":"db-container"}','[{"name":"main","ports":[{"container":5432}]}]','{"loomplane.revision":"deployment/db"}','running')`,
		`INSERT INTO loomplane_bindings(id,org_id,project_id,environment_id,subject,name,provider,engine,external_id,connection_id,config,created_at,updated_at) VALUES
		('api','org','proj','env','resource','app--api','server','resource','api','runtime','{"provider":"server","kind":"sample","component":{"kind":"sample","resource":"app","component":{"name":"api","role":"process"}},"routable_ports":[8080],` + placement + `}',now(),now()),
		('parent','org','proj','env','resource','app','server','resource','api','runtime','{"provider":"server","kind":"sample","composition":{"kind":"sample","version":"1","revision":"r"},"routable_ports":[8080],` + placement + `}',now(),now()),
		('migrate','org','proj','env','resource','app--migrate','server','resource','api','runtime','{"provider":"server","kind":"sample","component":{"kind":"sample","resource":"app","component":{"name":"migrate","role":"job"}},"routable_ports":[8080],` + placement + `}',now(),now()),
		('db','org','proj','env','resource','db','server','postgres','db','runtime','{"provider":"server","kind":"postgres","catalog":{"id":"postgres-17"},"routable_ports":[5432],` + placement + `}',now(),now()),
		('graph','org','proj','env','resource','app--graph','server','resource','api','runtime','{"provider":"server","kind":"graph","component":{"kind":"graph","resource":"app--graph","component":{"name":"server","role":"process"}},"routable_ports":[8080],` + placement + `}',now(),now())`,
	} {
		if _, err := sql.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	input := func(name string, port int) cloudinfra.RouteInput {
		return cloudinfra.RouteInput{CloudID: "cloud", EnvironmentID: "env", Service: name, Port: port}
	}
	binding, err := store.ResolveRouteBinding(ctx, "org", "proj", input("app--api", 8080))
	if err != nil || binding.BindingID != "api" || binding.ServerID != "host" || binding.ContainerID != "api-container" || binding.ReleaseID != "deployment/api" {
		t.Fatalf("routable component port: %+v %v", binding, err)
	}
	for _, refused := range []struct {
		why string
		in  cloudinfra.RouteInput
	}{
		{"control port", input("app--api", 9090)},
		{"composed parent", input("app", 8080)},
		{"job component", input("app--migrate", 8080)},
		{"engine database", input("db", 5432)},
		{"dependency-only component", input("app--graph", 8080)},
	} {
		if got, refuseErr := store.ResolveRouteBinding(ctx, "org", "proj", refused.in); !errors.Is(refuseErr, cloudinfra.ErrNotFound) {
			t.Errorf("%s resolved: %+v %v", refused.why, got, refuseErr)
		}
	}
}
