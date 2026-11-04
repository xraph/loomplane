package currentauthority

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/xraph/grove/driver"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/xraph/loomplane/internal/security"
)

func testBinding() Binding {
	return Binding{Actor: security.Principal{UserID: "user-1", OrgID: "org-1", KeyID: "key-1"}, AppID: "app-1", Operation: "snapshot.issue", InputDigest: strings.Repeat("a", 64), RecordID: "candidate-1", AttemptID: "attempt-1"}
}

func TestUnavailableByDefault(t *testing.T) {
	for _, c := range []*Coordinator{nil, {}} {
		err := c.Run(context.Background(), testBinding(), func(context.Context, *Attempt) error { t.Fatal("unavailable callback ran"); return nil })
		if !errors.Is(err, ErrUnavailable) {
			t.Fatalf("Run = %v", err)
		}
	}
}

func TestCanonicalRejectsUnsupportedPrincipalFields(t *testing.T) {
	mutations := map[string]func(*Binding){
		"read only":       func(b *Binding) { b.Actor.ReadOnly = true },
		"kind":            func(b *Binding) { b.Actor.Kind = security.KindAgent },
		"fleet pointer":   func(b *Binding) { b.Actor.Fleet = &security.FleetScope{FleetID: "f"} },
		"delegation":      func(b *Binding) { b.Actor.OnBehalfOf = &security.Principal{} },
		"reserved system": func(b *Binding) { b.Actor.UserID = "loomplane:" },
		"reserved agent":  func(b *Binding) { b.Actor.UserID = "agent:" },
		"reserved fleet":  func(b *Binding) { b.Actor.UserID = "fleet-key:k" },
		"digest":          func(b *Binding) { b.InputDigest = strings.Repeat("A", 64) },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			b := testBinding()
			mutate(&b)
			if _, err := canonical(b); !errors.Is(err, ErrBinding) {
				t.Fatalf("canonical = %v", err)
			}
		})
	}
	b := testBinding()
	original, err := canonical(b)
	if err != nil {
		t.Fatal(err)
	}
	b.Actor.KeyID = "other"
	updated, _ := canonical(b)
	if original == updated {
		t.Fatal("key identity omitted")
	}
}

func TestFenceInventoryAndSchemaQuoting(t *testing.T) {
	if len(authorityTables) != 13 {
		t.Fatalf("tables=%d", len(authorityTables))
	}
	sql := fenceSQL(`quoted"schema`)
	for _, table := range authorityTables {
		if !strings.Contains(sql, `"quoted""schema"."`+table+`"`) {
			t.Fatalf("unqualified table %s", table)
		}
	}
	if strings.Contains(sql, "check_logs") || strings.Contains(sql, "role_permission_grants") {
		t.Fatal(sql)
	}
}

func TestPoolConfigurationPreservesPrimaryOptions(t *testing.T) {
	primary, err := pgxpool.ParseConfig("postgres://user:secret@localhost:55471/db?sslmode=prefer&search_path=custom&application_name=authority&pool_max_conns=7&pool_min_conns=3&pool_min_idle_conns=2")
	if err != nil {
		t.Fatal(err)
	}
	{
		cfg := writerConfig(primary)
		if cfg.MaxConns != 1 || cfg.MinConns != 0 || cfg.MinIdleConns != 0 {
			t.Fatal("unbounded pool")
		}
		if cfg.ConnConfig.User != primary.ConnConfig.User || cfg.ConnConfig.Password != primary.ConnConfig.Password || cfg.ConnConfig.Database != primary.ConnConfig.Database || cfg.ConnConfig.RuntimeParams["search_path"] != "custom" || cfg.ConnConfig.RuntimeParams["application_name"] != "authority" || len(cfg.ConnConfig.Fallbacks) != len(primary.ConnConfig.Fallbacks) {
			t.Fatal("primary configuration changed")
		}
		if (cfg.ConnConfig.TLSConfig == nil) != (primary.ConnConfig.TLSConfig == nil) {
			t.Fatal("TLS configuration lost")
		}
	}
	if primary.MaxConns != 7 || primary.MinConns != 3 || primary.ConnConfig.RuntimeParams["statement_timeout"] != "" {
		t.Fatal("mutated primary")
	}
}

func TestClockStepAndReversal(t *testing.T) {
	start := time.Now()
	if clockChanged(start, start.Add(time.Millisecond)) {
		t.Fatal("stable interval rejected")
	}
	if !clockChanged(start, start.Add(-time.Millisecond)) {
		t.Fatal("reversal accepted")
	}
}

func writeNothing(context.Context, driver.Tx, time.Time) error { return nil }

func TestNativeTelemetryPreservesSQLStateAndConnection(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	defer otel.SetTracerProvider(previous)
	defer func() { _ = provider.Shutdown(context.Background()) }()
	backend, err := openPools(context.Background(), "postgres://user:secret@localhost:55471/authority?sslmode=disable", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer backend.close()
	ctx, root := provider.Tracer("test").Start(context.Background(), "root")
	_, op := backend.telemetry.StartSQL(ctx, "SELECT * FROM warden_roles")
	op.End(&pgconn.PgError{Code: "42501", Message: "test denial"})
	root.End()
	spans := exporter.GetSpans()
	if len(spans) != 2 {
		t.Fatalf("spans=%d", len(spans))
	}
	sql := spans[0]
	if sql.Status.Code != codes.Error {
		t.Fatalf("span status=%v", sql.Status)
	}
	attributes := map[string]string{}
	for _, attr := range sql.Attributes {
		attributes[string(attr.Key)] = attr.Value.String()
	}
	if attributes["db.response.status_code"] != "42501" || attributes["db.namespace"] != "authority" || attributes["server.address"] != "localhost" || attributes["server.port"] != "55471" {
		t.Fatalf("attributes=%v", attributes)
	}
}
