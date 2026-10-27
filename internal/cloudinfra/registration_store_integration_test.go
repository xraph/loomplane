//go:build integration

package cloudinfra

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/drivers/pgdriver"
	"github.com/xraph/grove/drivers/pgdriver/pgmigrate"

	"github.com/xraph/loomplane/database/migrations"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/provider"
)

func registrationDatabase(t *testing.T) *pgdriver.PgDB {
	t.Helper()
	dsn := os.Getenv("LOOMPLANE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set LOOMPLANE_TEST_DATABASE_URL for the registration store")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("domain_registrations_%d", time.Now().UnixNano())
	if _, err = admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		_ = admin.Close(ctx)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)")
		_ = admin.Close(context.Background())
	})
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	pg := pgdriver.New()
	if err = pg.Open(ctx, u.String()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pg.Close() })
	for _, stmt := range []string{
		`CREATE TABLE loomplane_cloud_connections(id TEXT PRIMARY KEY, org_id TEXT NOT NULL)`,
		`CREATE UNIQUE INDEX loomplane_connections_org_id ON loomplane_cloud_connections(org_id,id)`,
		`CREATE TABLE loomplane_connection_uses(org_id TEXT NOT NULL, connection_id TEXT NOT NULL, kind TEXT NOT NULL, owner_id TEXT NOT NULL,
 PRIMARY KEY(kind,owner_id,connection_id), FOREIGN KEY(org_id,connection_id) REFERENCES loomplane_cloud_connections(org_id,id) ON DELETE RESTRICT)`,
		`INSERT INTO loomplane_cloud_connections VALUES('cf','org'),('cf_other','other')`,
	} {
		if _, err = pg.Exec(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	ran := false
	for _, m := range migrations.Group().Migrations() {
		if m.Name == "domain_registrations" {
			if err = m.Up(ctx, pgmigrate.New(pg)); err != nil {
				t.Fatal(err)
			}
			ran = true
		}
	}
	if !ran {
		t.Fatal("no domain_registrations migration")
	}
	return pg
}

func TestRegistrationStore(t *testing.T) {
	pg := registrationDatabase(t)
	ctx := t.Context()
	st := NewPgStore(pg)
	approver := security.Principal{OrgID: "org", UserID: "admin", KeyID: "key"}
	preview := func(id, org, connection string) *Registration {
		t.Helper()
		reg := &Registration{ID: id, OrgID: org, ConnectionID: connection, Name: "acme.dev", Status: RegistrationPreview, Review: "token", Quote: provider.DomainPrice{Currency: "USD", RegistrationCost: "10.11"}, Revision: 1, Principal: approver}
		if err := st.CreateRegistration(ctx, reg); err != nil {
			t.Fatal(err)
		}
		return reg
	}
	uses := func() []string {
		t.Helper()
		rows, err := pg.Query(ctx, `SELECT owner_id FROM loomplane_connection_uses WHERE kind='domain_registration' ORDER BY owner_id`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		out := []string{}
		for rows.Next() {
			var id string
			if err = rows.Scan(&id); err != nil {
				t.Fatal(err)
			}
			out = append(out, id)
		}
		return out
	}
	first, second := preview("reg_1", "org", "cf"), preview("reg_2", "org", "cf")
	preview("reg_x", "other", "cf_other")
	if err := st.CreateRegistration(ctx, first); !errors.Is(err, ErrConflict) {
		t.Fatal("duplicate id", err)
	}
	got, err := st.Registration(ctx, "org", "reg_1")
	if err != nil || got.Principal != approver || got.Quote.RegistrationCost != "10.11" || got.Review != "token" {
		t.Fatal(got, err)
	}
	if _, err = st.Registration(ctx, "other", "reg_1"); !errors.Is(err, ErrNotFound) {
		t.Fatal("another organization read it", err)
	}

	stale := *got
	got.Status = RegistrationApproved
	if err = st.SaveRegistration(ctx, got); err != nil || got.Revision != 2 {
		t.Fatal(got.Revision, err)
	}
	if err = st.SaveRegistration(ctx, &stale); !errors.Is(err, ErrConflict) || stale.Revision != 1 {
		t.Fatal("stale save", stale.Revision, err)
	}
	if got := uses(); len(got) != 1 || got[0] != "reg_1" {
		t.Fatalf("uses: %v", got)
	}
	// Another organization may register the same name.
	other, _ := st.Registration(ctx, "other", "reg_x")
	other.Status = RegistrationApproved
	if err = st.SaveRegistration(ctx, other); err != nil {
		t.Fatal(err)
	}
	second.Status = RegistrationApproved
	if err = st.SaveRegistration(ctx, second); !errors.Is(err, ErrConflict) || second.Revision != 1 {
		t.Fatal("second active registration of acme.dev", second.Revision, err)
	}

	pending, err := st.PendingRegistrations(ctx)
	if err != nil || len(pending) != 3 {
		t.Fatal(pending, err)
	}
	list, err := st.Registrations(ctx, "org")
	if err != nil || len(list) != 2 || list[0].ID != "reg_2" {
		t.Fatal(list, err)
	}

	got.Status = RegistrationFailedStatus
	if err = st.SaveRegistration(ctx, got); err != nil {
		t.Fatal(err)
	}
	if got := uses(); len(got) != 1 || got[0] != "reg_x" {
		t.Fatalf("a failed registration kept its connection: %v", got)
	}
	if err = st.SaveRegistration(ctx, second); err != nil {
		t.Fatal("the name stayed taken after a failure", err)
	}
	other.Status = RegistrationSucceededStatus
	if err = st.SaveRegistration(ctx, other); err != nil {
		t.Fatal(err)
	}
	if done, listErr := st.SucceededRegistrations(ctx); listErr != nil || len(done) != 1 || done[0].ID != "reg_x" {
		t.Fatal("succeeded", done, listErr)
	}
	// reg_1 failed and reg_x succeeded, neither notified yet; only rows
	// saved since the cutoff count.
	got.UpdatedAt, other.UpdatedAt = time.Now().Add(-8*24*time.Hour), time.Now()
	for _, reg := range []*Registration{got, other} {
		if err = st.SaveRegistration(ctx, reg); err != nil {
			t.Fatal(err)
		}
	}
	unnotified, err := st.UnnotifiedRegistrations(ctx, time.Now().Add(-7*24*time.Hour))
	if err != nil || len(unnotified) != 1 || unnotified[0].ID != "reg_x" {
		t.Fatal("unnotified", unnotified, err)
	}
	other.ResultNotifiedFor = RegistrationSucceededStatus
	if err = st.SaveRegistration(ctx, other); err != nil {
		t.Fatal(err)
	}
	if unnotified, err = st.UnnotifiedRegistrations(ctx, time.Now().Add(-7*24*time.Hour)); err != nil || len(unnotified) != 0 {
		t.Fatal("notified row listed", unnotified, err)
	}
	if _, err = pg.Exec(ctx, `DELETE FROM loomplane_cloud_connections WHERE org_id='org' AND id='cf'`); err == nil {
		t.Fatal("an approved registration lost its connection")
	}
}
