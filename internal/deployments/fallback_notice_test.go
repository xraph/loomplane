package deployments

import (
	"context"
	"strings"
	"testing"
	"time"

	groved "github.com/xraph/grove/driver"
)

var _ FallbackNotifier = (*FallbackNotice)(nil)

type adminRows struct {
	users []string
	at    int
}

func (r *adminRows) Next() bool                 { r.at++; return r.at <= len(r.users) }
func (r *adminRows) Scan(dest ...any) error     { *(dest[0].(*string)) = r.users[r.at-1]; return nil }
func (r *adminRows) Columns() ([]string, error) { return []string{"user_id"}, nil }
func (r *adminRows) Close() error               { return nil }
func (r *adminRows) Err() error                 { return nil }

// noticeDB answers the admins query with one admin and records the insert.
type noticeDB struct {
	insert string
	args   []any
}

func (d *noticeDB) Exec(_ context.Context, q string, args ...any) (groved.Result, error) {
	d.insert, d.args = q, args
	return nil, nil
}

func (d *noticeDB) Query(context.Context, string, ...any) (groved.Rows, error) {
	return &adminRows{users: []string{"user_admin"}}, nil
}

func TestFallbackNoticeShape(t *testing.T) {
	db := &noticeDB{}
	created := time.Unix(1_700_000_000, 0)
	pe := &ProviderEnvironment{OrgID: "org_1", ProjectID: "proj_1", EnvironmentID: "env_1", Provider: "vercel", Scope: "api", Reason: "Vercel needs a Pro plan", CreatedAt: created}
	if err := (&FallbackNotice{DB: db}).NotifyFallback(t.Context(), pe, "staging"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(db.insert, "loomplane_notification_outbox") {
		t.Fatalf("nothing was queued: %q", db.insert)
	}
	// Enqueue's args: org, project, payload, kind, subject, ids, users, pushOnly, delay.
	if db.args[0] != "org_1" || db.args[1] != "proj_1" || db.args[3] != KindEnvironmentFallback {
		t.Fatalf("org, project or kind wrong: %v", db.args[:4])
	}
	if db.args[4] != "org_1:env_1:vercel:api:1700000000" {
		t.Fatalf("subject = %v", db.args[4])
	}
	if users := db.args[6].([]string); len(users) != 1 || users[0] != "user_admin" {
		t.Fatalf("recipients = %v", users)
	}
	payload := db.args[2].(string)
	for _, want := range []string{`"path":"/projects/proj_1"`, "staging deploys to a separate Vercel project", "Vercel needs a Pro plan"} {
		if !strings.Contains(payload, want) {
			t.Errorf("payload lacks %q: %s", want, payload)
		}
	}
}
