//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/xraph/grove/drivers/pgdriver"
	"github.com/xraph/grove/migrate"

	"github.com/xraph/loomplane/database/migrations"
	"github.com/xraph/loomplane/internal/clouds/keyless"
	"github.com/xraph/loomplane/internal/issuer"
	"github.com/xraph/loomplane/internal/ops"
)

// issuerSecrets stands in for Vault, shared by every replica in a test.
type issuerSecrets struct {
	mu sync.Mutex
	m  map[string][]byte
}

func (s *issuerSecrets) Put(_ context.Context, orgID, id string, v []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m == nil {
		s.m = map[string][]byte{}
	}
	s.m[orgID+"/"+id] = slices.Clone(v)
	return nil
}

func (s *issuerSecrets) Reveal(_ context.Context, orgID, id string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.m[orgID+"/"+id]
	if !ok {
		return nil, errors.New("secret not found")
	}
	return slices.Clone(v), nil
}

func (s *issuerSecrets) Delete(_ context.Context, orgID, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, orgID+"/"+id)
	return nil
}

func (s *issuerSecrets) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.m)
}

func openPg(t *testing.T, dsn string) *pgdriver.PgDB {
	t.Helper()
	pg := pgdriver.New()
	if err := pg.Open(context.Background(), dsn); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pg.Close() })
	return pg
}

// Replicas that start together and each find no signing key take turns
// under the advisory lock, so the issuer ends up with one key, not two.
func TestKeysAreCreatedOnceAcrossReplicas(t *testing.T) {
	dsn := freshDatabase(t)
	ctx := context.Background()
	if err := ops.Migrate(ctx, dsn, migrations.All()); err != nil {
		t.Fatal(err)
	}
	secrets := &issuerSecrets{}
	start := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	var clockMu sync.Mutex
	now := start
	clock := func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return now
	}
	replicas := []*issuer.Service{
		issuer.New("https://loomplane.example.com", "production", issuer.NewPgKeyStore(openPg(t, dsn)), secrets, clock),
		issuer.New("https://loomplane.example.com", "production", issuer.NewPgKeyStore(openPg(t, dsn)), secrets, clock),
	}

	var wg sync.WaitGroup
	ready := make(chan struct{})
	errs := make([]error, len(replicas))
	for i, s := range replicas {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-ready
			errs[i] = s.Rotate(ctx)
		}()
	}
	close(ready)
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		t.Fatal(err)
	}

	store := issuer.NewPgKeyStore(openPg(t, dsn))
	keys, err := store.Keys(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 {
		t.Fatalf("%d key rows, want 1", len(keys))
	}
	if secrets.count() != 2 {
		t.Fatalf("Vault holds %d secrets, want one private key and the ticket key", secrets.count())
	}
	if keys[0].RetiredAt != nil || !keys[0].CreatedAt.Equal(start) {
		t.Fatalf("key = %+v", keys[0])
	}

	// Either replica's ticket works with the other, and both sign with
	// the one key.
	subject := keyless.Subject("org_1", "conn_1")
	ticket, err := replicas[0].Ticket(ctx, subject, "sts.amazonaws.com")
	if err != nil {
		t.Fatal(err)
	}
	token, err := replicas[1].Mint(ctx, keyless.Handout{Type: keyless.Type, Subject: subject, Audience: "sts.amazonaws.com", Ticket: ticket})
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	if token == "" {
		t.Fatal("Mint returned no token")
	}
	var public struct{ KID, Kty string }
	if err = json.Unmarshal(keys[0].Public, &public); err != nil || public.KID != keys[0].KID || public.Kty != "RSA" {
		t.Fatalf("public JWK %s doesn't name its kid", keys[0].Public)
	}

	// A month on, one replica makes the successor, which is published
	// but doesn't sign yet. A day later, after the other replica has
	// rotated, the successor signs and the old key is retired, not gone.
	// A day after that, the old key is deleted.
	retired := func(want int, when string) {
		t.Helper()
		got, kerr := store.Keys(ctx)
		if kerr != nil {
			t.Fatal(kerr)
		}
		n := 0
		for _, k := range got {
			if k.RetiredAt != nil {
				n++
			}
		}
		if len(got) != 2 || n != want {
			t.Fatalf("%s: %d key rows with %d retired, want 2 with %d", when, len(got), n, want)
		}
	}
	advance := func(d time.Duration) {
		clockMu.Lock()
		now = now.Add(d)
		clockMu.Unlock()
	}
	advance(31 * 24 * time.Hour)
	if err = replicas[0].Rotate(ctx); err != nil {
		t.Fatal(err)
	}
	retired(0, "with a successor published")
	advance(25 * time.Hour)
	if err = replicas[1].Rotate(ctx); err != nil {
		t.Fatal(err)
	}
	retired(1, "after the switch")
	advance(25 * time.Hour)
	if err = replicas[0].Rotate(ctx); err != nil {
		t.Fatal(err)
	}
	if keys, err = store.Keys(ctx); err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 || keys[0].RetiredAt != nil {
		t.Fatalf("a day after the switch: keys = %+v, want the one current key", keys)
	}
}

