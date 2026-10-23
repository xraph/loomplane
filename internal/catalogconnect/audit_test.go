package catalogconnect

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// recordedAudit keeps what's audited.
type recordedAudit struct{ events []AuditEvent }

func (r *recordedAudit) Record(_ context.Context, e AuditEvent) { r.events = append(r.events, e) }

// last is the last event, checked never to hold a DSN's secrets.
func (r *recordedAudit) last(t *testing.T) AuditEvent {
	t.Helper()
	if len(r.events) == 0 {
		t.Fatal("nothing was audited")
	}
	e := r.events[len(r.events)-1]
	for _, secret := range []string{"hunter2", "sslmode", "u:", "@"} {
		if text := fmt.Sprintf("%+v", e); strings.Contains(text, secret) {
			t.Fatalf("the audit event shows %q: %s", secret, text)
		}
	}
	return e
}

func TestEndpointChangesAreAudited(t *testing.T) {
	store, audit := newMemStore(), &recordedAudit{}
	svc := NewService(store, memSecrets{}, roleAuthz{})
	svc.Audit = audit
	const dsn = "postgres://u:hunter2@db.example:6543/app?sslmode=require"

	ep, err := svc.Create(t.Context(), admin, CreateInput{Name: "prod-pg", Engine: EnginePostgres, DSN: dsn})
	if err != nil {
		t.Fatal(err)
	}
	want := AuditEvent{Action: AuditCreate, Principal: admin, EndpointID: ep.ID, Engine: EnginePostgres, Address: "db.example:6543", Outcome: OutcomeSuccess}
	if got := audit.last(t); got != want {
		t.Fatalf("create: %+v", got)
	}
	if _, err = svc.Create(t.Context(), admin, CreateInput{Name: "bad", Engine: EnginePostgres, DSN: "postgres://u:hunter2@db.example/app"}); err == nil {
		t.Fatal("created without sslmode")
	}
	want = AuditEvent{Action: AuditCreate, Principal: admin, Engine: EnginePostgres, Address: "db.example:5432", Outcome: OutcomeInvalid}
	if got := audit.last(t); got != want {
		t.Fatalf("invalid create: %+v", got)
	}
	if _, err = svc.Create(t.Context(), viewer, CreateInput{Name: "x", Engine: EngineRedis, DSN: "redis://:hunter2@cache.example/0"}); err == nil {
		t.Fatal("a viewer created an endpoint")
	}
	want = AuditEvent{Action: AuditCreate, Principal: viewer, Engine: EngineRedis, Address: "cache.example:6379", Outcome: OutcomeDenied}
	if got := audit.last(t); got != want {
		t.Fatalf("denied create: %+v", got)
	}

	store.bindings = []binding{{"org", `{"composition":{"dependencies":{"postgres":{"external":"` + ep.ID + `"}}}}`}}
	if err = svc.Delete(t.Context(), admin, ep.ID); err == nil {
		t.Fatal("deleted an endpoint in use")
	}
	want = AuditEvent{Action: AuditDelete, Principal: admin, EndpointID: ep.ID, Engine: EnginePostgres, Address: "db.example:6543", Outcome: OutcomeFailure}
	if got := audit.last(t); got != want {
		t.Fatalf("refused delete: %+v", got)
	}
	store.bindings = nil
	if err = svc.Delete(t.Context(), admin, ep.ID); err != nil {
		t.Fatal(err)
	}
	want.Outcome = OutcomeSuccess
	if got := audit.last(t); got != want {
		t.Fatalf("delete: %+v", got)
	}

	svc.RecordCheck(t.Context(), admin, CheckInput{Engine: EnginePostgres, DSN: dsn, Target: "conn_cluster", Kind: "sample", Dependency: "postgres"}, OutcomeFailure)
	want = AuditEvent{Action: AuditCheck, Principal: admin, Target: "conn_cluster", Engine: EnginePostgres, Address: "db.example:6543", Outcome: OutcomeFailure}
	if got := audit.last(t); got != want {
		t.Fatalf("check: %+v", got)
	}
	// A DSN that isn't a URL has no address to name.
	svc.RecordCheck(t.Context(), admin, CheckInput{Engine: EnginePostgres, DSN: "host=db u:hunter2@", Target: "conn_cluster"}, OutcomeInvalid)
	if got := audit.last(t); got.Address != "" || got.Outcome != OutcomeInvalid {
		t.Fatalf("unreadable check: %+v", got)
	}
}

func TestAuditIsOptional(t *testing.T) {
	svc := NewService(newMemStore(), memSecrets{}, roleAuthz{})
	if _, err := svc.Create(t.Context(), admin, CreateInput{Name: "prod-pg", Engine: EnginePostgres, DSN: testDSN}); err != nil {
		t.Fatal(err)
	}
	svc.RecordCheck(t.Context(), admin, CheckInput{DSN: testDSN}, OutcomeSuccess)
}

func TestDSNAddressNamesNothingWhenItsUserInfoIsAmbiguous(t *testing.T) {
	for dsn, want := range map[string]string{
		"postgres://u:hunter2@db.example:6543/app?sslmode=require": "db.example:6543",
		"rediss://:hunter2@cache.example/0":                        "cache.example:6379",
		// An unescaped ?, / or # in a password ends the address early:
		// what's before it would read as user:digits for host:port.
		"postgres://admin:9999?x@db.example/app":         "",
		"postgres://admin:9999/x@db.example/app":         "",
		"postgres://admin:9999#x@db.example/app":         "",
		"postgres://admin:pa/ss@db.example/app":          "",
		"postgres://u:p@ss/word@db.example/app":          "",
		"postgres://u:hunter2@db.example/app?x=a@b":      "",
		"postgres://u:hunter2@db.example/app#a@b":        "",
		"postgres://db.example/app?sslmode=disable":      "db.example:5432",
		"host=db.example user=u password=hunter2 dbname": "",
	} {
		if got := dsnAddress(dsn); got != want {
			t.Fatalf("%s: got %q, want %q", dsn, got, want)
		}
	}
}
