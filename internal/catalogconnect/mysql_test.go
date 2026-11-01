package catalogconnect

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/recipes"
)

func TestMySQLCreateKeepsAccessInVault(t *testing.T) {
	secrets := memSecrets{}
	svc := NewService(newMemStore(), secrets, roleAuthz{})
	dsn := "mysql://app:SECRET@db.example:3307/main?sslmode=verify-full"
	got, err := svc.Create(t.Context(), admin, CreateInput{Name: "mysql", Engine: EngineMySQL, DSN: dsn})
	if err != nil {
		t.Fatal(err)
	}
	if got.Host != "db.example" || got.Port != 3307 || got.Database != "main" || got.Username != "app" || got.TLS != TLSVerifyFull {
		t.Fatalf("%+v", got)
	}
	body, _ := json.Marshal(got)
	if strings.Contains(string(body), "SECRET") || string(secrets[DSNKey(admin.OrgID, got.ID)]) != dsn {
		t.Fatalf("metadata or vault mismatch: %s", body)
	}
}
func TestMySQLDSNRejectsUnsafeOptionsWithoutEchoingSecrets(t *testing.T) {
	for _, dsn := range []string{"mysql://app:SECRET@db/main?sslmode=disable", "mysql://app:SECRET@db/main?sslmode=preferred", "mysql://app:SECRET@db/main?sslmode=verify-full&tls=skip-verify", "mysql://app:SECRET@db/main?sslmode=verify-full&allowAllFiles=true", "mysql://app:SECRET@db/main?sslmode=verify-full&sslmode=require", "mysql://app:SECRET@db/main?sslmode=verify-full&connect_timeout=9999", "mysql://app:SECRET@db/main?sslmode=verify-full#SECRET", "mysql://app:SECRET@db/main?sslmode=verify-full&%ZZ=SECRET", "mysql://app:SECRET@db/main?SECRET=x"} {
		t.Run(dsn, func(t *testing.T) {
			e := &Endpoint{Engine: EngineMySQL}
			err := parseDSN(e, dsn)
			if !errors.Is(err, ErrInvalid) || strings.Contains(err.Error(), "SECRET") {
				t.Fatalf("%v", err)
			}
		})
	}
}
func TestMySQLProberUsesOnlyTargetDialerAndRedactsFailure(t *testing.T) {
	calls := 0
	dial := func(_ context.Context, network, address string) (net.Conn, error) {
		calls++
		if network != "tcp" || address != "db.example:3306" {
			t.Errorf("wrong dial %s %s", network, address)
		}
		return nil, errors.New("SECRET mysql://app:SECRET@db.example/main?sslmode=require")
	}
	checker := &Checker{Probers: map[string]Prober{EngineMySQL: MySQLProber{}}}
	got, err := checker.CheckDSN(t.Context(), "mysql://app:SECRET@db.example/main?sslmode=require", recipes.Requirement{Engine: EngineMySQL}, dial)
	if err != nil || calls < 1 || len(got.Problems) == 0 || strings.Contains(strings.Join(got.Problems, " "), "SECRET") {
		t.Fatalf("%+v %v calls=%d", got, err, calls)
	}
}