// Keyless adds the issuer's public keys, connection ID reservations, and
// connection.keyless_refused for every webhook that hears a lapse.
func TestKeylessSchema(t *testing.T) {
	dsn := freshDatabase(t)
	ctx := context.Background()
	if err := ops.Migrate(ctx, dsn, migrations.All()); err != nil {
		t.Fatal(err)
	}
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	now := time.Now().UTC()

	insertKey := `INSERT INTO loomplane_oidc_keys (kid, public_jwk, created_at) VALUES ('kid_1', '{"kty":"RSA"}', $1)`
	if _, err = conn.Exec(ctx, insertKey, now); err != nil {
		t.Fatal(err)
	}
	var retiredAt *time.Time
	var jwk string
	if err = conn.QueryRow(ctx, `SELECT retired_at, public_jwk->>'kty' FROM loomplane_oidc_keys WHERE kid = 'kid_1'`).Scan(&retiredAt, &jwk); err != nil {
		t.Fatal(err)
	}
	if retiredAt != nil || jwk != "RSA" {
		t.Fatalf("retired_at = %v, kty = %q; want null and RSA", retiredAt, jwk)
	}
	_, err = conn.Exec(ctx, insertKey, now)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		t.Fatalf("second key with one kid: err = %v, want a unique violation", err)
	}

	if _, err = conn.Exec(ctx, `INSERT INTO loomplane_keyless_reservations (id, org_id, user_id, cloud, expires_at, created_at)
		VALUES ('conn_1', 'org_1', 'usr_1', 'aws', $1, $2)`, now.Add(24*time.Hour), now); err != nil {
		t.Fatal(err)
	}
	var indexed bool
	if err = conn.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_indexes
		WHERE tablename = 'loomplane_keyless_reservations' AND indexdef LIKE '%(expires_at)%')`).Scan(&indexed); err != nil {
		t.Fatal(err)
	}
	if !indexed {
		t.Fatal("loomplane_keyless_reservations has no index on expires_at")
	}

	var keylessMigration *migrate.Migration
	for _, m := range migrations.Group().Migrations() {
		if m.Name == "keyless" {
			keylessMigration = m
		}
	}
	if keylessMigration == nil || keylessMigration.Version != "20261117000100" {
		t.Fatalf("keyless migration: %+v", keylessMigration)
	}
	if _, err = conn.Exec(ctx, `INSERT INTO relay_endpoints (id, tenant_id, url, event_types) VALUES
		('ep_old', 'org_1', 'https://example.com/a', ARRAY['deployment.succeeded','deployment.failed','connection.sign_in_needed']),
		('ep_new', 'org_1', 'https://example.com/b', ARRAY['deployment.succeeded','connection.sign_in_needed','connection.keyless_refused']),
		('ep_other', 'org_1', 'https://example.com/c', ARRAY['deployment.succeeded','deployment.failed'])`); err != nil {
		t.Fatal(err)
	}
	pg := openPg(t, dsn)
	exec, err := migrate.NewExecutorFor(pg)
	if err != nil {
		t.Fatal(err)
	}
	hears := func(want map[string][]string) {
		t.Helper()
		for id, types := range want {
			var got []string
			if qerr := conn.QueryRow(ctx, `SELECT event_types FROM relay_endpoints WHERE id = $1`, id).Scan(&got); qerr != nil {
				t.Fatal(qerr)
			}
			if strings.Join(got, ",") != strings.Join(types, ",") {
				t.Fatalf("%s hears %v, want %v", id, got, types)
			}
		}
	}
	for range 2 {
		if err = keylessMigration.Up(ctx, exec); err != nil {
			t.Fatal(err)
		}
	}
	hears(map[string][]string{
		"ep_old":   {"deployment.succeeded", "deployment.failed", "connection.sign_in_needed", "connection.keyless_refused"},
		"ep_new":   {"deployment.succeeded", "connection.sign_in_needed", "connection.keyless_refused"},
		"ep_other": {"deployment.succeeded", "deployment.failed"},
	})

	if err = keylessMigration.Down(ctx, exec); err != nil {
		t.Fatal(err)
	}
	hears(map[string][]string{
		"ep_old":   {"deployment.succeeded", "deployment.failed", "connection.sign_in_needed"},
		"ep_new":   {"deployment.succeeded", "connection.sign_in_needed"},
		"ep_other": {"deployment.succeeded", "deployment.failed"},
	})
	for _, table := range []string{"loomplane_oidc_keys", "loomplane_keyless_reservations"} {
		var exists bool
		if err = conn.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, table).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if exists {
			t.Fatalf("down left %s behind", table)
		}
	}
}
